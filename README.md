# Terraform provider for Lima

[![test](https://github.com/guidoiaquinti/terraform-provider-lima/actions/workflows/test.yml/badge.svg)](https://github.com/guidoiaquinti/terraform-provider-lima/actions/workflows/test.yml)
[![acceptance](https://github.com/guidoiaquinti/terraform-provider-lima/actions/workflows/acceptance.yml/badge.svg)](https://github.com/guidoiaquinti/terraform-provider-lima/actions/workflows/acceptance.yml)
[![lint](https://github.com/guidoiaquinti/terraform-provider-lima/actions/workflows/lint.yml/badge.svg)](https://github.com/guidoiaquinti/terraform-provider-lima/actions/workflows/lint.yml)
[![security](https://github.com/guidoiaquinti/terraform-provider-lima/actions/workflows/security.yml/badge.svg)](https://github.com/guidoiaquinti/terraform-provider-lima/actions/workflows/security.yml)

This Terraform / OpenTofu provider allows managing local
[Lima](https://lima-vm.io/) virtual machines declaratively.

Note: this provider is pre-1.0. The lifecycle is implemented and verified
against real VMs, but the schema may change in a breaking way between minor
releases — see [Versioning and stability](#versioning-and-stability) for what
that means for a configuration you depend on, and how to pin against it.

## Why

Lima is an excellent way to run Linux VMs on macOS and Linux, but its state
lives in an imperative CLI. If a team's development environment is a Lima VM
with specific mounts, port forwards and provisioning, that definition tends to
end up in a README rather than in code.

This provider lets you describe the VM in Terraform / OpenTofu, so it can be versioned,
reviewed and reproduced like any other infrastructure.

## Prerequisites

- [Lima](https://lima-vm.io/docs/installation/) 2.0 or newer, with `limactl`
  on `PATH`
- Terraform 1.0+ or OpenTofu 1.6+ — both floors are exercised in CI on every
  pull request, against a fixture that names the provider's whole schema
  surface. See [CLI compatibility][cli-compat].

[cli-compat]: CONTRIBUTING.md#cli-compatibility

Verify Lima works before using the provider:

```console
$ limactl --version
limactl version 2.2.0
$ limactl info | head -1
{
```

## Quick start

```hcl
terraform {
  required_providers {
    lima = {
      source = "guidoiaquinti/lima"
      # Pin to the patch range while the provider is pre-1.0. See
      # "Versioning and stability" below for why the minor is not enough.
      version = "~> 0.1.0"
    }
  }
}

provider "lima" {}

resource "lima_instance" "dev" {
  name     = "project-dev"
  template = "template:ubuntu"

  cpus   = 4
  memory = "8GiB"
  disk   = "50GiB"
}

output "ssh" {
  value = "ssh -F ${lima_instance.dev.ssh_config} ${lima_instance.dev.hostname}"
}
```

More examples, all of which are valid configurations, live in
[`examples/`](examples/).

## Versioning and stability

This provider follows [Semantic Versioning](https://semver.org/) and is
currently in the **0.x** line, where §4 permits the public interface to change
at any time. Concretely, until `1.0.0`:

- A **minor** bump (`0.1.0` → `0.2.0`) may rename, retype or remove an
  attribute, change what forces replacement, or change an import address.
- A **patch** bump (`0.1.0` → `0.1.1`) is fixes only, and never a schema change
  that an existing configuration or state has to react to.

**Pin to the patch range, not the minor.** Terraform does not special-case `0.x`
the way some package managers do, so `~> 0.1` expands to `>= 0.1, < 1.0` and
will happily upgrade you across a breaking minor:

```hcl
version = "~> 0.1.0" # >= 0.1.0, < 0.2.0 — breaking changes stay opt-in
```

This matters more for a provider than for a library. The interface here is not
just configuration you can edit; it is also recorded in **state**, so a breaking
schema change can require `terraform state` surgery rather than a search and
replace. In exchange for permitting those changes pre-1.0, every one of them
gets an entry in [`CHANGELOG.md`](CHANGELOG.md) under a `Changed` or `Removed`
heading, with the migration steps — a breaking change with no documented upgrade
path is treated as a bug.

`1.0.0` is the point at which the schema is declared stable and breaking changes
require a major bump. The planned work in [`ROADMAP.md`](ROADMAP.md) — in
particular provisioning drift detection — is where a schema change is most
likely to come from before then.

## Supported Lima versions

**Lima 2.0 or newer is required.** The 1.x line is not supported.

## Supported host platforms

| Platform      | Status                                                             |
| ------------- | ------------------------------------------------------------------ |
| Linux amd64   | Supported. Exercised against real VMs (`qemu`) on every pull request. |
| Linux arm64   | Supported. Exercised against real VMs (`qemu`) on every pull request. |
| macOS arm64   | Supported. Exercised against real VMs (`vz`) locally before release.  |
| macOS amd64   | Supported (`vz`), not exercised against real VMs.                     |
| Windows       | Untested. Lima supports WSL2; the provider is not verified there.     |

Terraform 1.0+ and OpenTofu 1.6+ are both validated in CI on every pull
request against a fixture that names the provider's whole schema surface.

How that coverage is produced, which runner classes it uses, and why there is
no macOS acceptance job are recorded in
[`CONTRIBUTING.md`](CONTRIBUTING.md#continuous-integration).

## Supported resources

Support is described against what `limactl` itself can do. **Full** means every
operation Lima exposes for that object is reachable through the provider;
**partial** means something Lima can do is deliberately or necessarily absent,
and the gap is named.

| Resource / data source     | Kind        | Support    | Coverage                                                                                                                                                              |
| -------------------------- | ----------- | ---------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `lima_instance`            | Resource    | 🟡 Partial | Create, read, update, delete and import. Typed attributes for the common fields; `config` and `config_overrides` reach anything Lima accepts but the schema does not name. Gap: `provisions` can be neither re-run nor drift-detected, because Lima runs scripts at creation only and does not record what ran. |
| `lima_disk`                | Resource    | 🟡 Partial | Create, read, grow, delete and import, matching `limactl disk`. Gap: `limactl disk unlock` is not exposed, by choice — the provider cannot distinguish a stale lock from a live one. |
| `lima_instance`            | Data source | 🟡 Partial | Identity, sizing, status, protection and the SSH endpoint from `limactl list --all-fields`. Gap: guest IP addresses and the resolved mount and network lists are not surfaced — see [Limitations](#limitations). |
| `lima_disk`                | Data source | ✅ Full     | Everything `limactl disk list` reports: size, format, backing directory, mount point and the instance currently holding it.                                             |
| `lima_host`                | Data source | 🟡 Partial | Lima version, host OS and architecture, available VM types, `LIMA_HOME`, the `limactl` path, templates and instance names. Gap: `limactl info` also returns `defaultTemplate` (omitted as too large) and `identityFile` (omitted as key material). |

### Not supported yet

Priorities and the full reasoning live in [`ROADMAP.md`](ROADMAP.md).

| Capability                       | Status              | Why not yet                                                                                                       |
| -------------------------------- | ------------------- | ------------------------------------------------------------------------------------------------------------------ |
| Provisioning re-run and drift    | Planned, next       | Needs something Lima does not expose: a supported way to re-run provisioning, and a record of what ran.            |
| Remote Lima hosts over SSH       | Planned             | Only the command adapter changes, but it needs a transport abstraction and a security model for remote execution.  |
| `lima_snapshot`                  | Planned             | `limactl snapshot` works only on some backends and disk formats, with no capability flag to detect that at plan time. |
| Instance cloning                 | Considered          | `limactl clone` exists, but the clone's relationship to its source has no obvious expression in Terraform's model. |
| Networks                         | Considered          | `limactl network` manages global host state, which needs careful serialisation.                                    |
| Guest command execution          | Rejected            | Effectively a provisioner that runs on every apply, which the design rules out.                                    |
| Kubernetes / Docker convenience  | Rejected            | Better served by those providers pointed at a Lima instance.                                                        |

### Update versus replacement

The table below reflects the plan modifiers in the code, not intentions. See
[`docs/resources/instance.md`](docs/resources/instance.md) for the reasoning.

| Attribute          | Update behaviour                                        |
| ------------------ | ------------------------------------------------------- |
| `name`             | Replace                                                 |
| `template`         | Replace                                                 |
| `config`           | Replace                                                 |
| `config_overrides` | Replace                                                 |
| `vm_type`          | Replace                                                 |
| `arch`             | Replace                                                 |
| `cpus`             | **In place** (stop, `limactl edit`, restart)            |
| `memory`           | **In place** (stop, `limactl edit`, restart)            |
| `disk`             | **In place** growth; shrinking is rejected at plan time |
| `start`            | In place (start / stop)                                 |
| `protect`          | In place (`limactl protect` / `unprotect`)              |
| `mounts`           | **In place** (stop, `limactl edit --set`, restart)      |
| `port_forwards`    | **In place** (stop, `limactl edit --set`, restart)      |
| `provisions`       | Replace                                                 |
| `additional_disks` | **In place** (stop, `limactl edit --set`, restart)      |

For `lima_disk`:

| Attribute | Update behaviour                                    |
| --------- | ---------------------------------------------------- |
| `name`    | Replace                                              |
| `size`    | **In place** growth; shrinking fails at plan time    |
| `format`  | Replace                                              |

`cpus`, `memory` and `disk` are applied with `limactl edit`, which needs no
editor when explicit flags are supplied. Lima refuses to edit a *running*
instance, so a running VM is stopped, reconfigured and started again — brief
downtime, but the disk and its data survive.

Everything else forces replacement because Lima offers no way to change it on
an existing instance that the provider could apply and then verify. See
[`docs/resources/instance.md`](docs/resources/instance.md#why-the-remaining-attributes-still-replace).

## Importing

The import ID is always the **real** Lima instance name, including any
`name_prefix`:

```console
$ terraform import lima_instance.example project-dev
```

With `name_prefix = "acme-"`, importing `acme-dev` sets `name = "dev"`, which
re-derives to `acme-dev`. No double-prefixing occurs. Importing a name that
does not begin with the prefix keeps it whole.

Import records everything Lima reports — `cpus`, `memory`, `disk`, `vm_type`,
`arch`, `start`, `protect` and all computed attributes — so a configuration
matching reality plans clean.

`template`, `config` and `config_overrides` are left unset, because Lima does
not record which template an instance came from. Declaring one afterwards
**adopts** the instance rather than recreating it: those attributes force
replacement only on a real change between two known values, so `null → value`
(adoption) and `value → null` (un-managing) are both non-destructive.

Declaring a template that the instance did not come from produces a warning:
the provider compares the instance's resolved disk images against the declared
template's. That refutes a wrong claim but cannot confirm a right one, since
`docker` and `ubuntu` share an image.

Adding `provisions` after import still forces replacement, since Lima
offers no way to run provisioning on an existing instance.

## Limitations

- Resizing `cpus`, `memory` or `disk` restarts a running instance, because
  Lima cannot edit a running VM. Expect downtime, not data loss.
- Disk can grow but never shrink; Lima does not support shrinking.
- Provisioning, `vm_type` and `arch` still force replacement.
- Changing mounts or port forwards restarts a running instance, because Lima
  cannot edit a running VM.
- Provisioning still forces replacement: Lima offers no way to re-run it on an
  existing instance.
- Remote Lima hosts over SSH are not supported; the provider is local-only.
- Guest IP addresses are not exposed. They are not reliable across Lima's
  networking modes, so the provider reports forwarded host endpoints instead.
- `ssh_address` and `ssh_port` on a **stopped** instance are last-known values,
  not proof of a live endpoint. Lima keeps reporting them after a stop.
- Drift in mounts, port forwards and provisioning is not detected. Lima's
  resolved configuration fills in defaults that cannot be compared reliably to
  the user's input without false positives.
- `limactl` must be present on the machine running `terraform apply`, so the
  provider cannot run in a normal remote-execution pipeline.
- Instance names are bounded by `UNIX_PATH_MAX`: `len(LIMA_HOME) + len(name) +
  27` must stay under 104. The provider checks this at plan time when `home` is
  configured.

## Contributing and development

Bug reports, questions and pull requests are welcome:

- [`CONTRIBUTING.md`](CONTRIBUTING.md) — where to report what, the scope of pull
  requests, and everything about working on the code: build and test targets,
  running a local build against a real configuration, the test strategy,
  acceptance testing, CI coverage and the architecture boundary.
- [`docs/development/lima-cli-contract.md`](docs/development/lima-cli-contract.md)
  — the recorded `limactl` behaviour the provider is built on.
- [`CODE_OF_CONDUCT.md`](CODE_OF_CONDUCT.md).

## Security considerations

- **No shell is ever invoked.** Arguments are passed as separate tokens to
  `exec.CommandContext`, so an instance name or path cannot be interpreted as
  shell syntax. Names are additionally validated against a strict pattern.
- **Temporary files are private.** Generated configuration is written with
  `os.CreateTemp`, which uses `O_EXCL` and mode `0600`, so it cannot be an
  attacker-planted symlink and is not readable by other users. Files are
  removed even when a command fails or the context is cancelled.
- **Diagnostics do not echo configuration.** YAML parse errors are stripped of
  the source excerpt the YAML library normally includes, because `config` and
  `config_overrides` may contain credentials.
- **`provision.script`, `config` and `config_overrides` are marked sensitive**,
  so Terraform redacts them in plan output.
- **No private keys in state.** `ssh_config` is a path to Lima's generated
  configuration; key material is never read or stored.
- **Environment secrets are never logged.** Values for keys matching `TOKEN`,
  `SECRET`, `PASSWORD`, `KEY`, `CREDENTIAL` or `AUTH` are redacted, and only
  environment *keys* are logged.

**Terraform state contains** host paths, the instance directory, SSH host and
port, hostname, and the configuration hash. `config` and `config_overrides` are
stored in state in full even though they are marked sensitive — marking affects
display, not storage. Use [encrypted remote
state](https://developer.hashicorp.com/terraform/language/state/sensitive-data)
if any of that is sensitive in your environment.

## Reporting security issues

See [`SECURITY.md`](SECURITY.md). Please do not open a public issue for a
suspected vulnerability.

## Roadmap

See [`ROADMAP.md`](ROADMAP.md).

## Licence

[Apache License 2.0](LICENSE). Every source file carries an
`SPDX-License-Identifier: Apache-2.0` header, and every release archive ships
[`THIRD-PARTY-NOTICES.md`](THIRD-PARTY-NOTICES.md) alongside the binary.
