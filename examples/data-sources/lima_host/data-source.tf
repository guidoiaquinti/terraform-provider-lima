terraform {
  required_providers {
    lima = {
      source = "guidoiaquinti/lima"
    }
  }
}

provider "lima" {}

data "lima_host" "current" {}

output "lima_version" {
  value = data.lima_host.current.lima_version
}

output "host" {
  value = "${data.lima_host.current.host_os}/${data.lima_host.current.host_arch}"
}

# Detected from limactl info, not assumed from the platform.
output "available_backends" {
  value = data.lima_host.current.vm_types
}

output "available_templates" {
  value = data.lima_host.current.templates
}

output "existing_instances" {
  value = data.lima_host.current.instance_names
}
