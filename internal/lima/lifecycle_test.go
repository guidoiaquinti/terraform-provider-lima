// Copyright 2026 Guido Iaquinti
// SPDX-License-Identifier: Apache-2.0

package lima_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/guidoiaquinti/terraform-provider-lima/internal/lima"
	"github.com/guidoiaquinti/terraform-provider-lima/internal/testutil"
)

// fastPoll removes real waiting from lifecycle tests. The polling algorithm
// itself is covered separately in poll_test.go.
var fastPoll = lima.PollOptions{
	Interval:    time.Millisecond,
	MaxInterval: time.Millisecond,
	Factor:      1,
}

func newService(t *testing.T, fake *testutil.FakeLimactl) *lima.Service {
	t.Helper()
	fake.ReadFile = os.ReadFile
	c, err := lima.NewExecClient(lima.Options{Binary: testutil.StubBinary(t), Runner: fake})
	if err != nil {
		t.Fatalf("NewExecClient: %v", err)
	}
	return lima.NewService(c, lima.WithPollOptions(fastPoll))
}

func TestServiceCreateStopped(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	svc := newService(t, fake)

	res, err := svc.Create(context.Background(), lima.CreateParams{
		Name:     "dev",
		Document: []byte("base:\n- url: template:ubuntu\n"),
		Start:    false,
		Validate: true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !res.Registered {
		t.Error("Registered = false, want true")
	}
	if res.Instance.Status() != lima.StatusStopped {
		t.Errorf("status = %q, want stopped", res.Instance.Status())
	}
	// start = false must not boot the VM.
	if n := len(fake.CallsFor("start")); n != 0 {
		t.Errorf("start was invoked %d times for start = false", n)
	}
	// Validation runs before create.
	if n := len(fake.CallsFor("validate")); n != 1 {
		t.Errorf("validate was invoked %d times, want 1", n)
	}
}

func TestServiceCreateAndStart(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	svc := newService(t, fake)

	res, err := svc.Create(context.Background(), lima.CreateParams{
		Name:     "dev",
		Document: []byte("cpus: 2\n"),
		Start:    true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if res.Instance.Status() != lima.StatusRunning {
		t.Errorf("status = %q, want running", res.Instance.Status())
	}
	// Connection details must be populated once running.
	ssh := res.Instance.SSH()
	if ssh.Address == "" || ssh.Port == 0 {
		t.Errorf("SSH details are empty after start: %+v", ssh)
	}
}

func TestServiceCreateWithProtection(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	svc := newService(t, fake)

	res, err := svc.Create(context.Background(), lima.CreateParams{
		Name: "dev", Document: []byte("cpus: 2\n"), Protect: true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !res.Instance.Protected {
		t.Error("instance was not protected after creation")
	}
}

func TestServiceCreateRejectsExistingInstance(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	fake.Seed(testutil.FakeInstance{Name: "dev"})
	svc := newService(t, fake)

	_, err := svc.Create(context.Background(), lima.CreateParams{Name: "dev", Document: []byte("cpus: 1\n")})
	if !errors.Is(err, lima.ErrAlreadyExists) {
		t.Fatalf("Create error = %v, want ErrAlreadyExists", err)
	}
	// The collision must be caught before anything is created, so the user
	// gets an import hint rather than Lima's terse message.
	if n := len(fake.CallsFor("create")); n != 0 {
		t.Errorf("create was invoked %d times despite the collision", n)
	}
}

func TestServiceCreatePartialFailureIsReported(t *testing.T) {
	t.Parallel()

	// Lima registered the instance but create failed. The caller must learn
	// that cleanup is needed, and nothing may be auto-deleted.
	fake := testutil.NewFakeLimactl()
	fake.CreateFailsFor = map[string]string{"dev": "image download failed"}
	fake.CreateRegistersOnFailure = true
	svc := newService(t, fake)

	res, err := svc.Create(context.Background(), lima.CreateParams{Name: "dev", Document: []byte("cpus: 1\n")})
	if err == nil {
		t.Fatal("Create succeeded, want an error")
	}
	if !res.Registered {
		t.Error("Registered = false, but Lima left the instance behind")
	}
	if _, ok := fake.Get("dev"); !ok {
		t.Error("the partially created instance was removed; the provider must not auto-delete")
	}
	if n := len(fake.CallsFor("delete")); n != 0 {
		t.Errorf("delete was invoked %d times during partial-failure handling, want 0", n)
	}
}

func TestServiceCreateCleanFailureIsNotRegistered(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	fake.CreateFailsFor = map[string]string{"dev": "bad template"}
	fake.CreateRegistersOnFailure = false
	svc := newService(t, fake)

	res, err := svc.Create(context.Background(), lima.CreateParams{Name: "dev", Document: []byte("cpus: 1\n")})
	if err == nil {
		t.Fatal("Create succeeded, want an error")
	}
	if res.Registered {
		t.Error("Registered = true, but Lima left nothing behind")
	}
}

func TestServiceCreateStartFailureLeavesInstance(t *testing.T) {
	t.Parallel()

	// Create succeeded, start failed. The instance must survive so a later
	// apply or destroy can recover it.
	fake := testutil.NewFakeLimactl()
	fake.StartFailsFor = map[string]string{"dev": "no free port"}
	svc := newService(t, fake)

	res, err := svc.Create(context.Background(), lima.CreateParams{
		Name: "dev", Document: []byte("cpus: 1\n"), Start: true,
	})
	if err == nil {
		t.Fatal("Create succeeded, want a start failure")
	}
	if !res.Registered {
		t.Error("Registered = false after a successful create")
	}
	if _, ok := fake.Get("dev"); !ok {
		t.Error("the instance was removed after a start failure")
	}
}

// Lima exits non-zero when a VM comes up but it cannot reach the guest agent,
// reporting `fatal: degraded, status={Running:true Degraded:true ...}` — the
// instance is running and usable, only its port forwards and file sharing may
// not be. Failing the apply for that is wrong twice over: Terraform reports
// "Unable to create Lima instance" for an instance that demonstrably exists and
// runs, and the user is left to reconcile state by hand.
//
// Observed repeatedly on CI runners under load; see the acceptance workflow.
func TestServiceStartAcceptsARunningButDegradedInstance(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	fake.Seed(testutil.FakeInstance{Name: "dev", Status: "Stopped"})
	// The VM comes up, then limactl exits 1 having declared it degraded.
	fake.Script(testutil.Scripted{
		Command:  "start",
		Name:     "dev",
		ExitCode: 1,
		Stderr:   "fatal: degraded, status={Running:true Degraded:true Errors:[guest agent does not seem to be running; port forwards will not work]}",
		Then: func(f *testutil.FakeLimactl) {
			f.Seed(testutil.FakeInstance{Name: "dev", Status: "Running"})
		},
	})
	svc := newService(t, fake)

	if err := svc.EnsureRunning(context.Background(), "dev"); err != nil {
		t.Fatalf("EnsureRunning on a running-but-degraded instance: %v", err)
	}
	if inst, _ := fake.Get("dev"); inst.Status != "Running" {
		t.Errorf("final status = %q, want Running", inst.Status)
	}
}

// The converse, so the fix cannot degenerate into ignoring start failures: when
// the instance is not running, a non-zero exit is still a failure.
func TestServiceStartStillFailsWhenTheInstanceIsNotRunning(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	fake.Seed(testutil.FakeInstance{Name: "dev", Status: "Stopped"})
	fake.Script(testutil.Scripted{
		Command:  "start",
		Name:     "dev",
		ExitCode: 1,
		Stderr:   "fatal: exiting, status={Running:false Degraded:false Exiting:true}",
	})
	svc := newService(t, fake)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := svc.EnsureRunning(ctx, "dev"); err == nil {
		t.Fatal("EnsureRunning succeeded for an instance that never started, want an error")
	}
}

func TestServiceEnsureRunning(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		startStatus string
		wantStarts  int
	}{
		{name: "stopped instance is started", startStatus: "Stopped", wantStarts: 1},
		// Starting an already-running instance is pointless work.
		{name: "running instance is left alone", startStatus: "Running", wantStarts: 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fake := testutil.NewFakeLimactl()
			fake.Seed(testutil.FakeInstance{Name: "dev", Status: tc.startStatus})
			svc := newService(t, fake)

			if err := svc.EnsureRunning(context.Background(), "dev"); err != nil {
				t.Fatalf("EnsureRunning: %v", err)
			}
			if n := len(fake.CallsFor("start")); n != tc.wantStarts {
				t.Errorf("start invoked %d times, want %d", n, tc.wantStarts)
			}
			if inst, _ := fake.Get("dev"); inst.Status != "Running" {
				t.Errorf("final status = %q, want Running", inst.Status)
			}
		})
	}
}

func TestServiceEnsureStopped(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		startStatus string
		wantStops   int
	}{
		{name: "running instance is stopped", startStatus: "Running", wantStops: 1},
		{
			// Lima errors on stopping a stopped instance, so skipping the
			// call is required for correctness, not just efficiency.
			name: "stopped instance is left alone", startStatus: "Stopped", wantStops: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fake := testutil.NewFakeLimactl()
			fake.Seed(testutil.FakeInstance{Name: "dev", Status: tc.startStatus})
			svc := newService(t, fake)

			if err := svc.EnsureStopped(context.Background(), "dev"); err != nil {
				t.Fatalf("EnsureStopped: %v", err)
			}
			if n := len(fake.CallsFor("stop")); n != tc.wantStops {
				t.Errorf("stop invoked %d times, want %d", n, tc.wantStops)
			}
			if inst, _ := fake.Get("dev"); inst.Status != "Stopped" {
				t.Errorf("final status = %q, want Stopped", inst.Status)
			}
		})
	}
}

func TestServiceSetProtection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		initial        bool
		want           bool
		wantProtects   int
		wantUnprotects int
	}{
		{name: "protect an unprotected instance", initial: false, want: true, wantProtects: 1},
		{name: "unprotect a protected instance", initial: true, want: false, wantUnprotects: 1},
		// No-op transitions must not run a command at all.
		{name: "already protected", initial: true, want: true},
		{name: "already unprotected", initial: false, want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fake := testutil.NewFakeLimactl()
			fake.Seed(testutil.FakeInstance{Name: "dev", Protected: tc.initial})
			svc := newService(t, fake)

			if err := svc.SetProtection(context.Background(), "dev", tc.want); err != nil {
				t.Fatalf("SetProtection: %v", err)
			}
			if inst, _ := fake.Get("dev"); inst.Protected != tc.want {
				t.Errorf("protected = %v, want %v", inst.Protected, tc.want)
			}
			if n := len(fake.CallsFor("protect")); n != tc.wantProtects {
				t.Errorf("protect invoked %d times, want %d", n, tc.wantProtects)
			}
			if n := len(fake.CallsFor("unprotect")); n != tc.wantUnprotects {
				t.Errorf("unprotect invoked %d times, want %d", n, tc.wantUnprotects)
			}
		})
	}
}

