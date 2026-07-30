package provider

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	fwprovider "github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// provider.Configure is where every user-facing misconfiguration is caught: a
// missing limactl, an unsupported Lima, a malformed timeout, a prefix that
// cannot be part of an instance name. It had no coverage at all, which is a poor
// trade — it is the first thing a new user hits, and each diagnostic here is the
// only guidance they get.
//
// The version gate is worth testing for a second reason. README.md documents a
// support policy (reject below 2.0, warn above the tested version, accept in
// between) and that policy exists in exactly one place, lima.CheckVersion. A
// test that drives it through Configure is what keeps the documented table and
// the code from drifting apart.

// stubLimactl writes an executable stand-in for limactl that answers
// `--version` with the given string.
//
// Configure resolves and executes a real binary — that is the point of it — so a
// no-op stub is not enough here, unlike in the resource harness where the runner
// is replaced wholesale.
func stubLimactl(t *testing.T, versionOutput string) string {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "limactl")
	script := fmt.Sprintf(`#!/bin/sh
case "$1" in
  --version|version) printf '%%s\n' %q ;;
  *) exit 0 ;;
esac
`, versionOutput)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("writing stub limactl: %v", err)
	}
	return path
}

// configureWith runs the real provider Configure over a raw configuration.
func configureWith(t *testing.T, overrides map[string]tftypes.Value) (*fwprovider.ConfigureResponse, *providerData) {
	t.Helper()
	ctx := context.Background()

	p := New("test")()

	schemaResp := &fwprovider.SchemaResponse{}
	p.Schema(ctx, fwprovider.SchemaRequest{}, schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("provider schema: %v", schemaResp.Diagnostics)
	}

	obj, ok := schemaResp.Schema.Type().TerraformType(ctx).(tftypes.Object)
	if !ok {
		t.Fatal("provider schema type is not an object")
	}
	values := make(map[string]tftypes.Value, len(obj.AttributeTypes))
	for name, attrType := range obj.AttributeTypes {
		if override, found := overrides[name]; found {
			values[name] = override
			continue
		}
		values[name] = tftypes.NewValue(attrType, nil)
	}

	resp := &fwprovider.ConfigureResponse{}
	p.Configure(ctx, fwprovider.ConfigureRequest{
		Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: tftypes.NewValue(obj, values)},
	}, resp)

	data, _ := resp.ResourceData.(*providerData)
	return resp, data
}

func TestConfigureAcceptsATestedLimaVersion(t *testing.T) {
	t.Parallel()

	binary := stubLimactl(t, "limactl version 2.2.0")
	resp, data := configureWith(t, map[string]tftypes.Value{
		"binary": tfString(binary),
	})

	if resp.Diagnostics.HasError() {
		t.Fatalf("configure failed for a supported Lima: %v", resp.Diagnostics)
	}
	if data == nil {
		t.Fatal("configure produced no provider data")
	}
	if data.Version.Major != 2 || data.Version.Minor != 2 {
		t.Errorf("detected version = %s, want 2.2", data.Version.String())
	}
	// Nothing configured, so each operation must keep its own default.
	if data.DefaultTimeout != 0 {
		t.Errorf("DefaultTimeout = %s with nothing configured; it must stay zero so per-operation defaults apply",
			data.DefaultTimeout)
	}
}

// The 2.x floor is a support decision, and rejecting at configure time is what
// turns a confusing mid-apply failure into a clear one up front.
func TestConfigureRejectsLimaBelowTheSupportedFloor(t *testing.T) {
	t.Parallel()

	binary := stubLimactl(t, "limactl version 1.0.0")
	resp, _ := configureWith(t, map[string]tftypes.Value{
		"binary": tfString(binary),
	})

	diags := harnessDiags{resp.Diagnostics}
	diags.requireError(t, "Unsupported Lima version")
}

// A newer-than-tested Lima warns but never errors, so upgrading Lima cannot
// break a working configuration.
func TestConfigureWarnsButAcceptsANewerLima(t *testing.T) {
	t.Parallel()

	binary := stubLimactl(t, "limactl version 99.0.0")
	resp, data := configureWith(t, map[string]tftypes.Value{
		"binary": tfString(binary),
	})

	if resp.Diagnostics.HasError() {
		t.Fatalf("a newer Lima must not be an error: %v", resp.Diagnostics)
	}
	if data == nil {
		t.Fatal("configure produced no provider data")
	}
	diags := harnessDiags{resp.Diagnostics}
	if !diags.hasWarning("Untested Lima version") {
		t.Errorf("a newer-than-tested Lima produced no warning; diagnostics:\n%s", diags.text())
	}
}

