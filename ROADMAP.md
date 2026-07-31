# Roadmap

Deliberately **out of scope for 0.1**. Each entry states why it was excluded,
so the boundary is a decision rather than an omission.

## Next three, in priority order

### 1. Provisioning: drift detection and re-running

Two halves of the same gap, and the last attribute that still forces
replacement.

Lima runs `provisions` scripts during creation only and does not report which
ones ran, so the provider can neither detect that a script changed nor apply a
new one to a live instance. `rerun_token` exists precisely because replacement
is the only mechanism available.

Both halves need something Lima does not currently expose: a supported way to
re-run provisioning, and a record of what ran. Until then, replacement is the
honest behaviour.

### 2. Remote Lima hosts over SSH

The command adapter is the only layer that would change, but it needs a
transport abstraction beneath it and a security model for remote execution.
Worth doing only once the local provider has seen real use.

### 3. `lima_snapshot` resource

`limactl snapshot` exists, but whether it works depends on the VM backend and
disk format of a specific instance, and there is no host-level capability flag
to detect that. A resource that fails at apply on some backends needs a way to
warn at plan time first.

## Done

Newest first.

- **Release plumbing** — shipped. `terraform-registry-manifest.json` declares
  protocol 6 and GoReleaser publishes it as a release asset; without it a
  published provider is uninstallable, and no build or test would have said so.
- **Concurrent creates into a fresh `LIMA_HOME`** — fixed. Lima generates the
  shared SSH keypair on first use by shelling out to `ssh-keygen` with no
  locking, so four parallel creates produced one instance and three failures.
  Creates now serialise until one succeeds, then run concurrently again. The
  loser's message also contains "already exists", which the provider had been
  reporting as a name collision; the marker is now anchored on Lima's
  backtick-quoted form. See the CLI contract §5.4.
- **Nested list attributes** — shipped. `mount`, `port_forward` and `provision`
  blocks became the `mounts`, `port_forwards` and `provisions` list attributes,
  so entries can be built with a `for` expression rather than a `dynamic` block.
  This also removed the duplicate validation model the block form required.
- **Per-operation timeouts** — fixed. The documented 30m/20m/20m/2m defaults were
  unreachable, because `default_timeout` was seeded before them and could never
  be zero; every operation, including refresh, ran on one 20-minute budget.
  `default_timeout` is now an override, and all four data sources accept their
  own `read` timeout.
- **`lima_instances` data source** — shipped, reporting every instance in the
  home with the same attributes as `lima_instance`. No filter arguments: a
  Terraform expression filters better and stays consistent for free.
- **`id` removed everywhere** — Lima exposes no object identifier
  (`limactl list --list-fields` has none, and the only UUID on disk belongs to
  the `vz` backend and to no `limactl` command), so an `id` could only repeat a
  name. `name` — or `instance_name` — is the identity.
- **Partial-failure state fidelity** — fixed. A failed update that had already
  cleared protection, and a failed restart after a successful edit, both returned
  without writing state, so state described something that was no longer true.
  Both now record what actually happened, and both are covered by tests that
  drive the real `Update` against a fault-injecting fake.
- **Instance lookups are name-scoped** — a missing name exits non-zero with empty
  stdout, so absence stays structural without listing every instance. A create
  went from five full-home listings to five single-instance ones.
- **`lima_disk` resource and data source** — shipped, with in-place growth,
  `additional_disks` attachment on instances, and explicit refusal to work
  around Lima's in-use lock.
- **Verified template adoption** — shipped. Declaring a template for an
  imported instance is checked against its resolved disk images; a definite
  mismatch warns. Refutes rather than confirms, because templates sharing a
  base image are indistinguishable.
- **In-place mount and port-forward changes** — shipped, via
  `limactl edit --set`, preserving template-contributed entries and pinned by
  an equivalence acceptance test.
- **Mount and port-forward drift detection** — shipped; divergence surfaces as
  a plan diff and is restored in place.
- **Import fidelity** — shipped. Import records everything Lima reports, and
  adoption of an imported instance no longer forces replacement.
- **In-place resizing of `cpus`, `memory` and `disk`** — shipped. `limactl
  edit` turned out to need no editor when explicit flags are supplied; the
  earlier assumption that it always required `$EDITOR` was wrong. The provider
  now stops a running instance, applies the edit, and restarts it, reporting a
  failed restart distinctly from a failed edit.

## Known limitations

Understood, measured, and left alone deliberately.

- **Disk lookups list every disk.** `limactl disk list` accepts no name
  argument, so there is no scoped equivalent of the instance fix above. A Lima
  limitation, not a choice.
- **`config` and `config_overrides` are not sensitive.** Marking them made every
  change to the primary configuration attribute render as `(sensitive value)`,
  which defeats reviewing a VM definition in version control. A secret written
  into raw Lima YAML will therefore appear in plan output and state; pass one
  through a `provisions` script from a sensitive variable instead.
- **No `ssh_identity_file`.** Lima reports the path, but the schema deliberately
  excludes anything named like key material, and a test enforces it. Use
  `ssh_config` with `ssh -F`.

## Later

| Capability | Why not in 0.1 |
| ---------- | -------------- |
| Instance cloning | `limactl clone` exists, but the resulting resource's relationship to its source is not obvious in Terraform's model. |
| Network resources | `limactl network` manages host-level shared networks, which are global state needing careful serialisation. |
| Guest command resource | Effectively a provisioner that runs on every apply, which the design explicitly rules out. |
| Kubernetes / Docker convenience resources | Better served by the existing Kubernetes and Docker providers pointed at a Lima instance. |
| Cluster orchestration | Out of scope for a provider whose job is one VM at a time. |

## Explicitly never

- Writing to files under `LIMA_HOME`
- Reading Lima's internal files — including `_config/user` and `vz-identifier`,
  which is why the provider has no object ID to expose
- Importing Lima's internal Go packages
- Driving QEMU, Virtualization.framework or SSH directly
- Parsing human-oriented CLI output when a machine-readable form exists
- Silently removing a user's deletion protection
