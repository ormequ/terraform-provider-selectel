package selectel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/resourcevalidator"
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
	_ resource.ResourceWithConfigure        = &mksNodegroupV2Resource{}
	_ resource.ResourceWithConfigValidators = &mksNodegroupV2Resource{}
	_ resource.ResourceWithIdentity         = &mksNodegroupV2Resource{}
	_ resource.ResourceWithImportState      = &mksNodegroupV2Resource{}
	_ resource.ResourceWithModifyPlan       = &mksNodegroupV2Resource{}
	_ resource.ResourceWithMoveState        = &mksNodegroupV2Resource{}
	_ resource.ResourceWithValidateConfig   = &mksNodegroupV2Resource{}
)

var mksNodegroupV2Docs = resourceDocs{Name: "node group"}

// mksNodegroupV2SegmentRegexp matches a pool segment such as ru-3a. The
// segment of a dedicated node group is a dedicated server location such as
// SPB-3 instead.
var mksNodegroupV2SegmentRegexp = regexp.MustCompile(`^[a-z]+-\d+[a-z]$`)

// mksNodegroupV2UUIDRegexp matches the UUIDs mk-api-v2 requires for
// service_uuid (validate/nodegroup.go:328).
var mksNodegroupV2UUIDRegexp = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

const (
	// mksNodegroupV2DedicatedMinRootSizeGB is minDedicatedRootSize of
	// mk-api-v2 validate/nodegroup.go:30.
	mksNodegroupV2DedicatedMinRootSizeGB = 30
	// mksNodegroupV2DedicatedMinNodes is the default
	// min_nodes_per_dedicated_group of mk-api-v2 config/config.go:59.
	mksNodegroupV2DedicatedMinNodes = 1
	// mksNodegroupV2DedicatedCreateTimeout covers the waits mk-cluster-bm
	// allows itself for one dedicated node group: 60m for the servers to
	// become active, 20m for the network, 60m for the reinstall and a 15m
	// pause after it (internal/pkg/actions/dedicated/create_dedicated_nodegroup.go:37-39,
	// setup_servers.go:41, common.go:106 with config.go:229-230), plus the
	// Kubernetes setup of the nodes.
	mksNodegroupV2DedicatedCreateTimeout = 160 * time.Minute
	// mksNodegroupV2DedicatedCIDRSize is the only prefix length mk-api-v2
	// accepts for the cidr of a dedicated node group (validate/network.go:30).
	mksNodegroupV2DedicatedCIDRSize = 24
)

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
	mksNodegroupV2DedicatedConfigAttrTypes = map[string]attr.Type{
		"service_uuid":             types.StringType,
		"price_plan_name":          types.StringType,
		"price_plan_uuid":          types.StringType,
		"root_size_gb":             types.Int64Type,
		"create_storage_partition": types.BoolType,
		"currency":                 types.StringType,
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
	DedicatedNodegroupConfig  types.Object   `tfsdk:"dedicated_nodegroup_config"`
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

type mksNodegroupV2DedicatedConfigModel struct {
	ServiceUUID            types.String `tfsdk:"service_uuid"`
	PricePlanName          types.String `tfsdk:"price_plan_name"`
	PricePlanUUID          types.String `tfsdk:"price_plan_uuid"`
	RootSizeGB             types.Int64  `tfsdk:"root_size_gb"`
	CreateStoragePartition types.Bool   `tfsdk:"create_storage_partition"`
	Currency               types.String `tfsdk:"currency"`
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
		Description: "Creates and manages a Managed Kubernetes node group of cloud or dedicated servers using API v2.",
		Attributes: mksNodegroupV2Docs.withFrameworkDocsHints(map[string]schema.Attribute{
			"id": mksNodegroupV2Docs.idFrameworkResourceSchema(),
			"cluster_id": schema.StringAttribute{
				Required:      true,
				Description:   "Unique identifier of the cluster.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"segment": schema.StringAttribute{
				Required: true,
				Description: "Pool segment where all nodes of the node group are located, for example, `ru-7a`; " +
					"for a dedicated node group, the dedicated server location, for example, `SPB-3`.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"nodes_count": schema.Int64Attribute{
				Optional: true,
				Computed: true,
				Description: "Number of nodes in the node group. Required unless `enable_autoscale` is `true`. " +
					"Changing it resizes the node group. While autoscaling is enabled, changes made by the autoscaler " +
					"or in the configuration are ignored.",
			},
			"cidr": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Description: "CIDR of the node group network, a private /24 such as `10.20.30.0/24`. " +
					"Applies to dedicated node groups only: setting it for a cloud node group fails the plan.",
				PlanModifiers: replaceStringUnlessImported,
			},
			"labels": schema.MapAttribute{
				Optional:    true,
				Computed:    true,
				ElementType: types.StringType,
				Description: "Kubernetes labels applied to each node in the node group. " +
					"Removing the `labels` argument keeps the current labels; set an empty map to remove them.",
				PlanModifiers: []planmodifier.Map{mapplanmodifier.UseStateForUnknown()},
			},
			"taints": schema.ListNestedAttribute{
				Optional: true,
				Computed: true,
				Description: "Kubernetes taints applied to each node in the node group. " +
					"Removing the `taints` argument keeps the current taints; set an empty list to remove them.",
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
				Description:   "Enables autoscaling of the node group within `autoscale_min_nodes` and `autoscale_max_nodes`. Cloud node groups only.",
				PlanModifiers: keepBool,
			},
			"autoscale_min_nodes": schema.Int64Attribute{
				Optional:      true,
				Computed:      true,
				Description:   "Minimum number of nodes while autoscaling is enabled. Cloud node groups only.",
				PlanModifiers: keepInt64,
			},
			"autoscale_max_nodes": schema.Int64Attribute{
				Optional:      true,
				Computed:      true,
				Description:   "Maximum number of nodes while autoscaling is enabled. Cloud node groups only.",
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
					"If omitted, the API enables it for flavors and dedicated servers with GPU.",
				PlanModifiers: replaceBool,
			},
			"preemptible": schema.BoolAttribute{
				Optional:      true,
				Computed:      true,
				Description:   "Makes the nodes preemptible. Cloud node groups only.",
				PlanModifiers: replaceBool,
			},
			"cloud_nodegroup_config": schema.SingleNestedAttribute{
				Optional: true,
				Description: "Cloud server configuration of the nodes. Changing any of its values recreates the node group. " +
					"Exactly one of cloud_nodegroup_config and dedicated_nodegroup_config is required; " +
					"a cluster with workers_type = CLOUD accepts only this one.",
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
						Description: "Volume size of each node in GB. Omit it with `flavor_id`: the API takes it from a flavor " +
							"that defines a volume, and a different configured value fails the apply.",
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
						Optional: true,
						Computed: true,
						Description: "Makes the nodes use a local volume instead of a network one. Omit it with `flavor_id`: " +
							"the API sets it for a flavor with a local disk, and a different configured value fails the apply.",
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
			"dedicated_nodegroup_config": schema.SingleNestedAttribute{
				Optional: true,
				Description: "Dedicated server configuration of the nodes. Changing any of its values recreates the node group. " +
					"A cluster with workers_type = DEDICATED accepts only this one.",
				Attributes: map[string]schema.Attribute{
					"service_uuid": schema.StringAttribute{
						Required:      true,
						Description:   "Unique identifier of the dedicated server configuration, `configurations[].id` of selectel_dedicated_configuration_v1.",
						PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
						Validators:    []validator.String{stringvalidator.RegexMatches(mksNodegroupV2UUIDRegexp, "must be a UUID")},
					},
					"price_plan_name": schema.StringAttribute{
						Required:      true,
						Description:   "Name of the price plan of the servers, for example, `1 day`.",
						PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
					},
					"price_plan_uuid": schema.StringAttribute{
						Computed:      true,
						Description:   "Unique identifier of the price plan that price_plan_name names.",
						PlanModifiers: keepString,
					},
					"root_size_gb": schema.Int64Attribute{
						Optional:      true,
						Computed:      true,
						Description:   "Size of the root partition of each server in GB, at least 30. If omitted, the API uses 100.",
						PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown(), int64planmodifier.RequiresReplace()},
						Validators:    []validator.Int64{int64validator.AtLeast(mksNodegroupV2DedicatedMinRootSizeGB)},
					},
					"create_storage_partition": schema.BoolAttribute{
						Optional:      true,
						Computed:      true,
						Description:   "Creates a storage partition on the fastest disk of each server. If omitted, the API uses `true`.",
						PlanModifiers: replaceBool,
					},
					"currency": schema.StringAttribute{
						Optional:      true,
						Computed:      true,
						Description:   "Balance that pays for the servers ordered at create: `main` or `bonus`. If omitted, the API uses `main`. A resize always pays from `main`.",
						PlanModifiers: replaceStringUnlessImported,
						Validators: []validator.String{stringvalidator.OneOf(
							string(mksclient.Main), string(mksclient.Bonus),
						)},
					},
				},
			},
			"nodegroup_type": schema.StringAttribute{
				Computed:      true,
				Description:   "Type of the node group: `STANDARD`, `GPU`, `DEDICATED` or `DEDICATED_GPU`.",
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

// setIdentity is a no-op when Terraform passed no identity. pool is the one
// the node group operations use.
func (m *mksNodegroupV2Model) setIdentity(ctx context.Context, identity *tfsdk.ResourceIdentity, pool string) diag.Diagnostics {
	if identity == nil {
		return nil
	}

	return identity.Set(ctx, mksNodegroupV2IdentityModel{
		ClusterID: m.ClusterID,
		ID:        types.StringValue(m.nodegroupID()),
		Pool:      types.StringValue(pool),
	})
}

// ConfigValidators mirrors mk-api-v2, which rejects both configs and neither
// (validate/nodegroup.go:97-106).
func (r *mksNodegroupV2Resource) ConfigValidators(_ context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{
		resourcevalidator.ExactlyOneOf(path.MatchRoot("cloud_nodegroup_config"), path.MatchRoot("dedicated_nodegroup_config")),
	}
}

// ValidateConfig rejects the combinations the API rejects and that depend on
// values, not only on presence: cloud_nodegroup_config ones (issues #273 and
// #300, see mk-api-v2 validate/nodegroup.go and validate/flavor.go), and
// dedicated_nodegroup_config ones.
func (r *mksNodegroupV2Resource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var config mksNodegroupV2Model
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if config.Count.IsNull() && !config.EnableAutoscale.IsUnknown() && !config.EnableAutoscale.ValueBool() {
		detail := "Set nodes_count, or enable autoscaling with enable_autoscale = true."
		if !config.DedicatedNodegroupConfig.IsNull() {
			detail = "Set nodes_count: dedicated node groups do not support autoscaling."
		}
		resp.Diagnostics.AddAttributeError(path.Root("nodes_count"), "Missing node count", detail)
	}
	if config.EnableAutoscale.ValueBool() && (config.AutoscaleMinNodes.IsNull() || config.AutoscaleMaxNodes.IsNull()) {
		resp.Diagnostics.AddAttributeError(path.Root("enable_autoscale"), "Missing autoscaling limits",
			"enable_autoscale = true requires both autoscale_min_nodes and autoscale_max_nodes.")
	}
	// The API stores a cidr as the network of a dedicated node group and
	// validates it only for those, see mk-api-v2 handlers/nodegroups/create.go.
	if !config.CIDR.IsNull() && !config.CIDR.IsUnknown() && config.DedicatedNodegroupConfig.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("cidr"), "Unsupported cidr",
			"cidr applies to dedicated node groups only; remove it from a cloud node group.")
	}

	if !config.DedicatedNodegroupConfig.IsNull() && !config.DedicatedNodegroupConfig.IsUnknown() {
		validateMKSNodegroupV2Dedicated(config, &resp.Diagnostics)
	}

	if config.CloudNodegroupConfig.IsNull() || config.CloudNodegroupConfig.IsUnknown() {
		return
	}
	if !config.Segment.IsUnknown() && config.DedicatedNodegroupConfig.IsNull() &&
		!mksNodegroupV2SegmentRegexp.MatchString(config.Segment.ValueString()) {
		resp.Diagnostics.AddAttributeError(path.Root("segment"), "Invalid segment",
			"A cloud node group takes a pool segment such as `ru-7a`.")
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
	if config.InstallNvidiaDevicePlugin.ValueBool() && !cloud.CPUs.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("install_nvidia_device_plugin"), "Conflicting node flavor",
			"install_nvidia_device_plugin = true needs a GPU flavor: set flavor_id instead of cpus and ram_mb.")
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

// validateMKSNodegroupV2Dedicated mirrors the checks of mk-api-v2 for a
// dedicated node group that need no API call.
func validateMKSNodegroupV2Dedicated(config mksNodegroupV2Model, diags *diag.Diagnostics) {
	// A dedicated location such as SPB-3 is the segment; a pool segment is
	// never one of the dedicated pools of a region (validate/nodegroup.go:340-349,
	// validate/dedicated.go:75-81).
	if !config.Segment.IsUnknown() && mksNodegroupV2SegmentRegexp.MatchString(config.Segment.ValueString()) {
		diags.AddAttributeError(path.Root("segment"), "Invalid segment",
			"A dedicated node group takes a dedicated server location such as `SPB-3`, not a pool segment.")
	}
	// The PATCH of a dedicated node group rejects any autoscale field
	// (validate/nodegroup.go:257-263), and create ignores them
	// (daladapter/worker_group.go:62-64).
	for _, autoscale := range []struct {
		name  string
		value attr.Value
	}{
		{"enable_autoscale", config.EnableAutoscale},
		{"autoscale_min_nodes", config.AutoscaleMinNodes},
		{"autoscale_max_nodes", config.AutoscaleMaxNodes},
	} {
		if !autoscale.value.IsNull() {
			diags.AddAttributeError(path.Root(autoscale.name), "Autoscaling of a dedicated node group",
				"Dedicated node groups do not support autoscaling: remove "+autoscale.name+".")
		}
	}
	// Dedicated nodes are never preemptible: the API stores false
	// (daladapter/worker_group.go:66).
	if config.Preemptible.ValueBool() {
		diags.AddAttributeError(path.Root("preemptible"), "Preemptible dedicated node group",
			"Dedicated node groups cannot be preemptible.")
	}
	// validate/nodegroup.go:302 with the default minimum.
	if !config.Count.IsNull() && !config.Count.IsUnknown() && config.Count.ValueInt64() < mksNodegroupV2DedicatedMinNodes {
		diags.AddAttributeError(path.Root("nodes_count"), "Too few nodes",
			fmt.Sprintf("Dedicated node groups should have at least %d nodes.", mksNodegroupV2DedicatedMinNodes))
	}
	if !config.CIDR.IsNull() && !config.CIDR.IsUnknown() {
		err := validateMKSNodegroupV2DedicatedCIDR(config.CIDR.ValueString())
		if err != nil {
			diags.AddAttributeError(path.Root("cidr"), "Invalid cidr", err.Error())
		}
	}
}

// validateMKSNodegroupV2DedicatedCIDR mirrors the checks of mk-api-v2
// validate/network.go:11-34 that need no API configuration: a private, not
// loopback /24, and also that it is the network address.
func validateMKSNodegroupV2DedicatedCIDR(cidr string) error {
	ip, network, err := net.ParseCIDR(cidr)
	if err != nil {
		return fmt.Errorf("%q is not a CIDR such as 10.20.30.0/24", cidr)
	}
	if ip.IsLoopback() || !ip.IsPrivate() {
		return fmt.Errorf("%q must be a private network", cidr)
	}
	size, _ := network.Mask.Size()
	if size != mksNodegroupV2DedicatedCIDRSize {
		return fmt.Errorf("%q must be a /%d network", cidr, mksNodegroupV2DedicatedCIDRSize)
	}
	// The API stores the cidr in a PostgreSQL cidr column, which rejects host
	// bits with a 500 that does not name the cidr (mk-dal migration
	// 20240709164711-dedicated-nodegroup-network.sql:11).
	if !ip.Equal(network.IP) {
		return fmt.Errorf("%q has host bits set: use the network address %s", cidr, network)
	}

	return nil
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

	r.planPricePlan(ctx, plan, state, resp)
	resp.Diagnostics.Append(resp.Plan.Get(ctx, &plan)...)
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

// planPricePlan resolves price_plan_name into price_plan_uuid at plan time
// when the name is new or changed and known, so a wrong name fails the plan.
// An unknown name leaves the UUID unknown for Create to resolve.
func (r *mksNodegroupV2Resource) planPricePlan(ctx context.Context, plan, state mksNodegroupV2Model, resp *resource.ModifyPlanResponse) {
	if plan.DedicatedNodegroupConfig.IsNull() || plan.DedicatedNodegroupConfig.IsUnknown() {
		return
	}
	var planned, prior mksNodegroupV2DedicatedConfigModel
	resp.Diagnostics.Append(plan.DedicatedNodegroupConfig.As(ctx, &planned, basetypes.ObjectAsOptions{UnhandledUnknownAsEmpty: true})...)
	if !state.DedicatedNodegroupConfig.IsNull() && !state.DedicatedNodegroupConfig.IsUnknown() {
		resp.Diagnostics.Append(state.DedicatedNodegroupConfig.As(ctx, &prior, basetypes.ObjectAsOptions{})...)
	}
	if resp.Diagnostics.HasError() {
		return
	}
	if !planned.PricePlanUUID.IsUnknown() && planned.PricePlanName.Equal(prior.PricePlanName) {
		return
	}

	uuidPath := path.Root("dedicated_nodegroup_config").AtName("price_plan_uuid")
	if planned.PricePlanName.IsUnknown() {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, uuidPath, types.StringUnknown())...)

		return
	}
	pricePlanUUID, err := r.pricePlanUUID(ctx, planned.PricePlanName.ValueString())
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("dedicated_nodegroup_config").AtName("price_plan_name"),
			"Error resolving the price plan", err.Error())

		return
	}
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, uuidPath, pricePlanUUID)...)
}

