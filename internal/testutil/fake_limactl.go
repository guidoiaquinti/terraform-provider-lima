// Copyright 2026 Guido Iaquinti
// SPDX-License-Identifier: Apache-2.0

// Package testutil provides test doubles for the Lima command adapter.
//
// FakeLimactl implements the lima.Runner interface structurally (it does not
// import internal/lima, which keeps it usable from that package's own external
// tests). Because it plugs in at the process-execution boundary, tests that
// use it exercise the provider's real argument construction, environment
// merging and output parsing — only the VM is simulated.
package testutil

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Invocation records one recorded command.
type Invocation struct {
	Binary string
	Args   []string
	Env    []string
}

// Arg reports whether the invocation contained an exact argument.
func (i Invocation) Arg(want string) bool {
	for _, a := range i.Args {
		if a == want {
			return true
		}
	}
	return false
}

// EnvValue returns the value of an environment key, and whether it was set.
func (i Invocation) EnvValue(key string) (string, bool) {
	prefix := key + "="
	// Later entries win, matching exec semantics.
	val, found := "", false
	for _, kv := range i.Env {
		if strings.HasPrefix(kv, prefix) {
			val, found = strings.TrimPrefix(kv, prefix), true
		}
	}
	return val, found
}

// Command returns the subcommand (the first non-flag argument).
func (i Invocation) Command() string {
	for _, a := range i.Args {
		if !strings.HasPrefix(a, "-") {
			return a
		}
	}
	if len(i.Args) > 0 {
		return i.Args[0]
	}
	return ""
}

// FakeInstance is the simulated state of one Lima instance.
type FakeInstance struct {
	Name          string     `json:"name"`
	Hostname      string     `json:"hostname"`
	Status        string     `json:"status"`
	Dir           string     `json:"dir"`
	VMType        string     `json:"vmType"`
	Arch          string     `json:"arch"`
	CPUs          int64      `json:"cpus"`
	Memory        int64      `json:"memory"`
	Disk          int64      `json:"disk"`
	SSHLocalPort  int64      `json:"sshLocalPort,omitempty"`
	SSHConfigFile string     `json:"sshConfigFile,omitempty"`
	SSHAddress    string     `json:"sshAddress,omitempty"`
	Protected     bool       `json:"protected"`
	LimaVersion   string     `json:"limaVersion,omitempty"`
	HostOS        string     `json:"HostOS,omitempty"`
	HostArch      string     `json:"HostArch,omitempty"`
	LimaHome      string     `json:"LimaHome,omitempty"`
	Config        FakeConfig `json:"config"`
}

// FakeConfig is the resolved `config` object Lima reports.
type FakeConfig struct {
	VMType string `json:"vmType,omitempty"`
	Arch   string `json:"arch,omitempty"`
	OS     string `json:"os,omitempty"`
	CPUs   int64  `json:"cpus,omitempty"`
	Memory string `json:"memory,omitempty"`
	Disk   string `json:"disk,omitempty"`
	User   struct {
		Name string `json:"name,omitempty"`
	} `json:"user"`
	Mounts       []FakeMount       `json:"mounts,omitempty"`
	PortForwards []FakePortForward `json:"portForwards,omitempty"`
	Images       []FakeImage       `json:"images,omitempty"`
}

// FakeImage mirrors a resolved Lima disk image.
type FakeImage struct {
	Location string `json:"location"`
	Arch     string `json:"arch,omitempty"`
}

// FakeMount mirrors a resolved Lima mount.
type FakeMount struct {
	Location   string `json:"location"`
	MountPoint string `json:"mountPoint,omitempty"`
	Writable   bool   `json:"writable"`
}

// FakePortForward mirrors a resolved Lima port forward.
type FakePortForward struct {
	GuestPort int64  `json:"guestPort"`
	HostPort  int64  `json:"hostPort,omitempty"`
	Proto     string `json:"proto,omitempty"`
	GuestIP   string `json:"guestIP,omitempty"`
	HostIP    string `json:"hostIP,omitempty"`
}