func TestServiceDelete(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	fake.Seed(testutil.FakeInstance{Name: "dev", Status: "Running"})
	svc := newService(t, fake)

	if err := svc.Delete(context.Background(), "dev"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := fake.Get("dev"); ok {
		t.Error("instance survived Delete")
	}
	// --force handles a running instance, so no separate stop is needed.
	if n := len(fake.CallsFor("stop")); n != 0 {
		t.Errorf("stop invoked %d times; delete --force should suffice", n)
	}
}

func TestServiceDeleteAbsentIsSuccess(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	svc := newService(t, fake)

	if err := svc.Delete(context.Background(), "nope"); err != nil {
		t.Errorf("deleting an absent instance returned error: %v", err)
	}
	if n := len(fake.CallsFor("delete")); n != 0 {
		t.Errorf("delete was invoked %d times for an instance that does not exist", n)
	}
}

func TestServiceDeleteProtectedFails(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	fake.Seed(testutil.FakeInstance{Name: "dev", Protected: true})
	svc := newService(t, fake)

	err := svc.Delete(context.Background(), "dev")
	if !errors.Is(err, lima.ErrProtected) {
		t.Fatalf("Delete error = %v, want ErrProtected", err)
	}
	if _, ok := fake.Get("dev"); !ok {
		t.Error("a protected instance was deleted")
	}
	// Protection is an explicit user statement; the provider must never work
	// around it by unprotecting first.
	if n := len(fake.CallsFor("unprotect")); n != 0 {
		t.Errorf("unprotect was invoked %d times during a protected delete", n)
	}
}

func TestServiceExists(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	fake.Seed(testutil.FakeInstance{Name: "dev"})
	svc := newService(t, fake)
	ctx := context.Background()

	if ok, err := svc.Exists(ctx, "dev"); err != nil || !ok {
		t.Errorf("Exists(dev) = %v, %v; want true, nil", ok, err)
	}
	if ok, err := svc.Exists(ctx, "nope"); err != nil || ok {
		t.Errorf("Exists(nope) = %v, %v; want false, nil", ok, err)
	}
}

func TestServiceGetNotFound(t *testing.T) {
	t.Parallel()

	svc := newService(t, testutil.NewFakeLimactl())
	_, err := svc.Get(context.Background(), "nope")
	if !lima.IsNotFound(err) {
		t.Errorf("Get error = %v, want a not-found error", err)
	}
}

func TestServiceBrokenStatusFailsFast(t *testing.T) {
	t.Parallel()

	// An instance that stays Broken is terminal: polling must abort rather
	// than spin until the timeout. The scripted start exits 0 without
	// changing state, which is how Lima behaves when the VM fails to come up
	// but the command itself returns.
	fake := testutil.NewFakeLimactl()
	fake.Seed(testutil.FakeInstance{Name: "dev", Status: "Broken"})
	fake.Script(testutil.Scripted{Command: "start", ExitCode: 0})
	svc := newService(t, fake)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := svc.EnsureRunning(ctx, "dev")
	if err == nil {
		t.Fatal("EnsureRunning on a broken instance succeeded, want an error")
	}
	if lima.IsTimeout(err) {
		t.Errorf("error = %v, want a fast failure rather than a timeout", err)
	}
}

func TestServiceOperationsAreSerializedPerInstance(t *testing.T) {
	t.Parallel()

	// Two concurrent operations on the same instance must not interleave.
	fake := testutil.NewFakeLimactl()
	fake.Seed(testutil.FakeInstance{Name: "dev", Status: "Stopped"})
	// Slow down the inspect that precedes start, widening the window in
	// which an unsynchronised second caller would also decide to start.
	fake.Script(testutil.Scripted{
		Command: "list",
		Delay:   50 * time.Millisecond,
		Times:   1,
		Stdout:  `{"name":"dev","status":"Stopped","cpus":4}` + "\n",
	})
	svc := newService(t, fake)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { errs <- svc.EnsureRunning(ctx, "dev") }()
	}
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Errorf("EnsureRunning: %v", err)
		}
	}

	// The second caller observes the instance already running and skips the
	// command entirely.
	if n := len(fake.CallsFor("start")); n != 1 {
		t.Errorf("start invoked %d times, want 1", n)
	}
}

