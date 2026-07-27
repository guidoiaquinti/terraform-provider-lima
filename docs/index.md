---
page_title: "Provider: Lima"
description: |-
  Manages local Lima virtual machines through the supported limactl CLI.
---

# Lima Provider

Manages local [Lima](https://lima-vm.io/) virtual machines declaratively.

> This provider orchestrates Lima through the supported `limactl` lifecycle.
> It does not replace Lima or manage Lima's internal files directly.

> **Unofficial.** Not affiliated with, endorsed by, or supported by the Lima
> project or HashiCorp.

## Example usage

```hcl
terraform {
  required_providers {
    lima = {
      source = "guidoiaquinti/lima"
    }
  }
}

provider "lima" {
  # Every attribute is optional; this block can be empty.
}

resource "lima_instance" "dev" {
  name     = "project-dev"
  template = "template:ubuntu"

  cpus   = 4
  memory = "8GiB"
  disk   = "50GiB"
}
```

### Isolated Lima home

```hcl
provider "lima" {
  home        = "~/.lima-terraform"
  name_prefix = "tf-"

  default_timeout = "30m"
}
```

## Resources and data sources

| Type | Name | Purpose |
| ---- | ---- | ------- |
| Resource | [`lima_instance`](resources/instance.md) | A Lima virtual machine |
| Resource | [`lima_disk`](resources/disk.md) | An additional Lima disk |
| Data source | [`lima_instance`](data-sources/instance.md) | Read an existing instance |
| Data source | [`lima_disk`](data-sources/disk.md) | Read an existing disk |
| Data source | [`lima_host`](data-sources/host.md) | Detected host and Lima capabilities |

## Requirements

`limactl` must be installed and on `PATH` on the machine that runs
`terraform apply`. The provider verifies this during configuration and runs a
version check, so a missing or unusable Lima produces an actionable error
before any resource is touched.

Because the provider shells out to a local binary, it cannot run in a remote
execution environment that lacks Lima.

## Schema

All attributes are optional.

### `binary`

- **Type:** String
- **Environment variable:** `LIMA_PROVIDER_BINARY`
- **Default:** `limactl` resolved from `PATH`

Path to the `limactl` executable. A leading `~` is expanded. During
configuration the provider checks that the path exists, is not a directory and
is executable, then runs `limactl --version`.

### `home`

- **Type:** String
- **Environment variable:** `LIMA_HOME`
- **Default:** Lima's own default, normally `~/.lima`

Passed to every `limactl` invocation as `LIMA_HOME`. A leading `~` is expanded
and the path is normalised.

The provider never creates or removes this directory itself; only Lima does.

Keep it short. Lima builds unix socket paths as
`<LIMA_HOME>/<name>/ssh.sock.<16 digits>` and enforces `UNIX_PATH_MAX = 104`.
The provider validates this at plan time when `home` is set, so an over-long
combination fails with a clear message instead of failing mid-apply.

### `environment`

- **Type:** Map of String
- **Default:** empty

Extra environment variables for every `limactl` invocation. Merged over the
inherited process environment; provider-defined values win on conflict,
including over `home`.

Values are never written to logs or diagnostics. Values whose key contains
`TOKEN`, `SECRET`, `PASSWORD`, `KEY`, `CREDENTIAL` or `AUTH` are additionally
redacted anywhere a key/value pair might be rendered.

### `default_timeout`

- **Type:** String (Go duration)
- **Environment variable:** `LIMA_PROVIDER_DEFAULT_TIMEOUT`
- **Default:** `20m`

Fallback timeout for operations that do not set their own. Validated with Go
duration parsing and must be greater than zero.

Resource-level `timeouts` blocks take precedence. See the `lima_instance`
resource for its per-operation defaults.

### `name_prefix`

- **Type:** String
- **Environment variable:** `LIMA_PROVIDER_NAME_PREFIX`
- **Default:** empty

Prefix applied to every managed instance's `name` to form the real Lima
instance name, which is exposed as `instance_name`.

Useful for keeping provider-managed VMs distinguishable from hand-made ones.

**Import IDs are always the real Lima name, prefix included.** With
`name_prefix = "acme-"`, `terraform import lima_instance.dev acme-dev` sets
`name = "dev"`, which re-derives to `acme-dev`. Importing a name that does not
begin with the prefix keeps it whole, so no double-prefixing occurs.

The `lima_instance` **data source** is not affected by `name_prefix`: it takes
the real Lima name, so it can read instances Terraform does not manage.

## Environment variables

| Variable                        | Equivalent attribute | Notes                            |
| ------------------------------- | -------------------- | -------------------------------- |
| `LIMA_PROVIDER_BINARY`          | `binary`             |                                  |
| `LIMA_HOME`                     | `home`               | Lima's own variable              |
| `LIMA_PROVIDER_NAME_PREFIX`     | `name_prefix`        |                                  |
| `LIMA_PROVIDER_DEFAULT_TIMEOUT` | `default_timeout`    |                                  |
| `TF_ACC`                        | —                    | Enables acceptance tests         |
| `LIMA_PROVIDER_ACC_HOME`        | —                    | Isolated home for acceptance tests |

Explicit configuration always takes precedence over an environment variable.

## Supported Lima versions

**Lima 2.0 or newer is required.** The 1.x line is not supported.

| Provider version | Lima version | Behaviour                                                 |
| ---------------- | ------------ | --------------------------------------------------------- |
| 0.1.0-dev        | 2.2.0        | Developed and verified against Lima 2.2.0, macOS 15 arm64. |
| 0.1.0-dev        | 2.0 – 2.1    | Accepted, not exercised. 2.0 is the enforced minimum.      |
| 0.1.0-dev        | > 2.2.0      | Accepted with a warning.                                   |
| 0.1.0-dev        | < 2.0        | **Rejected** at provider configuration time.               |

An older-than-minimum Lima is an **error**. A newer-than-tested Lima is a
**warning** only, so upgrading Lima cannot break a working configuration.

## Logging

The provider uses Terraform's plugin logging. Enable it with:

```console
$ TF_LOG=DEBUG terraform apply
```

Debug logs include the command name and redacted arguments, state transitions,
polling summaries, version detection and sanitised Lima stderr on failure.

They never include private keys, provisioning script bodies, sensitive YAML
values or environment secrets.
