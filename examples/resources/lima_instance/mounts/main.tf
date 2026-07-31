# Sharing host directories into the guest.

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

  mounts = [
    # abspath keeps the configuration portable across checkout locations.
    {
      location    = abspath(path.module)
      mount_point = "/workspace"
      writable    = true
    },
    # A read-only cache directory. A leading ~ is expanded by the provider.
    {
      location    = "~/.cache/shared"
      mount_point = "/mnt/cache"
      writable    = false
    },
  ]
}

# Notes:
#
# * Mount locations are absolute host paths, so a configuration hardcoding
#   /Users/... will not apply on a Linux host. Use abspath() or a variable.
#
# * Lima rejects guest system paths such as /etc or /usr as mount points. On
#   macOS, mounting /tmp without an explicit mount_point fails for this reason,
#   because /tmp resolves to /private/tmp.
#
# * Changing a mount is applied in place: the instance is stopped, reconfigured
#   with `limactl edit --set` and started again. Expect brief downtime, not a
#   rebuild. See the resource documentation.
#
# * Because `mounts` is a list attribute rather than a block, entries can be
#   derived from data:
#
#       mounts = [for d in var.shared_dirs : { location = d, writable = true }]
