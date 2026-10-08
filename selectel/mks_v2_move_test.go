package selectel

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	ctyjson "github.com/hashicorp/go-cty/cty/json"
	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/resource"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/terraform"
	"github.com/selectel/mks-go/v2/pkg/mksclient"
)

const testMKSV2MoveProvider = "registry.terraform.io/selectel/selectel"

// testMKSClusterV1State is a selectel_mks_cluster_v1 state after a Read, with
// every attribute set.
func testMKSClusterV1State() map[string]any {
	return map[string]any{
		"name":                              "tf-v2",
		"project_id":                        "v1project",
		"region":                            testMKSV2Pool,
		"kube_version":                      "1.30.3",
		"enable_autorepair":                 false,
		"enable_patch_version_auto_upgrade": false,
		"enable_pod_security_policy":        true,
		"network_id":                        "net-1",
		"subnet_id":                         "subnet-1",
		"maintenance_window_start":          "01:00:00",
		"maintenance_window_end":            "03:00:00",
		"zonal":                             false,
		"cluster_type":                      "HIGH_AVAILABILITY_MULTI_AZ",
		"kube_api_ip":                       "192.0.2.10",
		"status":                            "ACTIVE",
		"feature_gates":                     []any{"TopologyAwareHints"},
		"admission_controllers":             []any{"NodeRestriction"},
		"private_kube_api":                  true,
		"cni_type":                          "CILIUM",
		"cni_cilium_settings":               []any{map[string]any{"envoy_daemonset": false, "hubble_relay": true}},
		"enable_audit_logs":                 true,
		"oidc": []any{map[string]any{
			"enabled": true, "provider_name": "keycloak", "issuer_url": "https://issuer.example", "client_id": "kube",
			"username_claim": "email", "groups_claim": "roles", "ca_certs": "cert",
		}},
	}
}

// testMKSClusterV2MovedState is what testMKSClusterV1State moves to.
func testMKSClusterV2MovedState() mksClusterV2Model {
	oidc := types.ObjectValueMust(mksClusterV2OIDCAttrTypes, map[string]attr.Value{
		"enabled": types.BoolValue(true), "provider_name": types.StringValue("keycloak"),
		"issuer_url": types.StringValue("https://issuer.example"), "client_id": types.StringValue("kube"),
		"username_claim": types.StringValue("email"), "groups_claim": types.StringValue("roles"),
		"ca_certs": mksV2TrimmedString("cert"),
	})

	return mksClusterV2Model{
		ID:                            types.StringValue(testMKSV2ClusterID),
		Name:                          types.StringValue("tf-v2"),
		Pool:                          types.StringValue(testMKSV2Pool),
		ProjectID:                     types.StringValue("v1project"),
		KubeVersion:                   types.StringValue("1.30.3"),
		ClusterType:                   types.StringValue("HIGH_AVAILABILITY_MULTI_AZ"),
		WorkersType:                   types.StringNull(),
		NetworkID:                     types.StringValue("net-1"),
		SubnetID:                      types.StringValue("subnet-1"),
		CloudSubnetCIDR:               types.StringNull(),
		PrivateKubeAPI:                types.BoolValue(true),
		MaintenanceWindowStart:        types.StringValue("01:00:00"),
		MaintenanceWindowEnd:          types.StringValue("03:00:00"),
		EnableAutorepair:              types.BoolValue(false),
		EnablePatchVersionAutoUpgrade: types.BoolValue(false),
		CNIType:                       types.StringValue("CILIUM"),
		CNICiliumSettings: types.ObjectValueMust(mksClusterV2CiliumAttrTypes, map[string]attr.Value{
			"envoy_daemonset": types.BoolValue(false), "hubble_relay": types.BoolValue(true),
		}),
		KubernetesOptions: types.ObjectValueMust(mksClusterV2KubeOptionsAttrTypes, map[string]attr.Value{
			"feature_gates":         types.SetValueMust(types.StringType, []attr.Value{types.StringValue("TopologyAwareHints")}),
			"admission_controllers": types.SetValueMust(types.StringType, []attr.Value{types.StringValue("NodeRestriction")}),
			"audit_logs": types.ObjectValueMust(mksClusterV2AuditLogsAttrTypes, map[string]attr.Value{
				"enabled": types.BoolValue(true), "secret_name": types.StringValue(""),
			}),
			"oidc":                 oidc,
			"x509_ca_certificates": types.StringNull(),
		}),
		Status:    types.StringValue("ACTIVE"),
		KubeAPIIP: types.StringValue("192.0.2.10"),
		Timeouts:  timeouts.Value{Object: types.ObjectNull(mksV2TimeoutsAttrTypes)},
	}
}

