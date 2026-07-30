---
page_title: "lima_instances Data Source - terraform-provider-lima"
subcategory: ""
description: |-
  Reads every Lima instance in LIMA_HOME.
---

# lima_instances (Data Source)

Reads **every** Lima instance in `LIMA_HOME`, whether or not Terraform manages
it. This data source never modifies anything.

`lima_host` also reports instance names — but only the names. This reports the
full state of each instance, using the same attributes as the `lima_instance`
data source.

## Example usage

```hcl
data "lima_instances" "all" {}

# Filter with an ordinary Terraform expression.
output "running" {
  value = [for i in data.lima_instances.all.instances : i.name if i.status == "running"]
}

output "total_memory_bytes" {
  value = sum([for i in data.lima_instances.all.instances : parseint(i.memory, 10)])
}
```

There are deliberately **no filter arguments**. A Terraform expression filters a
list better than a bespoke argument could, and every filter attribute would be
another thing to keep consistent with `lima_instance`.

## Schema

### Read-only

- `instances` (List of Object) Every instance Lima reports, in the order
  `limactl list` returns them. Empty rather than null when there are none, so it
  can always be iterated.

Each entry carries the same attributes as the
[`lima_instance` data source](instance.md):

- `name` (String) The instance name, exactly as `limactl list` reports it.
- `status` (String) Normalised status: `running`, `stopped`, `creating`,
  `broken` or `unknown`. A status Lima introduces that this provider version does
  not recognise maps to `unknown`, with the original preserved in `raw_status`.
- `raw_status` (String) The status exactly as Lima reported it, e.g. `Running`.
- `arch` (String) Machine architecture, for example `aarch64`.
- `vm_type` (String) VM backend, for example `vz` or `qemu`.
- `cpus` (Number) Number of virtual CPUs.
- `memory` (String) Memory size in IEC notation, for example `4GiB`.
- `disk` (String) Primary disk size in IEC notation.
- `ssh_address` (String) Host address for SSH. For a stopped instance this is the
  last known value, not a live endpoint.
- `ssh_port` (Number) Host port forwarded to guest SSH. Last known when stopped.
- `ssh_user` (String) Guest login name.
- `ssh_config` (String) Path to Lima's generated SSH configuration file. No key
  material is exposed.
- `hostname` (String) Guest hostname, normally `lima-<name>`.
- `dir` (String) The instance directory inside `LIMA_HOME`.
- `protected` (String) Whether Lima's deletion protection is enabled.
- `lima_version` (String) The Lima version recorded against the instance,
  falling back to the detected host version.

### Optional

- `timeouts` (Attribute) See [Timeouts](#timeouts).

## Timeouts

- `read` — default `2m`

Setting the provider's `default_timeout` overrides it.

```hcl
data "lima_instances" "all" {
  timeouts = {
    read = "30s"
  }
}
```

## Names and `name_prefix`

Names here are the **real** Lima instance names, exactly as `limactl list`
reports them. The provider's `name_prefix` is not applied or stripped, because
this data source reports instances it does not manage alongside those it does.

To find only the instances a prefix covers, filter on it:

```hcl
output "ours" {
  value = [
    for i in data.lima_instances.all.instances : i.name
    if startswith(i.name, "acme-")
  ]
}
```
