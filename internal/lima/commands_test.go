package lima

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCommandArguments(t *testing.T) {
	t.Parallel()

	// Argument shapes are asserted exactly. Every mutating command must carry
	// --tty=false, or Lima can open an editor and hang Terraform forever.
	tests := []struct {
		name string
		got  []string
		want []string
	}{
		{"version", versionArgs(), []string{"--version"}},
		{"info", infoArgs(), []string{"info"}},
		{
			"list without a name",
			listArgs(),
			[]string{"list", "--format", "json", "--all-fields"},
		},
		{
			"list with a name",
			listArgs("dev"),
			[]string{"list", "--format", "json", "--all-fields", "dev"},
		},
		{"validate", validateArgs("/tmp/x.yaml"), []string{"validate", "/tmp/x.yaml"}},
		{
			"create",
			createArgs("dev", "/tmp/x.yaml"),
			[]string{"create", "--tty=false", "--name=dev", "/tmp/x.yaml"},
		},
		{"start", startArgs("dev"), []string{"start", "--tty=false", "dev"}},
		{"stop", stopArgs("dev"), []string{"stop", "--tty=false", "dev"}},
		{"delete", deleteArgs("dev"), []string{"delete", "--tty=false", "--force", "dev"}},
		{"protect", protectArgs("dev"), []string{"protect", "dev"}},
		{"unprotect", unprotectArgs("dev"), []string{"unprotect", "dev"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if !reflect.DeepEqual(tc.got, tc.want) {
				t.Errorf("args = %v, want %v", tc.got, tc.want)
			}
		})
	}
}

func TestMutatingCommandsAreNonInteractive(t *testing.T) {
	t.Parallel()

	mutating := map[string][]string{
		"create": createArgs("dev", "/tmp/x.yaml"),
		"start":  startArgs("dev"),
		"stop":   stopArgs("dev"),
		"delete": deleteArgs("dev"),
	}
	for name, args := range mutating {
		found := false
		for _, a := range args {
			if a == "--tty=false" {
				found = true
			}
		}
		if !found {
			t.Errorf("%s args %v are missing --tty=false", name, args)
		}
	}
}

func TestArgumentsAreNotShellInterpreted(t *testing.T) {
	t.Parallel()

	// A hostile instance name must land in exactly one argv slot rather than
	// being split or expanded. Nothing here goes through a shell, so the
	// name is inert; this guards against a future refactor changing that.
	hostile := "dev; rm -rf /"
	args := deleteArgs(hostile)

	count := 0
	for _, a := range args {
		if a == hostile {
			count++
		}
	}
	if count != 1 {
		t.Errorf("hostile name did not survive as a single argument: %v", args)
	}
	if len(args) != 4 {
		t.Errorf("argument count = %d, want 4: %v", len(args), args)
	}
}

