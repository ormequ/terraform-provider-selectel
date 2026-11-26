package mks

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	mksclient "github.com/selectel/mks-go/v2/pkg/mksclient"
)

type nodegroupModel struct {
	ID                        types.String   `tfsdk:"id"`
	ClusterID                 types.String   `tfsdk:"cluster_id"`
	ProjectID                 types.String   `tfsdk:"project_id"`
	Segment                   types.String   `tfsdk:"segment"`
	NodesCount                types.Int64    `tfsdk:"nodes_count"`
	Labels                    types.Map      `tfsdk:"labels"`
	Taints                    types.List     `tfsdk:"taints"`
	EnableAutoscale           types.Bool     `tfsdk:"enable_autoscale"`
	AutoscaleMinNodes         types.Int64    `tfsdk:"autoscale_min_nodes"`
	AutoscaleMaxNodes         types.Int64    `tfsdk:"autoscale_max_nodes"`
	UserData                  types.String   `tfsdk:"user_data"`
	InstallNvidiaDevicePlugin types.Bool     `tfsdk:"install_nvidia_device_plugin"`
	Preemptible               types.Bool     `tfsdk:"preemptible"`
	CloudNodegroupConfig      types.Object   `tfsdk:"cloud_nodegroup_config"`
	NodegroupType             types.String   `tfsdk:"nodegroup_type"`
	Status                    types.String   `tfsdk:"status"`
	Nodes                     types.List     `tfsdk:"nodes"`
	Timeouts                  timeouts.Value `tfsdk:"timeouts"`
}

type nodegroupTaintModel struct {
	Key    types.String `tfsdk:"key"`
	Value  types.String `tfsdk:"value"`
	Effect types.String `tfsdk:"effect"`
}

type nodegroupNodeModel struct {
	ID       types.String `tfsdk:"id"`
	IP       types.String `tfsdk:"ip"`
	Hostname types.String `tfsdk:"hostname"`
}

type nodegroupCloudConfigModel struct {
	FlavorID       types.String `tfsdk:"flavor_id"`
	CPUs           types.Int64  `tfsdk:"cpus"`
	RAMMB          types.Int64  `tfsdk:"ram_mb"`
	VolumeGB       types.Int64  `tfsdk:"volume_gb"`
	VolumeType     types.String `tfsdk:"volume_type"`
	LocalVolume    types.Bool   `tfsdk:"local_volume"`
	AffinityPolicy types.String `tfsdk:"affinity_policy"`
}

// nodegroupID is the node group part of the <cluster_id>/<nodegroup_id> ID.
func (m *nodegroupModel) nodegroupID() string {
	_, nodegroupID, _ := strings.Cut(m.ID.ValueString(), "/")

	return nodegroupID
}

// pool is the segment without its zone letter, or empty for an imported node group before its first read.
func (m *nodegroupModel) pool() string {
	segment := m.Segment.ValueString()
	if segment == "" {
		return ""
	}

	return segment[:len(segment)-1]
}

