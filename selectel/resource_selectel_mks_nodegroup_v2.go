package selectel

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/identityschema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	mksv2 "github.com/selectel/mks-go/v2/pkg"
	"github.com/selectel/mks-go/v2/pkg/cluster"
	"github.com/selectel/mks-go/v2/pkg/mksclient"
	"github.com/selectel/mks-go/v2/pkg/nodegroup"
)

var (
	_ resource.ResourceWithConfigure      = &mksNodegroupV2Resource{}
	_ resource.ResourceWithIdentity       = &mksNodegroupV2Resource{}
	_ resource.ResourceWithImportState    = &mksNodegroupV2Resource{}
	_ resource.ResourceWithModifyPlan     = &mksNodegroupV2Resource{}
	_ resource.ResourceWithMoveState      = &mksNodegroupV2Resource{}
	_ resource.ResourceWithValidateConfig = &mksNodegroupV2Resource{}
)

var mksNodegroupV2Docs = resourceDocs{Name: "node group"}

// mksNodegroupV2SegmentRegexp matches a pool segment such as ru-3a.
var mksNodegroupV2SegmentRegexp = regexp.MustCompile(`^[a-z]+-\d+[a-z]$`)

var (
	mksNodegroupV2TaintAttrTypes = map[string]attr.Type{
		"key":    types.StringType,
		"value":  types.StringType,
		"effect": types.StringType,
	}
	mksNodegroupV2NodeAttrTypes = map[string]attr.Type{
		"id":       types.StringType,
		"ip":       types.StringType,
		"hostname": types.StringType,
	}
	mksNodegroupV2CloudConfigAttrTypes = map[string]attr.Type{
		"flavor_id":       types.StringType,
		"cpus":            types.Int64Type,
		"ram_mb":          types.Int64Type,
		"volume_gb":       types.Int64Type,
		"volume_type":     types.StringType,
		"local_volume":    types.BoolType,
		"affinity_policy": types.StringType,
	}
)

