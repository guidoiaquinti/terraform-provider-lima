# Contributing

Thanks for your interest. This is an independent, unofficial provider.

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

- Add tests alongside behaviour, not afterwards.
- Prefer table-driven tests.
- Unit tests must never require a VM. Use the stateful fake in
  `internal/testutil`; it plugs in at the process-execution boundary, so your
  real argument construction and output parsing are still exercised.
- Tests must not depend on execution order and should run in parallel where
  they do not mutate process state.
- Acceptance tests must use unique, short instance names and register cleanup
  that runs even on failure.

## Documentation

The mutability table in `README.md` and `docs/resources/instance.md` must
reflect the plan modifiers in the code. If you change a modifier, change both
tables in the same commit.

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

## Licence

Contributions are accepted under the [license](LICENSE).
