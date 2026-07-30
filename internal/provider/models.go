package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/guidoiaquinti/terraform-provider-lima/internal/lima"
)

// instanceModel mirrors the lima_instance resource schema.
//
// Three kinds of value live here and are kept strictly apart, as the brief
// requires:
//
//   - desired configuration: name, template, config, typed attributes
//   - effective generated configuration: config_hash
//   - runtime state observed from Lima: status, ssh_*, hostname, ...
//
// Read only ever writes the third group (plus config_hash, which is derived
// from configuration rather than from Lima). It never overwrites the first,
// which is what keeps refresh from producing perpetual diffs.
//
// The three list attributes are held as types.List rather than as Go slices,
// because a list can be wholly unknown — `mounts = var.mounts` where the
// variable resolves at apply time — and a []mountModel cannot represent that.
// Decoding into a slice unconditionally fails with a Value Conversion Error
// before planning begins. They are decoded through declared() instead, which is
// the single place that has to reason about unknown.
type instanceModel struct {
	// Desired configuration.
	Name            types.String `tfsdk:"name"`
	Template        types.String `tfsdk:"template"`
	Config          types.String `tfsdk:"config"`
	ConfigOverrides types.String `tfsdk:"config_overrides"`
	VMType          types.String `tfsdk:"vm_type"`
	Arch            types.String `tfsdk:"arch"`
	CPUs            types.Int64  `tfsdk:"cpus"`
	Memory          types.String `tfsdk:"memory"`
	Disk            types.String `tfsdk:"disk"`
	Start           types.Bool   `tfsdk:"start"`
	Protect         types.Bool   `tfsdk:"protect"`
	AdditionalDisks types.List   `tfsdk:"additional_disks"`
	Mounts          types.List   `tfsdk:"mounts"`
	PortForwards    types.List   `tfsdk:"port_forwards"`
	Provisions      types.List   `tfsdk:"provisions"`

	// Effective configuration.
	ConfigHash types.String `tfsdk:"config_hash"`

	// Observed runtime state. There is no ID field: the resource has no `id`
	// attribute, because it duplicated instance_name exactly.
	InstanceName types.String `tfsdk:"instance_name"`
	Status       types.String `tfsdk:"status"`
	RawStatus    types.String `tfsdk:"raw_status"`
	SSHAddress   types.String `tfsdk:"ssh_address"`
	SSHPort      types.Int64  `tfsdk:"ssh_port"`
	SSHUser      types.String `tfsdk:"ssh_user"`
	SSHConfig    types.String `tfsdk:"ssh_config"`
	Hostname     types.String `tfsdk:"hostname"`
	Dir          types.String `tfsdk:"dir"`
	LimaVersion  types.String `tfsdk:"lima_version"`

	Timeouts timeouts.Value `tfsdk:"timeouts"`
}

// declaredLists is the decoded form of the three list attributes.
//
// It exists so that every caller reasons about the lists the same way, and so
// that the awkward parts — unknown values, and the difference between an absent
// attribute and an empty one — are handled exactly once.
type declaredLists struct {
	Mounts       []mountModel
	PortForwards []portModel
	Provisions   []provModel

	// MountsManaged and PortForwardsManaged separate "the attribute is absent"
	// from "the attribute is an empty list". Absent means the provider does not
	// manage these entries and must leave Lima's own alone; empty means the user
	// deleted every entry and wants them gone. Collapsing the two would silently
	// stop honouring a removal.
	//
	// A nil slice cannot carry this, because ElementsAs on a known empty list
	// yields an empty non-nil slice.
	MountsManaged       bool
	PortForwardsManaged bool

	// Unknown reports that at least one list is not yet resolved, so its
	// emptiness proves nothing about the final configuration. Checks that turn
	// on "nothing is set" must stay silent rather than guess.
	Unknown bool
}

