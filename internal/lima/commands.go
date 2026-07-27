package lima

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Argument construction lives here so command shape is unit-testable without
// executing anything.

// nonInteractive is passed to every mutating command. Lima defaults --tty to
// true when stdout is a terminal, which would open $EDITOR or prompt; both
// would hang Terraform forever.
const nonInteractive = "--tty=false"

func versionArgs() []string { return []string{"--version"} }

func infoArgs() []string { return []string{"info"} }

// listArgs builds the inspect/list invocation. --all-fields is required for
// protected, sshAddress and limaVersion.
//
// Passing no name lists everything and never fails on absence, which is why
// the lifecycle layer prefers it over a name-scoped call.
func listArgs(names ...string) []string {
	args := []string{"list", "--format", "json", "--all-fields"}
	return append(args, names...)
}

func validateArgs(path string) []string { return []string{"validate", path} }

// templateCopyArgs resolves a template reference to its effective YAML on
// stdout.
//
// --fill applies Lima's defaults, which is what makes the result comparable to
// an instance's resolved configuration. --embed-all is deliberately not used:
// it inlines external dependencies, which is slower and changes nothing about
// the image list.
func templateCopyArgs(ref string) []string {
	return []string{"template", "copy", "--fill", ref, "-"}
}

func createArgs(name, path string) []string {
	return []string{"create", nonInteractive, "--name=" + name, path}
}

// editArgs builds a non-interactive `limactl edit`.
//
// Only the fields the caller wants changed are passed; every other field in
// the instance's configuration is preserved by Lima. Passing no field at all
// would be a no-op edit, so callers must check first.
//
// No editor is opened because explicit flags are supplied, and --tty=false
// makes that guarantee independent of how the process was spawned.
func editArgs(name string, e EditRequest) ([]string, error) {
	args := []string{"edit", nonInteractive}

	if e.CPUs > 0 {
		args = append(args, "--cpus", strconv.FormatInt(e.CPUs, 10))
	}
	if e.MemoryBytes > 0 {
		v, err := GiBFlagValue(e.MemoryBytes)
		if err != nil {
			return nil, fmt.Errorf("memory: %w", err)
		}
		args = append(args, "--memory", v)
	}
	if e.DiskBytes > 0 {
		v, err := GiBFlagValue(e.DiskBytes)
		if err != nil {
			return nil, fmt.Errorf("disk: %w", err)
		}
		args = append(args, "--disk", v)
	}

	// Mounts and port forwards have no usable edit flags: --mount-only
	// discards template-contributed mounts and cannot express a guest mount
	// point, and there is no --port-forward at all. A yq expression carrying
	// a JSON structure is the supported path. See the CLI contract §12.7.
	if e.Mounts != nil {
		expr, err := setListExpr("mounts", mountsForSet(*e.Mounts))
		if err != nil {
			return nil, fmt.Errorf("mounts: %w", err)
		}
		args = append(args, "--set", expr)
	}
	if e.PortForwards != nil {
		expr, err := setListExpr("portForwards", portForwardsForSet(*e.PortForwards))
		if err != nil {
			return nil, fmt.Errorf("port forwards: %w", err)
		}
		args = append(args, "--set", expr)
	}

	if e.AdditionalDisks != nil {
		expr, err := setListExpr("additionalDisks", disksForSet(*e.AdditionalDisks))
		if err != nil {
			return nil, fmt.Errorf("additional disks: %w", err)
		}
		args = append(args, "--set", expr)
	}

	if len(args) == 2 {
		return nil, fmt.Errorf("edit requested with no fields to change")
	}
	return append(args, name), nil
}

// setListExpr builds a `.<field> = <json>` yq expression.
//
// The value is rendered with encoding/json, so quotes, spaces and any other
// awkward character in a path are escaped correctly and cannot break out of
// the literal. Verified against Lima 2.2.0 with a path containing a double
// quote; see the CLI contract §12.7.
func setListExpr(field string, value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encoding %s: %w", field, err)
	}
	return "." + field + " = " + string(encoded), nil
}

// setMount is the JSON shape Lima expects for a resolved mount.
//
// It is separate from Mount because that type carries YAML tags for document
// generation while yq needs JSON. Keeping the fields identical means the
// conversion below is a plain cast, so the two cannot drift apart without a
// compile error.
type setMount struct {
	Location   string `json:"location"`
	MountPoint string `json:"mountPoint,omitempty"`
	Writable   bool   `json:"writable"`
}

func mountsForSet(mounts []Mount) []setMount {
	out := make([]setMount, 0, len(mounts))
	for _, m := range mounts {
		out = append(out, setMount(m))
	}
	return out
}

// setPortForward is the JSON shape Lima expects for a resolved port forward.
type setPortForward struct {
	GuestPort int64  `json:"guestPort"`
	HostPort  int64  `json:"hostPort,omitempty"`
	Proto     string `json:"proto,omitempty"`
	GuestIP   string `json:"guestIP,omitempty"`
	HostIP    string `json:"hostIP,omitempty"`
}

// setAdditionalDisk is the JSON shape Lima expects for an attached disk.
type setAdditionalDisk struct {
	Name string `json:"name"`
}

