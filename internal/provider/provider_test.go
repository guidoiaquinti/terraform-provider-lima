package provider

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	fwdatasource "github.com/hashicorp/terraform-plugin-framework/datasource"
	fwprovider "github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"

	"github.com/guidoiaquinti/terraform-provider-lima/internal/lima"
)

// testAccProtoV6ProviderFactories wires the real provider into the acceptance
// test framework.
var testAccProtoV6ProviderFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"lima": providerserver.NewProtocol6WithError(New("test")()),
}

func TestProviderSchema(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	p := New("test")()

	resp := &fwprovider.SchemaResponse{}
	p.Schema(ctx, fwprovider.SchemaRequest{}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("provider schema produced errors: %v", resp.Diagnostics)
	}

	want := []string{"binary", "home", "environment", "default_timeout", "name_prefix"}
	for _, name := range want {
		attr, ok := resp.Schema.Attributes[name]
		if !ok {
			t.Errorf("provider schema is missing the %q attribute", name)
			continue
		}
		if attr.IsRequired() {
			t.Errorf("provider attribute %q is required; every provider attribute should be optional", name)
		}
		if attr.GetMarkdownDescription() == "" {
			t.Errorf("provider attribute %q has no description", name)
		}
	}
	if len(resp.Schema.Attributes) != len(want) {
		t.Errorf("provider has %d attributes, want exactly %v", len(resp.Schema.Attributes), want)
	}
}

func TestProviderMetadata(t *testing.T) {
	t.Parallel()

	resp := &fwprovider.MetadataResponse{}
	New("1.2.3")().Metadata(context.Background(), fwprovider.MetadataRequest{}, resp)

	if resp.TypeName != "lima" {
		t.Errorf("TypeName = %q, want lima", resp.TypeName)
	}
	if resp.Version != "1.2.3" {
		t.Errorf("Version = %q, want 1.2.3", resp.Version)
	}
}

func TestProviderRegistersResourcesAndDataSources(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	p := New("test")()

	// Type names must match the documented addresses.
	gotResources := map[string]bool{}
	for _, fn := range p.Resources(ctx) {
		rResp := &fwresource.MetadataResponse{}
		fn().Metadata(ctx, fwresource.MetadataRequest{ProviderTypeName: "lima"}, rResp)
		gotResources[rResp.TypeName] = true
	}
	wantResources := []string{"lima_instance", "lima_disk"}
	for _, want := range wantResources {
		if !gotResources[want] {
			t.Errorf("resource %q is not registered; got %v", want, gotResources)
		}
	}
	if len(gotResources) != len(wantResources) {
		t.Errorf("provider registers %v, want exactly %v", gotResources, wantResources)
	}

	gotDataSources := map[string]bool{}
	for _, fn := range p.DataSources(ctx) {
		dResp := &fwdatasource.MetadataResponse{}
		fn().Metadata(ctx, fwdatasource.MetadataRequest{ProviderTypeName: "lima"}, dResp)
		gotDataSources[dResp.TypeName] = true
	}
	wantDataSources := []string{"lima_instance", "lima_host", "lima_disk"}
	for _, want := range wantDataSources {
		if !gotDataSources[want] {
			t.Errorf("data source %q is not registered; got %v", want, gotDataSources)
		}
	}
	if len(gotDataSources) != len(wantDataSources) {
		t.Errorf("provider registers %v, want exactly %v", gotDataSources, wantDataSources)
	}
}

func TestInstanceResourceSchema(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	resp := &fwresource.SchemaResponse{}
	NewInstanceResource().Schema(ctx, fwresource.SchemaRequest{}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("resource schema produced errors: %v", resp.Diagnostics)
	}

	// Every attribute must be documented; undocumented attributes silently
	// become undocumented provider surface.
	for name, attr := range resp.Schema.Attributes {
		if name == "timeouts" {
			continue
		}
		if attr.GetMarkdownDescription() == "" {
			t.Errorf("attribute %q has no description", name)
		}
	}

	computed := []string{
		"id", "instance_name", "status", "raw_status", "ssh_address", "ssh_port",
		"ssh_user", "ssh_config", "hostname", "dir", "config_hash", "lima_version",
	}
	for _, name := range computed {
		attr, ok := resp.Schema.Attributes[name]
		if !ok {
			t.Errorf("schema is missing the computed attribute %q", name)
			continue
		}
		if !attr.IsComputed() {
			t.Errorf("attribute %q should be computed", name)
		}
		if attr.IsRequired() {
			t.Errorf("computed attribute %q must not be required", name)
		}
	}

	if !resp.Schema.Attributes["name"].IsRequired() {
		t.Error("name should be required")
	}

	// Nested attributes rather than blocks, so a user can build them with a
	// `for` expression instead of a `dynamic` block.
	for _, name := range []string{attrMounts, attrPortForwards, attrProvisions} {
		attr, ok := resp.Schema.Attributes[name]
		if !ok {
			t.Errorf("schema is missing the %q attribute", name)
			continue
		}
		if _, ok := attr.(schema.ListNestedAttribute); !ok {
			t.Errorf("attribute %q is %T, want schema.ListNestedAttribute", name, attr)
		}
	}
	if len(resp.Schema.Blocks) != 0 {
		t.Errorf("schema declares blocks %v; the resource is attribute-only", resp.Schema.Blocks)
	}
}