func TestServiceRespectsContextCancellation(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	fake.Seed(testutil.FakeInstance{Name: "dev", Status: "Stopped"})
	fake.Script(testutil.Scripted{Command: "start", Delay: 5 * time.Second})
	svc := newService(t, fake)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	if err := svc.EnsureRunning(ctx, "dev"); err == nil {
		t.Fatal("EnsureRunning succeeded, want cancellation")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("EnsureRunning took %s to honour cancellation", elapsed)
	}
}

func TestServiceCreateTimeoutReportsLastStatus(t *testing.T) {
	t.Parallel()

	// Lima never brings the instance to Running; the timeout must say what
	// it actually saw.
	fake := testutil.NewFakeLimactl()
	fake.Seed(testutil.FakeInstance{Name: "dev", Status: "Stopped"})
	fake.Script(testutil.Scripted{Command: "start", ExitCode: 0})
	svc := newService(t, fake)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := svc.EnsureRunning(ctx, "dev")
	if err == nil {
		t.Fatal("EnsureRunning succeeded, want a timeout")
	}
	if !lima.IsTimeout(err) {
		t.Fatalf("error = %v, want a TimeoutError", err)
	}
	var te *lima.TimeoutError
	if errors.As(err, &te) && te.LastSeen != "stopped" {
		t.Errorf("LastSeen = %q, want stopped", te.LastSeen)
	}
}

