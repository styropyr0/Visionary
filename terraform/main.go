// Command terraform-provider-visionary is the Terraform provider for Visionary.
package main

import (
	"context"
	"flag"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"

	"github.com/styropyr0/visionary/terraform/internal/provider"
)

// version is set by goreleaser.
var version = "dev"

// Docs for the Terraform registry, from the schema and examples/.
//go:generate go run github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs@v0.25.0 generate --provider-name visionary

func main() {
	var debug bool
	flag.BoolVar(&debug, "debug", false, "run with support for debuggers such as delve")
	flag.Parse()
	err := providerserver.Serve(context.Background(), provider.New(version), providerserver.ServeOpts{
		Address: "registry.terraform.io/styropyr0/visionary",
		Debug:   debug,
	})
	if err != nil {
		log.Fatal(err)
	}
}
