package lima

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseDisks(t *testing.T) {
	t.Parallel()

	// Captured verbatim from Lima 2.2.0: NDJSON, one object per line, with no
	// enclosing array. See docs/development/lima-cli-contract.md §13.1.
	const output = `{"name":"data","size":1073741824,"format":"raw","dir":"/private/tmp/ltfdisk/_disks/data","instance":"","instanceDir":"","mountPoint":"/mnt/lima-data"}
{"name":"scratch","size":10737418240,"format":"raw","dir":"/private/tmp/ltfdisk/_disks/scratch","instance":"dev","instanceDir":"/private/tmp/ltfdisk/dev","mountPoint":"/mnt/lima-scratch"}
`

	got, err := ParseDisksString(output)
	if err != nil {
		t.Fatalf("ParseDisks returned error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d disks, want 2", len(got))
	}

	if got[0].Name != "data" || got[0].SizeBytes != 1073741824 {
		t.Errorf("disk 0 = %+v", got[0])
	}
	if got[0].InUse() {
		t.Error("a disk with an empty instance must not report as in use")
	}
	if got[0].MountPoint != "/mnt/lima-data" {
		t.Errorf("mount point = %q", got[0].MountPoint)
	}

	if !got[1].InUse() || got[1].Instance != "dev" {
		t.Errorf("disk 1 should be in use by dev: %+v", got[1])
	}
}

func TestParseDisksEdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		input     string
		wantCount int
		wantErr   bool
	}{
		{name: "no disks", input: "", wantCount: 0},
		{name: "whitespace only", input: "\n\n", wantCount: 0},
		{
			// Forward compatibility, same as the instance parser.
			name:      "unknown fields are tolerated",
			input:     `{"name":"a","size":1,"brandNew":{"x":1}}`,
			wantCount: 1,
		},
		{name: "malformed", input: `{"name":`, wantErr: true},
		{name: "entry without a name", input: `{"size":1}`, wantErr: true},
		{name: "wrong type for size", input: `{"name":"a","size":"big"}`, wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseDisksString(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseDisks(%q) = %v, want error", tc.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseDisks(%q) returned error: %v", tc.input, err)
			}
			if len(got) != tc.wantCount {
				t.Errorf("got %d disks, want %d", len(got), tc.wantCount)
			}
		})
	}
}

func TestDiskCommandArguments(t *testing.T) {
	t.Parallel()

	t.Run("list", func(t *testing.T) {
		t.Parallel()
		want := []string{"disk", "list", "--json"}
		if got := diskListArgs(); !reflect.DeepEqual(got, want) {
			t.Errorf("diskListArgs = %v, want %v", got, want)
		}
	})

	t.Run("resize", func(t *testing.T) {
		t.Parallel()
		want := []string{"disk", "resize", "data", "--size", "2GiB"}
		if got := diskResizeArgs("data", 2<<30); !reflect.DeepEqual(got, want) {
			t.Errorf("diskResizeArgs = %v, want %v", got, want)
		}
	})

	t.Run("delete does not force", func(t *testing.T) {
		t.Parallel()
		// --force would override a lock the provider cannot prove is stale.
		want := []string{"disk", "delete", "data"}
		if got := diskDeleteArgs("data"); !reflect.DeepEqual(got, want) {
			t.Errorf("diskDeleteArgs = %v, want %v", got, want)
		}
	})

	createTests := []struct {
		name    string
		req     CreateDiskRequest
		want    []string
		wantErr bool
	}{
		{
			name: "size only",
			req:  CreateDiskRequest{Name: "data", SizeBytes: 10 << 30},
			want: []string{"disk", "create", "data", "--size", "10GiB"},
		},
		{
			name: "with format",
			req:  CreateDiskRequest{Name: "data", SizeBytes: 1 << 30, Format: "qcow2"},
			want: []string{"disk", "create", "data", "--size", "1GiB", "--format", "qcow2"},
		},
		{
			// Sizes are emitted in the most compact exact IEC form.
			name: "non-GiB size",
			req:  CreateDiskRequest{Name: "data", SizeBytes: 1500 << 20},
			want: []string{"disk", "create", "data", "--size", "1500MiB"},
		},
		{
			name:    "zero size is rejected",
			req:     CreateDiskRequest{Name: "data"},
			wantErr: true,
		},
	}

	for _, tc := range createTests {
		t.Run("create "+tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := diskCreateArgs(tc.req)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("diskCreateArgs = %v, want error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("diskCreateArgs returned error: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("diskCreateArgs = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDiskErrorClassifiers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		stderr string
		inUse  bool
		exists bool
		shrink bool
	}{
		{
			// Lima 2.2.0's exact wording for a delete.
			name:   "delete of an in-use disk",
			stderr: limaLog("fatal", "cannot delete disk `data` in use by instance `wd`"),
			inUse:  true,
		},
		{
			// And for a resize, which is worded differently.
			name:   "resize of an in-use disk",
			stderr: limaLog("fatal", "cannot resize disk `data` used by running instance `wd`. Please stop the VM instance"),
			inUse:  true,
		},
		{
			name:   "duplicate name",
			stderr: limaLog("fatal", "disk `data` already exists (`/tmp/lima/_disks/data`)"),
			exists: true,
		},
		{
			name:   "shrink refusal",
			stderr: limaLog("fatal", "specified size `512MiB` is less than the current disk size `2GiB`. Disk shrinking is currently unavailable"),
			shrink: true,
		},
		{
			name:   "unrelated failure",
			stderr: limaLog("fatal", "no space left on device"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := &CommandError{ExitCode: 1, Stderr: tc.stderr}
			if got := IsDiskInUse(err); got != tc.inUse {
				t.Errorf("IsDiskInUse = %v, want %v", got, tc.inUse)
			}
			if got := IsDiskExists(err); got != tc.exists {
				t.Errorf("IsDiskExists = %v, want %v", got, tc.exists)
			}
			if got := IsDiskShrink(err); got != tc.shrink {
				t.Errorf("IsDiskShrink = %v, want %v", got, tc.shrink)
			}
		})
	}
}

func TestDiskSizeFormattingMatchesLima(t *testing.T) {
	t.Parallel()

	// The size passed to --size must be one Lima accepts and round-trips, or
	// a create would silently produce a different disk than requested.
	for _, bytes := range []int64{1 << 30, 10 << 30, 512 << 20, 1500 << 20, 1 << 40} {
		args, err := diskCreateArgs(CreateDiskRequest{Name: "d", SizeBytes: bytes})
		if err != nil {
			t.Fatalf("diskCreateArgs(%d): %v", bytes, err)
		}
		size := args[len(args)-1]
		back, err := ParseSize(size)
		if err != nil {
			t.Fatalf("ParseSize(%q): %v", size, err)
		}
		if back != bytes {
			t.Errorf("%d formatted as %q parsed back as %d", bytes, size, back)
		}
		if !strings.HasSuffix(size, "B") {
			t.Errorf("size %q has no unit suffix", size)
		}
	}
}
