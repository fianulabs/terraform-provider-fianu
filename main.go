// Package main is the entrypoint for terraform-provider-fianu.
//
// The provider exposes Fianu compliance entities (controls, gates, policies,
// environments, targets, collections) as Terraform-managed resources. It
// targets terraform-plugin-framework v1.19+ over plugin protocol v6.
//
// Local development:
//
//	go install .
//	# add a dev_overrides block to ~/.terraformrc pointing at $GOPATH/bin
//
// Acceptance tests:
//
//	TF_ACC=1 go test ./internal/resources/...
package main

import (
	"context"
	"flag"
	"log"

	"github.com/fianulabs/terraform-provider-fianu/internal/provider"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
)

// version is overridden at build time via -ldflags so released binaries report
// the correct semver. Stays "dev" for local builds and acceptance tests.
var version = "dev"

func main() {
	var debug bool
	flag.BoolVar(&debug, "debug", false, "run the provider with support for debuggers like delve")
	flag.Parse()

	opts := providerserver.ServeOpts{
		Address: "registry.terraform.io/fianulabs/fianu",
		Debug:   debug,
	}

	if err := providerserver.Serve(context.Background(), provider.New(version), opts); err != nil {
		log.Fatal(err)
	}
}