func TestServiceResizeStoppedInstance(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	fake.Seed(testutil.FakeInstance{Name: "dev", Status: "Stopped", CPUs: 2, Memory: 1 << 30, Disk: 8 << 30})
	svc := newService(t, fake)

	err := svc.Resize(context.Background(), lima.ResizeParams{
		Name:        "dev",
		Desired:     lima.EditRequest{CPUs: 4, MemoryBytes: 2 << 30},
		WantRunning: false,
	})
	if err != nil {
		t.Fatalf("Resize: %v", err)
	}

	inst, _ := fake.Get("dev")
	if inst.CPUs != 4 || inst.Memory != 2<<30 {
		t.Errorf("after resize cpus=%d memory=%d, want 4 and %d", inst.CPUs, inst.Memory, int64(2<<30))
	}
	if inst.Status != "Stopped" {
		t.Errorf("status = %q, want it to stay Stopped", inst.Status)
	}
	// A stopped instance needs no stop and must not be started.
	if n := len(fake.CallsFor("stop")); n != 0 {
		t.Errorf("stop invoked %d times on an already-stopped instance", n)
	}
	if n := len(fake.CallsFor("start")); n != 0 {
		t.Errorf("start invoked %d times when WantRunning is false", n)
	}
}

func TestServiceResizeRunningInstanceStopsAndRestarts(t *testing.T) {
	t.Parallel()

	// Lima refuses to edit a running instance, so the service must stop it,
	// apply the change, and bring it back up.
	fake := testutil.NewFakeLimactl()
	fake.Seed(testutil.FakeInstance{Name: "dev", Status: "Running", CPUs: 2, Memory: 1 << 30, Disk: 8 << 30})
	svc := newService(t, fake)

	err := svc.Resize(context.Background(), lima.ResizeParams{
		Name:        "dev",
		Desired:     lima.EditRequest{CPUs: 8},
		WantRunning: true,
	})
	if err != nil {
		t.Fatalf("Resize: %v", err)
	}

	inst, _ := fake.Get("dev")
	if inst.CPUs != 8 {
		t.Errorf("cpus = %d, want 8", inst.CPUs)
	}
	if inst.Status != "Running" {
		t.Errorf("status = %q, want the instance to be running again", inst.Status)
	}
	for _, cmd := range []string{"stop", "edit", "start"} {
		if n := len(fake.CallsFor(cmd)); n != 1 {
			t.Errorf("%s invoked %d times, want 1", cmd, n)
		}
	}
}

func TestServiceResizeRunningToStoppedDoesNotRestart(t *testing.T) {
	t.Parallel()

	// A plan that both resizes and sets start = false must leave the
	// instance down, without a pointless start/stop cycle.
	fake := testutil.NewFakeLimactl()
	fake.Seed(testutil.FakeInstance{Name: "dev", Status: "Running", CPUs: 2})
	svc := newService(t, fake)

	if err := svc.Resize(context.Background(), lima.ResizeParams{
		Name:        "dev",
		Desired:     lima.EditRequest{CPUs: 4},
		WantRunning: false,
	}); err != nil {
		t.Fatalf("Resize: %v", err)
	}

	inst, _ := fake.Get("dev")
	if inst.Status != "Stopped" || inst.CPUs != 4 {
		t.Errorf("instance = %s/%d cpus, want Stopped/4", inst.Status, inst.CPUs)
	}
	if n := len(fake.CallsFor("start")); n != 0 {
		t.Errorf("start invoked %d times, want 0", n)
	}
}

func TestServiceResizeStoppedToRunning(t *testing.T) {
	t.Parallel()

	// Resize and start in one operation.
	fake := testutil.NewFakeLimactl()
	fake.Seed(testutil.FakeInstance{Name: "dev", Status: "Stopped", CPUs: 2})
	svc := newService(t, fake)

	if err := svc.Resize(context.Background(), lima.ResizeParams{
		Name:        "dev",
		Desired:     lima.EditRequest{CPUs: 4},
		WantRunning: true,
	}); err != nil {
		t.Fatalf("Resize: %v", err)
	}

	inst, _ := fake.Get("dev")
	if inst.Status != "Running" || inst.CPUs != 4 {
		t.Errorf("instance = %s/%d cpus, want Running/4", inst.Status, inst.CPUs)
	}
}

func TestServiceResizeEmptyRequestOnlyReconcilesRunState(t *testing.T) {
	t.Parallel()

	// An update touching only `start` must not stop the instance to run a
	// pointless edit.
	fake := testutil.NewFakeLimactl()
	fake.Seed(testutil.FakeInstance{Name: "dev", Status: "Stopped"})
	svc := newService(t, fake)

	if err := svc.Resize(context.Background(), lima.ResizeParams{
		Name:        "dev",
		Desired:     lima.EditRequest{},
		WantRunning: true,
	}); err != nil {
		t.Fatalf("Resize: %v", err)
	}

	if n := len(fake.CallsFor("edit")); n != 0 {
		t.Errorf("edit invoked %d times for an empty resize request", n)
	}
	if inst, _ := fake.Get("dev"); inst.Status != "Running" {
		t.Errorf("status = %q, want Running", inst.Status)
	}
}

