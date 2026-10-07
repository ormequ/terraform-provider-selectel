package selectel

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/selectel/mks-go/v2/pkg/mksclient"
)

// The _v2 resources take over the state of the _v1 ones of this provider
// through a moved block (Terraform 1.8 and later). The mapping reads only the
// _v1 state: Read fills what _v1 never stored on the next refresh.

// mksClusterV1MoveState is the JSON shape of a selectel_mks_cluster_v1 state,
// written by SDKv2 from resourceMKSClusterV1. A pointer is nil for null.
type mksClusterV1MoveState struct {
	ID                            *string  `json:"id"`
	Name                          *string  `json:"name"`
	ProjectID                     *string  `json:"project_id"`
	Region                        *string  `json:"region"`
	KubeVersion                   *string  `json:"kube_version"`
	EnableAutorepair              *bool    `json:"enable_autorepair"`
	EnablePatchVersionAutoUpgrade *bool    `json:"enable_patch_version_auto_upgrade"`
	NetworkID                     *string  `json:"network_id"`
	SubnetID                      *string  `json:"subnet_id"`
	MaintenanceWindowStart        *string  `json:"maintenance_window_start"`
	MaintenanceWindowEnd          *string  `json:"maintenance_window_end"`
	Zonal                         *bool    `json:"zonal"`
	ClusterType                   *string  `json:"cluster_type"`
	KubeAPIIP                     *string  `json:"kube_api_ip"`
	Status                        *string  `json:"status"`
	FeatureGates                  []string `json:"feature_gates"`
	AdmissionControllers          []string `json:"admission_controllers"`
	PrivateKubeAPI                *bool    `json:"private_kube_api"`
	CNIType                       *string  `json:"cni_type"`
	CNICiliumSettings             []struct {
		EnvoyDaemonset *bool `json:"envoy_daemonset"`
		HubbleRelay    *bool `json:"hubble_relay"`
	} `json:"cni_cilium_settings"`
	EnableAuditLogs bool `json:"enable_audit_logs"`
	OIDC            []struct {
		Enabled       bool   `json:"enabled"`
		ProviderName  string `json:"provider_name"`
		IssuerURL     string `json:"issuer_url"`
		ClientID      string `json:"client_id"`
		UsernameClaim string `json:"username_claim"`
		GroupsClaim   string `json:"groups_claim"`
		CACerts       string `json:"ca_certs"`
	} `json:"oidc"`
	// Dropped: enable_pod_security_policy (not in API v2), timeouts (the
	// _v2 block starts empty, so the defaults apply).
}

// mksNodegroupV1MoveState is the JSON shape of a selectel_mks_nodegroup_v1
// state, written by SDKv2 from resourceMKSNodegroupV1.
type mksNodegroupV1MoveState struct {
	ID               *string           `json:"id"`
	ClusterID        *string           `json:"cluster_id"`
	ProjectID        *string           `json:"project_id"`
	Status           *string           `json:"status"`
	AvailabilityZone *string           `json:"availability_zone"`
	NodesCount       *int64            `json:"nodes_count"`
	AffinityPolicy   *string           `json:"affinity_policy"`
	CPUs             *int64            `json:"cpus"`
	RAMMB            *int64            `json:"ram_mb"`
	VolumeGB         *int64            `json:"volume_gb"`
	VolumeType       *string           `json:"volume_type"`
	LocalVolume      *bool             `json:"local_volume"`
	FlavorID         *string           `json:"flavor_id"`
	Labels           map[string]string `json:"labels"`
	Taints           []struct {
		Key    string `json:"key"`
		Value  string `json:"value"`
		Effect string `json:"effect"`
	} `json:"taints"`
	EnableAutoscale           *bool   `json:"enable_autoscale"`
	AutoscaleMinNodes         *int64  `json:"autoscale_min_nodes"`
	AutoscaleMaxNodes         *int64  `json:"autoscale_max_nodes"`
	UserData                  *string `json:"user_data"`
	InstallNvidiaDevicePlugin *bool   `json:"install_nvidia_device_plugin"`
	Preemptible               *bool   `json:"preemptible"`
	NodegroupType             *string `json:"nodegroup_type"`
	Nodes                     []struct {
		ID       string `json:"id"`
		IP       string `json:"ip"`
		Hostname string `json:"hostname"`
	} `json:"nodes"`
	// Dropped: region (the pool follows from segment), keypair_name
	// (mk-api-v2 ignores it), timeouts (the _v2 block starts empty).
	// project_id goes to the private state only, see
	// mksNodegroupV2MovedProjectKey: the _v2 node group takes the project
	// from the provider.
}

