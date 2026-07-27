---
page_title: "lima_host Data Source - terraform-provider-lima"
description: |-
  Reports the capabilities of the local Lima installation.
---

# lima_host (Data Source)

Reports the capabilities of the local Lima installation, as detected by
`limactl info`.

Only values Lima actually reports are exposed. Nothing is inferred from the
host platform, and capabilities that cannot be determined reliably are omitted
rather than guessed.

## Example usage

```hcl
data "lima_host" "current" {}

output "lima_version" {
  value = data.lima_host.current.lima_version
}

output "available_backends" {
  value = data.lima_host.current.vm_types
}
```

Choosing a backend based on what the host actually supports:

```hcl
data "lima_host" "current" {}

resource "lima_instance" "dev" {
  name     = "dev"
  template = "template:ubuntu"

  # Prefer the native macOS backend when Lima reports it.
  vm_type = contains(data.lima_host.current.vm_types, "vz") ? "vz" : "qemu"
}
```

Guarding against a missing template:

```hcl
data "lima_host" "current" {}

resource "lima_instance" "dev" {
  name     = "dev"
  template = "template:ubuntu"

  lifecycle {
    precondition {
      condition     = contains(data.lima_host.current.templates, "ubuntu")
      error_message = "The ubuntu template is not available in this Lima installation."
    }
  }
}
```

## Schema

This data source takes no arguments.

### Read-only

- `id` (String) The resolved `limactl` path.
- `lima_version` (String) Lima version reported by `limactl info`.
- `host_os` (String) Host operating system, for example `darwin` or `linux`.
- `host_arch` (String) Host architecture, for example `aarch64` or `x86_64`.
- `vm_types` (List of String) VM backends this Lima build supports on this
  host, for example `["qemu", "vz", "krunkit"]`. Detected, not hardcoded.
- `lima_home` (String) The `LIMA_HOME` Lima is using, reflecting the provider's
  `home` setting when one is configured.
- `binary_path` (String) Absolute path to the resolved `limactl` executable.
- `templates` (List of String) Template names usable as
  `template = "template:<name>"`. Lima's internal composition fragments, whose
  names begin with `_`, are excluded.
- `instance_names` (List of String) Every instance currently present in
  `LIMA_HOME`, whether or not Terraform manages it.

## Notes on omitted attributes

The following were considered and deliberately **not** exposed, because Lima
provides no reliable way to detect them:

- **Snapshot support.** `limactl snapshot` exists, but whether it works depends
  on the VM backend and disk format of a specific instance, not on the host.
- **Disk support.** Same reasoning.
- **Default instance name.** Lima has no host-level notion of one; `default` is
  simply the name `limactl start` uses when given no argument.

Guessing these would produce a value that is wrong in some configurations,
which is worse than not offering it.
