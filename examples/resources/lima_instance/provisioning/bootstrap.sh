#!/bin/bash
# Example provisioning script, run as root during instance creation.
set -euo pipefail

export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq --no-install-recommends ca-certificates curl git jq

echo "bootstrap complete" >/var/log/terraform-provider-lima-bootstrap.log
