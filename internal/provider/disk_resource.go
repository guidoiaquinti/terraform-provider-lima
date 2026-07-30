package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/guidoiaquinti/terraform-provider-lima/internal/lima"
)

var (
	_ resource.Resource                   = (*diskResource)(nil)
	_ resource.ResourceWithConfigure      = (*diskResource)(nil)
	_ resource.ResourceWithImportState    = (*diskResource)(nil)
	_ resource.ResourceWithValidateConfig = (*diskResource)(nil)
)

type diskResource struct {
	data *providerData
}

// NewDiskResource returns the lima_disk resource.
func NewDiskResource() resource.Resource { return &diskResource{} }

func (r *diskResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_disk"
}

func (r *diskResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.data = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (r *diskResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages an additional Lima disk through `limactl disk`.\n\n" +
			"Additional disks exist independently of instances and can be attached to one with the " +
			"`additional_disks` attribute of `lima_instance`. A disk is locked while the instance " +
			"holding it is **running**, and cannot be resized or destroyed until that instance stops.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Disk name, which is also the import ID. " +
					"Unlike `lima_instance`, the provider's `name_prefix` is **not** applied: disks are " +
					"referenced by their real name in an instance's `additional_disks`. " +
					"Changing this forces a new disk.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:    []validator.String{InstanceName()},
			},
			"size": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Disk size, for example `50GiB`. " +
					"Growing is applied **in place**. Shrinking is rejected at plan time, because Lima " +
					"cannot shrink a disk and replacing it would destroy its contents.",
				PlanModifiers: []planmodifier.String{DiskGrowOnly()},
				Validators:    []validator.String{Size("size")},
			},
			"format": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Disk format requested at creation, for example `qcow2` or `raw`. " +
					"Changing this forces a new disk.\n\n" +
					"Lima may store a different format than requested — the `vz` driver requires raw " +
					"images, so a `qcow2` request is converted. This attribute records what was asked " +
					"for; see `actual_format` for what Lima reports.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:    []validator.String{KnownValue("format", []string{"qcow2", "raw"})},
			},
			"timeouts": timeouts.Attributes(ctx, timeouts.Opts{
				Create: true,
				Update: true,
				Delete: true,
				Read:   true,
			}),

			// Computed.
			// There is deliberately no `id`. Lima exposes no object identifier of its
			// own — `limactl list --list-fields` has none — so an `id` here could only
			// repeat `name`, which is Lima's actual primary key.
			"actual_format": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "The format Lima reports for the stored image, which may differ from " +
					"the requested `format`.",
			},
			"dir": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The disk's directory inside `LIMA_HOME`, normally `_disks/<name>`.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"mount_point": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "Guest path the disk is mounted at when attached, which Lima defaults " +
					"to `/mnt/lima-<name>`.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"in_use_by": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "Name of the **running** instance currently holding this disk, or null. " +
					"A disk attached to a stopped instance reports null, because Lima only locks it while " +
					"the instance runs.",
			},
		},
	}
}

// ValidateConfig checks what the schema cannot express on its own.
func (r *diskResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var config diskModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// A disk's directory sits under LIMA_HOME/_disks, so it does not hit the
	// socket-path limit instance names do. Only the name's own shape matters,
	// and the schema validator covers that.
	if config.Size.IsNull() || config.Size.IsUnknown() {
		return
	}
	if _, err := lima.ParseSize(config.Size.ValueString()); err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("size"), "Invalid disk size", err.Error())
	}
}

