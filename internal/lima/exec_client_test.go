package lima_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/guidoiaquinti/terraform-provider-lima/internal/lima"
	"github.com/guidoiaquinti/terraform-provider-lima/internal/testutil"
)

// newClient wires an ExecClient to a fake limactl. Argument construction,
// environment merging, output parsing and error classification are all real;
// only process execution is simulated.
func newClient(t *testing.T, fake *testutil.FakeLimactl, opts ...func(*lima.Options)) *lima.ExecClient {
	t.Helper()
	fake.ReadFile = os.ReadFile

	o := lima.Options{
		Binary: testutil.StubBinary(t),
		Runner: fake,
	}
	for _, fn := range opts {
		fn(&o)
	}
	c, err := lima.NewExecClient(o)
	if err != nil {
		t.Fatalf("NewExecClient: %v", err)
	}
	return c
}

func TestNewExecClientResolvesFromPath(t *testing.T) {
	// Not parallel: t.Setenv mutates process-wide state.

	// A bare name must resolve through PATH.
	dir := t.TempDir()
	path := filepath.Join(dir, "limactl")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("writing stub: %v", err)
	}
	t.Setenv("PATH", dir)

	c, err := lima.NewExecClient(lima.Options{Runner: testutil.NewFakeLimactl()})
	if err != nil {
		t.Fatalf("NewExecClient: %v", err)
	}
	if !strings.HasSuffix(c.Binary(), "limactl") {
		t.Errorf("Binary() = %q, want a path ending in limactl", c.Binary())
	}
}

