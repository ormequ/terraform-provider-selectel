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

// frameworkProvider serves the terraform-plugin-framework resources next to
// the SDKv2 provider.
type frameworkProvider struct {
	sdk     *sdkschema.Provider
	version string
	// configFn replaces newConfig when set, which only tests do: each test
	// provider then gets a Config of its own.
	configFn func(userAgent string, attr func(key string) string) *Config
}

var _ provider.Provider = &frameworkProvider{}

// ProviderServer returns the SDKv2 provider, upgraded to protocol 6, muxed
// with the framework provider.
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

// Schema copies the SDKv2 provider schema, because tf6muxserver requires the
// two to be identical. SDKv2 reports Required + EnvDefaultFunc as optional
// while the variable is set, so the copy does the same.
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

func (p *frameworkProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	// UserAgent reads it; the SDKv2 Configure sets it only when it runs first.
	p.sdk.TerraformVersion = req.TerraformVersion
	configFn := newConfig
	if p.configFn != nil {
		configFn = p.configFn
	}
	config := configFn(p.sdk.UserAgent(version.ProviderName, p.version), func(key string) string {
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

func sdkDefault(s *sdkschema.Schema) string {
	v, _ := s.DefaultValue()
	str, _ := v.(string)

	return str
}
