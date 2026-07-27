terraform {
  required_providers {
    lima = {
      source = "guidoiaquinti/lima"
    }
  }
}

# Every provider attribute is optional; an empty block uses limactl from PATH
# and Lima's default LIMA_HOME.
provider "lima" {}

# A fully configured provider, kept separate from the default one above.
provider "lima" {
  alias = "isolated"

  # Path to limactl. Defaults to resolving it from PATH.
  # Also settable with LIMA_PROVIDER_BINARY.
  binary = "/opt/homebrew/bin/limactl"

  # A dedicated Lima home keeps Terraform-managed VMs away from personal ones.
  # Keep this path short: Lima's unix socket paths must fit in 104 characters.
  # Also settable with LIMA_HOME.
  home = "~/.lima-terraform"

  # Prefix applied to every managed instance name. An instance named "dev"
  # becomes the real Lima instance "tf-dev".
  # Also settable with LIMA_PROVIDER_NAME_PREFIX.
  name_prefix = "tf-"

  # Fallback timeout for operations without their own.
  # Also settable with LIMA_PROVIDER_DEFAULT_TIMEOUT.
  default_timeout = "30m"

  # Extra environment for every limactl invocation. These override inherited
  # values, and are never written to logs or diagnostics.
  environment = {
    LIMA_INSTANCE = "unused-by-the-provider"
  }
}