func TestSensitiveAttributesAreMarked(t *testing.T) {
	t.Parallel()

	resp := &fwresource.SchemaResponse{}
	NewInstanceResource().Schema(context.Background(), fwresource.SchemaRequest{}, resp)

	// These can carry credentials or private configuration.
	for _, name := range []string{"config", "config_overrides"} {
		if !resp.Schema.Attributes[name].IsSensitive() {
			t.Errorf("attribute %q should be marked sensitive", name)
		}
	}

	// Provisioning script bodies must never appear in plan output.
	provisions, ok := resp.Schema.Attributes[attrProvisions].(schema.ListNestedAttribute)
	if !ok {
		t.Fatalf("%s has unexpected type %T", attrProvisions, resp.Schema.Attributes[attrProvisions])
	}
	script, ok := provisions.NestedObject.Attributes["script"]
	if !ok {
		t.Fatalf("%s has no script attribute", attrProvisions)
	}
	if !script.IsSensitive() {
		t.Errorf("%s[].script should be marked sensitive", attrProvisions)
	}
}

func TestNoSSHKeyMaterialInSchema(t *testing.T) {
	t.Parallel()

	resp := &fwresource.SchemaResponse{}
	NewInstanceResource().Schema(context.Background(), fwresource.SchemaRequest{}, resp)

	// State must never hold private keys. ssh_config is a path, which is
	// fine; anything named like key material is not.
	for name := range resp.Schema.Attributes {
		lower := strings.ToLower(name)
		if strings.Contains(lower, "private_key") || strings.Contains(lower, "identity_file") ||
			lower == "ssh_key" {
			t.Errorf("attribute %q suggests key material is stored in state", name)
		}
	}
}

func TestHostDataSourceSchema(t *testing.T) {
	t.Parallel()

	resp := &fwdatasource.SchemaResponse{}
	NewHostDataSource().Schema(context.Background(), fwdatasource.SchemaRequest{}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("host data source schema produced errors: %v", resp.Diagnostics)
	}
	for _, name := range []string{
		"id", "lima_version", "host_os", "host_arch", "vm_types",
		"lima_home", "binary_path", "templates", "instance_names",
	} {
		attr, ok := resp.Schema.Attributes[name]
		if !ok {
			t.Errorf("host data source is missing %q", name)
			continue
		}
		if !attr.IsComputed() {
			t.Errorf("host data source attribute %q should be computed", name)
		}
	}
}

func TestInstanceDataSourceSchema(t *testing.T) {
	t.Parallel()

	resp := &fwdatasource.SchemaResponse{}
	NewInstanceDataSource().Schema(context.Background(), fwdatasource.SchemaRequest{}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("instance data source schema produced errors: %v", resp.Diagnostics)
	}
	if !resp.Schema.Attributes["name"].IsRequired() {
		t.Error("the instance data source should require name")
	}
	for _, name := range []string{
		"status", "raw_status", "arch", "vm_type", "cpus", "memory", "disk",
		"ssh_address", "ssh_port", "ssh_user", "ssh_config", "hostname",
		"protected", "lima_version",
	} {
		if _, ok := resp.Schema.Attributes[name]; !ok {
			t.Errorf("instance data source is missing %q", name)
		}
	}
}

