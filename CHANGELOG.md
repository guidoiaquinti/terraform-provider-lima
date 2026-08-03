# Changelog

All notable changes to this project are documented here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/).

While the provider is pre-1.0 a **minor** bump may change the schema in a
breaking way. Every such change appears below under `Changed` or `Removed`, with
the steps to migrate. See
[Versioning and stability](README.md#versioning-and-stability).

## [Unreleased]

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

[Unreleased]: https://github.com/guidoiaquinti/terraform-provider-lima/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/guidoiaquinti/terraform-provider-lima/releases/tag/v0.1.0