func TestNewExecClientErrors(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	notExecutable := filepath.Join(dir, "notexec")
	if err := os.WriteFile(notExecutable, []byte("x"), 0o644); err != nil {
		t.Fatalf("writing file: %v", err)
	}

	tests := []struct {
		name    string
		binary  string
		wantErr string
	}{
		{
			name:    "missing file",
			binary:  filepath.Join(dir, "nope"),
			wantErr: "no such file",
		},
		{
			name:    "directory instead of a binary",
			binary:  dir,
			wantErr: "is a directory",
		},
		{
			name:    "file without the executable bit",
			binary:  notExecutable,
			wantErr: "not executable",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := lima.NewExecClient(lima.Options{Binary: tc.binary})
			if err == nil {
				t.Fatalf("NewExecClient(%q) succeeded, want error", tc.binary)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestNewExecClientNotInPath(t *testing.T) {
	// Not parallel: t.Setenv mutates process-wide state.
	t.Setenv("PATH", t.TempDir())

	_, err := lima.NewExecClient(lima.Options{Binary: "definitely-not-a-real-binary"})
	if err == nil {
		t.Fatal("NewExecClient succeeded, want an error")
	}
	if !strings.Contains(err.Error(), "not found in PATH") {
		t.Errorf("error = %q, want it to mention PATH", err)
	}
}

func TestExecClientVersion(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	c := newClient(t, fake)

	v, err := c.Version(context.Background())
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if v.Core() != "2.2.0" {
		t.Errorf("version = %s, want 2.2.0", v.Core())
	}

	// The second call must be served from cache rather than re-executing.
	if _, err := c.Version(context.Background()); err != nil {
		t.Fatalf("second Version: %v", err)
	}
	if n := len(fake.CallsFor("--version")); n != 1 {
		t.Errorf("--version was executed %d times, want 1 (result should be cached)", n)
	}
}

func TestExecClientInfo(t *testing.T) {
	t.Parallel()

	c := newClient(t, testutil.NewFakeLimactl())
	info, err := c.Info(context.Background())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Version != "2.2.0" || info.HostOS != "darwin" {
		t.Errorf("info = %+v", info)
	}
	// Internal composition fragments must be filtered out.
	for _, n := range info.TemplateNames() {
		if strings.HasPrefix(n, "_") {
			t.Errorf("internal template %q leaked into TemplateNames", n)
		}
	}
}

func TestExecClientListAndInspect(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	fake.Seed(testutil.FakeInstance{Name: "alpha", Status: "Running"})
	fake.Seed(testutil.FakeInstance{Name: "beta", Status: "Stopped"})
	c := newClient(t, fake)

	list, err := c.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("List returned %d instances, want 2", len(list))
	}

	inst, err := c.Inspect(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if inst.Status() != lima.StatusRunning {
		t.Errorf("status = %q, want running", inst.Status())
	}
}

func TestExecClientListEmptyHome(t *testing.T) {
	t.Parallel()

	// An empty LIMA_HOME warns on stderr but exits 0; that must read as "no
	// instances", not as an error.
	c := newClient(t, testutil.NewFakeLimactl())
	list, err := c.List(context.Background())
	if err != nil {
		t.Fatalf("List on an empty home returned error: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("List returned %d instances, want 0", len(list))
	}
}

func TestExecClientInspectNotFound(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	fake.Seed(testutil.FakeInstance{Name: "alpha"})
	c := newClient(t, fake)

	_, err := c.Inspect(context.Background(), "missing")
	if !errors.Is(err, lima.ErrNotFound) {
		t.Errorf("Inspect error = %v, want ErrNotFound", err)
	}
	if !lima.IsNotFound(err) {
		t.Error("IsNotFound did not recognise the error")
	}

	// Inspect must resolve absence by listing everything, so Lima is never
	// asked about a name that might not exist.
	for _, call := range fake.CallsFor("list") {
		for _, a := range call.Args {
			if a == "missing" {
				t.Errorf("Inspect passed the instance name to limactl: %v", call.Args)
			}
		}
	}
}

func TestExecClientPassesLimaHome(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	c := newClient(t, fake, func(o *lima.Options) { o.Home = "/tmp/custom-lima" })

	if _, err := c.List(context.Background()); err != nil {
		t.Fatalf("List: %v", err)
	}

	calls := fake.Calls()
	if len(calls) == 0 {
		t.Fatal("no invocations recorded")
	}
	got, ok := calls[0].EnvValue("LIMA_HOME")
	if !ok {
		t.Fatal("LIMA_HOME was not set in the command environment")
	}
	if got != "/tmp/custom-lima" {
		t.Errorf("LIMA_HOME = %q, want /tmp/custom-lima", got)
	}
	if c.Home() != "/tmp/custom-lima" {
		t.Errorf("Home() = %q", c.Home())
	}
}

func TestExecClientMergesEnvironment(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	c := newClient(t, fake, func(o *lima.Options) {
		o.Env = map[string]string{"LIMA_TEST_VAR": "provider-value"}
	})
	if _, err := c.List(context.Background()); err != nil {
		t.Fatalf("List: %v", err)
	}

	got, ok := fake.Calls()[0].EnvValue("LIMA_TEST_VAR")
	if !ok || got != "provider-value" {
		t.Errorf("LIMA_TEST_VAR = %q (set: %v), want provider-value", got, ok)
	}
	// The inherited environment must still be present.
	if _, ok := fake.Calls()[0].EnvValue("PATH"); !ok {
		t.Error("PATH was dropped from the command environment")
	}
}

func TestExecClientCreateWritesDocumentAndCleansUp(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	c := newClient(t, fake)

	doc := []byte("base:\n- url: template:ubuntu\ncpus: 4\n")
	if err := c.Create(context.Background(), lima.CreateRequest{Name: "dev", Document: doc}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// The fake read the temporary file back, proving the document reached
	// Lima intact.
	if fake.LastDocument != string(doc) {
		t.Errorf("document passed to limactl = %q, want %q", fake.LastDocument, doc)
	}

	calls := fake.CallsFor("create")
	if len(calls) != 1 {
		t.Fatalf("create was called %d times, want 1", len(calls))
	}
	if !calls[0].Arg("--name=dev") {
		t.Errorf("create args missing --name=dev: %v", calls[0].Args)
	}
	if !calls[0].Arg("--tty=false") {
		t.Errorf("create args missing --tty=false: %v", calls[0].Args)
	}

	// The temporary file must be gone once the call returns.
	path := calls[0].Args[len(calls[0].Args)-1]
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("temporary file %s still exists after Create", path)
	}
}

func TestExecClientCreateRemovesTempFileOnFailure(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	fake.CreateFailsFor = map[string]string{"dev": "something broke"}
	c := newClient(t, fake)

	err := c.Create(context.Background(), lima.CreateRequest{Name: "dev", Document: []byte("cpus: 1\n")})
	if err == nil {
		t.Fatal("Create succeeded, want an error")
	}

	calls := fake.CallsFor("create")
	path := calls[0].Args[len(calls[0].Args)-1]
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Errorf("temporary file %s survived a failed Create", path)
	}
}

func TestExecClientTempFilePermissions(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	var mode os.FileMode
	fake.ReadFile = func(path string) ([]byte, error) {
		info, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		mode = info.Mode().Perm()
		return os.ReadFile(path)
	}

	c, err := lima.NewExecClient(lima.Options{Binary: testutil.StubBinary(t), Runner: fake})
	if err != nil {
		t.Fatalf("NewExecClient: %v", err)
	}
	if err := c.Create(context.Background(), lima.CreateRequest{
		Name:     "dev",
		Document: []byte("provision:\n- script: secret\n"),
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// The document can contain provisioning scripts, so it must not be
	// world- or group-readable.
	if mode&0o077 != 0 {
		t.Errorf("temporary file mode = %04o, want no group or other access", mode)
	}
}

func TestExecClientCreateDuplicateName(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	fake.Seed(testutil.FakeInstance{Name: "dev"})
	c := newClient(t, fake)

	err := c.Create(context.Background(), lima.CreateRequest{Name: "dev", Document: []byte("cpus: 1\n")})
	if !errors.Is(err, lima.ErrAlreadyExists) {
		t.Errorf("Create error = %v, want ErrAlreadyExists", err)
	}
}

func TestExecClientStartStopDelete(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	fake.Seed(testutil.FakeInstance{Name: "dev", Status: "Stopped"})
	c := newClient(t, fake)
	ctx := context.Background()

	if err := c.Start(ctx, "dev"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if inst, _ := fake.Get("dev"); inst.Status != "Running" {
		t.Errorf("after Start status = %q, want Running", inst.Status)
	}

	if err := c.Stop(ctx, "dev"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if inst, _ := fake.Get("dev"); inst.Status != "Stopped" {
		t.Errorf("after Stop status = %q, want Stopped", inst.Status)
	}

	if err := c.Delete(ctx, "dev"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := fake.Get("dev"); ok {
		t.Error("instance still present after Delete")
	}
}

func TestExecClientStopAlreadyStoppedFails(t *testing.T) {
	t.Parallel()

	// Lima refuses to stop a stopped instance. The adapter surfaces that
	// faithfully; guarding on status is the Service layer's job.
	fake := testutil.NewFakeLimactl()
	fake.Seed(testutil.FakeInstance{Name: "dev", Status: "Stopped"})
	c := newClient(t, fake)

	err := c.Stop(context.Background(), "dev")
	if err == nil {
		t.Fatal("Stop on a stopped instance succeeded, want the error Lima returns")
	}
	if !strings.Contains(err.Error(), "expected status") {
		t.Errorf("error = %q, want Lima's status refusal", err)
	}
}

func TestExecClientDeleteAbsentSucceeds(t *testing.T) {
	t.Parallel()

	// Lima exits 0 for a missing instance, which makes delete idempotent.
	c := newClient(t, testutil.NewFakeLimactl())
	if err := c.Delete(context.Background(), "nope"); err != nil {
		t.Errorf("Delete of an absent instance returned error: %v", err)
	}
}

func TestExecClientDeleteProtected(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	fake.Seed(testutil.FakeInstance{Name: "dev", Protected: true})
	c := newClient(t, fake)

	err := c.Delete(context.Background(), "dev")
	if !errors.Is(err, lima.ErrProtected) {
		t.Errorf("Delete error = %v, want ErrProtected", err)
	}
}

func TestExecClientProtectUnprotect(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	fake.Seed(testutil.FakeInstance{Name: "dev"})
	c := newClient(t, fake)
	ctx := context.Background()

	if err := c.Protect(ctx, "dev"); err != nil {
		t.Fatalf("Protect: %v", err)
	}
	if inst, _ := fake.Get("dev"); !inst.Protected {
		t.Error("Protect did not set the flag")
	}
	if err := c.Unprotect(ctx, "dev"); err != nil {
		t.Fatalf("Unprotect: %v", err)
	}
	if inst, _ := fake.Get("dev"); inst.Protected {
		t.Error("Unprotect did not clear the flag")
	}
}

func TestExecClientValidate(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	c := newClient(t, fake)

	if err := c.Validate(context.Background(), []byte("cpus: 2\n")); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if fake.LastDocument != "cpus: 2\n" {
		t.Errorf("validated document = %q", fake.LastDocument)
	}

	calls := fake.CallsFor("validate")
	path := calls[0].Args[len(calls[0].Args)-1]
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("temporary file %s survived Validate", path)
	}
}

func TestExecClientCommandErrorIsStructured(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	fake.Script(testutil.Scripted{
		Command:  "start",
		Stderr:   "time=\"...\" level=fatal msg=\"could not boot\"\n",
		ExitCode: 3,
	})
	fake.Seed(testutil.FakeInstance{Name: "dev"})
	c := newClient(t, fake)

	err := c.Start(context.Background(), "dev")
	var ce *lima.CommandError
	if !errors.As(err, &ce) {
		t.Fatalf("error %v is not a CommandError", err)
	}
	if ce.ExitCode != 3 {
		t.Errorf("ExitCode = %d, want 3", ce.ExitCode)
	}
	if ce.Message() != "could not boot" {
		t.Errorf("Message() = %q, want could not boot", ce.Message())
	}
	if !strings.Contains(strings.Join(ce.Args, " "), "start") {
		t.Errorf("Args = %v, want them to include start", ce.Args)
	}
}

func TestExecClientRespectsContextCancellation(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	fake.Seed(testutil.FakeInstance{Name: "dev"})
	fake.Script(testutil.Scripted{Command: "start", Delay: 5 * time.Second})
	c := newClient(t, fake)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := c.Start(ctx, "dev")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Start error = %v, want DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("Start took %s to honour cancellation", elapsed)
	}
}

func TestExecClientRejectsUnexpectedSubcommand(t *testing.T) {
	t.Parallel()

	// A guard on the fake: if the provider ever invents a subcommand, tests
	// must fail loudly instead of silently passing.
	fake := testutil.NewFakeLimactl()
	stdout, stderr, code, err := fake.Run(context.Background(), "limactl", []string{"teleport", "dev"}, nil)
	if err != nil {
		t.Fatalf("fake returned a transport error: %v", err)
	}
	if code == 0 {
		t.Errorf("unexpected subcommand exited 0 with stdout %q", stdout)
	}
	if !strings.Contains(stderr, "unexpected subcommand") {
		t.Errorf("stderr = %q, want it to flag the unexpected subcommand", stderr)
	}
}