func TestEffectiveAndLogicalNames(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		prefix      string
		logical     string
		wantEffect  string
		importID    string
		wantLogical string
	}{
		{
			name:        "no prefix",
			prefix:      "",
			logical:     "dev",
			wantEffect:  "dev",
			importID:    "dev",
			wantLogical: "dev",
		},
		{
			name:        "prefix is applied and stripped symmetrically",
			prefix:      "acme-",
			logical:     "dev",
			wantEffect:  "acme-dev",
			importID:    "acme-dev",
			wantLogical: "dev",
		},
		{
			// Importing a name that does not carry the prefix must not
			// invent one, or the round trip would target a different VM.
			name:        "import of an unprefixed name keeps it whole",
			prefix:      "acme-",
			logical:     "other",
			wantEffect:  "acme-other",
			importID:    "legacy-vm",
			wantLogical: "legacy-vm",
		},
		{
			// Stripping must never produce an empty name.
			name:        "name identical to the prefix",
			prefix:      "acme-",
			logical:     "x",
			wantEffect:  "acme-x",
			importID:    "acme-",
			wantLogical: "acme-",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := effectiveName(tc.prefix, tc.logical); got != tc.wantEffect {
				t.Errorf("effectiveName(%q, %q) = %q, want %q", tc.prefix, tc.logical, got, tc.wantEffect)
			}
			if got := logicalName(tc.prefix, tc.importID); got != tc.wantLogical {
				t.Errorf("logicalName(%q, %q) = %q, want %q", tc.prefix, tc.importID, got, tc.wantLogical)
			}
		})
	}
}

func TestNamePrefixRoundTrip(t *testing.T) {
	t.Parallel()

	// The central guarantee: importing the real name of a managed instance
	// must yield a logical name that maps back to the same real name. This
	// is what prevents double-prefixing.
	for _, prefix := range []string{"", "acme-", "team.", "x_"} {
		for _, logical := range []string{"dev", "web1", "a"} {
			actual := effectiveName(prefix, logical)
			back := logicalName(prefix, actual)
			if again := effectiveName(prefix, back); again != actual {
				t.Errorf("round trip failed for prefix %q name %q: %q -> %q -> %q",
					prefix, logical, actual, back, again)
			}
		}
	}
}

func TestStringOrEnv(t *testing.T) {
	const key = "LIMA_PROVIDER_TEST_VALUE"

	t.Run("environment is used when the attribute is null", func(t *testing.T) {
		t.Setenv(key, "from-env")
		if got := stringOrEnv(nullString(), key); got != "from-env" {
			t.Errorf("stringOrEnv = %q, want from-env", got)
		}
	})

	t.Run("explicit configuration wins over the environment", func(t *testing.T) {
		t.Setenv(key, "from-env")
		if got := stringOrEnv(knownString("explicit"), key); got != "explicit" {
			t.Errorf("stringOrEnv = %q, want explicit", got)
		}
	})

	t.Run("empty when neither is set", func(t *testing.T) {
		if err := os.Unsetenv(key); err != nil {
			t.Fatalf("unsetenv: %v", err)
		}
		if got := stringOrEnv(nullString(), key); got != "" {
			t.Errorf("stringOrEnv = %q, want empty", got)
		}
	})
}

func TestFormatCommandError(t *testing.T) {
	t.Parallel()

	ce := &lima.CommandError{
		Binary:   "/usr/bin/limactl",
		Args:     []string{"start", "dev"},
		ExitCode: 1,
		Stderr:   `time="x" level=fatal msg="could not boot the VM"` + "\n",
	}
	got := formatCommandError(ce)

	// The diagnostic must answer "what did Lima report".
	for _, want := range []string{"exited with code 1", "Lima reported", "could not boot the VM"} {
		if !strings.Contains(got, want) {
			t.Errorf("formatCommandError = %q, want it to contain %q", got, want)
		}
	}
}

func TestFormatCommandErrorPlainError(t *testing.T) {
	t.Parallel()

	if got := formatCommandError(errPlain{}); got != "plain failure" {
		t.Errorf("formatCommandError = %q, want the plain error text", got)
	}
}

type errPlain struct{}

func (errPlain) Error() string { return "plain failure" }

// nullString and knownString build framework values for table tests.
func nullString() types.String          { return types.StringNull() }
func knownString(s string) types.String { return types.StringValue(s) }

// Compile-time guard that the acceptance factories stay referenced even when
// only unit tests are run.
var _ = testAccProtoV6ProviderFactories
var _ tfprotov6.ProviderServer

