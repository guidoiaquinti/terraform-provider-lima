// Copyright 2026 Guido Iaquinti
// SPDX-License-Identifier: Apache-2.0

package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/guidoiaquinti/terraform-provider-lima/internal/lima"
)

// Attribute names, shared by the schema, the diagnostic paths that point into
// these lists, and the tests that drive them.
const (
	attrMounts       = "mounts"
	attrPortForwards = "port_forwards"
	attrProvisions   = "provisions"

	attrConfig          = "config"
	attrConfigOverrides = "config_overrides"

	// attrInstanceName is the real Lima name, and the resource's identity now
	// that there is no `id`.
	attrInstanceName = "instance_name"
)

var (
	_ resource.Resource                   = (*instanceResource)(nil)
	_ resource.ResourceWithConfigure      = (*instanceResource)(nil)
	_ resource.ResourceWithImportState    = (*instanceResource)(nil)
	_ resource.ResourceWithValidateConfig = (*instanceResource)(nil)
)

type instanceResource struct {
	data *providerData
}

// NewInstanceResource returns the lima_instance resource.
func NewInstanceResource() resource.Resource { return &instanceResource{} }

func (r *instanceResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_instance"
}

func (r *instanceResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.data = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (r *instanceResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	// RequiresReplace is used for every attribute Lima cannot change safely
	// in place without an interactive editor. See the mutability table in
	// docs/resources/instance.md, which mirrors these modifiers exactly.
	// Replacement fires only on a real change between two known values, so
	// adopting an imported instance and un-managing an attribute are both
	// non-destructive. See ReplaceOnRealChange.
	replace := []planmodifier.String{ReplaceOnRealChange()}

	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a local Lima virtual machine instance through `limactl`.\n\n" +
			"The provider generates a single Lima YAML document from the chosen template or raw configuration, " +
			"the typed attributes below, and `config_overrides`, then passes it to `limactl create`. " +
			"It never edits files inside `LIMA_HOME` directly.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Logical instance name. The real Lima instance name is this value prefixed with the provider's `name_prefix`, " +
					"and is exposed as `instance_name`. Changing this forces a new instance.",
				// name is the one exception: it is never null, so plain
				// RequiresReplace is correct and unambiguous.
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:    []validator.String{InstanceName()},
			},
			"template": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Lima template to build on, such as `template:ubuntu` or `template:docker`, or a path to a local template file. " +
					"Rendered as a `base:` entry so typed attributes layer cleanly on top. " +
					"Conflicts with `config`. Changing this forces a new instance.",
				PlanModifiers: replace,
				Validators: []validator.String{
					NonEmpty("template"),
					stringvalidator.ConflictsWith(path.MatchRoot(attrConfig)),
				},
			},
			"config": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Complete Lima YAML configuration, used instead of `template`. " +
					"Typed attributes and `config_overrides` are merged over it. " +
					"Stored normalised, so whitespace and key-order changes do not produce a diff. " +
					"Conflicts with `template`. Changing this forces a new instance.\n\n" +
					"Not marked sensitive, so changes to it are reviewable in a plan — which is the point of " +
					"keeping a VM definition in version control. Lima YAML is configuration, not a credential " +
					"store; if you do embed a secret here it will appear in plan output and in state, so pass it " +
					"through a `provision` script from a sensitive variable instead.",
				PlanModifiers: replace,
				Validators:    []validator.String{YAML("config")},
			},
			attrConfigOverrides: schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "YAML fragment merged last, as an escape hatch for Lima options without a typed attribute. " +
					"Mappings merge key by key, sequences are replaced wholesale, and an explicit `null` removes a key. " +
					"An empty sequence is a removal too, so `mounts: []` and `mounts: null` both clear the list. " +
					"Changing this forces a new instance.\n\n" +
					"Removing a key here is the only way to drop something a `template` contributed, " +
					"such as the home directory mount most stock templates bring in: `mounts: null` leaves the " +
					"instance with no mounts at all. Because this layer is merged last, it also clears whatever a " +
					"typed attribute set.\n\n" +
					"Not marked sensitive, for the same reason as `config`.",
				PlanModifiers: replace,
				Validators:    []validator.String{YAML(attrConfigOverrides)},
			},
			"vm_type": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Lima VM backend, for example `vz` or `qemu`. " +
					"Unrecognised values produce a warning rather than an error, so a newer Lima backend can be used without a provider upgrade. " +
					"Changing this forces a new instance.",
				PlanModifiers: replace,
				Validators:    []validator.String{KnownValue("vm_type", lima.KnownVMTypes)},
			},
			"arch": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Machine architecture, for example `aarch64` or `x86_64`. Changing this forces a new instance.",
				PlanModifiers:       replace,
				Validators:          []validator.String{KnownValue("arch", lima.KnownArches)},
			},
			"cpus": schema.Int64Attribute{
				Optional: true,
				MarkdownDescription: "Number of virtual CPUs. Must be greater than zero. " +
					"Changing this is applied **in place** via `limactl edit`. " +
					"Because Lima cannot edit a running instance, a running VM is stopped, reconfigured and started again, which means brief downtime.",
				Validators: []validator.Int64{int64validator.AtLeast(1)},
			},
			"memory": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Memory size, for example `4GiB` or `8192MiB`. Sizes are compared by byte count, so equivalent spellings do not differ. " +
					"Changing this is applied **in place** via `limactl edit`, stopping and restarting a running instance.",
				Validators: []validator.String{Size("memory")},
			},
			"disk": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Primary disk size, for example `50GiB`. " +
					"Growing is applied **in place** via `limactl edit`, stopping and restarting a running instance. " +
					"Shrinking is rejected at plan time, because Lima cannot shrink a disk.",
				PlanModifiers: []planmodifier.String{DiskGrowOnly()},
				Validators:    []validator.String{Size("disk")},
			},
			"start": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(true),
				MarkdownDescription: "Whether the instance should be running after apply. " +
					"Defaults to `true`. Changing this starts or stops the instance **in place**, without replacement.",
			},
			"protect": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(false),
				MarkdownDescription: "Whether Lima's deletion protection is enabled, via `limactl protect`. " +
					"Applied **in place**. While `true`, `terraform destroy` fails with an explanatory error rather than silently removing protection.",
			},
			"additional_disks": schema.ListAttribute{
				Optional:    true,
				ElementType: types.StringType,
				MarkdownDescription: "Names of `lima_disk` disks to attach, in order. " +
					"Each is mounted in the guest at the disk's `mount_point`, normally `/mnt/lima-<name>`.\n\n" +
					"Changing this is applied **in place**, stopping and restarting a running instance. " +
					"A disk is locked while the instance holding it runs, so detach it here before " +
					"destroying or resizing the `lima_disk`.",
			},
			// The three lists below are nested *attributes* rather than blocks.
			// A list attribute takes an ordinary expression, so deriving entries
			// from data is a `for` comprehension rather than a `dynamic` block:
			//
			//	mounts = [for d in var.shared_dirs : { location = d, writable = true }]
			//
			// They are lists rather than sets because Lima treats their order as
			// significant and reports them back in the order it was given.
			attrMounts: schema.ListNestedAttribute{
				Optional: true,
				MarkdownDescription: "Host directories shared into the guest, in order. " +
					"Changing them is applied **in place** via `limactl edit`, which stops and restarts a running instance.\n\n" +
					"These are **added to** the mounts the base template contributes, which for most stock templates " +
					"includes your home directory. Setting this to `[]` therefore shares nothing extra rather than " +
					"sharing nothing at all: an empty list cannot remove what the template brought in. " +
					"To have no mounts, clear them in `config_overrides` with `mounts: null`.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"location": schema.StringAttribute{
							Required: true,
							MarkdownDescription: "Absolute host path to share. A leading `~` is expanded and the path is normalised, " +
								"without resolving symlinks, so the value stays stable across plans.",
							Validators: []validator.String{AbsolutePath()},
						},
						"mount_point": schema.StringAttribute{
							Optional: true,
							MarkdownDescription: "Guest path to mount at. Defaults to Lima's behaviour of reusing `location`. " +
								"Lima rejects guest system paths such as `/etc` or `/usr`, and on macOS a `/tmp` mount needs an explicit value here.",
							Validators: []validator.String{AbsolutePath()},
						},
						"writable": schema.BoolAttribute{
							Optional:            true,
							Computed:            true,
							Default:             booldefault.StaticBool(false),
							MarkdownDescription: "Whether the guest may write to the mount. Defaults to `false`.",
						},
					},
				},
			},
			attrPortForwards: schema.ListNestedAttribute{
				Optional: true,
				MarkdownDescription: "Guest ports forwarded to the host, in order. " +
					"Changing them is applied **in place** via `limactl edit`, which stops and restarts a running instance. " +
					"Like `mounts`, these are added to what the base template contributes; " +
					"`config_overrides` with `portForwards: null` is what removes those.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"guest_port": schema.Int64Attribute{
							Required:            true,
							MarkdownDescription: "Port inside the guest, 1-65535.",
							Validators:          []validator.Int64{int64validator.Between(1, 65535)},
						},
						"host_port": schema.Int64Attribute{
							Optional:            true,
							MarkdownDescription: "Port on the host, 1-65535. Defaults to Lima's own choice when omitted.",
							Validators:          []validator.Int64{int64validator.Between(1, 65535)},
						},
						"protocol": schema.StringAttribute{
							Optional:            true,
							Computed:            true,
							Default:             stringdefault.StaticString("tcp"),
							MarkdownDescription: "Protocol, `tcp` or `udp`. Defaults to `tcp`.",
							Validators:          []validator.String{OneOf("protocol", []string{"tcp", "udp"})},
						},
						"guest_ip": schema.StringAttribute{
							Optional:            true,
							MarkdownDescription: "Guest-side bind address. Leave unset unless you know the guest networking mode provides it.",
						},
						"host_ip": schema.StringAttribute{
							Optional:            true,
							MarkdownDescription: "Host-side bind address, for example `0.0.0.0` to expose the forward beyond loopback.",
						},
					},
				},
			},
			attrProvisions: schema.ListNestedAttribute{
				Optional: true,
				MarkdownDescription: "Native Lima provisioning steps, in order. These run during instance creation, not on every apply. " +
					"Changing any of them forces a new instance.",
				PlanModifiers: []planmodifier.List{listplanmodifier.RequiresReplace()},
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"mode": schema.StringAttribute{
							Optional:            true,
							Computed:            true,
							Default:             stringdefault.StaticString("system"),
							MarkdownDescription: "Lima provisioning mode. Defaults to `system`.",
							Validators:          []validator.String{KnownValue("provisioning mode", lima.KnownProvisionModes)},
						},
						"script": schema.StringAttribute{
							Required:            true,
							Sensitive:           true,
							MarkdownDescription: "Script body. Treated as sensitive and never echoed in diagnostics or logs.",
							Validators:          []validator.String{NonEmpty("script")},
						},
						"rerun_token": schema.StringAttribute{
							Optional: true,
							MarkdownDescription: "Arbitrary value whose change forces a new instance, typically `filesha256(...)`. " +
								"Because Lima runs provisioning at creation time, this is how you request a re-run.",
						},
					},
				},
			},
			"timeouts": timeouts.AttributesAll(ctx),

			// Computed.
			//
			// There is deliberately no `id`. It held exactly the same value as
			// instance_name for the whole life of the resource, so it was two
			// attributes for one fact. terraform-plugin-framework does not
			// require one.
			attrInstanceName: schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "The real Lima instance name, that is `name_prefix` followed by `name`. " +
					"This is also the import ID.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"status": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "Normalised instance status: one of " + statusVocabulary() + ". " +
					"A Lima status the provider does not recognise maps to `unknown`, with the original preserved in `raw_status`.",
			},
			"raw_status": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The instance status exactly as Lima reported it, for example `Running`.",
			},
			"ssh_address": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "Host address for SSH, normally `127.0.0.1`. " +
					"For a stopped instance this is the last known value, not proof of a live endpoint.",
			},
			"ssh_port": schema.Int64Attribute{
				Computed: true,
				MarkdownDescription: "Host port forwarded to the guest SSH service. " +
					"For a stopped instance this is the last known value.",
			},
			"ssh_user": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Guest login name, taken from Lima's resolved configuration.",
				// Only changeable through `config`, which requires
				// replacement.
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"ssh_config": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "Path to the SSH configuration file Lima generates for this instance, usable as `ssh -F <path> <hostname>`. " +
					"This is a path; no private key material is stored in state.",
				// A path under the instance directory: stable for the
				// instance's lifetime.
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"hostname": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Guest hostname Lima assigns, normally `lima-<instance_name>`.",
				// Derived from the instance name, which requires replacement
				// to change, so it can never move during an update. Holding
				// it steady keeps update plans free of pointless
				// "(known after apply)" noise.
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"dir": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Lima's directory for this instance inside `LIMA_HOME`. Reported for diagnostics; the provider never writes to it.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"config_hash": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "SHA-256 hash of the effective generated Lima configuration. " +
					"Because generation is deterministic, this changes only when the configuration meaningfully changes.",
			},
			"lima_version": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The Lima version recorded against this instance.",
				// Recorded when the instance was created; an update never
				// changes it.
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
		},
	}
}