// checkClusterWorkers fails the plan for a node group whose kind the cluster
// does not accept.
func (r *mksNodegroupV2Resource) checkClusterWorkers(ctx context.Context, plan mksNodegroupV2Model, diags *diag.Diagnostics) {
	if plan.ClusterID.IsUnknown() || plan.Segment.IsUnknown() || r.config == nil {
		return
	}

	client, _, d := r.nodegroupClient(ctx, plan.Segment, nil, nil)
	diags.Append(d...)
	if diags.HasError() {
		return
	}

	err := mksNodegroupV2CheckCluster(ctx, client, plan.ClusterID.ValueString(), plan.isDedicated())
	if isMKSV2NotFound(err) {
		// Create reports it.
		return
	}
	if err != nil {
		diags.AddAttributeError(path.Root("cluster_id"), "Node group kind not accepted by the cluster", err.Error())
	}
}

func (r *mksNodegroupV2Resource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan mksNodegroupV2Model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	timeout, diags := plan.Timeouts.Create(ctx, plan.defaultTimeout())
	resp.Diagnostics.Append(diags...)
	client, pool, diags := r.nodegroupClient(ctx, plan.Segment, nil, nil)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// The plan resolved the price plan unless its name was unknown then.
	resp.Diagnostics.Append(r.resolvePricePlan(ctx, &plan)...)
	opts, diags := expandMKSNodegroupV2CreateOpts(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	clusterID := plan.ClusterID.ValueString()
	err := mksNodegroupV2CheckCluster(ctx, client, clusterID, plan.isDedicated())
	if err != nil {
		err = mksNodegroupV2PoolNotFound(err, plan.isDedicated(), pool)
		resp.Diagnostics.AddAttributeError(path.Root("cluster_id"), "Error creating node group", errCreatingObject(objectNodegroup, err).Error())

		return
	}

	nodegroupID, err := createMKSNodegroupV2(ctx, client, clusterID, opts)
	if err != nil {
		err = mksNodegroupV2PoolNotFound(err, plan.isDedicated(), pool)
		resp.Diagnostics.AddError("Error creating node group", errCreatingObject(objectNodegroup, err).Error())

		return
	}
	resp.Diagnostics.Append(mksNodegroupV2SavePool(ctx, plan.isDedicated(), pool, nil, resp.Private)...)

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
	resp.Diagnostics.Append(state.setIdentity(ctx, resp.Identity, pool)...)
	if waitErr != nil {
		resp.Diagnostics.AddError("Error waiting for the node group to become ready", waitErr.Error())
	}
	resp.Diagnostics.Append(mksNodegroupV2CheckFlavorVolume(ctx, plan.CloudNodegroupConfig, got.CloudNodegroupConfig)...)
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
	client, pool, diags := r.nodegroupClient(ctx, state.Segment, req.Identity, req.Private)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	got, err := nodegroup.Get(ctx, client, clusterID, nodegroupID)
	if isMKSV2NotFound(err) {
		resp.Diagnostics.Append(r.checkMovedProject(ctx, req.Private)...)
		if !resp.Diagnostics.HasError() {
			resp.State.RemoveResource(ctx)
		}

		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Error reading node group", errGettingObject(objectNodegroup, state.ID.ValueString(), err).Error())

		return
	}
	resp.Diagnostics.Append(resp.Private.SetKey(ctx, mksNodegroupV2MovedProjectKey, nil)...)

	// A null segment means this is the read of the import itself.
	if !state.Segment.IsNull() {
		resp.Diagnostics.Append(mksNodegroupV2AgeImportMark(ctx, req.Private, resp.Private)...)
	}
	prior := state
	resp.Diagnostics.Append(state.fromAPI(ctx, got, prior, false)...)
	resp.Diagnostics.Append(r.readPricePlanName(ctx, &state)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
	resp.Diagnostics.Append(state.setIdentity(ctx, resp.Identity, pool)...)
	// The read of an import, or of a node group created before the pool was
	// saved.
	resp.Diagnostics.Append(mksNodegroupV2SavePool(ctx, state.isDedicated(), pool, req.Private, resp.Private)...)
}

// checkMovedProject fails a Read that got 404 for a node group moved from
// _v1 in another project than the provider one: mk-api-v2 answers 404 for a
// cluster of another project, and removing the node group from the state
// would plan a new one.
func (r *mksNodegroupV2Resource) checkMovedProject(ctx context.Context, private mksNodegroupV2PrivateState) diag.Diagnostics {
	raw, diags := private.GetKey(ctx, mksNodegroupV2MovedProjectKey)
	if len(raw) == 0 || diags.HasError() {
		return diags
	}
	var movedProject string
	err := json.Unmarshal(raw, &movedProject)
	if err != nil {
		diags.AddError("Error reading node group", "can't read the project_id moved from _v1: "+err.Error())

		return diags
	}
	providerProject := ""
	if r.config != nil {
		providerProject = r.config.ProjectID
	}
	if providerProject != movedProject {
		diags.AddError("Error reading node group", fmt.Sprintf("provider project %q is not the node group's project %q "+
			"(moved from _v1): set project_id of the provider to %q", providerProject, movedProject, movedProject))
	}

	return diags
}

func (r *mksNodegroupV2Resource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state mksNodegroupV2Model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	timeout, diags := plan.Timeouts.Update(ctx, plan.defaultTimeout())
	resp.Diagnostics.Append(diags...)
	client, pool, diags := r.nodegroupClient(ctx, state.Segment, req.Identity, req.Private)
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
			err = mksNodegroupV2PoolNotFound(err, state.isDedicated(), pool)
			resp.Diagnostics.AddError("Error updating node group", errUpdatingObject(objectNodegroup, state.ID.ValueString(), err).Error())

			return
		}
	}

	if !plan.Count.IsUnknown() && !plan.Count.Equal(state.Count) && !plan.EnableAutoscale.ValueBool() {
		err := mksNodegroupV2CallAndWait(ctx, client, clusterID, nodegroupID, func() error {
			return nodegroup.Resize(ctx, client, clusterID, nodegroupID, plan.Count.ValueInt64())
		})
		if err != nil {
			err = mksNodegroupV2PoolNotFound(err, state.isDedicated(), pool)
			resp.Diagnostics.AddError("Error resizing node group", errUpdatingObject(objectNodegroup, state.ID.ValueString(), err).Error())

			return
		}
	}

	got, err := nodegroup.Get(ctx, client, clusterID, nodegroupID)
	if err != nil {
		resp.Diagnostics.AddError("Error reading node group", errGettingObject(objectNodegroup, state.ID.ValueString(), err).Error())

		return
	}
	planned := plan.CloudNodegroupConfig
	resp.Diagnostics.Append(plan.fromAPI(ctx, got, plan, true)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
	resp.Diagnostics.Append(plan.setIdentity(ctx, resp.Identity, pool)...)
	resp.Diagnostics.Append(resp.Private.SetKey(ctx, mksNodegroupV2ImportedKey, nil)...)
	resp.Diagnostics.Append(mksNodegroupV2CheckFlavorVolume(ctx, planned, got.CloudNodegroupConfig)...)
}

func (r *mksNodegroupV2Resource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state mksNodegroupV2Model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	timeout, diags := state.Timeouts.Delete(ctx, state.defaultTimeout())
	resp.Diagnostics.Append(diags...)
	client, _, diags := r.nodegroupClient(ctx, state.Segment, req.Identity, req.Private)
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
		resp.Diagnostics.Append(resp.Private.SetKey(ctx, mksNodegroupV2ImportedKey, mksNodegroupV2ImportedNew)...)

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
	resp.Diagnostics.Append(resp.Private.SetKey(ctx, mksNodegroupV2ImportedKey, mksNodegroupV2ImportedNew)...)
}

const (
	mksNodegroupV2ImportedKey                      = "imported"
	mksNodegroupV2ReplaceUnlessImportedDescription = "Changing the value recreates the node group, " +
		"unless the node group was imported and the state has no value yet."
)

// The import mark is mksNodegroupV2ImportedNew until the first refresh after
// the import, then mksNodegroupV2ImportedRefreshed, and the refresh after that
// removes it. So it holds for every plan up to the first apply after the
// import, which removes it too when it updates the node group.
var (
	mksNodegroupV2ImportedNew       = []byte(`"imported"`)
	mksNodegroupV2ImportedRefreshed = []byte(`"refreshed"`)
)

// mksNodegroupV2PrivateState is the private state of a Read request or
// response.
type mksNodegroupV2PrivateState interface {
	GetKey(ctx context.Context, key string) ([]byte, diag.Diagnostics)
	SetKey(ctx context.Context, key string, value []byte) diag.Diagnostics
}

// mksNodegroupV2AgeImportMark moves the import mark one step on a refresh.
func mksNodegroupV2AgeImportMark(ctx context.Context, prior, next mksNodegroupV2PrivateState) diag.Diagnostics {
	mark, diags := prior.GetKey(ctx, mksNodegroupV2ImportedKey)
	switch {
	case len(mark) == 0:
	case string(mark) == string(mksNodegroupV2ImportedNew):
		diags.Append(next.SetKey(ctx, mksNodegroupV2ImportedKey, mksNodegroupV2ImportedRefreshed)...)
	default:
		diags.Append(next.SetKey(ctx, mksNodegroupV2ImportedKey, nil)...)
	}

	return diags
}

// mksNodegroupV2ReplaceUnlessImported requires a replacement for a changed
// value the API never returns, unless the state lacks it because of a recent
// import, see mksNodegroupV2AgeImportMark.
func mksNodegroupV2ReplaceUnlessImported(ctx context.Context, stateNull bool, getKey func(context.Context, string) ([]byte, diag.Diagnostics)) bool {
	if !stateNull {
		return true
	}
	imported, _ := getKey(ctx, mksNodegroupV2ImportedKey)

	return len(imported) == 0
}

// nodegroupClient builds the client for the provider project and returns it
// with its pool: the pool of a pool segment; otherwise, for a dedicated
// location such as SPB-3 or before an import has read the segment, the pool
// saved in the private state or the identity, which keep the one used before,
// or else the pool of the provider. private is nil before the node group
// exists.
func (r *mksNodegroupV2Resource) nodegroupClient(ctx context.Context, segment types.String, identity *tfsdk.ResourceIdentity,
	private mksNodegroupV2PrivateState,
) (*mksv2.ServiceClient, string, diag.Diagnostics) {
	var diags diag.Diagnostics
	knownSegment := !segment.IsNull() && !segment.IsUnknown()

	pool := ""
	if knownSegment && mksNodegroupV2SegmentRegexp.MatchString(segment.ValueString()) {
		pool = mksNodegroupV2Pool(segment.ValueString())
	}
	if pool == "" && private != nil {
		pool, diags = mksNodegroupV2SavedPool(ctx, private)
	}
	if pool == "" && identity != nil && !identity.Raw.IsNull() {
		var id mksNodegroupV2IdentityModel
		diags.Append(identity.Get(ctx, &id)...)
		pool = id.Pool.ValueString()
	}
	if pool == "" && r.config != nil {
		pool = r.config.Region
	}
	if pool == "" && knownSegment {
		diags.AddError("Missing pool", fmt.Sprintf("Segment %s of a dedicated node group is a server location, not a pool segment, "+
			"so the pool comes from the provider: set region in the provider configuration "+
			"or the INFRA_REGION environment variable to the pool of the cluster.", segment.ValueString()))

		return nil, "", diags
	}
	if diags.HasError() {
		return nil, "", diags
	}

	client, d := r.poolClient(ctx, pool)
	diags.Append(d...)

	return client, pool, diags
}

// mksNodegroupV2PoolKey holds the pool of a dedicated node group: its
// location tells none, and without it a later change of the provider region
// would make Read look in another pool, get 404 and drop the node group from
// the state on Terraform before 1.12, which sends no identity.
const mksNodegroupV2PoolKey = "pool"

// mksNodegroupV2SavedPool returns the pool saved in the private state, or "".
func mksNodegroupV2SavedPool(ctx context.Context, private mksNodegroupV2PrivateState) (string, diag.Diagnostics) {
	raw, diags := private.GetKey(ctx, mksNodegroupV2PoolKey)
	if len(raw) == 0 || diags.HasError() {
		return "", diags
	}
	var pool string
	err := json.Unmarshal(raw, &pool)
	if err != nil {
		diags.AddError("Error reading the pool of the node group", err.Error())
	}

	return pool, diags
}

// mksNodegroupV2SavePool saves the pool of a dedicated node group in next
// unless prior has one already; prior is nil in Create.
func mksNodegroupV2SavePool(ctx context.Context, dedicated bool, pool string, prior, next mksNodegroupV2PrivateState) diag.Diagnostics {
	if !dedicated || pool == "" || next == nil {
		return nil
	}
	if prior != nil {
		saved, diags := mksNodegroupV2SavedPool(ctx, prior)
		if saved != "" || diags.HasError() {
			return diags
		}
	}
	raw, err := json.Marshal(pool)
	if err != nil {
		var diags diag.Diagnostics
		diags.AddError("Error saving the pool of the node group", err.Error())

		return diags
	}

	return next.SetKey(ctx, mksNodegroupV2PoolKey, raw)
}

// mksNodegroupV2PoolNotFound names the pool in a 404 for a dedicated node
// group, whose pool comes from the provider at create, not from its segment.
func mksNodegroupV2PoolNotFound(err error, dedicated bool, pool string) error {
	if !dedicated || !isMKSV2NotFound(err) {
		return err
	}

	return fmt.Errorf("%w; looked up in pool %s: a dedicated node group takes the pool from the provider region "+
		"when it is created, so set region of the provider to the pool of the cluster", err, pool)
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

// mksNodegroupV2CheckCluster fails when the cluster does not accept the kind
// of node group: mk-api-v2 creates dedicated node groups only in L3VPN
// clusters and cloud ones only in STANDARD clusters
// (validate/nodegroup.go:309 and :391).
func mksNodegroupV2CheckCluster(ctx context.Context, client *mksv2.ServiceClient, clusterID string, dedicated bool) error {
	c, err := cluster.Get(ctx, client, clusterID)
	if err != nil {
		return err
	}
	l3vpn := c.NetworkType == mksclient.ClusterDetailedNetworkTypeL3VPN
	if l3vpn && !dedicated {
		return fmt.Errorf("cluster %s has workers_type = DEDICATED and accepts only dedicated node groups, "+
			"while cloud_nodegroup_config creates a cloud one; changing workers_type of the cluster recreates the cluster", clusterID)
	}
	if !l3vpn && dedicated {
		return fmt.Errorf("cluster %s has workers_type = CLOUD and accepts only cloud node groups, "+
			"while dedicated_nodegroup_config creates one of dedicated servers; changing workers_type of the cluster recreates the cluster", clusterID)
	}

	return nil
}

// resolvePricePlan sets price_plan_uuid of a dedicated node group when the
// plan could not resolve it.
func (r *mksNodegroupV2Resource) resolvePricePlan(ctx context.Context, plan *mksNodegroupV2Model) diag.Diagnostics {
	if !plan.isDedicated() {
		return nil
	}
	var cfg mksNodegroupV2DedicatedConfigModel
	diags := plan.DedicatedNodegroupConfig.As(ctx, &cfg, basetypes.ObjectAsOptions{UnhandledUnknownAsEmpty: true})
	if diags.HasError() || !cfg.PricePlanUUID.IsUnknown() {
		return diags
	}

	pricePlanUUID, err := r.pricePlanUUID(ctx, cfg.PricePlanName.ValueString())
	if err != nil {
		diags.AddAttributeError(path.Root("dedicated_nodegroup_config").AtName("price_plan_name"),
			"Error resolving the price plan", err.Error())

		return diags
	}
	cfg.PricePlanUUID = types.StringValue(pricePlanUUID)
	var d diag.Diagnostics
	plan.DedicatedNodegroupConfig, d = types.ObjectValueFrom(ctx, mksNodegroupV2DedicatedConfigAttrTypes, cfg)
	diags.Append(d...)

	return diags
}

// readPricePlanName fills price_plan_name from the UUID the API returns when
// the state has none, as after an import, and fails for a UUID the price plans
// lack, like selectel_dedicated_server_v1.
func (r *mksNodegroupV2Resource) readPricePlanName(ctx context.Context, m *mksNodegroupV2Model) diag.Diagnostics {
	if !m.isDedicated() {
		return nil
	}
	var cfg mksNodegroupV2DedicatedConfigModel
	diags := m.DedicatedNodegroupConfig.As(ctx, &cfg, basetypes.ObjectAsOptions{})
	if diags.HasError() || !cfg.PricePlanName.IsNull() {
		return diags
	}

	plans, err := r.pricePlans(ctx)
	if err != nil {
		diags.AddError("Error reading node group", err.Error())

		return diags
	}
	plan := plans.FindOneID(cfg.PricePlanUUID.ValueString())
	if plan == nil {
		// A null name would make the configured one replace the node group.
		diags.AddError("Error reading node group", fmt.Sprintf("price plan %s of the node group is not among the "+
			"price plans of the dedicated servers API, so price_plan_name can't be read", cfg.PricePlanUUID.ValueString()))

		return diags
	}
	cfg.PricePlanName = types.StringValue(plan.Name)
	var d diag.Diagnostics
	m.DedicatedNodegroupConfig, d = types.ObjectValueFrom(ctx, mksNodegroupV2DedicatedConfigAttrTypes, cfg)
	diags.Append(d...)

	return diags
}

// isDedicated tells a dedicated node group from a cloud one.
func (m *mksNodegroupV2Model) isDedicated() bool {
	return !m.DedicatedNodegroupConfig.IsNull() && !m.DedicatedNodegroupConfig.IsUnknown()
}

// defaultTimeout is the default of every timeout of the node group: a resize
// of a dedicated node group orders servers and a delete waits for their
// cancellation with the same mk-cluster-bm limits as a create
// (create_dedicated_servers.go:72-93, delete_dedicated_nodegroup.go:143-149).
func (m *mksNodegroupV2Model) defaultTimeout() time.Duration {
	if m.isDedicated() {
		return mksNodegroupV2DedicatedCreateTimeout
	}

	return mksClusterV2DefaultTimeout
}

// createMKSNodegroupV2 creates the node group and finds its ID: the API
// returns none, so it is the one listed after the call and absent before it.
// The lock keeps the provider's other node groups of the cluster out of the
// difference; the create options narrow out the ones created elsewhere.
func createMKSNodegroupV2(ctx context.Context, client *mksv2.ServiceClient, clusterID string, opts mksclient.NodegroupCreateStruct) (string, error) {
	selMutexKV.Lock(clusterID)
	defer selMutexKV.Unlock(clusterID)

	before, err := nodegroup.List(ctx, client, clusterID)
	if err != nil {
		return "", errGettingObject("all nodegroups in the cluster", clusterID, err)
	}
	known := make([]string, 0, len(before))
	for _, ng := range before {
		known = append(known, ng.Id)
	}

	err = nodegroup.Create(ctx, client, clusterID, []mksclient.NodegroupCreateStruct{opts})
	if err != nil {
		// The API may have stored the node group before it failed.
		listCtx, cancel := mksV2ReadAfterWaitContext(ctx)
		defer cancel()
		after, listErr := nodegroup.List(listCtx, client, clusterID)
		created := mksNodegroupV2NewIDs(known, after)
		if listErr == nil && len(created) > 0 {
			return "", fmt.Errorf("%w; cluster %s now has new node groups %s that may be this one: "+
				"import it as <cluster_id>/<nodegroup_id> or delete it", err, clusterID, strings.Join(created, ", "))
		}

		return "", err
	}

	after, err := mksNodegroupV2ListAfterCreate(ctx, client, clusterID)
	if err != nil {
		candidates := "any node group of the cluster, which had none before"
		if len(known) > 0 {
			candidates = "the node group not among " + strings.Join(known, ", ")
		}

		return "", fmt.Errorf("the node group was created in cluster %s, but listing the node groups failed, so its ID "+
			"is unknown: it is %s; import it as <cluster_id>/<nodegroup_id> or delete it: %w",
			clusterID, candidates, err)
	}
	created := mksNodegroupV2NewIDs(known, after)
	matching := mksNodegroupV2Matching(created, after, opts)
	if len(matching) == 1 {
		return matching[0], nil
	}
	candidates := created
	if len(matching) > 1 {
		candidates = matching
	}

	return "", fmt.Errorf("can't find the created node group in cluster %s: %d new node groups listed match it, want 1 "+
		"(candidates: %s); import the right one as <cluster_id>/<nodegroup_id>", clusterID, len(matching), strings.Join(candidates, ", "))
}

// mksNodegroupV2ListAfterCreate lists the node groups once more if the first
// attempt fails. It outlives ctx: the node group already exists.
func mksNodegroupV2ListAfterCreate(ctx context.Context, client *mksv2.ServiceClient, clusterID string) ([]mksclient.NodegroupListItem, error) {
	listCtx, cancel := mksV2ReadAfterWaitContext(ctx)
	defer cancel()

	after, err := nodegroup.List(listCtx, client, clusterID)
	if err == nil {
		return after, nil
	}
	select {
	case <-listCtx.Done():
		return nil, err
	case <-time.After(mksV2PollInterval):
	}

	after, retryErr := nodegroup.List(listCtx, client, clusterID)
	if retryErr != nil {
		return nil, errors.Join(err, retryErr)
	}

	return after, nil
}

// mksNodegroupV2NewIDs lists the IDs absent from known.
func mksNodegroupV2NewIDs(known []string, after []mksclient.NodegroupListItem) []string {
	var created []string
	for _, ng := range after {
		if !slices.Contains(known, ng.Id) {
			created = append(created, ng.Id)
		}
	}

	return created
}

// mksNodegroupV2Matching keeps the created node groups with the segment,
// labels and flavor_id, or service_uuid and price_plan_uuid, of the create
// options. The API returns neither cpus nor ram_mb, so those cannot narrow it.
func mksNodegroupV2Matching(created []string, after []mksclient.NodegroupListItem, opts mksclient.NodegroupCreateStruct) []string {
	var matching []string
	for _, ng := range after {
		if !slices.Contains(created, ng.Id) || ng.Segment != opts.Segment {
			continue
		}
		if opts.Labels != nil && !maps.Equal(*opts.Labels, ng.Labels) {
			continue
		}
		if !mksNodegroupV2MatchingConfig(ng, opts) {
			continue
		}
		matching = append(matching, ng.Id)
	}

	return matching
}

// mksNodegroupV2MatchingConfig compares the server configuration the API
// returns as sent (apiadapter/nodegroups.go:76-90).
func mksNodegroupV2MatchingConfig(ng mksclient.NodegroupListItem, opts mksclient.NodegroupCreateStruct) bool {
	if opts.DedicatedNodegroupConfig != nil {
		got := ng.DedicatedNodegroupConfig

		return got != nil && got.ServiceUuid == opts.DedicatedNodegroupConfig.ServiceUuid &&
			got.PricePlanUuid == opts.DedicatedNodegroupConfig.PricePlanUuid
	}
	flavorID := opts.CloudNodegroupConfig.FlavorId

	return flavorID == "" || (ng.CloudNodegroupConfig != nil && ng.CloudNodegroupConfig.FlavorId == flavorID)
}

// mksNodegroupV2CheckFlavorVolume fails when the API replaced a configured
// local_volume or volume_gb with the value of the flavor_id flavor, see
// mk-api-v2 validate/flavor.go: Terraform would otherwise report an
// inconsistent result.
func mksNodegroupV2CheckFlavorVolume(ctx context.Context, planned types.Object, got *mksclient.CloudNodegroupConfigInfo) diag.Diagnostics {
	var diags diag.Diagnostics
	if got == nil || planned.IsNull() || planned.IsUnknown() {
		return diags
	}
	var cloud mksNodegroupV2CloudConfigModel
	diags.Append(planned.As(ctx, &cloud, basetypes.ObjectAsOptions{UnhandledUnknownAsEmpty: true})...)
	if diags.HasError() || !cloud.CPUs.IsNull() {
		return diags
	}

	var mismatches []string
	if !cloud.LocalVolume.IsNull() && !cloud.LocalVolume.IsUnknown() && cloud.LocalVolume.ValueBool() != got.LocalVolume {
		mismatches = append(mismatches, fmt.Sprintf("local_volume = %t, while the configuration sets %t",
			got.LocalVolume, cloud.LocalVolume.ValueBool()))
	}
	if !cloud.VolumeGB.IsNull() && !cloud.VolumeGB.IsUnknown() && cloud.VolumeGB.ValueInt64() != got.VolumeGb {
		mismatches = append(mismatches, fmt.Sprintf("volume_gb = %d, while the configuration sets %d",
			got.VolumeGb, cloud.VolumeGB.ValueInt64()))
	}
	if len(mismatches) > 0 {
		diags.AddAttributeError(path.Root("cloud_nodegroup_config"), "Values replaced by the flavor",
			fmt.Sprintf("Flavor %s gives %s. Omit local_volume and volume_gb when flavor_id is set: "+
				"the API takes them from the flavor.", got.FlavorId, strings.Join(mismatches, "; ")))
	}

	return diags
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

	if plan.isDedicated() {
		var dedicated mksNodegroupV2DedicatedConfigModel
		diags.Append(plan.DedicatedNodegroupConfig.As(ctx, &dedicated, basetypes.ObjectAsOptions{})...)
		opts.DedicatedNodegroupConfig = &mksclient.DedicatedNodegroupConfig{
			ServiceUuid:            dedicated.ServiceUUID.ValueString(),
			PricePlanUuid:          dedicated.PricePlanUUID.ValueString(),
			RootSizeGb:             knownInt64Pointer(dedicated.RootSizeGB),
			CreateStoragePartition: knownBoolPointer(dedicated.CreateStoragePartition),
		}
		if !dedicated.Currency.IsNull() && !dedicated.Currency.IsUnknown() {
			opts.DedicatedNodegroupConfig.Currency = new(mksclient.DedicatedNodegroupConfigCurrency(dedicated.Currency.ValueString()))
		}
		// The API takes a cidr only for a dedicated node group, see
		// ValidateConfig.
		opts.Cidr = knownStringOrNull(plan.CIDR).ValueString()

		return opts, diags
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
// ram_mb, affinity_policy, and cidr of a cloud node group, nor price_plan_name
// and currency of a dedicated one, so they come from prior. After an apply
// count is the planned one: the autoscaler may move the nodes at any time.
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

	dedicated, d := flattenMKSNodegroupV2DedicatedConfig(ctx, ng.DedicatedNodegroupConfig, prior.DedicatedNodegroupConfig)
	diags.Append(d...)
	m.DedicatedNodegroupConfig = dedicated

	return diags
}

// flattenMKSNodegroupV2DedicatedConfig maps what the API returns of a
// dedicated node group (apiadapter/nodegroups.go:76-85); price_plan_name and
// currency come from prior: the API never returns them. A currency left to
// the API is main: mk-cluster-bm orders with the main balance unless told
// bonus (internal/models/dedicated/servers/converters.go:3-11).
func flattenMKSNodegroupV2DedicatedConfig(ctx context.Context, info *mksclient.DedicatedNodegroupConfig, prior types.Object) (types.Object, diag.Diagnostics) {
	if info == nil {
		// Not a dedicated node group.
		return prior, nil
	}

	var diags diag.Diagnostics
	priorConfig := mksNodegroupV2DedicatedConfigModel{PricePlanName: types.StringNull(), Currency: types.StringNull()}
	if !prior.IsNull() && !prior.IsUnknown() {
		diags.Append(prior.As(ctx, &priorConfig, basetypes.ObjectAsOptions{UnhandledUnknownAsEmpty: true})...)
	}

	currency := priorConfig.Currency
	if currency.IsUnknown() {
		currency = types.StringValue(string(mksclient.Main))
	}
	obj, d := types.ObjectValueFrom(ctx, mksNodegroupV2DedicatedConfigAttrTypes, mksNodegroupV2DedicatedConfigModel{
		ServiceUUID:            types.StringValue(info.ServiceUuid),
		PricePlanName:          knownStringOrNull(priorConfig.PricePlanName),
		PricePlanUUID:          types.StringValue(info.PricePlanUuid),
		RootSizeGB:             types.Int64PointerValue(info.RootSizeGb),
		CreateStoragePartition: types.BoolPointerValue(info.CreateStoragePartition),
		Currency:               currency,
	})
	diags.Append(d...)

	return obj, diags
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