func TestServiceResizeRestartFailureIsReportedDistinctly(t *testing.T) {
	t.Parallel()

	// The dangerous case: the edit applied, but the VM will not come back up.
	// The caller must be able to tell this apart from a failed edit, because
	// the requested change *did* take effect.
	fake := testutil.NewFakeLimactl()
	fake.Seed(testutil.FakeInstance{Name: "dev", Status: "Running", CPUs: 2})
	fake.StartFailsFor = map[string]string{"dev": "could not allocate memory"}
	svc := newService(t, fake)

	err := svc.Resize(context.Background(), lima.ResizeParams{
		Name:        "dev",
		Desired:     lima.EditRequest{CPUs: 64},
		WantRunning: true,
	})
	if err == nil {
		t.Fatal("Resize succeeded, want a restart failure")
	}

	var restartErr *lima.RestartAfterEditError
	if !errors.As(err, &restartErr) {
		t.Fatalf("error %v is not a RestartAfterEditError", err)
	}
	if restartErr.Name != "dev" {
		t.Errorf("Name = %q, want dev", restartErr.Name)
	}
	if !strings.Contains(err.Error(), "reconfigured successfully") {
		t.Errorf("error %q should make clear that the edit applied", err)
	}

	// The edit really did apply, and the instance is simply down. State must
	// not be corrupted.
	inst, _ := fake.Get("dev")
	if inst.CPUs != 64 {
		t.Errorf("cpus = %d, want the edit to have been applied (64)", inst.CPUs)
	}
	if inst.Status != "Stopped" {
		t.Errorf("status = %q, want Stopped", inst.Status)
	}
}

func TestServiceResizeEditFailureLeavesInstanceStopped(t *testing.T) {
	t.Parallel()

	// When the edit itself fails, the instance is left stopped rather than
	// restarted with the old configuration, so the user cannot mistake a
	// running VM for a successful change.
	fake := testutil.NewFakeLimactl()
	fake.Seed(testutil.FakeInstance{Name: "dev", Status: "Running", CPUs: 2})
	fake.EditFailsFor = map[string]string{"dev": "invalid configuration"}
	svc := newService(t, fake)

	err := svc.Resize(context.Background(), lima.ResizeParams{
		Name:        "dev",
		Desired:     lima.EditRequest{CPUs: 4},
		WantRunning: true,
	})
	if err == nil {
		t.Fatal("Resize succeeded, want an edit failure")
	}

	var restartErr *lima.RestartAfterEditError
	if errors.As(err, &restartErr) {
		t.Error("an edit failure must not be reported as a restart failure")
	}

	inst, _ := fake.Get("dev")
	if inst.CPUs != 2 {
		t.Errorf("cpus = %d, want the original 2", inst.CPUs)
	}
	if n := len(fake.CallsFor("start")); n != 0 {
		t.Errorf("start invoked %d times after a failed edit, want 0", n)
	}
}

func TestServiceResizeRejectsDiskShrink(t *testing.T) {
	t.Parallel()

	// The provider blocks this at plan time, but Lima is the backstop and the
	// service must surface its refusal rather than swallowing it.
	fake := testutil.NewFakeLimactl()
	fake.Seed(testutil.FakeInstance{Name: "dev", Status: "Stopped", Disk: 12 << 30})
	svc := newService(t, fake)

	err := svc.Resize(context.Background(), lima.ResizeParams{
		Name:    "dev",
		Desired: lima.EditRequest{DiskBytes: 4 << 30},
	})
	if err == nil {
		t.Fatal("Resize succeeded, want Lima's shrink refusal")
	}
	if !strings.Contains(err.Error(), "shrinking the disk") {
		t.Errorf("error = %q, want Lima's shrink message", err)
	}
	if inst, _ := fake.Get("dev"); inst.Disk != 12<<30 {
		t.Errorf("disk = %d, want it unchanged", inst.Disk)
	}
}

func TestServiceResizeSerializesWithOtherOperations(t *testing.T) {
	t.Parallel()

	// Resize takes the same per-instance lock as every other mutation, so a
	// concurrent start cannot interleave with the stop/edit/start sequence.
	fake := testutil.NewFakeLimactl()
	fake.Seed(testutil.FakeInstance{Name: "dev", Status: "Running", CPUs: 2})
	svc := newService(t, fake)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	errs := make(chan error, 2)
	go func() {
		errs <- svc.Resize(ctx, lima.ResizeParams{
			Name: "dev", Desired: lima.EditRequest{CPUs: 4}, WantRunning: true,
		})
	}()
	go func() { errs <- svc.EnsureRunning(ctx, "dev") }()

	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Errorf("concurrent operation failed: %v", err)
		}
	}

	inst, _ := fake.Get("dev")
	if inst.Status != "Running" || inst.CPUs != 4 {
		t.Errorf("final state = %s/%d cpus, want Running/4", inst.Status, inst.CPUs)
	}
}