type mksNodegroupV2Model struct {
	ID                        types.String   `tfsdk:"id"`
	ClusterID                 types.String   `tfsdk:"cluster_id"`
	Segment                   types.String   `tfsdk:"segment"`
	Count                     types.Int64    `tfsdk:"nodes_count"`
	CIDR                      types.String   `tfsdk:"cidr"`
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

// mksNodegroupV2IdentityModel identifies a node group for import by
// identity. The project comes from the provider: the node group has no
// project_id attribute.
type mksNodegroupV2IdentityModel struct {
	ClusterID types.String `tfsdk:"cluster_id"`
	ID        types.String `tfsdk:"id"`
	Pool      types.String `tfsdk:"pool"`
}

type mksNodegroupV2TaintModel struct {
	Key    types.String `tfsdk:"key"`
	Value  types.String `tfsdk:"value"`
	Effect types.String `tfsdk:"effect"`
}

type mksNodegroupV2NodeModel struct {
	ID       types.String `tfsdk:"id"`
	IP       types.String `tfsdk:"ip"`
	Hostname types.String `tfsdk:"hostname"`
}

type mksNodegroupV2CloudConfigModel struct {
	FlavorID       types.String `tfsdk:"flavor_id"`
	CPUs           types.Int64  `tfsdk:"cpus"`
	RAMMB          types.Int64  `tfsdk:"ram_mb"`
	VolumeGB       types.Int64  `tfsdk:"volume_gb"`
	VolumeType     types.String `tfsdk:"volume_type"`
	LocalVolume    types.Bool   `tfsdk:"local_volume"`
	AffinityPolicy types.String `tfsdk:"affinity_policy"`
}

type mksNodegroupV2Resource struct {
	mksV2Provided
}

func newMKSNodegroupV2Resource() resource.Resource {
	return &mksNodegroupV2Resource{}
}

func (r *mksNodegroupV2Resource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_mks_nodegroup_v2"
}

func (r *mksNodegroupV2Resource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	resp.Diagnostics.Append(r.configure(req.ProviderData)...)
}

func (r *mksNodegroupV2Resource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	keepString := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	replaceString := []planmodifier.String{stringplanmodifier.UseStateForUnknown(), stringplanmodifier.RequiresReplace()}
	replaceBool := []planmodifier.Bool{boolplanmodifier.UseStateForUnknown(), boolplanmodifier.RequiresReplace()}
	keepBool := []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()}
	keepInt64 := []planmodifier.Int64{int64planmodifier.UseStateForUnknown()}
	// The API never returns these, so an imported node group has none:
	// setting them afterwards only fills the state.
	replaceInt64 := []planmodifier.Int64{int64planmodifier.RequiresReplaceIf(
		func(ctx context.Context, req planmodifier.Int64Request, resp *int64planmodifier.RequiresReplaceIfFuncResponse) {
			resp.RequiresReplace = mksNodegroupV2ReplaceUnlessImported(ctx, req.StateValue.IsNull(), req.Private.GetKey)
		}, mksNodegroupV2ReplaceUnlessImportedDescription, mksNodegroupV2ReplaceUnlessImportedDescription,
	)}
	replaceStringUnlessImported := []planmodifier.String{
		stringplanmodifier.UseStateForUnknown(),
		stringplanmodifier.RequiresReplaceIf(
			func(ctx context.Context, req planmodifier.StringRequest, resp *stringplanmodifier.RequiresReplaceIfFuncResponse) {
				resp.RequiresReplace = mksNodegroupV2ReplaceUnlessImported(ctx, req.StateValue.IsNull(), req.Private.GetKey)
			}, mksNodegroupV2ReplaceUnlessImportedDescription, mksNodegroupV2ReplaceUnlessImportedDescription,
		),
	}
	flavorPath := path.MatchRelative().AtParent().AtName("flavor_id")

	resp.Schema = schema.Schema{
		Description: "Creates and manages a Managed Kubernetes cloud node group using API v2.",
		Attributes: mksNodegroupV2Docs.withFrameworkDocsHints(map[string]schema.Attribute{
			"id": mksNodegroupV2Docs.idFrameworkResourceSchema(),
			"cluster_id": schema.StringAttribute{
				Required:      true,
				Description:   "Unique identifier of the cluster.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"segment": schema.StringAttribute{
				Required:      true,
				Description:   "Pool segment where all nodes of the node group are located, for example, `ru-7a`.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators: []validator.String{stringvalidator.RegexMatches(mksNodegroupV2SegmentRegexp,
					"must be a pool segment such as `ru-7a`")},
			},
			"nodes_count": schema.Int64Attribute{
				Optional: true,
				Computed: true,
				Description: "Number of nodes in the node group. Required unless `enable_autoscale` is `true`. " +
					"Changing it resizes the node group. While autoscaling is enabled, changes made by the autoscaler " +
					"or in the configuration are ignored.",
			},
			"cidr": schema.StringAttribute{
				Optional:      true,
				Computed:      true,
				Description:   "CIDR of the node group network.",
				PlanModifiers: replaceStringUnlessImported,
			},
			"labels": schema.MapAttribute{
				Optional:    true,
				Computed:    true,
				ElementType: types.StringType,
				Description: "Kubernetes labels applied to each node in the node group. " +
					"Labels removed from the configuration stay; set an empty map to remove all of them.",
				PlanModifiers: []planmodifier.Map{mapplanmodifier.UseStateForUnknown()},
			},
			"taints": schema.ListNestedAttribute{
				Optional: true,
				Computed: true,
				Description: "Kubernetes taints applied to each node in the node group. " +
					"Taints removed from the configuration stay; set an empty list to remove all of them.",
				PlanModifiers: []planmodifier.List{listplanmodifier.UseStateForUnknown()},
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"key":   schema.StringAttribute{Required: true, Description: "Taint key."},
						"value": schema.StringAttribute{Required: true, Description: "Taint value."},
						"effect": schema.StringAttribute{
							Required:    true,
							Description: "Taint effect: `NoSchedule`, `PreferNoSchedule` or `NoExecute`.",
							Validators: []validator.String{stringvalidator.OneOf(
								string(mksclient.NoSchedule), string(mksclient.PreferNoSchedule), string(mksclient.NoExecute),
							)},
						},
					},
				},
			},
			"enable_autoscale": schema.BoolAttribute{
				Optional:      true,
				Computed:      true,
				Description:   "Enables autoscaling of the node group within `autoscale_min_nodes` and `autoscale_max_nodes`.",
				PlanModifiers: keepBool,
			},
			"autoscale_min_nodes": schema.Int64Attribute{
				Optional:      true,
				Computed:      true,
				Description:   "Minimum number of nodes while autoscaling is enabled.",
				PlanModifiers: keepInt64,
			},
			"autoscale_max_nodes": schema.Int64Attribute{
				Optional:      true,
				Computed:      true,
				Description:   "Maximum number of nodes while autoscaling is enabled.",
				PlanModifiers: keepInt64,
			},
			"user_data": schema.StringAttribute{
				Optional:      true,
				Computed:      true,
				Description:   "Base64-encoded script that the nodes run on the first boot.",
				PlanModifiers: replaceString,
			},
			"install_nvidia_device_plugin": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				Description: "Installs the NVIDIA Device Plugin and GPU drivers. " +
					"If omitted, the API enables it for flavors with GPU.",
				PlanModifiers: replaceBool,
			},
			"preemptible": schema.BoolAttribute{
				Optional:      true,
				Computed:      true,
				Description:   "Makes the nodes preemptible.",
				PlanModifiers: replaceBool,
			},
			"cloud_nodegroup_config": schema.SingleNestedAttribute{
				Required:    true,
				Description: "Cloud server configuration of the nodes. Changing any of its values recreates the node group.",
				Attributes: map[string]schema.Attribute{
					"flavor_id": schema.StringAttribute{
						Optional:      true,
						Computed:      true,
						Description:   "Unique identifier of a flavor for the nodes. Conflicts with `cpus` and `ram_mb`.",
						PlanModifiers: replaceString,
					},
					"cpus": schema.Int64Attribute{
						Optional:      true,
						Description:   "Number of vCPUs of each node. Requires `ram_mb`, conflicts with `flavor_id`.",
						PlanModifiers: replaceInt64,
						Validators: []validator.Int64{
							int64validator.ConflictsWith(flavorPath),
							int64validator.AlsoRequires(path.MatchRelative().AtParent().AtName("ram_mb")),
						},
					},
					"ram_mb": schema.Int64Attribute{
						Optional:      true,
						Description:   "Amount of RAM of each node in MB. Requires `cpus`, conflicts with `flavor_id`.",
						PlanModifiers: replaceInt64,
						Validators: []validator.Int64{
							int64validator.ConflictsWith(flavorPath),
							int64validator.AlsoRequires(path.MatchRelative().AtParent().AtName("cpus")),
						},
					},
					"volume_gb": schema.Int64Attribute{
						Optional: true,
						Computed: true,
						Description: "Volume size of each node in GB. Cannot be set together with `flavor_id` " +
							"when `local_volume` is `true`: such a flavor defines the volume itself.",
						PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown(), int64planmodifier.RequiresReplace()},
					},
					"volume_type": schema.StringAttribute{
						Optional: true,
						Computed: true,
						Description: "Network volume type of each node in the `<type>.<segment>` format, for example, `fast.ru-7a`. " +
							"Cannot be set when `local_volume` is `true`.",
						PlanModifiers: replaceString,
					},
					"local_volume": schema.BoolAttribute{
						Optional:      true,
						Computed:      true,
						Description:   "Makes the nodes use a local volume instead of a network one.",
						PlanModifiers: replaceBool,
					},
					"affinity_policy": schema.StringAttribute{
						Optional:      true,
						Computed:      true,
						Description:   "Affinity policy of the nodes, for example, `soft-anti-affinity`.",
						PlanModifiers: replaceStringUnlessImported,
					},
				},
			},
			"nodegroup_type": schema.StringAttribute{
				Computed:      true,
				Description:   "Type of the node group, for example, `STANDARD` or `GPU`.",
				PlanModifiers: keepString,
			},
			"status": schema.StringAttribute{
				Computed:    true,
				Description: "Node group status.",
			},
			"nodes": schema.ListNestedAttribute{
				Computed:    true,
				Description: "Nodes of the node group.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id":       schema.StringAttribute{Computed: true, Description: "Unique identifier of the node."},
						"ip":       schema.StringAttribute{Computed: true, Description: "IP address of the node."},
						"hostname": schema.StringAttribute{Computed: true, Description: "Hostname of the node."},
					},
				},
			},
		}),
		Blocks: map[string]schema.Block{
			"timeouts": timeouts.Block(ctx, timeouts.Opts{Create: true, Update: true, Delete: true}),
		},
	}
}

