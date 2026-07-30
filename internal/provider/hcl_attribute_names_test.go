package provider

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/hashicorp/hcl/v2/hclsyntax"

	fwdatasource "github.com/hashicorp/terraform-plugin-framework/datasource"
	dsschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
)

// Every attribute name used in a shipped .tf file must exist in the schema.
//
// This closes a measured blind spot rather than a hypothetical one.
// `terraform validate` does NOT catch a misspelled attribute *inside* a nested
// list attribute: renaming `port_forwards[].protocol` to `proto` validates
// cleanly on Terraform 1.0 through 1.15 and OpenTofu 1.6 through 1.10, because
// the object literal is converted at plan time, not at validate time. Verified
// by doing it.
//
// So the CLI compatibility job in CI cannot be the guard here, and a wrong
// nested key would ship in an example — silently doing nothing at apply, since
// an unknown key in an object literal is simply absent from the resulting
// object. This test is the guard instead: it parses the real HCL and checks
// every name against the real schema.

// schemaAttributeNames returns every attribute name the provider defines, at any
// nesting depth, across every resource and data source.
//
// Flattened into one set deliberately. Checking each block against only its own
// type's schema would be more precise, but it would also mean tracking which
// nested object a given key belongs to — and the failure this test exists to
// catch is a name that appears nowhere at all, which a flat set catches with far
// less machinery.
func schemaAttributeNames(t *testing.T) map[string]bool {
	t.Helper()
	ctx := t.Context()
	names := map[string]bool{}

	var walkResource func(attrs map[string]rschema.Attribute)
	walkResource = func(attrs map[string]rschema.Attribute) {
		for name, attr := range attrs {
			names[name] = true
			switch nested := attr.(type) {
			case rschema.ListNestedAttribute:
				walkResource(nested.NestedObject.Attributes)
			case rschema.SetNestedAttribute:
				walkResource(nested.NestedObject.Attributes)
			case rschema.SingleNestedAttribute:
				walkResource(nested.Attributes)
			case rschema.MapNestedAttribute:
				walkResource(nested.NestedObject.Attributes)
			}
		}
	}

	var walkDataSource func(attrs map[string]dsschema.Attribute)
	walkDataSource = func(attrs map[string]dsschema.Attribute) {
		for name, attr := range attrs {
			names[name] = true
			switch nested := attr.(type) {
			case dsschema.ListNestedAttribute:
				walkDataSource(nested.NestedObject.Attributes)
			case dsschema.SetNestedAttribute:
				walkDataSource(nested.NestedObject.Attributes)
			case dsschema.SingleNestedAttribute:
				walkDataSource(nested.Attributes)
			case dsschema.MapNestedAttribute:
				walkDataSource(nested.NestedObject.Attributes)
			}
		}
	}

	p := New("test")()
	for _, ctor := range p.Resources(ctx) {
		resp := &fwresource.SchemaResponse{}
		ctor().Schema(ctx, fwresource.SchemaRequest{}, resp)
		if resp.Diagnostics.HasError() {
			t.Fatalf("resource schema: %v", resp.Diagnostics)
		}
		walkResource(resp.Schema.Attributes)
	}
	for _, ctor := range p.DataSources(ctx) {
		resp := &fwdatasource.SchemaResponse{}
		ctor().Schema(ctx, fwdatasource.SchemaRequest{}, resp)
		if resp.Diagnostics.HasError() {
			t.Fatalf("data source schema: %v", resp.Diagnostics)
		}
		walkDataSource(resp.Schema.Attributes)
	}

	// The framework's own timeouts helper contributes these, and they are
	// nested inside an attribute whose schema is built by that package.
	for _, name := range []string{"create", "read", "update", "delete"} {
		names[name] = true
	}
	return names
}

