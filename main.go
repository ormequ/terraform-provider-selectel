package main

import (
	"context"
	"log"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6/tf6server"
	"github.com/terraform-providers/terraform-provider-selectel/selectel"
	"github.com/terraform-providers/terraform-provider-selectel/version"
)

func main() {
	// As in SDKv2 plugin.Serve: log levels reach Terraform only via hclog JSON.
	logger := hclog.New(&hclog.LoggerOptions{
		Level:      hclog.Trace,
		JSONFormat: true,
	})
	log.SetOutput(logger.StandardWriter(&hclog.StandardLoggerOptions{InferLevels: true}))

	providerServer, err := selectel.ProviderServer(context.Background(), version.Version)
	if err != nil {
		log.Fatal(err)
	}

	err = tf6server.Serve("registry.terraform.io/selectel/selectel", providerServer)
	if err != nil {
		log.Fatal(err)
	}
}