func (r *mksNodegroupV2Resource) IdentitySchema(_ context.Context, _ resource.IdentitySchemaRequest, resp *resource.IdentitySchemaResponse) {
	resp.IdentitySchema = identityschema.Schema{
		Attributes: map[string]identityschema.Attribute{
			"cluster_id": identityschema.StringAttribute{
				RequiredForImport: true,
				Description: fmt.Sprintf("%s, for example, `%s`. %s", mksClusterV2Docs.idDescription(), exampleResourceID,
					mksClusterV2IDHint),
			},
			"id": mksNodegroupV2Docs.idFrameworkIdentitySchema("To get the node group ID, in the " +
				"[Control panel](https://my.selectel.ru/vpc/mks/), go to **Cloud Platform** ⟶ **Kubernetes**. Click the required cluster. " +
				"The node group ID is at the top of the node group card, near the pool."),
			"pool": mksNodegroupV2Docs.regionFrameworkIdentitySchema(mksClusterV2PoolHint),
		},
	}
}

// setIdentity is a no-op when Terraform passed no identity.
func (m *mksNodegroupV2Model) setIdentity(ctx context.Context, identity *tfsdk.ResourceIdentity) diag.Diagnostics {
	if identity == nil {
		return nil
	}

	return identity.Set(ctx, mksNodegroupV2IdentityModel{
		ClusterID: m.ClusterID,
		ID:        types.StringValue(m.nodegroupID()),
		Pool:      types.StringValue(mksNodegroupV2Pool(m.Segment.ValueString())),
	})
}

