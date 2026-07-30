package lima

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

// Status is the normalised instance status exposed by the provider.
type Status string

// The normalised status vocabulary. Lima's own values are mapped onto these;
// anything unrecognised becomes StatusUnknown while raw_status keeps the
// original so a new Lima status cannot break a refresh.
const (
	StatusRunning  Status = "running"
	StatusStopped  Status = "stopped"
	StatusCreating Status = "creating"
	StatusBroken   Status = "broken"
	StatusUnknown  Status = "unknown"
)

// AllStatuses lists every value the provider may report, for documentation and
// schema description purposes.
//
// It holds only values NormalizeStatus can actually return. Earlier revisions
// also advertised "starting" and "stopping", which nothing mapped to: Lima
// reports Running, Stopped, Uninitialized, Installing, Broken or an empty status
// (CLI contract §4.7), so a configuration waiting for "starting" waited forever.
// A transition Lima introduces later arrives as "unknown" with the original in
// raw_status, which is the honest reading of a value this version does not know.
var AllStatuses = []Status{
	StatusRunning, StatusStopped, StatusCreating, StatusBroken, StatusUnknown,
}

// rawStatusMap maps Lima's status strings onto the normalised vocabulary.
// Keys are lowercased before lookup so casing changes are tolerated.
//
// "Uninitialized" and "Installing" both describe an instance that Lima has
// registered but not finished materialising, which is what the provider means
// by "creating".
var rawStatusMap = map[string]Status{
	"running":       StatusRunning,
	"stopped":       StatusStopped,
	"broken":        StatusBroken,
	"uninitialized": StatusCreating,
	"installing":    StatusCreating,
	"":              StatusUnknown,
}

// NormalizeStatus maps a Lima status string onto the provider vocabulary.
func NormalizeStatus(raw string) Status {
	if s, ok := rawStatusMap[strings.ToLower(strings.TrimSpace(raw))]; ok {
		return s
	}
	return StatusUnknown
}

// Instance is the provider's view of one Lima instance, decoded from
// `limactl list --format json --all-fields`.
//
// Unknown fields are ignored by encoding/json, which is what lets the provider
// tolerate Lima adding keys. Only Name is treated as required.
type Instance struct {
	Name          string `json:"name"`
	Hostname      string `json:"hostname"`
	RawStatus     string `json:"status"`
	Dir           string `json:"dir"`
	VMType        string `json:"vmType"`
	Arch          string `json:"arch"`
	CPUs          int64  `json:"cpus"`
	MemoryBytes   int64  `json:"memory"`
	DiskBytes     int64  `json:"disk"`
	SSHLocalPort  int64  `json:"sshLocalPort"`
	SSHConfigFile string `json:"sshConfigFile"`
	SSHAddress    string `json:"sshAddress"`
	Protected     bool   `json:"protected"`
	LimaVersion   string `json:"limaVersion"`
	HostAgentPID  int64  `json:"hostAgentPID"`
	DriverPID     int64  `json:"driverPID"`

	// Lima emits these four with capitalised keys, unlike every other field.
	HostOS       string `json:"HostOS"`
	HostArch     string `json:"HostArch"`
	LimaHome     string `json:"LimaHome"`
	IdentityFile string `json:"IdentityFile"`

	// Config is the fully resolved LimaYAML document for the instance. Only
	// the subset the provider needs is decoded.
	Config InstanceConfigView `json:"config"`
}

// InstanceConfigView is the decoded subset of the resolved `config` object.
type InstanceConfigView struct {
	VMType string   `json:"vmType"`
	Arch   string   `json:"arch"`
	OS     string   `json:"os"`
	CPUs   int64    `json:"cpus"`
	Memory string   `json:"memory"`
	Disk   string   `json:"disk"`
	User   UserView `json:"user"`

	// Mounts and PortForwards are Lima's *resolved* lists. They include
	// entries contributed by the base template and defaults Lima filled in,
	// so they are a superset of whatever the user configured.
	Mounts       []MountView       `json:"mounts"`
	PortForwards []PortForwardView `json:"portForwards"`

	// AdditionalDisks are the disks attached to this instance. Lima resolves
	// an empty list to null, so absence and emptiness are indistinguishable.
	AdditionalDisks []AdditionalDiskView `json:"additionalDisks"`

	// Images are the disk images Lima resolved for this instance. They are
	// the strongest available signal of which template it came from, since
	// the instance itself records no template reference.
	Images []ImageView `json:"images"`
}

