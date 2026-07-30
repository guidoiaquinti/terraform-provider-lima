package provider

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/guidoiaquinti/terraform-provider-lima/internal/lima"
	"github.com/guidoiaquinti/terraform-provider-lima/internal/testutil"
)

// An update can fail after it has already changed something. Those paths were the
// last correctness findings without automated coverage, because provoking them
// needs Lima to fail on demand — the acceptance suite drives the real binary,
// which succeeds.
//
// The fake limactl already injects failures per command and per instance, so the
// resource's Update can be driven directly against it. That exercises the real
// Update, with the real Service and the real argument construction, rather than
// an extracted fragment of the logic.

// updateHarness drives instanceResource.Update against a fake limactl.
type updateHarness struct {
	resource *instanceResource
	schema   rschema.Schema
	fake     *testutil.FakeLimactl
}

func newUpdateHarness(t *testing.T, fake *testutil.FakeLimactl) *updateHarness {
	t.Helper()
	ctx := context.Background()

	fake.ReadFile = os.ReadFile
	client, err := lima.NewExecClient(lima.Options{
		Binary: testutil.StubBinary(t),
		Runner: fake,
	})
	if err != nil {
		t.Fatalf("NewExecClient: %v", err)
	}

	schemaResp := &resource.SchemaResponse{}
	r := &instanceResource{}
	r.Schema(ctx, resource.SchemaRequest{}, schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("Schema: %v", schemaResp.Diagnostics)
	}

	r.data = &providerData{
		Service: lima.NewService(client, lima.WithPollOptions(lima.PollOptions{
			Interval:    time.Millisecond,
			MaxInterval: time.Millisecond,
		})),
		Client: client,
	}

	return &updateHarness{resource: r, schema: schemaResp.Schema, fake: fake}
}

// value builds a raw instance value: every attribute null except the overrides.
func (h *updateHarness) value(t *testing.T, overrides map[string]tftypes.Value) tftypes.Value {
	t.Helper()
	ctx := context.Background()

	objType, ok := h.schema.Type().TerraformType(ctx).(tftypes.Object)
	if !ok {
		t.Fatal("schema type is not an object")
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

// update runs Update and returns the resulting state plus diagnostics.
func (h *updateHarness) update(t *testing.T, state, plan tftypes.Value) (instanceModel, []string) {
	t.Helper()
	ctx := context.Background()

	req := resource.UpdateRequest{
		State: tfsdk.State{Schema: h.schema, Raw: state},
		Plan:  tfsdk.Plan{Schema: h.schema, Raw: plan},
	}
	resp := &resource.UpdateResponse{
		State: tfsdk.State{Schema: h.schema, Raw: state},
	}
	h.resource.Update(ctx, req, resp)

	var summaries []string
	for _, d := range resp.Diagnostics {
		summaries = append(summaries, d.Summary())
	}

	var got instanceModel
	if diags := resp.State.Get(ctx, &got); diags.HasError() {
		t.Fatalf("reading the resulting state: %v", diags)
	}
	return got, summaries
}

// Protection is cleared before the rest of an update and reapplied afterwards, so
// that a protected instance can be reconfigured in one apply. A failure in between
// used to return without writing state, leaving `protect = true` recorded against
// an instance that had just been unprotected.
func TestUpdateRecordsProtectionAfterAFailedResize(t *testing.T) {
	t.Parallel()

	const name = "dev"
	fake := testutil.NewFakeLimactl()
	fake.Seed(testutil.FakeInstance{Name: name, Status: "Stopped", CPUs: 2, Protected: true})
	// The edit fails, after protection has already been cleared.
	fake.EditFailsFor = map[string]string{name: "cannot edit for reasons"}

	h := newUpdateHarness(t, fake)

	state := h.value(t, map[string]tftypes.Value{
		"name":          tftypes.NewValue(tftypes.String, name),
		"instance_name": tftypes.NewValue(tftypes.String, name),
		"cpus":          tftypes.NewValue(tftypes.Number, 2),
		"protect":       tftypes.NewValue(tftypes.Bool, true),
		"start":         tftypes.NewValue(tftypes.Bool, false),
	})
	plan := h.value(t, map[string]tftypes.Value{
		"name":          tftypes.NewValue(tftypes.String, name),
		"instance_name": tftypes.NewValue(tftypes.String, name),
		"cpus":          tftypes.NewValue(tftypes.Number, 4),
		"protect":       tftypes.NewValue(tftypes.Bool, false),
		"start":         tftypes.NewValue(tftypes.Bool, false),
	})

	got, summaries := h.update(t, state, plan)

	if len(summaries) == 0 {
		t.Fatal("a failed resize produced no diagnostic")
	}
	// Lima really is unprotected now, so state must not claim otherwise.
	if got.Protect.ValueBool() {
		t.Errorf("state records protect = true after protection was cleared and the resize failed; summaries: %v", summaries)
	}
}

// A restart failure after a successful edit is not a failed change: the resources
// were applied and only bringing the instance back up did not work. Keeping the
// old values in state would make the next plan propose a change that has already
// happened.
func TestUpdateRecordsAppliedResourcesWhenTheRestartFails(t *testing.T) {
	t.Parallel()

	const name = "dev"
	fake := testutil.NewFakeLimactl()
	fake.Seed(testutil.FakeInstance{Name: name, Status: "Running", CPUs: 2})
	// The edit succeeds; the start afterwards does not.
	fake.StartFailsFor = map[string]string{name: "could not boot"}

	h := newUpdateHarness(t, fake)

	state := h.value(t, map[string]tftypes.Value{
		"name":          tftypes.NewValue(tftypes.String, name),
		"instance_name": tftypes.NewValue(tftypes.String, name),
		"cpus":          tftypes.NewValue(tftypes.Number, 2),
		"start":         tftypes.NewValue(tftypes.Bool, true),
		"protect":       tftypes.NewValue(tftypes.Bool, false),
	})
	plan := h.value(t, map[string]tftypes.Value{
		"name":          tftypes.NewValue(tftypes.String, name),
		"instance_name": tftypes.NewValue(tftypes.String, name),
		"cpus":          tftypes.NewValue(tftypes.Number, 4),
		"start":         tftypes.NewValue(tftypes.Bool, true),
		"protect":       tftypes.NewValue(tftypes.Bool, false),
	})

	got, summaries := h.update(t, state, plan)

	if len(summaries) == 0 {
		t.Fatal("a failed restart produced no diagnostic")
	}

	// The edit applied, so the new CPU count is the truth.
	if got.CPUs.ValueInt64() != 4 {
		t.Errorf("state records cpus = %d after a successful edit; want 4, so the next plan does not re-apply it. summaries: %v",
			got.CPUs.ValueInt64(), summaries)
	}
	// And the instance is down, which is the thing the user has to act on.
	if got.Start.ValueBool() {
		t.Errorf("state records start = true after the restart failed; summaries: %v", summaries)
	}
}
