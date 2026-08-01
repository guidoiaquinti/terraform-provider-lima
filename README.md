# Terraform provider for Lima

[![test](https://github.com/guidoiaquinti/terraform-provider-lima/actions/workflows/test.yml/badge.svg)](https://github.com/guidoiaquinti/terraform-provider-lima/actions/workflows/test.yml)
[![acceptance](https://github.com/guidoiaquinti/terraform-provider-lima/actions/workflows/acceptance.yml/badge.svg)](https://github.com/guidoiaquinti/terraform-provider-lima/actions/workflows/acceptance.yml)
[![lint](https://github.com/guidoiaquinti/terraform-provider-lima/actions/workflows/lint.yml/badge.svg)](https://github.com/guidoiaquinti/terraform-provider-lima/actions/workflows/lint.yml)
[![security](https://github.com/guidoiaquinti/terraform-provider-lima/actions/workflows/security.yml/badge.svg)](https://github.com/guidoiaquinti/terraform-provider-lima/actions/workflows/security.yml)

This Terraform / OpenTofu provider allows managing local
[Lima](https://lima-vm.io/) virtual machines declaratively.

Note: the lifecycle is implemented and
verified against real VMs, but the provider has not been released to any
registry and its interface may still change. To use it today you build it
yourself — see [Local development
installation](#local-development-installation).

## Why

Lima is an excellent way to run Linux VMs on macOS and Linux, but its state
lives in an imperative CLI. If a team's development environment is a Lima VM
with specific mounts, port forwards and provisioning, that definition tends to
end up in a README rather than in code.

This provider lets you describe the VM in Terraform / OpenTofu, so it can be versioned,
reviewed and reproduced like any other infrastructure.

## Architecture boundary

Three layers, each with a single job:

```text
Terraform provider / resource / data source     internal/provider
                    ↓
Lima domain and lifecycle service               internal/lima  (Service)
                    ↓
limactl command adapter                         internal/lima  (ExecClient)
```

- The Terraform layer never constructs a command line.
- The domain layer decides *when* to run a command, based on observed state,
  and depends on no Terraform types beyond logging.
- The command adapter owns executable discovery, environment construction,
  argument construction, execution, cancellation, exit codes, structured
  output parsing, redaction and error formatting.

`limactl` is treated as the public integration API. The provider **never**:

- writes to `~/.lima/<instance>/lima.yaml` or any file under `LIMA_HOME`
- removes Lima directories directly
- builds disks or drives QEMU, Virtualization.framework or SSH itself
- imports Lima's internal Go packages
- parses human-oriented tables when machine-readable output exists

The exact command contract, captured by running the real CLI, is documented in
[`docs/development/lima-cli-contract.md`](docs/development/lima-cli-contract.md).

## Features

- `lima_instance` resource: create, read, update, delete and import
- `lima_disk` resource: additional disks, with in-place growth
- `lima_instance`, `lima_instances`, `lima_disk` and `lima_host` data sources
- Typed attributes for CPU, memory, disk, VM type and architecture
- Nested list attributes for mounts, port forwards and native provisioning
- Raw Lima YAML and a `config_overrides` escape hatch, merged deterministically
- Deterministic YAML generation with a stable `config_hash`
- In-place CPU, memory and disk resizing via `limactl edit`
- In-place start/stop and protection changes
- In-place mount and port-forward changes, preserving template-contributed entries
- Mount and port-forward drift detection, restored in place
- Per-instance locking so concurrent applies do not race, plus a home-wide gate
  for Lima's first-use setup, which is not per-instance and does race
- Context-aware, non-interactive command execution
- Redaction of sensitive values in logs and diagnostics

## Prerequisites

- [Lima](https://lima-vm.io/docs/installation/) 2.0 or newer, with `limactl`
  on `PATH`
- Terraform 1.0+ or OpenTofu 1.6+ — both floors are exercised in CI on every
  pull request, against a fixture that names the provider's whole schema
  surface. See [CLI compatibility](#cli-compatibility).
- Go 1.26.5+ (only to build the provider)

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

## Local development installation

The provider is not published to a registry, so it has to be supplied locally.
There are two ways, and which one you want depends on whether your
configuration has **state**.

### A development override — no state, or no modules

```console
$ make build
```

Then in `~/.terraformrc` (or `~/.tofurc` for OpenTofu):

```hcl
provider_installation {
  dev_overrides {
    "guidoiaquinti/lima" = "/absolute/path/to/terraform-provider-lima"
  }

  direct {}
}
```

The path is the **directory** containing the built binary, not the binary
itself. Terraform prints a warning on every command that overrides are in
effect; that is expected.

An override installs nothing, so `terraform init` is not needed to obtain the
provider — but it **is** still needed for anything else init does, most
commonly installing modules. A configuration calling `module "..."` fails with
*"Module not installed"* until init has run.

**And this is where the override runs out.** `terraform init` performs version
selection for every provider the *state* requires, and an overridden provider
does not take part in it. Once state contains a single `lima_instance`, init
goes looking for `guidoiaquinti/lima` in the registry, does not find it, and
fails:

```text
Error: Failed to query available provider packages

Could not retrieve the list of available versions for provider
guidoiaquinti/lima: provider registry registry.terraform.io does not have a
provider named registry.terraform.io/guidoiaquinti/lima
```

The first init succeeds and every later one fails. If your configuration has
modules *and* state — so init is unavoidable — use a mirror instead.

### A filesystem mirror — works with state

A mirror is a real installable package, so version selection succeeds and no
warning is printed:

```console
$ make build
$ V=0.0.1
$ DIR="$PWD/mirror/registry.terraform.io/guidoiaquinti/lima/$V/$(go env GOOS)_$(go env GOARCH)"
$ mkdir -p "$DIR"
$ cp terraform-provider-lima "$DIR/terraform-provider-lima_v$V"
```

```hcl
provider_installation {
  filesystem_mirror {
    path    = "/absolute/path/to/mirror"
    include = ["guidoiaquinti/lima"]
  }

  direct {
    exclude = ["guidoiaquinti/lima"]
  }
}
```

Two things to know:

- The version in the mirror path must **not** be a prerelease. Terraform will
  not select one for an unconstrained requirement, so `0.0.1-dev` is silently
  skipped and you are back to the registry error. Use `0.0.1`; it labels the
  local build and claims nothing about a release.
- `terraform init` writes a checksum of the binary into `.terraform.lock.hcl`,
  so **delete the lock file whenever you rebuild** or the next init rejects the
  new binary.

`make install` is the same idea into `~/.terraform.d/plugins/...`, at the cost
of making the version in use ambient rather than per-project.

## Supported Lima versions

**Lima 2.0 or newer is required.** The 1.x line is not supported.

| Provider version | Lima version | Behaviour                                                        |
| ---------------- | ------------ | ------------------------------------------------------------------ |
| 0.0.1-dev        | > 2.2.0      | Accepted with a warning. Report incompatibilities as issues.        |
| 0.0.1-dev        | 2.2.0        | Developed and verified against Lima 2.2.0 on macOS 15 / arm64.      |
| 0.0.1-dev        | 2.0 – 2.1    | Accepted but **not** exercised. 2.0 is the enforced minimum.        |
| 0.0.1-dev        | < 2.0        | **Rejected** at provider configuration time.                        |

The 2.x floor is a support decision rather than a known incompatibility: the
provider is neither tested nor exercised against 1.x, so accepting it would
claim support that nothing verifies. Rejecting at configure time turns what
would be a confusing mid-apply failure into a clear one up front.

A newer-than-tested Lima produces a warning only, never an error, so upgrading
Lima cannot break a working configuration.

Version policy lives in one place, `lima.CheckVersion`; version checks are not
scattered through resource code.

## CLI compatibility

Terraform and OpenTofu floors are claims, so CI checks them rather than the
README asserting them. Every pull request validates
[`test/compat/main.tf`](test/compat/main.tf) — one fixture naming every provider
attribute, every nested attribute and every computed attribute — against:

| CLI      | Versions exercised            |
| -------- | ----------------------------- |
| Terraform | 1.0.0, 1.5.7, latest         |
| OpenTofu  | 1.6.0, latest                |

The shipped examples are validated separately, on current releases of both CLIs,
because an example is written for a person and may use HCL newer than the
provider needs. One of them declares `required_version = ">= 1.2.0"` for exactly
that reason: it uses `lifecycle { precondition }`, which arrived in Terraform
1.2. The provider itself works on 1.0.

**What this does not prove.** `terraform validate` does not check attribute names
*inside* a nested attribute — renaming `port_forwards[].protocol` to `proto`
validates cleanly on every version above. That gap is covered by a unit test
which parses the shipped HCL and checks each name against the real schema.

## Supported host platforms

| Platform      | Unit tests | Acceptance tests            | Notes                                             |
| ------------- | ---------- | --------------------------- | ------------------------------------------------- |
| Linux amd64   | CI         | CI, every pull request      | `qemu` backend, with Terraform **and** OpenTofu.   |
| Linux arm64   | CI         | CI, every pull request      | `qemu` backend, on a free arm64 runner.            |
| macOS arm64   | CI         | locally, before release     | `vz` backend. No free runner can boot a VM on macOS. |
| macOS amd64   | CI         | not run                     | `vz` backend. Same reason.                         |
| Windows       | not run    | not applicable              | Lima supports WSL2; the provider is untested there.|

Unit tests need no VM and run on any platform.

**Every CI job runs on a free runner class.** `ubuntu-26.04` and
`ubuntu-26.04-arm` are both free for public repositories, as are the standard
`macos-latest` runners the unit tests use.

OpenTofu gets one acceptance job, on Linux amd64, rather than a copy of every
job: what differs between the two CLIs is the plugin protocol handshake and the
test harness's provider address, none of which is architecture- or
backend-dependent, whereas each added VM job costs runner minutes on every pull
request.

Acceptance tests **do** run on GitHub-hosted runners. The upstream Lima project
runs its own VM integration tests there, which is the evidence this is modelled
on — from a recent `lima-vm/lima` CI run, `Integration tests (QEMU, Linux host)
(alpine.yaml)` took **7.7 minutes on a standard `ubuntu-26.04` runner**. So the
Linux + QEMU job runs on every pull request, on both `ubuntu-26.04` and
`ubuntu-26.04-arm` — both are free runner classes for public repositories. The
two Linux jobs differ in more than host CPU: a different QEMU system emulator,
a different EFI firmware package, and a different guest image per template.

### Why there is no macOS acceptance job

Lima's `vz` driver is Virtualization.framework, which needs hardware
virtualisation. GitHub's Apple-silicon runners are themselves virtual machines
and cannot nest it, so `vz` needs an Intel macOS runner — and every Intel macOS
class (`macos-*-large`) is a *larger* runner, which GitHub bills even for public
repositories.

This is not a limitation of this project's CI setup. Upstream Lima runs **all**
of its macOS jobs on `macos-15-large`, including QEMU-on-macOS, for the same
reason: there is no free GitHub runner on which Lima can boot a virtual machine
on macOS.

So `vz` coverage is not automated, and this README does not pretend otherwise.
The Linux jobs are not a substitute — `vz` and `qemu` are different Lima drivers.
Run the suite locally on macOS before cutting a release:

```console
$ make testacc                      # whole suite, ~40 min on an M-series Mac
$ make testacc-run RUN=TestAccInstance_basic   # one test while iterating
```

The suite derives the backend and its mount paths from `limactl info` rather
than hardcoding either, so the same tests run unchanged on `vz` and `qemu`.

If you later decide per-pull-request `vz` coverage is worth paying for, add a job
on `macos-15-large` — [the workflow header][accept] records exactly what it
needs.

[accept]: .github/workflows/acceptance.yml

Linux and macOS are not redundant — `vz` and `qemu` are different Lima drivers.
The acceptance suite derives the backend and its mount paths from `limactl info`
rather than hardcoding either, so the same tests run on both.

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

## Development

```console
$ make help          # list targets
$ make build         # build the binary
$ make test          # unit tests, no VM required
$ make test-race     # unit tests with the race detector
$ make cover         # unit tests with a coverage summary
$ make lint          # golangci-lint
$ make docs          # regenerate docs/ from templates/ and the schema
$ make docs-check    # fail if docs/ is stale or hand-edited
$ make check         # fmt-check, vet, test, lint, build, docs-check
$ make sweep         # remove VMs left by an interrupted acceptance run
```

### Documentation is generated

`docs/` is produced by
[tfplugindocs](https://github.com/hashicorp/terraform-plugin-docs) from
`templates/` plus the provider schema. **Edit `templates/`, never `docs/`** — a
hand edit survives until the next `make docs` silently reverts it, and
`make docs-check` fails the build in the meantime.

The split is deliberate. The attribute reference comes from the schema, so a
description can no longer drift from the code; the narrative around it — why an
attribute replaces, what a timeout means, how import adopts an instance — stays
hand-written, because no generator derives reasoning from a schema.

tfplugindocs is pinned as a `tool` dependency in `go.mod`, so `make docs` runs
the same version everywhere and its checksum is in `go.sum`.

Some things generation cannot check, and tests cover instead: the mutability
table matching the plan modifiers, the documented timeout defaults matching the
constants they describe, and every shipped `.tf` file using attribute names that
actually exist — which `terraform validate` cannot verify inside a nested
attribute.

### Testing

Unit tests never create a VM. The `internal/testutil` package provides a
stateful fake `limactl` that plugs in at the process-execution boundary, so
tests exercise the provider's real argument construction, environment merging
and output parsing.

That fake is also what makes the provider layer testable without a hypervisor:
resources and data sources are driven through their real
`Create`/`Read`/`Update`/`Delete`/`ImportState` methods against it, including the
failure paths a real `limactl` will not produce on demand — a name collision, a
protected instance, a locked disk, a restart that fails after a successful edit.
The harness mirrors how the framework itself initialises each response, which
matters: for create and update the framework starts the response state *null*,
so "did the resource record what it did" is a question the tests can actually
ask.

A small set of tests named `TestReal*` runs against a real `limactl` when one
is installed, and skips otherwise. They are cheap — no VM is created — and
they verify that documents the provider generates are accepted by Lima's own
`limactl validate`.

## Acceptance testing

Acceptance tests create **real virtual machines**. They run only with
`TF_ACC=1`:

```console
$ make testacc
```

Safety properties, all enforced in code:

- Every run uses a dedicated `LIMA_HOME` created under `/tmp` and removed
  afterwards. Your `~/.lima` is never touched.
- The provider is configured with that home explicitly, so a bug in
  environment handling cannot reach the default location.
- Tests refuse to run if the resolved home is the default `~/.lima`.
- Instance names are unique per run, and every test registers cleanup that
  runs even on failure.
- The Alpine template is used throughout to keep image downloads small.

Set `LIMA_PROVIDER_ACC_HOME` to place the isolated home somewhere specific.
Keep it short: Lima's socket paths must fit inside `UNIX_PATH_MAX`.

### Recovering from an interrupted run

Every safety property above holds when a test *fails*. None of them holds when
the run is **killed**: `Ctrl-C` skips every registered cleanup, so real VMs keep
running and the temporary `LIMA_HOME` stays behind. That is the ordinary case for
anyone who changes their mind mid-suite, so it has a first-class remedy:

```console
$ make sweep                     # the LIMA_HOME the suite uses
$ make sweep SWEEP_HOME=/tmp/x   # a specific one
$ make sweep-tmp                 # every leftover acceptance home under /tmp
```

Two properties matter more than tidiness, and both are unit-tested:

- **Instances are removed before disks**, because a running instance holds a lock
  on anything attached to it, so a disk-first sweep fails on exactly the disks
  that most need removing.
- **The sweep refuses Lima's default `~/.lima`**, including when given no target
  at all — an empty home means the default. A tool that deletes every VM in a
  directory must not be able to point at the one holding your real machines.

A failure does not stop the sweep: the caller is running it precisely because
state is already inconsistent, so failures are collected and reported together
rather than abandoning everything after the first one.

CI uses the same target, which replaced an inline shell loop that extracted disk
names from JSON with a `sed` expression — one that would silently match nothing
if Lima reordered its keys.

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
`SPDX-License-Identifier: Apache-2.0` header.

Apache-2.0 rather than a copyleft licence, deliberately. This provider is an
integration layer: its value is the recorded `limactl` contract and the
behaviour built on it, not an implementation anyone would want to keep secret,
so there is little for copyleft to protect. Meanwhile a provider is something
people install inside companies, where blanket "no copyleft" policies are
enforced by scanners that match on the licence identifier rather than on how the
code is actually combined. Apache-2.0 also carries an express patent grant, which
MIT and BSD do not.

It matches the ecosystem: HashiCorp's own providers are MPL-2.0, and
[terraform-provider-libvirt](https://github.com/dmacvicar/terraform-provider-libvirt)
— the closest comparable project — is Apache-2.0.

### Third-party licences

The provider ships as a statically linked binary containing 26 modules under
Apache-2.0, MPL-2.0, BSD and MIT. All of those licences require their copyright
notices to accompany a binary redistribution, so every release archive includes
[`THIRD-PARTY-NOTICES.md`](THIRD-PARTY-NOTICES.md) alongside the binary.

That file is generated from the module graph by `make notices`, and
`make notices-check` — part of `make check` and of CI — fails the build if it no
longer matches. A dependency change that is not reflected there is a defective
release, not a cosmetic omission.

Note that the provider runs as a **separate process**, launched by Terraform and
spoken to over gRPC. It is not linked into Terraform and not linked into your
configuration, so this licence governs the provider alone.

## References

- https://github.com/lima-vm/lima/discussions/2111
- https://github.com/dmacvicar/terraform-provider-libvirt#supported-resources--xml-coverage