func TestConfigureReportsAMissingBinary(t *testing.T) {
	t.Parallel()

	resp, _ := configureWith(t, map[string]tftypes.Value{
		"binary": tfString(filepath.Join(t.TempDir(), "definitely-not-here")),
	})

	diags := harnessDiags{resp.Diagnostics}
	diags.requireError(t, "limactl")
	// The remedy has to include how to point the provider somewhere else.
	if !strings.Contains(diags.text(), EnvBinary) {
		t.Errorf("the diagnostic does not mention the %s override; got:\n%s", EnvBinary, diags.text())
	}
}

// A binary that exists but is not limactl fails at version detection, which is a
// different diagnostic from "not found" and points at a different fix.
func TestConfigureReportsAnUnusableBinary(t *testing.T) {
	t.Parallel()

	binary := stubLimactl(t, "this is not a version string")
	resp, _ := configureWith(t, map[string]tftypes.Value{
		"binary": tfString(binary),
	})

	diags := harnessDiags{resp.Diagnostics}
	diags.requireError(t, "Unable to determine the Lima version")
	if !strings.Contains(diags.text(), "--version") {
		t.Errorf("the diagnostic does not suggest running --version by hand; got:\n%s", diags.text())
	}
}

func TestConfigureValidatesDefaultTimeout(t *testing.T) {
	t.Parallel()

	binary := stubLimactl(t, "limactl version 2.2.0")

	for _, tc := range []struct {
		name  string
		value string
		valid bool
	}{
		{"a duration", "45m", true},
		{"hours", "1h30m", true},
		{"not a duration", "soon", false},
		{"bare number", "20", false},
		// Zero would silently mean "no timeout at all" if accepted, so it is
		// rejected rather than treated as unset.
		{"zero", "0s", false},
		{"negative", "-5m", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			resp, data := configureWith(t, map[string]tftypes.Value{
				"binary":          tfString(binary),
				"default_timeout": tfString(tc.value),
			})
			diags := harnessDiags{resp.Diagnostics}

			if tc.valid {
				diags.requireNoError(t)
				if data == nil || data.DefaultTimeout <= 0 {
					t.Errorf("%q was accepted but produced DefaultTimeout %v", tc.value, data)
				}
				return
			}
			diags.requireError(t, "default_timeout")
		})
	}
}

// The prefix becomes part of a real Lima instance name, so it has to satisfy the
// same character rules — caught here rather than at apply.
func TestConfigureValidatesNamePrefix(t *testing.T) {
	t.Parallel()

	binary := stubLimactl(t, "limactl version 2.2.0")

	t.Run("a usable prefix", func(t *testing.T) {
		t.Parallel()
		resp, data := configureWith(t, map[string]tftypes.Value{
			"binary":      tfString(binary),
			"name_prefix": tfString("acme-"),
		})
		harnessDiags{resp.Diagnostics}.requireNoError(t)
		if data == nil || data.NamePrefix != "acme-" {
			t.Errorf("NamePrefix = %v, want acme-", data)
		}
	})

	t.Run("a prefix Lima would reject", func(t *testing.T) {
		t.Parallel()
		resp, _ := configureWith(t, map[string]tftypes.Value{
			"binary":      tfString(binary),
			"name_prefix": tfString("Bad Prefix!"),
		})
		harnessDiags{resp.Diagnostics}.requireError(t, "name_prefix")
	})
}

// Provider configuration cannot depend on a value produced by another resource:
// the provider is configured before those exist. Saying so plainly beats the
// framework's generic unknown-value failure.
func TestConfigureRejectsUnknownConfigurationValues(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"binary", "home", "default_timeout", "name_prefix"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			resp, _ := configureWith(t, map[string]tftypes.Value{
				name: tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
			})
			diags := harnessDiags{resp.Diagnostics}
			diags.requireError(t, "not known until apply")
			if !strings.Contains(diags.text(), name) {
				t.Errorf("the diagnostic does not name the offending attribute %q; got:\n%s", name, diags.text())
			}
		})
	}
}