// Scripted overrides the outcome of a matching command.
type Scripted struct {
	// Command is the limactl subcommand to intercept, e.g. "start".
	Command string
	// Name optionally restricts the override to one instance name.
	Name string
	// Stdout, Stderr and ExitCode replace the simulated result.
	Stdout   string
	Stderr   string
	ExitCode int
	// Err makes the runner itself fail, as if the process could not start.
	Err error
	// Delay is slept (cancellably) before responding.
	Delay time.Duration
	// Times limits how many invocations this applies to; 0 means unlimited.
	Times int
	used  int
}

// TemplateImages maps a template reference to the image URLs it resolves to,
// mirroring `limactl template copy --fill`.
//
// The defaults reproduce a real property of Lima's catalogue that matters for
// template verification: ubuntu and docker share a base image, so images
// cannot tell them apart.
func defaultTemplateImages() map[string][]string {
	ubuntu := []string{
		"https://cloud-images.ubuntu.com/releases/resolute/release/ubuntu-26.04-server-cloudimg-amd64.img",
		"https://cloud-images.ubuntu.com/releases/resolute/release/ubuntu-26.04-server-cloudimg-arm64.img",
	}
	return map[string][]string{
		"template:ubuntu": ubuntu,
		"template:docker": ubuntu,
		"template:alpine": {
			"https://dl-cdn.alpinelinux.org/alpine/v3.23/releases/cloud/nocloud_alpine-3.23.4-x86_64-uefi-cloudinit-r0.qcow2",
			"https://dl-cdn.alpinelinux.org/alpine/v3.23/releases/cloud/nocloud_alpine-3.23.4-aarch64-uefi-cloudinit-r0.qcow2",
		},
		"template:fedora": {
			"https://download.fedoraproject.org/pub/fedora/linux/releases/44/Cloud/x86_64/images/Fedora-Cloud-Base-44.x86_64.qcow2",
		},
	}
}

// FakeDisk is the simulated state of one Lima disk.
type FakeDisk struct {
	Name        string `json:"name"`
	Size        int64  `json:"size"`
	Format      string `json:"format"`
	Dir         string `json:"dir"`
	Instance    string `json:"instance"`
	InstanceDir string `json:"instanceDir"`
	MountPoint  string `json:"mountPoint"`
}

// FakeLimactl simulates limactl over an in-memory instance store.
type FakeLimactl struct {
	mu sync.Mutex

	// Version is reported by --version.
	Version string
	// HostOS and HostArch are reported by info and list --all-fields.
	HostOS   string
	HostArch string
	// LimaHome is reported where Lima would report it.
	LimaHome string
	// Templates is the catalogue reported by info.
	Templates []string
	// SSHUser is the guest login reported in config.user.name.
	SSHUser string

	// StartFailsFor makes start fail for these instance names.
	StartFailsFor map[string]string
	// CreateRegistersOnFailure simulates partial creation: create fails but
	// the instance directory is left behind.
	CreateRegistersOnFailure bool
	// CreateFailsFor makes create fail for these names.
	CreateFailsFor map[string]string
	// EditFailsFor makes edit fail for these names.
	EditFailsFor map[string]string
	// TemplateImages maps a template reference to its resolved image URLs.
	TemplateImages map[string][]string

	instances map[string]*FakeInstance
	disks     map[string]*FakeDisk
	calls     []Invocation
	scripts   []*Scripted
	nextPort  int64
	// LastDocument holds the bytes of the most recent create/validate file,
	// captured before the adapter deletes it.
	LastDocument string
	// ReadFile lets the fake read the temporary document the adapter wrote.
	ReadFile func(path string) ([]byte, error)
}

// NewFakeLimactl returns a fake preloaded with realistic host metadata that
// matches the values observed from Lima 2.2.0 on macOS/arm64.
func NewFakeLimactl() *FakeLimactl {
	return &FakeLimactl{
		Version:        "2.2.0",
		HostOS:         "darwin",
		HostArch:       "aarch64",
		LimaHome:       "/tmp/fake-lima",
		Templates:      []string{"_images/ubuntu", "_default/mounts", "ubuntu", "docker", "alpine", "fedora"},
		SSHUser:        "testuser",
		TemplateImages: defaultTemplateImages(),
		instances:      map[string]*FakeInstance{},
		disks:          map[string]*FakeDisk{},
		nextPort:       60022,
	}
}

