# Design: timeout defaults, nested attributes, import warning

Date: 2026-07-30
Status: approved, not yet implemented

Three changes to `terraform-provider-lima`, arising from a review of the
codebase. They are independent and can land in any order, but they touch
overlapping files so they are specified together.

## 1. Per-operation timeout defaults are unreachable

### Problem

`instanceResource.defaultTimeout` returns the provider-wide default whenever it
is greater than zero:

```go
func (r *instanceResource) defaultTimeout(fallback time.Duration) time.Duration {
	if r.data != nil && r.data.DefaultTimeout > 0 {
		return r.data.DefaultTimeout
	}
	return fallback
}
```

`providerData.DefaultTimeout` is always greater than zero. `Configure` seeds it
with `DefaultTimeout` (20 minutes) and only replaces it with a parsed value that
it has already rejected if `<= 0`. The `fallback` argument is therefore dead, and
with it every entry in `lima.DefaultTimeouts`.

Two consequences:

- `create` gets 20 minutes where `docs/resources/instance.md` documents 30, and
  `read` gets 20 minutes where the same table documents 2.
- A user who sets `default_timeout = "1h"` to give slow VM creation more room
  silently gives every refresh an hour as well.

`diskResource` has the same bug in a different shape: create, update and delete
read `r.data.DefaultTimeout` directly while read uses `lima.DefaultTimeouts.Read`.
Two resources, two rules, neither matching the documentation.

The root cause is representational. `providerData.DefaultTimeout` cannot
distinguish "the user asked for 20 minutes" from "nobody asked".

### Design

Make zero mean "not configured".

- `Configure` stops seeding `timeout` with a default. It assigns only when
  `default_timeout` or `LIMA_PROVIDER_DEFAULT_TIMEOUT` yields a value, keeping
  the existing validation that rejects unparseable and non-positive durations.
- A single helper on `providerData` replaces both resources' logic:

  ```go
  // timeout returns the provider-wide default when one was configured, and the
  // per-operation default otherwise.
  func (d *providerData) timeout(perOperation time.Duration) time.Duration
  ```

  It tolerates a nil receiver, because `r.data` is nil until `Configure` runs.
- `instanceResource.defaultTimeout` is deleted. Its four call sites become
  `r.data.timeout(lima.DefaultTimeouts.Create)` and so on.
- `diskResource` moves onto the same helper for all four operations.
- The `provider.DefaultTimeout` constant becomes unused and is removed. The
  `default_timeout` schema description is rewritten to name the per-operation
  defaults rather than a single number.

Resulting behaviour, which is what the documentation already claims: unset means
create 30m, update 20m, delete 20m, read 2m; set overrides all four.

### Tests

- Unit test: with no `default_timeout`, each operation receives its
  per-operation default. With one set, all four receive it.
- Documentation test: parse the defaults out of the Timeouts section of
  `docs/resources/instance.md` and compare them against `lima.DefaultTimeouts`.
  This is the check whose absence let the bug live, so it is part of the fix
  rather than an extra.

## 2. Blocks become plural nested attributes

### Problem

`mount`, `port_forward` and `provision` are `ListNestedBlock`s. Users who want to
derive them from data must write `dynamic "mount" { ... }`, which is verbose and
which Terraform expands *after* `ValidateResourceConfig`. That ordering forced a
workaround: `instanceConfigModel`, a field-for-field duplicate of
`instanceModel` holding the three lists as `types.List`, plus `validationModel`,
`elementsIfKnown`, `hasUnknownBlocks`, and a reflection test to keep the two
models in step.

Note that converting blocks to attributes does not by itself remove the unknown
value problem. An attribute list can be unknown too, for instance
`mounts = var.mounts` where the variable resolves at apply time. What removes the
duplicate model is holding the lists as `types.List` in a *single* model and
decoding them where they are known. That could have been done without touching
blocks.

The block-to-attribute change therefore stands on ergonomics alone: a list
attribute accepts an ordinary `for` expression.

```hcl
mounts = [for d in var.shared_dirs : { location = d, writable = true }]
```

The provider is unpublished, so this is the moment to make a breaking schema
change rather than after a registry release.

### Design

**Schema.** The `Blocks` map is deleted. Three `schema.ListNestedAttribute`
entries are added to `Attributes`, named `mounts`, `port_forwards` and
`provisions`.

List rather than set, because Lima treats the order of mounts and port forwards
as significant and the resource documentation already promises that order is
preserved.

Nested attributes, validators and defaults carry over unchanged, including
`writable` defaulting to `false`, `protocol` to `tcp` and `mode` to `system`.
`provisions` keeps `listplanmodifier.RequiresReplace()`; the other two keep no
replace modifier, because both are applied in place.

The `blockMount`, `blockPortForward` and `blockProvision` constants are renamed
to `attrMounts`, `attrPortForwards` and `attrProvisions` and take the new plural
values. They remain the single source shared by the schema, the diagnostic paths
and the tests.

