package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/datasource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"

	"github.com/guidoiaquinti/terraform-provider-lima/internal/lima"
)

var (
	_ datasource.DataSource              = (*instancesDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*instancesDataSource)(nil)
)

type instancesDataSource struct {
	data *providerData
}

// NewInstancesDataSource returns the lima_instances data source.
func NewInstancesDataSource() datasource.DataSource { return &instancesDataSource{} }

func (d *instancesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_instances"
}

func (d *instancesDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.data = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *instancesDataSource) Schema(ctx context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	// No filter arguments. Terraform expressions already filter a list better
	// than a bespoke argument could, and every filter attribute would be another
	// thing to keep consistent with `lima_instance`:
	//
	//	[for i in data.lima_instances.all.instances : i.name if i.status == "running"]
	resp.Schema = schema.Schema{
		MarkdownDescription: "Reads every Lima instance in `LIMA_HOME`, whether or not Terraform manages it. " +
			"This data source never modifies anything.\n\n" +
			"Use it to discover instances; filter the result with an ordinary Terraform expression. " +
			"`lima_host` also reports instance names, but only the names — this reports the full state of each.",
		Attributes: map[string]schema.Attribute{
			"instances": schema.ListNestedAttribute{
				Computed: true,
				MarkdownDescription: "Every instance Lima reports, in the order `limactl list` returns them. " +
					"Each entry carries the same attributes as the `lima_instance` data source.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: instanceObservedAttributes(),
				},
			},
			"timeouts": timeouts.Attributes(ctx),
		},
	}
}

func (d *instancesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config instancesDataSourceModel
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

	// Read through the client rather than the Service: no locking is needed for
	// an operation that cannot mutate anything.
	instances, err := d.data.Client.List(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Unable to list Lima instances", formatCommandError(err))
		return
	}

	// An empty list rather than null, so a consumer can always iterate it.
	entries := make([]instanceEntryModel, 0, len(instances))
	for _, inst := range instances {
		var entry instanceEntryModel
		entry.applyInstance(inst, d.data.Version.String())
		entries = append(entries, entry)
	}
	config.Instances = entries

	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}