// declared decodes the three list attributes.
//
// A list that is null or unknown decodes to nil, so per-entry checks such as
// duplicate detection simply have nothing to inspect. That is the honest
// outcome: their contents do not exist yet.
func (m *instanceModel) declared(ctx context.Context) (declaredLists, diag.Diagnostics) {
	var diags diag.Diagnostics
	var out declaredLists

	diags.Append(elementsIfKnown(ctx, m.Mounts, &out.Mounts)...)
	diags.Append(elementsIfKnown(ctx, m.PortForwards, &out.PortForwards)...)
	diags.Append(elementsIfKnown(ctx, m.Provisions, &out.Provisions)...)

	out.MountsManaged = isManaged(m.Mounts)
	out.PortForwardsManaged = isManaged(m.PortForwards)
	out.Unknown = m.Mounts.IsUnknown() || m.PortForwards.IsUnknown() || m.Provisions.IsUnknown()

	return out, diags
}

// isManaged reports whether a list attribute is present and resolved, and so
// describes entries the provider is responsible for.
func isManaged(list types.List) bool {
	return !list.IsNull() && !list.IsUnknown()
}

// elementsIfKnown decodes a list into target, leaving it nil when the list is
// null or not yet known.
func elementsIfKnown[T any](ctx context.Context, list types.List, target *[]T) diag.Diagnostics {
	if list.IsNull() || list.IsUnknown() {
		return nil
	}
	return list.ElementsAs(ctx, target, false)
}

type mountModel struct {
	Location   types.String `tfsdk:"location"`
	MountPoint types.String `tfsdk:"mount_point"`
	Writable   types.Bool   `tfsdk:"writable"`
}

type portModel struct {
	GuestPort types.Int64  `tfsdk:"guest_port"`
	HostPort  types.Int64  `tfsdk:"host_port"`
	Protocol  types.String `tfsdk:"protocol"`
	GuestIP   types.String `tfsdk:"guest_ip"`
	HostIP    types.String `tfsdk:"host_ip"`
}

type provModel struct {
	Mode       types.String `tfsdk:"mode"`
	Script     types.String `tfsdk:"script"`
	RerunToken types.String `tfsdk:"rerun_token"`
}

// effectiveName returns the real Lima instance name for a logical name.
//
// Prefixing happens in exactly one place so that create, read, update, delete
// and import can never disagree about which instance they mean.
func effectiveName(prefix, logical string) string {
	return prefix + logical
}

// logicalName reverses effectiveName for import.
//
// Import IDs are always the real Lima name. When it starts with the configured
// prefix, the prefix is stripped so `name` matches what the user would write
// in configuration; otherwise the real name is used as-is. That avoids the
// double-prefixing the brief warns about: importing "acme-dev" under prefix
// "acme-" yields name = "dev", which re-derives to "acme-dev".
func logicalName(prefix, actual string) string {
	if prefix != "" && strings.HasPrefix(actual, prefix) {
		trimmed := strings.TrimPrefix(actual, prefix)
		if trimmed != "" {
			return trimmed
		}
	}
	return actual
}

