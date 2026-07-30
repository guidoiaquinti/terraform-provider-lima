package lima

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "testutil", "fixtures", name))
	if err != nil {
		t.Fatalf("reading fixture %s: %v", name, err)
	}
	return string(b)
}

func TestParseInstancesRunningFixture(t *testing.T) {
	t.Parallel()

	got, err := ParseInstancesString(loadFixture(t, "list_running.json"))
	if err != nil {
		t.Fatalf("ParseInstances returned error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d instances, want 1", len(got))
	}

	inst := got[0]
	checks := []struct {
		field string
		got   any
		want  any
	}{
		{"Name", inst.Name, "tfdisco"},
		{"Hostname", inst.Hostname, "lima-tfdisco"},
		{"RawStatus", inst.RawStatus, "Running"},
		{"Status", inst.Status(), StatusRunning},
		{"VMType", inst.VMType, "vz"},
		{"Arch", inst.Arch, "aarch64"},
		{"CPUs", inst.CPUs, int64(2)},
		// Lima reports memory and disk as raw byte counts at the top level.
		{"MemoryBytes", inst.MemoryBytes, int64(1073741824)},
		{"DiskBytes", inst.DiskBytes, int64(8589934592)},
		{"SSHLocalPort", inst.SSHLocalPort, int64(61627)},
		{"SSHAddress", inst.SSHAddress, "127.0.0.1"},
		{"Protected", inst.Protected, false},
		{"LimaVersion", inst.LimaVersion, "2.2.0"},
		// These four use capitalised keys in Lima's output.
		{"HostOS", inst.HostOS, "darwin"},
		{"HostArch", inst.HostArch, "aarch64"},
		{"LimaHome", inst.LimaHome, "/private/tmp/ltfdisco"},
		// Inside config, the same values are IEC strings rather than bytes.
		{"Config.Memory", inst.Config.Memory, "1GiB"},
		{"Config.Disk", inst.Config.Disk, "8GiB"},
		{"Config.User.Name", inst.Config.User.Name, "alice"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.field, c.got, c.want)
		}
	}
}

func TestInstanceSSHDerivedFromInspect(t *testing.T) {
	t.Parallel()

	got, err := ParseInstancesString(loadFixture(t, "list_running.json"))
	if err != nil {
		t.Fatalf("ParseInstances returned error: %v", err)
	}

	// SSH details come from the list output because `limactl show-ssh` is
	// deprecated and has no machine-readable format.
	ssh := got[0].SSH()
	if ssh.Address != "127.0.0.1" {
		t.Errorf("Address = %q, want 127.0.0.1", ssh.Address)
	}
	if ssh.Port != 61627 {
		t.Errorf("Port = %d, want 61627", ssh.Port)
	}
	if ssh.User != "alice" {
		t.Errorf("User = %q, want alice", ssh.User)
	}
	if !strings.HasSuffix(ssh.ConfigFile, "/ssh.config") {
		t.Errorf("ConfigFile = %q, want a path ending in /ssh.config", ssh.ConfigFile)
	}
	if ssh.Hostname != "lima-tfdisco" {
		t.Errorf("Hostname = %q, want lima-tfdisco", ssh.Hostname)
	}
}

func TestParseInstancesNDJSON(t *testing.T) {
	t.Parallel()

	// Lima emits one JSON object per line with no enclosing array. This is
	// the single most important parsing property; a slice unmarshal would
	// fail outright here.
	got, err := ParseInstancesString(loadFixture(t, "list_multiple.json"))
	if err != nil {
		t.Fatalf("ParseInstances returned error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d instances, want 2", len(got))
	}
	if got[0].Name != "second" || got[1].Name != "tfdisco" {
		t.Errorf("names = %q, %q; want second, tfdisco", got[0].Name, got[1].Name)
	}
	if got[0].Status() != StatusStopped {
		t.Errorf("second status = %q, want stopped", got[0].Status())
	}
	if got[1].Status() != StatusRunning {
		t.Errorf("tfdisco status = %q, want running", got[1].Status())
	}
}