// ValidateConfig rejects the cloud_nodegroup_config combinations the API
// rejects and that depend on values, not only on presence (issues #273 and
// #300), see mk-api-v2 validate/nodegroup.go and validate/flavor.go.
func (r *mksNodegroupV2Resource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var config mksNodegroupV2Model
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if config.Count.IsNull() && !config.EnableAutoscale.IsUnknown() && !config.EnableAutoscale.ValueBool() {
		resp.Diagnostics.AddAttributeError(path.Root("nodes_count"), "Missing node count",
			"Set nodes_count, or enable autoscaling with enable_autoscale = true.")
	}

	if config.CloudNodegroupConfig.IsNull() || config.CloudNodegroupConfig.IsUnknown() {
		return
	}
	var cloud mksNodegroupV2CloudConfigModel
	resp.Diagnostics.Append(config.CloudNodegroupConfig.As(ctx, &cloud, basetypes.ObjectAsOptions{UnhandledUnknownAsEmpty: true})...)
	if resp.Diagnostics.HasError() {
		return
	}
	configPath := path.Root("cloud_nodegroup_config")

	if cloud.FlavorID.IsNull() && cloud.CPUs.IsNull() {
		resp.Diagnostics.AddAttributeError(configPath, "Missing node flavor",
			"Set either flavor_id, or cpus and ram_mb in cloud_nodegroup_config.")
	}
	if !cloud.LocalVolume.ValueBool() {
		return
	}
	if !cloud.VolumeType.IsNull() {
		resp.Diagnostics.AddAttributeError(configPath.AtName("volume_type"), "Conflicting volume settings",
			"volume_type sets a network volume and cannot be used with local_volume = true.")
	}
	if !cloud.FlavorID.IsNull() && !cloud.VolumeGB.IsNull() {
		resp.Diagnostics.AddAttributeError(configPath.AtName("volume_gb"), "Conflicting volume settings",
			"A flavor with a local volume defines its size: volume_gb cannot be used with flavor_id and local_volume = true.")
	}
}

func (r *mksNodegroupV2Resource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}

	var plan, state mksNodegroupV2Model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if !req.State.Raw.IsNull() {
		resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	}
	if resp.Diagnostics.HasError() {
		return
	}

	if req.State.Raw.IsNull() || !plan.ClusterID.Equal(state.ClusterID) {
		// An unknown cluster_id means the cluster is created in the same
		// apply; Create checks it before it sends anything.
		r.checkClusterWorkers(ctx, plan, &resp.Diagnostics)

		return
	}

	if !plan.EnableAutoscale.ValueBool() || state.Count.IsNull() || plan.Count.Equal(state.Count) {
		return
	}

	// The autoscaler owns the node count.
	plan.Count = state.Count
	// When the count was the only change, the computed values the framework
	// marked unknown keep their state too, so the plan stays empty.
	unchanged := plan
	unchanged.Status = state.Status
	unchanged.Nodes = state.Nodes
	resp.Diagnostics.Append(resp.Plan.Set(ctx, unchanged)...)
	if !resp.Plan.Raw.Equal(req.State.Raw) {
		resp.Diagnostics.Append(resp.Plan.Set(ctx, plan)...)
	}
}

// checkClusterWorkers fails the plan for a cloud node group in a cluster
// that accepts only dedicated ones.
func (r *mksNodegroupV2Resource) checkClusterWorkers(ctx context.Context, plan mksNodegroupV2Model, diags *diag.Diagnostics) {
	if plan.ClusterID.IsUnknown() || plan.Segment.IsUnknown() || r.config == nil {
		return
	}

	client, d := r.nodegroupClient(ctx, plan.Segment)
	diags.Append(d...)
	if diags.HasError() {
		return
	}

	err := mksNodegroupV2CheckCluster(ctx, client, plan.ClusterID.ValueString())
	if isMKSV2NotFound(err) {
		// Create reports it.
		return
	}
	if err != nil {
		diags.AddAttributeError(path.Root("cluster_id"), "Cloud node group in a DEDICATED cluster", err.Error())
	}
}

func (r *mksNodegroupV2Resource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan mksNodegroupV2Model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	timeout, diags := plan.Timeouts.Create(ctx, mksClusterV2DefaultTimeout)
	resp.Diagnostics.Append(diags...)
	client, diags := r.nodegroupClient(ctx, plan.Segment)
	resp.Diagnostics.Append(diags...)
	opts, diags := expandMKSNodegroupV2CreateOpts(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	clusterID := plan.ClusterID.ValueString()
	err := mksNodegroupV2CheckCluster(ctx, client, clusterID)
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("cluster_id"), "Error creating node group", errCreatingObject(objectNodegroup, err).Error())

		return
	}

	nodegroupID, err := createMKSNodegroupV2(ctx, client, clusterID, opts)
	if err != nil {
		resp.Diagnostics.AddError("Error creating node group", errCreatingObject(objectNodegroup, err).Error())

		return
	}

	waitErr := newMKSV2CreatedTaskWaiter(client, clusterID, nodegroupID).Wait(ctx)

	// Save the node group even when the wait failed, so Terraform taints it
	// instead of losing it. The wait may have used up ctx.
	readCtx, readCancel := mksV2ReadAfterWaitContext(ctx)
	defer readCancel()
	got, err := nodegroup.Get(readCtx, client, clusterID, nodegroupID)
	if err != nil {
		resp.Diagnostics.Append(mksV2PlannedState(req.Plan, &resp.State)...)
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), clusterID+"/"+nodegroupID)...)
		resp.Diagnostics.AddError("Error reading node group",
			errGettingObject(objectNodegroup, clusterID+"/"+nodegroupID, errors.Join(waitErr, err)).Error())

		return
	}
	state := plan
	resp.Diagnostics.Append(state.fromAPI(ctx, got, plan, true)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
	resp.Diagnostics.Append(state.setIdentity(ctx, resp.Identity)...)
	if waitErr != nil {
		resp.Diagnostics.AddError("Error waiting for the node group to become ready", waitErr.Error())
	}
}

