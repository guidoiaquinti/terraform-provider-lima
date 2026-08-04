# Changelog

All notable changes to this project are documented here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/).

While the provider is pre-1.0 a **minor** bump may change the schema in a
breaking way. Every such change appears below under `Changed` or `Removed`, with
the steps to migrate. See
[Versioning and stability](README.md#versioning-and-stability).

## [Unreleased]

## [0.1.2] - 2026-08-04

### Fixed

- Removing a key in `config_overrides` now removes it from the configuration
  Lima resolves, including values a `template` contributed. Previously the key
  was dropped from the generated document, which Lima could not distinguish from
  a key that was never set, so the template's value won — most visibly leaving
  `mounts: null` unable to remove the home directory mount that stock templates
  bring in via `template:_default/mounts`. Removals are now written out as an
  explicit `null`, which Lima honours across its `base:` merge. An empty
  sequence in `config_overrides` is treated as the same removal, so
  `mounts: []` and `mounts: null` agree. ([#12])

  This changes `config_hash` for configurations that remove a key or set one to
  an empty sequence in `config_overrides`. The hash covers the generated
  document, and that document is now different — and correct. No instance is
  replaced by the change alone, but the first plan after upgrading shows the new
  hash.

### Changed

- Documented that typed `mounts` and `port_forwards` are **added to** what the
  base template contributes and cannot remove them, so `mounts = []` shares
  nothing extra rather than nothing at all. The previous wording ("Mounts the
  base template contributes are preserved") read as a note about ordering. The
  new text points at `config_overrides` with `mounts: null` as the way to end up
  with no mounts. ([#12])

[#12]: https://github.com/guidoiaquinti/terraform-provider-lima/issues/12

## [0.1.1] - 2026-08-03

### Fixed

- Include the Terraform Registry manifest in the signed SHA256 checksum file as
  well as in the release assets. The Registry rejected `v0.1.0` because the
  manifest was uploaded without a corresponding checksum, so that version was
  never installable from the Registry; use `v0.1.1` instead.
- Generate GitHub release notes from merged pull requests rather than raw
  commits, avoiding duplicate branch/merge entries and commit-author email
  addresses.

## [0.1.0] - 2026-08-02

First release.

### Added

- `lima_instance` resource — the Lima VM lifecycle. Builds on a `template` or a
  complete `config`, with typed `cpus`, `memory`, `disk`, `vm_type` and `arch`,
  a `config_overrides` escape hatch for Lima options without a typed attribute,
  `mounts`, `port_forwards`, native `provisions`, `additional_disks`, `start` to
  hold an instance stopped, and `protect` for Lima's deletion protection.
  Existing instances can be adopted with `terraform import`.
- `lima_disk` resource — standalone Lima data disks, including growing an
  existing disk in place.
- `lima_instance`, `lima_instances` and `lima_disk` data sources — read one
  instance, enumerate all of them, or read one disk.
- `lima_host` data source — Lima version, host OS and architecture, available VM
  types, `LIMA_HOME`, the `limactl` path, templates and instance names.
- Provider configuration for the `limactl` binary path, `LIMA_HOME`, an instance
  name prefix, a default operation timeout and extra environment for every
  `limactl` call. Each is also settable by environment variable, with explicit
  configuration taking precedence.
- In-place updates where Lima allows them. `cpus`, `memory`, a growing `disk`,
  `mounts` and `port_forwards` reconfigure via `limactl edit` rather than
  replacing the VM; because Lima cannot edit a running instance, a running VM is
  stopped and restarted, which means brief downtime.
- Plan-time rejection of changes Lima cannot perform, notably shrinking a disk,
  so they fail before an apply starts rather than part-way through.
- A Lima version floor enforced at provider configuration time: Lima 2.0 or
  newer is required, and a newer-than-tested Lima warns rather than errors, so
  upgrading Lima cannot break a working configuration.

### Known limitations

- Attributes that force replacement rather than updating in place — `template`,
  `config`, `config_overrides`, `vm_type`, `arch`, `provisions` and `name`. The
  [`lima_instance` documentation](docs/resources/instance.md) records each one
  and why.
- macOS `vz` is verified by hand before each release rather than in CI: no free
  GitHub runner can boot a VM on macOS.
- Windows binaries are published because Lima supports WSL2, but the provider is
  untested there.

[Unreleased]: https://github.com/guidoiaquinti/terraform-provider-lima/compare/v0.1.2...HEAD
[0.1.2]: https://github.com/guidoiaquinti/terraform-provider-lima/compare/v0.1.1...v0.1.2
[0.1.1]: https://github.com/guidoiaquinti/terraform-provider-lima/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/guidoiaquinti/terraform-provider-lima/releases/tag/v0.1.0
