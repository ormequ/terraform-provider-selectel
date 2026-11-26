package mks

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/selectel/mks-go/v2/pkg/mksclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testKubeconfig = `apiVersion: v1
kind: Config
# server: https://192.0.2.1:6443
clusters:
- name: kubernetes
  cluster:
    certificate-authority-data: Y2EtZGF0YQ==` + " " + `
    server: https://192.0.2.10:6443 # public
- name: other
  cluster:
    certificate-authority-data: b3RoZXItY2E=
    server: https://192.0.2.20:6443
users:
- name: kubernetes-admin
  user:
    client-certificate-data: Y2xpZW50LWNlcnQ=
    client-key-data: Y2xpZW50LWtleQ==
`

func dataSourceSchema(d datasource.DataSource) schema.Schema {
	var resp datasource.SchemaResponse
	d.Schema(context.Background(), datasource.SchemaRequest{}, &resp)

	return resp.Schema
}

func readData(t *testing.T, d datasource.DataSource, config *testProviderConfig, attrs map[string]string) (tfsdk.State, diag.Diagnostics) {
	t.Helper()
	ctx := context.Background()
	var configured datasource.ConfigureResponse
	d.(datasource.DataSourceWithConfigure).Configure(ctx, datasource.ConfigureRequest{ProviderData: config}, &configured)
	require.False(t, configured.Diagnostics.HasError())

	s := dataSourceSchema(d)
	input := tfsdk.State{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(ctx), nil)}
	for name, value := range attrs {
		require.False(t, input.SetAttribute(ctx, path.Root(name), value).HasError())
	}
	resp := datasource.ReadResponse{State: input}
	d.Read(ctx, datasource.ReadRequest{Config: tfsdk.Config(input)}, &resp)

	return resp.State, resp.Diagnostics
}

func stringAttribute(t *testing.T, state tfsdk.State, name string) string {
	t.Helper()
	var value types.String
	require.False(t, state.GetAttribute(context.Background(), path.Root(name), &value).HasError())

	return value.ValueString()
}

func TestKubeconfigDataSource(t *testing.T) {
	fake := newFakeAPI(t)
	fake.seedCluster(testClusterID)

	state, diags := readData(t, NewKubeconfigDataSource(), &testProviderConfig{client: fake.client()},
		map[string]string{"cluster_id": testClusterID, "pool": "ru-7", "project_id": apiProject})

	require.False(t, diags.HasError(), "%v", diags)
	for name, want := range map[string]string{
		"id":              testClusterID,
		"cluster_id":      testClusterID,
		"pool":            "ru-7",
		"project_id":      apiProject,
		"raw_config":      testKubeconfig,
		"server":          "https://192.0.2.10:6443",
		"cluster_ca_cert": "Y2EtZGF0YQ==",
		"client_cert":     "Y2xpZW50LWNlcnQ=",
		"client_key":      "Y2xpZW50LWtleQ==",
	} {
		assert.Equal(t, want, stringAttribute(t, state, name), name)
	}
	assert.Zero(t, fake.callCount(routeCluster))
}

func TestKubeconfigDataSourceAPIError(t *testing.T) {
	fake := newFakeAPI(t)
	fake.fail(routeKubeconfig, http.StatusConflict)

	_, diags := readData(t, NewKubeconfigDataSource(), &testProviderConfig{client: fake.client()},
		map[string]string{"cluster_id": testClusterID, "pool": "ru-7", "project_id": apiProject})

	assertErrorContains(t, diags, "Error reading kubeconfig: error getting kubeconfig '"+testClusterID+"'", "fake error 409")
}

func TestKubeconfigDataSourceMissingField(t *testing.T) {
	for _, field := range []string{"certificate-authority-data", "server", "client-certificate-data", "client-key-data"} {
		t.Run(field, func(t *testing.T) {
			fake := newFakeAPI(t)
			fake.seedCluster(testClusterID)
			fake.kubeconfig = regexp.MustCompile(`(?m)^ +`+field+`:.*\n`).ReplaceAllString(testKubeconfig, "")

			_, diags := readData(t, NewKubeconfigDataSource(), &testProviderConfig{client: fake.client()},
				map[string]string{"cluster_id": testClusterID, "pool": "ru-7", "project_id": apiProject})

			assertErrorContains(t, diags, "Error reading kubeconfig: error getting kubeconfig '"+testClusterID+"'",
				"unable to find "+field+" field in kubeconfig")
			for _, value := range []string{"Y2EtZGF0YQ==", "https://192.0.2.10:6443", "Y2xpZW50LWNlcnQ=", "Y2xpZW50LWtleQ=="} {
				assert.NotContains(t, diags.Errors()[0].Detail(), value)
			}
		})
	}
}