func TestBuildEnv(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		base  []string
		home  string
		extra map[string]string
		want  map[string]string
		// absent lists keys that must not be present at all.
		absent []string
	}{
		{
			name: "inherited environment passes through",
			base: []string{"PATH=/usr/bin", "HOME=/home/alice"},
			want: map[string]string{"PATH": "/usr/bin", "HOME": "/home/alice"},
		},
		{
			name: "home is injected as LIMA_HOME",
			base: []string{"PATH=/usr/bin"},
			home: "/tmp/lima",
			want: map[string]string{"LIMA_HOME": "/tmp/lima", "PATH": "/usr/bin"},
		},
		{
			name: "configured home overrides an inherited LIMA_HOME",
			base: []string{"LIMA_HOME=/inherited"},
			home: "/configured",
			want: map[string]string{"LIMA_HOME": "/configured"},
		},
		{
			name:  "provider environment overrides inherited values",
			base:  []string{"FOO=inherited", "PATH=/usr/bin"},
			extra: map[string]string{"FOO": "provider"},
			want:  map[string]string{"FOO": "provider", "PATH": "/usr/bin"},
		},
		{
			name:  "provider environment overrides the configured home",
			base:  []string{"PATH=/usr/bin"},
			home:  "/configured",
			extra: map[string]string{"LIMA_HOME": "/explicit"},
			want:  map[string]string{"LIMA_HOME": "/explicit"},
		},
		{
			name: "no home leaves LIMA_HOME unset",
			base: []string{"PATH=/usr/bin"},
			want: map[string]string{"PATH": "/usr/bin"},
			// Lima's own default must apply when the provider says nothing.
			absent: []string{"LIMA_HOME"},
		},
		{
			name: "malformed entries are dropped",
			base: []string{"PATH=/usr/bin", "NOEQUALS", "=novalue"},
			want: map[string]string{"PATH": "/usr/bin"},
		},
		{
			name: "empty values are preserved",
			base: []string{"EMPTY="},
			want: map[string]string{"EMPTY": ""},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := buildEnv(tc.base, tc.home, tc.extra)

			seen := map[string]string{}
			for _, kv := range got {
				k, v, _ := strings.Cut(kv, "=")
				if _, dup := seen[k]; dup {
					t.Errorf("key %q appears more than once in %v", k, got)
				}
				seen[k] = v
			}
			for k, want := range tc.want {
				if seen[k] != want {
					t.Errorf("env[%q] = %q, want %q", k, seen[k], want)
				}
			}
			for _, k := range tc.absent {
				if _, ok := seen[k]; ok {
					t.Errorf("env[%q] should not be set, got %q", k, seen[k])
				}
			}
		})
	}
}

func TestBuildEnvIsDeterministic(t *testing.T) {
	t.Parallel()

	base := []string{"PATH=/usr/bin", "HOME=/home/alice"}
	extra := map[string]string{"C": "3", "A": "1", "B": "2"}

	first := buildEnv(base, "/tmp/lima", extra)
	for i := 0; i < 50; i++ {
		if got := buildEnv(base, "/tmp/lima", extra); !reflect.DeepEqual(got, first) {
			t.Fatalf("buildEnv is not deterministic:\n%v\nvs\n%v", got, first)
		}
	}
}