// mksV2TimeoutsAttrTypes are the attributes of the timeouts block of both _v2
// resources.
var mksV2TimeoutsAttrTypes = map[string]attr.Type{
	"create": types.StringType,
	"update": types.StringType,
	"delete": types.StringType,
}

// mksV2MoveSource decodes the raw state when the source is typeName of this
// provider. Any registry host and namespace is accepted, so a dev_overrides
// address such as terraform.local/local/selectel works too. It returns false
// for another source, leaving the target state null: the framework then
// reports that the move is not supported.
func mksV2MoveSource(req resource.MoveStateRequest, typeName string, v any, diags *diag.Diagnostics) bool {
	parts := strings.Split(req.SourceProviderAddress, "/")
	if req.SourceTypeName != typeName || parts[len(parts)-1] != "selectel" || req.SourceRawState == nil {
		return false
	}

	err := json.Unmarshal(req.SourceRawState.JSON, v)
	if err != nil {
		diags.AddError("Unable to read the "+typeName+" state", err.Error())

		return false
	}

	return true
}

func (r *mksClusterV2Resource) MoveState(_ context.Context) []resource.StateMover {
	return []resource.StateMover{{StateMover: moveMKSClusterV1State}}
}

func moveMKSClusterV1State(ctx context.Context, req resource.MoveStateRequest, resp *resource.MoveStateResponse) {
	var v1 mksClusterV1MoveState
	if !mksV2MoveSource(req, "selectel_mks_cluster_v1", &v1, &resp.Diagnostics) {
		return
	}

	// cluster_type wins over the deprecated zonal, as in the API. With
	// neither, Read takes it from the API.
	clusterType := types.StringNull()
	switch {
	case v1.ClusterType != nil && *v1.ClusterType != "":
		clusterType = types.StringValue(strings.ToUpper(*v1.ClusterType))
	case v1.Zonal != nil && *v1.Zonal:
		clusterType = types.StringValue(string(mksclient.ClusterCreateStructClusterTypeBASIC))
	case v1.Zonal != nil:
		clusterType = types.StringValue(string(mksclient.ClusterCreateStructClusterTypeHIGHAVAILABILITY))
	}

	cilium := types.ObjectNull(mksClusterV2CiliumAttrTypes)
	if len(v1.CNICiliumSettings) > 0 {
		var d diag.Diagnostics
		cilium, d = types.ObjectValueFrom(ctx, mksClusterV2CiliumAttrTypes, mksClusterV2CiliumModel{
			EnvoyDaemonset: types.BoolPointerValue(v1.CNICiliumSettings[0].EnvoyDaemonset),
			HubbleRelay:    types.BoolPointerValue(v1.CNICiliumSettings[0].HubbleRelay),
		})
		resp.Diagnostics.Append(d...)
	}

	// The same shape Read gives the API options: _v1 has no audit log secret
	// and no x509 certificates, and a missing oidc block is a disabled OIDC.
	options := mksclient.KubernetesOptions{
		FeatureGates:         v1.FeatureGates,
		AdmissionControllers: v1.AdmissionControllers,
		AuditLogs:            mksclient.AuditLogs{Enabled: v1.EnableAuditLogs},
	}
	if len(v1.OIDC) > 0 {
		oidc := v1.OIDC[0]
		options.Oidc = mksclient.OIDC{
			Enabled:       oidc.Enabled,
			ProviderName:  oidc.ProviderName,
			IssuerUrl:     oidc.IssuerURL,
			ClientId:      oidc.ClientID,
			UsernameClaim: oidc.UsernameClaim,
			GroupsClaim:   oidc.GroupsClaim,
			CaCerts:       oidc.CACerts,
		}
	}
	kubeOptions, d := flattenMKSClusterV2KubeOptions(ctx, options, types.StringNull())
	resp.Diagnostics.Append(d...)

	state := mksClusterV2Model{
		ID:          types.StringPointerValue(v1.ID),
		Name:        types.StringPointerValue(v1.Name),
		Pool:        types.StringPointerValue(v1.Region),
		ProjectID:   types.StringPointerValue(v1.ProjectID),
		KubeVersion: types.StringPointerValue(v1.KubeVersion),
		ClusterType: clusterType,
		// Read sets it from the network type of the cluster.
		WorkersType: types.StringNull(),
		NetworkID:   types.StringPointerValue(v1.NetworkID),
		SubnetID:    types.StringPointerValue(v1.SubnetID),
		// The API never returns it, as after an import.
		CloudSubnetCIDR:               types.StringNull(),
		PrivateKubeAPI:                types.BoolPointerValue(v1.PrivateKubeAPI),
		MaintenanceWindowStart:        types.StringPointerValue(v1.MaintenanceWindowStart),
		MaintenanceWindowEnd:          types.StringPointerValue(v1.MaintenanceWindowEnd),
		EnableAutorepair:              types.BoolPointerValue(v1.EnableAutorepair),
		EnablePatchVersionAutoUpgrade: types.BoolPointerValue(v1.EnablePatchVersionAutoUpgrade),
		CNIType:                       types.StringPointerValue(v1.CNIType),
		CNICiliumSettings:             cilium,
		KubernetesOptions:             kubeOptions,
		Status:                        types.StringPointerValue(v1.Status),
		KubeAPIIP:                     types.StringPointerValue(v1.KubeAPIIP),
		Timeouts:                      timeouts.Value{Object: types.ObjectNull(mksV2TimeoutsAttrTypes)},
	}
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.TargetState.Set(ctx, state)...)
}