func TestMKSClusterV2MoveState(t *testing.T) {
	disabledOptions := types.ObjectValueMust(mksClusterV2KubeOptionsAttrTypes, map[string]attr.Value{
		"feature_gates":         types.SetValueMust(types.StringType, []attr.Value{}),
		"admission_controllers": types.SetValueMust(types.StringType, []attr.Value{}),
		"audit_logs": types.ObjectValueMust(mksClusterV2AuditLogsAttrTypes, map[string]attr.Value{
			"enabled": types.BoolValue(false), "secret_name": types.StringValue(""),
		}),
		"oidc": types.ObjectValueMust(mksClusterV2OIDCAttrTypes, map[string]attr.Value{
			"enabled": types.BoolValue(false), "provider_name": types.StringValue(""), "issuer_url": types.StringValue(""),
			"client_id": types.StringValue(""), "username_claim": types.StringValue(""), "groups_claim": types.StringValue(""),
			"ca_certs": mksV2TrimmedString(""),
		}),
		"x509_ca_certificates": types.StringNull(),
	})

	tests := []struct {
		name string
		v1   func(v map[string]any)
		want func(m *mksClusterV2Model)
	}{
		{
			name: "every attribute, cluster_type set",
			v1:   func(map[string]any) {},
			want: func(*mksClusterV2Model) {},
		},
		{
			name: "dashed upper-case project",
			v1:   func(v map[string]any) { v["project_id"] = strings.ToUpper(testMKSV2APIProject) },
			want: func(m *mksClusterV2Model) { m.ProjectID = types.StringValue(testMKSV2KeystoneProject) },
		},
		{
			name: "only zonal true",
			v1:   func(v map[string]any) { delete(v, "cluster_type"); v["zonal"] = true },
			want: func(m *mksClusterV2Model) { m.ClusterType = types.StringValue("BASIC") },
		},
		{
			name: "only zonal false",
			v1:   func(v map[string]any) { delete(v, "cluster_type") },
			want: func(m *mksClusterV2Model) { m.ClusterType = types.StringValue("HIGH_AVAILABILITY") },
		},
		{
			name: "lower-case cluster_type wins over zonal",
			v1:   func(v map[string]any) { v["cluster_type"] = "basic"; v["zonal"] = false },
			want: func(m *mksClusterV2Model) { m.ClusterType = types.StringValue("BASIC") },
		},
		{
			name: "neither cluster_type nor zonal",
			v1:   func(v map[string]any) { delete(v, "cluster_type"); delete(v, "zonal") },
			want: func(m *mksClusterV2Model) { m.ClusterType = types.StringNull() },
		},
		{
			name: "no oidc, no cilium, no options",
			v1: func(v map[string]any) {
				for _, key := range []string{"oidc", "cni_cilium_settings", "feature_gates", "admission_controllers"} {
					delete(v, key)
				}
				v["cni_type"] = "CALICO"
				v["enable_audit_logs"] = false
			},
			want: func(m *mksClusterV2Model) {
				m.CNIType = types.StringValue("CALICO")
				m.CNICiliumSettings = types.ObjectNull(mksClusterV2CiliumAttrTypes)
				m.KubernetesOptions = disabledOptions
			},
		},
	}

	r := &mksClusterV2Resource{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v1 := testMKSClusterV1State()
			tt.v1(v1)
			want := testMKSClusterV2MovedState()
			tt.want(&want)

			got := testMKSV2MoveState(t, r, r.MoveState(t.Context()),
				testMKSV1StateJSON(t, resourceMKSClusterV1(), testMKSV2ClusterID, v1), "selectel_mks_cluster_v1", testMKSV2MoveProvider)
			testMKSV2StateEquals(t, got, want)
		})
	}
}

// testMKSNodegroupV1State is a selectel_mks_nodegroup_v1 state after a Read,
// with every attribute set.
func testMKSNodegroupV1State() map[string]any {
	return map[string]any{
		"cluster_id":                   testMKSV2ClusterID,
		"project_id":                   "v1-project",
		"status":                       "ACTIVE",
		"region":                       testMKSV2Pool,
		"availability_zone":            "ru-7a",
		"nodes_count":                  2,
		"keypair_name":                 "ssh-key",
		"affinity_policy":              "soft-anti-affinity",
		"cpus":                         2,
		"ram_mb":                       4096,
		"volume_gb":                    20,
		"volume_type":                  "fast.ru-7a",
		"local_volume":                 false,
		"flavor_id":                    "fake-flavor",
		"labels":                       map[string]any{"env": "test"},
		"taints":                       []any{map[string]any{"key": "dedicated", "value": "gpu", "effect": "NoSchedule"}},
		"enable_autoscale":             true,
		"autoscale_min_nodes":          1,
		"autoscale_max_nodes":          3,
		"user_data":                    "IyEvYmluL2Jhc2g=",
		"install_nvidia_device_plugin": false,
		"preemptible":                  true,
		"nodegroup_type":               "STANDARD",
		"nodes": []any{
			map[string]any{"id": "node-1", "ip": "10.0.0.1", "hostname": "ng-1-node-1"},
			map[string]any{"id": "node-2", "ip": "10.0.0.2", "hostname": "ng-1-node-2"},
		},
	}
}

