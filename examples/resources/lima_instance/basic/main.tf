# Minimal Ubuntu instance with explicit resources.

terraform {
  required_providers {
    lima = {
      source = "guidoiaquinti/lima"
    }
  }
}

provider "lima" {}

resource "lima_instance" "dev" {
  name     = "project-dev"
  template = "template:ubuntu"

  cpus   = 4
  memory = "8GiB"
  disk   = "50GiB"
}

output "status" {
  value = lima_instance.dev.status
}

# Lima generates a ready-to-use SSH configuration file for each instance.
output "ssh_command" {
  value = "ssh -F ${lima_instance.dev.ssh_config} ${lima_instance.dev.hostname}"
}