// AdditionalDiskView is one attached disk.
type AdditionalDiskView struct {
	Name string `json:"name"`
}

// AttachedDiskNames returns the names of the attached disks.
func (c InstanceConfigView) AttachedDiskNames() []string {
	out := make([]string, 0, len(c.AdditionalDisks))
	for _, d := range c.AdditionalDisks {
		if d.Name != "" {
			out = append(out, d.Name)
		}
	}
	return out
}

// ImageView is one resolved disk image.
type ImageView struct {
	Location string `json:"location"`
	Arch     string `json:"arch"`
}

// ImageLocations returns the resolved image URLs.
func (c InstanceConfigView) ImageLocations() []string {
	out := make([]string, 0, len(c.Images))
	for _, i := range c.Images {
		if i.Location != "" {
			out = append(out, i.Location)
		}
	}
	return out
}

// MountView is one resolved mount.
type MountView struct {
	Location   string `json:"location"`
	MountPoint string `json:"mountPoint"`
	Writable   bool   `json:"writable"`
}

// PortForwardView is one resolved port forward.
type PortForwardView struct {
	GuestPort int64  `json:"guestPort"`
	HostPort  int64  `json:"hostPort"`
	Proto     string `json:"proto"`
	GuestIP   string `json:"guestIP"`
	HostIP    string `json:"hostIP"`
}

// HasMount reports whether the instance has a mount matching the given
// location, guest mount point and writability.
//
// Matching is by subset, not by equality of the whole list: Lima's resolved
// mounts also contain entries the base template contributed (the default
// templates mount the user's home directory, for example), and those are not
// drift. An empty mountPoint means "whatever Lima chose", because Lima
// defaults it to the location.
//
// Paths are compared after cleaning, and macOS's /tmp -> /private/tmp
// indirection is tolerated in both directions, so a configuration written
// either way matches.
func (c InstanceConfigView) HasMount(location, mountPoint string, writable bool) bool {
	for _, m := range c.Mounts {
		if !samePath(m.Location, location) {
			continue
		}
		if mountPoint != "" && !samePath(m.MountPoint, mountPoint) {
			continue
		}
		if m.Writable != writable {
			continue
		}
		return true
	}
	return false
}

// HasPortForward reports whether a forward for the given guest port, host port
// and protocol is present.
//
// A zero host port means Lima chose one, so only the guest port and protocol
// are compared. Lima fills guestIP and hostIP with defaults, so they are not
// part of the match.
func (c InstanceConfigView) HasPortForward(guestPort, hostPort int64, proto string) bool {
	if proto == "" {
		proto = "tcp"
	}
	for _, p := range c.PortForwards {
		if p.GuestPort != guestPort {
			continue
		}
		resolvedProto := p.Proto
		if resolvedProto == "" {
			resolvedProto = "tcp"
		}
		if !strings.EqualFold(resolvedProto, proto) {
			continue
		}
		if hostPort != 0 && p.HostPort != hostPort {
			continue
		}
		return true
	}
	return false
}

// FindMount returns the resolved mount at a location, if any.
//
// Location is Lima's notion of mount identity, so this is the lookup used
// both for drift reconciliation and for merging an in-place change.
func (c InstanceConfigView) FindMount(location string) (MountView, bool) {
	for _, m := range c.Mounts {
		if samePath(m.Location, location) {
			return m, true
		}
	}
	return MountView{}, false
}

// FindPortForward returns the resolved forward for a guest port and protocol.
func (c InstanceConfigView) FindPortForward(guestPort int64, proto string) (PortForwardView, bool) {
	for _, p := range c.PortForwards {
		if p.GuestPort != guestPort {
			continue
		}
		if normalizeProto(p.Proto) != normalizeProto(proto) {
			continue
		}
		return p, true
	}
	return PortForwardView{}, false
}

