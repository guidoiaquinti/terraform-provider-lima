# Minimum-CLI compatibility fixture.
#
# This is not an example and is not rendered into the documentation. Its only
# job is to name every part of the provider's schema surface in one place, so
# that `terraform validate` against the oldest supported CLI proves the README's
# compatibility claim instead of asserting it.
#
# Deliberately separate from examples/: the examples are written for people and
# use whatever HCL reads best, including features newer than the provider's
# floor. Mixing the two questions is what let "Terraform 1.0+" go untested.
#
# Keep this file free of any HCL feature newer than the documented floor.
# In particular: no `lifecycle { precondition }` (Terraform 1.2+), no optional
# object attributes, no `moved` blocks, no provider-defined functions.
#
# What validating this file does and does not prove, measured rather than
# assumed (Terraform 1.0.11, 1.2.9, 1.4.7, 1.15.8; OpenTofu 1.6.2, 1.10.6):
#
#   Proved   the provider binary starts and serves its schema over protocol 6
#            on that CLI; every top-level attribute named below exists; every
#            computed attribute referenced in the outputs exists.
#   NOT      attribute names *inside* a nested list attribute. Renaming
#   proved   `mounts[].mount_point` to something nonexistent validates cleanly
#            on every version tested, because the object literal is converted
#            at plan time rather than at validate time. Nested attribute names
#            are covered by the unit tests instead, not here.
#
# Adding a `mounts[].bogus` here and expecting CI to catch it will not work.
# Add a schema assertion to internal/provider instead.

terraform {
  required_providers {
    lima = {
      source = "guidoiaquinti/lima"
    }
  }
}

# Every provider attribute.
provider "lima" {
  binary          = "/usr/bin/limactl"
  home            = "/tmp/compat-lima"
  name_prefix     = "compat-"
  default_timeout = "25m"
  environment = {
    LIMA_EXAMPLE = "1"
  }
}

# lima_instance: every configurable attribute, including all three nested list
# attributes and the timeouts attribute.
resource "lima_instance" "full" {
  name     = "compat"
  template = "template:alpine"

  cpus    = 2
  memory  = "2GiB"
  disk    = "20GiB"
  vm_type = "qemu"
  arch    = "x86_64"

  start   = true
  protect = false

  config_overrides = <<-YAML
    mountType: reverse-sshfs
  YAML

  mounts = [
    {
      location    = "/tmp/compat-share"
      mount_point = "/mnt/share"
      writable    = true
    },
  ]

  port_forwards = [
    {
      guest_port = 8080
      host_port  = 18080
      protocol   = "tcp"
    },
  ]

  provisions = [
    {
      mode   = "system"
      script = "#!/bin/sh\necho compat\n"
    },
  ]

  additional_disks = [lima_disk.data.name]

  timeouts = {
    create = "30m"
    update = "20m"
    delete = "20m"
    read   = "2m"
  }
}

# The raw-config path, which is mutually exclusive with template in practice but
# valid on its own.
resource "lima_instance" "raw" {
  name = "compat-raw"

  config = <<-YAML
    images:
      - location: "https://example.invalid/img.qcow2"
        arch: "x86_64"
  YAML
}

resource "lima_disk" "data" {
  name   = "compat-data"
  size   = "10GiB"
  format = "qcow2"

  timeouts = {
    create = "10m"
    update = "10m"
    delete = "5m"
    read   = "2m"
  }
}

data "lima_host" "this" {
  timeouts = {
    read = "1m"
  }
}

data "lima_instance" "one" {
  name = lima_instance.full.instance_name

  timeouts = {
    read = "1m"
  }
}

data "lima_instances" "all" {
  timeouts = {
    read = "1m"
  }
}

data "lima_disk" "one" {
  name = lima_disk.data.name

  timeouts = {
    read = "1m"
  }
}

# Reference every computed attribute, so a rename of one is a validate failure
# rather than a silent documentation change.
output "instance_computed" {
  value = {
    instance_name = lima_instance.full.instance_name
    status        = lima_instance.full.status
    raw_status    = lima_instance.full.raw_status
    ssh_address   = lima_instance.full.ssh_address
    ssh_port      = lima_instance.full.ssh_port
    ssh_user      = lima_instance.full.ssh_user
    ssh_config    = lima_instance.full.ssh_config
    hostname      = lima_instance.full.hostname
    dir           = lima_instance.full.dir
    config_hash   = lima_instance.full.config_hash
    lima_version  = lima_instance.full.lima_version
  }
}

output "disk_computed" {
  value = {
    dir         = lima_disk.data.dir
    in_use_by   = lima_disk.data.in_use_by
    mount_point = lima_disk.data.mount_point
  }
}

output "host_computed" {
  value = {
    lima_version   = data.lima_host.this.lima_version
    host_os        = data.lima_host.this.host_os
    host_arch      = data.lima_host.this.host_arch
    vm_types       = data.lima_host.this.vm_types
    lima_home      = data.lima_host.this.lima_home
    binary_path    = data.lima_host.this.binary_path
    templates      = data.lima_host.this.templates
    instance_names = data.lima_host.this.instance_names
  }
}

output "instances_computed" {
  value = [for i in data.lima_instances.all.instances : i.name]
}
