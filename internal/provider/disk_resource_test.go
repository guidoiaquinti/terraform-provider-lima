package provider

import (
	"context"
	"strings"
	"testing"

	fwdatasource "github.com/hashicorp/terraform-plugin-framework/datasource"
	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/guidoiaquinti/terraform-provider-lima/internal/lima"
)

func TestDiskResourceSchema(t *testing.T) {
	t.Parallel()

	resp := &fwresource.SchemaResponse{}
	NewDiskResource().Schema(context.Background(), fwresource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema errors: %v", resp.Diagnostics)
	}

	for name, attr := range resp.Schema.Attributes {
		if name == "timeouts" {
			continue
		}
		if attr.GetMarkdownDescription() == "" {
			t.Errorf("attribute %q has no description", name)
		}
	}

	for _, name := range []string{"name", "size"} {
		if !resp.Schema.Attributes[name].IsRequired() {
			t.Errorf("attribute %q should be required", name)
		}
	}
	for _, name := range []string{"actual_format", "dir", "mount_point", "in_use_by"} {
		attr, ok := resp.Schema.Attributes[name]
		if !ok {
			t.Errorf("schema is missing computed attribute %q", name)
			continue
		}
		if !attr.IsComputed() || attr.IsRequired() {
			t.Errorf("attribute %q should be computed and not required", name)
		}
	}
}

func TestDiskMutabilityMatchesDocumentation(t *testing.T) {
	t.Parallel()

	// Keep in sync with the table in docs/resources/disk.md.
	wantReplace := map[string]bool{
		"name":   true,
		"format": true,
		// The whole point of the resource: growing must not destroy data.
		"size": false,
	}

	resp := &fwresource.SchemaResponse{}
	NewDiskResource().Schema(context.Background(), fwresource.SchemaRequest{}, resp)

	for name, want := range wantReplace {
		attr, ok := resp.Schema.Attributes[name]
		if !ok {
			t.Errorf("attribute %q is missing", name)
			continue
		}
		if got := hasRequiresReplace(attr); got != want {
			t.Errorf("attribute %q RequiresReplace = %v, want %v (update the mutability table if intended)",
				name, got, want)
		}
	}
}

func TestDiskDataSourceSchema(t *testing.T) {
	t.Parallel()

	resp := &fwdatasource.SchemaResponse{}
	NewDiskDataSource().Schema(context.Background(), fwdatasource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema errors: %v", resp.Diagnostics)
	}
	if !resp.Schema.Attributes["name"].IsRequired() {
		t.Error("the disk data source should require name")
	}
	for _, name := range []string{"size", "size_bytes", "format", "dir", "mount_point", "in_use_by"} {
		if _, ok := resp.Schema.Attributes[name]; !ok {
			t.Errorf("data source is missing %q", name)
		}
	}
}

func TestDiskModelApply(t *testing.T) {
	t.Parallel()

	disk := lima.Disk{
		Name:       "data",
		SizeBytes:  10 << 30,
		Format:     "raw",
		Dir:        "/tmp/lima/_disks/data",
		MountPoint: "/mnt/lima-data",
	}

	t.Run("format is never written back into the request attribute", func(t *testing.T) {
		t.Parallel()
		// Lima reports raw even when qcow2 was requested, so copying it into
		// `format` would fight the configuration on every plan.
		m := diskModel{Format: types.StringValue("qcow2"), Size: types.StringValue("10GiB")}
		m.applyDisk(disk)

		if m.Format.ValueString() != "qcow2" {
			t.Errorf("format = %q, want the requested value preserved", m.Format.ValueString())
		}
		if m.ActualFormat.ValueString() != "raw" {
			t.Errorf("actual_format = %q, want what Lima reports", m.ActualFormat.ValueString())
		}
	})

	t.Run("equivalent size spelling is preserved", func(t *testing.T) {
		t.Parallel()
		m := diskModel{Size: types.StringValue("10240MiB")}
		m.applyDisk(disk)
		if m.Size.ValueString() != "10240MiB" {
			t.Errorf("size = %q, want the configured spelling to survive", m.Size.ValueString())
		}
	})

	t.Run("a real size change is picked up", func(t *testing.T) {
		t.Parallel()
		m := diskModel{Size: types.StringValue("5GiB")}
		m.applyDisk(disk)
		if m.Size.ValueString() != "10GiB" {
			t.Errorf("size = %q, want the observed 10GiB", m.Size.ValueString())
		}
	})

	t.Run("a free disk reports no holder", func(t *testing.T) {
		t.Parallel()
		var m diskModel
		m.applyDisk(disk)
		if !m.InUseBy.IsNull() {
			t.Errorf("in_use_by = %v, want null for a free disk", m.InUseBy)
		}
	})

	t.Run("an in-use disk names the holder", func(t *testing.T) {
		t.Parallel()
		held := disk
		held.Instance = "dev"
		var m diskModel
		m.applyDisk(held)
		if m.InUseBy.ValueString() != "dev" {
			t.Errorf("in_use_by = %q, want dev", m.InUseBy.ValueString())
		}
	})
}