func disksForSet(disks []AdditionalDisk) []setAdditionalDisk {
	out := make([]setAdditionalDisk, 0, len(disks))
	for _, d := range disks {
		out = append(out, setAdditionalDisk(d))
	}
	return out
}

func portForwardsForSet(forwards []PortForward) []setPortForward {
	out := make([]setPortForward, 0, len(forwards))
	for _, p := range forwards {
		out = append(out, setPortForward{
			GuestPort: p.GuestPort,
			HostPort:  p.HostPort,
			Proto:     p.Proto,
			GuestIP:   p.GuestIP,
			HostIP:    p.HostIP,
		})
	}
	return out
}

func startArgs(name string) []string { return []string{"start", nonInteractive, name} }

func stopArgs(name string) []string { return []string{"stop", nonInteractive, name} }

// deleteArgs uses --force so a running instance is torn down in one call.
// Force does not bypass protection: Lima checks protection first, which the
// contract doc records and an acceptance test covers.
func deleteArgs(name string) []string {
	return []string{"delete", nonInteractive, "--force", name}
}

func protectArgs(name string) []string { return []string{"protect", name} }

func unprotectArgs(name string) []string { return []string{"unprotect", name} }

// buildEnv composes the environment for a limactl invocation.
//
// Precedence, lowest to highest: inherited process environment, then LIMA_HOME
// when configured, then provider-supplied entries. Provider values win on
// collision, as specified.
func buildEnv(base []string, home string, extra map[string]string) []string {
	merged := make(map[string]string, len(base)+len(extra)+1)
	order := make([]string, 0, len(base)+len(extra)+1)

	put := func(k, v string) {
		if _, seen := merged[k]; !seen {
			order = append(order, k)
		}
		merged[k] = v
	}

	for _, kv := range base {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			continue
		}
		put(k, v)
	}
	if home != "" {
		put("LIMA_HOME", home)
	}
	// Deterministic ordering for keys the caller supplied via a map.
	keys := make([]string, 0, len(extra))
	for k := range extra {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		put(k, extra[k])
	}

	out := make([]string, 0, len(order))
	for _, k := range order {
		out = append(out, k+"="+merged[k])
	}
	return out
}

// ExpandPath resolves a leading ~ and returns a cleaned absolute path.
//
// Normalisation is deliberately conservative: symlinks are not resolved,
// because doing so would make a mount path in state differ from what the user
// wrote and produce a perpetual diff.
func ExpandPath(p string) (string, error) {
	if p == "" {
		return "", nil
	}
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("expanding %q: %w", p, err)
		}
		if p == "~" {
			p = home
		} else {
			p = filepath.Join(home, p[2:])
		}
	}
	if !filepath.IsAbs(p) {
		abs, err := filepath.Abs(p)
		if err != nil {
			return "", fmt.Errorf("resolving %q: %w", p, err)
		}
		p = abs
	}
	return filepath.Clean(p), nil
}

// redactedEnvKeys are never written to logs or diagnostics. Matching is on
// substring of the uppercased key, so LIMA_TOKEN and MY_API_KEY both match.
var redactedEnvKeys = []string{
	"TOKEN", "SECRET", "PASSWORD", "PASSWD", "KEY", "CREDENTIAL", "AUTH",
}

// RedactEnvValue reports whether an environment key's value must be hidden.
func RedactEnvValue(key string) bool {
	up := strings.ToUpper(key)
	for _, k := range redactedEnvKeys {
		if strings.Contains(up, k) {
			return true
		}
	}
	return false
}

// RedactArgs returns a loggable copy of an argument list.
//
// The only argument that can carry user content is the temporary file path
// passed to create/validate; the file itself may hold provisioning scripts, so
// the path is kept (it is not secret) but nothing is ever read back into logs.
// Any argument that looks like key=value with a sensitive key is masked.
func RedactArgs(args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		k, _, ok := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if ok && RedactEnvValue(k) {
			out[i] = strings.SplitN(a, "=", 2)[0] + "=(redacted)"
			continue
		}
		out[i] = a
	}
	return out
}

// Disk command arguments. `limactl disk list --json` emits NDJSON, the same
// shape as the instance list; see the CLI contract §13.
func diskListArgs() []string { return []string{"disk", "list", "--json"} }

func diskCreateArgs(req CreateDiskRequest) ([]string, error) {
	if req.SizeBytes <= 0 {
		return nil, fmt.Errorf("disk size must be greater than zero")
	}
	args := []string{"disk", "create", req.Name, "--size", FormatSize(req.SizeBytes)}
	if req.Format != "" {
		args = append(args, "--format", req.Format)
	}
	return args, nil
}

func diskResizeArgs(name string, sizeBytes int64) []string {
	return []string{"disk", "resize", name, "--size", FormatSize(sizeBytes)}
}

// diskDeleteArgs deliberately omits --force. Force exists to tear down a disk
// Lima considers busy, and the provider refuses that case explicitly rather
// than overriding a lock it cannot prove is stale.
func diskDeleteArgs(name string) []string {
	return []string{"disk", "delete", name}
}