func TestKubeconfigDataSourceSensitive(t *testing.T) {
	s := dataSourceSchema(NewKubeconfigDataSource())

	for _, name := range []string{"raw_config", "server", "cluster_ca_cert", "client_cert", "client_key"} {
		assert.True(t, s.Attributes[name].IsSensitive(), name)
	}
}

func TestKubeVersionsDataSource(t *testing.T) {
	fake := newFakeAPI(t)
	fake.seedKubeVersions(
		mksclient.KubeVersionInfo{Version: new("1.9.8")},
		mksclient.KubeVersionInfo{Version: new("1.10.2"), IsDefault: new(true)},
		mksclient.KubeVersionInfo{Version: new("1.10.11"), IsDefault: new(false)},
		mksclient.KubeVersionInfo{Version: new("1.10.9")},
	)

	state, diags := readData(t, NewKubeVersionsDataSource(), &testProviderConfig{client: fake.client()},
		map[string]string{"pool": "ru-7", "project_id": apiProject})

	require.False(t, diags.HasError(), "%v", diags)
	var versions []string
	require.False(t, state.GetAttribute(context.Background(), path.Root("versions"), &versions).HasError())
	assert.Equal(t, []string{"1.9.8", "1.10.2", "1.10.11", "1.10.9"}, versions)
	assert.Equal(t, "1.10.11", stringAttribute(t, state, "latest_version"))
	assert.Equal(t, "1.10.2", stringAttribute(t, state, "default_version"))
	assert.Equal(t, "ru-7", stringAttribute(t, state, "id"))
	assert.Equal(t, apiProject, stringAttribute(t, state, "project_id"))
}

func TestKubeVersionsDataSourceErrors(t *testing.T) {
	for _, tt := range []struct {
		name string
		set  func(fake *fakeAPI)
		want string
	}{
		{
			name: "an API error",
			set:  func(fake *fakeAPI) { fake.fail(routeKubeVersions, http.StatusInternalServerError) },
			want: "fake error 500",
		},
		{
			name: "an unreadable version",
			set: func(fake *fakeAPI) {
				fake.seedKubeVersions(mksclient.KubeVersionInfo{Version: new("1.31.x")})
			},
			want: "kube version 1.31.x is invalid",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fake := newFakeAPI(t)
			tt.set(fake)

			_, diags := readData(t, NewKubeVersionsDataSource(), &testProviderConfig{client: fake.client()},
				map[string]string{"pool": "ru-7", "project_id": apiProject})

			assertErrorContains(t, diags, "Error reading kube-versions: error getting kube-versions: ", tt.want)
		})
	}
}