// Script installs an override.
func (f *FakeLimactl) Script(s Scripted) *FakeLimactl {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scripts = append(f.scripts, &s)
	return f
}

// Seed inserts an instance into the simulated store.
func (f *FakeLimactl) Seed(inst FakeInstance) *FakeLimactl {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seedLocked(inst)
	return f
}

func (f *FakeLimactl) seedLocked(inst FakeInstance) {
	if inst.Hostname == "" {
		inst.Hostname = "lima-" + inst.Name
	}
	if inst.Status == "" {
		inst.Status = "Stopped"
	}
	if inst.Dir == "" {
		inst.Dir = f.LimaHome + "/" + inst.Name
	}
	if inst.VMType == "" {
		inst.VMType = "vz"
	}
	if inst.Arch == "" {
		inst.Arch = f.HostArch
	}
	if inst.CPUs == 0 {
		inst.CPUs = 4
	}
	if inst.Memory == 0 {
		inst.Memory = 4 << 30
	}
	if inst.Disk == 0 {
		inst.Disk = 100 << 30
	}
	if inst.SSHConfigFile == "" {
		inst.SSHConfigFile = inst.Dir + "/ssh.config"
	}
	inst.Config.VMType = inst.VMType
	inst.Config.Arch = inst.Arch
	inst.Config.OS = "Linux"
	inst.Config.CPUs = inst.CPUs
	inst.Config.User.Name = f.SSHUser
	f.instances[inst.Name] = &inst
}

// SeedDisk inserts a disk into the simulated store.
func (f *FakeLimactl) SeedDisk(d FakeDisk) *FakeLimactl {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seedDiskLocked(d)
	return f
}

func (f *FakeLimactl) seedDiskLocked(d FakeDisk) {
	if d.Dir == "" {
		d.Dir = f.LimaHome + "/_disks/" + d.Name
	}
	if d.MountPoint == "" {
		d.MountPoint = "/mnt/lima-" + d.Name
	}
	if d.Format == "" {
		// Lima reports raw regardless of what was requested, because the vz
		// driver converts. Reproducing that is the point.
		d.Format = "raw"
	}
	f.disks[d.Name] = &d
}

// GetDisk returns one simulated disk.
func (f *FakeLimactl) GetDisk(name string) (FakeDisk, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.disks[name]
	if !ok {
		return FakeDisk{}, false
	}
	return *d, true
}

// AttachDisk records that a disk belongs to an instance.
//
// Attachment alone does not make the disk appear in use. Lima reports a disk as
// held only while the named instance exists and is running, so a caller wanting
// the in-use state must also Seed that instance with status Running. Attaching
// to an instance that was never seeded describes a state Lima cannot be in, and
// the disk will read as free.
func (f *FakeLimactl) AttachDisk(diskName, instanceName string) *FakeLimactl {
	f.mu.Lock()
	defer f.mu.Unlock()
	if d, ok := f.disks[diskName]; ok {
		d.Instance = instanceName
		d.InstanceDir = f.LimaHome + "/" + instanceName
	}
	return f
}

// disk dispatches the `limactl disk` subcommands.
//
// The caller in Run already holds f.mu.
func (f *FakeLimactl) disk(args []string) (string, string, int, error) {
	if len(args) < 2 {
		return "", fatal("fake limactl: disk requires a subcommand"), 1, nil
	}
	switch args[1] {
	case "list", "ls":
		return f.diskList()
	case "create":
		return f.diskCreate(args)
	case "resize":
		return f.diskResize(args)
	case "delete", "rm", "remove":
		return f.diskDelete(args)
	default:
		return "", fatal(fmt.Sprintf("fake limactl: unsupported disk subcommand %q", args[1])), 1, nil
	}
}

// diskList emits NDJSON, exactly like Lima.
func (f *FakeLimactl) diskList() (string, string, int, error) {
	names := make([]string, 0, len(f.disks))
	for n := range f.disks {
		names = append(names, n)
	}
	for i := 1; i < len(names); i++ {
		for j := i; j > 0 && names[j] < names[j-1]; j-- {
			names[j], names[j-1] = names[j-1], names[j]
		}
	}

	var b strings.Builder
	for _, n := range names {
		reported := *f.disks[n]
		// Lima reports `instance` only while a holder is actually running, so
		// the field is derived here rather than replayed from what AttachDisk
		// stored. See liveHolder.
		reported.Instance = f.liveHolder(f.disks[n])
		if reported.Instance == "" {
			reported.InstanceDir = ""
		}
		j, _ := json.Marshal(reported)
		b.Write(j)
		b.WriteByte('\n')
	}
	return b.String(), "", 0, nil
}

