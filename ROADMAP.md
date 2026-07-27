# Roadmap

Deliberately **out of scope for 0.1**. Each entry states why it was excluded,
so the boundary is a decision rather than an omission.

## Next three, in priority order

### 1. Provisioning: drift detection and re-running

Two halves of the same gap, and the last attribute that still forces
replacement.

Lima runs `provision` scripts during creation only and does not report which
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
- Importing Lima's internal Go packages
- Driving QEMU, Virtualization.framework or SSH directly
- Parsing human-oriented CLI output when a machine-readable form exists
- Silently removing a user's deletion protection