func TestParseInstances(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		input     string
		wantCount int
		wantErr   bool
	}{
		{
			name:      "empty output means no instances",
			input:     "",
			wantCount: 0,
		},
		{
			name:      "whitespace only",
			input:     "\n\n",
			wantCount: 0,
		},
		{
			// Forward compatibility: a future Lima adding fields must not
			// break the provider.
			name:      "unknown fields are tolerated",
			input:     `{"name":"a","status":"Running","brandNewField":{"nested":true},"anotherOne":[1,2]}`,
			wantCount: 1,
		},
		{
			name:      "pretty printed multi-line object",
			input:     "{\n  \"name\": \"a\",\n  \"status\": \"Stopped\"\n}\n",
			wantCount: 1,
		},
		{
			name:    "malformed json is rejected",
			input:   `{"name":"a",`,
			wantErr: true,
		},
		{
			// A required field being absent is a real parse failure, not
			// something to paper over.
			name:    "entry without a name is rejected",
			input:   `{"status":"Running"}`,
			wantErr: true,
		},
		{
			name:    "wrong type for a required field",
			input:   `{"name":123}`,
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseInstancesString(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseInstances(%q) = %v, want error", tc.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseInstances(%q) returned unexpected error: %v", tc.input, err)
			}
			if len(got) != tc.wantCount {
				t.Errorf("got %d instances, want %d", len(got), tc.wantCount)
			}
		})
	}
}

func TestNormalizeStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		raw  string
		want Status
	}{
		{"Running", StatusRunning},
		{"Stopped", StatusStopped},
		{"Broken", StatusBroken},
		{"Uninitialized", StatusCreating},
		{"Installing", StatusCreating},
		{"", StatusUnknown},
		// Casing must not matter.
		{"running", StatusRunning},
		{"  Stopped  ", StatusStopped},
		// A status Lima has not shipped yet must degrade, not break.
		{"Hibernating", StatusUnknown},
	}

	for _, tc := range tests {
		t.Run(tc.raw, func(t *testing.T) {
			t.Parallel()
			if got := NormalizeStatus(tc.raw); got != tc.want {
				t.Errorf("NormalizeStatus(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestParseHostInfo(t *testing.T) {
	t.Parallel()

	got, err := ParseHostInfo(strings.NewReader(loadFixture(t, "info.json")))
	if err != nil {
		t.Fatalf("ParseHostInfo returned error: %v", err)
	}

	if got.Version != "2.2.0" {
		t.Errorf("Version = %q, want 2.2.0", got.Version)
	}
	if got.HostOS != "darwin" || got.HostArch != "aarch64" {
		t.Errorf("host = %s/%s, want darwin/aarch64", got.HostOS, got.HostArch)
	}
	if got.LimaHome != "/Users/alice/.lima" {
		t.Errorf("LimaHome = %q", got.LimaHome)
	}
	if len(got.VMTypes) != 3 {
		t.Errorf("VMTypes = %v, want 3 entries", got.VMTypes)
	}

	// Internal composition fragments must not be offered as templates.
	names := got.TemplateNames()
	for _, n := range names {
		if strings.HasPrefix(n, "_") {
			t.Errorf("UserTemplates leaked internal template %q", n)
		}
	}
	if !Contains(names, "ubuntu") || !Contains(names, "docker") {
		t.Errorf("template names = %v, want ubuntu and docker", names)
	}
}

func TestParseHostInfoErrors(t *testing.T) {
	t.Parallel()

	tests := []struct{ name, input string }{
		{"malformed", `{"version":`},
		{"missing version", `{"hostOS":"darwin"}`},
		{"not an object", `["a"]`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := ParseHostInfo(strings.NewReader(tc.input)); err == nil {
				t.Errorf("ParseHostInfo(%q) succeeded, want error", tc.input)
			}
		})
	}
}

func TestHasMountSubsetMatching(t *testing.T) {
	t.Parallel()

	// Mirrors what Lima 2.2.0 actually resolved for a configuration declaring
	// one mount: the declared entry, plus one the base template contributed.
	cfg := InstanceConfigView{Mounts: []MountView{
		{Location: "/private/tmp", MountPoint: "/workspace", Writable: true},
		{Location: "/Users/alice", MountPoint: "/Users/alice", Writable: false},
	}}

	tests := []struct {
		name       string
		location   string
		mountPoint string
		writable   bool
		want       bool
	}{
		{name: "exact match", location: "/private/tmp", mountPoint: "/workspace", writable: true, want: true},
		{
			// An unset mount_point means "whatever Lima chose".
			name: "unspecified mount point matches", location: "/private/tmp", writable: true, want: true,
		},
		{
			// macOS reports /tmp as /private/tmp, so a configuration written
			// either way must match rather than reporting false drift.
			name: "tmp and private tmp are the same path", location: "/tmp", mountPoint: "/workspace", writable: true, want: true,
		},
		{name: "trailing slash is irrelevant", location: "/private/tmp/", mountPoint: "/workspace", writable: true, want: true},
		{
			// A template-contributed mount is not drift, and is findable.
			name: "template mount is present", location: "/Users/alice", writable: false, want: true,
		},
		{name: "writability differs", location: "/private/tmp", mountPoint: "/workspace", writable: false, want: false},
		{name: "mount point differs", location: "/private/tmp", mountPoint: "/elsewhere", writable: true, want: false},
		{name: "location absent", location: "/nowhere", writable: true, want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := cfg.HasMount(tc.location, tc.mountPoint, tc.writable); got != tc.want {
				t.Errorf("HasMount(%q, %q, %v) = %v, want %v", tc.location, tc.mountPoint, tc.writable, got, tc.want)
			}
		})
	}
}

func TestHasPortForwardIgnoresLimaDefaults(t *testing.T) {
	t.Parallel()

	// Lima fills in guestIP and hostIP; matching on them would report drift
	// for every forward.
	cfg := InstanceConfigView{PortForwards: []PortForwardView{
		{GuestPort: 8080, HostPort: 18080, Proto: "tcp", GuestIP: "127.0.0.1", HostIP: "127.0.0.1"},
		{GuestPort: 53, HostPort: 5353, Proto: "udp", GuestIP: "127.0.0.1", HostIP: "127.0.0.1"},
	}}

	tests := []struct {
		name                string
		guestPort, hostPort int64
		proto               string
		want                bool
	}{
		{name: "exact match", guestPort: 8080, hostPort: 18080, proto: "tcp", want: true},
		{
			// An unset host port means Lima chose one.
			name: "unspecified host port matches", guestPort: 8080, proto: "tcp", want: true,
		},
		{
			// The schema defaults protocol to tcp, so an empty value must too.
			name: "empty protocol defaults to tcp", guestPort: 8080, hostPort: 18080, want: true,
		},
		{name: "udp forward", guestPort: 53, hostPort: 5353, proto: "udp", want: true},
		{name: "case insensitive protocol", guestPort: 53, hostPort: 5353, proto: "UDP", want: true},
		{name: "protocol mismatch", guestPort: 53, hostPort: 5353, proto: "tcp", want: false},
		{name: "host port mismatch", guestPort: 8080, hostPort: 9999, proto: "tcp", want: false},
		{name: "guest port absent", guestPort: 9090, proto: "tcp", want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := cfg.HasPortForward(tc.guestPort, tc.hostPort, tc.proto); got != tc.want {
				t.Errorf("HasPortForward(%d, %d, %q) = %v, want %v", tc.guestPort, tc.hostPort, tc.proto, got, tc.want)
			}
		})
	}
}

func TestEmptyConfigViewReportsNothingPresent(t *testing.T) {
	t.Parallel()

	// An instance Lima has not resolved yet must not claim to have anything.
	var cfg InstanceConfigView
	if cfg.HasMount("/tmp", "", false) {
		t.Error("an empty config view reported a mount")
	}
	if cfg.HasPortForward(80, 8080, "tcp") {
		t.Error("an empty config view reported a port forward")
	}
}

// Every status the provider advertises must be one it can actually produce.
//
// `starting` and `stopping` were in AllStatuses and in both schema descriptions,
// but nothing mapped to them: Lima defines Running, Stopped, Uninitialized,
// Installing, Broken and an empty status (CLI contract §4.7). A user waiting for
// `status == "starting"` would therefore wait forever, and a reader of the docs
// would reasonably expect a transition the provider never reports.
func TestEveryAdvertisedStatusIsReachable(t *testing.T) {
	t.Parallel()

	// Every raw value Lima is known to emit, plus one it does not, so the
	// unknown fallback counts as reachable too.
	reachable := map[Status]bool{}
	for _, raw := range []string{
		"Running", "Stopped", "Broken", "Uninitialized", "Installing", "", "Hibernating",
	} {
		reachable[NormalizeStatus(raw)] = true
	}

	for _, s := range AllStatuses {
		if !reachable[s] {
			t.Errorf("AllStatuses advertises %q, but NormalizeStatus can never return it", s)
		}
	}
}