func TestKubeOptionsDataSources(t *testing.T) {
	for _, source := range []struct {
		attribute, object, route string
		create                   func() datasource.DataSource
		names                    map[string][]string
	}{
		{
			attribute: "feature_gates", object: objectFeatureGates, route: routeFeatureGates, create: NewFeatureGatesDataSource,
			names: map[string][]string{"1.30": {"TopologyManager"}, "1.31": {"SidecarContainers", "TopologyManager"}, "1.32": {}, "1.33": {"SidecarContainers", "TopologyManager"}},
		},
		{
			attribute: "admission_controllers", object: objectAdmissionControllers, route: routeAdmissionControllers, create: NewAdmissionControllersDataSource,
			names: map[string][]string{"1.30": {"NodeRestriction"}, "1.31": {"AlwaysPullImages", "NodeRestriction"}, "1.32": {}, "1.33": {"AlwaysPullImages", "NodeRestriction"}},
		},
	} {
		for _, tt := range []struct {
			name, kubeVersion string
			status            int
			want              []string
			wantErr           string
		}{
			{name: "every version without kube_version", want: []string{"1.30", "1.31", "1.32", "1.33"}},
			{name: "one version by the patch", kubeVersion: "1.31.8", want: []string{"1.31"}},
			{name: "one version by the minor", kubeVersion: "1.30", want: []string{"1.30"}},
			{
				name: "a version not in the list", kubeVersion: "1.3.1",
				wantErr: `: available kubernetes options for kubernetes version "1.3" is not found`,
			},
			{
				name: "an API error", status: http.StatusInternalServerError,
				wantErr: ": error getting " + source.object + ": ",
			},
		} {
			t.Run(source.attribute+"/"+tt.name, func(t *testing.T) {
				fake := newFakeAPI(t)
				if tt.status != 0 {
					fake.fail(source.route, tt.status)
				}
				attrs := map[string]string{"pool": "ru-7", "project_id": apiProject}
				if tt.kubeVersion != "" {
					attrs["kube_version"] = tt.kubeVersion
				}

				state, diags := readData(t, source.create(), &testProviderConfig{client: fake.client()}, attrs)

				if tt.wantErr != "" {
					assertErrorContains(t, diags, "Error reading "+source.object+tt.wantErr)
					withPath, ok := diags.Errors()[0].(diag.DiagnosticWithPath)
					assert.Equal(t, tt.kubeVersion != "", ok)
					if ok {
						assert.Equal(t, path.Root("kube_version"), withPath.Path())
					}

					return
				}
				require.False(t, diags.HasError(), "%v", diags)
				var options []kubeOptionsDataModel
				require.False(t, state.GetAttribute(context.Background(), path.Root(source.attribute), &options).HasError())
				var want []kubeOptionsDataModel
				for _, version := range tt.want {
					want = append(want, kubeOptionsDataModel{KubeVersion: version, Names: source.names[version]})
				}
				assert.ElementsMatch(t, want, options)
				assert.Equal(t, "ru-7", stringAttribute(t, state, "id"))
				assert.Equal(t, apiProject, stringAttribute(t, state, "project_id"))
			})
		}
	}
}

func TestKubeOptionsDataSourcesRejectKubeVersionForm(t *testing.T) {
	var resp validator.StringResponse
	for _, rule := range dataSourceSchema(NewFeatureGatesDataSource()).Attributes["kube_version"].(schema.StringAttribute).Validators {
		rule.ValidateString(context.Background(), validator.StringRequest{
			Path: path.Root("kube_version"), ConfigValue: types.StringValue("v1.31"),
		}, &resp)
	}

	assertErrorContains(t, resp.Diagnostics, "must be in the x.y or x.y.z form")
}

func TestDataSourcesConnection(t *testing.T) {
	for name, create := range map[string]func() datasource.DataSource{
		"kubeconfig":            NewKubeconfigDataSource,
		"kube_versions":         NewKubeVersionsDataSource,
		"feature_gates":         NewFeatureGatesDataSource,
		"admission_controllers": NewAdmissionControllersDataSource,
	} {
		for _, tt := range []struct {
			name      string
			project   string
			clientErr error
			want      []string
		}{
			{name: "the project of the provider", project: apiProject},
			{name: "no project", want: []string{"Missing project ID"}},
			{
				name: "a failing client", project: apiProject, clientErr: errors.New("no such auth token"),
				want: []string{"Error initializing MKS client", "connect to Managed Kubernetes", "no such auth token"},
			},
		} {
			t.Run(name+"/"+tt.name, func(t *testing.T) {
				fake := newFakeAPI(t)
				fake.seedCluster(testClusterID)
				attrs := map[string]string{"pool": "ru-7"}
				if name == "kubeconfig" {
					attrs["cluster_id"] = testClusterID
				}

				config := &testProviderConfig{client: fake.client(), clientErr: tt.clientErr, project: tt.project}
				state, diags := readData(t, create(), config, attrs)

				if tt.want != nil {
					assertErrorContains(t, diags, tt.want...)

					return
				}
				require.False(t, diags.HasError(), "%v", diags)
				assert.Equal(t, keystoneProject, stringAttribute(t, state, "project_id"))
				assert.Equal(t, []string{keystoneProject}, config.gotProject)
				assert.Equal(t, []string{"ru-7"}, config.gotPool)
			})
		}
	}
}
