// terraform-provider-lima manages local Lima virtual machines through the
// supported limactl command line interface.
//
// This is an independent, unofficial provider. It is not affiliated with the
// Lima project or HashiCorp.
package main

import (
	"context"
	"flag"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"

	"github.com/guidoiaquinti/terraform-provider-lima/internal/provider"
)

// version is overridden at build time by GoReleaser via -ldflags.
var version = "0.1.0-dev"

func main() {
	var debug bool
	flag.BoolVar(&debug, "debug", false,
		"run the provider with support for debuggers such as delve")
	flag.Parse()

	opts := providerserver.ServeOpts{
		// Derived from the repository, github.com/guidoiaquinti/terraform-provider-lima:
		// the registry takes the namespace from the owner and the provider name
		// from the repository name minus its terraform-provider- prefix. It must
		// match the source in required_providers and in any dev_overrides block,
		// and the namespace in the Makefile.
		Address: "registry.terraform.io/guidoiaquinti/lima",
		Debug:   debug,
	}

	if err := providerserver.Serve(context.Background(), provider.New(version), opts); err != nil {
		log.Fatal(err.Error())
	}
}