func (r *mksNodegroupV2Resource) MoveState(_ context.Context) []resource.StateMover {
	return []resource.StateMover{{StateMover: moveMKSNodegroupV1State}}
}

func moveMKSNodegroupV1State(ctx context.Context, req resource.MoveStateRequest, resp *resource.MoveStateResponse) {
	var v1 mksNodegroupV1MoveState
	if !mksV2MoveSource(req, "selectel_mks_nodegroup_v1", &v1, &resp.Diagnostics) {
		return
	}

	// _v1 stores 0 for cpus and ram_mb of a node group with a flavor, which
	// _v2 would take for a configured value and recreate the node group.
	positiveOrNull := func(v *int64) types.Int64 {
		if v == nil || *v <= 0 {
			return types.Int64Null()
		}

		return types.Int64Value(*v)
	}
	// _v1 stores "" for an unset affinity_policy.
	nonEmptyOrNull := func(v *string) types.String {
		if v == nil || *v == "" {
			return types.StringNull()
		}

		return types.StringValue(*v)
	}

	var diags diag.Diagnostics
	cloud, d := types.ObjectValueFrom(ctx, mksNodegroupV2CloudConfigAttrTypes, mksNodegroupV2CloudConfigModel{
		FlavorID:       types.StringPointerValue(v1.FlavorID),
		CPUs:           positiveOrNull(v1.CPUs),
		RAMMB:          positiveOrNull(v1.RAMMB),
		VolumeGB:       types.Int64PointerValue(v1.VolumeGB),
		VolumeType:     types.StringPointerValue(v1.VolumeType),
		LocalVolume:    types.BoolPointerValue(v1.LocalVolume),
		AffinityPolicy: nonEmptyOrNull(v1.AffinityPolicy),
	})
	diags.Append(d...)

	labels := types.MapNull(types.StringType)
	if v1.Labels != nil {
		labels, d = types.MapValueFrom(ctx, types.StringType, v1.Labels)
		diags.Append(d...)
	}

	taints := types.ListNull(types.ObjectType{AttrTypes: mksNodegroupV2TaintAttrTypes})
	if v1.Taints != nil {
		models := make([]mksNodegroupV2TaintModel, 0, len(v1.Taints))
		for _, t := range v1.Taints {
			models = append(models, mksNodegroupV2TaintModel{
				Key: types.StringValue(t.Key), Value: types.StringValue(t.Value), Effect: types.StringValue(t.Effect),
			})
		}
		taints, d = types.ListValueFrom(ctx, types.ObjectType{AttrTypes: mksNodegroupV2TaintAttrTypes}, models)
		diags.Append(d...)
	}

	nodes := types.ListNull(types.ObjectType{AttrTypes: mksNodegroupV2NodeAttrTypes})
	if v1.Nodes != nil {
		models := make([]mksNodegroupV2NodeModel, 0, len(v1.Nodes))
		for _, n := range v1.Nodes {
			models = append(models, mksNodegroupV2NodeModel{
				ID: types.StringValue(n.ID), IP: types.StringValue(n.IP), Hostname: types.StringValue(n.Hostname),
			})
		}
		nodes, d = types.ListValueFrom(ctx, types.ObjectType{AttrTypes: mksNodegroupV2NodeAttrTypes}, models)
		diags.Append(d...)
	}

	state := mksNodegroupV2Model{
		// Both use <cluster_id>/<nodegroup_id>.
		ID:        types.StringPointerValue(v1.ID),
		ClusterID: types.StringPointerValue(v1.ClusterID),
		Segment:   types.StringPointerValue(v1.AvailabilityZone),
		Count:     types.Int64PointerValue(v1.NodesCount),
		// _v1 had none; Read keeps it null for a cloud node group.
		CIDR:                      types.StringNull(),
		Labels:                    labels,
		Taints:                    taints,
		EnableAutoscale:           types.BoolPointerValue(v1.EnableAutoscale),
		AutoscaleMinNodes:         types.Int64PointerValue(v1.AutoscaleMinNodes),
		AutoscaleMaxNodes:         types.Int64PointerValue(v1.AutoscaleMaxNodes),
		UserData:                  types.StringPointerValue(v1.UserData),
		InstallNvidiaDevicePlugin: types.BoolPointerValue(v1.InstallNvidiaDevicePlugin),
		Preemptible:               types.BoolPointerValue(v1.Preemptible),
		CloudNodegroupConfig:      cloud,
		DedicatedNodegroupConfig:  types.ObjectNull(mksNodegroupV2DedicatedConfigAttrTypes),
		NodegroupType:             types.StringPointerValue(v1.NodegroupType),
		Status:                    types.StringPointerValue(v1.Status),
		Nodes:                     nodes,
		Timeouts:                  timeouts.Value{Object: types.ObjectNull(mksV2TimeoutsAttrTypes)},
	}
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.TargetState.Set(ctx, state)...)
	// The framework always sets TargetPrivate; a direct call may not.
	if v1.ProjectID != nil && *v1.ProjectID != "" && resp.TargetPrivate != nil {
		project, err := json.Marshal(*v1.ProjectID)
		if err != nil {
			resp.Diagnostics.AddError("Unable to save the _v1 project_id", err.Error())

			return
		}
		resp.Diagnostics.Append(resp.TargetPrivate.SetKey(ctx, mksNodegroupV2MovedProjectKey, project)...)
	}
}

// mksNodegroupV2MovedProjectKey holds the project_id of a node group moved
// from _v1 until its first successful Read: a 404 before then more likely
// means that the provider project is another one than that the node group is
// gone.
const mksNodegroupV2MovedProjectKey = "moved_project_id"