func expandNodegroupCreateOpts(ctx context.Context, plan nodegroupModel) (mksclient.NodegroupCreateStruct, diag.Diagnostics) {
	opts := mksclient.NodegroupCreateStruct{
		Count:                     plan.NodesCount.ValueInt64(),
		Segment:                   plan.Segment.ValueString(),
		EnableAutoscale:           knownBoolPointer(plan.EnableAutoscale),
		AutoscaleMinNodes:         plan.AutoscaleMinNodes.ValueInt64Pointer(),
		AutoscaleMaxNodes:         plan.AutoscaleMaxNodes.ValueInt64Pointer(),
		InstallNvidiaDevicePlugin: knownBoolPointer(plan.InstallNvidiaDevicePlugin),
		Preemptible:               plan.Preemptible.ValueBool(),
		UserData:                  plan.UserData.ValueString(),
	}
	if plan.NodesCount.IsNull() || plan.NodesCount.IsUnknown() {
		// Only with autoscaling, see ValidateConfig.
		opts.Count = plan.AutoscaleMinNodes.ValueInt64()
	}

	var diags diag.Diagnostics
	if !plan.Labels.IsNull() {
		labels := map[string]string{}
		diags.Append(plan.Labels.ElementsAs(ctx, &labels, false)...)
		opts.Labels = &labels
	}
	if !plan.Taints.IsNull() {
		taints, d := expandNodegroupTaints(ctx, plan.Taints)
		diags.Append(d...)
		opts.Taints = &taints
	}

	var cloud nodegroupCloudConfigModel
	diags.Append(plan.CloudNodegroupConfig.As(ctx, &cloud, basetypes.ObjectAsOptions{UnhandledUnknownAsEmpty: true})...)
	opts.CloudNodegroupConfig = &mksclient.CloudNodegroupConfig{
		FlavorId:       cloud.FlavorID.ValueString(),
		Cpus:           cloud.CPUs.ValueInt64(),
		RamMb:          cloud.RAMMB.ValueInt64(),
		VolumeGb:       cloud.VolumeGB.ValueInt64(),
		VolumeType:     cloud.VolumeType.ValueString(),
		LocalVolume:    cloud.LocalVolume.ValueBool(),
		AffinityPolicy: cloud.AffinityPolicy.ValueString(),
	}

	return opts, diags
}

// expandNodegroupPatch collects the fields that change in place. A removed
// labels or taints argument is sent empty, which clears it.
func expandNodegroupPatch(ctx context.Context, plan, state nodegroupModel) (mksclient.NodegroupUpdateStruct, bool, diag.Diagnostics) {
	var diags diag.Diagnostics
	patch := mksclient.NodegroupUpdateStruct{}
	changed := false

	if !plan.Labels.Equal(state.Labels) {
		labels := map[string]string{}
		if !plan.Labels.IsNull() {
			diags.Append(plan.Labels.ElementsAs(ctx, &labels, false)...)
		}
		patch.Labels = &labels
		changed = true
	}
	if !plan.Taints.Equal(state.Taints) {
		taints, d := expandNodegroupTaints(ctx, plan.Taints)
		diags.Append(d...)
		patch.Taints = &taints
		changed = true
	}
	if !plan.EnableAutoscale.Equal(state.EnableAutoscale) || !plan.AutoscaleMinNodes.Equal(state.AutoscaleMinNodes) ||
		!plan.AutoscaleMaxNodes.Equal(state.AutoscaleMaxNodes) {
		patch.EnableAutoscale = plan.EnableAutoscale.ValueBoolPointer()
		if !plan.AutoscaleMinNodes.IsNull() {
			patch.AutoscaleMinNodes = new(int(plan.AutoscaleMinNodes.ValueInt64()))
		}
		if !plan.AutoscaleMaxNodes.IsNull() {
			patch.AutoscaleMaxNodes = new(int(plan.AutoscaleMaxNodes.ValueInt64()))
		}
		changed = true
	}

	return patch, changed, diags
}

// expandNodegroupTaints never returns nil, so a null or empty list clears the taints.
func expandNodegroupTaints(ctx context.Context, list types.List) ([]mksclient.NodegroupTaint, diag.Diagnostics) {
	var models []nodegroupTaintModel
	diags := list.ElementsAs(ctx, &models, false)

	taints := make([]mksclient.NodegroupTaint, 0, len(models))
	for _, m := range models {
		taints = append(taints, mksclient.NodegroupTaint{
			Key:    m.Key.ValueString(),
			Value:  m.Value.ValueString(),
			Effect: mksclient.NodegroupTaintEffect(m.Effect.ValueString()),
		})
	}

	return taints, diags
}