**Model.** `instanceModel` holds the three lists as `types.List`.
`instanceConfigModel`, `validationModel` and `hasUnknownBlocks` are deleted.

One decode step feeds every entry point:

```go
// declaredLists is the decoded form of the three list attributes.
type declaredLists struct {
	Mounts       []mountModel
	PortForwards []portModel
	Provisions   []provModel

	// MountsManaged and PortForwardsManaged distinguish an absent attribute
	// from an empty one: absent means leave Lima's own entries alone, empty
	// means the user removed every entry and wants them gone.
	MountsManaged       bool
	PortForwardsManaged bool

	// Unknown reports that at least one list is not yet resolved, so its
	// emptiness proves nothing about the final configuration.
	Unknown bool
}

func (m *instanceModel) declared(ctx context.Context) (declaredLists, diag.Diagnostics)
```

The two `Managed` flags preserve the null-versus-empty distinction that
`plannedMounts` and `plannedPortForwards` depend on. Relying on a nil slice for
this would be fragile, because `ElementsAs` on a known empty list yields an
empty non-nil slice.

`toRenderRequest`, `configuredMounts`, `plannedMounts`, `configuredPortForwards`,
`plannedPortForwards` and the validation function take `declaredLists` rather
than `*instanceModel`.

`validateInstanceConfigWithBlocks` collapses back into `validateInstanceConfig`,
because the unknown flag now travels inside `declaredLists` instead of as a
separate parameter.

**Writing back.** `reconcileDeclaredBlocks` currently rewrites the typed slices
in place. With `types.List` it must re-encode, which needs an element type. It
takes that from the value it is replacing:

```go
types.ListValueFrom(ctx, m.Mounts.ElementType(ctx), kept)
```

Deriving the type from the value rather than hand-writing an `attr.Type` means
there is nothing to drift when a nested attribute is added. A null list is never
written back, so there is no case where the element type is unavailable.

### Blast radius

- 49 Go references to the `Mounts`, `PortForwards` and `Provisions` fields
- 11 references to the three block-name constants
- 5 HCL configurations in `internal/provider/acceptance_test.go`
- 3 example files: `mounts`, `port_forwards`, `provisioning`
- 12 mentions across `docs/`, including the mutability table
- `internal/provider/instance_dynamic_block_test.go`, which is reframed:
  `dynamic` blocks no longer apply to these attributes, so the equivalent hazard
  is an unknown list arriving from a variable. The test drives that instead.
- `internal/provider/docs_test.go` iterates `schema.Blocks`, which is now empty;
  the mutability table rows are renamed to the plural attribute names.

### Tests

- The reframed unknown-list test, driving the real schema and a real
  `tfsdk.Config` as the current dynamic-block test does.
- Existing validation tests, updated for the renamed paths. Duplicate detection,
  plain-mode warnings and the mount and port-forward merge logic are unchanged
  in behaviour and their tests should continue to pass on renamed input.
- `docs_test.go` continues to derive its assertions from the schema, so it
  covers the new attributes without new cases.

## 3. The import warning contradicts the provider

### Problem

`ImportState` warns:

> Mount, port_forward and provision blocks were also left unset. Adding one
> plans a replacement, because the provider has no way to apply it to an
> existing instance.

Only `provision` carries `RequiresReplace`. Mounts and port forwards have been
applied in place since the `limactl edit --set` work, which the mutability table
and `ROADMAP.md` both record. The warning actively discourages users from a
shipped feature.

`examples/resources/lima_instance/mounts/main.tf` carries the same stale claim
in a trailing comment.

### Design

Rewrite the warning to say that mounts and port forwards are left unset and are
applied in place on the next apply, and that only provisioning forces
replacement. Fix the example comment.

### Tests

A unit test derives the claim from the schema rather than restating it: for each
of the three list attributes it checks whether the schema declares
`RequiresReplace`, then asserts the warning text mentions replacement only for
those that do. Adding a replace modifier without updating the warning, or
removing one without updating it, then fails.

## Verification

- `go build ./...`, `go vet ./...`, `go test ./...`, `golangci-lint run`
- `terraform fmt -check -recursive ./examples`
- `terraform validate` against a locally built provider through `dev_overrides`,
  for at least one rewritten example
- `make testacc` creates real VMs and can run for two hours. The configurations
  are updated as part of this work but the suite is not run by default; running
  it is a separate, explicit decision.

## Out of scope

The review that produced these three items raised others, deliberately excluded
here: the `Inspect`-as-`List` cost, `ValidateNameForHome` skipping the default
`LIMA_HOME`, protection being cleared before work that can fail, the unreachable
`starting` and `stopping` statuses, `id` duplicating `instance_name`, `config`
being marked sensitive, and the unused exported surface. Each is separable and
gets its own decision.
