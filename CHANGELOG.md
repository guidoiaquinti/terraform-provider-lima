# Changelog

All notable changes to this project are documented here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

Nothing released yet. The first entry will be added when `0.1.0` is tagged.

### Changed

- **Breaking.** No type has an `id` attribute any more — not `lima_instance`,
  `lima_disk`, or any of the four data sources.

  Lima exposes no object identifier of its own. `limactl list --list-fields`
  reports 25 fields and none is an id: the closest, `AutoStartedIdentifier`, is an
  auto-start registration label and is empty unless an instance was registered for
  auto-start. The only UUID that exists is in `<LIMA_HOME>/<name>/vz-identifier`,
  which no `limactl` command surfaces, which belongs to the `vz` backend alone
  (a `qemu` instance has no equivalent), and which lives inside `LIMA_HOME` where
  the provider does not reach. In Lima's model the **name is the primary key** —
  it is the directory name, the `lima-<name>` hostname, the argument to every
  command, and the import ID.

  So an `id` here could only ever repeat a name, which is two attributes for one
  fact. Migration is mechanical:

  ```hcl
  lima_instance.dev.id        → lima_instance.dev.instance_name
  lima_disk.data.id           → lima_disk.data.name
  data.lima_instance.dev.id   → data.lima_instance.dev.name
  data.lima_disk.data.id      → data.lima_disk.data.name
  data.lima_host.this.id      → data.lima_host.this.binary_path
  ```

  terraform-plugin-framework, unlike the older SDK, does not require an `id`.
  Import is unaffected: the import ID is still the real Lima name. A test asserts
  no type reintroduces one.
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

- A failed restart after a successful reconfiguration now records what was
  applied. `limactl edit` succeeding and the following start failing is not a
  failed change: the instance has the new resources and is simply down. State kept
  the old values, so the next plan proposed a change that had already happened and
  anyone reading state saw values the instance no longer had. The error already
  explained the situation; state now agrees with it.
- Instance lookups no longer list every instance in `LIMA_HOME`. `Inspect` listed
  everything and filtered in Go so that absence was structural rather than a match
  against Lima's error text, but that cost a full `--all-fields` listing per call —
  five per create, each making Lima resolve the configuration of every instance in
  the home. A name-scoped list gives the same structural signal, because a missing
  name exits non-zero with **empty stdout** (measured against Lima 2.2.0), so
  "failed and produced no object" identifies absence without reading the message.
  Cancellation is checked first, since a cancelled command looks identical. Disk
  lookups are unchanged: `limactl disk list` accepts no name argument.
- A stray carriage return in a Lima log line no longer reaches a diagnostic,
  where it would overwrite whatever the terminal had already drawn. The
  hand-rolled unquoting always dropped `\r`; nothing tested it, so the behaviour
  was invisible and easy to lose. It is now pinned by a test covering both the
  fast path and the fallback.
- Creating several instances at once in a fresh `LIMA_HOME` no longer fails. Lima
  generates the shared SSH keypair in `_config/user` on first use by shelling out
  to `ssh-keygen` with no locking, so concurrent first creates raced: verified
  against Lima 2.2.0, four parallel `terraform apply` creates into an empty home
  produced **one** instance and three failures. The per-instance lock could not
  help, because the contended resource belongs to the home rather than to any
  instance. Creates now serialise until one has succeeded, after which the keypair
  exists and they run concurrently again — so the cost is paid once per process,
  not on every create. The same configuration now creates all four.
- A racing create is no longer reported as a name collision. The loser's message
  is `<home>/_config/user already exists. Overwrite (y/n)?`, and the provider
  matched a bare `already exists` substring, so it claimed the instance name was
  taken and advised `terraform import` for an instance that did not exist. Lima
  backtick-quotes the object in a genuine collision — ``instance `dev` already
  exists`` — so the marker is now anchored on that. The disk marker had the same
  flaw and got the same treatment.
- A failed `lima_instance` update no longer leaves state claiming a protection the
  instance does not have. Protection is cleared before the rest of an update and
  reapplied afterwards, so that a protected instance can be reconfigured in one
  apply — but a failure in between returned without writing state, leaving
  `protect = true` recorded against an instance that had just been unprotected.
  The read-back after a successful protection change had the mirror-image problem.
  Both paths now record the protection actually in effect.
- Removed an unsynchronised write on the shared `limactl` adapter. It cached the
  detected Lima version in a struct field, filled lazily by `Version()`; one
  adapter is shared by every resource and Terraform applies them in parallel, so
  the write was a latent data race. It was safe only because nothing in the
  provider called `Version()` — the version is detected once during configuration
  and kept in `providerData`. The cache, the method and `CachedVersion()` are
  gone, so the adapter now holds no mutable state at all.
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

- `terraform-registry-manifest.json`, declaring protocol version 6. The Terraform
  Registry reads the wire protocol from this file; without it a published release
  is treated as an older-SDK provider and every `terraform init` against it fails.
  GoReleaser now publishes it as a release asset under the name the registry looks
  for, and a test asserts both, because nothing else would have caught the omission
  before a release.
- A `lima_instances` data source, reporting every instance in `LIMA_HOME` with the
  same attributes as `lima_instance`. `lima_host` already reported instance names,
  but only the names, so anything data-driven needed one `lima_instance` data
  source per instance. There are deliberately no filter arguments — a Terraform
  expression filters a list better than a bespoke argument, and each filter would
  be another thing to keep consistent:

  ```hcl
  [for i in data.lima_instances.all.instances : i.name if i.status == "running"]
  ```

  Both instance data sources build their per-instance attributes from one shared
  map, and a test asserts the two describe an instance identically.
- All four data sources now accept a `timeouts` attribute with a `read` value.
  Previously a lookup inherited the provider-wide `default_timeout`, so a
  configuration that raised it to accommodate slow VM creation also gave a
  one-second `limactl list` the same budget before it would be reported as stuck.
- `default_timeout` is now validated at plan time as well as at configure time,
  using the `Duration` validator that already existed but was never wired to
  anything.
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
