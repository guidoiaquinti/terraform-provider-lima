---
page_title: "lima_instance Data Source - terraform-provider-lima"
description: |-
  Reads an existing Lima instance.
---

# lima_instance (Data Source)

Reads an existing Lima instance. This data source never modifies the instance.

`name` is the **real** Lima instance name and is **not** affected by the
provider's `name_prefix`, so it can read instances Terraform does not manage.

## Example usage

```hcl
data "lima_instance" "default" {
  name = "default"
}

output "default_status" {
  value = data.lima_instance.default.status
}

output "ssh_command" {
  value = "ssh -F ${data.lima_instance.default.ssh_config} ${data.lima_instance.default.hostname}"
}
```

Reading an instance managed elsewhere in the same configuration:

```hcl
data "lima_instance" "managed" {
  name = lima_instance.dev.instance_name
}
```

## Schema

### Required

- `name` (String) The Lima instance name, exactly as `limactl list` reports it.

### Read-only

There is deliberately **no `id`**. Lima exposes no object identifier of its own —
`limactl list --list-fields` reports none, and the only UUID on disk belongs to the
`vz` backend and to no `limactl` command — so an `id` could only repeat the name,
which is Lima's actual primary key.

- `status` (String) Normalised status: `running`, `stopped`, `creating`,
  `broken` or `unknown`. A status Lima introduces that this provider version does
  not recognise maps to `unknown`, with the original preserved in `raw_status`.
- `raw_status` (String) The status exactly as Lima reported it.
- `arch` (String) Machine architecture, for example `aarch64`.
- `vm_type` (String) VM backend, for example `vz` or `qemu`.
- `cpus` (Number) Virtual CPU count.
- `memory` (String) Memory size in IEC notation, for example `4GiB`.
- `disk` (String) Primary disk size in IEC notation.
- `ssh_address` (String) Host address for SSH.
- `ssh_port` (Number) Host port forwarded to guest SSH.
- `ssh_user` (String) Guest login name.
- `ssh_config` (String) Path to Lima's generated SSH configuration file.
- `hostname` (String) Guest hostname, normally `lima-<name>`.
- `dir` (String) Instance directory inside `LIMA_HOME`.
- `protected` (Boolean) Whether deletion protection is enabled.
- `lima_version` (String) Lima version recorded against the instance, falling
  back to the detected host version.

## Notes

**On a stopped instance, `ssh_address` and `ssh_port` are last-known values,
not a live endpoint.** Lima continues to report them after a stop.

If the instance does not exist, the data source fails with an error naming the
`LIMA_HOME` that was searched and suggesting `limactl list`. Use the
`lima_host` data source's `instance_names` attribute if you need to check for
existence without failing.
