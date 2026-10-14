package mks

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/selectel/mks-go/v2/pkg/mksclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The IDs that the _v1 data sources return for the same lists, computed with
// their helpers in package selectel.
const (
	kubeVersionsID            = "59f3b70fc6380a9fa7e75ecab2ce46bd"
	featureGatesID            = "d43e1defe470fa3b027f4c054639f361"
	featureGates131ID         = "5883d8131ec2aa4a9cd6cbf4a3cd02c0"
	admissionControllersID    = "986b3fb2967b5bed3542ca287275edb0"
	admissionControllers130ID = "5f5b933841f6fa4d991f3bfb64ce67ea"
)

func TestKubeconfigLookup(t *testing.T) {
	for _, scenario := range []string{"found", "cluster not found", "kubeconfig error", "missing field"} {
		t.Run(scenario, func(t *testing.T) {
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v2/clusters/" + clusterID:
					if scenario == "cluster not found" {
						writeError(t, w, http.StatusNotFound, clusterID)
						return
					}
					writeJSON(t, w, http.StatusOK, mksclient.ClusterResp{Cluster: &mksclient.ClusterDetailed{Id: clusterID, Name: "tf-v2"}})
				case "/v2/clusters/" + clusterID + "/kubeconfig":
					if scenario == "kubeconfig error" {
						writeError(t, w, http.StatusInternalServerError, "")
						return
					}
					body := kubeconfig
					if scenario == "missing field" {
						body = strings.ReplaceAll(body, "server:", "address:")
					}
					w.Header().Set("Content-Type", "application/yaml")
					_, _ = w.Write([]byte(body))
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
				}
			})
			source := NewKubeconfigDataSource().(*kubeconfigDataSource)
			source.config = testConfig{client: client}
			response := read(t, source, dataConfig(t, source, kubeconfigDataModel{
				ProjectID: types.StringValue("project"),
				Pool:      types.StringValue(pool),
				ClusterID: types.StringValue(clusterID),
			}))
			wantError := map[string][2]string{
				"cluster not found": {"Read cluster", "Cluster not found"},
				"kubeconfig error":  {"Read kubeconfig", "fake error 500"},
				"missing field":     {"Parse kubeconfig", "unable to find server field in kubeconfig"},
			}[scenario]
			if wantError[0] != "" {
				requireError(t, response, wantError[0], wantError[1])
				return
			}
			require.False(t, response.Diagnostics.HasError(), "%v", response.Diagnostics)
			var state kubeconfigDataModel
			require.False(t, response.State.Get(context.Background(), &state).HasError())
			assert.Equal(t, clusterID, state.ID.ValueString())
			assert.Equal(t, "project", state.ProjectID.ValueString())
			assert.Equal(t, pool, state.Pool.ValueString())
			assert.Equal(t, kubeconfig, state.RawConfig.ValueString())
			assert.Equal(t, "https://192.0.2.10:6443", state.Server.ValueString())
			assert.Equal(t, "Y2EtZGF0YQ==", state.ClusterCACert.ValueString())
			assert.Equal(t, "Y2xpZW50LWNlcnQ=", state.ClientCert.ValueString())
			assert.Equal(t, "Y2xpZW50LWtleQ==", state.ClientKey.ValueString())
		})
	}
}

func TestKubeconfigSensitive(t *testing.T) {
	var response datasource.SchemaResponse
	NewKubeconfigDataSource().Schema(context.Background(), datasource.SchemaRequest{}, &response)
	for _, name := range []string{"raw_config", "server", "cluster_ca_cert", "client_cert", "client_key"} {
		assert.True(t, response.Schema.Attributes[name].IsSensitive(), "attribute %s is not Sensitive", name)
	}
}

func TestParseKubeconfigMissingField(t *testing.T) {
	for _, field := range []string{"certificate-authority-data", "server", "client-certificate-data", "client-key-data"} {
		t.Run(field, func(t *testing.T) {
			lines := strings.Split(kubeconfig, "\n")
			kept := lines[:0]
			for _, line := range lines {
				if !strings.Contains(line, field) {
					kept = append(kept, line)
				}
			}
			_, err := parseKubeconfig([]byte(strings.Join(kept, "\n")))
			require.Error(t, err)
			assert.Contains(t, err.Error(), field)
		})
	}
}