func testMKSNodegroupV2MovedState() mksNodegroupV2Model {
	taintType := types.ObjectType{AttrTypes: mksNodegroupV2TaintAttrTypes}
	nodeType := types.ObjectType{AttrTypes: mksNodegroupV2NodeAttrTypes}
	node := func(n string) attr.Value {
		return types.ObjectValueMust(mksNodegroupV2NodeAttrTypes, map[string]attr.Value{
			"id": types.StringValue("node-" + n), "ip": types.StringValue("10.0.0." + n), "hostname": types.StringValue("ng-1-node-" + n),
		})
	}

	return mksNodegroupV2Model{
		ID:        types.StringValue(testMKSNodegroupV2ID),
		ClusterID: types.StringValue(testMKSV2ClusterID),
		Segment:   types.StringValue("ru-7a"),
		Count:     types.Int64Value(2),
		CIDR:      types.StringNull(),
		Labels:    types.MapValueMust(types.StringType, map[string]attr.Value{"env": types.StringValue("test")}),
		Taints: types.ListValueMust(taintType, []attr.Value{types.ObjectValueMust(mksNodegroupV2TaintAttrTypes, map[string]attr.Value{
			"key": types.StringValue("dedicated"), "value": types.StringValue("gpu"), "effect": types.StringValue("NoSchedule"),
		})}),
		EnableAutoscale:           types.BoolValue(true),
		AutoscaleMinNodes:         types.Int64Value(1),
		AutoscaleMaxNodes:         types.Int64Value(3),
		UserData:                  types.StringValue("IyEvYmluL2Jhc2g="),
		InstallNvidiaDevicePlugin: types.BoolValue(false),
		Preemptible:               types.BoolValue(true),
		CloudNodegroupConfig: types.ObjectValueMust(mksNodegroupV2CloudConfigAttrTypes, map[string]attr.Value{
			"flavor_id": types.StringValue("fake-flavor"), "cpus": types.Int64Value(2), "ram_mb": types.Int64Value(4096),
			"volume_gb": types.Int64Value(20), "volume_type": types.StringValue("fast.ru-7a"), "local_volume": types.BoolValue(false),
			"affinity_policy": types.StringValue("soft-anti-affinity"),
		}),
		DedicatedNodegroupConfig: types.ObjectNull(mksNodegroupV2DedicatedConfigAttrTypes),
		NodegroupType:            types.StringValue("STANDARD"),
		Status:                   types.StringValue("ACTIVE"),
		Nodes:                    types.ListValueMust(nodeType, []attr.Value{node("1"), node("2")}),
		Timeouts:                 timeouts.Value{Object: types.ObjectNull(mksV2TimeoutsAttrTypes)},
	}
}

func TestMKSNodegroupV2MoveState(t *testing.T) {
	cloudConfig := func(flavorID string, cpus, ramMB types.Int64, affinityPolicy types.String) types.Object {
		return types.ObjectValueMust(mksNodegroupV2CloudConfigAttrTypes, map[string]attr.Value{
			"flavor_id": types.StringValue(flavorID), "cpus": cpus, "ram_mb": ramMB,
			"volume_gb": types.Int64Value(20), "volume_type": types.StringValue("fast.ru-7a"), "local_volume": types.BoolValue(false),
			"affinity_policy": affinityPolicy,
		})
	}

	tests := []struct {
		name string
		v1   func(v map[string]any)
		want func(m *mksNodegroupV2Model)
	}{
		{
			name: "every attribute, cpus and ram_mb, taints",
			v1:   func(map[string]any) {},
			want: func(*mksNodegroupV2Model) {},
		},
		{
			name: "flavor_id with zero cpus and ram_mb, no affinity policy",
			v1: func(v map[string]any) {
				v["flavor_id"], v["cpus"], v["ram_mb"], v["affinity_policy"] = "1013", 0, 0, ""
			},
			want: func(m *mksNodegroupV2Model) {
				m.CloudNodegroupConfig = cloudConfig("1013", types.Int64Null(), types.Int64Null(), types.StringNull())
			},
		},
		{
			name: "flavor_id with unset cpus and ram_mb",
			v1: func(v map[string]any) {
				v["flavor_id"] = "1013"
				delete(v, "cpus")
				delete(v, "ram_mb")
			},
			want: func(m *mksNodegroupV2Model) {
				m.CloudNodegroupConfig = cloudConfig("1013", types.Int64Null(), types.Int64Null(), types.StringValue("soft-anti-affinity"))
			},
		},
		{
			name: "no taints, no labels",
			v1: func(v map[string]any) {
				delete(v, "taints")
				delete(v, "labels")
			},
			want: func(m *mksNodegroupV2Model) {
				m.Taints = types.ListNull(types.ObjectType{AttrTypes: mksNodegroupV2TaintAttrTypes})
				m.Labels = types.MapNull(types.StringType)
			},
		},
	}

	r := &mksNodegroupV2Resource{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v1 := testMKSNodegroupV1State()
			tt.v1(v1)
			want := testMKSNodegroupV2MovedState()
			tt.want(&want)

			got := testMKSV2MoveState(t, r, r.MoveState(t.Context()),
				testMKSV1StateJSON(t, resourceMKSNodegroupV1(), testMKSNodegroupV2ID, v1), "selectel_mks_nodegroup_v1", testMKSV2MoveProvider)
			testMKSV2StateEquals(t, got, want)
		})
	}
}