func (r *mksNodegroupV2Resource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state mksNodegroupV2Model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	clusterID, nodegroupID, err := mksNodegroupV1ParseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Error reading node group", err.Error())

		return
	}
	pool := ""
	if !state.Segment.IsNull() {
		pool = mksNodegroupV2Pool(state.Segment.ValueString())
	} else if req.Identity != nil && !req.Identity.Raw.IsNull() {
		// Import by identity: the segment is not read yet.
		var identity mksNodegroupV2IdentityModel
		resp.Diagnostics.Append(req.Identity.Get(ctx, &identity)...)
		pool = identity.Pool.ValueString()
	}
	client, diags := r.poolClient(ctx, pool)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	got, err := nodegroup.Get(ctx, client, clusterID, nodegroupID)
	if isMKSV2NotFound(err) {
		resp.State.RemoveResource(ctx)

		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Error reading node group", errGettingObject(objectNodegroup, state.ID.ValueString(), err).Error())

		return
	}

	prior := state
	resp.Diagnostics.Append(state.fromAPI(ctx, got, prior, false)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
	resp.Diagnostics.Append(state.setIdentity(ctx, resp.Identity)...)
}

func (r *mksNodegroupV2Resource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state mksNodegroupV2Model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	timeout, diags := plan.Timeouts.Update(ctx, mksClusterV2DefaultTimeout)
	resp.Diagnostics.Append(diags...)
	client, diags := r.nodegroupClient(ctx, state.Segment)
	resp.Diagnostics.Append(diags...)
	patch, changed, diags := expandMKSNodegroupV2Patch(ctx, plan, state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	clusterID, nodegroupID := state.ClusterID.ValueString(), state.nodegroupID()
	if changed {
		err := mksNodegroupV2CallAndWait(ctx, client, clusterID, nodegroupID, func() error {
			return nodegroup.Patch(ctx, client, clusterID, nodegroupID, patch)
		})
		if err != nil {
			resp.Diagnostics.AddError("Error updating node group", errUpdatingObject(objectNodegroup, state.ID.ValueString(), err).Error())

			return
		}
	}

	if !plan.Count.IsUnknown() && !plan.Count.Equal(state.Count) && !plan.EnableAutoscale.ValueBool() {
		err := mksNodegroupV2CallAndWait(ctx, client, clusterID, nodegroupID, func() error {
			return nodegroup.Resize(ctx, client, clusterID, nodegroupID, plan.Count.ValueInt64())
		})
		if err != nil {
			resp.Diagnostics.AddError("Error resizing node group", errUpdatingObject(objectNodegroup, state.ID.ValueString(), err).Error())

			return
		}
	}

	got, err := nodegroup.Get(ctx, client, clusterID, nodegroupID)
	if err != nil {
		resp.Diagnostics.AddError("Error reading node group", errGettingObject(objectNodegroup, state.ID.ValueString(), err).Error())

		return
	}
	resp.Diagnostics.Append(plan.fromAPI(ctx, got, plan, true)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
	resp.Diagnostics.Append(plan.setIdentity(ctx, resp.Identity)...)
	resp.Diagnostics.Append(resp.Private.SetKey(ctx, mksNodegroupV2ImportedKey, nil)...)
}

func (r *mksNodegroupV2Resource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state mksNodegroupV2Model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	timeout, diags := state.Timeouts.Delete(ctx, mksClusterV2DefaultTimeout)
	resp.Diagnostics.Append(diags...)
	client, diags := r.nodegroupClient(ctx, state.Segment)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	clusterID, nodegroupID := state.ClusterID.ValueString(), state.nodegroupID()
	waiter, err := func() (*mksV2TaskWaiter, error) {
		selMutexKV.Lock(clusterID)
		defer selMutexKV.Unlock(clusterID)

		waiter, err := newMKSV2TaskWaiter(ctx, client, clusterID, nodegroupID)
		if err != nil {
			return nil, err
		}

		return waiter, nodegroup.Delete(ctx, client, clusterID, nodegroupID)
	}()
	if isMKSV2NotFound(err) {
		return
	}
	if err == nil {
		err = waiter.WaitNodegroupDeleted(ctx)
	}
	if err != nil {
		resp.Diagnostics.AddError("Error deleting node group", errDeletingObject(objectNodegroup, state.ID.ValueString(), err).Error())
	}
}

// ImportState takes the project from the provider configuration, and the pool
// from the identity or, for import by ID, from the provider configuration,
// because the ID carries neither, like selectel_mks_nodegroup_v1. Read then
// sets the segment, which gives the pool from then on.
func (r *mksNodegroupV2Resource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if req.ID == "" {
		var identity mksNodegroupV2IdentityModel
		resp.Diagnostics.Append(req.Identity.Get(ctx, &identity)...)
		if resp.Diagnostics.HasError() {
			return
		}
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"),
			identity.ClusterID.ValueString()+"/"+identity.ID.ValueString())...)
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("cluster_id"), identity.ClusterID)...)
		resp.Diagnostics.Append(resp.Private.SetKey(ctx, mksNodegroupV2ImportedKey, []byte("true"))...)

		return
	}

	if r.config == nil || r.config.ProjectID == "" {
		resp.Diagnostics.AddError("Missing project ID", "INFRA_PROJECT_ID must be set for the resource import")

		return
	}
	if r.config.Region == "" {
		resp.Diagnostics.AddError("Missing pool", "INFRA_REGION must be set for the resource import")

		return
	}
	clusterID, _, err := mksNodegroupV1ParseID(req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error()+", want <cluster_id>/<nodegroup_id>")

		return
	}

	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("cluster_id"), clusterID)...)
	resp.Diagnostics.Append(resp.Private.SetKey(ctx, mksNodegroupV2ImportedKey, []byte("true"))...)
}

