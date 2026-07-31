# Contributing

Thanks for your interest. This is an independent, unofficial provider.

By taking part you agree to the [Code of Conduct](CODE_OF_CONDUCT.md).

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

## Before opening a pull request

```console
$ make check
```

That runs `fmt-check`, `vet`, `test`, `lint` and `build` — the same gates CI
enforces.

If you touched anything that talks to `limactl`, also run the acceptance
tests. They create real VMs in an isolated `LIMA_HOME` under `/tmp` and never
touch your `~/.lima`:

```console
$ make testacc
```

They also run in CI on every pull request, on Linux with the `qemu` backend.
Keep them **host-agnostic**: derive the VM type from `limactl info` with
`accVMType`, and create mount directories with `accMountDir` rather than
hardcoding a path. `vz` and `/private/tmp` are macOS-only, and an earlier
revision of the suite could not run on Linux for exactly that reason.

## The architecture boundary is not negotiable

Three layers, each with one job:

```text
internal/provider   Terraform resources and data sources
internal/lima       domain / lifecycle service, then the limactl adapter
```

- The Terraform layer must not construct command lines.
- The domain layer must not depend on Terraform framework types beyond logging.
- The adapter owns everything about running `limactl`.

And the hard rules:

- `limactl` is the only integration point. Do not write to `LIMA_HOME`, do not
  import Lima's internal Go packages, do not drive QEMU or SSH directly.
- Do not parse human-oriented output when a machine-readable form exists.
- Never invoke a shell. Arguments go to `exec.CommandContext` as separate
  tokens.

## Changing how the provider talks to Lima

`docs/development/lima-cli-contract.md` records the real, observed behaviour of
`limactl`, captured by running it. **Verify against a real Lima and update that
document in the same pull request.** Do not describe command behaviour from
memory.

If Lima's output changes, update the fixtures in
`internal/testutil/fixtures/` at the same time.

## Tests

- Add tests alongside behaviour, not afterwards. Write the test first and watch
  it fail; a test that passed the moment you wrote it has not been shown to test
  anything.
- Prefer table-driven tests.
- Unit tests must never require a VM. Use the stateful fake in
  `internal/testutil`; it plugs in at the process-execution boundary, so your
  real argument construction and output parsing are still exercised.
- For resource and data source behaviour, use the harness in
  `internal/provider/harness_test.go` rather than calling methods directly. It
  drives the real `Create`/`Read`/`Update`/`Delete`/`ImportState` against the
  fake and initialises each response the way the framework does — notably a
  **null** state for create and update, which is what makes "did the resource
  record what it did" a real assertion rather than a tautology.
- Tests must not depend on execution order and should run in parallel where
  they do not mutate process state.
- Acceptance tests must use unique, short instance names and register cleanup
  that runs even on failure. If you leave VMs behind, `make sweep` removes them.
- If you extend `internal/testutil`, keep it faithful to Lima rather than
  convenient. A fake that models a state Lima cannot be in produces tests that
  pass against behaviour that cannot happen — for example, a disk is reported
  in use only while its holder is *running*, so `AttachDisk` alone does not lock
  it and the holder has to be seeded too.

## Documentation

**`docs/` is generated. Edit `templates/`.**

```console
$ make docs          # regenerate docs/ from templates/ and the schema
$ make docs-check    # fail if docs/ is stale or was hand-edited
```

`docs-check` runs in CI on every pull request and compares `docs/` against a
fresh regeneration, so both directions fail the build: a schema description you
changed without regenerating, and a page you edited by hand.

What lives where:

| Content | Source |
| ------- | ------ |
| Attribute reference — names, types, descriptions | the provider schema, via `{{ .SchemaMarkdown }}` |
| Narrative — why an attribute replaces, what a timeout costs, how import adopts | `templates/**/*.md.tmpl` |
| Guides | `templates/guides/*.md.tmpl` |

So an attribute's description belongs in its `MarkdownDescription` in the schema,
not in a documentation page. Writing it in both is how they drift.

Two things generation cannot derive, which tests enforce instead:

- The mutability table in `README.md` and `templates/resources/instance.md.tmpl`
  must reflect the plan modifiers in the code. Change a modifier, change both
  tables in the same commit.
- The documented timeout defaults must match the constants in
  `lima.DefaultTimeouts`.

Attribute names used in `examples/` and `test/` are checked against the real
schema by `TestShippedHCLUsesOnlyRealAttributeNames`, because `terraform
validate` does **not** catch a misspelled name inside a nested attribute — it
validates cleanly on every Terraform and OpenTofu version this repository tests.
Do not rely on the CLI compatibility job to catch that class of mistake.

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
needs the public half of the key. `docs/development/releasing.md` records the
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
