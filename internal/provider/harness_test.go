package provider

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	dsschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/guidoiaquinti/terraform-provider-lima/internal/lima"
	"github.com/guidoiaquinti/terraform-provider-lima/internal/testutil"
)

// A harness for driving real resources and data sources against the fake
// limactl, without Terraform.
//
// The acceptance suite covers the happy path against real VMs, but it is slow,
// needs a hypervisor, and cannot make Lima fail on demand — so the provider
// layer's error handling, state round-tripping and diagnostics were the parts
// with no automated coverage at all. Everything here plugs in at the
// process-execution boundary, so the resource's real Create/Read/Update/Delete
// run, with the real argument construction and output parsing; only the VM is
// simulated.
//
// Response-state fidelity is the fiddly part and it is worth stating explicitly,
// because getting it wrong makes tests that pass for the wrong reason. Measured
// from terraform-plugin-framework v1.19.0 internal/fwserver:
//
//	Create  resp.State.Raw starts NULL          (server_createresource.go)
//	Update  resp.State.Raw starts NULL          (server_updateresource.go)
//	Delete  resp.State.Raw starts NULL, and the request carries prior state
//	Read    resp.State.Raw starts as a COPY of current state
//	Import  resp.State.Raw starts NULL
//
// Null-initialised responses matter: a resource that returns early without
// writing state yields a null state, so "did it record what happened" is a real
// question the harness can answer. Seeding the response with the prior state —
// which is the intuitive thing to do — would silently answer "yes" every time.

// harnessDiags wraps diagnostics with assertions the tests actually need.
type harnessDiags struct {
	diag.Diagnostics
}

// summaries returns each diagnostic's summary line.
func (d harnessDiags) summaries() []string {
	out := make([]string, 0, len(d.Diagnostics))
	for _, one := range d.Diagnostics {
		out = append(out, one.Summary())
	}
	return out
}

// text returns every summary and detail joined, for substring assertions about
// what a user would actually read.
func (d harnessDiags) text() string {
	var b strings.Builder
	for _, one := range d.Diagnostics {
		b.WriteString(one.Summary())
		b.WriteString("\n")
		b.WriteString(one.Detail())
		b.WriteString("\n")
	}
	return b.String()
}

// requireError fails the test unless an error diagnostic mentioning want was
// produced.
func (d harnessDiags) requireError(t *testing.T, want string) {
	t.Helper()
	if !d.HasError() {
		t.Fatalf("expected an error mentioning %q, got diagnostics %v", want, d.summaries())
	}
	if want != "" && !strings.Contains(d.text(), want) {
		t.Errorf("diagnostics do not mention %q; got:\n%s", want, d.text())
	}
}

// requireNoError fails the test if any error diagnostic was produced.
func (d harnessDiags) requireNoError(t *testing.T) {
	t.Helper()
	if d.HasError() {
		t.Fatalf("unexpected error diagnostics:\n%s", d.text())
	}
}

// hasWarning reports whether a warning mentioning want was produced.
func (d harnessDiags) hasWarning(want string) bool {
	for _, one := range d.Diagnostics {
		if one.Severity() == diag.SeverityWarning && strings.Contains(one.Summary()+one.Detail(), want) {
			return true
		}
	}
	return false
}

// fakeProviderData builds the providerData a configured resource receives.
//
// The poll cadence is compressed to a millisecond: the polling algorithm is
// covered on its own in internal/lima/poll_test.go, and leaving the real cadence
// here would trade seconds of test time for no additional coverage.
func fakeProviderData(t *testing.T, fake *testutil.FakeLimactl, mutate ...func(*providerData)) *providerData {
	t.Helper()

	fake.ReadFile = os.ReadFile
	client, err := lima.NewExecClient(lima.Options{
		Binary: testutil.StubBinary(t),
		Runner: fake,
	})
	if err != nil {
		t.Fatalf("NewExecClient: %v", err)
	}

	locks := lima.NewKeyedMutex()
	data := &providerData{
		Service: lima.NewService(client,
			lima.WithLocks(locks),
			lima.WithPollOptions(lima.PollOptions{
				Interval:    time.Millisecond,
				MaxInterval: time.Millisecond,
				Factor:      1,
			})),
		Disks:   lima.NewDiskService(client, locks),
		Client:  client,
		Version: lima.Version{Major: 2, Minor: 2},
		Binary:  client.Binary(),
	}
	for _, m := range mutate {
		m(data)
	}
	return data
}

// resourceHarness drives one framework resource against a fake limactl.
type resourceHarness struct {
	t      *testing.T
	res    resource.Resource
	schema rschema.Schema
	fake   *testutil.FakeLimactl
	data   *providerData
}

