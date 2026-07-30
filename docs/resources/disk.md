---
page_title: "lima_disk Resource - terraform-provider-lima"
subcategory: ""
description: |-
  Manages an additional Lima disk through limactl disk.
---

# lima_disk (Resource)

Manages an additional Lima disk.

Disks exist independently of instances. Attach one with the `additional_disks`
attribute of [`lima_instance`](instance.md); Lima mounts it in the guest at the
disk's `mount_point`, normally `/mnt/lima-<name>`.

## Example usage

```hcl
resource "lima_disk" "data" {
  name = "project-data"
  size = "50GiB"
}

resource "lima_instance" "dev" {
  name     = "project-dev"
  template = "template:ubuntu"

  additional_disks = [lima_disk.data.name]
}

output "guest_path" {
  value = lima_disk.data.mount_point
}
```

## Schema

### Required

- `name` (String) Disk name, which is also the import ID.

  The provider's `name_prefix` is **not** applied. Disks are referenced by their
  real name in an instance's `additional_disks`, and prefixing would make that
  reference surprising.

- `size` (String) Disk size, for example `50GiB`. Growing is applied **in
  place**; shrinking is rejected at plan time.

### Optional

- `format` (String) Format requested at creation, `qcow2` or `raw`. Changing it
  forces a new disk.

  Lima may store a **different** format than requested: the `vz` driver
  requires raw images, so a `qcow2` request is converted. This attribute
  records what you asked for and is never overwritten from Lima; see
  `actual_format` for what Lima reports.

- `timeouts` (Attribute) See [Timeouts](#timeouts).

### Read-only

- `id` (String) The disk name.
- `actual_format` (String) The format Lima reports for the stored image.
- `dir` (String) The disk's directory inside `LIMA_HOME`, normally
  `_disks/<name>`.
- `mount_point` (String) Guest path the disk is mounted at when attached.
- `in_use_by` (String) Name of the **running** instance currently holding this
  disk, or null.

## Timeouts

- `create` — default `30m`
- `update` — default `20m`
- `delete` — default `20m`
- `read` — default `2m`

Setting the provider's `default_timeout` replaces **all four**. Leave it unset to
keep the per-operation defaults above.

`timeouts` is an **attribute**, not a block, so it takes an equals sign:

```hcl
resource "lima_disk" "data" {
  # ...
  timeouts = {
    create = "10m"
  }
}
```

## Update versus replacement

| Attribute | Update behaviour                                       |
| --------- | ------------------------------------------------------ |
| `name`    | Replace                                                |
| `size`    | **In place** growth; shrinking fails at plan time      |
| `format`  | Replace                                                |

Growing is in place because replacing a disk destroys its contents — the exact
thing an additional disk exists to avoid.

## Locking: a disk is busy only while its instance runs

Lima locks a disk while the instance holding it is **running**. Attaching a
disk to a *stopped* instance does not lock it, and `in_use_by` reports null.

While locked, Lima refuses to resize or delete the disk, and so does the
provider:

```text
Error: Unable to delete Lima disk "project-data"

The disk "project-data" is held by running instance "project-dev".

Lima locks a disk while the instance holding it is running, and the provider
does not stop other people's instances to get around that.

Stop the instance first — set `start = false` on it and apply, or run
`limactl stop project-dev` — then destroy the disk.
```

The provider deliberately does **not** stop the instance for you. One resource
silently acting on another is not a decision a provider should make, and the
instance may be doing something you care about.

It also never calls `limactl disk unlock`. That command exists to clear a stale
lock left by a force-stopped instance, and the provider cannot tell a stale
lock from a live one — unlocking a disk a running VM is writing to risks
corrupting it.

### Ordering when destroying both

Terraform destroys the instance before the disk when the instance references
`lima_disk.data.name`, which is the usual case and works without help. If you
hardcode the disk name instead, add an explicit dependency:

```hcl
resource "lima_instance" "dev" {
  # ...
  additional_disks = ["project-data"]
  depends_on       = [lima_disk.data]
}
```

## Import

```console
$ terraform import lima_disk.data project-data
```

Everything about a disk is observable, so import records `size`,
`actual_format`, `dir`, `mount_point` and `in_use_by`, and a matching
configuration plans clean.

`format` is left unset, because Lima reports what it stored rather than what
was requested — recording `raw` would fight a configuration that asked for
`qcow2` on a `vz` host.
