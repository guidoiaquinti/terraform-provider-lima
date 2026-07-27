# Native Lima provisioning.

terraform {
  required_providers {
    lima = {
      source = "guidoiaquinti/lima"
    }
  }
}

provider "lima" {}

resource "lima_instance" "dev" {
  name     = "provisioned-dev"
  template = "template:ubuntu"

  # Provisioning runs during instance creation, which is Lima's own model.
  # The provider never uses remote-exec and never re-runs scripts on refresh.
  provision {
    mode        = "system"
    script      = file("${path.module}/bootstrap.sh")
    rerun_token = filesha256("${path.module}/bootstrap.sh")
  }

  # A second step, run as the guest user.
  provision {
    mode   = "user"
    script = <<-EOT
      #!/bin/bash
      set -euo pipefail
      mkdir -p "$HOME/.config"
      echo "provisioned by terraform" > "$HOME/.config/provisioned"
    EOT
  }
}

# Changing rerun_token (or any provisioning field) replaces the instance,
# which is how you request that provisioning runs again. Using filesha256
# means editing bootstrap.sh is enough to trigger it.