func TestServiceResizeSkipsNoOpEdits(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		seeded     testutil.FakeInstance
		desired    lima.EditRequest
		wantEdits  int
		wantStops  int
		wantStarts int
	}{
		{
			// The import case: state is silent about cpus, so the provider
			// asks for 4 even though the VM already has 4. Restarting to
			// apply nothing would be pure downtime.
			name:      "requested values already match",
			seeded:    testutil.FakeInstance{Name: "dev", Status: "Running", CPUs: 4, Memory: 4 << 30, Disk: 100 << 30},
			desired:   lima.EditRequest{CPUs: 4, MemoryBytes: 4 << 30, DiskBytes: 100 << 30},
			wantEdits: 0, wantStops: 0, wantStarts: 0,
		},
		{
			// Only the field that genuinely differs is sent to Lima.
			name:      "one field differs",
			seeded:    testutil.FakeInstance{Name: "dev", Status: "Stopped", CPUs: 4, Memory: 4 << 30, Disk: 100 << 30},
			desired:   lima.EditRequest{CPUs: 4, MemoryBytes: 8 << 30, DiskBytes: 100 << 30},
			wantEdits: 1,
		},
		{
			name:      "everything differs",
			seeded:    testutil.FakeInstance{Name: "dev", Status: "Stopped", CPUs: 2, Memory: 2 << 30, Disk: 50 << 30},
			desired:   lima.EditRequest{CPUs: 4, MemoryBytes: 4 << 30, DiskBytes: 100 << 30},
			wantEdits: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fake := testutil.NewFakeLimactl()
			fake.Seed(tc.seeded)
			svc := newService(t, fake)

			wantRunning := tc.seeded.Status == "Running"
			if err := svc.Resize(context.Background(), lima.ResizeParams{
				Name: "dev", Desired: tc.desired, WantRunning: wantRunning,
			}); err != nil {
				t.Fatalf("Resize: %v", err)
			}

			if n := len(fake.CallsFor("edit")); n != tc.wantEdits {
				t.Errorf("edit invoked %d times, want %d", n, tc.wantEdits)
			}
			if tc.wantEdits == 0 {
				if n := len(fake.CallsFor("stop")); n != tc.wantStops {
					t.Errorf("stop invoked %d times, want %d (a no-op resize must not restart the VM)", n, tc.wantStops)
				}
				if n := len(fake.CallsFor("start")); n != tc.wantStarts {
					t.Errorf("start invoked %d times, want %d", n, tc.wantStarts)
				}
			}
		})
	}
}

func TestServiceResizeNoOpStillReconcilesRunState(t *testing.T) {
	t.Parallel()

	// Resources already match, but the plan also asks for the instance to be
	// stopped. That part must still happen.
	fake := testutil.NewFakeLimactl()
	fake.Seed(testutil.FakeInstance{Name: "dev", Status: "Running", CPUs: 4})
	svc := newService(t, fake)

	if err := svc.Resize(context.Background(), lima.ResizeParams{
		Name: "dev", Desired: lima.EditRequest{CPUs: 4}, WantRunning: false,
	}); err != nil {
		t.Fatalf("Resize: %v", err)
	}

	if n := len(fake.CallsFor("edit")); n != 0 {
		t.Errorf("edit invoked %d times for a no-op resize", n)
	}
	if inst, _ := fake.Get("dev"); inst.Status != "Stopped" {
		t.Errorf("status = %q, want Stopped", inst.Status)
	}
}

func TestServiceResizeMountsPreserveTemplateEntries(t *testing.T) {
	t.Parallel()

	// Reproduces exactly what Lima 2.2.0 resolves: the declared mount first,
	// then one the base template contributed. Replacing the declared mount
	// must not unmount the template's.
	fake := testutil.NewFakeLimactl()
	inst := testutil.FakeInstance{Name: "dev", Status: "Stopped"}
	inst.Config.Mounts = []testutil.FakeMount{
		{Location: "/private/tmp", MountPoint: "/workspace", Writable: true},
		{Location: "/Users/alice", MountPoint: "/Users/alice", Writable: false},
	}
	fake.Seed(inst)
	svc := newService(t, fake)

	desired := []lima.Mount{{Location: "/private/var/tmp", MountPoint: "/scratch", Writable: true}}
	previous := []lima.Mount{{Location: "/private/tmp", MountPoint: "/workspace", Writable: true}}

	if err := svc.Resize(context.Background(), lima.ResizeParams{
		Name:           "dev",
		Mounts:         &desired,
		PreviousMounts: previous,
	}); err != nil {
		t.Fatalf("Resize: %v", err)
	}

	got, _ := fake.Get("dev")
	want := []testutil.FakeMount{
		// The new declared mount, then the template's, matching create order.
		{Location: "/private/var/tmp", MountPoint: "/scratch", Writable: true},
		{Location: "/Users/alice", MountPoint: "/Users/alice", Writable: false},
	}
	if len(got.Config.Mounts) != len(want) {
		t.Fatalf("resolved mounts = %+v, want %+v", got.Config.Mounts, want)
	}
	for i := range want {
		if got.Config.Mounts[i] != want[i] {
			t.Errorf("mount %d = %+v, want %+v", i, got.Config.Mounts[i], want[i])
		}
	}
}

func TestServiceResizeRestoresAMountRemovedOutsideTerraform(t *testing.T) {
	t.Parallel()

	// The declared mount was removed behind Terraform's back. Applying must
	// put it back rather than failing, and must not disturb the template's.
	fake := testutil.NewFakeLimactl()
	inst := testutil.FakeInstance{Name: "dev", Status: "Stopped"}
	inst.Config.Mounts = []testutil.FakeMount{
		{Location: "/Users/alice", MountPoint: "/Users/alice", Writable: false},
	}
	fake.Seed(inst)
	svc := newService(t, fake)

	declared := []lima.Mount{{Location: "/private/tmp", MountPoint: "/workspace", Writable: true}}

	if err := svc.Resize(context.Background(), lima.ResizeParams{
		Name: "dev", Mounts: &declared, PreviousMounts: declared,
	}); err != nil {
		t.Fatalf("Resize: %v", err)
	}

	got, _ := fake.Get("dev")
	want := []testutil.FakeMount{
		{Location: "/private/tmp", MountPoint: "/workspace", Writable: true},
		{Location: "/Users/alice", MountPoint: "/Users/alice", Writable: false},
	}
	if len(got.Config.Mounts) != len(want) {
		t.Fatalf("mounts = %+v, want %+v", got.Config.Mounts, want)
	}
	for i := range want {
		if got.Config.Mounts[i] != want[i] {
			t.Errorf("mount %d = %+v, want %+v", i, got.Config.Mounts[i], want[i])
		}
	}
}

