package selectel

import (
	"context"
	"os"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
)

// frameworkProvider serves the terraform-plugin-framework resources muxed
// next to the SDKv2 provider. Its provider schema must be identical to the
// one the SDKv2 provider reports, or tf6muxserver rejects GetProviderSchema.
type frameworkProvider struct {
	version string
}

var _ provider.Provider = &frameworkProvider{}

func NewFrameworkProvider(version string) func() provider.Provider {
	return func() provider.Provider {
		return &frameworkProvider{version: version}
	}
}

func (p *frameworkProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "selectel"
	resp.Version = p.version
}

// envBackedString mirrors an SDKv2 attribute declared as
// Required + DefaultFunc: schema.EnvDefaultFunc(env, nil). SDKv2 reports such
// an attribute as optional when the variable is set and as required otherwise
// (helper/schema/core_schema.go), so the framework side has to do the same.
func envBackedString(env, description string) schema.StringAttribute {
	set := os.Getenv(env) != ""

	return schema.StringAttribute{
		Required:    !set,
		Optional:    set,
		Description: description,
	}
}

func (p *frameworkProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Attributes: map[string]schema.Attribute{
			"project_id": schema.StringAttribute{
				Optional:    true,
				Description: "VPC project ID to import resources that need the project scope auth token.",
			},
			"region": schema.StringAttribute{
				Optional:    true,
				Description: "VPC region to import resources associated with the specific region.",
			},
			"auth_url":    envBackedString("OS_AUTH_URL", "Base url to work with auth API (Keystone URL)."),
			"auth_region": envBackedString("OS_REGION_NAME", "Region for Keystone and Resell API URLs."),
			"domain_name": envBackedString("OS_DOMAIN_NAME", "Your domain name i.e. your account id"),
			"username":    envBackedString("OS_USERNAME", "Service user username"),
			"user_domain_name": schema.StringAttribute{
				Optional:    true,
				Description: "Used for service accounts in other domain. If you don't know exactly what this field means then don't use it",
			},
			"password": envBackedString("OS_PASSWORD", "Service user password"),
		},
	}
}

func (p *frameworkProvider) Configure(_ context.Context, _ provider.ConfigureRequest, _ *provider.ConfigureResponse) {
	// PoC: the stub resources never call an API, so no client is built here.
}

func (p *frameworkProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		newMKSClusterV2Resource,
	}
}

func (p *frameworkProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return nil
}