const (
	mksNodegroupV2ImportedKey                      = "imported"
	mksNodegroupV2ReplaceUnlessImportedDescription = "Changing the value recreates the node group, " +
		"unless the node group was imported and the state has no value yet."
)

// mksNodegroupV2ReplaceUnlessImported requires a replacement for a changed
// value the API never returns, unless the state lacks it because of an
// import; the first apply after the import clears that mark.
func mksNodegroupV2ReplaceUnlessImported(ctx context.Context, stateNull bool, getKey func(context.Context, string) ([]byte, diag.Diagnostics)) bool {
	if !stateNull {
		return true
	}
	imported, _ := getKey(ctx, mksNodegroupV2ImportedKey)

	return len(imported) == 0
}

// nodegroupClient builds the client for the provider project and the pool of
// the segment, or of the provider before an import has read the segment.
func (r *mksNodegroupV2Resource) nodegroupClient(ctx context.Context, segment types.String) (*mksv2.ServiceClient, diag.Diagnostics) {
	pool := ""
	if !segment.IsNull() && !segment.IsUnknown() {
		pool = mksNodegroupV2Pool(segment.ValueString())
	}

	return r.poolClient(ctx, pool)
}

// poolClient builds the client for the provider project and the pool, or the
// pool of the provider when pool is empty.
func (r *mksNodegroupV2Resource) poolClient(ctx context.Context, pool string) (*mksv2.ServiceClient, diag.Diagnostics) {
	var diags diag.Diagnostics

	if r.config == nil || r.config.ProjectID == "" {
		diags.AddError("Missing project ID",
			"selectel_mks_nodegroup_v2 takes the project from the provider: set project_id in the provider configuration "+
				"or the INFRA_PROJECT_ID environment variable.")

		return nil, diags
	}
	if pool == "" {
		pool = r.config.Region
	}
	if pool == "" {
		diags.AddError("Missing pool", "INFRA_REGION must be set for the resource import")

		return nil, diags
	}

	client, _, d := r.client(ctx, types.StringValue(r.config.ProjectID), pool)
	diags.Append(d...)

	return client, diags
}

// mksNodegroupV2Pool removes the zone letter from a segment: ru-3a is in ru-3.
func mksNodegroupV2Pool(segment string) string {
	return strings.TrimRight(segment, "abcdefghijklmnopqrstuvwxyz")
}

// mksNodegroupV2CheckCluster fails for a cluster whose workers_type is
// DEDICATED: mk-api-v2 accepts only dedicated node groups there.
func mksNodegroupV2CheckCluster(ctx context.Context, client *mksv2.ServiceClient, clusterID string) error {
	c, err := cluster.Get(ctx, client, clusterID)
	if err != nil {
		return err
	}
	if c.NetworkType == mksclient.ClusterDetailedNetworkTypeL3VPN {
		return fmt.Errorf("cluster %s has workers_type = DEDICATED and accepts only dedicated node groups, "+
			"while cloud_nodegroup_config creates a cloud one; changing workers_type of the cluster recreates the cluster", clusterID)
	}

	return nil
}

// createMKSNodegroupV2 creates the node group and finds its ID: the API
// returns none, so it is the one listed after the call and absent before it.
// The lock keeps the provider's other node groups of the cluster out of the
// difference.
func createMKSNodegroupV2(ctx context.Context, client *mksv2.ServiceClient, clusterID string, opts mksclient.NodegroupCreateStruct) (string, error) {
	selMutexKV.Lock(clusterID)
	defer selMutexKV.Unlock(clusterID)

	before, err := nodegroup.List(ctx, client, clusterID)
	if err != nil {
		return "", errGettingObject("all nodegroups in the cluster", clusterID, err)
	}
	known := make(map[string]struct{}, len(before))
	for _, ng := range before {
		known[ng.Id] = struct{}{}
	}

	err = nodegroup.Create(ctx, client, clusterID, []mksclient.NodegroupCreateStruct{opts})
	if err != nil {
		return "", err
	}

	after, err := nodegroup.List(ctx, client, clusterID)
	if err != nil {
		return "", errGettingObject("all nodegroups in the cluster", clusterID, err)
	}
	var created []string
	for _, ng := range after {
		if _, ok := known[ng.Id]; !ok {
			created = append(created, ng.Id)
		}
	}
	if len(created) != 1 {
		return "", fmt.Errorf("can't find the created node group in cluster %s: %d new node groups listed, want 1", clusterID, len(created))
	}

	return created[0], nil
}