// liveHolder returns the instance currently holding a disk, or empty.
//
// Lima's `instance` field on a disk means "in use right now", not "attached
// to": it is populated while the holding instance is running and empty
// otherwise. Storing it statically at attach time made two things impossible
// that really happen — deleting the holder, and stopping it — because the disk
// stayed locked forever against an instance that was gone. That is not a
// hypothetical: it is what the sweep ordering test hit, and a sweep is precisely
// the operation that deletes a holder and then its disks.
//
// The caller must hold f.mu.
func (f *FakeLimactl) liveHolder(d *FakeDisk) string {
	if d.Instance == "" {
		return ""
	}
	holder, ok := f.instances[d.Instance]
	if !ok {
		// The holder was deleted; Lima releases the lock with it.
		return ""
	}
	if !strings.EqualFold(holder.Status, "Running") {
		return ""
	}
	return d.Instance
}

func (f *FakeLimactl) diskCreate(args []string) (string, string, int, error) {
	name := diskNameArg(args)
	if _, exists := f.disks[name]; exists {
		return "", fatal(fmt.Sprintf("disk `%s` already exists (`%s/_disks/%s`)", name, f.LimaHome, name)), 1, nil
	}
	size := flagValue(args, "--size")
	bytes, ok := parseIECSize(size)
	if !ok {
		return "", fatal(fmt.Sprintf("invalid size %q", size)), 1, nil
	}
	f.seedDiskLocked(FakeDisk{Name: name, Size: bytes})
	return "", info(fmt.Sprintf("Creating qcow2 disk `%s` with size %s", name, size)), 0, nil
}

func (f *FakeLimactl) diskResize(args []string) (string, string, int, error) {
	name := diskNameArg(args)
	d, ok := f.disks[name]
	if !ok {
		return "", fatal(fmt.Sprintf("disk `%s` not found", name)), 1, nil
	}
	if d.Instance != "" {
		return "", fatal(fmt.Sprintf(
			"cannot resize disk `%s` used by running instance `%s`. Please stop the VM instance",
			name, d.Instance)), 1, nil
	}
	size := flagValue(args, "--size")
	bytes, ok2 := parseIECSize(size)
	if !ok2 {
		return "", fatal(fmt.Sprintf("invalid size %q", size)), 1, nil
	}
	if bytes < d.Size {
		return "", fatal(fmt.Sprintf(
			"specified size `%s` is less than the current disk size `%s`. Disk shrinking is currently unavailable",
			size, iecString(d.Size))), 1, nil
	}
	d.Size = bytes
	return "", info(fmt.Sprintf("Resized disk `%s`", name)), 0, nil
}

func (f *FakeLimactl) diskDelete(args []string) (string, string, int, error) {
	name := diskNameArg(args)
	d, ok := f.disks[name]
	if !ok {
		// Lima exits 0 for an absent disk.
		return "", warn(fmt.Sprintf("Ignoring non-existent disk `%s`", name)), 0, nil
	}
	// Same liveness rule as the listing: a lock belongs to a running holder, so
	// a disk whose holder was deleted or stopped is deletable.
	if holder := f.liveHolder(d); holder != "" {
		return "", fatal(fmt.Sprintf("cannot delete disk `%s` in use by instance `%s`", name, holder)), 1, nil
	}
	delete(f.disks, name)
	return "", info(fmt.Sprintf("Deleted disk `%s`", name)), 0, nil
}

// diskNameArg returns the first positional argument after the subcommand.
func diskNameArg(args []string) string {
	for i, a := range args {
		if i < 2 || strings.HasPrefix(a, "-") {
			continue
		}
		if i > 0 && strings.HasPrefix(args[i-1], "--") {
			continue
		}
		return a
	}
	return ""
}