func TestKubeVersionsLookup(t *testing.T) {
	for _, scenario := range []string{"found", "list error", "invalid version"} {
		t.Run(scenario, func(t *testing.T) {
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "/v2/kubeversions", r.URL.Path)
				if scenario == "list error" {
					writeError(t, w, http.StatusInternalServerError, "")
					return
				}
				versions := []mksclient.KubeVersionInfo{
					{Version: new("1.29.5"), IsDefault: new(true)},
					{Version: new("1.31.2"), IsDefault: new(false)},
					{Version: new("1.30.1")},
				}
				if scenario == "invalid version" {
					versions = append(versions, mksclient.KubeVersionInfo{Version: new("1")})
				}
				writeJSON(t, w, http.StatusOK, mksclient.KubeVersionsList{KubeVersions: &versions})
			})
			source := NewKubeVersionsDataSource().(*kubeVersionsDataSource)
			source.config = testConfig{client: client}
			response := read(t, source, dataConfig(t, source, kubeVersionsDataModel{
				ProjectID: types.StringValue("project"),
				Pool:      types.StringValue(pool),
				Versions:  types.ListNull(types.StringType),
			}))
			if scenario == "list error" {
				requireError(t, response, "Read Kubernetes versions", "fake error 500")
				return
			}
			if scenario == "invalid version" {
				requireError(t, response, "Find the latest Kubernetes version", "kube version 1 is invalid")
				return
			}
			require.False(t, response.Diagnostics.HasError(), "%v", response.Diagnostics)
			var state kubeVersionsDataModel
			require.False(t, response.State.Get(context.Background(), &state).HasError())
			assert.Equal(t, kubeVersionsID, state.ID.ValueString())
			assert.Equal(t, "project", state.ProjectID.ValueString())
			assert.Equal(t, "1.31.2", state.LatestVersion.ValueString())
			assert.Equal(t, "1.29.5", state.DefaultVersion.ValueString())
			var versions []string
			require.False(t, state.Versions.ElementsAs(context.Background(), &versions, false).HasError())
			assert.Equal(t, []string{"1.29.5", "1.31.2", "1.30.1"}, versions)
		})
	}
}