// TestMutabilityMatchesDocumentation pins the update-versus-replacement
// behaviour of every attribute.
//
// The tables in README.md and docs/resources/instance.md must describe what
// the code actually does. This test is the guard: changing a plan modifier
// without updating this list fails, which is the prompt to update the docs.
func TestMutabilityMatchesDocumentation(t *testing.T) {
	t.Parallel()

	// Keep in sync with the tables in README.md and
	// docs/resources/instance.md. "Replace" here means the attribute forces
	// replacement on a real change; adopting an imported instance and
	// removing an attribute from configuration are deliberately exempt (see
	// ReplaceOnRealChange).
	wantReplace := map[string]bool{
		"name":             true,
		"template":         true,
		"config":           true,
		"config_overrides": true,
		"vm_type":          true,
		"arch":             true,
		// Resources are applied in place via `limactl edit`, which needs the
		// instance stopped but not recreated.
		"cpus":    false,
		"memory":  false,
		"disk":    false,
		"start":   false,
		"protect": false,
		// mounts and port_forwards are reconciled in place via
		// `limactl edit --set`. provisions cannot be: Lima has no way to re-run
		// provisioning on an existing instance, so changing it must rebuild the VM.
		attrMounts:       false,
		attrPortForwards: false,
		attrProvisions:   true,
	}

	resp := &fwresource.SchemaResponse{}
	NewInstanceResource().Schema(context.Background(), fwresource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema errors: %v", resp.Diagnostics)
	}

	for name, want := range wantReplace {
		attr, ok := resp.Schema.Attributes[name]
		if !ok {
			t.Errorf("attribute %q is missing from the schema", name)
			continue
		}
		got := hasRequiresReplace(attr)
		if got != want {
			t.Errorf("attribute %q RequiresReplace = %v, want %v (update the mutability tables if this change is intended)",
				name, got, want)
		}
	}
}

// The provider-wide default_timeout is an override, not a floor. When it is
// unset each operation must get its own documented default; a read in particular
// must not inherit the half-hour budget a VM creation needs.
//
// This was silently broken: providerData.DefaultTimeout was always non-zero, so
// the per-operation fallbacks were unreachable and every operation, including
// refresh, ran on the same 20-minute budget.
func TestProviderDataTimeoutPrefersPerOperationDefault(t *testing.T) {
	t.Parallel()

	unset := &providerData{}
	if got := unset.timeout(lima.DefaultTimeouts.Read); got != lima.DefaultTimeouts.Read {
		t.Errorf("unset default_timeout: read = %s, want %s", got, lima.DefaultTimeouts.Read)
	}
	if got := unset.timeout(lima.DefaultTimeouts.Create); got != lima.DefaultTimeouts.Create {
		t.Errorf("unset default_timeout: create = %s, want %s", got, lima.DefaultTimeouts.Create)
	}

	configured := &providerData{DefaultTimeout: 90 * time.Minute}
	for _, per := range []time.Duration{
		lima.DefaultTimeouts.Create, lima.DefaultTimeouts.Update,
		lima.DefaultTimeouts.Delete, lima.DefaultTimeouts.Read,
	} {
		if got := configured.timeout(per); got != 90*time.Minute {
			t.Errorf("configured default_timeout: timeout(%s) = %s, want 1h30m", per, got)
		}
	}

	// Resources reach for this before Configure has run.
	var nilData *providerData
	if got := nilData.timeout(lima.DefaultTimeouts.Read); got != lima.DefaultTimeouts.Read {
		t.Errorf("nil providerData: timeout = %s, want %s", got, lima.DefaultTimeouts.Read)
	}
}

// hasRequiresReplace reports whether an attribute carries a RequiresReplace
// plan modifier, across the typed modifier slices the framework uses.
func hasRequiresReplace(attr schema.Attribute) bool {
	var modifiers []any
	switch a := attr.(type) {
	case schema.StringAttribute:
		for _, m := range a.PlanModifiers {
			modifiers = append(modifiers, m)
		}
	case schema.Int64Attribute:
		for _, m := range a.PlanModifiers {
			modifiers = append(modifiers, m)
		}
	case schema.BoolAttribute:
		for _, m := range a.PlanModifiers {
			modifiers = append(modifiers, m)
		}
	// The list cases matter as much as the scalar ones: without them a
	// ListNestedAttribute reports "no replacement" whatever its modifiers say,
	// so `provisions` would pass against a "Replace" row purely by accident.
	case schema.ListNestedAttribute:
		for _, m := range a.PlanModifiers {
			modifiers = append(modifiers, m)
		}
	case schema.ListAttribute:
		for _, m := range a.PlanModifiers {
			modifiers = append(modifiers, m)
		}
	default:
		return false
	}
	for _, m := range modifiers {
		if isRequiresReplace(m) {
			return true
		}
	}
	return false
}