// createdNodegroupID picks the new node group that matches the create options, because the API returns no ID.
// Another client can create node groups in the cluster at the same time.
func createdNodegroupID(before, after []mksclient.NodegroupListItem, opts mksclient.NodegroupCreateStruct) (string, error) {
	var created, matching []string
	for _, ng := range after {
		if slices.ContainsFunc(before, func(b mksclient.NodegroupListItem) bool { return b.Id == ng.Id }) {
			continue
		}
		created = append(created, ng.Id)
		// The API returns neither cpus nor ram_mb, so only a configured flavor_id narrows the match.
		flavorID := opts.CloudNodegroupConfig.FlavorId
		if ng.Segment != opts.Segment || !maps.Equal(ng.Labels, valueOrZero(opts.Labels)) ||
			flavorID != "" && (ng.CloudNodegroupConfig == nil || ng.CloudNodegroupConfig.FlavorId != flavorID) {
			continue
		}
		matching = append(matching, ng.Id)
	}
	if len(matching) == 1 {
		return matching[0], nil
	}

	return "", fmt.Errorf("%d of the new node groups %v match the configuration, want 1: "+
		"import the right one as <cluster_id>/<nodegroup_id>", len(matching), created)
}

func (m *nodegroupModel) fromAPI(ctx context.Context, ng *mksclient.NodegroupDetailed, prior nodegroupModel, applied bool) diag.Diagnostics {
	var diags diag.Diagnostics

	m.ID = types.StringValue(ng.ClusterId + "/" + ng.Id)
	m.ClusterID = types.StringValue(ng.ClusterId)
	m.Segment = types.StringValue(ng.Segment)
	if !applied || prior.NodesCount.IsNull() || prior.NodesCount.IsUnknown() {
		// After an apply the state keeps the planned count: the autoscaler can move the nodes at any time.
		m.NodesCount = types.Int64Value(int64(len(ng.Nodes)))
	}
	m.EnableAutoscale = types.BoolValue(ng.EnableAutoscale)
	m.AutoscaleMinNodes = knownInt64OrNull(prior.AutoscaleMinNodes)
	m.AutoscaleMaxNodes = knownInt64OrNull(prior.AutoscaleMaxNodes)
	if ng.EnableAutoscale {
		// The API answers 0 for both while autoscaling is off.
		m.AutoscaleMinNodes = types.Int64PointerValue(ng.AutoscaleMinNodes)
		m.AutoscaleMaxNodes = types.Int64PointerValue(ng.AutoscaleMaxNodes)
	}
	m.UserData = types.StringValue(ng.UserData)
	m.InstallNvidiaDevicePlugin = types.BoolValue(ng.InstallNvidiaDevicePlugin)
	m.Preemptible = types.BoolValue(ng.Preemptible)
	m.NodegroupType = types.StringValue(string(ng.NodegroupType))
	m.Status = types.StringValue(string(ng.Status))

	var d diag.Diagnostics
	m.Labels = types.MapNull(types.StringType)
	if len(ng.Labels) > 0 {
		m.Labels, d = types.MapValueFrom(ctx, types.StringType, ng.Labels)
		diags.Append(d...)
	} else if !prior.Labels.IsNull() && !prior.Labels.IsUnknown() && len(prior.Labels.Elements()) == 0 {
		// A configured empty map stays, so the state matches the configuration.
		m.Labels = prior.Labels
	}

	taintType := types.ObjectType{AttrTypes: nodegroupTaintAttrTypes}
	m.Taints = types.ListNull(taintType)
	if len(ng.Taints) > 0 {
		taints := make([]nodegroupTaintModel, 0, len(ng.Taints))
		for _, t := range ng.Taints {
			taints = append(taints, nodegroupTaintModel{
				Key:    types.StringValue(t.Key),
				Value:  types.StringValue(t.Value),
				Effect: types.StringValue(string(t.Effect)),
			})
		}
		m.Taints, d = types.ListValueFrom(ctx, taintType, taints)
		diags.Append(d...)
	} else if !prior.Taints.IsNull() && !prior.Taints.IsUnknown() && len(prior.Taints.Elements()) == 0 {
		m.Taints = prior.Taints
	}

	nodes := make([]nodegroupNodeModel, 0, len(ng.Nodes))
	for _, n := range ng.Nodes {
		nodes = append(nodes, nodegroupNodeModel{
			ID:       types.StringValue(n.Id),
			IP:       types.StringValue(n.Ip),
			Hostname: types.StringValue(n.Hostname),
		})
	}
	m.Nodes, d = types.ListValueFrom(ctx, types.ObjectType{AttrTypes: nodegroupNodeAttrTypes}, nodes)
	diags.Append(d...)

	if ng.CloudNodegroupConfig == nil {
		return diags
	}
	var priorCloud nodegroupCloudConfigModel
	if !prior.CloudNodegroupConfig.IsNull() && !prior.CloudNodegroupConfig.IsUnknown() {
		diags.Append(prior.CloudNodegroupConfig.As(ctx, &priorCloud, basetypes.ObjectAsOptions{UnhandledUnknownAsEmpty: true})...)
	}
	// The API never returns cpus, ram_mb, and affinity_policy, so they come from prior.
	m.CloudNodegroupConfig, d = types.ObjectValueFrom(ctx, nodegroupCloudConfigAttrTypes, nodegroupCloudConfigModel{
		FlavorID:       types.StringValue(ng.CloudNodegroupConfig.FlavorId),
		CPUs:           knownInt64OrNull(priorCloud.CPUs),
		RAMMB:          knownInt64OrNull(priorCloud.RAMMB),
		VolumeGB:       types.Int64Value(ng.CloudNodegroupConfig.VolumeGb),
		VolumeType:     types.StringValue(ng.CloudNodegroupConfig.VolumeType),
		LocalVolume:    types.BoolValue(ng.CloudNodegroupConfig.LocalVolume),
		AffinityPolicy: knownStringOrNull(priorCloud.AffinityPolicy),
	})
	diags.Append(d...)

	return diags
}

