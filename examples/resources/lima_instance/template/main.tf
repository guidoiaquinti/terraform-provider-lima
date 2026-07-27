# Choosing templates, and leaving an instance stopped.

terraform {
  required_providers {
    lima = {
      source = "guidoiaquinti/lima"
    }
  }
}

provider "lima" {}

data "lima_host" "current" {}

# A Docker-ready VM using the backend Lima reports as available on this host.
resource "lima_instance" "docker" {
  name     = "docker-dev"
  template = "template:docker"

  vm_type = contains(data.lima_host.current.vm_types, "vz") ? "vz" : "qemu"

  cpus   = 4
  memory = "8GiB"

  lifecycle {
    precondition {
      condition     = contains(data.lima_host.current.templates, "docker")
      error_message = "This Lima installation does not provide the docker template."
    }
  }
}