func TestHoldingInstanceExtraction(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			// The message DiskService produces.
			name: "service error names the instance",
			err:  errorf(`lima: disk is in use: "data" is held by running instance "project-dev"`),
			want: "project-dev",
		},
		{
			// Never guess: an unrecognised shape yields a placeholder the
			// user will obviously need to replace.
			name: "unknown shape falls back to a placeholder",
			err:  errorf("something else entirely"),
			want: "INSTANCE",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := holdingInstance(tc.err); got != tc.want {
				t.Errorf("holdingInstance = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDiskInUseDetailStripsSentinel(t *testing.T) {
	t.Parallel()

	err := errorf(`lima: disk is in use: "data" is held by running instance "dev"`)
	got := diskInUseDetail(err)
	if strings.Contains(got, "lima: disk is in use") {
		t.Errorf("detail %q still carries the internal sentinel text", got)
	}
	if !strings.Contains(got, "dev") {
		t.Errorf("detail %q lost the instance name", got)
	}
}

func TestPlannedDisks(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	t.Run("unmanaged attribute is nil", func(t *testing.T) {
		t.Parallel()
		// Lima's own attachments must be left alone when the user says
		// nothing about them.
		m := instanceModel{AdditionalDisks: types.ListNull(types.StringType)}
		if got := plannedDisks(&m); got != nil {
			t.Errorf("plannedDisks = %v, want nil for an unmanaged attribute", got)
		}
	})

	t.Run("empty list is a real detach request", func(t *testing.T) {
		t.Parallel()
		list, diags := stringList(ctx, []string{})
		if diags.HasError() {
			t.Fatalf("stringList: %v", diags)
		}
		m := instanceModel{AdditionalDisks: list}
		got := plannedDisks(&m)
		if got == nil {
			t.Fatal("plannedDisks = nil, want an empty non-nil list")
		}
		if len(*got) != 0 {
			t.Errorf("plannedDisks = %v, want empty", *got)
		}
	})

	t.Run("order is preserved", func(t *testing.T) {
		t.Parallel()
		list, diags := stringList(ctx, []string{"b", "a"})
		if diags.HasError() {
			t.Fatalf("stringList: %v", diags)
		}
		m := instanceModel{AdditionalDisks: list}
		got := plannedDisks(&m)
		if got == nil || len(*got) != 2 {
			t.Fatalf("plannedDisks = %v", got)
		}
		// Lima attaches in order, so this is positional, not a set.
		if (*got)[0].Name != "b" || (*got)[1].Name != "a" {
			t.Errorf("plannedDisks = %v, want the declared order preserved", *got)
		}
	})
}

func errorf(msg string) error { return &simpleError{msg} }

type simpleError struct{ msg string }

func (e *simpleError) Error() string { return e.msg }

func TestToRenderRequestIncludesAttachedDisks(t *testing.T) {
	t.Parallel()

	// Regression guard: attachments were originally wired only into the
	// update path, so a disk declared at create time was silently not
	// attached. The generated document must carry them.
	ctx := context.Background()
	list, diags := stringList(ctx, []string{"data", "scratch"})
	if diags.HasError() {
		t.Fatalf("stringList: %v", diags)
	}

	model := instanceModel{
		Template:        types.StringValue("template:ubuntu"),
		AdditionalDisks: list,
	}
	req, d := model.toRenderRequest(declaredLists{})
	if d.HasError() {
		t.Fatalf("toRenderRequest: %v", d)
	}

	if len(req.Typed.AdditionalDisks) != 2 {
		t.Fatalf("additionalDisks = %+v, want 2 entries", req.Typed.AdditionalDisks)
	}
	// Lima attaches in order, so this is positional.
	if req.Typed.AdditionalDisks[0].Name != "data" || req.Typed.AdditionalDisks[1].Name != "scratch" {
		t.Errorf("additionalDisks = %+v, want the declared order", req.Typed.AdditionalDisks)
	}

	doc, err := lima.Render(req)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(string(doc), "additionalDisks:") {
		t.Errorf("rendered document has no additionalDisks:\n%s", doc)
	}
	if !strings.Contains(string(doc), "name: data") {
		t.Errorf("rendered document is missing the disk name:\n%s", doc)
	}
}

func TestToRenderRequestOmitsUnmanagedDisks(t *testing.T) {
	t.Parallel()

	// An unset additional_disks must not emit an empty list, which would
	// override whatever a template or raw config attached.
	model := instanceModel{
		Template:        types.StringValue("template:ubuntu"),
		AdditionalDisks: types.ListNull(types.StringType),
	}
	req, d := model.toRenderRequest(declaredLists{})
	if d.HasError() {
		t.Fatalf("toRenderRequest: %v", d)
	}
	if req.Typed.AdditionalDisks != nil {
		t.Errorf("additionalDisks = %+v, want nil", req.Typed.AdditionalDisks)
	}

	doc, err := lima.Render(req)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Contains(string(doc), "additionalDisks") {
		t.Errorf("rendered document should not mention additionalDisks:\n%s", doc)
	}
}