// checkFlavorVolume fails when the API replaced a configured volume_gb or
// local_volume with the values of the flavor, which it does without an error.
func checkFlavorVolume(ctx context.Context, planned types.Object, got *mksclient.CloudNodegroupConfigInfo) diag.Diagnostics {
	var cloud nodegroupCloudConfigModel
	diags := planned.As(ctx, &cloud, basetypes.ObjectAsOptions{UnhandledUnknownAsEmpty: true})
	if diags.HasError() || got == nil {
		return diags
	}

	var replaced []string
	if !cloud.LocalVolume.IsNull() && !cloud.LocalVolume.IsUnknown() && cloud.LocalVolume.ValueBool() != got.LocalVolume {
		replaced = append(replaced, fmt.Sprintf("local_volume = %t instead of %t", got.LocalVolume, cloud.LocalVolume.ValueBool()))
	}
	if !cloud.VolumeGB.IsNull() && !cloud.VolumeGB.IsUnknown() && cloud.VolumeGB.ValueInt64() != got.VolumeGb {
		replaced = append(replaced, fmt.Sprintf("volume_gb = %d instead of %d", got.VolumeGb, cloud.VolumeGB.ValueInt64()))
	}
	if len(replaced) > 0 {
		diags.AddAttributeError(path.Root("cloud_nodegroup_config"), "Volume replaced by the flavor",
			fmt.Sprintf("Flavor %s has a local disk, so the node group got %s. "+
				"Omit volume_gb and local_volume with this flavor: the API takes them from the flavor.",
				got.FlavorId, strings.Join(replaced, " and ")))
	}

	return diags
}

func knownStringOrNull(v types.String) types.String {
	if v.IsUnknown() {
		return types.StringNull()
	}

	return v
}

func knownInt64OrNull(v types.Int64) types.Int64 {
	if v.IsUnknown() {
		return types.Int64Null()
	}

	return v
}
