// Package provider implements the Terraform provider for Lima.
//
// It is the top layer of a three-layer design:
//
//	provider / resource / data source   (this package)
//	          ↓
//	Lima domain and lifecycle service   (internal/lima, Service)
//	          ↓
//	limactl command adapter             (internal/lima, ExecClient)
//
// Nothing in this package constructs a command line. Everything it needs is
// reached through lima.Service or lima.Client.
package provider

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/guidoiaquinti/terraform-provider-lima/internal/lima"
)

// Environment variables recognised by the provider. Each mirrors a schema
// attribute; explicit configuration always wins over the environment.
const (
	// EnvBinary overrides the limactl path.
	EnvBinary = "LIMA_PROVIDER_BINARY"
	// EnvHome overrides LIMA_HOME. This is Lima's own variable, so setting
	// it affects limactl on the command line too.
	EnvHome = "LIMA_HOME"
	// EnvNamePrefix overrides name_prefix.
	EnvNamePrefix = "LIMA_PROVIDER_NAME_PREFIX"
	// EnvDefaultTimeout overrides default_timeout.
	EnvDefaultTimeout = "LIMA_PROVIDER_DEFAULT_TIMEOUT"
)

// Ensure the implementation satisfies the framework interfaces.
//
// Deliberately not provider.ProviderWithFunctions: the provider ships no
// provider-defined functions, and asserting the interface to return nil
// advertises a capability that resolves to an empty set. Add the assertion
// back together with the first real function.
var _ provider.Provider = (*limaProvider)(nil)

type limaProvider struct {
	// version is set at build time and reported to Terraform.
	version string
}

// New returns a provider constructor for the given build version.
func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &limaProvider{version: version}
	}
}

// providerData is handed to every resource and data source.
type providerData struct {
	// Service is the lifecycle layer used for mutations.
	Service *lima.Service
	// Client is the raw adapter, used by read-only data sources.
	Client lima.Client
	// Disks is the disk lifecycle layer. It shares the instance service's
	// keyed mutex, so an operation on disk:<name> cannot race another.
	Disks *lima.DiskService
	// Version is the detected Lima version.
	Version lima.Version
	// Home is the resolved LIMA_HOME, empty when Lima's default applies.
	Home string
	// Binary is the resolved limactl path.
	Binary string
	// NamePrefix is prepended to instance names the provider creates.
	NamePrefix string
	// DefaultTimeout is the provider-wide override for every operation that
	// sets no explicit timeout. Zero means none was configured, in which case
	// each operation uses its own default from lima.DefaultTimeouts.
	DefaultTimeout time.Duration
}

// timeout returns the provider-wide default when one was configured, and the
// per-operation default otherwise.
//
// The distinction matters in both directions. Without a configured value, a
// refresh must not be given the budget a VM creation needs; with one, the user
// has asked for a single number to govern everything.
//
// A nil receiver is valid: Terraform calls schema and validation methods before
// Configure runs, so resources reach for this with no provider data yet.
func (d *providerData) timeout(perOperation time.Duration) time.Duration {
	if d != nil && d.DefaultTimeout > 0 {
		return d.DefaultTimeout
	}
	return perOperation
}

// providerModel mirrors the provider schema.
type providerModel struct {
	Binary         types.String `tfsdk:"binary"`
	Home           types.String `tfsdk:"home"`
	Environment    types.Map    `tfsdk:"environment"`
	DefaultTimeout types.String `tfsdk:"default_timeout"`
	NamePrefix     types.String `tfsdk:"name_prefix"`
}

func (p *limaProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "lima"
	resp.Version = p.version
}

