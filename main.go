package main

import (
	"context"
	"log"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6/tf6server"
	"github.com/hashicorp/terraform-plugin-mux/tf5to6server"
	"github.com/hashicorp/terraform-plugin-mux/tf6muxserver"
	"github.com/terraform-providers/terraform-provider-selectel/selectel"
	"github.com/terraform-providers/terraform-provider-selectel/version"
)

func main() {
	// Same log plumbing as SDKv2 plugin.Serve (plugin/serve.go:102-115): go-plugin
	// passes log levels to Terraform only from an hclog JSON logger.
	logger := hclog.New(&hclog.LoggerOptions{
		Level:      hclog.Trace,
		JSONFormat: true,
	})
	log.SetOutput(logger.StandardWriter(&hclog.StandardLoggerOptions{InferLevels: true}))

	ctx := context.Background()

	upgradedSDKProvider, err := tf5to6server.UpgradeServer(ctx, selectel.Provider(version.Version).GRPCProvider)
	if err != nil {
		log.Fatal(err)
	}

	muxServer, err := tf6muxserver.NewMuxServer(ctx,
		func() tfprotov6.ProviderServer { return upgradedSDKProvider },
		providerserver.NewProtocol6(selectel.NewFrameworkProvider(version.Version)()),
	)
	if err != nil {
		log.Fatal(err)
	}

	err = tf6server.Serve("registry.terraform.io/selectel/selectel", muxServer.ProviderServer)
	if err != nil {
		log.Fatal(err)
	}
}
