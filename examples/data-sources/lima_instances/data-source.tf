# Reading every Lima instance, managed or not.

terraform {
  required_providers {
    lima = {
      source = "guidoiaquinti/lima"
    }
  }
}

provider "lima" {}

data "lima_instances" "all" {}

# There are no filter arguments: an ordinary Terraform expression does the job,
# and would have to be kept consistent with lima_instance otherwise.
output "running" {
  value = [for i in data.lima_instances.all.instances : i.name if i.status == "running"]
}

# Names are the real Lima names, so name_prefix is neither applied nor stripped.
# Filter on it to find only the instances a prefix covers.
output "prefixed" {
  value = [
    for i in data.lima_instances.all.instances : i.name
    if startswith(i.name, "tf-")
  ]
}

# Everything the singular data source exposes is available per entry.
output "ssh_endpoints" {
  value = {
    for i in data.lima_instances.all.instances :
    i.name => "${i.ssh_address}:${i.ssh_port}"
    if i.status == "running"
  }
}