func TestExpandPath(t *testing.T) {
	t.Parallel()

	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home directory available: %v", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"empty stays empty", "", ""},
		{"absolute path is unchanged", "/tmp/lima", "/tmp/lima"},
		{"tilde alone expands to home", "~", home},
		{"tilde prefix expands", "~/lima", filepath.Join(home, "lima")},
		{"redundant separators are cleaned", "/tmp//lima/", "/tmp/lima"},
		{"dot segments are cleaned", "/tmp/lima/../lima", "/tmp/lima"},
		{"relative paths become absolute", "rel", filepath.Join(cwd, "rel")},
		// A tilde that is not a home reference must be left alone.
		{"embedded tilde is literal", "/tmp/~backup", "/tmp/~backup"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ExpandPath(tc.input)
			if err != nil {
				t.Fatalf("ExpandPath(%q) returned error: %v", tc.input, err)
			}
			if got != tc.want {
				t.Errorf("ExpandPath(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestExpandPathIsIdempotent(t *testing.T) {
	t.Parallel()

	// Re-normalising an already-normalised path must not change it, or a
	// mount location would drift between plan and apply.
	for _, in := range []string{"/tmp/lima", "~/projects", "/a/b/../c"} {
		once, err := ExpandPath(in)
		if err != nil {
			t.Fatalf("ExpandPath(%q): %v", in, err)
		}
		twice, err := ExpandPath(once)
		if err != nil {
			t.Fatalf("ExpandPath(%q): %v", once, err)
		}
		if once != twice {
			t.Errorf("ExpandPath is not idempotent for %q: %q then %q", in, once, twice)
		}
	}
}

func TestRedactEnvValue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		key  string
		want bool
	}{
		{"GITHUB_TOKEN", true},
		{"MY_SECRET", true},
		{"password", true},
		{"API_KEY", true},
		{"AWS_CREDENTIAL_FILE", true},
		{"AUTH_HEADER", true},
		{"PATH", false},
		{"LIMA_HOME", false},
		{"HOME", false},
	}

	for _, tc := range tests {
		t.Run(tc.key, func(t *testing.T) {
			t.Parallel()
			if got := RedactEnvValue(tc.key); got != tc.want {
				t.Errorf("RedactEnvValue(%q) = %v, want %v", tc.key, got, tc.want)
			}
		})
	}
}

func TestRedactArgs(t *testing.T) {
	t.Parallel()

	got := RedactArgs([]string{"create", "--tty=false", "--name=dev", "--token=hunter2", "/tmp/x.yaml"})
	joined := strings.Join(got, " ")

	if strings.Contains(joined, "hunter2") {
		t.Errorf("secret survived redaction: %v", got)
	}
	if !strings.Contains(joined, "--token=(redacted)") {
		t.Errorf("redaction marker missing: %v", got)
	}
	// Non-sensitive arguments must stay legible so diagnostics remain useful.
	for _, want := range []string{"--name=dev", "/tmp/x.yaml", "--tty=false"} {
		if !strings.Contains(joined, want) {
			t.Errorf("useful argument %q was lost: %v", want, got)
		}
	}
}

func TestEditArgs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		req     EditRequest
		want    []string
		wantErr string
	}{
		{
			name: "cpus only",
			req:  EditRequest{CPUs: 4},
			want: []string{"edit", "--tty=false", "--cpus", "4", "dev"},
		},
		{
			name: "memory only",
			req:  EditRequest{MemoryBytes: 8 << 30},
			want: []string{"edit", "--tty=false", "--memory", "8", "dev"},
		},
		{
			name: "disk only",
			req:  EditRequest{DiskBytes: 100 << 30},
			want: []string{"edit", "--tty=false", "--disk", "100", "dev"},
		},
		{
			// Field order is fixed so the command is reproducible.
			name: "all three",
			req:  EditRequest{CPUs: 2, MemoryBytes: 4 << 30, DiskBytes: 50 << 30},
			want: []string{"edit", "--tty=false", "--cpus", "2", "--memory", "4", "--disk", "50", "dev"},
		},
		{
			name: "non-integer GiB memory",
			req:  EditRequest{MemoryBytes: 1000 << 20},
			want: []string{"edit", "--tty=false", "--memory", "0.9765625", "dev"},
		},
		{
			// An empty request would be a pointless invocation; callers must
			// check first.
			name:    "nothing to change",
			req:     EditRequest{},
			wantErr: "no fields to change",
		},
		{
			name:    "unrepresentable memory",
			req:     EditRequest{MemoryBytes: (1 << 30) + 1},
			wantErr: "memory:",
		},
		{
			name:    "unrepresentable disk",
			req:     EditRequest{DiskBytes: (1 << 30) + 1},
			wantErr: "disk:",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := editArgs("dev", tc.req)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("editArgs = %v, want error containing %q", got, tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("error = %q, want it to contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("editArgs returned error: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("editArgs = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestEditIsNonInteractive(t *testing.T) {
	t.Parallel()

	// Without --tty=false, Lima can open $EDITOR and hang Terraform forever.
	args, err := editArgs("dev", EditRequest{CPUs: 2})
	if err != nil {
		t.Fatalf("editArgs: %v", err)
	}
	found := false
	for _, a := range args {
		if a == "--tty=false" {
			found = true
		}
	}
	if !found {
		t.Errorf("edit args %v are missing --tty=false", args)
	}
}

func TestEditRequestIsEmpty(t *testing.T) {
	t.Parallel()

	if !(EditRequest{}).IsEmpty() {
		t.Error("a zero EditRequest should be empty")
	}
	for _, req := range []EditRequest{
		{CPUs: 1}, {MemoryBytes: 1}, {DiskBytes: 1},
	} {
		if req.IsEmpty() {
			t.Errorf("%+v should not be empty", req)
		}
	}
}
