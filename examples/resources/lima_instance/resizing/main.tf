# Resizing an existing instance in place.

terraform {
  required_providers {
    lima = {
      source = "guidoiaquinti/lima"
    }
  }
}

provider "lima" {}

variable "cpus" {
  type        = number
  description = "Virtual CPUs. Changing this resizes the VM in place."
  default     = 2
}

variable "memory" {
  type        = string
  description = "Memory size. Changing this resizes the VM in place."
  default     = "4GiB"
}

resource "lima_instance" "dev" {
  name     = "resizable-dev"
  template = "template:ubuntu"

  cpus   = var.cpus
  memory = var.memory

  # Disk can grow in place, but never shrink: Lima has no shrink operation,
  # and the provider rejects a smaller value during planning rather than
  # replacing the instance and destroying its contents.
  disk = "50GiB"
}

# Changing cpus, memory or disk is an update, not a destroy-and-recreate:
#
#   terraform apply -var cpus=4 -var memory=8GiB
#
# Lima cannot reconfigure a running instance, so the provider stops the VM,
# applies the change with `limactl edit`, and starts it again. Expect brief
# downtime — the disk and everything on it survive.
#
# To avoid downtime entirely, resize while the instance is already stopped:
#
#   terraform apply -var cpus=4 -var memory=8GiB   # with start = false
#
# Sizes are compared by byte count, so switching between "8GiB" and "8192MiB"
# changes nothing and performs no resize.

output "resources" {
  value = {
    cpus   = lima_instance.dev.cpus
    memory = lima_instance.dev.memory
    disk   = lima_instance.dev.disk
    status = lima_instance.dev.status
  }
}
