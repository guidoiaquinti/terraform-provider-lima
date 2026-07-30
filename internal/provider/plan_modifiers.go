// Copyright 2026 Guido Iaquinti
// SPDX-License-Identifier: Apache-2.0

package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"

	"github.com/guidoiaquinti/terraform-provider-lima/internal/lima"
)

// Sizes are deliberately NOT canonicalised by a plan modifier.
//
// An earlier revision rewrote the planned value ("8192MiB" -> "8GiB") so that
// equivalent spellings would not differ. That fought with Read, which writes
// the canonical form into state: Terraform core decides an update is needed by
// comparing the raw config to prior state *before* plan modifiers run, so a
// non-canonical size produced a permanent "1 to change" plan whose apply did
// nothing. Verified directly against Terraform 1.x.
//
// Instead, Read preserves whatever spelling the user wrote whenever it is
// semantically identical to what Lima reports (see instanceModel.
// applyObservedConfig), and every comparison is done on byte counts. Any
// spelling is then stable, and real drift still surfaces.

// diskGrowOnlyModifier forbids shrinking the primary disk.
//
// Lima has no supported shrink operation, and a replacement triggered by a
// smaller number would silently destroy the VM's data. Failing the plan is the
// safe behaviour.
type diskGrowOnlyModifier struct{}

// DiskGrowOnly returns a plan modifier rejecting disk shrinks.
func DiskGrowOnly() planmodifier.String { return diskGrowOnlyModifier{} }

func (diskGrowOnlyModifier) Description(context.Context) string {
	return "rejects a disk size smaller than the current one"
}

func (m diskGrowOnlyModifier) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (diskGrowOnlyModifier) PlanModifyString(_ context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	// Only meaningful on update, where a prior size is known.
	if req.StateValue.IsNull() || req.StateValue.IsUnknown() {
		return
	}
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}

	oldSize, err := lima.ParseSize(req.StateValue.ValueString())
	if err != nil {
		return
	}
	newSize, err := lima.ParseSize(req.ConfigValue.ValueString())
	if err != nil {
		return
	}

	if newSize < oldSize {
		resp.Diagnostics.AddAttributeError(req.Path,
			"Disk cannot be shrunk",
			fmt.Sprintf("The disk is currently %s and the configuration requests %s.\n\n"+
				"Lima provides no supported way to shrink a disk, and replacing the instance "+
				"to apply a smaller disk would destroy its data.\n\n"+
				"To proceed deliberately, destroy the instance and create it again, "+
				"or restore the disk size to %s or larger.",
				lima.FormatSize(oldSize), lima.FormatSize(newSize), lima.FormatSize(oldSize)))
	}
}

// replaceOnRealChangeModifier forces replacement only for a genuine change
// between two known values.
//
// Plain RequiresReplace also fires on two transitions that should not destroy
// a VM:
//
//   - null → value, which is how an **imported** instance is adopted. Lima
//     does not record which template an instance came from, so import leaves
//     `template` unset; the user then writes what is already true. Recreating
//     the VM at that point would destroy data to end up where it started.
//   - value → null, which is a user removing an attribute from configuration.
//     That means "stop managing this", not "rebuild the machine".
//
// Both are treated as no-ops here. A change between two known, different
// values still replaces, which is the case that genuinely cannot be applied to
// a live instance.
type replaceOnRealChangeModifier struct{}

// ReplaceOnRealChange returns a plan modifier that replaces only when both the
// prior and configured values are known and differ.
func ReplaceOnRealChange() planmodifier.String { return replaceOnRealChangeModifier{} }

func (replaceOnRealChangeModifier) Description(context.Context) string {
	return "replaces the instance only when this attribute changes between two known values"
}

func (m replaceOnRealChangeModifier) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (replaceOnRealChangeModifier) PlanModifyString(_ context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	// Never applies to create or destroy.
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}
	// Adoption of an imported instance, or the user un-managing the attribute.
	if req.StateValue.IsNull() || req.ConfigValue.IsNull() {
		return
	}
	if req.ConfigValue.IsUnknown() {
		return
	}
	if req.StateValue.Equal(req.ConfigValue) {
		return
	}

	resp.RequiresReplace = true
}