// TestMKSV2MoveStateOtherSources leaves the state null for a source that is
// not the matching _v1 resource of this provider; the framework then reports
// that the move is not supported.
func TestMKSV2MoveStateOtherSources(t *testing.T) {
	cluster, nodegroup := &mksClusterV2Resource{}, &mksNodegroupV2Resource{}
	clusterJSON := testMKSV1StateJSON(t, resourceMKSClusterV1(), testMKSV2ClusterID, testMKSClusterV1State())
	nodegroupJSON := testMKSV1StateJSON(t, resourceMKSNodegroupV1(), testMKSNodegroupV2ID, testMKSNodegroupV1State())

	tests := []struct {
		name       string
		target     fwresource.ResourceWithMoveState
		state      []byte
		sourceType string
		provider   string
		moved      bool
	}{
		{"cluster from dev_overrides address", cluster, clusterJSON, "selectel_mks_cluster_v1", "terraform.local/local/selectel", true},
		{"cluster from another provider", cluster, clusterJSON, "selectel_mks_cluster_v1", "registry.terraform.io/hashicorp/aws", false},
		{"cluster from another resource type", cluster, nodegroupJSON, "selectel_mks_nodegroup_v1", testMKSV2MoveProvider, false},
		{"nodegroup from dev_overrides address", nodegroup, nodegroupJSON, "selectel_mks_nodegroup_v1", "terraform.local/local/selectel", true},
		{"nodegroup from a provider named like selectel", nodegroup, nodegroupJSON, "selectel_mks_nodegroup_v1", "example.com/x/notselectel", false},
		{"nodegroup from another resource type", nodegroup, clusterJSON, "selectel_mks_cluster_v1", testMKSV2MoveProvider, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := testMKSV2MoveState(t, tt.target, tt.target.MoveState(t.Context()), tt.state, tt.sourceType, tt.provider)
			if got.Raw.IsNull() == tt.moved {
				t.Errorf("moved state is null: %t, want moved: %t", got.Raw.IsNull(), tt.moved)
			}
		})
	}
}

// testMKSV1StateJSON writes values into the SDKv2 schema of a _v1 resource
// and encodes the state the way Terraform stores it.
func testMKSV1StateJSON(t *testing.T, r *schema.Resource, id string, values map[string]any) []byte {
	t.Helper()

	d := r.TestResourceData()
	d.SetId(id)
	for key, value := range values {
		err := d.Set(key, value)
		if err != nil {
			t.Fatalf("setting %s: %v", key, err)
		}
	}
	ty := r.CoreConfigSchema().ImpliedType()
	value, err := d.State().AttrsAsObjectValue(ty)
	if err != nil {
		t.Fatalf("converting the state: %v", err)
	}
	raw, err := ctyjson.Marshal(value, ty)
	if err != nil {
		t.Fatalf("encoding the state: %v", err)
	}

	return raw
}

// testMKSV2MoveState runs the movers like the framework does: the first one
// that returns a non-null state wins.
func testMKSV2MoveState(t *testing.T, r fwresource.Resource, movers []fwresource.StateMover, raw []byte, sourceType, provider string) tfsdk.State {
	t.Helper()

	ctx := t.Context()
	var schemaResp fwresource.SchemaResponse
	r.Schema(ctx, fwresource.SchemaRequest{}, &schemaResp)

	for _, mover := range movers {
		resp := fwresource.MoveStateResponse{TargetState: tfsdk.State{
			Schema: schemaResp.Schema,
			Raw:    tftypes.NewValue(schemaResp.Schema.Type().TerraformType(ctx), nil),
		}}
		mover.StateMover(ctx, fwresource.MoveStateRequest{
			SourceProviderAddress: provider,
			SourceTypeName:        sourceType,
			SourceRawState:        &tfprotov6.RawState{JSON: raw},
		}, &resp)
		if resp.Diagnostics.HasError() {
			t.Fatalf("move failed: %v", resp.Diagnostics)
		}
		if !resp.TargetState.Raw.IsNull() {
			return resp.TargetState
		}
	}

	return tfsdk.State{Schema: schemaResp.Schema, Raw: tftypes.NewValue(schemaResp.Schema.Type().TerraformType(ctx), nil)}
}

func testMKSV2StateEquals(t *testing.T, got tfsdk.State, model any) {
	t.Helper()

	want := tfsdk.State{Schema: got.Schema, Raw: tftypes.NewValue(got.Schema.Type().TerraformType(t.Context()), nil)}
	diags := want.Set(t.Context(), model)
	if diags.HasError() {
		t.Fatalf("building the expected state: %v", diags)
	}
	if !got.Raw.Equal(want.Raw) {
		diffs, _ := got.Raw.Diff(want.Raw)
		for _, d := range diffs {
			t.Errorf("%s: got %v, want %v", d.Path, d.Value1, d.Value2)
		}
	}
}

// testMKSV2TerraformAtLeast skips the test when the terraform binary the
// tests run is older than version (major.minor).
func testMKSV2TerraformAtLeast(t *testing.T, major, minor int) {
	t.Helper()

	binary := os.Getenv("TF_ACC_TERRAFORM_PATH")
	if binary == "" {
		binary = "terraform"
	}
	// The binary the SDKv2 harness runs, see TF_ACC_TERRAFORM_PATH.
	out, err := exec.CommandContext(t.Context(), binary, "version", "-json").Output() //nolint:gosec
	if err != nil {
		t.Fatalf("terraform version: %v", err)
	}
	var version struct {
		TerraformVersion string `json:"terraform_version"`
	}
	err = json.Unmarshal(out, &version)
	if err != nil {
		t.Fatalf("terraform version: %v", err)
	}
	var gotMajor, gotMinor int
	_, err = fmt.Sscanf(version.TerraformVersion, "%d.%d", &gotMajor, &gotMinor)
	if err != nil {
		t.Fatalf("terraform version %q: %v", version.TerraformVersion, err)
	}
	if gotMajor < major || gotMajor == major && gotMinor < minor {
		t.Skipf("terraform %s is older than %d.%d", version.TerraformVersion, major, minor)
	}
}