// parseIECSize understands the size strings the provider emits.
func parseIECSize(s string) (int64, bool) {
	units := []struct {
		suffix string
		mult   int64
	}{
		{"TiB", 1 << 40}, {"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10}, {"B", 1},
	}
	for _, u := range units {
		if strings.HasSuffix(s, u.suffix) {
			n, err := strconv.ParseInt(strings.TrimSuffix(s, u.suffix), 10, 64)
			if err != nil {
				return 0, false
			}
			return n * u.mult, true
		}
	}
	return 0, false
}

// iecString mirrors the provider's own size formatting, for error messages.
func iecString(bytes int64) string {
	for _, u := range []struct {
		name string
		size int64
	}{{"TiB", 1 << 40}, {"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10}} {
		if bytes >= u.size && bytes%u.size == 0 {
			return strconv.FormatInt(bytes/u.size, 10) + u.name
		}
	}
	return strconv.FormatInt(bytes, 10) + "B"
}

// Instances returns a snapshot of the simulated store.
func (f *FakeLimactl) Instances() map[string]FakeInstance {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]FakeInstance, len(f.instances))
	for k, v := range f.instances {
		out[k] = *v
	}
	return out
}

// Get returns one simulated instance.
func (f *FakeLimactl) Get(name string) (FakeInstance, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	inst, ok := f.instances[name]
	if !ok {
		return FakeInstance{}, false
	}
	return *inst, true
}

// Calls returns every recorded invocation.
func (f *FakeLimactl) Calls() []Invocation {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Invocation(nil), f.calls...)
}

// CallsFor returns recorded invocations of one subcommand.
func (f *FakeLimactl) CallsFor(command string) []Invocation {
	var out []Invocation
	for _, c := range f.Calls() {
		if c.Command() == command {
			out = append(out, c)
		}
	}
	return out
}

// Reset clears recorded invocations, keeping instance state.
func (f *FakeLimactl) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = nil
}

// knownCommands guards against the provider inventing a subcommand. Anything
// outside this set is reported as an error rather than silently succeeding,
// which is how a test notices an unexpected argument.
var knownCommands = map[string]bool{
	"info": true, "list": true, "validate": true, "create": true,
	"start": true, "stop": true, "delete": true, "protect": true,
	"unprotect": true, "edit": true, "template": true, "disk": true,
	"--version": true,
}

// Run implements the runner interface.
func (f *FakeLimactl) Run(ctx context.Context, binary string, args []string, env []string) (string, string, int, error) {
	f.mu.Lock()
	f.calls = append(f.calls, Invocation{
		Binary: binary,
		Args:   append([]string(nil), args...),
		Env:    append([]string(nil), env...),
	})
	f.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return "", "", 0, err
	}

	cmd := ""
	if len(args) > 0 {
		cmd = args[0]
	}
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			cmd = a
			break
		}
	}

	if s := f.matchScript(cmd, args); s != nil {
		if s.Delay > 0 {
			select {
			case <-time.After(s.Delay):
			case <-ctx.Done():
				return "", "", 0, ctx.Err()
			}
		}
		if s.Err != nil {
			return s.Stdout, s.Stderr, 0, s.Err
		}
		return s.Stdout, s.Stderr, s.ExitCode, nil
	}

	if !knownCommands[cmd] {
		return "", fatal(fmt.Sprintf("fake limactl: unexpected subcommand %q in args %v", cmd, args)), 1, nil
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	switch cmd {
	case "--version":
		return "limactl version " + f.Version + "\n", "", 0, nil
	case "info":
		return f.infoJSON(), "", 0, nil
	case "template":
		return f.templateCopy(args)
	case "disk":
		return f.disk(args)
	case "list":
		return f.listJSON(args)
	case "validate":
		return f.validate(args)
	case "create":
		return f.create(args)
	case "edit":
		return f.edit(args)
	case "start":
		return f.start(args)
	case "stop":
		return f.stop(args)
	case "delete":
		return f.del(args)
	case "protect":
		return f.setProtect(args, true)
	case "unprotect":
		return f.setProtect(args, false)
	}
	return "", "", 0, nil
}

func (f *FakeLimactl) matchScript(cmd string, args []string) *Scripted {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.scripts {
		if s.Command != cmd {
			continue
		}
		if s.Name != "" && !argsContain(args, s.Name) {
			continue
		}
		if s.Times > 0 && s.used >= s.Times {
			continue
		}
		s.used++
		return s
	}
	return nil
}