// ValidateConfig performs the cross-attribute checks the schema cannot express.
func (r *instanceResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var config instanceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	lists, diags := config.declared(ctx)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	prefix, home := "", ""
	if r.data != nil {
		prefix, home = r.data.NamePrefix, r.data.Home
	}
	resp.Diagnostics.Append(validateInstanceConfig(&config, lists, prefix, home)...)
}

// validateInstanceConfig holds the plan-time cross-attribute checks.
//
// It is a plain function over the model so it can be unit tested without
// constructing Terraform framework plumbing. It never runs a command, so it
// works with `terraform validate` and with no Lima installed.
//
// A list that is still unknown is reported by lists.Unknown, and the checks that
// turn on "nothing is set" are skipped rather than guessed at.
func validateInstanceConfig(config *instanceModel, lists declaredLists, namePrefix, home string) diag.Diagnostics {
	var diags diag.Diagnostics

	templateKnown := !config.Template.IsUnknown()
	configKnown := !config.Config.IsUnknown()
	hasTemplate := !config.Template.IsNull() && templateKnown
	hasConfig := !config.Config.IsNull() && configKnown

	// Exactly one instance source is expected. Neither is an error only when
	// no typed attribute is set either, because an instance with no image and
	// no settings would fail confusingly inside Lima.
	//
	// An unknown source, or an unresolved list that might carry a typed
	// attribute, means the question cannot be answered yet. Saying nothing is
	// the only honest option: warning here fired on every plan whose `config`
	// came from a variable or another resource.
	sourceKnown := templateKnown && configKnown && !lists.Unknown
	if !hasTemplate && !hasConfig && sourceKnown {
		hasTyped := !config.VMType.IsNull() || !config.Arch.IsNull() || !config.CPUs.IsNull() ||
			!config.Memory.IsNull() || !config.Disk.IsNull() ||
			len(lists.Mounts) > 0 || len(lists.PortForwards) > 0 || len(lists.Provisions) > 0 ||
			!config.ConfigOverrides.IsNull()
		if !hasTyped {
			diags.AddError(
				"Missing instance source",
				"Set either \"template\" (for example template:ubuntu) or \"config\" (raw Lima YAML) so Lima knows which image to use.\n\n"+
					"Run `limactl create --list-templates` to see the available templates.",
			)
			return diags
		}
		diags.AddWarning(
			"No template or config set",
			"Neither \"template\" nor \"config\" is set, so the instance relies entirely on Lima's built-in defaults. "+
				"Set \"template\" to pin the base image explicitly.",
		)
	}

	// Plain mode makes Lima ignore mounts and port forwarding outright, and
	// skip the guest agent that implements forwarding. Nothing fails: the
	// settings simply have no effect, and the only symptom is a service that
	// cannot be reached from the host.
	if len(lists.Mounts) > 0 || len(lists.PortForwards) > 0 {
		for _, doc := range []struct {
			attribute string
			value     types.String
		}{
			{attrConfig, config.Config},
			{attrConfigOverrides, config.ConfigOverrides},
		} {
			if doc.value.IsNull() || doc.value.IsUnknown() {
				continue
			}
			if plain, known := lima.PlainMode(doc.value.ValueString()); known && plain {
				diags.AddAttributeWarning(
					path.Root(doc.attribute),
					"Plain mode ignores mounts and port forwarding",
					fmt.Sprintf("%q sets \"plain: true\", so Lima ignores the mounts and port_forwards "+
						"declared here and does not start the guest agent that implements forwarding. "+
						"They will apply cleanly and have no effect.\n\n"+
						"Remove \"plain: true\" to use them, or drop the entries. Everything plain disables "+
						"can be disabled individually instead: mounts: [] and containerd off.", doc.attribute),
				)
				break
			}
		}
	}

	// Duplicate port forwards are almost certainly a mistake, and Lima's own
	// error for the resulting conflict is much harder to act on.
	seenGuest := map[string]int{}
	seenHost := map[string]int{}
	for i, pf := range lists.PortForwards {
		if pf.GuestPort.IsNull() || pf.GuestPort.IsUnknown() {
			continue
		}
		proto := "tcp"
		if !pf.Protocol.IsNull() && !pf.Protocol.IsUnknown() {
			proto = pf.Protocol.ValueString()
		}

		guestKey := fmt.Sprintf("%s/%d/%s", proto, pf.GuestPort.ValueInt64(), stringValue(pf.GuestIP))
		if prev, dup := seenGuest[guestKey]; dup {
			diags.AddAttributeError(
				path.Root(attrPortForwards).AtListIndex(i).AtName("guest_port"),
				"Duplicate port forward",
				fmt.Sprintf("Guest port %d/%s is already forwarded by port_forwards[%d].",
					pf.GuestPort.ValueInt64(), proto, prev),
			)
		} else {
			seenGuest[guestKey] = i
		}

		if pf.HostPort.IsNull() || pf.HostPort.IsUnknown() {
			continue
		}
		hostKey := fmt.Sprintf("%s/%d/%s", proto, pf.HostPort.ValueInt64(), stringValue(pf.HostIP))
		if prev, dup := seenHost[hostKey]; dup {
			diags.AddAttributeError(
				path.Root(attrPortForwards).AtListIndex(i).AtName("host_port"),
				"Duplicate host port",
				fmt.Sprintf("Host port %d/%s is already used by port_forwards[%d]. "+
					"Two forwards cannot bind the same host port.",
					pf.HostPort.ValueInt64(), proto, prev),
			)
		} else {
			seenHost[hostKey] = i
		}
	}

	// Overlapping mount locations confuse Lima, so flag them before apply.
	seenMount := map[string]int{}
	for i, m := range lists.Mounts {
		if m.Location.IsNull() || m.Location.IsUnknown() {
			continue
		}
		expanded, err := lima.ExpandPath(m.Location.ValueString())
		if err != nil {
			continue
		}
		if prev, dup := seenMount[expanded]; dup {
			diags.AddAttributeError(
				path.Root(attrMounts).AtListIndex(i).AtName("location"),
				"Duplicate mount location",
				fmt.Sprintf("%q is already mounted by mounts[%d].", expanded, prev),
			)
		} else {
			seenMount[expanded] = i
		}
	}

	// A name valid on its own can still be invalid or too long once the
	// provider's prefix and LIMA_HOME are taken into account.
	if !config.Name.IsNull() && !config.Name.IsUnknown() {
		full := effectiveName(namePrefix, config.Name.ValueString())
		if err := lima.ValidateName(full); err != nil {
			diags.AddAttributeError(path.Root("name"),
				"Invalid Lima instance name",
				fmt.Sprintf("With the provider's name_prefix %q the real instance name is %q, which is invalid: %s",
					namePrefix, full, err))
		}
		if err := lima.ValidateNameForHome(full, home); err != nil {
			diags.AddAttributeError(path.Root("name"),
				"Instance name is too long for this LIMA_HOME", err.Error())
		}
	}

	return diags
}

