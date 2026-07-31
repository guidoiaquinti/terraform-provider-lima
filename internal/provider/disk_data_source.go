// Copyright 2026 Guido Iaquinti
// SPDX-License-Identifier: Apache-2.0

package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/datasource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"

	"github.com/guidoiaquinti/terraform-provider-lima/internal/lima"
)

var (
	_ datasource.DataSource              = (*diskDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*diskDataSource)(nil)
)

type diskDataSource struct {
	data *providerData
}

// NewDiskDataSource returns the lima_disk data source.
func NewDiskDataSource() datasource.DataSource { return &diskDataSource{} }

func (d *diskDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_disk"
}

func (d *diskDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.data = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *diskDataSource) Schema(ctx context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Reads an existing Lima disk. This data source never modifies the disk.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "The disk name, exactly as `limactl disk list` reports it.",
				Validators:          []validator.String{InstanceName()},
			},
			// There is deliberately no `id`. Lima exposes no object identifier of its
			// own — `limactl list --list-fields` has none — so an `id` here could only
			// repeat `name`, which is Lima's actual primary key.
			"size": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Disk size in IEC notation, for example `50GiB`.",
			},
			"size_bytes": schema.Int64Attribute{
				Computed:            true,
				MarkdownDescription: "Disk size in bytes, as Lima reports it.",
			},
			"format": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "The format Lima reports for the stored image. Note that this is what " +
					"was stored, not necessarily what was requested: the `vz` driver requires raw images.",
			},
			"dir": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The disk's directory inside `LIMA_HOME`.",
			},
			"mount_point": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Guest path the disk is mounted at when attached.",
			},
			"in_use_by": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "Name of the **running** instance holding this disk, or null. " +
					"A disk attached to a stopped instance reports null.",
			},
			"timeouts": timeouts.Attributes(ctx),
		},
	}
}

func (d *diskDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config diskDataSourceModel
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

	name := config.Name.ValueString()

	disk, err := d.data.Disks.Get(ctx, name)
	if err != nil {
		if lima.IsDiskNotFound(err) {
			resp.Diagnostics.AddError(
				fmt.Sprintf("Lima disk %q not found", name),
				fmt.Sprintf("No disk named %q exists in %s.\n\n"+
					"List the available disks with:\n\n    limactl disk list", name, homeLabel(d.data)))
			return
		}
		resp.Diagnostics.AddError(
			fmt.Sprintf("Unable to read Lima disk %q", name),
			formatCommandError(err))
		return
	}

	config.applyDisk(disk)
	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}
