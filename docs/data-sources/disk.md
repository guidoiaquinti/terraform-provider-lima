---
page_title: "lima_disk Data Source - terraform-provider-lima"
description: |-
  Reads an existing Lima disk.
---

# lima_disk (Data Source)

Reads an existing Lima disk. This data source never modifies the disk.

## Example usage

```hcl
data "lima_disk" "data" {
  name = "project-data"
}

output "guest_path" {
  value = data.lima_disk.data.mount_point
}

output "free_to_destroy" {
  value = data.lima_disk.data.in_use_by == null
}
```

## Schema

### Required

- `name` (String) The disk name, exactly as `limactl disk list` reports it.

### Read-only

- `id` (String) The disk name.
- `size` (String) Size in IEC notation, for example `50GiB`.
- `size_bytes` (Number) Size in bytes, as Lima reports it.
- `format` (String) The format Lima reports for the stored image. This is what
  was **stored**, not necessarily what was requested: the `vz` driver requires
  raw images.
- `dir` (String) The disk's directory inside `LIMA_HOME`.
- `mount_point` (String) Guest path the disk is mounted at when attached.
- `in_use_by` (String) Name of the **running** instance holding this disk, or
  null. A disk attached to a stopped instance reports null, because Lima only
  locks it while the instance runs.