// toRenderRequest builds the configuration render request from the model and
// its decoded list attributes.
func (m *instanceModel) toRenderRequest(lists declaredLists) (lima.RenderRequest, diag.Diagnostics) {
	var diags diag.Diagnostics

	typed := lima.InstanceConfig{
		VMType: stringValue(m.VMType),
		Arch:   stringValue(m.Arch),
		Memory: stringValue(m.Memory),
		Disk:   stringValue(m.Disk),
	}
	if !m.CPUs.IsNull() && !m.CPUs.IsUnknown() {
		typed.CPUs = m.CPUs.ValueInt64()
	}

	for i, mount := range lists.Mounts {
		location, err := lima.ExpandPath(mount.Location.ValueString())
		if err != nil {
			diags.AddError("Invalid mount location",
				fmt.Sprintf("mount %d: %s", i, err))
			continue
		}
		typed.Mounts = append(typed.Mounts, lima.Mount{
			Location:   location,
			MountPoint: stringValue(mount.MountPoint),
			Writable:   boolValue(mount.Writable),
		})
	}

	for _, pf := range lists.PortForwards {
		entry := lima.PortForward{
			GuestPort: pf.GuestPort.ValueInt64(),
			Proto:     stringValue(pf.Protocol),
			GuestIP:   stringValue(pf.GuestIP),
			HostIP:    stringValue(pf.HostIP),
		}
		if !pf.HostPort.IsNull() && !pf.HostPort.IsUnknown() {
			entry.HostPort = pf.HostPort.ValueInt64()
		}
		typed.PortForwards = append(typed.PortForwards, entry)
	}

	for _, p := range lists.Provisions {
		typed.Provision = append(typed.Provision, lima.Provision{
			Mode:   stringValue(p.Mode),
			Script: p.Script.ValueString(),
		})
	}

	// Attachments must reach the generated document too, or a disk declared
	// at create time would silently not be attached.
	if disks := plannedDisks(m); disks != nil {
		typed.AdditionalDisks = *disks
	}

	return lima.RenderRequest{
		Template:  stringValue(m.Template),
		RawConfig: stringValue(m.Config),
		Typed:     typed,
		Overrides: stringValue(m.ConfigOverrides),
	}, diags
}

// applyInstance copies observed Lima state onto the model.
//
// Only runtime fields are written. Desired-configuration fields are left
// exactly as the user wrote them, because Lima resolves defaults that would
// otherwise appear as drift on every plan.
func (m *instanceModel) applyInstance(inst lima.Instance) {
	m.InstanceName = types.StringValue(inst.Name)
	m.Status = types.StringValue(string(inst.Status()))
	m.RawStatus = types.StringValue(inst.RawStatus)
	m.Hostname = types.StringValue(inst.Hostname)
	m.Dir = types.StringValue(inst.Dir)
	m.LimaVersion = optionalString(inst.LimaVersion)

	ssh := inst.SSH()
	m.SSHAddress = optionalString(ssh.Address)
	m.SSHUser = optionalString(ssh.User)
	m.SSHConfig = optionalString(ssh.ConfigFile)
	if ssh.Port > 0 {
		m.SSHPort = types.Int64Value(ssh.Port)
	} else {
		m.SSHPort = types.Int64Null()
	}

	// protect is a real, inspectable property, so drift in it is detectable
	// and worth reflecting.
	m.Protect = types.BoolValue(inst.Protected)
}

// applyObservedConfig updates the typed attributes that Lima reports back, but
// only where the user actually set them.
//
// This is the narrow case where reading Lima's view is safe: if the user
// pinned cpus = 4 and someone edited the VM to 8, that is genuine drift worth
// surfacing. If the user never set cpus, Lima's resolved default must not be
// written into state, or every plan would show a diff.
func (m *instanceModel) applyObservedConfig(inst lima.Instance) {
	if !m.CPUs.IsNull() && inst.CPUs > 0 {
		m.CPUs = types.Int64Value(inst.CPUs)
	}
	m.Memory = observedSize(m.Memory, inst.MemoryBytes)
	m.Disk = observedSize(m.Disk, inst.DiskBytes)
	if !m.VMType.IsNull() && inst.VMType != "" {
		m.VMType = types.StringValue(inst.VMType)
	}
	if !m.Arch.IsNull() && inst.Arch != "" {
		m.Arch = types.StringValue(inst.Arch)
	}
}

// observedStart returns the run state to record for a refresh.
//
// Only a definitive observation overwrites the desired state. Deriving it from
// `status == running` meant every other status read as "stopped", so a
// half-created instance, a broken one, or a status a newer Lima introduces would
// all record start = false and produce a diff proposing a start the user never
// asked for. Leaving the value alone keeps a genuine external stop visible while
// staying quiet about a state that answers nothing.
func observedStart(current types.Bool, inst lima.Instance) types.Bool {
	switch inst.Status() {
	case lima.StatusRunning:
		return types.BoolValue(true)
	case lima.StatusStopped:
		return types.BoolValue(false)
	default:
		return current
	}
}