func TestKubeOptionsLookup(t *testing.T) {
	featureGates := []mksclient.AvailableFeatureGates{
		{KubeVersionMinor: new("1.30"), Names: &[]string{"GracefulNodeShutdown", "TopologyManager"}},
		{KubeVersionMinor: new("1.31"), Names: &[]string{"TopologyManager", "SidecarContainers", "TopologyManager"}},
	}
	admissionControllers := []mksclient.AvailableAdmissionControllers{
		{KubeVersionMinor: new("1.30"), Names: &[]string{"NodeRestriction", "PodSecurity"}},
		{KubeVersionMinor: new("1.31"), Names: &[]string{"NodeRestriction", "PodSecurity", "AlwaysPullImages"}},
	}
	tests := []struct {
		name      string
		source    func() datasource.DataSource
		attribute string
		// filter is the kube_version of the filter block, nil without one.
		filter []string
		fail   bool
		wantID string
		// wantOptions maps a Kubernetes version to its deduplicated, sorted names.
		wantOptions map[string][]string
		wantError   [2]string
	}{
		{
			name: "feature gates without filter", source: NewFeatureGatesDataSource, attribute: "feature_gates",
			wantID: featureGatesID,
			wantOptions: map[string][]string{
				"1.30": {"GracefulNodeShutdown", "TopologyManager"},
				"1.31": {"SidecarContainers", "TopologyManager"},
			},
		},
		{
			name: "feature gates by patch version", source: NewFeatureGatesDataSource, attribute: "feature_gates",
			filter: []string{"1.31.2"}, wantID: featureGates131ID,
			wantOptions: map[string][]string{"1.31": {"SidecarContainers", "TopologyManager"}},
		},
		{
			name: "feature gates for unknown version", source: NewFeatureGatesDataSource, attribute: "feature_gates",
			filter:    []string{"1.20.0"},
			wantError: [2]string{"Filter feature gates", `available kubernetes options for kubernetes version "1.20" is not found`},
		},
		{
			name: "feature gates for empty version", source: NewFeatureGatesDataSource, attribute: "feature_gates",
			filter:    []string{""},
			wantError: [2]string{"Filter feature gates", "kubernetes version is not set"},
		},
		{
			name: "feature gates server error", source: NewFeatureGatesDataSource, attribute: "feature_gates",
			fail: true, wantError: [2]string{"Read feature gates", "fake error 500"},
		},
		{
			name: "admission controllers without filter", source: NewAdmissionControllersDataSource, attribute: "admission_controllers",
			wantID: admissionControllersID,
			wantOptions: map[string][]string{
				"1.30": {"NodeRestriction", "PodSecurity"},
				"1.31": {"AlwaysPullImages", "NodeRestriction", "PodSecurity"},
			},
		},
		{
			name: "admission controllers by minor version", source: NewAdmissionControllersDataSource, attribute: "admission_controllers",
			filter: []string{"1.30"}, wantID: admissionControllers130ID,
			wantOptions: map[string][]string{"1.30": {"NodeRestriction", "PodSecurity"}},
		},
		{
			name: "admission controllers server error", source: NewAdmissionControllersDataSource, attribute: "admission_controllers",
			fail: true, wantError: [2]string{"Read admission controllers", "fake error 500"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if tt.fail {
					writeError(t, w, http.StatusInternalServerError, "")
					return
				}
				switch r.URL.Path {
				case "/v2/feature-gates":
					writeJSON(t, w, http.StatusOK, mksclient.FeatureGatesList{FeatureGates: &featureGates})
				case "/v2/admission-controllers":
					writeJSON(t, w, http.StatusOK, mksclient.AdmissionControllersList{AdmissionControllers: &admissionControllers})
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
				}
			})
			source := tt.source().(*kubeOptionsDataSource)
			source.config = testConfig{client: client}
			response := read(t, source, kubeOptionsConfig(t, source, tt.filter))
			if tt.wantError[0] != "" {
				requireError(t, response, tt.wantError[0], tt.wantError[1])
				return
			}
			require.False(t, response.Diagnostics.HasError(), "%v", response.Diagnostics)
			var id types.String
			require.False(t, response.State.GetAttribute(context.Background(), path.Root("id"), &id).HasError())
			assert.Equal(t, tt.wantID, id.ValueString())
			var options []kubeOptionsModel
			require.False(t, response.State.GetAttribute(context.Background(), path.Root(tt.attribute), &options).HasError())
			got := map[string][]string{}
			for _, option := range options {
				var names []string
				require.False(t, option.Names.ElementsAs(context.Background(), &names, false).HasError())
				got[option.KubeVersion.ValueString()] = names
			}
			assert.Equal(t, tt.wantOptions, got)
		})
	}
}

func TestProjectID(t *testing.T) {
	const apiProject, keystoneProject = "35b965e8-8e59-4848-8eed-5e509833033c", "35b965e88e5948488eed5e509833033c"
	tests := []struct {
		name      string
		attribute types.String
		provider  string
		// wantState is the project_id in the state, wantClient the one the client is built for.
		wantState, wantClient string
	}{
		{name: "attribute", attribute: types.StringValue("attribute-project"), provider: "provider-project", wantState: "attribute-project", wantClient: "attributeproject"},
		{name: "provider", provider: "provider-project", wantState: "providerproject", wantClient: "providerproject"},
		{name: "provider in the API form", provider: apiProject, wantState: keystoneProject, wantClient: keystoneProject},
		{name: "empty attribute", attribute: types.StringValue(""), provider: "provider-project", wantState: "providerproject", wantClient: "providerproject"},
		{name: "missing"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(t, w, http.StatusOK, mksclient.KubeVersionsList{KubeVersions: &[]mksclient.KubeVersionInfo{}})
			})
			var gotProject, gotPool string
			source := NewKubeVersionsDataSource().(*kubeVersionsDataSource)
			source.config = testConfig{client: client, providerProject: tt.provider, gotProject: &gotProject, gotPool: &gotPool}
			response := read(t, source, dataConfig(t, source, kubeVersionsDataModel{
				ProjectID: tt.attribute,
				Pool:      types.StringValue(pool),
				Versions:  types.ListNull(types.StringType),
			}))
			if tt.wantState == "" {
				requireError(t, response, "Missing project ID", "Set project_id")
				assert.Empty(t, gotProject, "client built without a project")
				return
			}
			require.False(t, response.Diagnostics.HasError(), "%v", response.Diagnostics)
			var state kubeVersionsDataModel
			require.False(t, response.State.Get(context.Background(), &state).HasError())
			assert.Equal(t, tt.wantState, state.ProjectID.ValueString())
			assert.Equal(t, tt.wantClient, gotProject)
			assert.Equal(t, pool, gotPool)
		})
	}
}