// testMKSV2SeedV1State returns a PreConfig that writes a Terraform state
// holding one _v1 instance into the working directory of the test, as for a
// user who has applied _v1. The SDKv2 harness has no option to seed state; it
// creates the working directory under TF_ACC_TEMP_DIR, which this sets.
func testMKSV2SeedV1State(t *testing.T, resourceType, name string, attributes []byte) func() {
	t.Helper()

	tempDir := t.TempDir()
	t.Setenv("TF_ACC_TEMP_DIR", tempDir)

	state := map[string]any{
		"version":           4,
		"terraform_version": "1.8.0",
		"serial":            1,
		"lineage":           "mks-v2-move-test",
		"outputs":           map[string]any{},
		"resources": []any{map[string]any{
			"mode":     "managed",
			"type":     resourceType,
			"name":     name,
			"provider": `provider["registry.terraform.io/hashicorp/selectel"]`,
			"instances": []any{map[string]any{
				"schema_version":       0,
				"attributes":           json.RawMessage(attributes),
				"sensitive_attributes": []any{},
			}},
		}},
	}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("encoding the state file: %v", err)
	}

	return func() {
		dirs, err := filepath.Glob(filepath.Join(tempDir, "plugintest*", "work*"))
		if err != nil || len(dirs) != 1 {
			t.Fatalf("found working directories %v (%v), want one", dirs, err)
		}
		err = os.WriteFile(filepath.Join(dirs[0], "terraform.tfstate"), raw, 0o600)
		if err != nil {
			t.Fatalf("writing the state file: %v", err)
		}
	}
}