// observedSize reconciles a configured size string with the byte count Lima
// reports.
//
// The configured spelling is kept whenever it means the same number of bytes,
// so writing "8192MiB" does not fight with Lima reporting 8GiB. Only a real
// difference overwrites it, which is what makes drift visible without making
// every plan noisy.
func observedSize(configured types.String, observedBytes int64) types.String {
	if configured.IsNull() || observedBytes <= 0 {
		return configured
	}
	if have, err := lima.ParseSize(configured.ValueString()); err == nil && have == observedBytes {
		return configured
	}
	return types.StringValue(lima.FormatSize(observedBytes))
}

// instanceDataSourceModel mirrors the lima_instance data source schema.
type instanceDataSourceModel struct {
	Name        types.String `tfsdk:"name"`
	ID          types.String `tfsdk:"id"`
	Status      types.String `tfsdk:"status"`
	RawStatus   types.String `tfsdk:"raw_status"`
	Arch        types.String `tfsdk:"arch"`
	VMType      types.String `tfsdk:"vm_type"`
	CPUs        types.Int64  `tfsdk:"cpus"`
	Memory      types.String `tfsdk:"memory"`
	Disk        types.String `tfsdk:"disk"`
	SSHAddress  types.String `tfsdk:"ssh_address"`
	SSHPort     types.Int64  `tfsdk:"ssh_port"`
	SSHUser     types.String `tfsdk:"ssh_user"`
	SSHConfig   types.String `tfsdk:"ssh_config"`
	Hostname    types.String `tfsdk:"hostname"`
	Dir         types.String `tfsdk:"dir"`
	Protected   types.Bool   `tfsdk:"protected"`
	LimaVersion types.String `tfsdk:"lima_version"`
}

func (m *instanceDataSourceModel) applyInstance(inst lima.Instance, providerVersion string) {
	m.ID = types.StringValue(inst.Name)
	m.Name = types.StringValue(inst.Name)
	m.Status = types.StringValue(string(inst.Status()))
	m.RawStatus = types.StringValue(inst.RawStatus)
	m.Arch = optionalString(inst.Arch)
	m.VMType = optionalString(inst.VMType)
	m.Hostname = optionalString(inst.Hostname)
	m.Dir = optionalString(inst.Dir)
	m.Protected = types.BoolValue(inst.Protected)

	if inst.CPUs > 0 {
		m.CPUs = types.Int64Value(inst.CPUs)
	} else {
		m.CPUs = types.Int64Null()
	}
	if inst.MemoryBytes > 0 {
		m.Memory = types.StringValue(lima.FormatSize(inst.MemoryBytes))
	} else {
		m.Memory = types.StringNull()
	}
	if inst.DiskBytes > 0 {
		m.Disk = types.StringValue(lima.FormatSize(inst.DiskBytes))
	} else {
		m.Disk = types.StringNull()
	}

	ssh := inst.SSH()
	m.SSHAddress = optionalString(ssh.Address)
	m.SSHUser = optionalString(ssh.User)
	m.SSHConfig = optionalString(ssh.ConfigFile)
	if ssh.Port > 0 {
		m.SSHPort = types.Int64Value(ssh.Port)
	} else {
		m.SSHPort = types.Int64Null()
	}

	// Prefer the version that created the instance; fall back to the version
	// the provider detected at configure time.
	if inst.LimaVersion != "" {
		m.LimaVersion = types.StringValue(inst.LimaVersion)
	} else {
		m.LimaVersion = optionalString(providerVersion)
	}
}