// SamePath reports whether two filesystem paths refer to the same location,
// tolerating macOS's /tmp -> /private/tmp indirection.
func SamePath(a, b string) bool { return samePath(a, b) }

// samePath compares two filesystem paths tolerantly.
//
// On macOS /tmp is a symlink to /private/tmp and Lima reports the resolved
// form, so a configuration written either way must match. Symlinks are not
// otherwise resolved: doing so would make the comparison depend on host state
// that can change between plan and apply.
func samePath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if a == b {
		return true
	}
	return strings.TrimPrefix(a, "/private") == strings.TrimPrefix(b, "/private")
}

// UserView is the guest user block; only the login name is used.
type UserView struct {
	Name string `json:"name"`
}

// Status returns the normalised status.
func (i Instance) Status() Status { return NormalizeStatus(i.RawStatus) }

// SSHInfo is the connection information derived from an inspect result.
//
// It is assembled from the list output rather than from `limactl show-ssh`,
// which is deprecated in Lima 2.x and offers no machine-readable format. See
// docs/development/lima-cli-contract.md §11.
type SSHInfo struct {
	Address    string
	Port       int64
	User       string
	ConfigFile string
	Hostname   string
}

// SSH derives connection information for the instance.
//
// For a stopped instance Lima still reports the last-known address and port;
// they are returned as-is and documented as last-known rather than live.
func (i Instance) SSH() SSHInfo {
	return SSHInfo{
		Address:    i.SSHAddress,
		Port:       i.SSHLocalPort,
		User:       i.Config.User.Name,
		ConfigFile: i.SSHConfigFile,
		Hostname:   i.Hostname,
	}
}

// ParseInstances decodes `limactl list --format json` output.
//
// The output is NDJSON: one self-contained JSON object per line with no
// enclosing array (verified against Lima 2.2.0, see the CLI contract §4.1).
// A streaming decoder handles both the single-object and multi-object cases,
// and also copes with pretty-printed objects spanning multiple lines.
func ParseInstances(r io.Reader) ([]Instance, error) {
	dec := json.NewDecoder(r)
	var out []Instance
	for {
		var inst Instance
		err := dec.Decode(&inst)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("parsing limactl list output: %w", err)
		}
		if inst.Name == "" {
			return nil, fmt.Errorf("parsing limactl list output: entry %d has no name", len(out))
		}
		out = append(out, inst)
	}
	return out, nil
}

// ParseInstancesString is a convenience wrapper over ParseInstances.
func ParseInstancesString(s string) ([]Instance, error) {
	return ParseInstances(strings.NewReader(s))
}

// HostInfo is the decoded subset of `limactl info`.
type HostInfo struct {
	Version   string         `json:"version"`
	LimaHome  string         `json:"limaHome"`
	VMTypes   []string       `json:"vmTypes"`
	HostOS    string         `json:"hostOS"`
	HostArch  string         `json:"hostArch"`
	Templates []TemplateInfo `json:"templates"`
}

// TemplateInfo is one entry of the template catalogue.
type TemplateInfo struct {
	Name     string `json:"name"`
	Location string `json:"location"`
}

// UserTemplates returns only templates a user would instantiate, filtering out
// Lima's internal composition fragments (`_images/…`, `_default/…`).
func (h HostInfo) UserTemplates() []TemplateInfo {
	out := make([]TemplateInfo, 0, len(h.Templates))
	for _, t := range h.Templates {
		if strings.HasPrefix(t.Name, "_") {
			continue
		}
		out = append(out, t)
	}
	return out
}

// TemplateNames returns the names of UserTemplates.
func (h HostInfo) TemplateNames() []string {
	ts := h.UserTemplates()
	out := make([]string, 0, len(ts))
	for _, t := range ts {
		out = append(out, t.Name)
	}
	return out
}

// ParseHostInfo decodes `limactl info` output.
func ParseHostInfo(r io.Reader) (HostInfo, error) {
	var h HostInfo
	if err := json.NewDecoder(r).Decode(&h); err != nil {
		return HostInfo{}, fmt.Errorf("parsing limactl info output: %w", err)
	}
	if h.Version == "" {
		return HostInfo{}, fmt.Errorf("parsing limactl info output: missing version field")
	}
	return h, nil
}