// newResourceHarness configures a resource exactly as the framework would,
// through its real Configure method, so that path is covered too.
func newResourceHarness(
	t *testing.T,
	fake *testutil.FakeLimactl,
	ctor func() resource.Resource,
	mutate ...func(*providerData),
) *resourceHarness {
	t.Helper()
	ctx := context.Background()

	res := ctor()

	schemaResp := &resource.SchemaResponse{}
	res.Schema(ctx, resource.SchemaRequest{}, schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("Schema: %v", schemaResp.Diagnostics)
	}

	data := fakeProviderData(t, fake, mutate...)

	withConfigure, ok := res.(resource.ResourceWithConfigure)
	if !ok {
		t.Fatalf("%T does not implement ResourceWithConfigure", res)
	}
	configureResp := &resource.ConfigureResponse{}
	withConfigure.Configure(ctx, resource.ConfigureRequest{ProviderData: data}, configureResp)
	if configureResp.Diagnostics.HasError() {
		t.Fatalf("Configure: %v", configureResp.Diagnostics)
	}

	return &resourceHarness{t: t, res: res, schema: schemaResp.Schema, fake: fake, data: data}
}

// objectType returns the schema's Terraform object type.
func (h *resourceHarness) objectType() tftypes.Object {
	obj, ok := h.schema.Type().TerraformType(context.Background()).(tftypes.Object)
	if !ok {
		h.t.Fatal("schema type is not an object")
	}
	return obj
}

// nullState returns a fully null value of the schema type, which is what the
// framework hands a resource as the initial response state for create, update,
// delete and import.
func (h *resourceHarness) nullState() tftypes.Value {
	return tftypes.NewValue(h.objectType(), nil)
}

// value builds a raw value with every attribute null except the overrides.
func (h *resourceHarness) value(overrides map[string]tftypes.Value) tftypes.Value {
	h.t.Helper()

	obj := h.objectType()
	values := make(map[string]tftypes.Value, len(obj.AttributeTypes))
	for name, attrType := range obj.AttributeTypes {
		if override, found := overrides[name]; found {
			values[name] = override
			continue
		}
		values[name] = tftypes.NewValue(attrType, nil)
	}
	return tftypes.NewValue(obj, values)
}

func (h *resourceHarness) create(plan tftypes.Value) (tfsdk.State, harnessDiags) {
	h.t.Helper()
	ctx := context.Background()

	resp := &resource.CreateResponse{
		State: tfsdk.State{Schema: h.schema, Raw: h.nullState()},
	}
	h.res.Create(ctx, resource.CreateRequest{
		Plan:   tfsdk.Plan{Schema: h.schema, Raw: plan},
		Config: tfsdk.Config{Schema: h.schema, Raw: plan},
	}, resp)

	return resp.State, harnessDiags{resp.Diagnostics}
}

func (h *resourceHarness) read(state tftypes.Value) (tfsdk.State, harnessDiags) {
	h.t.Helper()
	ctx := context.Background()

	resp := &resource.ReadResponse{
		State: tfsdk.State{Schema: h.schema, Raw: state.Copy()},
	}
	h.res.Read(ctx, resource.ReadRequest{
		State: tfsdk.State{Schema: h.schema, Raw: state},
	}, resp)

	return resp.State, harnessDiags{resp.Diagnostics}
}

func (h *resourceHarness) update(state, plan tftypes.Value) (tfsdk.State, harnessDiags) {
	h.t.Helper()
	ctx := context.Background()

	resp := &resource.UpdateResponse{
		State: tfsdk.State{Schema: h.schema, Raw: h.nullState()},
	}
	h.res.Update(ctx, resource.UpdateRequest{
		State:  tfsdk.State{Schema: h.schema, Raw: state},
		Plan:   tfsdk.Plan{Schema: h.schema, Raw: plan},
		Config: tfsdk.Config{Schema: h.schema, Raw: plan},
	}, resp)

	return resp.State, harnessDiags{resp.Diagnostics}
}

func (h *resourceHarness) delete(state tftypes.Value) harnessDiags {
	h.t.Helper()
	ctx := context.Background()

	resp := &resource.DeleteResponse{
		State: tfsdk.State{Schema: h.schema, Raw: h.nullState()},
	}
	h.res.Delete(ctx, resource.DeleteRequest{
		State: tfsdk.State{Schema: h.schema, Raw: state},
	}, resp)

	return harnessDiags{resp.Diagnostics}
}

func (h *resourceHarness) importState(id string) (tfsdk.State, harnessDiags) {
	h.t.Helper()
	ctx := context.Background()

	withImport, ok := h.res.(resource.ResourceWithImportState)
	if !ok {
		h.t.Fatalf("%T does not implement ResourceWithImportState", h.res)
	}

	resp := &resource.ImportStateResponse{
		State: tfsdk.State{Schema: h.schema, Raw: h.nullState()},
	}
	withImport.ImportState(ctx, resource.ImportStateRequest{ID: id}, resp)

	return resp.State, harnessDiags{resp.Diagnostics}
}

