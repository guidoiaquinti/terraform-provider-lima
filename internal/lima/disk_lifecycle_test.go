// Copyright 2026 Guido Iaquinti
// SPDX-License-Identifier: Apache-2.0

package lima_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/guidoiaquinti/terraform-provider-lima/internal/lima"
	"github.com/guidoiaquinti/terraform-provider-lima/internal/testutil"
)

func newDiskService(t *testing.T, fake *testutil.FakeLimactl) *lima.DiskService {
	t.Helper()
	c, err := lima.NewExecClient(lima.Options{Binary: testutil.StubBinary(t), Runner: fake})
	if err != nil {
		t.Fatalf("NewExecClient: %v", err)
	}
	return lima.NewDiskService(c, nil)
}

func TestDiskServiceCreate(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	svc := newDiskService(t, fake)

	disk, err := svc.Create(context.Background(), lima.CreateDiskRequest{
		Name: "data", SizeBytes: 10 << 30,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if disk.Name != "data" || disk.SizeBytes != 10<<30 {
		t.Errorf("created disk = %+v", disk)
	}
	if disk.MountPoint != "/mnt/lima-data" {
		t.Errorf("mount point = %q, want Lima's default", disk.MountPoint)
	}
	// Lima reports raw whatever was requested; the provider must not fight it.
	if disk.Format != "raw" {
		t.Errorf("format = %q, want the value Lima reports", disk.Format)
	}
}

func TestDiskServiceCreateRejectsDuplicate(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	fake.SeedDisk(testutil.FakeDisk{Name: "data", Size: 1 << 30})
	svc := newDiskService(t, fake)

	_, err := svc.Create(context.Background(), lima.CreateDiskRequest{Name: "data", SizeBytes: 1 << 30})
	if !errors.Is(err, lima.ErrDiskExists) {
		t.Fatalf("Create error = %v, want ErrDiskExists", err)
	}
	// Caught before invoking Lima, so the caller can suggest an import.
	if n := len(fake.CallsFor("disk")); n != 1 {
		t.Errorf("disk commands invoked %d times, want only the existence check", n)
	}
}

func TestDiskServiceResize(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		current    int64
		want       int64
		attachedTo string
		wantErr    string
		wantResize bool
	}{
		{name: "grow", current: 1 << 30, want: 2 << 30, wantResize: true},
		{
			// Re-applying an unchanged configuration must cost nothing.
			name: "same size is a no-op", current: 2 << 30, want: 2 << 30,
		},
		{
			// Refused before Lima is asked, so the message can name both sizes.
			name: "shrink is refused", current: 2 << 30, want: 1 << 30,
			wantErr: "cannot shrink",
		},
		{
			name: "in-use disk cannot be resized", current: 1 << 30, want: 2 << 30,
			attachedTo: "dev", wantErr: "in use",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fake := testutil.NewFakeLimactl()
			fake.SeedDisk(testutil.FakeDisk{Name: "data", Size: tc.current})
			if tc.attachedTo != "" {
				// The holder has to exist and be running for Lima to report the
				// disk as in use: the lock belongs to a live VM, not to the
				// attachment. Seeding only the attachment described a state
				// Lima cannot be in.
				fake.Seed(testutil.FakeInstance{Name: tc.attachedTo, Status: "Running"})
				fake.AttachDisk("data", tc.attachedTo)
			}
			svc := newDiskService(t, fake)

			err := svc.Resize(context.Background(), "data", tc.want)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("Resize succeeded, want an error containing %q", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("error = %q, want it to mention %q", err, tc.wantErr)
				}
				// Nothing may have changed.
				if d, _ := fake.GetDisk("data"); d.Size != tc.current {
					t.Errorf("size = %d, want it unchanged at %d", d.Size, tc.current)
				}
				return
			}
			if err != nil {
				t.Fatalf("Resize: %v", err)
			}

			d, _ := fake.GetDisk("data")
			if d.Size != tc.want {
				t.Errorf("size = %d, want %d", d.Size, tc.want)
			}

			resized := 0
			for _, c := range fake.Calls() {
				if len(c.Args) > 1 && c.Args[0] == "disk" && c.Args[1] == "resize" {
					resized++
				}
			}
			if tc.wantResize != (resized > 0) {
				t.Errorf("resize invoked %d times, wantResize %v", resized, tc.wantResize)
			}
		})
	}
}