func argsContain(args []string, want string) bool {
	for _, a := range args {
		if a == want || strings.HasSuffix(a, "="+want) {
			return true
		}
	}
	return false
}

// fatal renders a message the way Lima's logrus text formatter does, so error
// parsing is exercised against the real output shape.
func fatal(msg string) string {
	return fmt.Sprintf("time=%q level=fatal msg=%q\n", "2026-07-27T00:00:00+02:00", msg)
}

func warn(msg string) string {
	return fmt.Sprintf("time=%q level=warning msg=%q\n", "2026-07-27T00:00:00+02:00", msg)
}

func info(msg string) string {
	return fmt.Sprintf("time=%q level=info msg=%q\n", "2026-07-27T00:00:00+02:00", msg)
}

// templateCopy reproduces `limactl template copy --fill <ref> -`, emitting
// just enough YAML for the provider's parser.
func (f *FakeLimactl) templateCopy(args []string) (string, string, int, error) {
	if len(args) < 2 || args[1] != "copy" {
		return "", fatal(fmt.Sprintf("fake limactl: unsupported template subcommand %v", args)), 1, nil
	}

	ref := ""
	for _, a := range args[2:] {
		if strings.HasPrefix(a, "-") || a == "copy" {
			continue
		}
		ref = a
		break
	}

	// The dispatch switch in Run already holds f.mu; locking again here would
	// deadlock.
	images, ok := f.TemplateImages[ref]
	if !ok {
		return "", fatal(fmt.Sprintf("template %q not found", ref)), 1, nil
	}

	var b strings.Builder
	b.WriteString("images:\n")
	for _, loc := range images {
		fmt.Fprintf(&b, "- location: %q\n", loc)
	}
	return b.String(), "", 0, nil
}

func (f *FakeLimactl) infoJSON() string {
	type tmpl struct {
		Name     string `json:"name"`
		Location string `json:"location"`
	}
	templates := make([]tmpl, 0, len(f.Templates))
	for _, t := range f.Templates {
		templates = append(templates, tmpl{Name: t, Location: "/opt/lima/templates/" + t + ".yaml"})
	}
	doc := map[string]any{
		"version":   f.Version,
		"limaHome":  f.LimaHome,
		"vmTypes":   []string{"qemu", "vz", "krunkit"},
		"hostOS":    f.HostOS,
		"hostArch":  f.HostArch,
		"templates": templates,
		// A field the provider does not decode, present to prove unknown
		// fields are tolerated.
		"guestAgents": map[string]any{"aarch64": map[string]string{"location": "/opt/lima/ga.gz"}},
	}
	b, _ := json.Marshal(doc)
	return string(b) + "\n"
}

// listJSON emits NDJSON, exactly like Lima: one object per line, no array.
func (f *FakeLimactl) listJSON(args []string) (string, string, int, error) {
	var names []string
	for i, a := range args {
		if i == 0 || strings.HasPrefix(a, "-") {
			continue
		}
		// Skip flag values.
		if i > 0 && (args[i-1] == "--format") {
			continue
		}
		if a == "list" {
			continue
		}
		names = append(names, a)
	}

	all := make([]*FakeInstance, 0, len(f.instances))
	if len(names) == 0 {
		for _, n := range f.sortedNames() {
			all = append(all, f.instances[n])
		}
		if len(all) == 0 {
			return "", warn("No instance found. Run `limactl create` to create an instance."), 0, nil
		}
	} else {
		for _, n := range names {
			inst, ok := f.instances[n]
			if !ok {
				return "", warn(fmt.Sprintf("No instance matching %s found.", n)) + fatal("unmatched instances"), 1, nil
			}
			all = append(all, inst)
		}
	}

	var b strings.Builder
	for _, inst := range all {
		out := *inst
		out.HostOS = f.HostOS
		out.HostArch = f.HostArch
		out.LimaHome = f.LimaHome
		out.LimaVersion = f.Version
		j, _ := json.Marshal(out)
		b.Write(j)
		b.WriteByte('\n')
	}
	return b.String(), "", 0, nil
}