func (p *limaProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages local [Lima](https://lima-vm.io/) virtual machines through the supported `limactl` command line interface.\n\n" +
			"This provider orchestrates Lima through the supported `limactl` lifecycle. " +
			"It does not replace Lima or manage Lima's internal files directly.",
		Attributes: map[string]schema.Attribute{
			"binary": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Path to the `limactl` executable. " +
					"Defaults to `limactl` resolved from `PATH`. " +
					"May also be set with the `" + EnvBinary + "` environment variable. " +
					"The provider verifies that the file exists and is executable, and runs a version check, during configuration.",
			},
			"home": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Value to pass to Lima as `LIMA_HOME`. " +
					"May also be set with the `" + EnvHome + "` environment variable. " +
					"A leading `~` is expanded and the path is normalised. " +
					"The provider never creates or removes this directory itself; only Lima does. " +
					"When unset, Lima's own default (`~/.lima`) applies.",
			},
			"environment": schema.MapAttribute{
				Optional:    true,
				ElementType: types.StringType,
				MarkdownDescription: "Additional environment variables for every `limactl` invocation. " +
					"These are merged over the inherited process environment and take precedence on conflict, " +
					"including over `home`. Values are never written to logs or diagnostics.",
			},
			"default_timeout": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Overrides the timeout of every operation that does not set its own, as a Go duration such as `20m` or `1h30m`. " +
					"When unset, each operation uses its own default: `" + lima.DefaultTimeouts.Create.String() + "` to create, " +
					"`" + lima.DefaultTimeouts.Update.String() + "` to update, `" + lima.DefaultTimeouts.Delete.String() + "` to delete " +
					"and `" + lima.DefaultTimeouts.Read.String() + "` to read. " +
					"Setting this applies the same value to all four, so keep in mind that it raises the budget for a refresh as well as for a slow VM creation. " +
					"May also be set with the `" + EnvDefaultTimeout + "` environment variable.",
				// Catches a malformed duration during `terraform validate`,
				// before the provider is configured. Configure still parses the
				// value, because it also has to handle the environment variable,
				// which no schema validator can see.
				Validators: []validator.String{Duration()},
			},
			"name_prefix": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Prefix applied to the `name` of every managed instance to form the real Lima instance name. " +
					"Defaults to an empty string. " +
					"May also be set with the `" + EnvNamePrefix + "` environment variable. " +
					"Import IDs are always the **real** Lima instance name, prefix included; " +
					"see the `lima_instance` resource documentation for details.",
			},
		},
	}
}

func (p *limaProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var config providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// An unknown value at this point means it depends on another resource,
	// which cannot work for provider configuration.
	for name, attr := range map[string]types.String{
		"binary":          config.Binary,
		"home":            config.Home,
		"default_timeout": config.DefaultTimeout,
		"name_prefix":     config.NamePrefix,
	} {
		if attr.IsUnknown() {
			resp.Diagnostics.AddAttributeError(
				path.Root(name),
				"Unknown provider configuration value",
				fmt.Sprintf("The provider cannot be configured because %q is not known until apply. "+
					"Provider configuration must not depend on values produced by other resources. "+
					"Set %q to a literal value, or supply it through an environment variable.", name, name),
			)
		}
	}
	if resp.Diagnostics.HasError() {
		return
	}

	binary := stringOrEnv(config.Binary, EnvBinary)
	home := stringOrEnv(config.Home, EnvHome)
	namePrefix := stringOrEnv(config.NamePrefix, EnvNamePrefix)

	// Left at zero when nothing was configured, which is what lets each
	// operation fall back to its own default rather than to one shared number.
	var timeout time.Duration
	if raw := stringOrEnv(config.DefaultTimeout, EnvDefaultTimeout); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			resp.Diagnostics.AddAttributeError(
				path.Root("default_timeout"),
				"Invalid default_timeout",
				fmt.Sprintf("%q is not a valid Go duration: %s\n\n"+
					"Use a value such as \"20m\", \"1h\" or \"90s\".", raw, err),
			)
			return
		}
		if parsed <= 0 {
			resp.Diagnostics.AddAttributeError(
				path.Root("default_timeout"),
				"Invalid default_timeout",
				fmt.Sprintf("%q must be greater than zero.", raw),
			)
			return
		}
		timeout = parsed
	}

	env := map[string]string{}
	if !config.Environment.IsNull() && !config.Environment.IsUnknown() {
		var raw map[string]types.String
		resp.Diagnostics.Append(config.Environment.ElementsAs(ctx, &raw, false)...)
		if resp.Diagnostics.HasError() {
			return
		}
		for k, v := range raw {
			if v.IsUnknown() {
				resp.Diagnostics.AddAttributeError(
					path.Root("environment"),
					"Unknown environment value",
					fmt.Sprintf("The value for environment key %q is not known until apply. "+
						"Provider configuration must not depend on values produced by other resources.", k),
				)
				return
			}
			if v.IsNull() {
				continue
			}
			env[k] = v.ValueString()
		}
	}

	if namePrefix != "" {
		// The prefix becomes part of a real Lima instance name, so it has to
		// satisfy the same character rules.
		if err := lima.ValidateName(namePrefix + "x"); err != nil {
			resp.Diagnostics.AddAttributeError(
				path.Root("name_prefix"),
				"Invalid name_prefix",
				fmt.Sprintf("%q cannot be used as an instance name prefix: %s", namePrefix, err),
			)
			return
		}
	}

	client, err := lima.NewExecClient(lima.Options{Binary: binary, Home: home, Env: env})
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to locate the limactl executable",
			fmt.Sprintf("The Lima provider could not use the configured limactl binary: %s\n\n"+
				"Install Lima (https://lima-vm.io/docs/installation/), or set the provider's \"binary\" "+
				"attribute or the %s environment variable to the full path of limactl.", err, EnvBinary),
		)
		return
	}

	version, err := client.DetectVersion(ctx)
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to determine the Lima version",
			fmt.Sprintf("Running %q to detect the Lima version failed.\n\n%s\n\n"+
				"Verify that the binary is a working limactl by running:\n  %s --version",
				client.Binary(), formatCommandError(err), client.Binary()),
		)
		return
	}

	warning, err := lima.CheckVersion(version)
	if err != nil {
		resp.Diagnostics.AddError("Unsupported Lima version", err.Error())
		return
	}
	if warning != "" {
		resp.Diagnostics.AddWarning("Untested Lima version", warning)
	}

	tflog.Debug(ctx, "configured Lima provider", map[string]any{
		"binary":      client.Binary(),
		"home":        client.Home(),
		"version":     version.String(),
		"name_prefix": namePrefix,
		// Only the keys, never the values.
		"environment_keys": sortedKeys(env),
	})

	// One lock table across both services: an instance operation and a disk
	// operation take different keys, but two operations on the same disk must
	// still serialise.
	locks := lima.NewKeyedMutex()

	data := &providerData{
		Service:        lima.NewService(client, lima.WithLocks(locks)),
		Disks:          lima.NewDiskService(client, locks),
		Client:         client,
		Version:        version,
		Home:           client.Home(),
		Binary:         client.Binary(),
		NamePrefix:     namePrefix,
		DefaultTimeout: timeout,
	}
	resp.DataSourceData = data
	resp.ResourceData = data
}

