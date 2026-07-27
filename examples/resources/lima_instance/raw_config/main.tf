# Choosing templates, and leaving an instance stopped.

terraform {
  required_providers {
    lima = {
      source = "guidoiaquinti/lima"
    }
  }
}

provider "lima" {}

# Raw Lima YAML instead of a template, for options with no typed attribute.
# Typed attributes and config_overrides still layer on top of this.
#
# yamlencode keeps the document structured rather than a hand-written string,
# so it cannot drift into invalid YAML.
resource "lima_instance" "custom" {
  name = "custom"

  config = yamlencode({
    base = ["template:ubuntu"]
    containerd = {
      system = false
      user   = false
    }
  })

  # Typed attributes override the raw config above.
  cpus = 2

  # Merged last of all. Mappings merge key by key, sequences replace
  # wholesale, and an explicit null removes a key.
  config_overrides = yamlencode({
    nestedVirtualization = false
    ssh                  = { forwardAgent = true }
  })
}
