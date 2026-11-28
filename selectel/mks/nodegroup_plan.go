package mks

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
)

func (r *nodegroupResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var config nodegroupModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// The API needs a count to create the node group, and the autoscaler starts from autoscale_min_nodes.
	if config.NodesCount.IsNull() && !config.EnableAutoscale.IsUnknown() && !config.EnableAutoscale.ValueBool() {
		resp.Diagnostics.AddAttributeError(path.Root("nodes_count"), "Missing node count",
			"Set nodes_count, or enable autoscaling with enable_autoscale = true.")
	}
	if config.EnableAutoscale.ValueBool() && (config.AutoscaleMinNodes.IsNull() || config.AutoscaleMaxNodes.IsNull()) {
		resp.Diagnostics.AddAttributeError(path.Root("enable_autoscale"), "Missing autoscaling limits",
			"enable_autoscale = true requires both autoscale_min_nodes and autoscale_max_nodes.")
	}

	if config.CloudNodegroupConfig.IsNull() || config.CloudNodegroupConfig.IsUnknown() {
		return
	}
	var cloud nodegroupCloudConfigModel
	resp.Diagnostics.Append(config.CloudNodegroupConfig.As(ctx, &cloud, basetypes.ObjectAsOptions{UnhandledUnknownAsEmpty: true})...)
	if resp.Diagnostics.HasError() {
		return
	}
	// The API installs the plugin only for a GPU flavor, which cpus and ram_mb never make.
	if config.InstallNvidiaDevicePlugin.ValueBool() && !cloud.CPUs.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("install_nvidia_device_plugin"), "NVIDIA device plugin needs a GPU flavor",
			"Set cloud_nodegroup_config.flavor_id to a GPU flavor instead of cpus and ram_mb, or omit install_nvidia_device_plugin.")
	}
	if cloud.LocalVolume.ValueBool() && !cloud.VolumeType.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("cloud_nodegroup_config").AtName("volume_type"), "Volume type with a local volume",
			"volume_type sets a network volume: remove it or set local_volume to false.")
	}
}

func (r *nodegroupResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}

	var state, plan nodegroupModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// The autoscaler owns the node count.
	if plan.EnableAutoscale.ValueBool() && !plan.NodesCount.Equal(state.NodesCount) {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("nodes_count"), state.NodesCount)...)
	}

	// An update changes the status and can change the nodes while it runs.
	if !resp.Plan.Raw.Equal(req.State.Raw) {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("status"), types.StringUnknown())...)
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("nodes"), types.ListUnknown(types.ObjectType{AttrTypes: nodegroupNodeAttrTypes}))...)
	}
}
