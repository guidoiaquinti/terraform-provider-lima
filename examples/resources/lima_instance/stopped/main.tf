# Choosing templates, and leaving an instance stopped.

terraform {
  required_providers {
    lima = {
      source = "guidoiaquinti/lima"
    }
  }
}

provider "lima" {}

# An instance that exists but is not running. Creating it downloads and
# prepares the image without paying the cost of booting it.
#
# Flipping start to true later updates in place; it does not replace the VM.
resource "lima_instance" "image_builder" {
  name     = "image-builder"
  template = "template:ubuntu"
  start    = false
}

output "status" {
  value = lima_instance.image_builder.status
}