// TestMKSClusterV2ResourceMovedFromV1 runs a moved block on a _v1 state and
// the _v2 configuration of the same cluster: the move changes nothing in the
// cluster, and the plans before and after it are empty.
func TestMKSClusterV2ResourceMovedFromV1(t *testing.T) {
	testMKSV2TerraformAtLeast(t, 1, 8)

	tests := []struct {
		name string
		// version is the one of the cluster and the _v1 state, which stores
		// what the API reports.
		version string
		// configName and configVersion are the _v2 configuration. The
		// upper-case case covers the plain move as well.
		configName, configVersion string
		// oidc is the cluster OIDC, which the _v1 state stores as well;
		// configOIDC is the _v2 oidc object body, none when empty.
		oidc       mksclient.OIDC
		configOIDC string
	}{
		{name: "patch auto-upgraded past the configuration", version: "1.30.5", configName: "tf-v2", configVersion: "1.30.3"},
		{name: "lower minor configured", version: "1.30.5", configName: "tf-v2", configVersion: "1.29.8"},
		{name: "upper-case name configured", version: "1.30.3", configName: "TF-v2", configVersion: "1.30.3"},
		{
			name: "OIDC disabled in the configuration", version: "1.30.3", configName: "tf-v2", configVersion: "1.30.3",
			configOIDC: `
      enabled = false`,
		},
		{
			// mk-api V1 stores the same claim defaults as mk-api-v2.
			name: "OIDC enabled with the claims omitted", version: "1.30.3", configName: "tf-v2", configVersion: "1.30.3",
			oidc: mksclient.OIDC{
				Enabled: true, ProviderName: "keycloak", IssuerUrl: "https://issuer.example.com", ClientId: "kubernetes",
				UsernameClaim: "sub", GroupsClaim: "groups",
			},
			configOIDC: testMKSClusterV2EnabledOIDC,
		},
		{
			name: "OIDC enabled with the claims configured", version: "1.30.3", configName: "tf-v2", configVersion: "1.30.3",
			oidc: mksclient.OIDC{
				Enabled: true, ProviderName: "keycloak", IssuerUrl: "https://issuer.example.com", ClientId: "kubernetes",
				UsernameClaim: "email", GroupsClaim: "roles",
			},
			configOIDC: testMKSClusterV2EnabledOIDC + `
      username_claim = "email"
      groups_claim   = "roles"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// mk-api-v2 returns the project dashed, while mk-api V1 and so
			// the _v1 state have it without dashes.
			fake := newMKSV2Fake(t)
			fake.seedCluster(mksclient.ClusterDetailed{
				Id: testMKSV2ClusterID, Name: "tf-v2", Pool: testMKSV2Pool, ProjectId: testMKSV2APIProject, KubeVersion: tt.version,
				ClusterType: mksclient.HIGHAVAILABILITY, NetworkType: mksclient.ClusterDetailedNetworkTypeSTANDARD,
				NetworkId: "net-1", SubnetId: "subnet-1", EnableAutorepair: true, EnablePatchVersionAutoUpgrade: true,
				MaintenanceWindowStart: "01:00:00", MaintenanceWindowEnd: "03:00:00",
				CniType: mksclient.ClusterDetailedCniType(mksclient.ClusterCniTypeCALICO), KubeApiIp: "192.0.2.10", Status: "ACTIVE",
				KubernetesOptions: mksclient.KubernetesOptions{
					FeatureGates: []string{"TopologyAwareHints"}, AuditLogs: mksclient.AuditLogs{Enabled: true},
					Oidc: tt.oidc,
				},
			}, "")

			// The state _v1 left after a refresh of that cluster: zonal and
			// no cluster_type, an OIDC block like the cluster one.
			v1 := map[string]any{
				"name": "tf-v2", "project_id": testMKSV2KeystoneProject, "region": testMKSV2Pool, "kube_version": tt.version,
				"enable_autorepair": true, "enable_patch_version_auto_upgrade": true, "enable_pod_security_policy": false,
				"network_id": "net-1", "subnet_id": "subnet-1", "maintenance_window_start": "01:00:00", "maintenance_window_end": "03:00:00",
				"zonal": false, "kube_api_ip": "192.0.2.10", "status": "ACTIVE", "feature_gates": []any{"TopologyAwareHints"},
				"admission_controllers": []any{}, "private_kube_api": false, "cni_type": "CALICO", "enable_audit_logs": true,
				"oidc": []any{map[string]any{"enabled": false}},
			}
			if tt.oidc.Enabled {
				v1["oidc"] = []any{map[string]any{
					"enabled": true, "provider_name": tt.oidc.ProviderName, "issuer_url": tt.oidc.IssuerUrl,
					"client_id": tt.oidc.ClientId, "username_claim": tt.oidc.UsernameClaim, "groups_claim": tt.oidc.GroupsClaim,
				}}
			}
			oidcConfig := ""
			if tt.configOIDC != "" {
				oidcConfig = "    oidc = {" + tt.configOIDC + "\n    }\n"
			}
			seedState := testMKSV2SeedV1State(t, "selectel_mks_cluster_v1", "cluster_tf_test_1",
				testMKSV1StateJSON(t, resourceMKSClusterV1(), testMKSV2ClusterID, v1))

			config := `
moved {
  from = selectel_mks_cluster_v1.cluster_tf_test_1
  to   = selectel_mks_cluster_v2.cluster_tf_test_1
}
` + strings.Replace(testMKSClusterV2Config(testMKSClusterV2ProviderConfig(testMKSV2KeystoneProject, testMKSV2Pool), fmt.Sprintf(`
  kube_version             = %q
  workers_type             = "CLOUD"
  maintenance_window_start = "01:00:00"
  kubernetes_options = {
    feature_gates = ["TopologyAwareHints"]
    audit_logs = {
      enabled = true
    }
%s  }
`, tt.configVersion, oidcConfig)), `name = "tf-v2"`, fmt.Sprintf(`name = %q`, tt.configName), 1)

			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: fake.providerFactories(),
				CheckDestroy:             testMKSClusterV2Destroyed(fake),
				Steps: []resource.TestStep{
					{
						// The first plan after the move is empty.
						PreConfig: seedState,
						Config:    config,
						PlanOnly:  true,
					},
					{
						Config: config,
						Check: resource.ComposeTestCheckFunc(
							resource.TestCheckResourceAttr(testMKSClusterV2Name, "id", testMKSV2ClusterID),
							resource.TestCheckResourceAttr(testMKSClusterV2Name, "name", "tf-v2"),
							resource.TestCheckResourceAttr(testMKSClusterV2Name, "kube_version", tt.version),
							resource.TestCheckResourceAttr(testMKSClusterV2Name, "cluster_type", "HIGH_AVAILABILITY"),
							resource.TestCheckResourceAttr(testMKSClusterV2Name, "workers_type", "CLOUD"),
							resource.TestCheckResourceAttr(testMKSClusterV2Name, "project_id", testMKSV2KeystoneProject),
							testMKSV2NoResource("selectel_mks_cluster_v1.cluster_tf_test_1"),
							testMKSClusterV2Calls(fake, map[string]int{
								mksV2RouteCreateCluster: 0, mksV2RoutePatchCluster: 0, mksV2RouteDeleteCluster: 0,
								mksV2RouteUpgradePatch: 0, mksV2RouteUpgradeMinor: 0,
							}),
						),
					},
				},
			})

			fake.checkClients(t, testMKSV2KeystoneProject)
		})
	}
}

// TestMKSNodegroupV2ResourceMovedFromV1 is the same for a node group.
func TestMKSNodegroupV2ResourceMovedFromV1(t *testing.T) {
	testMKSV2TerraformAtLeast(t, 1, 8)
	fake := newMKSV2Fake(t)
	testMKSNodegroupV2SeedCluster(fake, mksclient.ClusterDetailedNetworkTypeSTANDARD)
	fake.seedNodegroup(mksclient.NodegroupDetailed{
		Id: "ng-1", ClusterId: testMKSV2ClusterID, Segment: "ru-7a", Status: "ACTIVE", NodegroupType: "STANDARD",
		Nodes:                mksV2FakeNodes("ng-1", 2),
		Labels:               map[string]string{"env": "test"},
		CloudNodegroupConfig: &mksclient.CloudNodegroupConfigInfo{FlavorId: "1013", VolumeGb: 20, VolumeType: "fast.ru-7a"},
	})

	nodes := make([]any, 0, 2)
	for _, n := range mksV2FakeNodes("ng-1", 2) {
		nodes = append(nodes, map[string]any{"id": n.Id, "ip": n.Ip, "hostname": n.Hostname})
	}
	v1 := map[string]any{
		"cluster_id": testMKSV2ClusterID, "project_id": "provider-project", "status": "ACTIVE", "region": testMKSV2Pool,
		"availability_zone": "ru-7a", "nodes_count": 2, "keypair_name": "ssh-key", "cpus": 0, "ram_mb": 0, "volume_gb": 20,
		"volume_type": "fast.ru-7a", "local_volume": false, "flavor_id": "1013", "labels": map[string]any{"env": "test"},
		"enable_autoscale": false, "autoscale_min_nodes": 0, "autoscale_max_nodes": 0, "user_data": "",
		"install_nvidia_device_plugin": false, "preemptible": false, "nodegroup_type": "STANDARD", "nodes": nodes,
	}
	seedState := testMKSV2SeedV1State(t, "selectel_mks_nodegroup_v1", "nodegroup_tf_test_1",
		testMKSV1StateJSON(t, resourceMKSNodegroupV1(), testMKSNodegroupV2ID, v1))

	config := `
moved {
  from = selectel_mks_nodegroup_v1.nodegroup_tf_test_1
  to   = selectel_mks_nodegroup_v2.nodegroup_tf_test_1
}
` + testMKSNodegroupV2Config(`
  nodes_count                  = 2
  install_nvidia_device_plugin = false
  labels = {
    env = "test"
  }
  cloud_nodegroup_config = {
    flavor_id   = "1013"
    volume_gb   = 20
    volume_type = "fast.ru-7a"
  }
`)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: fake.providerFactories(),
		CheckDestroy:             testMKSNodegroupV2Destroyed(fake, "ng-1"),
		Steps: []resource.TestStep{
			{
				// The first plan after the move is empty.
				PreConfig: seedState,
				Config:    config,
				PlanOnly:  true,
			},
			{
				Config: config,
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(testMKSNodegroupV2Name, "id", testMKSNodegroupV2ID),
					resource.TestCheckResourceAttr(testMKSNodegroupV2Name, "segment", "ru-7a"),
					resource.TestCheckNoResourceAttr(testMKSNodegroupV2Name, "cloud_nodegroup_config.cpus"),
					testMKSV2NoResource("selectel_mks_nodegroup_v1.nodegroup_tf_test_1"),
					testMKSClusterV2Calls(fake, map[string]int{
						mksV2RouteCreateNodegroups: 0, mksV2RoutePatchNodegroup: 0, mksV2RouteDeleteNodegroup: 0,
						mksV2RouteResizeNodegroup: 0,
					}),
				),
			},
		},
	})
}

// TestMKSNodegroupV2ResourceMovedFromV1OtherProject moves a node group whose
// _v1 project_id is not the provider project: mk-api-v2 answers 404 for it,
// which must fail the plan instead of planning a new node group.
func TestMKSNodegroupV2ResourceMovedFromV1OtherProject(t *testing.T) {
	testMKSV2TerraformAtLeast(t, 1, 8)
	fake := newMKSV2Fake(t)
	testMKSNodegroupV2SeedCluster(fake, mksclient.ClusterDetailedNetworkTypeSTANDARD)
	fake.seedNodegroup(mksclient.NodegroupDetailed{
		Id: "ng-1", ClusterId: testMKSV2ClusterID, Segment: "ru-7a", Status: "ACTIVE", NodegroupType: "STANDARD",
		Nodes:                mksV2FakeNodes("ng-1", 1),
		CloudNodegroupConfig: &mksclient.CloudNodegroupConfigInfo{FlavorId: "1013", VolumeGb: 20, VolumeType: "fast.ru-7a"},
	})
	// The fake has no projects: the 404 stands in for the cluster of
	// another project.
	fake.fail(mksV2RouteNodegroup, http.StatusNotFound)

	v1 := map[string]any{
		"cluster_id": testMKSV2ClusterID, "project_id": "other-project", "status": "ACTIVE", "region": testMKSV2Pool,
		"availability_zone": "ru-7a", "nodes_count": 1, "cpus": 0, "ram_mb": 0, "volume_gb": 20,
		"volume_type": "fast.ru-7a", "local_volume": false, "flavor_id": "1013",
		"enable_autoscale": false, "autoscale_min_nodes": 0, "autoscale_max_nodes": 0, "user_data": "",
		"install_nvidia_device_plugin": false, "preemptible": false, "nodegroup_type": "STANDARD",
	}
	seedState := testMKSV2SeedV1State(t, "selectel_mks_nodegroup_v1", "nodegroup_tf_test_1",
		testMKSV1StateJSON(t, resourceMKSNodegroupV1(), testMKSNodegroupV2ID, v1))

	config := `
moved {
  from = selectel_mks_nodegroup_v1.nodegroup_tf_test_1
  to   = selectel_mks_nodegroup_v2.nodegroup_tf_test_1
}
` + testMKSNodegroupV2Config(`
  nodes_count = 1
  cloud_nodegroup_config = {
    flavor_id   = "1013"
    volume_gb   = 20
    volume_type = "fast.ru-7a"
  }
`)

	resource.UnitTest(t, resource.TestCase{
		// The last step leaves nothing in the state to destroy.
		ProtoV6ProviderFactories: fake.providerFactories(),
		Steps: []resource.TestStep{
			{
				PreConfig: seedState,
				Config:    config,
				PlanOnly:  true,
				ExpectError: testMKSClusterV2Error(`provider project "provider-project" is not the node group's project ` +
					`"other-project" \(moved from _v1\): set project_id of the provider to "other-project"`),
			},
			{
				// The move goes through once the node group is found, and
				// its first read clears the mark.
				PreConfig: func() { fake.fail(mksV2RouteNodegroup, 0) },
				Config:    config,
				Check:     testMKSClusterV2Calls(fake, map[string]int{mksV2RouteCreateNodegroups: 0}),
			},
			{
				// A 404 then means the node group is gone: the refresh drops
				// it and the plan creates one.
				PreConfig:          func() { fake.fail(mksV2RouteNodegroup, http.StatusNotFound) },
				Config:             config,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

// TestMKSClusterV2ResourceImportByIdentity imports with an import block that
// carries the identity: project and pool come from it, not from the provider.
// The state takes the identity project without dashes, like _v1, and a
// configuration of either form matches it.
func TestMKSClusterV2ResourceImportByIdentity(t *testing.T) {
	testMKSV2TerraformAtLeast(t, 1, 12)
	t.Parallel()

	tests := []struct {
		name                           string
		identityProject, configProject string
	}{
		{name: "same project", identityProject: "identity-project", configProject: "identity-project"},
		{name: "dashed identity project", identityProject: testMKSV2APIProject, configProject: testMKSV2KeystoneProject},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fake := newMKSV2Fake(t)
			fake.seedCluster(mksclient.ClusterDetailed{
				Id: testMKSV2ClusterID, Name: "tf-v2", Pool: testMKSV2Pool, ProjectId: tt.identityProject, KubeVersion: "1.30.3",
				ClusterType: mksclient.HIGHAVAILABILITY, NetworkType: mksclient.ClusterDetailedNetworkTypeSTANDARD,
				NetworkId: "net-1", SubnetId: "subnet-1", EnableAutorepair: true, EnablePatchVersionAutoUpgrade: true,
				MaintenanceWindowStart: "01:00:00", MaintenanceWindowEnd: "03:00:00",
				CniType: mksclient.ClusterDetailedCniType(mksclient.ClusterCniTypeCALICO), KubeApiIp: "192.0.2.10", Status: "ACTIVE",
			}, "")

			config := fmt.Sprintf(`
import {
  to = %s
  identity = {
    id         = %q
    project_id = %q
    pool       = %q
  }
}
`, testMKSClusterV2Name, testMKSV2ClusterID, tt.identityProject, testMKSV2Pool) + testMKSClusterV2Config("", fmt.Sprintf(`
  project_id   = %q
  kube_version = "1.30.3"
  workers_type = "CLOUD"
`, tt.configProject))

			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: fake.providerFactories(),
				CheckDestroy:             testMKSClusterV2Destroyed(fake),
				Steps: []resource.TestStep{
					{
						Config: config,
						Check: resource.ComposeTestCheckFunc(
							resource.TestCheckResourceAttr(testMKSClusterV2Name, "id", testMKSV2ClusterID),
							resource.TestCheckResourceAttr(testMKSClusterV2Name, "project_id", mksV2KeystoneProjectID(tt.identityProject)),
							resource.TestCheckResourceAttr(testMKSClusterV2Name, "pool", testMKSV2Pool),
							resource.TestCheckResourceAttr(testMKSClusterV2Name, "maintenance_window_start", "01:00:00"),
							testMKSClusterV2Calls(fake, map[string]int{mksV2RouteCreateCluster: 0, mksV2RoutePatchCluster: 0}),
						),
					},
					{
						Config:   config,
						PlanOnly: true,
					},
				},
			})

			fake.checkClients(t, mksV2KeystoneProjectID(tt.identityProject))
		})
	}
}

