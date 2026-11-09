// SPDX-License-Identifier: MPL-2.0

package main

import (
	"context"
	"flag"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"

	"github.com/solcreek/terraform-provider-kinsta/internal/provider"
)

// Generate the registry documentation from schema + examples/ + templates/.
//go:generate go tool tfplugindocs generate --provider-name kinsta

// version is overridden at build time via -ldflags.
var version = "dev"

func main() {
	var debug bool
	flag.BoolVar(&debug, "debug", false, "set to run the provider with support for debuggers")
	flag.Parse()

	err := providerserver.Serve(context.Background(), provider.New(version), providerserver.ServeOpts{
		Address: "registry.opentofu.org/solcreek/kinsta",
		Debug:   debug,
	})
	if err != nil {
		log.Fatal(err)
	}
}
