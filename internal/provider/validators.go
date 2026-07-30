// Copyright 2026 Guido Iaquinti
// SPDX-License-Identifier: Apache-2.0

package provider

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/schema/validator"

	"github.com/guidoiaquinti/terraform-provider-lima/internal/lima"
)

// The validators here run at plan time and never execute a command, so they
// work with `terraform validate` and with no Lima installed.
//
// The rule of thumb the brief asks for is applied consistently: an error is
// only used where the configuration is *definitely* wrong, and a warning where
// the provider merely does not recognise a value that a newer Lima might.

// instanceNameValidator checks Lima's naming constraints.
type instanceNameValidator struct{}

// InstanceName returns a validator for Lima instance names.
func InstanceName() validator.String { return instanceNameValidator{} }

func (instanceNameValidator) Description(context.Context) string {
	return "must be a valid Lima instance name"
}

func (v instanceNameValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (instanceNameValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if err := lima.ValidateName(req.ConfigValue.ValueString()); err != nil {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid Lima instance name", err.Error())
	}
}

// sizeValidator checks Lima-compatible byte sizes.
type sizeValidator struct{ attribute string }

// Size returns a validator for size strings such as "4GiB".
func Size(attribute string) validator.String { return sizeValidator{attribute: attribute} }

func (sizeValidator) Description(context.Context) string {
	return "must be a size such as \"4GiB\" or \"8192MiB\""
}

func (v sizeValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v sizeValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	size, err := lima.ParseSize(req.ConfigValue.ValueString())
	if err != nil {
		resp.Diagnostics.AddAttributeError(req.Path, fmt.Sprintf("Invalid %s", v.attribute), err.Error())
		return
	}
	if size <= 0 {
		resp.Diagnostics.AddAttributeError(req.Path, fmt.Sprintf("Invalid %s", v.attribute),
			fmt.Sprintf("%q resolves to zero bytes; specify a positive size.", req.ConfigValue.ValueString()))
	}
}

// yamlValidator checks that a string parses as a YAML mapping.
type yamlValidator struct{ field string }

// YAML returns a validator for a YAML mapping attribute.
func YAML(field string) validator.String { return yamlValidator{field: field} }

func (yamlValidator) Description(context.Context) string {
	return "must be a valid YAML mapping"
}

func (v yamlValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v yamlValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if err := lima.ValidateYAML(req.ConfigValue.ValueString(), v.field); err != nil {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid YAML",
			// Deliberately does not echo the document: it may contain
			// provisioning scripts or other sensitive content.
			fmt.Sprintf("The %s attribute could not be parsed: %s", v.field, err))
	}
}

// knownValueValidator warns, rather than fails, for unrecognised values.
type knownValueValidator struct {
	label string
	known []string
}

// KnownValue returns a forward-compatible validator: values outside the known
// set produce a warning, so a newer Lima backend or architecture does not
// require a provider release to become usable.
func KnownValue(label string, known []string) validator.String {
	return knownValueValidator{label: label, known: known}
}

func (v knownValueValidator) Description(context.Context) string {
	return fmt.Sprintf("should be one of the known %s values", v.label)
}

func (v knownValueValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v knownValueValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	value := req.ConfigValue.ValueString()
	if value == "" {
		resp.Diagnostics.AddAttributeError(req.Path, fmt.Sprintf("Empty %s", v.label),
			"The value must not be an empty string. Remove the attribute to use Lima's default.")
		return
	}
	if slices.Contains(v.known, value) {
		return
	}
	resp.Diagnostics.AddAttributeWarning(req.Path,
		fmt.Sprintf("Unrecognised %s value", v.label),
		fmt.Sprintf("%q is not one of the %s values this provider version knows about (%v).\n\n"+
			"The value is passed through to Lima unchanged. If your Lima release supports it, this warning is harmless; "+
			"otherwise Lima will reject it during apply.", value, v.label, v.known))
}

// requiredValueValidator rejects values outside a fixed set.
type requiredValueValidator struct {
	label   string
	allowed []string
}

// OneOf returns a validator that errors for values outside the allowed set.
// Use it only where the set is genuinely closed.
func OneOf(label string, allowed []string) validator.String {
	return requiredValueValidator{label: label, allowed: allowed}
}

func (v requiredValueValidator) Description(context.Context) string {
	return fmt.Sprintf("must be one of %v", v.allowed)
}

func (v requiredValueValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v requiredValueValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if !slices.Contains(v.allowed, req.ConfigValue.ValueString()) {
		resp.Diagnostics.AddAttributeError(req.Path, fmt.Sprintf("Invalid %s", v.label),
			fmt.Sprintf("%q is not a valid %s. Valid values are: %v.",
				req.ConfigValue.ValueString(), v.label, v.allowed))
	}
}

// absolutePathValidator requires an absolute host path.
type absolutePathValidator struct{}

// AbsolutePath returns a validator requiring an absolute path.
func AbsolutePath() validator.String { return absolutePathValidator{} }

func (absolutePathValidator) Description(context.Context) string {
	return "must be an absolute path"
}

func (v absolutePathValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (absolutePathValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	value := req.ConfigValue.ValueString()
	if value == "" {
		resp.Diagnostics.AddAttributeError(req.Path, "Empty path", "The path must not be empty.")
		return
	}
	// A leading ~ is accepted because the provider expands it before use.
	if value[0] == '/' || value[0] == '~' {
		return
	}
	resp.Diagnostics.AddAttributeError(req.Path, "Relative path",
		fmt.Sprintf("%q is a relative path. Lima resolves mount locations against its own working directory, "+
			"which is not the Terraform module directory, so relative paths are ambiguous.\n\n"+
			"Use an absolute path, a leading \"~\", or a Terraform expression such as abspath(path.module).", value))
}

// nonEmptyValidator rejects empty and whitespace-only strings.
type nonEmptyValidator struct{ label string }

// NonEmpty returns a validator rejecting blank strings.
func NonEmpty(label string) validator.String { return nonEmptyValidator{label: label} }

func (v nonEmptyValidator) Description(context.Context) string {
	return "must not be empty"
}

func (v nonEmptyValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v nonEmptyValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if isBlank(req.ConfigValue.ValueString()) {
		resp.Diagnostics.AddAttributeError(req.Path, fmt.Sprintf("Empty %s", v.label),
			fmt.Sprintf("The %s must not be empty or contain only whitespace.", v.label))
	}
}

func isBlank(s string) bool {
	for _, r := range s {
		if r != ' ' && r != '\t' && r != '\n' && r != '\r' {
			return false
		}
	}
	return true
}

// durationValidator checks a Go duration string.
type durationValidator struct{}

// Duration returns a validator for Go duration strings.
func Duration() validator.String { return durationValidator{} }

func (durationValidator) Description(context.Context) string {
	return "must be a Go duration such as \"20m\""
}

func (v durationValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (durationValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	d, err := time.ParseDuration(req.ConfigValue.ValueString())
	if err != nil {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid duration",
			fmt.Sprintf("%q is not a valid Go duration: %s\n\nUse a value such as \"30m\", \"1h\" or \"90s\".",
				req.ConfigValue.ValueString(), err))
		return
	}
	if d <= 0 {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid duration",
			fmt.Sprintf("%q must be greater than zero.", req.ConfigValue.ValueString()))
	}
}
