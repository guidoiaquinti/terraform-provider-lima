# Choosing templates, and leaving an instance stopped.

terraform {
  # The provider itself works on Terraform 1.0 and OpenTofu 1.6. This example
  # is the one that does not: the `lifecycle { precondition }` block below
  # arrived in Terraform 1.2, and on 1.0 the configuration fails with
  # `Blocks of type "precondition" are not expected here`. Declaring the
  # requirement turns that into a clear version error instead of a puzzling
  # syntax one, and keeps the provider's floor and this example's floor as two
  # separate facts.
  required_version = ">= 1.2.0"

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
