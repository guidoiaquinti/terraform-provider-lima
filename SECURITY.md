# Security policy

This is an independent, unofficial provider. It is not covered by any Lima or
HashiCorp security process.

## Reporting a vulnerability

Please report suspected vulnerabilities privately rather than in a public
issue: open a [GitHub security
advisory](https://github.com/guidoiaquinti/terraform-provider-lima/security/advisories/new),
or email the maintainers.

Include the provider version, the Lima version, your host platform, and a
configuration that reproduces the problem. Please redact any real credentials
from it first.

## What is in scope

This provider executes a local binary and generates configuration that Lima
consumes, so the interesting surfaces are:

- **Command execution.** Arguments are passed as separate tokens to
  `exec.CommandContext`; no shell is involved. A way to make the provider
  invoke something other than the configured `limactl`, or to inject an
  argument through an attribute value, is in scope.
- **Generated configuration.** The provider writes a YAML document to a
  temporary file for `limactl create`. A way to make that file readable by
  another user, to make the provider follow a symlink when creating it, or to
  leave it behind after an apply, is in scope.
- **Diagnostics and logs.** `config`, `config_overrides` and
  `provision.script` may hold secrets. YAML parse errors are stripped of the
  source excerpt the YAML library would otherwise include, and environment
  values whose key looks sensitive are redacted. A path that leaks any of that
  into a diagnostic, a log line or a plan is in scope.
- **State contents.** State holds host paths, the instance directory, SSH host
  and port, hostname and a configuration hash. Private key material is never
  read or stored — `ssh_config` is a path. Key material appearing in state is
  in scope.

## What is not in scope

- Vulnerabilities in Lima itself. Please report those to
  [lima-vm/lima](https://github.com/lima-vm/lima/security). If the same problem
  reproduces by running `limactl` directly, it belongs upstream.
- Vulnerabilities in Terraform or OpenTofu.
- The fact that Terraform state contains host paths and connection metadata.
  That is documented, and the remedy is
  [encrypted remote state](https://developer.hashicorp.com/terraform/language/state/sensitive-data).
- Anything requiring the attacker to already control the machine running
  `terraform apply`. The provider runs local commands as the invoking user by
  design.

## Supported versions

The provider is pre-release (`0.1.0-dev`) and has never been published, so
there are no supported older versions. Fixes land on `main`.
