// Copyright 2026 Guido Iaquinti
// SPDX-License-Identifier: Apache-2.0

// Package lima is the Lima integration layer.
//
// It holds two of the provider's three layers:
//
//   - Service, the domain and lifecycle layer, which decides *when* to run a
//     Lima command based on observed state.
//   - ExecClient, the limactl command adapter, which owns executable
//     discovery, environment construction, argument construction, execution,
//     cancellation, exit codes, structured-output parsing and redaction.
//
// Nothing here depends on the Terraform plugin framework beyond tflog, so the
// whole package is testable with a fake limactl and no Terraform machinery.
//
// limactl is treated as the public integration API. The observed contract is
// recorded in docs/development/lima-cli-contract.md.
package lima

import "context"

// Client is the whole surface the provider uses to talk to Lima. Every method
// maps onto exactly one `limactl` invocation, except Inspect, which is a
// filtered List.
//
// Deviations from the original design sketch, both forced by Lima 2.x reality
// and documented in docs/development/lima-cli-contract.md:
//
//   - ShowSSH is absent. `limactl show-ssh` is deprecated and has no
//     machine-readable output; SSH details come from Inspect instead
//     (Instance.SSH).
//   - Create takes a rendered YAML document rather than a template name,
//     because `limactl create` accepts a single positional source and the
//     provider always composes one document via Lima's `base:` mechanism.
//   - Validate, Protect, Unprotect and Info were added; each has a real,
//     scriptable command behind it.
type Client interface {
	// Info returns host capabilities from `limactl info`.
	Info(ctx context.Context) (HostInfo, error)

	// Validate checks a rendered configuration document without creating
	// anything, using `limactl validate`.
	Validate(ctx context.Context, doc []byte) error

	// Create registers a new instance from a rendered configuration
	// document. The instance is left stopped.
	Create(ctx context.Context, req CreateRequest) error

	// Edit changes an existing instance's resources in place.
	//
	// Lima refuses to edit a running instance, so callers must stop it
	// first; Service.Resize handles that.
	Edit(ctx context.Context, name string, req EditRequest) error

	// Start boots an instance and blocks until Lima reports it ready.
	Start(ctx context.Context, name string) error

	// Stop shuts an instance down gracefully.
	//
	// Lima errors when asked to stop an already-stopped instance, so callers
	// must check status first; the lifecycle layer does this.
	Stop(ctx context.Context, name string) error

	// Delete removes an instance. Deleting an absent instance succeeds.
	Delete(ctx context.Context, name string) error

	// Protect marks an instance as protected against deletion.
	Protect(ctx context.Context, name string) error

	// Unprotect clears the protection flag.
	Unprotect(ctx context.Context, name string) error

	// ResolveTemplate expands a template reference into its effective
	// configuration, without creating anything.
	ResolveTemplate(ctx context.Context, ref string) (Template, error)

	// Inspect returns a single instance, or ErrNotFound.
	Inspect(ctx context.Context, name string) (Instance, error)

	// List returns every instance in LIMA_HOME.
	List(ctx context.Context) ([]Instance, error)
}

// CreateRequest describes one `limactl create` invocation.
type CreateRequest struct {
	// Name is the Lima instance name, already prefixed if the provider is
	// configured with a name_prefix.
	Name string
	// Document is the complete rendered Lima YAML configuration. The adapter
	// writes it to a private temporary file and passes that path to Lima.
	Document []byte
}

// EditRequest describes an in-place resource change.
//
// A zero field means "leave unchanged", which maps directly onto Lima's
// behaviour of only altering the flags it is given.
type EditRequest struct {
	CPUs        int64
	MemoryBytes int64
	DiskBytes   int64

	// Mounts and PortForwards replace the instance's whole resolved list when
	// non-nil. A nil slice pointer means "leave alone"; a pointer to an empty
	// slice means "set to empty", which is a meaningful request.
	//
	// These must already include any entries the base template contributed:
	// `--set` operates on the already-merged configuration and base merging
	// does not run again on edit. Service.Resize computes that.
	Mounts       *[]Mount
	PortForwards *[]PortForward
	// AdditionalDisks replaces the instance's attached-disk list when
	// non-nil. Lima resolves an empty list back to null, so callers must
	// treat both as "none attached".
	AdditionalDisks *[]AdditionalDisk
}

// IsEmpty reports whether the request would change nothing.
func (e EditRequest) IsEmpty() bool {
	return e.CPUs == 0 && e.MemoryBytes == 0 && e.DiskBytes == 0 &&
		e.Mounts == nil && e.PortForwards == nil && e.AdditionalDisks == nil
}

// Template is the decoded subset of a resolved Lima template.
type Template struct {
	// Images are the disk images the template would use. Comparing these
	// against an instance's resolved images is the only reliable way to tell
	// whether a claimed template is plausible.
	Images []TemplateImage `yaml:"images"`
}

// TemplateImage is one image entry in a resolved template.
type TemplateImage struct {
	Location string `yaml:"location"`
	Arch     string `yaml:"arch"`
}

// ImageLocations returns the template's image URLs.
func (t Template) ImageLocations() []string {
	out := make([]string, 0, len(t.Images))
	for _, i := range t.Images {
		if i.Location != "" {
			out = append(out, i.Location)
		}
	}
	return out
}

// Compile-time assertion that the exec adapter satisfies the interface.
var _ Client = (*ExecClient)(nil)