func TestDiskServiceResizeInUseNamesTheInstance(t *testing.T) {
	t.Parallel()

	// The diagnostic has to say which VM to stop, or the user has to go
	// hunting for it.
	fake := testutil.NewFakeLimactl()
	fake.SeedDisk(testutil.FakeDisk{Name: "data", Size: 1 << 30})
	fake.Seed(testutil.FakeInstance{Name: "project-dev", Status: "Running"})
	fake.AttachDisk("data", "project-dev")
	svc := newDiskService(t, fake)

	err := svc.Resize(context.Background(), "data", 2<<30)
	if !errors.Is(err, lima.ErrDiskInUse) {
		t.Fatalf("error = %v, want ErrDiskInUse", err)
	}
	if !strings.Contains(err.Error(), "project-dev") {
		t.Errorf("error %q does not name the holding instance", err)
	}
}

func TestDiskServiceDelete(t *testing.T) {
	t.Parallel()

	t.Run("free disk is deleted", func(t *testing.T) {
		t.Parallel()
		fake := testutil.NewFakeLimactl()
		fake.SeedDisk(testutil.FakeDisk{Name: "data", Size: 1 << 30})
		svc := newDiskService(t, fake)

		if err := svc.Delete(context.Background(), "data"); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if _, ok := fake.GetDisk("data"); ok {
			t.Error("disk survived Delete")
		}
	})

	t.Run("absent disk is success", func(t *testing.T) {
		t.Parallel()
		fake := testutil.NewFakeLimactl()
		svc := newDiskService(t, fake)
		if err := svc.Delete(context.Background(), "nope"); err != nil {
			t.Errorf("deleting an absent disk returned error: %v", err)
		}
	})

	t.Run("in-use disk is refused", func(t *testing.T) {
		t.Parallel()
		fake := testutil.NewFakeLimactl()
		fake.SeedDisk(testutil.FakeDisk{Name: "data", Size: 1 << 30})
		fake.Seed(testutil.FakeInstance{Name: "dev", Status: "Running"})
		fake.AttachDisk("data", "dev")
		svc := newDiskService(t, fake)

		err := svc.Delete(context.Background(), "data")
		if !errors.Is(err, lima.ErrDiskInUse) {
			t.Fatalf("error = %v, want ErrDiskInUse", err)
		}
		if _, ok := fake.GetDisk("data"); !ok {
			t.Error("an in-use disk was deleted anyway")
		}
		// The provider must never reach for `limactl disk unlock`: it cannot
		// tell a stale lock from a live one.
		for _, c := range fake.Calls() {
			for _, a := range c.Args {
				if a == "unlock" {
					t.Error("the provider invoked disk unlock")
				}
			}
		}
	})
}

func TestDiskServiceGetAndExists(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	fake.SeedDisk(testutil.FakeDisk{Name: "data", Size: 1 << 30})
	svc := newDiskService(t, fake)
	ctx := context.Background()

	if ok, err := svc.Exists(ctx, "data"); err != nil || !ok {
		t.Errorf("Exists(data) = %v, %v; want true, nil", ok, err)
	}
	if ok, err := svc.Exists(ctx, "nope"); err != nil || ok {
		t.Errorf("Exists(nope) = %v, %v; want false, nil", ok, err)
	}

	if _, err := svc.Get(ctx, "nope"); !lima.IsDiskNotFound(err) {
		t.Errorf("Get error = %v, want a not-found error", err)
	}
}

func TestDiskServiceListIsSorted(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	fake.SeedDisk(testutil.FakeDisk{Name: "zeta", Size: 1 << 30})
	fake.SeedDisk(testutil.FakeDisk{Name: "alpha", Size: 1 << 30})
	svc := newDiskService(t, fake)

	disks, err := svc.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(disks) != 2 || disks[0].Name != "alpha" {
		t.Errorf("List = %+v, want a deterministic order", disks)
	}
}
