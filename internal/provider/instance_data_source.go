package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"

	"github.com/guidoiaquinti/terraform-provider-lima/internal/lima"
)

var (
	_ datasource.DataSource              = (*instanceDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*instanceDataSource)(nil)
)

type instanceDataSource struct {
	data *providerData
}

// NewInstanceDataSource returns the lima_instance data source.
func NewInstanceDataSource() datasource.DataSource { return &instanceDataSource{} }

func (d *instanceDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_instance"
}

func (d *instanceDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.data = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *instanceDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Reads an existing Lima instance. This data source never modifies the instance.\n\n" +
			"`name` is the **real** Lima instance name and is not affected by the provider's `name_prefix`, " +
			"so the same value works whether or not the instance is managed by Terraform.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "The Lima instance name to look up, exactly as `limactl list` reports it.",
				Validators:          []validator.String{InstanceName()},
			},
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The Lima instance name.",
			},
			"status": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "Normalised status: one of " + statusVocabulary() + ". " +
					"A Lima status the provider does not recognise maps to `unknown`, with the original preserved in `raw_status`.",
			},
			"raw_status": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The status exactly as Lima reported it, for example `Running`.",
			},
			"arch": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Machine architecture, for example `aarch64`.",
			},
			"vm_type": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "VM backend, for example `vz` or `qemu`.",
			},
			"cpus": schema.Int64Attribute{
				Computed:            true,
				MarkdownDescription: "Number of virtual CPUs.",
			},
			"memory": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Memory size in IEC notation, for example `4GiB`.",
			},
			"disk": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Primary disk size in IEC notation, for example `100GiB`.",
			},
			"ssh_address": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Host address for SSH. For a stopped instance this is the last known value, not a live endpoint.",
			},
			"ssh_port": schema.Int64Attribute{
				Computed:            true,
				MarkdownDescription: "Host port forwarded to guest SSH. For a stopped instance this is the last known value.",
			},
			"ssh_user": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Guest login name from Lima's resolved configuration.",
			},
			"ssh_config": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Path to the SSH configuration file Lima generates for this instance. No key material is exposed.",
			},
			"hostname": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Guest hostname, normally `lima-<name>`.",
			},
			"dir": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The instance directory inside `LIMA_HOME`.",
			},
			"protected": schema.BoolAttribute{
				Computed:            true,
				MarkdownDescription: "Whether Lima's deletion protection is enabled for this instance.",
			},
			"lima_version": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The Lima version recorded against this instance, falling back to the detected host version.",
			},
		},
	}
}

func (d *instanceDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config instanceDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() || d.data == nil {
		return
	}

	ctx, cancel := context.WithTimeout(ctx, d.data.timeout(lima.DefaultTimeouts.Read))
	defer cancel()

	name := config.Name.ValueString()

	// The data source reads through the client rather than the Service: no
	// locking is needed for an operation that cannot mutate anything.
	inst, err := d.data.Client.Inspect(ctx, name)
	if err != nil {
		if lima.IsNotFound(err) {
			resp.Diagnostics.AddError(
				fmt.Sprintf("Lima instance %q not found", name),
				fmt.Sprintf("No instance named %q exists in %s.\n\n"+
					"List the available instances with:\n\n    limactl list\n\n"+
					"Note that this data source takes the real Lima instance name; the provider's "+
					"name_prefix is not applied to it.",
					name, homeLabel(d.data)))
			return
		}
		resp.Diagnostics.AddError(
			fmt.Sprintf("Unable to read Lima instance %q", name),
			formatCommandError(err))
		return
	}

	config.applyInstance(inst, d.data.Version.String())
	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}
