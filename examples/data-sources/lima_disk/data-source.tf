terraform {
  required_providers {
    lima = {
      source = "guidoiaquinti/lima"
    }
  }
}

provider "lima" {}

# Reads a disk that Terraform does not necessarily manage.
data "lima_disk" "data" {
  name = "project-data"
}

output "size" {
  value = data.lima_disk.data.size
}

output "guest_path" {
  value = data.lima_disk.data.mount_point
}

# in_use_by is null unless a *running* instance holds the disk, so this is a
# reliable way to check whether it can be resized or removed right now.
output "can_be_modified" {
  value = data.lima_disk.data.in_use_by == null
}
