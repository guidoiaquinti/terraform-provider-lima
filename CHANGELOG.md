# Changelog

All notable changes to this project are documented here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

Nothing released yet. The first entry will be added when `0.1.0` is tagged.

### Changed

- **Breaking.** `lima_instance` no longer has an `id` attribute. It held exactly
  the same value as `instance_name` for the resource's whole life, so it was two
  attributes for one fact; terraform-plugin-framework, unlike the older SDK, does
  not require one. Replace `lima_instance.x.id` with
  `lima_instance.x.instance_name`. Import is unaffected — the import ID is still
  the real Lima instance name. `lima_disk` and the data sources keep their `id`
  for now.
- `config` and `config_overrides` are no longer marked sensitive. Marking them
  meant every change to the primary configuration attribute rendered as
  `(sensitive value)`, so a user editing one line of Lima YAML could not review
  the diff — the opposite of what putting a VM definition in version control is
  for. Redaction is unchanged where content would leak without being asked for:
  YAML parse errors still never echo the document, `RedactArgs` still masks
  sensitive flags, and `provisions[].script` is still sensitive. If you embed a
  secret in raw Lima YAML it will now appear in plan output and in state; pass it
  through a provisioning script from a sensitive variable instead.
- **Breaking.** The `mount`, `port_forward` and `provision` blocks of
  `lima_instance` are now the list attributes `mounts`, `port_forwards` and
  `provisions`. Migration is mechanical — an equals sign, brackets, and a comma
  between entries:

  ```hcl
  # Before
  mount {
    location = abspath(path.module)
    writable = true
  }

  # After
  mounts = [
    { location = abspath(path.module), writable = true },
  ]
  ```

  The reason is ergonomic. A block cannot be produced by an expression, so
  deriving entries from data meant `dynamic "mount"`, and Terraform expands
  dynamic blocks *after* `ValidateResourceConfig`, which is what the previous
  release had to work around. A list attribute takes an ordinary comprehension:

  ```hcl
  mounts = [for d in var.shared_dirs : { location = d, writable = true }]
  ```

  Old configurations fail with `Blocks of type "mount" are not expected here`.
  Behaviour is otherwise unchanged: order is still significant, mounts and port
  forwards are still applied in place, provisioning still forces replacement, and
  an empty list still differs from an omitted attribute. Internally this also
  removed the duplicate validation model the block form required.

### Fixed

- The instance-name length check now runs for the **default** `LIMA_HOME`. It
  returned early whenever no `home` was configured — which is the default
  installation, and therefore most users — so the mid-apply
  `UNIX_PATH_MAX=104` failure it exists to pre-empt still arrived from Lima
  itself. An unset home now resolves to `~/.lima` before the socket path is
  measured. Note that a configuration with a long instance name which previously
  planned and then failed during apply will now fail at plan time instead, which
  is the intended behaviour but may surface as a new error.
- `terraform refresh` no longer records `start = false` for an instance whose
  status settles nothing. The value was derived from `status == "running"`, so a
  half-created, broken, or newly-introduced status all read as "stopped" and
  produced a plan proposing a start the user never asked for. Only `running` and
  `stopped` now overwrite it; anything else leaves the desired state alone. A
  genuine external stop still surfaces as drift.
- `status` no longer advertises `starting` or `stopping`. Nothing could return
  them: Lima reports `Running`, `Stopped`, `Uninitialized`, `Installing`,
  `Broken` or an empty status, so a configuration waiting for `starting` waited
  forever. Both schema descriptions are now derived from `lima.AllStatuses`, and
  a test checks the documentation against it. A status a future Lima introduces
  still arrives as `unknown` with the original in `raw_status`.
- The warning shown after `terraform import` claimed that adding mounts, port
  forwards or provisioning would force a replacement. That has been true only of
  provisioning since mounts and port forwards became in-place edits, so the
  warning discouraged users from a feature the provider had shipped. The same
  stale claim was in `docs/resources/instance.md` and in the mounts example. A
  test now derives the expectation from the schema's plan modifiers, so the
  wording cannot drift from the behaviour again.
- Creating an instance no longer runs a redundant `limactl list`. The resource
  probed for a name collision before calling the lifecycle layer, which checks
  the same thing under the instance lock and returns `ErrAlreadyExists` that the
  error path already renders as the identical import instruction. The probe cost
  a full listing per create and could not be authoritative anyway.
- Per-operation timeout defaults are now reachable. `default_timeout` was seeded
  with 20 minutes before the per-operation fallbacks were consulted, and since it
  could never be zero those fallbacks were dead code: every `lima_instance`
  operation ran on the same 20-minute budget, so `create` got 20 minutes where
  30 was documented and `read` got 20 minutes where 2 was documented. A user
  raising `default_timeout` for a slow creation also, silently, gave every
  refresh the same budget. `default_timeout` is now unset by default and acts
  purely as an override; when it is absent each operation uses its own default.
  `lima_disk` applied two different rules across its four operations and now uses
  the same one throughout, and the three data sources no longer inherit a
  create-sized budget for a read. A test compares the documented defaults against
  the constants they describe, which is the check whose absence let this survive.
- `lima_instance` can now be used with `dynamic "mount"`, `dynamic
  "port_forward"` and `dynamic "provision"` blocks. Terraform calls
  `ValidateResourceConfig` before dynamic blocks are expanded, so those lists
  arrive unknown; the configuration was decoded into Go slices, which cannot
  represent unknown, and every such configuration failed with a `Value
  Conversion Error` before planning began. Validation now decodes the
  configuration into a model holding those blocks as `types.List` and converts
  them once they are known. Checks that do not depend on block contents, such
  as instance-name validation, still run.
- `terraform plan` no longer warns *"No template or config set"* when `config`
  or `template` is set from a variable, another resource, or any other value
  that is unknown at validation time. An unknown source says nothing about
  whether one was provided, so the check is skipped rather than guessed at. The
  same applies when an unexpanded block might yet carry a typed attribute.

### Added

- `ValidateConfig` warns when a configuration sets `plain: true` alongside
  `mount` or `port_forward` blocks. Lima ignores both outright in plain mode and
  never starts the guest agent that implements forwarding, so such a
  configuration applies cleanly and simply has no forwards — the only symptom
  being a service that cannot be reached. `lima.PlainMode` exposes the
  detection.

### Documentation

- `timeouts` was documented as a block. The schema makes it an attribute, so
  the documented form was rejected outright with `Blocks of type "timeouts" are
  not expected here`. It is now shown as `timeouts = { ... }`.
- `additional_disks` was in the schema and in the README's mutability table but
  absent from the resource page entirely. It now has both a schema entry and a
  mutability row.
- Both slipped past the existing tests, which did not check what their names
  implied: `TestDocumentationCoverage` only asserted that a page exists per
  type, and `TestMutabilityMatchesDocumentation` compared the schema against a
  map hardcoded in the test rather than against the documentation. New tests
  derive from the schema and parse the real table, in both directions, so an
  undocumented attribute or a stale row now fails the build.
- Rewrote the local-development instructions. A development override removes
  the need for `terraform init` to install the *provider*, but `init` is still
  required for anything else it does, notably installing modules — and an
  override cannot survive that: init performs version selection for providers
  required by **state**, overridden providers do not take part, so the first
  init succeeds and every one after a resource exists fails against the
  registry. Configurations with both modules and state need a filesystem
  mirror, which is now documented alongside the two traps it carries: a
  prerelease version is never selected for an unconstrained requirement, and
  the dependency lock must be deleted after each rebuild.