func (f *FakeLimactl) sortedNames() []string {
	names := make([]string, 0, len(f.instances))
	for n := range f.instances {
		names = append(names, n)
	}
	// Simple insertion sort keeps this dependency-free and deterministic.
	for i := 1; i < len(names); i++ {
		for j := i; j > 0 && names[j] < names[j-1]; j-- {
			names[j], names[j-1] = names[j-1], names[j]
		}
	}
	return names
}

func (f *FakeLimactl) capture(args []string) {
	path := lastPositional(args)
	if path == "" || f.ReadFile == nil {
		return
	}
	if b, err := f.ReadFile(path); err == nil {
		f.LastDocument = string(b)
	}
}

func lastPositional(args []string) string {
	for i := len(args) - 1; i >= 0; i-- {
		if !strings.HasPrefix(args[i], "-") {
			return args[i]
		}
	}
	return ""
}

func (f *FakeLimactl) validate(args []string) (string, string, int, error) {
	f.capture(args)
	return "", info(fmt.Sprintf("`%s`: OK", lastPositional(args))), 0, nil
}

func (f *FakeLimactl) create(args []string) (string, string, int, error) {
	f.capture(args)
	name := flagValue(args, "--name")
	if name == "" {
		name = "default"
	}
	if _, exists := f.instances[name]; exists {
		return "", fatal(fmt.Sprintf("instance `%s` already exists", name)), 1, nil
	}
	if msg, bad := f.CreateFailsFor[name]; bad {
		if f.CreateRegistersOnFailure {
			f.seedLocked(FakeInstance{Name: name, Status: "Broken"})
		}
		return "", fatal(msg), 1, nil
	}
	f.seedLocked(FakeInstance{Name: name, Status: "Stopped"})
	return "", info(fmt.Sprintf("Run `limactl start %s` to start the instance.", name)), 0, nil
}

// edit reproduces `limactl edit`, including the two refusals that shape the
// provider's resize path: it cannot edit a running instance, and it will not
// shrink a disk.
func (f *FakeLimactl) edit(args []string) (string, string, int, error) {
	name := lastPositional(args)
	inst, ok := f.instances[name]
	if !ok {
		return "", fatal(fmt.Sprintf("instance %q not found", name)), 1, nil
	}
	if inst.Status == "Running" {
		return "", fatal("cannot edit a running instance"), 1, nil
	}
	if msg, bad := f.EditFailsFor[name]; bad {
		return "", fatal(msg), 1, nil
	}

	if v := flagValue(args, "--cpus"); v != "" {
		n, ok := parseCPUs(v)
		if !ok {
			return "", fatal(fmt.Sprintf("invalid --cpus %q", v)), 1, nil
		}
		inst.CPUs = n
		inst.Config.CPUs = n
	}
	if v := flagValue(args, "--memory"); v != "" {
		b, ok := gibToBytes(v)
		if !ok {
			return "", fatal(fmt.Sprintf("invalid --memory %q", v)), 1, nil
		}
		inst.Memory = b
	}
	if v := flagValue(args, "--disk"); v != "" {
		b, ok := gibToBytes(v)
		if !ok {
			return "", fatal(fmt.Sprintf("invalid --disk %q", v)), 1, nil
		}
		if b < inst.Disk {
			// Lima's own wording, including the rejected-buffer side effect.
			return "", fatal(fmt.Sprintf(
				"the YAML is invalid, saved the buffer as `lima.REJECTED.yaml`: field `disk`: shrinking the disk (%dGiB --> %dGiB) is not supported",
				inst.Disk>>30, b>>30)), 1, nil
		}
		inst.Disk = b
	}

	for _, expr := range flagValues(args, "--set") {
		if err := applySetExpr(inst, expr); err != nil {
			return "", fatal(err.Error()), 1, nil
		}
	}

	return "", info(fmt.Sprintf("Instance `%s` configuration edited", name)), 0, nil
}

