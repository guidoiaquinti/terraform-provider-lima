// Copyright 2026 Guido Iaquinti
// SPDX-License-Identifier: Apache-2.0

package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// mounts, port_forwards and provisions can arrive wholly unknown — most obviously
// as `mounts = var.mounts` where the variable resolves at apply time, or from a
// value another resource produces. A []mountModel cannot represent unknown, so
// decoding the configuration straight into one fails with a Value Conversion
// Error before planning even starts, and the resource simply cannot be used that
// way. That is what instanceModel holding types.List, and declared() decoding
// only where the value is known, exists to prevent.
//
// This was originally a defect in the block form of these attributes, where
// Terraform expands `dynamic` blocks after ValidateResourceConfig and so every
// dynamic block hit the same path. Converting them to list attributes removed
// the `dynamic` route but not the underlying hazard, so the coverage stays.
//
// These tests drive the real schema and a real tfsdk.Config, because the defect
// was in decoding rather than in any logic a hand-built model would reach.

func instanceSchemaForTest(t *testing.T) (context.Context, tfsdk.Config) {
	t.Helper()
	ctx := context.Background()

	r := &instanceResource{}
	schemaResp := &resource.SchemaResponse{}
	r.Schema(ctx, resource.SchemaRequest{}, schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("Schema() diagnostics: %v", schemaResp.Diagnostics)
	}

	return ctx, tfsdk.Config{Schema: schemaResp.Schema}
}

// configValue builds a raw configuration in which every attribute is null,
// except those named in overrides. That mirrors a configuration where only a
// couple of things are written and the rest are left to defaults.
func configValue(t *testing.T, ctx context.Context, cfg tfsdk.Config, overrides map[string]tftypes.Value) tftypes.Value {
	t.Helper()

	objType, ok := cfg.Schema.Type().TerraformType(ctx).(tftypes.Object)
	if !ok {
		t.Fatalf("schema type is not an object")
	}

	values := make(map[string]tftypes.Value, len(objType.AttributeTypes))
	for name, attrType := range objType.AttributeTypes {
		if override, found := overrides[name]; found {
			values[name] = override
			continue
		}
		values[name] = tftypes.NewValue(attrType, nil)
	}
	return tftypes.NewValue(objType, values)
}

// attributeType returns the Terraform type of a schema attribute.
func attributeType(t *testing.T, ctx context.Context, cfg tfsdk.Config, name string) tftypes.Type {
	t.Helper()
	objType, ok := cfg.Schema.Type().TerraformType(ctx).(tftypes.Object)
	if !ok {
		t.Fatalf("schema type is not an object")
	}
	attrType, found := objType.AttributeTypes[name]
	if !found {
		t.Fatalf("schema has no %q", name)
	}
	return attrType
}

func TestConfigDecodesUnknownListAttributes(t *testing.T) {
	t.Parallel()

	for _, attr := range []string{attrProvisions, attrMounts, attrPortForwards} {
		t.Run(attr, func(t *testing.T) {
			t.Parallel()
			ctx, cfg := instanceSchemaForTest(t)

			cfg.Raw = configValue(t, ctx, cfg, map[string]tftypes.Value{
				"name":     tftypes.NewValue(tftypes.String, "dev"),
				"template": tftypes.NewValue(tftypes.String, "template:ubuntu"),
				attr:       tftypes.NewValue(attributeType(t, ctx, cfg, attr), tftypes.UnknownValue),
			})

			var config instanceModel
			if diags := cfg.Get(ctx, &config); diags.HasError() {
				t.Fatalf("decoding a configuration with an unknown %s failed: %v", attr, diags)
			}

			lists, diags := config.declared(ctx)
			if diags.HasError() {
				t.Fatalf("declared() diagnostics: %v", diags)
			}
			if !lists.Unknown {
				t.Errorf("declared().Unknown = false, want true for an unknown %s", attr)
			}
		})
	}
}

// Validation must still do its job around an unknown list rather than being
// skipped wholesale, so a bad name is caught even when one is unresolved.
func TestValidateConfigWithUnknownListsStillChecksOtherAttributes(t *testing.T) {
	t.Parallel()
	ctx, cfg := instanceSchemaForTest(t)

	cfg.Raw = configValue(t, ctx, cfg, map[string]tftypes.Value{
		"name":         tftypes.NewValue(tftypes.String, "not a valid lima name"),
		"template":     tftypes.NewValue(tftypes.String, "template:ubuntu"),
		attrProvisions: tftypes.NewValue(attributeType(t, ctx, cfg, attrProvisions), tftypes.UnknownValue),
	})

	var config instanceModel
	if diags := cfg.Get(ctx, &config); diags.HasError() {
		t.Fatalf("Get() diagnostics: %v", diags)
	}

	lists, diags := config.declared(ctx)
	if diags.HasError() {
		t.Fatalf("declared() diagnostics: %v", diags)
	}
	if got := validateInstanceConfig(&config, lists, "", "").ErrorsCount(); got != 1 {
		t.Errorf("errors = %d, want 1 for an invalid instance name", got)
	}
}