func TestServiceResizeReplacesAMountModifiedOutsideTerraform(t *testing.T) {
	t.Parallel()

	// The declared mount was altered externally (writable flipped). Matching
	// by location means the stale entry is replaced rather than the location
	// ending up mounted twice.
	fake := testutil.NewFakeLimactl()
	inst := testutil.FakeInstance{Name: "dev", Status: "Stopped"}
	inst.Config.Mounts = []testutil.FakeMount{
		{Location: "/private/tmp", MountPoint: "/elsewhere", Writable: false},
		{Location: "/Users/alice", MountPoint: "/Users/alice", Writable: false},
	}
	fake.Seed(inst)
	svc := newService(t, fake)

	declared := []lima.Mount{{Location: "/private/tmp", MountPoint: "/workspace", Writable: true}}

	if err := svc.Resize(context.Background(), lima.ResizeParams{
		Name: "dev", Mounts: &declared, PreviousMounts: declared,
	}); err != nil {
		t.Fatalf("Resize: %v", err)
	}

	got, _ := fake.Get("dev")
	if len(got.Config.Mounts) != 2 {
		t.Fatalf("mounts = %+v, want exactly 2 (no duplicate location)", got.Config.Mounts)
	}
	if got.Config.Mounts[0] != (testutil.FakeMount{Location: "/private/tmp", MountPoint: "/workspace", Writable: true}) {
		t.Errorf("mount 0 = %+v, want the declared configuration restored", got.Config.Mounts[0])
	}
}

func TestServiceResizeMountsNoOpWhenAlreadyMatching(t *testing.T) {
	t.Parallel()

	// Re-applying the same mounts must not stop and restart the VM.
	fake := testutil.NewFakeLimactl()
	inst := testutil.FakeInstance{Name: "dev", Status: "Running"}
	inst.Config.Mounts = []testutil.FakeMount{
		{Location: "/private/tmp", MountPoint: "/workspace", Writable: true},
		{Location: "/Users/alice", MountPoint: "/Users/alice", Writable: false},
	}
	fake.Seed(inst)
	svc := newService(t, fake)

	same := []lima.Mount{{Location: "/private/tmp", MountPoint: "/workspace", Writable: true}}

	if err := svc.Resize(context.Background(), lima.ResizeParams{
		Name: "dev", Mounts: &same, PreviousMounts: same, WantRunning: true,
	}); err != nil {
		t.Fatalf("Resize: %v", err)
	}

	if n := len(fake.CallsFor("edit")); n != 0 {
		t.Errorf("edit invoked %d times for an unchanged mount list", n)
	}
	for _, cmd := range []string{"stop", "start"} {
		if n := len(fake.CallsFor(cmd)); n != 0 {
			t.Errorf("%s invoked %d times for a no-op mount change", cmd, n)
		}
	}
}

func TestServiceResizeRemovingEveryMount(t *testing.T) {
	t.Parallel()

	// Deleting all mount blocks is a real request. The template's mounts must
	// still survive, because the user never declared those.
	fake := testutil.NewFakeLimactl()
	inst := testutil.FakeInstance{Name: "dev", Status: "Stopped"}
	inst.Config.Mounts = []testutil.FakeMount{
		{Location: "/private/tmp", MountPoint: "/workspace", Writable: true},
		{Location: "/Users/alice", MountPoint: "/Users/alice", Writable: false},
	}
	fake.Seed(inst)
	svc := newService(t, fake)

	none := []lima.Mount{}
	previous := []lima.Mount{{Location: "/private/tmp", MountPoint: "/workspace", Writable: true}}

	if err := svc.Resize(context.Background(), lima.ResizeParams{
		Name: "dev", Mounts: &none, PreviousMounts: previous,
	}); err != nil {
		t.Fatalf("Resize: %v", err)
	}

	got, _ := fake.Get("dev")
	if len(got.Config.Mounts) != 1 || got.Config.Mounts[0].Location != "/Users/alice" {
		t.Errorf("resolved mounts = %+v, want only the template's /Users/alice", got.Config.Mounts)
	}
}

func TestServiceResizeUnmanagedMountsAreLeftAlone(t *testing.T) {
	t.Parallel()

	// No mount blocks declared at all means the attribute is unmanaged, and
	// Lima's mounts must not be touched even while resizing CPUs.
	fake := testutil.NewFakeLimactl()
	inst := testutil.FakeInstance{Name: "dev", Status: "Stopped", CPUs: 2}
	inst.Config.Mounts = []testutil.FakeMount{
		{Location: "/Users/alice", MountPoint: "/Users/alice", Writable: false},
	}
	fake.Seed(inst)
	svc := newService(t, fake)

	if err := svc.Resize(context.Background(), lima.ResizeParams{
		Name:    "dev",
		Desired: lima.EditRequest{CPUs: 4},
		Mounts:  nil,
	}); err != nil {
		t.Fatalf("Resize: %v", err)
	}

	got, _ := fake.Get("dev")
	if len(got.Config.Mounts) != 1 {
		t.Errorf("unmanaged mounts were modified: %+v", got.Config.Mounts)
	}
	if got.CPUs != 4 {
		t.Errorf("cpus = %d, want the resize to still have applied", got.CPUs)
	}
}