// hclFiles returns every .tf file the repository ships.
func hclFiles(t *testing.T) []string {
	t.Helper()

	root := filepath.Join("..", "..")
	var found []string
	for _, dir := range []string{"examples", "test"} {
		err := filepath.Walk(filepath.Join(root, dir), func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if !info.IsDir() && strings.HasSuffix(path, ".tf") {
				found = append(found, path)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walking %s: %v", dir, err)
		}
	}
	if len(found) == 0 {
		t.Fatal("found no .tf files; has the layout changed?")
	}
	sort.Strings(found)
	return found
}

// rawLimaYAML names the attributes whose value is a Lima document rather than
// provider configuration.
var rawLimaYAML = map[string]bool{
	attrConfig:          true,
	attrConfigOverrides: true,
}

func TestShippedHCLUsesOnlyRealAttributeNames(t *testing.T) {
	t.Parallel()

	known := schemaAttributeNames(t)
	parser := hclparse.NewParser()

	for _, file := range hclFiles(t) {
		t.Run(filepath.Base(filepath.Dir(file))+"/"+filepath.Base(file), func(t *testing.T) {
			src, err := os.ReadFile(file)
			if err != nil {
				t.Fatalf("reading %s: %v", file, err)
			}
			parsed, diags := parser.ParseHCL(src, file)
			if diags.HasErrors() {
				t.Fatalf("parsing %s: %v", file, diags)
			}
			body, ok := parsed.Body.(*hclsyntax.Body)
			if !ok {
				t.Fatalf("%s did not parse as native HCL syntax", file)
			}

			for _, block := range body.Blocks {
				// Only lima resources and data sources: a provider block, a
				// terraform block, locals and outputs are not described by the
				// resource schemas.
				if block.Type != "resource" && block.Type != "data" {
					continue
				}
				if len(block.Labels) == 0 || !strings.HasPrefix(block.Labels[0], "lima_") {
					continue
				}

				for name, attr := range block.Body.Attributes {
					if !known[name] {
						t.Errorf("%s: %s %q sets %q, which is not in the provider schema",
							file, block.Type, strings.Join(block.Labels, "."), name)
					}
					// config and config_overrides carry raw Lima YAML, so the
					// keys inside them belong to Lima's schema and not to this
					// provider's — that is the entire purpose of those two
					// attributes. Examples build them with yamlencode({...}),
					// which parses as an object literal, so without this they
					// would be checked against the wrong schema.
					if rawLimaYAML[name] {
						continue
					}
					// The keys of an object literal are the nested attribute
					// names. This is the case terraform validate misses.
					for _, key := range objectKeys(attr.Expr) {
						if !known[key] {
							t.Errorf("%s: %s %q sets %s = { %s = ... }, and %q is not in the provider schema "+
								"(terraform validate cannot catch this)",
								file, block.Type, strings.Join(block.Labels, "."), name, key, key)
						}
					}
				}
			}
		})
	}
}

// objectKeys returns the literal keys of every object constructor anywhere
// inside an expression, including inside a list or a nested object.
func objectKeys(expr hclsyntax.Expression) []string {
	var keys []string

	visit := func(node hclsyntax.Node) hcl.Diagnostics {
		obj, ok := node.(*hclsyntax.ObjectConsExpr)
		if !ok {
			return nil
		}
		for _, item := range obj.Items {
			// A key written bare (`protocol = ...`) parses as a traversal wrapped
			// in ObjectConsKeyExpr; a quoted key parses as a template. Only bare
			// and simple quoted keys are checkable, and both are what people
			// actually write.
			keyExpr, ok := item.KeyExpr.(*hclsyntax.ObjectConsKeyExpr)
			if !ok {
				continue
			}
			if traversal, diags := hcl.AbsTraversalForExpr(keyExpr.Wrapped); !diags.HasErrors() && len(traversal) == 1 {
				if root, ok := traversal[0].(hcl.TraverseRoot); ok {
					keys = append(keys, root.Name)
				}
			}
		}
		return nil
	}

	// Walk returns diagnostics from the callback; ours never produces any.
	_ = hclsyntax.Walk(expr, walkFunc(visit))
	return keys
}

// walkFunc adapts a single function to hclsyntax.Walker, which wants Enter and
// Exit.
type walkFunc func(hclsyntax.Node) hcl.Diagnostics

func (w walkFunc) Enter(node hclsyntax.Node) hcl.Diagnostics { return w(node) }
func (w walkFunc) Exit(hclsyntax.Node) hcl.Diagnostics       { return nil }