func (h *resourceHarness) validateConfig(config tftypes.Value) harnessDiags {
	h.t.Helper()
	ctx := context.Background()

	withValidate, ok := h.res.(resource.ResourceWithValidateConfig)
	if !ok {
		h.t.Fatalf("%T does not implement ResourceWithValidateConfig", h.res)
	}

	resp := &resource.ValidateConfigResponse{}
	withValidate.ValidateConfig(ctx, resource.ValidateConfigRequest{
		Config: tfsdk.Config{Schema: h.schema, Raw: config},
	}, resp)

	return harnessDiags{resp.Diagnostics}
}

// get decodes a state into a model, failing the test if the state is null.
//
// A null state is the framework's signal that the resource was removed, so it is
// almost always a distinct outcome from "state holds these values" and the two
// must not be confused by a silent zero value.
func getState[T any](t *testing.T, state tfsdk.State) T {
	t.Helper()
	var target T
	if state.Raw.IsNull() {
		t.Fatal("state is null; the resource wrote no state at all")
	}
	if diags := state.Get(context.Background(), &target); diags.HasError() {
		t.Fatalf("decoding state: %v", diags)
	}
	return target
}

// dataSourceHarness drives one framework data source against a fake limactl.
type dataSourceHarness struct {
	t      *testing.T
	ds     datasource.DataSource
	schema dsschema.Schema
	fake   *testutil.FakeLimactl
	data   *providerData
}

func newDataSourceHarness(
	t *testing.T,
	fake *testutil.FakeLimactl,
	ctor func() datasource.DataSource,
	mutate ...func(*providerData),
) *dataSourceHarness {
	t.Helper()
	ctx := context.Background()

	ds := ctor()

	schemaResp := &datasource.SchemaResponse{}
	ds.Schema(ctx, datasource.SchemaRequest{}, schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("Schema: %v", schemaResp.Diagnostics)
	}

	data := fakeProviderData(t, fake, mutate...)

	withConfigure, ok := ds.(datasource.DataSourceWithConfigure)
	if !ok {
		t.Fatalf("%T does not implement DataSourceWithConfigure", ds)
	}
	configureResp := &datasource.ConfigureResponse{}
	withConfigure.Configure(ctx, datasource.ConfigureRequest{ProviderData: data}, configureResp)
	if configureResp.Diagnostics.HasError() {
		t.Fatalf("Configure: %v", configureResp.Diagnostics)
	}

	return &dataSourceHarness{t: t, ds: ds, schema: schemaResp.Schema, fake: fake, data: data}
}

func (h *dataSourceHarness) objectType() tftypes.Object {
	obj, ok := h.schema.Type().TerraformType(context.Background()).(tftypes.Object)
	if !ok {
		h.t.Fatal("schema type is not an object")
	}
	return obj
}

// value builds a raw config with every attribute null except the overrides.
func (h *dataSourceHarness) value(overrides map[string]tftypes.Value) tftypes.Value {
	h.t.Helper()

	obj := h.objectType()
	values := make(map[string]tftypes.Value, len(obj.AttributeTypes))
	for name, attrType := range obj.AttributeTypes {
		if override, found := overrides[name]; found {
			values[name] = override
			continue
		}
		values[name] = tftypes.NewValue(attrType, nil)
	}
	return tftypes.NewValue(obj, values)
}

func (h *dataSourceHarness) read(config tftypes.Value) (tfsdk.State, harnessDiags) {
	h.t.Helper()
	ctx := context.Background()

	resp := &datasource.ReadResponse{
		State: tfsdk.State{Schema: h.schema, Raw: tftypes.NewValue(h.objectType(), nil)},
	}
	h.ds.Read(ctx, datasource.ReadRequest{
		Config: tfsdk.Config{Schema: h.schema, Raw: config},
	}, resp)

	return resp.State, harnessDiags{resp.Diagnostics}
}

// Convenience constructors, so tests read as the thing being tested rather than
// as a type conversion.
func instanceResourceCtor() resource.Resource        { return NewInstanceResource() }
func diskResourceCtor() resource.Resource            { return NewDiskResource() }
func instanceDataSourceCtor() datasource.DataSource  { return NewInstanceDataSource() }
func instancesDataSourceCtor() datasource.DataSource { return NewInstancesDataSource() }
func hostDataSourceCtor() datasource.DataSource      { return NewHostDataSource() }
func diskDataSourceCtor() datasource.DataSource      { return NewDiskDataSource() }

// tfString and friends keep the raw-value literals in tests short.
func tfString(s string) tftypes.Value { return tftypes.NewValue(tftypes.String, s) }
func tfNumber(n int64) tftypes.Value  { return tftypes.NewValue(tftypes.Number, n) }
func tfBool(b bool) tftypes.Value     { return tftypes.NewValue(tftypes.Bool, b) }