// TestMKSNodegroupV2ResourceImportByIdentity imports with the identity: the
// pool comes from it, the project from the provider.
func TestMKSNodegroupV2ResourceImportByIdentity(t *testing.T) {
	testMKSV2TerraformAtLeast(t, 1, 12)
	t.Parallel()
	fake := newMKSV2Fake(t)
	testMKSNodegroupV2SeedCluster(fake, mksclient.ClusterDetailedNetworkTypeSTANDARD)
	fake.seedNodegroup(mksclient.NodegroupDetailed{
		Id: "ng-1", ClusterId: testMKSV2ClusterID, Segment: "ru-7a", Status: "ACTIVE",
		Nodes:                mksV2FakeNodes("ng-1", 1),
		CloudNodegroupConfig: &mksclient.CloudNodegroupConfigInfo{FlavorId: "1013", VolumeGb: 20, VolumeType: "fast.ru-7a"},
	})

	config := fmt.Sprintf(`
import {
  to = %s
  identity = {
    cluster_id = %q
    id         = "ng-1"
    pool       = %q
  }
}
`, testMKSNodegroupV2Name, testMKSV2ClusterID, testMKSV2Pool) + testMKSNodegroupV2Config(testMKSNodegroupV2Flavor)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: fake.providerFactories(),
		CheckDestroy:             testMKSNodegroupV2Destroyed(fake, "ng-1"),
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(testMKSNodegroupV2Name, "id", testMKSNodegroupV2ID),
					resource.TestCheckResourceAttr(testMKSNodegroupV2Name, "cluster_id", testMKSV2ClusterID),
					resource.TestCheckResourceAttr(testMKSNodegroupV2Name, "segment", "ru-7a"),
					testMKSClusterV2Calls(fake, map[string]int{mksV2RouteCreateNodegroups: 0, mksV2RouteDeleteNodegroup: 0}),
				),
			},
		},
	})

	testMKSNodegroupV2NoCreate(t, fake)
	fake.checkClients(t, "provider-project")
}

// testMKSV2NoResource checks that the state has no resource at address.
func testMKSV2NoResource(address string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		_, ok := s.RootModule().Resources[address]
		if ok {
			return fmt.Errorf("%s is still in the state", address)
		}

		return nil
	}
}
