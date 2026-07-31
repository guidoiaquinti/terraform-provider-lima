// Copyright 2026 Guido Iaquinti
// SPDX-License-Identifier: Apache-2.0

package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/datasource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/guidoiaquinti/terraform-provider-lima/internal/lima"
)

var (
	_ datasource.DataSource              = (*hostDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*hostDataSource)(nil)
)

type hostDataSource struct {
	data *providerData
}

// NewHostDataSource returns the lima_host data source.
func NewHostDataSource() datasource.DataSource { return &hostDataSource{} }

func (d *hostDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_host"
}

func (d *hostDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.data = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *hostDataSource) Schema(ctx context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	// Every attribute here is reported directly by `limactl info` or is
	// already known to the provider. Capabilities that cannot be determined
	// reliably are omitted rather than guessed: there is no "supports
	// snapshots" flag, for instance, because Lima exposes no way to detect it
	// short of attempting the operation.
	resp.Schema = schema.Schema{
		MarkdownDescription: "Reports the capabilities of the local Lima installation, as detected by `limactl info`.\n\n" +
			"Only values Lima actually reports are exposed; nothing is inferred from the host platform.",
		Attributes: map[string]schema.Attribute{
			// No `id`: it held the resolved limactl path, which `binary_path` already
			// reports under a name that says what it is.
			"lima_version": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Lima version reported by `limactl info`.",
			},
			"host_os": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Host operating system as Lima reports it, for example `darwin` or `linux`.",
			},
			"host_arch": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Host architecture as Lima reports it, for example `aarch64` or `x86_64`.",
			},
			"vm_types": schema.ListAttribute{
				Computed:    true,
				ElementType: types.StringType,
				MarkdownDescription: "VM backends this Lima build supports on this host, for example `[\"qemu\", \"vz\", \"krunkit\"]`. " +
					"Detected, not hardcoded per platform.",
			},
			"lima_home": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The `LIMA_HOME` Lima is using, which reflects the provider's `home` setting when one is configured.",
			},
			"binary_path": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Absolute path to the `limactl` executable the provider resolved.",
			},
			"templates": schema.ListAttribute{
				Computed:    true,
				ElementType: types.StringType,
				MarkdownDescription: "Names of the templates available for `template = \"template:<name>\"`. " +
					"Lima's internal composition fragments, whose names begin with `_`, are excluded.",
			},
			"instance_names": schema.ListAttribute{
				Computed:            true,
				ElementType:         types.StringType,
				MarkdownDescription: "Names of every instance currently present in `LIMA_HOME`, whether or not Terraform manages them.",
			},
			"timeouts": timeouts.Attributes(ctx),
		},
	}
}

func (d *hostDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config hostDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() || d.data == nil {
		return
	}

	timeout, diags := config.Timeouts.Read(ctx, d.data.timeout(lima.DefaultTimeouts.Read))
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	info, err := d.data.Client.Info(ctx)
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to read Lima host information",
			formatCommandError(err)+
				"\n\nVerify the installation with:\n\n    "+d.data.Binary+" info")
		return
	}

	instances, err := d.data.Client.List(ctx)
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to list Lima instances",
			formatCommandError(err))
		return
	}
	names := make([]string, 0, len(instances))
	for _, inst := range instances {
		names = append(names, inst.Name)
	}

	config.LimaVersion = types.StringValue(info.Version)
	config.HostOS = optionalString(info.HostOS)
	config.HostArch = optionalString(info.HostArch)
	config.LimaHome = optionalString(info.LimaHome)
	config.BinaryPath = types.StringValue(d.data.Binary)

	vmTypes, diags := stringList(ctx, info.VMTypes)
	resp.Diagnostics.Append(diags...)
	templates, diags := stringList(ctx, info.TemplateNames())
	resp.Diagnostics.Append(diags...)
	instanceNames, diags := stringList(ctx, names)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	config.VMTypes = vmTypes
	config.Templates = templates
	config.InstanceNames = instanceNames

	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}
