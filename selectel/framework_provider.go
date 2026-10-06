package selectel

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	providerschema "github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-mux/tf5to6server"
	"github.com/hashicorp/terraform-plugin-mux/tf6muxserver"
	sdkschema "github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/terraform-providers/terraform-provider-selectel/version"
)

// frameworkProvider serves the terraform-plugin-framework resources muxed
// next to the SDKv2 provider. Its schema and Config both come from the SDKv2
// provider, because tf6muxserver requires identical provider schemas.
type frameworkProvider struct {
	sdk     *sdkschema.Provider
	version string
}

var _ provider.Provider = &frameworkProvider{}

// ProviderServer returns the provider served by main.go: the SDKv2 provider,
// upgraded to protocol 6, muxed with the framework provider.
func ProviderServer(ctx context.Context, providerVersion string) (func() tfprotov6.ProviderServer, error) {
	sdk := Provider(providerVersion)

	return muxProviderServer(ctx, sdk, &frameworkProvider{sdk: sdk, version: providerVersion})
}

func muxProviderServer(ctx context.Context, sdk *sdkschema.Provider, fw provider.Provider) (func() tfprotov6.ProviderServer, error) {
	upgradedSDK, err := tf5to6server.UpgradeServer(ctx, sdk.GRPCProvider)
	if err != nil {
		return nil, err
	}

	mux, err := tf6muxserver.NewMuxServer(ctx,
		func() tfprotov6.ProviderServer { return upgradedSDK },
		providerserver.NewProtocol6(fw),
	)
	if err != nil {
		return nil, err
	}

	return mux.ProviderServer, nil
}

func (p *frameworkProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "selectel"
	resp.Version = p.version
}

// Schema mirrors the SDKv2 provider schema. Every SDKv2 provider attribute is
// a string. SDKv2 reports Required + EnvDefaultFunc as optional while the
// variable is set (helper/schema/core_schema.go), so the same rule applies here.
func (p *frameworkProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	attrs := make(map[string]providerschema.Attribute, len(p.sdk.Schema))
	for name, s := range p.sdk.Schema {
		required := s.Required && sdkDefault(s) == ""
		attrs[name] = providerschema.StringAttribute{
			Required:    required,
			Optional:    !required,
			Description: s.Description,
		}
	}

	resp.Schema = providerschema.Schema{Attributes: attrs}
}

// Configure shares the process-wide Config with the SDKv2 provider through
// newConfig. tf6muxserver configures the SDKv2 provider first, so in practice
// this side gets the Config the SDKv2 side has already built.
func (p *frameworkProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	config := newConfig(p.sdk.UserAgent(version.ProviderName, p.version), func(key string) string {
		var v types.String
		resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root(key), &v)...)
		if v.IsNull() {
			return sdkDefault(p.sdk.Schema[key])
		}

		return v.ValueString()
	})
	if resp.Diagnostics.HasError() {
		return
	}

	resp.ResourceData = config
	resp.DataSourceData = config
}

func (p *frameworkProvider) Resources(_ context.Context) []func() resource.Resource {
	return nil
}

func (p *frameworkProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return nil
}

// sdkDefault returns what the attribute's EnvDefaultFunc resolves to now.
func sdkDefault(s *sdkschema.Schema) string {
	v, _ := s.DefaultValue()
	str, _ := v.(string)

	return str
}