// hostDataSourceModel mirrors the lima_host data source schema.
type hostDataSourceModel struct {
	ID            types.String `tfsdk:"id"`
	LimaVersion   types.String `tfsdk:"lima_version"`
	HostOS        types.String `tfsdk:"host_os"`
	HostArch      types.String `tfsdk:"host_arch"`
	VMTypes       types.List   `tfsdk:"vm_types"`
	LimaHome      types.String `tfsdk:"lima_home"`
	BinaryPath    types.String `tfsdk:"binary_path"`
	Templates     types.List   `tfsdk:"templates"`
	InstanceNames types.List   `tfsdk:"instance_names"`
}

func stringValue(v types.String) string {
	if v.IsNull() || v.IsUnknown() {
		return ""
	}
	return v.ValueString()
}

func boolValue(v types.Bool) bool {
	if v.IsNull() || v.IsUnknown() {
		return false
	}
	return v.ValueBool()
}

// optionalString maps an empty string to null, so an absent value reads as
// absent rather than as an empty string.
func optionalString(s string) types.String {
	if s == "" {
		return types.StringNull()
	}
	return types.StringValue(s)
}

// stringList converts a Go slice into a Terraform list, mapping nil to an
// empty list rather than null so consumers can always iterate it.
func stringList(ctx context.Context, values []string) (types.List, diag.Diagnostics) {
	if values == nil {
		values = []string{}
	}
	return types.ListValueFrom(ctx, types.StringType, values)
}

// reconcileDeclaredEntries updates the mounts and port_forwards attributes in
// state to reflect what Lima actually has.
//
// Only **declared** entries are touched. Lima's resolved lists also contain
// entries the base template contributed, and writing those into state would
// make every plan propose removing entries the user never wrote.
//
// This is what turns an out-of-band change into an ordinary Terraform diff:
// state stops matching configuration, a plan proposes an update, and Resize
// restores the declared entry in place. Before mounts became reconcilable the
// provider only warned, because the sole remedy then was destroying the VM.
//
// A declared entry whose location has disappeared is removed from state, so
// the plan shows it being added back.
func reconcileDeclaredEntries(ctx context.Context, m *instanceModel, lists declaredLists, inst lima.Instance) diag.Diagnostics {
	var diags diag.Diagnostics

	switch inst.Status() {
	case lima.StatusCreating, lima.StatusUnknown, lima.StatusBroken:
		// Lima has not resolved a configuration worth comparing against.
		return diags
	}

	if lists.MountsManaged {
		kept := make([]mountModel, 0, len(lists.Mounts))
		for _, declared := range lists.Mounts {
			observed, ok := observedMount(inst, declared)
			if !ok {
				// Gone entirely: drop it so the plan re-adds it.
				continue
			}
			kept = append(kept, observed)
		}
		diags.Append(setEntries(ctx, &m.Mounts, kept)...)
	}

	if lists.PortForwardsManaged {
		kept := make([]portModel, 0, len(lists.PortForwards))
		for _, declared := range lists.PortForwards {
			observed, ok := observedPortForward(inst, declared)
			if !ok {
				continue
			}
			kept = append(kept, observed)
		}
		diags.Append(setEntries(ctx, &m.PortForwards, kept)...)
	}

	return diags
}

// setEntries writes entries back into a list attribute.
//
// The element type is taken from the value being replaced rather than
// hand-written, so adding a nested attribute cannot leave a stale type behind
// here. Only a managed list is ever written, so its type is always available.
func setEntries[T any](ctx context.Context, list *types.List, entries []T) diag.Diagnostics {
	updated, diags := types.ListValueFrom(ctx, list.ElementType(ctx), entries)
	if !diags.HasError() {
		*list = updated
	}
	return diags
}

