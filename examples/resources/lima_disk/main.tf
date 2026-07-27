# An additional disk, attached to an instance.

terraform {
  required_providers {
    lima = {
      source = "guidoiaquinti/lima"
    }
  }
}

provider "lima" {}

resource "lima_disk" "data" {
  name = "project-data"
  size = "50GiB"
}

resource "lima_instance" "dev" {
  name     = "project-dev"
  template = "template:ubuntu"

  # Referencing the disk resource rather than hardcoding the name gives
  # Terraform the dependency it needs to destroy them in the right order.
  additional_disks = [lima_disk.data.name]
}

# Growing the disk is an in-place update; the data on it survives.
#
#   terraform apply -var ...   # after changing size to "100GiB"
#
# Shrinking is rejected during planning, because Lima cannot shrink a disk.

output "guest_path" {
  # Lima mounts an additional disk at /mnt/lima-<name> by default.
  value = lima_disk.data.mount_point
}

output "in_use" {
  # Lima locks a disk only while the instance holding it is running. While
  # locked, the disk cannot be resized or destroyed.
  value = lima_disk.data.in_use_by
}