func (r *diskResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan diskModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() || r.data == nil {
		return
	}

	timeout, diags := plan.Timeouts.Create(ctx, r.data.timeout(lima.DefaultTimeouts.Create))
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	name := plan.Name.ValueString()
	size, err := lima.ParseSize(plan.Size.ValueString())
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("size"), "Invalid disk size", err.Error())
		return
	}

	tflog.Debug(ctx, "creating Lima disk", map[string]any{"name": name, "size_bytes": size})

	disk, err := r.data.Disks.Create(ctx, lima.CreateDiskRequest{
		Name:      name,
		SizeBytes: size,
		Format:    stringValue(plan.Format),
	})
	if err != nil {
		if errors.Is(err, lima.ErrDiskExists) {
			resp.Diagnostics.AddError(
				fmt.Sprintf("Unable to create Lima disk %q", name),
				fmt.Sprintf("A disk with that name already exists in %s.\n\n"+
					"Import it instead:\n\n    terraform import lima_disk.example %s",
					homeLabel(r.data), name))
			return
		}
		resp.Diagnostics.AddError(
			fmt.Sprintf("Unable to create Lima disk %q", name),
			formatCommandError(err)+"\n\nList the existing disks with:\n\n    limactl disk list")
		return
	}

	plan.applyDisk(disk)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *diskResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state diskModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() || r.data == nil {
		return
	}

	timeout, diags := state.Timeouts.Read(ctx, r.data.timeout(lima.DefaultTimeouts.Read))
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	name := state.Name.ValueString()

	disk, err := r.data.Disks.Get(ctx, name)
	if err != nil {
		if lima.IsDiskNotFound(err) {
			tflog.Debug(ctx, "disk is gone, removing from state", map[string]any{"name": name})
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError(
			fmt.Sprintf("Unable to read Lima disk %q", name),
			formatCommandError(err))
		return
	}

	state.applyDisk(disk)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *diskResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state diskModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() || r.data == nil {
		return
	}

	timeout, diags := plan.Timeouts.Update(ctx, r.data.timeout(lima.DefaultTimeouts.Update))
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	name := state.Name.ValueString()

	// size is the only mutable attribute; everything else replaces.
	size, err := lima.ParseSize(plan.Size.ValueString())
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("size"), "Invalid disk size", err.Error())
		return
	}

	if err := r.data.Disks.Resize(ctx, name, size); err != nil {
		r.addResizeError(resp, name, err)
		return
	}

	disk, err := r.data.Disks.Get(ctx, name)
	if err != nil {
		resp.Diagnostics.AddError(
			fmt.Sprintf("Unable to read Lima disk %q after resizing it", name),
			formatCommandError(err)+
				"\n\nThe resize itself may have succeeded. Run `terraform refresh` to reconcile state.")
		return
	}

	plan.applyDisk(disk)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *diskResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state diskModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() || r.data == nil {
		return
	}

	timeout, diags := state.Timeouts.Delete(ctx, r.data.timeout(lima.DefaultTimeouts.Delete))
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	name := state.Name.ValueString()

	err := r.data.Disks.Delete(ctx, name)
	if err == nil {
		return
	}

	if errors.Is(err, lima.ErrDiskInUse) {
		resp.Diagnostics.AddError(
			fmt.Sprintf("Unable to delete Lima disk %q", name),
			fmt.Sprintf("%s\n\n"+
				"Lima locks a disk while the instance holding it is running, and the provider does not "+
				"stop other people's instances to get around that.\n\n"+
				"Stop the instance first — set `start = false` on it and apply, or run "+
				"`limactl stop %s` — then destroy the disk.",
				diskInUseDetail(err), holdingInstance(err)))
		return
	}

	resp.Diagnostics.AddError(
		fmt.Sprintf("Unable to delete Lima disk %q", name),
		formatCommandError(err)+
			"\n\nCheck whether it is still present with:\n\n    limactl disk list")
}

// ImportState imports an existing disk by name.
func (r *diskResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if r.data == nil {
		resp.Diagnostics.AddError("Provider not configured",
			"The Lima provider must be configured before importing.")
		return
	}

	name := strings.TrimSpace(req.ID)
	if name == "" {
		resp.Diagnostics.AddError("Invalid import ID",
			"The import ID must be the Lima disk name, for example:\n\n    terraform import lima_disk.example data")
		return
	}

	disk, err := r.data.Disks.Get(ctx, name)
	if err != nil {
		if lima.IsDiskNotFound(err) {
			resp.Diagnostics.AddError(
				fmt.Sprintf("Lima disk %q not found", name),
				fmt.Sprintf("No disk named %q exists in %s.\n\n"+
					"List the available disks with:\n\n    limactl disk list", name, homeLabel(r.data)))
			return
		}
		resp.Diagnostics.AddError(
			fmt.Sprintf("Unable to read Lima disk %q", name),
			formatCommandError(err))
		return
	}

	// Everything a disk has is observable, so unlike an instance there is
	// nothing to leave unset. `format` is the one exception: Lima reports what
	// it stored, not what was requested, so recording it would fight a
	// configuration that asked for qcow2 on a vz host.
	for _, a := range []struct {
		path  path.Path
		value attr.Value
	}{
		{path.Root("name"), types.StringValue(disk.Name)},
		{path.Root("size"), types.StringValue(lima.FormatSize(disk.SizeBytes))},
		{path.Root("actual_format"), optionalString(disk.Format)},
		{path.Root("dir"), optionalString(disk.Dir)},
		{path.Root("mount_point"), optionalString(disk.MountPoint)},
		{path.Root("in_use_by"), optionalString(disk.Instance)},
	} {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, a.path, a.value)...)
	}
}

func (r *diskResource) addResizeError(resp *resource.UpdateResponse, name string, err error) {
	if errors.Is(err, lima.ErrDiskInUse) {
		resp.Diagnostics.AddError(
			fmt.Sprintf("Unable to resize Lima disk %q", name),
			fmt.Sprintf("%s\n\n"+
				"Lima cannot resize a disk while the instance holding it is running, and the provider "+
				"does not stop other people's instances to get around that.\n\n"+
				"Stop the instance first — set `start = false` on it and apply, or run "+
				"`limactl stop %s` — then apply the new size.",
				diskInUseDetail(err), holdingInstance(err)))
		return
	}

	resp.Diagnostics.AddError(
		fmt.Sprintf("Unable to resize Lima disk %q", name),
		formatCommandError(err))
}

// diskInUseDetail renders the in-use error without its sentinel prefix.
func diskInUseDetail(err error) string {
	msg := err.Error()
	if _, rest, ok := strings.Cut(msg, lima.ErrDiskInUse.Error()+": "); ok {
		return "The disk " + rest + "."
	}
	return msg
}

// holdingInstance extracts the instance name from an in-use error, for the
// suggested command. It falls back to a placeholder rather than guessing.
func holdingInstance(err error) string {
	msg := err.Error()
	const marker = "running instance "
	if _, rest, ok := strings.Cut(msg, marker); ok {
		return strings.Trim(strings.Fields(rest)[0], `"`)
	}
	return "INSTANCE"
}