// observedMount returns the declared mount updated with Lima's view of it.
//
// The declared spelling of the location is preserved, so a configuration
// written as /tmp does not churn to /private/tmp. An unset mount_point stays
// unset while Lima's value is just the default (the location itself),
// otherwise a null would immediately become a diff.
func observedMount(inst lima.Instance, declared mountModel) (mountModel, bool) {
	if declared.Location.IsNull() || declared.Location.IsUnknown() {
		return declared, true
	}
	location, err := lima.ExpandPath(declared.Location.ValueString())
	if err != nil {
		return declared, true
	}

	found, ok := inst.Config.FindMount(location)
	if !ok {
		return mountModel{}, false
	}

	out := declared
	out.Writable = types.BoolValue(found.Writable)
	if declared.MountPoint.IsNull() {
		// Lima defaults mountPoint to the location; that is not a change.
		if !lima.SamePath(found.MountPoint, found.Location) {
			out.MountPoint = types.StringValue(found.MountPoint)
		}
	} else if !lima.SamePath(found.MountPoint, declared.MountPoint.ValueString()) {
		out.MountPoint = types.StringValue(found.MountPoint)
	}
	return out, true
}

// observedPortForward returns the declared forward updated with Lima's view.
func observedPortForward(inst lima.Instance, declared portModel) (portModel, bool) {
	if declared.GuestPort.IsNull() || declared.GuestPort.IsUnknown() {
		return declared, true
	}

	found, ok := inst.Config.FindPortForward(declared.GuestPort.ValueInt64(), stringValue(declared.Protocol))
	if !ok {
		return portModel{}, false
	}

	out := declared
	// An unset host_port means Lima chose one, so its value is not a change.
	if !declared.HostPort.IsNull() && declared.HostPort.ValueInt64() != found.HostPort {
		out.HostPort = types.Int64Value(found.HostPort)
	}
	return out, true
}

// configuredMounts converts the model's mount blocks into the domain type.
//
// Host paths are expanded the same way the create path expands them, so a
// mount recorded in state matches what Lima was told.
func configuredMounts(lists declaredLists) []lima.Mount {
	out := make([]lima.Mount, 0, len(lists.Mounts))
	for _, mount := range lists.Mounts {
		if mount.Location.IsNull() || mount.Location.IsUnknown() {
			continue
		}
		location, err := lima.ExpandPath(mount.Location.ValueString())
		if err != nil {
			continue
		}
		out = append(out, lima.Mount{
			Location:   location,
			MountPoint: stringValue(mount.MountPoint),
			Writable:   boolValue(mount.Writable),
		})
	}
	return out
}

// plannedMounts returns the configured mounts as a settable list.
//
// A nil result means the attribute is absent and Lima's own mounts must be left
// alone. An empty (but non-nil) list means the user removed every entry, which
// is a real request to unmount them.
func plannedMounts(lists declaredLists) *[]lima.Mount {
	if !lists.MountsManaged {
		return nil
	}
	mounts := configuredMounts(lists)
	return &mounts
}

// configuredPortForwards converts the model's port_forward blocks.
func configuredPortForwards(lists declaredLists) []lima.PortForward {
	out := make([]lima.PortForward, 0, len(lists.PortForwards))
	for _, pf := range lists.PortForwards {
		if pf.GuestPort.IsNull() || pf.GuestPort.IsUnknown() {
			continue
		}
		entry := lima.PortForward{
			GuestPort: pf.GuestPort.ValueInt64(),
			Proto:     stringValue(pf.Protocol),
			GuestIP:   stringValue(pf.GuestIP),
			HostIP:    stringValue(pf.HostIP),
		}
		if !pf.HostPort.IsNull() && !pf.HostPort.IsUnknown() {
			entry.HostPort = pf.HostPort.ValueInt64()
		}
		out = append(out, entry)
	}
	return out
}

// plannedPortForwards returns the configured forwards as a settable list.
func plannedPortForwards(lists declaredLists) *[]lima.PortForward {
	if !lists.PortForwardsManaged {
		return nil
	}
	forwards := configuredPortForwards(lists)
	return &forwards
}

