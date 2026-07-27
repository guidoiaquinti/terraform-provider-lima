terraform {
  required_providers {
    lima = {
      source = "guidoiaquinti/lima"
    }
  }
}

provider "lima" {}

# Reads an existing instance. The name is the real Lima instance name and is
# not affected by the provider's name_prefix, so this works for instances
# Terraform does not manage.
data "lima_instance" "default" {
  name = "default"
}

output "default_status" {
  value = data.lima_instance.default.status
}

output "default_resources" {
  value = {
    cpus   = data.lima_instance.default.cpus
    memory = data.lima_instance.default.memory
    disk   = data.lima_instance.default.disk
    arch   = data.lima_instance.default.arch
  }
}

# On a stopped instance these are last-known values, not a live endpoint.
output "default_ssh" {
  value = "ssh -F ${data.lima_instance.default.ssh_config} ${data.lima_instance.default.hostname}"
}