func TestServiceResizePortForwards(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	inst := testutil.FakeInstance{Name: "dev", Status: "Stopped"}
	inst.Config.PortForwards = []testutil.FakePortForward{
		{GuestPort: 8080, HostPort: 18080, Proto: "tcp", GuestIP: "127.0.0.1", HostIP: "127.0.0.1"},
	}
	fake.Seed(inst)
	svc := newService(t, fake)

	desired := []lima.PortForward{{GuestPort: 9090, HostPort: 19090, Proto: "tcp"}}
	previous := []lima.PortForward{{GuestPort: 8080, HostPort: 18080, Proto: "tcp"}}

	if err := svc.Resize(context.Background(), lima.ResizeParams{
		Name: "dev", PortForwards: &desired, PreviousPortForwards: previous,
	}); err != nil {
		t.Fatalf("Resize: %v", err)
	}

	got, _ := fake.Get("dev")
	if len(got.Config.PortForwards) != 1 {
		t.Fatalf("resolved forwards = %+v, want 1", got.Config.PortForwards)
	}
	if got.Config.PortForwards[0].GuestPort != 9090 || got.Config.PortForwards[0].HostPort != 19090 {
		t.Errorf("forward = %+v, want 9090 -> 19090", got.Config.PortForwards[0])
	}
}

func TestServiceResizeMountsAndCPUsTogether(t *testing.T) {
	t.Parallel()

	// One stop/start cycle must cover both changes.
	fake := testutil.NewFakeLimactl()
	inst := testutil.FakeInstance{Name: "dev", Status: "Running", CPUs: 2}
	inst.Config.Mounts = []testutil.FakeMount{{Location: "/a", Writable: false}}
	fake.Seed(inst)
	svc := newService(t, fake)

	desired := []lima.Mount{{Location: "/b", Writable: true}}
	previous := []lima.Mount{{Location: "/a", Writable: false}}

	if err := svc.Resize(context.Background(), lima.ResizeParams{
		Name:           "dev",
		Desired:        lima.EditRequest{CPUs: 8},
		Mounts:         &desired,
		PreviousMounts: previous,
		WantRunning:    true,
	}); err != nil {
		t.Fatalf("Resize: %v", err)
	}

	for _, cmd := range []string{"stop", "edit", "start"} {
		if n := len(fake.CallsFor(cmd)); n != 1 {
			t.Errorf("%s invoked %d times, want exactly 1", cmd, n)
		}
	}
	got, _ := fake.Get("dev")
	if got.CPUs != 8 {
		t.Errorf("cpus = %d, want 8", got.CPUs)
	}
	if len(got.Config.Mounts) != 1 || got.Config.Mounts[0].Location != "/b" {
		t.Errorf("mounts = %+v, want only /b", got.Config.Mounts)
	}
}

func TestServiceVerifyTemplate(t *testing.T) {
	t.Parallel()

	alpineImages := []testutil.FakeImage{
		{Location: "https://dl-cdn.alpinelinux.org/alpine/v3.23/releases/cloud/nocloud_alpine-3.23.4-x86_64-uefi-cloudinit-r0.qcow2"},
		{Location: "https://dl-cdn.alpinelinux.org/alpine/v3.23/releases/cloud/nocloud_alpine-3.23.4-aarch64-uefi-cloudinit-r0.qcow2"},
	}
	ubuntuImages := []testutil.FakeImage{
		{Location: "https://cloud-images.ubuntu.com/releases/resolute/release/ubuntu-26.04-server-cloudimg-amd64.img"},
	}

	tests := []struct {
		name    string
		images  []testutil.FakeImage
		claim   string
		want    lima.TemplateMatch
		wantErr bool
	}{
		{
			name:   "matching template is consistent",
			images: alpineImages,
			claim:  "template:alpine",
			want:   lima.TemplateConsistent,
		},
		{
			// The useful case: a definite refutation.
			name:   "wrong template is refuted",
			images: alpineImages,
			claim:  "template:ubuntu",
			want:   lima.TemplateMismatch,
		},
		{
			// docker is ubuntu plus provisioning, so they share an image and
			// this check cannot distinguish them. Reporting "consistent"
			// rather than "confirmed" is exactly why the result is named that
			// way.
			name:   "templates sharing a base image cannot be told apart",
			images: ubuntuImages,
			claim:  "template:docker",
			want:   lima.TemplateConsistent,
		},
		{
			// No evidence either way is not a refutation.
			name:   "instance without images is unknown",
			images: nil,
			claim:  "template:alpine",
			want:   lima.TemplateUnknown,
		},
		{
			name:    "unresolvable template reports an error",
			images:  alpineImages,
			claim:   "template:does-not-exist",
			want:    lima.TemplateUnknown,
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fake := testutil.NewFakeLimactl()
			inst := testutil.FakeInstance{Name: "dev", Status: "Stopped"}
			inst.Config.Images = tc.images
			fake.Seed(inst)
			svc := newService(t, fake)

			observed, err := svc.Get(context.Background(), "dev")
			if err != nil {
				t.Fatalf("Get: %v", err)
			}

			got, err := svc.VerifyTemplate(context.Background(), tc.claim, observed)
			if tc.wantErr != (err != nil) {
				t.Fatalf("VerifyTemplate error = %v, wantErr %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("VerifyTemplate = %v, want %v", got, tc.want)
			}
		})
	}
}