func (r *instanceResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan instanceModel
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

	name := effectiveName(r.data.NamePrefix, plan.Name.ValueString())

	lists, diags := plan.declared(ctx)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	document, hash, diags := r.render(&plan, lists)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Collision detection is left to Service.Create, which checks under the
	// instance lock and returns ErrAlreadyExists. addCreateError turns that into
	// the same import instruction this function used to emit itself. Probing
	// first cost an extra full `limactl list` per create and could not be
	// authoritative anyway, since the answer can change before the create runs.
	tflog.Debug(ctx, "creating instance", map[string]any{"name": name, "config_hash": hash})

	result, err := r.data.Service.Create(ctx, lima.CreateParams{
		Name:     name,
		Document: document,
		Start:    boolValue(plan.Start),
		Protect:  boolValue(plan.Protect),
		Validate: true,
	})
	if err != nil {
		r.addCreateError(resp, name, result, err)
		// When Lima registered the instance despite the failure, state must
		// be written so a later destroy can clean it up. Without this the
		// instance would be orphaned outside Terraform's knowledge.
		if result.Registered {
			plan.applyInstance(r.bestEffortInstance(ctx, name))
			plan.ConfigHash = types.StringValue(hash)
			resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
		}
		return
	}

	plan.applyInstance(result.Instance)
	plan.ConfigHash = types.StringValue(hash)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *instanceResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state instanceModel
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

	name := r.stateName(&state)

	inst, err := r.data.Service.Get(ctx, name)
	if err != nil {
		if lima.IsNotFound(err) {
			// Deleted outside Terraform: drop it from state so the next plan
			// proposes recreation.
			tflog.Debug(ctx, "instance is gone, removing from state", map[string]any{"name": name})
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError(
			fmt.Sprintf("Unable to read Lima instance %q", name),
			formatCommandError(err))
		return
	}

	lists, diags := state.declared(ctx)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	state.applyInstance(inst)
	state.applyObservedConfig(inst)

	// Mounts and port forwards are reconcilable in place, so divergence is
	// written into state and surfaces as an ordinary plan diff rather than a
	// warning. See reconcileDeclaredEntries.
	resp.Diagnostics.Append(reconcileDeclaredEntries(ctx, &state, lists, inst)...)
	resp.Diagnostics.Append(state.applyAttachedDisks(ctx, inst)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// The reconciled lists are what the hash below must describe.
	lists, diags = state.declared(ctx)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// start reflects observed reality so an externally stopped instance shows as
	// drift, but only where Lima reports a state that settles the question. See
	// observedStart.
	state.Start = observedStart(state.Start, inst)

	// config_hash is derived from configuration, not from Lima, so it is
	// recomputed rather than read back. Recomputing keeps it correct after a
	// provider upgrade changes rendering.
	if _, hash, d := r.render(&state, lists); !d.HasError() {
		state.ConfigHash = types.StringValue(hash)
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *instanceResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state instanceModel
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

	name := r.stateName(&state)

	// Both sides are needed: the plan says what the lists should become, and
	// state says which resolved entries the provider previously owned, which is
	// how Resize tells them apart from the base template's.
	planLists, diags := plan.declared(ctx)
	resp.Diagnostics.Append(diags...)
	stateLists, d := state.declared(ctx)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Attributes reachable here: start, protect, cpus, memory, disk, mounts,
	// port_forwards and additional_disks. Everything else carries
	// RequiresReplace, so the framework routes it to a replacement instead.
	wantProtect := boolValue(plan.Protect)
	wantRunning := boolValue(plan.Start)

	// Protection is cleared before the rest of the update and reapplied after it,
	// so a protected instance can still be reconfigured in a single apply. That
	// means a failure in between leaves the flag already changed, so `protection`
	// tracks what is actually in effect and recordProtection writes it into state
	// on the way out. Without that, a failed apply left state claiming a
	// protection the instance no longer had.
	protection := boolValue(state.Protect)

	recordProtection := func(actual bool) {
		if actual == boolValue(state.Protect) {
			return
		}
		state.Protect = types.BoolValue(actual)
		resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
	}

	if !wantProtect && protection {
		if err := r.data.Service.SetProtection(ctx, name, false); err != nil {
			resp.Diagnostics.AddError(
				fmt.Sprintf("Unable to remove protection from Lima instance %q", name),
				formatCommandError(err))
			return
		}
		protection = false
	}

	// Adoption: the user is declaring a template for an instance that has
	// none recorded, which happens after an import. The claim cannot be
	// confirmed, but an obviously wrong one can be refuted.
	if state.Template.IsNull() && !plan.Template.IsNull() && !plan.Template.IsUnknown() {
		resp.Diagnostics.Append(r.verifyAdoptedTemplate(ctx, name, plan.Template.ValueString())...)
	}

	resize, diags := resizeRequest(&plan, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Resize subsumes the run-state change: it has to stop and restart the
	// instance anyway, so doing both in one operation avoids a needless
	// extra stop/start cycle.
	if err := r.data.Service.Resize(ctx, lima.ResizeParams{
		Name:                 name,
		Desired:              resize,
		WantRunning:          wantRunning,
		AdditionalDisks:      plannedDisks(&plan),
		Mounts:               plannedMounts(planLists),
		PreviousMounts:       configuredMounts(stateLists),
		PortForwards:         plannedPortForwards(planLists),
		PreviousPortForwards: configuredPortForwards(stateLists),
	}); err != nil {
		r.addResizeError(resp, name, resize, wantRunning, err)

		// A failed restart is not a failed change: the edit applied, and only
		// bringing the instance back up did not. State has to say so, or the next
		// plan proposes applying resources the instance already has, and anyone
		// reading state sees values that are no longer true.
		var restartErr *lima.RestartAfterEditError
		if errors.As(err, &restartErr) {
			r.recordAppliedResize(ctx, resp, &plan, planLists, name, protection)
			return
		}

		recordProtection(protection)
		return
	}

	if wantProtect && !protection {
		if err := r.data.Service.SetProtection(ctx, name, true); err != nil {
			resp.Diagnostics.AddError(
				fmt.Sprintf("Unable to protect Lima instance %q", name),
				formatCommandError(err))
			recordProtection(protection)
			return
		}
		protection = true
	}

	inst, err := r.data.Service.Get(ctx, name)
	if err != nil {
		resp.Diagnostics.AddError(
			fmt.Sprintf("Unable to read Lima instance %q after updating it", name),
			formatCommandError(err)+
				"\n\nThe update itself may have succeeded. Run `terraform refresh` to reconcile state.")
		recordProtection(protection)
		return
	}

	_, hash, diags := r.render(&plan, planLists)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	plan.applyInstance(inst)
	plan.ConfigHash = types.StringValue(hash)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *instanceResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state instanceModel
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

	name := r.stateName(&state)

	err := r.data.Service.Delete(ctx, name)
	if err == nil {
		return
	}

	if lima.IsProtected(err) {
		// Never silently unprotect: the flag exists precisely to stop an
		// accidental destroy.
		resp.Diagnostics.AddError(
			fmt.Sprintf("Unable to delete protected Lima instance %q", name),
			fmt.Sprintf("The instance is protected against removal, so Lima refused to delete it.\n\n"+
				"The provider does not remove protection automatically, because doing so would defeat its purpose.\n\n"+
				"To allow deletion, either set protect = false and apply first:\n\n"+
				"    resource \"lima_instance\" \"%s\" {\n      protect = false\n    }\n\n"+
				"or remove protection manually:\n\n    limactl unprotect %s",
				state.Name.ValueString(), name))
		return
	}

	resp.Diagnostics.AddError(
		fmt.Sprintf("Unable to delete Lima instance %q", name),
		formatCommandError(err)+"\n\n"+r.troubleshoot(name, "delete"))
}

// ImportState imports an existing instance by its real Lima name.
func (r *instanceResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if r.data == nil {
		resp.Diagnostics.AddError("Provider not configured",
			"The Lima provider must be configured before importing.")
		return
	}

	actual := strings.TrimSpace(req.ID)
	if actual == "" {
		resp.Diagnostics.AddError("Invalid import ID",
			"The import ID must be the Lima instance name, for example:\n\n    terraform import lima_instance.example project-dev")
		return
	}

	inst, err := r.data.Service.Get(ctx, actual)
	if err != nil {
		if lima.IsNotFound(err) {
			resp.Diagnostics.AddError(
				fmt.Sprintf("Lima instance %q not found", actual),
				fmt.Sprintf("No instance named %q exists in %s.\n\n"+
					"Import IDs are the real Lima instance name, including any name_prefix. "+
					"List the available instances with:\n\n    limactl list", actual, homeLabel(r.data)))
			return
		}
		resp.Diagnostics.AddError(
			fmt.Sprintf("Unable to read Lima instance %q", actual),
			formatCommandError(err))
		return
	}

	// The import ID is the real name; `name` is derived by stripping the
	// prefix so it round-trips back to the same instance.
	logical := logicalName(r.data.NamePrefix, actual)

	// Attributes are set individually rather than by writing a whole model.
	//
	// Constructing an instanceModel would require a timeouts.Value, and its
	// zero value carries an empty object type that fails the framework's type
	// check on import.
	//
	// Everything Lima *reports* is populated, so a configuration matching
	// reality plans clean. Nothing is guessed: each value below comes
	// straight from `limactl list --format json`.
	attrs := []struct {
		path  path.Path
		value attr.Value
	}{
		{path.Root(attrInstanceName), types.StringValue(inst.Name)},
		{path.Root("name"), types.StringValue(logical)},
		{path.Root("start"), types.BoolValue(inst.Status() == lima.StatusRunning)},
		{path.Root("protect"), types.BoolValue(inst.Protected)},
		{path.Root("vm_type"), optionalString(inst.VMType)},
		{path.Root("arch"), optionalString(inst.Arch)},
	}

	if inst.CPUs > 0 {
		attrs = append(attrs, struct {
			path  path.Path
			value attr.Value
		}{path.Root("cpus"), types.Int64Value(inst.CPUs)})
	}
	if inst.MemoryBytes > 0 {
		attrs = append(attrs, struct {
			path  path.Path
			value attr.Value
		}{path.Root("memory"), types.StringValue(lima.FormatSize(inst.MemoryBytes))})
	}
	if inst.DiskBytes > 0 {
		attrs = append(attrs, struct {
			path  path.Path
			value attr.Value
		}{path.Root("disk"), types.StringValue(lima.FormatSize(inst.DiskBytes))})
	}

	for _, a := range attrs {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, a.path, a.value)...)
	}
	if resp.Diagnostics.HasError() {
		return
	}

	// template, config and config_overrides stay null: Lima's resolved
	// configuration does not record which template an instance came from, and
	// its defaults are indistinguishable from explicit settings. Declaring one
	// afterwards adopts the instance rather than replacing it, because those
	// attributes use ReplaceOnRealChange.

	resp.Diagnostics.AddWarning("Review the imported configuration", importWarningDetail(logical, actual))
}

// importWarningDetail explains what import recorded and what declaring the rest
// will do.
//
// The claim about each list attribute has to match its plan modifiers. An
// earlier revision said all three forced replacement, which stopped being true
// once mounts and port forwards became in-place edits, and the warning then
// discouraged users from a feature the provider had shipped.
// TestImportWarningMatchesReplacementBehaviour derives the expectation from the
// schema so the two cannot diverge again.
func importWarningDetail(logical, actual string) string {
	return fmt.Sprintf("Imported Lima instance %q as name = %q.\n\n"+
		"Everything Lima reports has been recorded: cpus, memory, disk, vm_type, arch, "+
		"start and protect. Write those values in your configuration and `terraform plan` "+
		"will be clean.\n\n"+
		"\"template\", \"config\" and \"config_overrides\" were left unset, because Lima does not "+
		"record which template an instance came from. Declaring one adopts the instance "+
		"without recreating it.\n\n"+
		"%q and %q were left unset too, because Lima's resolved lists do not distinguish "+
		"the entries a user asked for from the ones a template contributed. Declaring them is "+
		"applied in place on the next apply, which stops the instance, reconfigures it and "+
		"starts it again.\n\n"+
		"%q was also left unset. Declaring it forces a new instance, because Lima runs "+
		"provisioning only at creation time and offers no supported way to re-run it.",
		actual, logical, attrMounts, attrPortForwards, attrProvisions)
}

// render generates the effective document and its hash.
func (r *instanceResource) render(m *instanceModel, lists declaredLists) ([]byte, string, diag.Diagnostics) {
	var diags diag.Diagnostics

	req, d := m.toRenderRequest(lists)
	diags.Append(d...)
	if diags.HasError() {
		return nil, "", diags
	}

	doc, err := lima.Render(req)
	if err != nil {
		diags.AddError("Unable to generate the Lima configuration",
			// The error may quote the user's YAML, which can be sensitive;
			// Render's messages are structural and do not echo values.
			err.Error())
		return nil, "", diags
	}
	return doc, lima.HashDocument(doc), diags
}

// stateName returns the real Lima name recorded in state, falling back to
// re-deriving it from the logical name.
func (r *instanceResource) stateName(m *instanceModel) string {
	if !m.InstanceName.IsNull() && m.InstanceName.ValueString() != "" {
		return m.InstanceName.ValueString()
	}
	return effectiveName(r.data.NamePrefix, m.Name.ValueString())
}

// troubleshoot suggests the command most likely to explain a failure.
func (r *instanceResource) troubleshoot(name, operation string) string {
	switch operation {
	case "start":
		return fmt.Sprintf("Inspect the instance and its boot logs with:\n\n    limactl list %s\n    limactl start --debug %s", name, name)
	case "stop":
		return fmt.Sprintf("Check the current status, and force a stop if the guest is unresponsive:\n\n    limactl list %s\n    limactl stop --force %s", name, name)
	case "delete":
		return fmt.Sprintf(
			"Check whether the instance is still present, then remove it forcibly if required:\n\n"+
				"    limactl list %s\n    limactl delete --force %s", name, name)
	case "resize":
		return fmt.Sprintf(
			"Check the current resources and try the change directly:\n\n"+
				"    limactl list %s\n    limactl edit --tty=false --cpus N --memory G %s", name, name)
	default:
		return "Inspect the instance with:\n\n    limactl list " + name
	}
}

// recordAppliedResize writes state after a reconfiguration that succeeded but
// left the instance down.
//
// The plan's configuration values are what Lima was given and accepted, so they
// are recorded as-is. Everything else comes from a fresh read, and `start`
// reflects what is actually true rather than what was asked for — the whole point
// is that the instance is not running.
func (r *instanceResource) recordAppliedResize(
	ctx context.Context,
	resp *resource.UpdateResponse,
	plan *instanceModel,
	lists declaredLists,
	name string,
	protection bool,
) {
	inst := r.bestEffortInstance(ctx, name)
	plan.applyInstance(inst)

	// applyInstance takes protect from the instance it read; if that read failed
	// it is a placeholder, so the value tracked through the update wins.
	plan.Protect = types.BoolValue(protection)

	// Default to stopped, since the restart is what failed, and let a definitive
	// observation override it.
	plan.Start = observedStart(types.BoolValue(false), inst)

	if _, hash, d := r.render(plan, lists); !d.HasError() {
		plan.ConfigHash = types.StringValue(hash)
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

// bestEffortInstance reads an instance, returning a minimal placeholder when
// the read itself fails. Used only on the partial-creation path, where losing
// the name would orphan the VM.
func (r *instanceResource) bestEffortInstance(ctx context.Context, name string) lima.Instance {
	if inst, err := r.data.Service.Get(ctx, name); err == nil {
		return inst
	}
	return lima.Instance{Name: name, RawStatus: ""}
}

func (r *instanceResource) addCreateError(resp *resource.CreateResponse, name string, result lima.CreateResult, err error) {
	title := fmt.Sprintf("Unable to create Lima instance %q", name)

	if lima.IsAlreadyExists(err) {
		resp.Diagnostics.AddError(title,
			fmt.Sprintf("An instance with that name already exists in %s.\n\n"+
				"Import it instead:\n\n    terraform import lima_instance.example %s", homeLabel(r.data), name))
		return
	}

	body := formatCommandError(err)
	if lima.IsTimeout(err) {
		body = err.Error() + "\n\nThe instance may still be starting. " +
			"Increase the create timeout, or check progress with:\n\n    limactl list " + name
	}

	if result.Registered {
		// Being explicit about the leftover instance is essential: the
		// provider deliberately does not delete it.
		body += fmt.Sprintf("\n\nLima registered the instance %q before failing, and the provider has "+
			"deliberately left it in place rather than deleting a VM that may hold data.\n\n"+
			"Terraform has recorded it in state, so `terraform destroy` will remove it. "+
			"To remove it manually instead:\n\n    limactl delete --force %s", name, name)
	}

	resp.Diagnostics.AddError(title, body)
}

// verifyAdoptedTemplate warns when a declared template cannot be the one an
// instance was built from.
//
// This is a warning rather than an error on purpose. The check compares disk
// images, which refutes a wrong claim but cannot confirm a right one, and a
// user adopting an instance may reasonably not know its exact origin. Failing
// the apply would make import harder for no safety gain, since the template
// only affects a future replacement.
func (r *instanceResource) verifyAdoptedTemplate(ctx context.Context, name, template string) diag.Diagnostics {
	var diags diag.Diagnostics

	inst, err := r.data.Service.Get(ctx, name)
	if err != nil {
		// Not worth failing an apply over; the real read happens later.
		return diags
	}

	match, err := r.data.Service.VerifyTemplate(ctx, template, inst)
	if err != nil {
		diags.AddAttributeWarning(path.Root("template"),
			"Could not verify the declared template",
			fmt.Sprintf("Resolving %q to compare it against instance %q failed, so the declaration "+
				"was accepted unchecked.\n\n%s", template, name, formatCommandError(err)))
		return diags
	}

	if match == lima.TemplateMismatch {
		diags.AddAttributeWarning(path.Root("template"),
			"Declared template does not match the instance",
			fmt.Sprintf("Instance %q uses none of the disk images that %q resolves to, so it was "+
				"almost certainly not created from that template.\n\n"+
				"The declaration has been recorded anyway, because Lima does not track which template "+
				"an instance came from and the value only takes effect if the instance is later "+
				"replaced. At that point it would be rebuilt from %q.\n\n"+
				"Compare them with:\n\n"+
				"    limactl list --format json %s\n"+
				"    limactl template copy --fill %s -",
				name, template, template, name, template))
	}

	return diags
}

// resizeRequest computes the in-place change between state and plan.
//
// Only attributes that actually differ are included, so an update touching
// only `start` produces an empty request and no edit is attempted. Sizes are
// compared as byte counts rather than strings, so "8GiB" and "8192MiB" are
// correctly seen as the same value.
//
// An attribute the user has removed from configuration (null in the plan but
// set in state) is deliberately *not* reverted: Lima has no notion of
// "unset back to the default" for an existing instance, and guessing what the
// default would have been could silently shrink a VM.
func resizeRequest(plan, state *instanceModel) (lima.EditRequest, diag.Diagnostics) {
	var diags diag.Diagnostics
	var req lima.EditRequest

	if !plan.CPUs.IsNull() && !plan.CPUs.IsUnknown() {
		if state.CPUs.IsNull() || plan.CPUs.ValueInt64() != state.CPUs.ValueInt64() {
			req.CPUs = plan.CPUs.ValueInt64()
		}
	}

	sizeChange := func(planned, current types.String, attribute string) int64 {
		if planned.IsNull() || planned.IsUnknown() {
			return 0
		}
		want, err := lima.ParseSize(planned.ValueString())
		if err != nil {
			diags.AddAttributeError(path.Root(attribute), "Invalid "+attribute, err.Error())
			return 0
		}
		if !current.IsNull() {
			if have, err := lima.ParseSize(current.ValueString()); err == nil && have == want {
				return 0
			}
		}
		return want
	}

	req.MemoryBytes = sizeChange(plan.Memory, state.Memory, "memory")
	req.DiskBytes = sizeChange(plan.Disk, state.Disk, "disk")

	return req, diags
}

// addResizeError turns a resize failure into an actionable diagnostic.
func (r *instanceResource) addResizeError(resp *resource.UpdateResponse, name string, req lima.EditRequest, wantRunning bool, err error) {
	// The instance was reconfigured but could not be restarted. This is the
	// one case where the user must know the change *did* apply, or they may
	// wrongly assume nothing happened and reapply something different.
	var restartErr *lima.RestartAfterEditError
	if errors.As(err, &restartErr) {
		resp.Diagnostics.AddError(
			fmt.Sprintf("Lima instance %q was reconfigured but could not be restarted", name),
			fmt.Sprintf("%s\n\n"+
				"The configuration change was applied successfully, so the instance now has the requested "+
				"resources — it is simply stopped.\n\n"+
				"Run `terraform apply` again to retry the start, or investigate with:\n\n"+
				"    limactl list %s\n    limactl start --debug %s",
				formatCommandError(restartErr.Cause), name, name))
		return
	}

	if !req.IsEmpty() {
		hint := ""
		if wantRunning {
			hint = "\n\nLima cannot reconfigure a running instance, so the provider stops it, applies the change " +
				"and starts it again. The instance may currently be stopped."
		}
		resp.Diagnostics.AddError(
			fmt.Sprintf("Unable to reconfigure Lima instance %q", name),
			formatCommandError(err)+hint+"\n\n"+r.troubleshoot(name, "resize"))
		return
	}

	operation := "start"
	if !wantRunning {
		operation = "stop"
	}
	resp.Diagnostics.AddError(
		fmt.Sprintf("Unable to %s Lima instance %q", operation, name),
		formatCommandError(err)+"\n\n"+r.troubleshoot(name, operation))
}