// mksNodegroupV2CallAndWait runs a mutating call on the node group under the
// cluster lock and waits for the node group tasks it created.
func mksNodegroupV2CallAndWait(ctx context.Context, client *mksv2.ServiceClient, clusterID, nodegroupID string, call func() error) error {
	waiter, err := func() (*mksV2TaskWaiter, error) {
		selMutexKV.Lock(clusterID)
		defer selMutexKV.Unlock(clusterID)

		waiter, err := newMKSV2TaskWaiter(ctx, client, clusterID, nodegroupID)
		if err != nil {
			return nil, err
		}

		return waiter, call()
	}()
	if err != nil {
		return err
	}

	return waiter.Wait(ctx)
}

func (m *mksNodegroupV2Model) nodegroupID() string {
	_, nodegroupID, _ := mksNodegroupV1ParseID(m.ID.ValueString())

	return nodegroupID
}

func expandMKSNodegroupV2CreateOpts(ctx context.Context, plan mksNodegroupV2Model) (mksclient.NodegroupCreateStruct, diag.Diagnostics) {
	opts := mksclient.NodegroupCreateStruct{
		Count:                     plan.Count.ValueInt64(),
		Segment:                   plan.Segment.ValueString(),
		Cidr:                      plan.CIDR.ValueString(),
		EnableAutoscale:           knownBoolPointer(plan.EnableAutoscale),
		AutoscaleMinNodes:         knownInt64Pointer(plan.AutoscaleMinNodes),
		AutoscaleMaxNodes:         knownInt64Pointer(plan.AutoscaleMaxNodes),
		InstallNvidiaDevicePlugin: knownBoolPointer(plan.InstallNvidiaDevicePlugin),
		Preemptible:               plan.Preemptible.ValueBool(),
		UserData:                  plan.UserData.ValueString(),
	}
	if plan.Count.IsNull() || plan.Count.IsUnknown() {
		// Only possible with autoscaling, see ValidateConfig.
		opts.Count = plan.AutoscaleMinNodes.ValueInt64()
	}

	var diags diag.Diagnostics
	if !plan.Labels.IsNull() && !plan.Labels.IsUnknown() {
		labels := map[string]string{}
		diags.Append(plan.Labels.ElementsAs(ctx, &labels, false)...)
		opts.Labels = &labels
	}
	if !plan.Taints.IsNull() && !plan.Taints.IsUnknown() {
		taints, d := expandMKSNodegroupV2Taints(ctx, plan.Taints)
		diags.Append(d...)
		opts.Taints = &taints
	}

	var cloud mksNodegroupV2CloudConfigModel
	diags.Append(plan.CloudNodegroupConfig.As(ctx, &cloud, basetypes.ObjectAsOptions{})...)
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

// expandMKSNodegroupV2Patch collects the changed in-place fields. Labels and
// taints go whole; the autoscale fields go together, like _v1 sent them.
func expandMKSNodegroupV2Patch(ctx context.Context, plan, state mksNodegroupV2Model) (mksclient.NodegroupUpdateStruct, bool, diag.Diagnostics) {
	var diags diag.Diagnostics
	patch := mksclient.NodegroupUpdateStruct{}
	changed := false

	if !plan.Labels.IsUnknown() && !plan.Labels.Equal(state.Labels) {
		labels := map[string]string{}
		diags.Append(plan.Labels.ElementsAs(ctx, &labels, false)...)
		patch.Labels = &labels
		changed = true
	}
	if !plan.Taints.IsUnknown() && !plan.Taints.Equal(state.Taints) {
		taints, d := expandMKSNodegroupV2Taints(ctx, plan.Taints)
		diags.Append(d...)
		patch.Taints = &taints
		changed = true
	}
	if !plan.EnableAutoscale.Equal(state.EnableAutoscale) || !plan.AutoscaleMinNodes.Equal(state.AutoscaleMinNodes) ||
		!plan.AutoscaleMaxNodes.Equal(state.AutoscaleMaxNodes) {
		patch.EnableAutoscale = knownBoolPointer(plan.EnableAutoscale)
		patch.AutoscaleMinNodes = knownIntPointer(plan.AutoscaleMinNodes)
		patch.AutoscaleMaxNodes = knownIntPointer(plan.AutoscaleMaxNodes)
		changed = true
	}

	return patch, changed, diags
}

// expandMKSNodegroupV2Taints never returns nil, so an empty list clears the
// taints.
func expandMKSNodegroupV2Taints(ctx context.Context, list types.List) ([]mksclient.NodegroupTaint, diag.Diagnostics) {
	var models []mksNodegroupV2TaintModel
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

// fromAPI maps the node group onto the model. The API never returns cpus,
// ram_mb, affinity_policy, and cidr of a cloud node group, so
// they come from prior. After an apply count is the planned one: the
// autoscaler may move the nodes at any time.
func (m *mksNodegroupV2Model) fromAPI(ctx context.Context, ng *mksclient.NodegroupDetailed, prior mksNodegroupV2Model, applied bool) diag.Diagnostics {
	var diags diag.Diagnostics

	m.ID = types.StringValue(ng.ClusterId + "/" + ng.Id)
	m.ClusterID = types.StringValue(ng.ClusterId)
	m.Segment = types.StringValue(ng.Segment)
	if !applied || prior.Count.IsNull() || prior.Count.IsUnknown() {
		m.Count = types.Int64Value(int64(len(ng.Nodes)))
	}
	m.CIDR = knownStringOrNull(prior.CIDR)
	if ng.Cidr != nil {
		m.CIDR = types.StringValue(*ng.Cidr)
	}
	m.EnableAutoscale = types.BoolValue(ng.EnableAutoscale)
	m.AutoscaleMinNodes = types.Int64PointerValue(ng.AutoscaleMinNodes)
	m.AutoscaleMaxNodes = types.Int64PointerValue(ng.AutoscaleMaxNodes)
	m.UserData = types.StringValue(ng.UserData)
	m.InstallNvidiaDevicePlugin = types.BoolValue(ng.InstallNvidiaDevicePlugin)
	m.Preemptible = types.BoolValue(ng.Preemptible)
	m.NodegroupType = types.StringValue(string(ng.NodegroupType))
	m.Status = types.StringValue(string(ng.Status))

	labels := ng.Labels
	if labels == nil {
		labels = map[string]string{}
	}
	var d diag.Diagnostics
	m.Labels, d = types.MapValueFrom(ctx, types.StringType, labels)
	diags.Append(d...)

	taints := make([]mksNodegroupV2TaintModel, 0, len(ng.Taints))
	for _, t := range ng.Taints {
		taints = append(taints, mksNodegroupV2TaintModel{
			Key:    types.StringValue(t.Key),
			Value:  types.StringValue(t.Value),
			Effect: types.StringValue(string(t.Effect)),
		})
	}
	m.Taints, d = types.ListValueFrom(ctx, types.ObjectType{AttrTypes: mksNodegroupV2TaintAttrTypes}, taints)
	diags.Append(d...)

	nodes := make([]mksNodegroupV2NodeModel, 0, len(ng.Nodes))
	for _, n := range ng.Nodes {
		nodes = append(nodes, mksNodegroupV2NodeModel{
			ID:       types.StringValue(n.Id),
			IP:       types.StringValue(n.Ip),
			Hostname: types.StringValue(n.Hostname),
		})
	}
	m.Nodes, d = types.ListValueFrom(ctx, types.ObjectType{AttrTypes: mksNodegroupV2NodeAttrTypes}, nodes)
	diags.Append(d...)

	cloud, d := flattenMKSNodegroupV2CloudConfig(ctx, ng.CloudNodegroupConfig, prior.CloudNodegroupConfig)
	diags.Append(d...)
	m.CloudNodegroupConfig = cloud

	return diags
}

func flattenMKSNodegroupV2CloudConfig(ctx context.Context, info *mksclient.CloudNodegroupConfigInfo, prior types.Object) (types.Object, diag.Diagnostics) {
	var diags diag.Diagnostics

	priorConfig := mksNodegroupV2CloudConfigModel{
		FlavorID:       types.StringNull(),
		CPUs:           types.Int64Null(),
		RAMMB:          types.Int64Null(),
		VolumeGB:       types.Int64Null(),
		VolumeType:     types.StringNull(),
		LocalVolume:    types.BoolNull(),
		AffinityPolicy: types.StringNull(),
	}
	if !prior.IsNull() && !prior.IsUnknown() {
		diags.Append(prior.As(ctx, &priorConfig, basetypes.ObjectAsOptions{UnhandledUnknownAsEmpty: true})...)
	}
	if info == nil {
		// Not a cloud node group.
		return prior, diags
	}

	knownInt64 := func(v types.Int64) types.Int64 {
		if v.IsUnknown() {
			return types.Int64Null()
		}

		return v
	}
	obj, d := types.ObjectValueFrom(ctx, mksNodegroupV2CloudConfigAttrTypes, mksNodegroupV2CloudConfigModel{
		FlavorID:       types.StringValue(info.FlavorId),
		CPUs:           knownInt64(priorConfig.CPUs),
		RAMMB:          knownInt64(priorConfig.RAMMB),
		VolumeGB:       types.Int64Value(info.VolumeGb),
		VolumeType:     types.StringValue(info.VolumeType),
		LocalVolume:    types.BoolValue(info.LocalVolume),
		AffinityPolicy: knownStringOrNull(priorConfig.AffinityPolicy),
	})
	diags.Append(d...)

	return obj, diags
}

func knownStringOrNull(v types.String) types.String {
	if v.IsUnknown() {
		return types.StringNull()
	}

	return v
}

// knownInt64Pointer is nil for a null or unknown value.
func knownInt64Pointer(v types.Int64) *int64 {
	if v.IsUnknown() {
		return nil
	}

	return v.ValueInt64Pointer()
}

// knownIntPointer is knownInt64Pointer for the int fields of the patch body.
func knownIntPointer(v types.Int64) *int {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}

	return new(int(v.ValueInt64()))
}