func (p *limaProvider) Resources(context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewInstanceResource,
		NewDiskResource,
	}
}

func (p *limaProvider) DataSources(context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		NewInstanceDataSource,
		NewInstancesDataSource,
		NewHostDataSource,
		NewDiskDataSource,
	}
}

// stringOrEnv returns the configured value, falling back to an environment
// variable. Explicit configuration always wins, which is the idiomatic
// precedence for Terraform providers.
func stringOrEnv(v types.String, envKey string) string {
	if !v.IsNull() && !v.IsUnknown() {
		return v.ValueString()
	}
	return os.Getenv(envKey)
}

func sortedKeys(m map[string]string) []string {
	return slices.Sorted(maps.Keys(m))
}

// formatCommandError renders a Lima failure for a diagnostic body, keeping
// stderr sanitised and bounded.
func formatCommandError(err error) string {
	var ce *lima.CommandError
	if !asCommandError(err, &ce) {
		return err.Error()
	}

	var b strings.Builder
	if ce.ExitCode != 0 {
		fmt.Fprintf(&b, "limactl exited with code %d.\n", ce.ExitCode)
	} else {
		b.WriteString("limactl could not be executed.\n")
	}
	if details := ce.Details(); details != "" {
		b.WriteString("\nLima reported:\n")
		b.WriteString(details)
	} else if ce.Cause != nil {
		fmt.Fprintf(&b, "\n%v", ce.Cause)
	}
	return b.String()
}

// asCommandError finds a *lima.CommandError anywhere in err's chain.
func asCommandError(err error, target **lima.CommandError) bool {
	return errors.As(err, target)
}

// statusVocabulary renders the statuses the provider can report, for a schema
// description.
//
// Derived from lima.AllStatuses rather than written out, because the hand-written
// lists had drifted: both schemas and both documentation pages advertised
// `starting` and `stopping`, which nothing could ever return.
func statusVocabulary() string {
	labels := make([]string, 0, len(lima.AllStatuses))
	for _, s := range lima.AllStatuses {
		labels = append(labels, "`"+string(s)+"`")
	}
	if len(labels) < 2 {
		return strings.Join(labels, "")
	}
	return strings.Join(labels[:len(labels)-1], ", ") + " or " + labels[len(labels)-1]
}

// homeLabel describes the LIMA_HOME a diagnostic is talking about.
//
// Shared by every resource and data source, so the phrasing users see is
// identical wherever an object turns out to be missing.
func homeLabel(data *providerData) string {
	if data == nil || data.Home == "" {
		return "Lima's default LIMA_HOME"
	}
	return fmt.Sprintf("LIMA_HOME %q", data.Home)
}

// providerDataFrom extracts the shared provider data, reporting a clear
// diagnostic if the framework handed over something unexpected.
func providerDataFrom(raw any, diags interface{ AddError(string, string) }) *providerData {
	if raw == nil {
		// Terraform calls schema methods before Configure; returning nil is
		// expected and callers must handle it.
		return nil
	}
	data, ok := raw.(*providerData)
	if !ok {
		diags.AddError(
			"Unexpected provider configuration type",
			fmt.Sprintf("Expected *providerData, got %T. This is a bug in the Lima provider; please report it.", raw),
		)
		return nil
	}
	return data
}