// An absent mounts attribute means "leave Lima's own mounts alone"; an empty one
// means the user deleted every entry and wants them unmounted. Collapsing the two
// would silently stop honouring a removal, and the distinction is easy to lose
// when the model holds a types.List rather than a nil-able slice.
func TestPlannedListsDistinguishAbsentFromEmpty(t *testing.T) {
	t.Parallel()

	if got := plannedMounts(declaredLists{}); got != nil {
		t.Errorf("absent mounts: plannedMounts = %v, want nil", got)
	}
	mounts := plannedMounts(declaredLists{MountsManaged: true})
	if mounts == nil {
		t.Fatal("empty mounts: plannedMounts = nil, want a pointer to an empty slice")
	}
	if len(*mounts) != 0 {
		t.Errorf("empty mounts: plannedMounts = %v, want empty", *mounts)
	}

	if got := plannedPortForwards(declaredLists{}); got != nil {
		t.Errorf("absent port_forwards: plannedPortForwards = %v, want nil", got)
	}
	forwards := plannedPortForwards(declaredLists{PortForwardsManaged: true})
	if forwards == nil {
		t.Fatal("empty port_forwards: plannedPortForwards = nil, want a pointer to an empty slice")
	}
	if len(*forwards) != 0 {
		t.Errorf("empty port_forwards: plannedPortForwards = %v, want empty", *forwards)
	}
}

// elementType returns a list attribute's element type, taken from the real
// schema rather than hand-written, so it cannot go stale when a nested attribute
// is added.
func elementType(t *testing.T, name string) attr.Type {
	t.Helper()

	resp := &resource.SchemaResponse{}
	NewInstanceResource().Schema(context.Background(), resource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema errors: %v", resp.Diagnostics)
	}
	nested, ok := resp.Schema.Attributes[name].(schema.ListNestedAttribute)
	if !ok {
		t.Fatalf("%q is not a ListNestedAttribute", name)
	}
	return nested.NestedObject.Type()
}

// mountsList builds the mounts attribute value from typed entries, so a test can
// drive the real types.List round trip rather than a Go slice.
func mountsList(t *testing.T, entries []mountModel) types.List {
	t.Helper()
	list, diags := types.ListValueFrom(context.Background(), elementType(t, attrMounts), entries)
	if diags.HasError() {
		t.Fatalf("building a mounts list: %v", diags)
	}
	return list
}

// portForwardsList is mountsList for port forwards.
func portForwardsList(t *testing.T, entries []portModel) types.List {
	t.Helper()
	list, diags := types.ListValueFrom(context.Background(), elementType(t, attrPortForwards), entries)
	if diags.HasError() {
		t.Fatalf("building a port_forwards list: %v", diags)
	}
	return list
}

// declaredOf decodes a model's list attributes, failing the test on a problem.
func declaredOf(t *testing.T, m *instanceModel) declaredLists {
	t.Helper()
	lists, diags := m.declared(context.Background())
	if diags.HasError() {
		t.Fatalf("declared(): %v", diags)
	}
	return lists
}

// declared() must report a known empty list as managed, and a null one as not.
// This is the decode step the two Managed flags come from, so it is worth pinning
// separately from the planned* helpers above.
func TestDeclaredSeparatesNullFromEmpty(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	mountElem := elementType(t, attrMounts)
	allNull := func() instanceModel {
		return instanceModel{
			Mounts:       types.ListNull(mountElem),
			PortForwards: types.ListNull(elementType(t, attrPortForwards)),
			Provisions:   types.ListNull(elementType(t, attrProvisions)),
		}
	}

	null := allNull()
	lists, diags := null.declared(ctx)
	if diags.HasError() {
		t.Fatalf("declared() diagnostics: %v", diags)
	}
	if lists.MountsManaged {
		t.Error("a null mounts attribute reported as managed")
	}
	if lists.Unknown {
		t.Error("a null mounts attribute reported as unknown")
	}

	empty, d := types.ListValue(mountElem, nil)
	if d.HasError() {
		t.Fatalf("building an empty list: %v", d)
	}
	model := allNull()
	model.Mounts = empty
	lists, diags = model.declared(ctx)
	if diags.HasError() {
		t.Fatalf("declared() diagnostics: %v", diags)
	}
	if !lists.MountsManaged {
		t.Error("a known empty mounts attribute reported as unmanaged")
	}
	if len(lists.Mounts) != 0 {
		t.Errorf("Mounts = %v, want empty", lists.Mounts)
	}
}