// Explicit configuration wins over the environment, in both directions.
func TestConfigurePrefersExplicitValuesOverTheEnvironment(t *testing.T) {
	// Not parallel: mutates process environment.
	good := stubLimactl(t, "limactl version 2.2.0")

	t.Setenv(EnvBinary, filepath.Join(t.TempDir(), "not-limactl"))
	resp, data := configureWith(t, map[string]tftypes.Value{
		"binary": tfString(good),
	})
	harnessDiags{resp.Diagnostics}.requireNoError(t)
	if data == nil || data.Binary != good {
		t.Errorf("Binary = %v, want the explicitly configured %q", data, good)
	}

	// And with nothing explicit, the environment is used.
	t.Setenv(EnvBinary, good)
	t.Setenv(EnvNamePrefix, "envpfx-")
	resp, data = configureWith(t, nil)
	harnessDiags{resp.Diagnostics}.requireNoError(t)
	if data == nil || data.NamePrefix != "envpfx-" {
		t.Errorf("NamePrefix = %v, want envpfx- from the environment", data)
	}
}

// An unknown value inside the environment map is the same problem as an unknown
// top-level attribute, and was a separate code path.
func TestConfigureRejectsAnUnknownEnvironmentValue(t *testing.T) {
	t.Parallel()

	binary := stubLimactl(t, "limactl version 2.2.0")
	resp, _ := configureWith(t, map[string]tftypes.Value{
		"binary": tfString(binary),
		"environment": tftypes.NewValue(
			tftypes.Map{ElementType: tftypes.String},
			map[string]tftypes.Value{
				"TOKEN": tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
			}),
	})

	diags := harnessDiags{resp.Diagnostics}
	diags.requireError(t, "not known until apply")
	if !strings.Contains(diags.text(), "TOKEN") {
		t.Errorf("the diagnostic does not name the offending environment key; got:\n%s", diags.text())
	}
}

// homeLabel is what every "object not found" diagnostic uses to say *where* it
// looked. Getting it wrong sends people hunting in the wrong directory.
func TestHomeLabel(t *testing.T) {
	t.Parallel()

	if got := homeLabel(nil); !strings.Contains(got, "default") {
		t.Errorf("homeLabel(nil) = %q, want it to describe Lima's default home", got)
	}
	if got := homeLabel(&providerData{}); !strings.Contains(got, "default") {
		t.Errorf("homeLabel(empty) = %q, want it to describe Lima's default home", got)
	}
	if got := homeLabel(&providerData{Home: "/tmp/x"}); !strings.Contains(got, "/tmp/x") {
		t.Errorf("homeLabel = %q, want it to name the configured home", got)
	}
}

// providerDataFrom guards the one type assertion every resource makes. A nil is
// expected — the framework calls schema methods before Configure — but a wrong
// type is a provider bug and must say so rather than panic.
func TestProviderDataFrom(t *testing.T) {
	t.Parallel()

	t.Run("nil is expected and silent", func(t *testing.T) {
		t.Parallel()
		var diags collectingDiags
		if got := providerDataFrom(nil, &diags); got != nil {
			t.Errorf("providerDataFrom(nil) = %v, want nil", got)
		}
		if diags.errors != 0 {
			t.Errorf("nil provider data produced %d errors; the framework passes nil routinely", diags.errors)
		}
	})

	t.Run("the right type passes through", func(t *testing.T) {
		t.Parallel()
		var diags collectingDiags
		want := &providerData{NamePrefix: "x-"}
		if got := providerDataFrom(want, &diags); got != want {
			t.Errorf("providerDataFrom returned %v, want the same pointer", got)
		}
	})

	t.Run("a wrong type is reported as a provider bug", func(t *testing.T) {
		t.Parallel()
		var diags collectingDiags
		if got := providerDataFrom("not provider data", &diags); got != nil {
			t.Errorf("providerDataFrom returned %v for a wrong type, want nil", got)
		}
		if diags.errors == 0 {
			t.Error("a wrong provider data type produced no diagnostic")
		}
		if !strings.Contains(diags.text, "bug") {
			t.Errorf("the diagnostic does not identify itself as a provider bug: %q", diags.text)
		}
	})
}

// collectingDiags satisfies the narrow interface providerDataFrom accepts.
type collectingDiags struct {
	errors int
	text   string
}

func (c *collectingDiags) AddError(summary, detail string) {
	c.errors++
	c.text += summary + "\n" + detail + "\n"
}

func TestSortedKeys(t *testing.T) {
	t.Parallel()

	// Only the keys are ever logged, never the values, so this is what appears
	// in a debug log for a provider configured with secrets in `environment`.
	got := sortedKeys(map[string]string{"b": "2", "a": "1", "c": "3"})
	if strings.Join(got, ",") != "a,b,c" {
		t.Errorf("sortedKeys = %v, want [a b c]", got)
	}
	if len(sortedKeys(nil)) != 0 {
		t.Error("sortedKeys(nil) is not empty")
	}
}
