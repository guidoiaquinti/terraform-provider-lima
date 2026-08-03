# Contributing

Thanks for your interest. This is an independent, unofficial provider.

By taking part you agree to the [Code of Conduct](CODE_OF_CONDUCT.md).

Using the provider needs none of this — see the [README](README.md) for that.

**Getting involved**

- [Getting help, versus reporting a bug](#getting-help-versus-reporting-a-bug)
- [Scope of pull requests](#scope-of-pull-requests)
- [Use of AI](#use-of-ai)
- [Commits](#commits)
- [Licence](#licence)

**Working on the code**

- [Getting set up](#getting-set-up)
- [Make targets](#make-targets)
- [Before opening a pull request](#before-opening-a-pull-request)
- [The architecture boundary is not negotiable](#the-architecture-boundary-is-not-negotiable)
- [Changing how the provider talks to Lima](#changing-how-the-provider-talks-to-lima)
- [Tests](#tests)
- [Acceptance testing](#acceptance-testing)
- [Continuous integration](#continuous-integration)
- [Documentation](#documentation)
- [Running your own build against a real configuration](#running-your-own-build-against-a-real-configuration)
- [Adding an in-place update](#adding-an-in-place-update)
- [Releasing](#releasing)

## Getting help, versus reporting a bug

These are different things and they go to different places.

| You have | Use |
| -------- | --- |
| A question, or a configuration that does not work the way you expected | [Discussions → Q&A](https://github.com/guidoiaquinti/terraform-provider-lima/discussions/categories/q-a) |
| A reproducible defect in the provider | [Bug report](https://github.com/guidoiaquinti/terraform-provider-lima/issues/new?template=bug_report.yml) |
| A capability the provider does not have | [Feature request](https://github.com/guidoiaquinti/terraform-provider-lima/issues/new?template=feature_request.yml) — check [`ROADMAP.md`](ROADMAP.md) first, which explains what was deliberately left out and why |
| A suspected vulnerability | [`SECURITY.md`](SECURITY.md) — **not** a public issue |
| A problem that also happens when you run `limactl` directly | [lima-vm/lima](https://github.com/lima-vm/lima/issues) — this provider is an unaffiliated wrapper |

Issues are for things a maintainer can act on. "My stack does not come up" with
no isolation is a discussion, and will be moved to one.

Before filing a bug, please confirm it is the provider and not Lima: run the
equivalent `limactl` command by hand. That single step resolves most reports,
and the answer belongs in the issue either way.

## Scope of pull requests

Please **open an issue or a discussion before writing a significant feature.**
This is not a formality. A merged feature transfers its maintenance to the
maintainers indefinitely, and this provider has a deliberately narrow contract
with `limactl` (see below) that a well-meaning patch can quietly break. Several
capabilities that look like obvious additions are absent on purpose, with the
reasoning recorded in [`ROADMAP.md`](ROADMAP.md) — a pull request implementing
one of those needs the reasoning addressed, not just the code.

Always welcome without prior discussion:

- Bug fixes with a test that fails before the fix
- Documentation corrections
- Additional test coverage
- Updates to `docs/development/lima-cli-contract.md` verified against a real
  `limactl`

## Use of AI

This project is developed with AI assistance, and that is not treated as
incidental: the architecture boundary, the recorded CLI contract and the
unusually explicit test rationale all exist partly to make AI-assisted changes
reviewable. The maintainer owns the outcome regardless of how a change was
produced.

**If you used an AI tool to produce a contribution — code, tests,
documentation, or an issue report — say so in the pull request or issue.**
Name the tool. You do not need to paste your prompts, but do flag anything you
have not personally verified, and in particular:

- Any claim about `limactl` behaviour that you did not confirm by running it.
  Generated descriptions of CLI behaviour are frequently plausible and wrong,
  and this repository's correctness rests on that contract.
- Any test that you did not watch fail before the change that makes it pass.

Undisclosed AI-generated content that turns out to be unverified is the one
thing likely to get a pull request closed rather than reviewed.

## Getting set up

```console
$ git clone https://github.com/guidoiaquinti/terraform-provider-lima
$ cd terraform-provider-lima
$ make build
$ make test
```

Unit tests need no Lima installation and create no VMs.

What you need:

- Go 1.26.5+
- [Lima](https://lima-vm.io/docs/installation/) 2.0 or newer with `limactl` on
  `PATH` — only for the acceptance tests and the `TestReal*` unit tests
- Terraform 1.0+ or OpenTofu 1.6+ to exercise a build against a real
  configuration

## Make targets

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

## Before opening a pull request

```console
$ make check
```

That runs `fmt-check`, `vet`, `test`, `lint` and `build` — the same gates CI
enforces.

If you touched anything that talks to `limactl`, also run the [acceptance
tests](#acceptance-testing). They create real VMs in an isolated `LIMA_HOME`
under `/tmp` and never touch your `~/.lima`:

```console
$ make testacc
```

They also run in CI on every pull request, on Linux with the `qemu` backend.
Keep them **host-agnostic**: derive the VM type from `limactl info` with
`accVMType`, and create mount directories with `accMountDir` rather than
hardcoding a path. `vz` and `/private/tmp` are macOS-only, and an earlier
revision of the suite could not run on Linux for exactly that reason.

## The architecture boundary is not negotiable

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
  and depends on no Terraform framework types beyond logging.
- The command adapter owns executable discovery, environment construction,
  argument construction, execution, cancellation, exit codes, structured
  output parsing, redaction and error formatting.

`limactl` is treated as the public integration API. The provider **never**:

- writes to `~/.lima/<instance>/lima.yaml` or any file under `LIMA_HOME`
- removes Lima directories directly
- builds disks or drives QEMU, Virtualization.framework or SSH itself
- imports Lima's internal Go packages
- parses human-oriented tables when machine-readable output exists
- invokes a shell — arguments go to `exec.CommandContext` as separate tokens

## Changing how the provider talks to Lima

[`docs/development/lima-cli-contract.md`](docs/development/lima-cli-contract.md)
records the real, observed behaviour of `limactl`, captured by running it.
**Verify against a real Lima and update that document in the same pull
request.** Do not describe command behaviour from memory.

If Lima's output changes, update the fixtures in
`internal/testutil/fixtures/` at the same time.

## Tests

Unit tests never create a VM. The `internal/testutil` package provides a
stateful fake `limactl` that plugs in at the process-execution boundary, so
tests exercise the provider's real argument construction, environment merging
and output parsing.

That fake is also what makes the provider layer testable without a hypervisor:
resources and data sources are driven through their real
`Create`/`Read`/`Update`/`Delete`/`ImportState` methods against it, including the
failure paths a real `limactl` will not produce on demand — a name collision, a
protected instance, a locked disk, a restart that fails after a successful edit.

A small set of tests named `TestReal*` runs against a real `limactl` when one
is installed, and skips otherwise. They are cheap — no VM is created — and
they verify that documents the provider generates are accepted by Lima's own
`limactl validate`.

What is expected of a new test:

- Add tests alongside behaviour, not afterwards. Write the test first and watch
  it fail; a test that passed the moment you wrote it has not been shown to test
  anything.
- Prefer table-driven tests.
- Unit tests must never require a VM. Use the fake described above.
- For resource and data source behaviour, use the harness in
  `internal/provider/harness_test.go` rather than calling methods directly. It
  drives the real lifecycle methods against the fake and initialises each
  response the way the framework does — notably a **null** state for create and
  update, which is what makes "did the resource record what it did" a real
  assertion rather than a tautology.
- Tests must not depend on execution order and should run in parallel where
  they do not mutate process state.
- Acceptance tests must use unique, short instance names and register cleanup
  that runs even on failure. If you leave VMs behind, `make sweep` removes them.
- If you extend `internal/testutil`, keep it faithful to Lima rather than
  convenient. A fake that models a state Lima cannot be in produces tests that
  pass against behaviour that cannot happen — for example, a disk is reported
  in use only while its holder is *running*, so `AttachDisk` alone does not lock
  it and the holder has to be seeded too.

## Acceptance testing

Acceptance tests create **real virtual machines**. They run only with
`TF_ACC=1`:

```console
$ make testacc                                 # whole suite, ~40 min on an M-series Mac
$ make testacc-run RUN=TestAccInstance_basic   # one test while iterating
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

The suite derives the backend and its mount paths from `limactl info` rather
than hardcoding either, so the same tests run unchanged on `vz` and `qemu`.

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

## Continuous integration

### Host platform coverage

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
`ubuntu-26.04-arm`. The two Linux jobs differ in more than host CPU: a different
QEMU system emulator, a different EFI firmware package, and a different guest
image per template.

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

So `vz` coverage is not automated, and the documentation does not pretend
otherwise. The Linux jobs are not a substitute — `vz` and `qemu` are different
Lima drivers. Run the suite locally on macOS before cutting a release.

If you later decide per-pull-request `vz` coverage is worth paying for, add a job
on `macos-15-large` — [the workflow header][accept] records exactly what it
needs.

[accept]: .github/workflows/acceptance.yml

### CLI compatibility

Terraform and OpenTofu floors are claims, so CI checks them rather than the
documentation asserting them. Every pull request validates
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
which parses the shipped HCL and checks each name against the real schema. Do
not rely on the CLI compatibility job to catch that class of mistake.

## Documentation

**`docs/` is generated. Edit `templates/`.**

```console
$ make docs          # regenerate docs/ from templates/ and the schema
$ make docs-check    # fail if docs/ is stale or was hand-edited
```

`docs-check` runs in CI on every pull request and compares `docs/` against a
fresh regeneration, so both directions fail the build: a schema description you
changed without regenerating, and a page you edited by hand. A hand edit
otherwise survives only until the next `make docs` silently reverts it.

tfplugindocs is pinned as a `tool` dependency in `go.mod`, so `make docs` runs
the same version everywhere and its checksum is in `go.sum`.

What lives where:

| Content | Source |
| ------- | ------ |
| Attribute reference — names, types, descriptions | the provider schema, via `{{ .SchemaMarkdown }}` |
| Narrative — why an attribute replaces, what a timeout costs, how import adopts | `templates/**/*.md.tmpl` |
| Guides | `templates/guides/*.md.tmpl` |

The split is deliberate: a description generated from the schema can no longer
drift from the code, while the reasoning around it stays hand-written, because
no generator derives reasoning from a schema. So an attribute's description
belongs in its `MarkdownDescription`, not in a documentation page. Writing it in
both is how they drift.

Two things generation cannot derive, which tests enforce instead:

- The mutability table in `README.md` and `templates/resources/instance.md.tmpl`
  must reflect the plan modifiers in the code. Change a modifier, change both
  tables in the same commit.
- The documented timeout defaults must match the constants in
  `lima.DefaultTimeouts`.

Attribute names used in `examples/` and `test/` are checked against the real
schema by `TestShippedHCLUsesOnlyRealAttributeNames`, for the reason given under
[CLI compatibility](#cli-compatibility).

## Running your own build against a real configuration

There are two ways, and which one you want depends on how sure you need to be
that the binary in use is yours.

### A development override — fastest iteration

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

**Where the override stops being enough.** An overridden provider takes no part
in version selection, so `terraform init` resolves `guidoiaquinti/lima` from the
registry anyway and writes the *released* version into `.terraform.lock.hcl`.
That download is not the binary your `plan` and `apply` then run — the override
still wins for those — but nothing in the init output says so. The only signal
that your build is the one executing is the override warning Terraform prints on
each subsequent command, so read it rather than assuming.

Use a mirror instead when you need certainty rather than a warning: when the run
must not reach the registry at all, or when you are verifying a release artifact
and a silent fallback to a different binary would invalidate the result.

### A filesystem mirror — no registry involved

A mirror is a real installable package, so version selection resolves to *your*
binary and no override warning is printed:

```console
$ make build
$ V=0.1.0
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
  not select one for an unconstrained requirement, so `0.1.0-dev` is silently
  skipped and init falls back to the registry — the exact substitution the
  mirror exists to prevent. Use a plain `0.1.0`.
- `terraform init` writes a checksum of the binary into `.terraform.lock.hcl`,
  so **delete the lock file whenever you rebuild** or the next init rejects the
  new binary.

`make install` is the same idea into `~/.terraform.d/plugins/...`, at the cost
of making the version in use ambient rather than per-project.

## Adding an in-place update

The bar is deliberately high, because a half-applied edit corrupts a VM. A new
in-place path needs all of:

1. a supported, scriptable `limactl` command or documented workflow
2. no interactive editor
3. safe stop/restart handling that records prior state
4. acceptance tests showing no state corruption, including when a restart
   fails after a successful edit
5. a refresh that accurately reflects the result

If any of those is missing, use `RequiresReplace` and document why.

## Commits

Small and coherent. Conventional Commit prefixes (`feat:`, `fix:`, `docs:`,
`test:`, `ci:`, `chore:`) are used to group release notes.

## Releasing

Pushing a `v*` tag builds and signs the release, but several prerequisites are
outside the repository and fail in ways the error message does not explain — the
repository has to be public, two signing secrets have to exist, and the registry
needs the public half of the key.
[`docs/development/releasing.md`](docs/development/releasing.md) records the
whole sequence, including why a draft release looks to the registry exactly like
no release at all.

## Licence

The project is [Apache-2.0](LICENSE). By contributing you agree that your
contribution is licensed under it — there is no separate CLA.

Two practical points:

- **New Go files need the header.** Two lines at the top, then a blank line, then
  the package doc comment if there is one:

  ```go
  // Copyright 2026 Guido Iaquinti
  // SPDX-License-Identifier: Apache-2.0

  package lima
  ```

  The blank line matters: without it the copyright block becomes the package's
  documentation.

- **New dependencies change the notices.** `THIRD-PARTY-NOTICES.md` reproduces
  the licence of every module linked into the shipped binary, which Apache-2.0
  §4, BSD and MIT all require of a binary redistribution. Run `make notices` and
  commit the result; `make notices-check` fails CI otherwise.

  Please avoid adding a dependency under a copyleft licence. It would not be
  merely a licence-compatibility question — it would change what users of the
  compiled provider are obliged to do.
