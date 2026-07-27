# Forwarding guest ports to the host.

terraform {
  required_providers {
    lima = {
      source = "guidoiaquinti/lima"
    }
  }
}

provider "lima" {}

resource "lima_instance" "web" {
  name     = "local-web"
  template = "template:ubuntu"

  port_forward {
    guest_port = 8080
    host_port  = 18080
    protocol   = "tcp"
  }

  port_forward {
    guest_port = 5432
    host_port  = 15432
    protocol   = "tcp"
  }

  # Bind beyond loopback so other machines on the network can reach it.
  port_forward {
    guest_port = 9090
    host_port  = 19090
    host_ip    = "0.0.0.0"
  }
}

# Use the forwarded host endpoint. The provider does not expose guest IP
# addresses, because they are not reliable across Lima's networking modes.
output "web_url" {
  value = "http://127.0.0.1:18080"
}