// applySetExpr interprets the narrow subset of yq the provider emits:
// `.mounts = <json>` and `.portForwards = <json>`.
//
// Parsing the provider's own expression rather than accepting it blindly means
// a malformed expression fails the test instead of passing silently.
func applySetExpr(inst *FakeInstance, expr string) error {
	field, payload, ok := strings.Cut(expr, " = ")
	if !ok {
		return fmt.Errorf("fake limactl: unsupported --set expression %q", expr)
	}
	switch strings.TrimSpace(field) {
	case ".mounts":
		var mounts []FakeMount
		if err := json.Unmarshal([]byte(payload), &mounts); err != nil {
			return fmt.Errorf("fake limactl: invalid mounts payload %q: %w", payload, err)
		}
		inst.Config.Mounts = mounts
	case ".portForwards":
		var forwards []FakePortForward
		if err := json.Unmarshal([]byte(payload), &forwards); err != nil {
			return fmt.Errorf("fake limactl: invalid portForwards payload %q: %w", payload, err)
		}
		inst.Config.PortForwards = forwards
	default:
		return fmt.Errorf("fake limactl: unsupported --set target %q", field)
	}
	return nil
}

// flagValues returns every value given for a repeatable flag.
func flagValues(args []string, flag string) []string {
	var out []string
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			out = append(out, args[i+1])
		}
	}
	return out
}

// gibToBytes converts the GiB float Lima's flags take back into bytes, the
// same way Lima does, so tests detect any conversion drift.
//
// A malformed value is reported as "not ok" rather than as a Go error,
// because the fake models it the way Lima does: a non-zero exit code with a
// message on stderr, not a failure to launch the process.
func gibToBytes(v string) (int64, bool) {
	f, err := strconv.ParseFloat(v, 32)
	if err != nil {
		return 0, false
	}
	return int64(float64(float32(f)) * (1 << 30)), true
}

// parseCPUs mirrors gibToBytes for the integer --cpus flag.
func parseCPUs(v string) (int64, bool) {
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

func (f *FakeLimactl) start(args []string) (string, string, int, error) {
	name := lastPositional(args)
	inst, ok := f.instances[name]
	if !ok {
		return "", fatal(fmt.Sprintf("instance %q not found", name)), 1, nil
	}
	if msg, bad := f.StartFailsFor[name]; bad {
		return "", fatal(msg), 1, nil
	}
	inst.Status = "Running"
	inst.SSHAddress = "127.0.0.1"
	inst.SSHLocalPort = f.nextPort
	f.nextPort++
	return "", info(fmt.Sprintf("READY. Run `limactl shell %s` to open the shell.", name)), 0, nil
}

func (f *FakeLimactl) stop(args []string) (string, string, int, error) {
	name := lastPositional(args)
	inst, ok := f.instances[name]
	if !ok {
		return "", fatal(fmt.Sprintf("instance %q not found", name)), 1, nil
	}
	if inst.Status != "Running" {
		// Lima's real refusal, reproduced verbatim.
		return "", fatal(fmt.Sprintf("expected status `Running`, got `%s` (maybe use `limactl stop -f`?)", inst.Status)), 1, nil
	}
	inst.Status = "Stopped"
	return "", info(fmt.Sprintf("The instance %s has shut down", name)), 0, nil
}

func (f *FakeLimactl) del(args []string) (string, string, int, error) {
	name := lastPositional(args)
	inst, ok := f.instances[name]
	if !ok {
		// Lima exits 0 for an absent instance.
		return "", warn(fmt.Sprintf("Ignoring non-existent instance `%s`", name)), 0, nil
	}
	if inst.Protected {
		return "", fatal(fmt.Sprintf("failed to delete instance `%s`: instance is protected to prohibit accidental removal (Hint: use `limactl unprotect`)", name)), 1, nil
	}
	delete(f.instances, name)
	return "", info(fmt.Sprintf("Deleted `%s`", name)), 0, nil
}

func (f *FakeLimactl) setProtect(args []string, want bool) (string, string, int, error) {
	name := lastPositional(args)
	inst, ok := f.instances[name]
	if !ok {
		return "", fatal(fmt.Sprintf("instance %q not found", name)), 1, nil
	}
	inst.Protected = want
	if want {
		return "", info(fmt.Sprintf("Protected `%s`", name)), 0, nil
	}
	return "", info(fmt.Sprintf("Unprotected `%s`", name)), 0, nil
}

func flagValue(args []string, flag string) string {
	for i, a := range args {
		if strings.HasPrefix(a, flag+"=") {
			return strings.TrimPrefix(a, flag+"=")
		}
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}