// diskModel mirrors the lima_disk resource schema.
//
// Unlike an instance, everything about a disk is directly observable, so there
// is no "desired versus effective" split to maintain — except `format`, which
// Lima does not report back faithfully (see actual_format).
type diskModel struct {
	Name   types.String `tfsdk:"name"`
	Size   types.String `tfsdk:"size"`
	Format types.String `tfsdk:"format"`

	ID           types.String `tfsdk:"id"`
	ActualFormat types.String `tfsdk:"actual_format"`
	Dir          types.String `tfsdk:"dir"`
	MountPoint   types.String `tfsdk:"mount_point"`
	InUseBy      types.String `tfsdk:"in_use_by"`

	Timeouts timeouts.Value `tfsdk:"timeouts"`
}

// applyDisk copies observed state onto the model.
//
// `size` keeps whatever spelling was configured when it means the same number
// of bytes, for the same reason instance memory does: Terraform compares the
// raw configuration to prior state, so rewriting "10240MiB" to "10GiB" would
// make every plan dirty.
//
// `format` is never written back. Lima reports the format it stored, which on
// a vz host is raw even when qcow2 was requested; copying that into `format`
// would fight the configuration forever. It goes to actual_format instead.
func (m *diskModel) applyDisk(d lima.Disk) {
	m.ID = types.StringValue(d.Name)
	m.Name = types.StringValue(d.Name)
	m.Size = observedSize(m.Size, d.SizeBytes)
	m.ActualFormat = optionalString(d.Format)
	m.Dir = optionalString(d.Dir)
	m.MountPoint = optionalString(d.MountPoint)
	m.InUseBy = optionalString(d.Instance)
}

// diskDataSourceModel mirrors the lima_disk data source schema.
type diskDataSourceModel struct {
	Name       types.String `tfsdk:"name"`
	ID         types.String `tfsdk:"id"`
	Size       types.String `tfsdk:"size"`
	SizeBytes  types.Int64  `tfsdk:"size_bytes"`
	Format     types.String `tfsdk:"format"`
	Dir        types.String `tfsdk:"dir"`
	MountPoint types.String `tfsdk:"mount_point"`
	InUseBy    types.String `tfsdk:"in_use_by"`
}

func (m *diskDataSourceModel) applyDisk(d lima.Disk) {
	m.ID = types.StringValue(d.Name)
	m.Name = types.StringValue(d.Name)
	m.Size = types.StringValue(lima.FormatSize(d.SizeBytes))
	m.SizeBytes = types.Int64Value(d.SizeBytes)
	m.Format = optionalString(d.Format)
	m.Dir = optionalString(d.Dir)
	m.MountPoint = optionalString(d.MountPoint)
	m.InUseBy = optionalString(d.Instance)
}

// plannedDisks returns the configured disk attachments as a settable list.
//
// Nil means the attribute is unmanaged, so Lima's own attachments are left
// alone. An empty (non-nil) list means the user removed every attachment,
// which is a real request to detach.
func plannedDisks(m *instanceModel) *[]lima.AdditionalDisk {
	if m.AdditionalDisks.IsNull() || m.AdditionalDisks.IsUnknown() {
		return nil
	}
	names := make([]string, 0, len(m.AdditionalDisks.Elements()))
	for _, e := range m.AdditionalDisks.Elements() {
		if s, ok := e.(types.String); ok && !s.IsNull() && !s.IsUnknown() {
			names = append(names, s.ValueString())
		}
	}
	disks := make([]lima.AdditionalDisk, 0, len(names))
	for _, n := range names {
		disks = append(disks, lima.AdditionalDisk{Name: n})
	}
	return &disks
}

// applyAttachedDisks records the disks Lima reports attached.
//
// Only written when the attribute is managed, so an unset additional_disks
// does not adopt whatever a raw config or template attached.
func (m *instanceModel) applyAttachedDisks(ctx context.Context, inst lima.Instance) diag.Diagnostics {
	var diags diag.Diagnostics
	if m.AdditionalDisks.IsNull() || m.AdditionalDisks.IsUnknown() {
		return diags
	}
	list, d := stringList(ctx, inst.Config.AttachedDiskNames())
	diags.Append(d...)
	if !diags.HasError() {
		m.AdditionalDisks = list
	}
	return diags
}
