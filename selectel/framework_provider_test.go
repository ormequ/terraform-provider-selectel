package selectel

import (
	"context"
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	dsschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/resource"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testAccProtoV6ProviderFactories serves the provider through the same mux as
// main.go, plus the selectel_mux_test data source.
var testAccProtoV6ProviderFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"selectel": func() (tfprotov6.ProviderServer, error) {
		sdk := Provider("test")
		server, err := muxProviderServer(context.Background(), sdk,
			testFrameworkProvider{&frameworkProvider{sdk: sdk, version: "test"}})
		if err != nil {
			return nil, err
		}

		return server(), nil
	},
}

type testFrameworkProvider struct {
	*frameworkProvider
}

func (p testFrameworkProvider) DataSources(ctx context.Context) []func() datasource.DataSource {
	return append(p.frameworkProvider.DataSources(ctx), func() datasource.DataSource {
		return &muxTestDataSource{}
	})
}

type muxTestDataSource struct {
	config *Config
}

func (d *muxTestDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_mux_test"
}

func (d *muxTestDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = dsschema.Schema{
		Attributes: map[string]dsschema.Attribute{
			// SDKv2 helper/resource refuses state without an id.
			"id":       dsschema.StringAttribute{Computed: true},
			"username": dsschema.StringAttribute{Computed: true},
		},
	}
}

func (d *muxTestDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, _ *datasource.ConfigureResponse) {
	d.config, _ = req.ProviderData.(*Config)
}

func (d *muxTestDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	if d.config == nil {
		resp.Diagnostics.AddError("Provider not configured", "The data source got no *Config from the framework provider.")

		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), "mux_test")...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("username"), d.config.Username)...)
}

func setTestProviderEnv(t *testing.T) {
	t.Helper()

	for _, env := range []string{"OS_AUTH_URL", "OS_REGION_NAME", "OS_DOMAIN_NAME", "OS_USERNAME", "OS_PASSWORD"} {
		if os.Getenv(env) == "" {
			t.Setenv(env, "test")
		}
	}
}

func TestProviderServerSchema(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T)
	}{
		{
			name:  "credentials in env",
			setup: setTestProviderEnv,
		},
		{
			name: "credentials in provider block",
			setup: func(t *testing.T) {
				t.Helper()
				for _, env := range []string{"OS_AUTH_URL", "OS_REGION_NAME", "OS_DOMAIN_NAME", "OS_USERNAME", "OS_PASSWORD"} {
					t.Setenv(env, "")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setup(t)

			server, err := testAccProtoV6ProviderFactories["selectel"]()
			require.NoError(t, err)

			resp, err := server.GetProviderSchema(context.Background(), &tfprotov6.GetProviderSchemaRequest{})
			require.NoError(t, err)
			assert.Empty(t, resp.Diagnostics)
			assert.Contains(t, resp.ResourceSchemas, "selectel_vpc_project_v2")
			assert.Contains(t, resp.DataSourceSchemas, "selectel_mux_test")
		})
	}
}

func TestFrameworkProviderSharesConfig(t *testing.T) {
	setTestProviderEnv(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `data "selectel_mux_test" "test" {}`,
				Check:  resource.TestCheckResourceAttr("data.selectel_mux_test.test", "username", os.Getenv("OS_USERNAME")),
			},
		},
	})
}