// isRequiresReplace identifies a replacement-forcing plan modifier by type
// name.
//
// Two shapes count. The framework implements RequiresReplace() in terms of
// RequiresReplaceIf and returns an *unexported* type
// (stringplanmodifier.requiresReplaceIfModifier), so the match is
// case-insensitive on the type name rather than a type assertion. This
// provider's own ReplaceOnRealChange is the second shape: it forces
// replacement only between two known, differing values, which is what makes
// importing and un-managing an attribute non-destructive.
func isRequiresReplace(m any) bool {
	name := strings.ToLower(fmt.Sprintf("%T", m))
	return strings.Contains(name, "requiresreplace") || strings.Contains(name, "replaceonrealchange")
}

// TestDocumentationCoverage checks that every registered type has a
// documentation page and that nothing is left undescribed.
//
// The docs under docs/ are hand-written rather than generated: they record why
// the provider behaves as it does, which no generator can derive from a
// schema. That makes them easy to forget when a resource is added, so this
// test is the guard. It caught lima_disk missing from docs/index.md.
func TestDocumentationCoverage(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	p := New("test")()

	root := filepath.Join("..", "..")
	index, err := os.ReadFile(filepath.Join(root, "docs", "index.md"))
	if err != nil {
		t.Fatalf("reading docs/index.md: %v", err)
	}

	check := func(kind, typeName string) {
		t.Helper()
		page := filepath.Join(root, "docs", kind, strings.TrimPrefix(typeName, "lima_")+".md")
		if _, err := os.Stat(page); err != nil {
			t.Errorf("%s %q has no documentation page at %s", kind, typeName, page)
		}
		if !strings.Contains(string(index), typeName) {
			t.Errorf("%s %q is not listed in docs/index.md", kind, typeName)
		}
	}

	for _, fn := range p.Resources(ctx) {
		resp := &fwresource.MetadataResponse{}
		fn().Metadata(ctx, fwresource.MetadataRequest{ProviderTypeName: "lima"}, resp)
		check("resources", resp.TypeName)
	}
	for _, fn := range p.DataSources(ctx) {
		resp := &fwdatasource.MetadataResponse{}
		fn().Metadata(ctx, fwdatasource.MetadataRequest{ProviderTypeName: "lima"}, resp)
		check("data-sources", resp.TypeName)
	}
}

// TestEveryAttributeIsDescribed fails if any schema attribute ships without a
// description, since that is what users see in the registry and in editors.
func TestEveryAttributeIsDescribed(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	resourceSchemas := map[string]*fwresource.SchemaResponse{
		"lima_instance": {},
		"lima_disk":     {},
	}
	NewInstanceResource().Schema(ctx, fwresource.SchemaRequest{}, resourceSchemas["lima_instance"])
	NewDiskResource().Schema(ctx, fwresource.SchemaRequest{}, resourceSchemas["lima_disk"])

	for name, resp := range resourceSchemas {
		for attrName, attr := range resp.Schema.Attributes {
			if attrName == "timeouts" {
				continue // described by the framework's own helper
			}
			if attr.GetMarkdownDescription() == "" {
				t.Errorf("%s attribute %q has no description", name, attrName)
			}
		}
		for blockName, block := range resp.Schema.Blocks {
			if block.GetMarkdownDescription() == "" {
				t.Errorf("%s block %q has no description", name, blockName)
			}
		}
	}

	dataSchemas := map[string]*fwdatasource.SchemaResponse{
		"lima_instance": {},
		"lima_disk":     {},
		"lima_host":     {},
	}
	NewInstanceDataSource().Schema(ctx, fwdatasource.SchemaRequest{}, dataSchemas["lima_instance"])
	NewDiskDataSource().Schema(ctx, fwdatasource.SchemaRequest{}, dataSchemas["lima_disk"])
	NewHostDataSource().Schema(ctx, fwdatasource.SchemaRequest{}, dataSchemas["lima_host"])

	for name, resp := range dataSchemas {
		for attrName, attr := range resp.Schema.Attributes {
			if attr.GetMarkdownDescription() == "" {
				t.Errorf("%s data source attribute %q has no description", name, attrName)
			}
		}
	}
}
