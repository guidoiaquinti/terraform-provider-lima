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

  # abspath keeps the configuration portable across checkout locations.
  mount {
    location    = abspath(path.module)
    mount_point = "/workspace"
    writable    = true
  }

  # A read-only cache directory. A leading ~ is expanded by the provider.
  mount {
    location    = "~/.cache/shared"
    mount_point = "/mnt/cache"
    writable    = false
  }
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
# * Changing any mount replaces the instance. See the resource documentation.