func TestConnection(t *testing.T) {
	for _, scenario := range []string{"client error", "not configured", "invalid provider data"} {
		t.Run(scenario, func(t *testing.T) {
			source := NewKubeVersionsDataSource().(*kubeVersionsDataSource)
			if scenario == "client error" {
				source.config = testConfig{err: errors.New("catalog has no endpoint")}
			}
			if scenario == "invalid provider data" {
				var response datasource.ConfigureResponse
				source.Configure(context.Background(), datasource.ConfigureRequest{ProviderData: "config"}, &response)
				requireDiagnostic(t, response.Diagnostics.Errors(), "Invalid provider configuration", "Managed Kubernetes")

				return
			}
			response := read(t, source, dataConfig(t, source, kubeVersionsDataModel{
				ProjectID: types.StringValue("project"),
				Pool:      types.StringValue(pool),
				Versions:  types.ListNull(types.StringType),
			}))
			wantDetail := "provider is not configured"
			if scenario == "client error" {
				wantDetail = "connect to Managed Kubernetes (project_id=project, pool=ru-7): catalog has no endpoint"
			}
			requireError(t, response, "Connect to Managed Kubernetes", wantDetail)
		})
	}
	var response datasource.ConfigureResponse
	source := NewKubeVersionsDataSource().(*kubeVersionsDataSource)
	source.Configure(context.Background(), datasource.ConfigureRequest{}, &response)
	assert.False(t, response.Diagnostics.HasError(), "nil provider data before Configure")
}

// kubeOptionsConfig is the configuration of a feature gates or admission
// controllers data source, with a filter block for each of filter.
func kubeOptionsConfig(t *testing.T, source datasource.DataSource, filter []string) tfsdk.Config {
	t.Helper()
	ctx := context.Background()
	var response datasource.SchemaResponse
	source.Schema(ctx, datasource.SchemaRequest{}, &response)
	state := tfsdk.State{Schema: response.Schema, Raw: tftypes.NewValue(response.Schema.Type().TerraformType(ctx), nil)}
	filterType := response.Schema.Attributes["filter"].(schema.SetNestedAttribute).NestedObject.Type()
	filterSet := types.SetNull(filterType)
	if filter != nil {
		models := make([]kubeOptionsFilterModel, len(filter))
		for i, kubeVersion := range filter {
			models[i] = kubeOptionsFilterModel{KubeVersion: types.StringValue(kubeVersion)}
		}
		var d diag.Diagnostics
		filterSet, d = types.SetValueFrom(ctx, filterType, models)
		require.False(t, d.HasError(), "%v", d)
	}
	require.False(t, state.SetAttribute(ctx, path.Root("project_id"), "project").HasError())
	require.False(t, state.SetAttribute(ctx, path.Root("pool"), pool).HasError())
	require.False(t, state.SetAttribute(ctx, path.Root("filter"), filterSet).HasError())

	return tfsdk.Config{Schema: response.Schema, Raw: state.Raw}
}

func requireError(t *testing.T, response datasource.ReadResponse, summary, detail string) {
	t.Helper()
	assert.True(t, response.State.Raw.IsNull(), "state written despite the error")
	requireDiagnostic(t, response.Diagnostics.Errors(), summary, detail)
}

func requireDiagnostic(t *testing.T, errs diag.Diagnostics, summary, detail string) {
	t.Helper()
	require.Len(t, errs, 1, "%v", errs)
	assert.Equal(t, summary, errs[0].Summary())
	assert.Contains(t, errs[0].Detail(), detail)
}
