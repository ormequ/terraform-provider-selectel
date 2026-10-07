package selectel

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/identityschema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	mksv2 "github.com/selectel/mks-go/v2/pkg"
	"github.com/selectel/mks-go/v2/pkg/cluster"
	"github.com/selectel/mks-go/v2/pkg/kubeversion"
	"github.com/selectel/mks-go/v2/pkg/mksclient"
)

const mksClusterV2DefaultTimeout = 60 * time.Minute

var (
	_ resource.ResourceWithConfigure      = &mksClusterV2Resource{}
	_ resource.ResourceWithIdentity       = &mksClusterV2Resource{}
	_ resource.ResourceWithImportState    = &mksClusterV2Resource{}
	_ resource.ResourceWithModifyPlan     = &mksClusterV2Resource{}
	_ resource.ResourceWithMoveState      = &mksClusterV2Resource{}
	_ resource.ResourceWithValidateConfig = &mksClusterV2Resource{}
)

var mksClusterV2Docs = resourceDocs{Name: "cluster"}

const (
	mksClusterV2IDHint = "To get the cluster ID, in the [Control panel](https://my.selectel.ru/vpc/mks/), go to " +
		"**Cloud Platform** ⟶ **Kubernetes** ⟶ the cluster page ⟶ copy the ID at the top of the page under the cluster name, " +
		"near the region and pool."
	mksClusterV2PoolHint = "To get information about the pool, in the [Control panel](https://my.selectel.ru/vpc/mks/), " +
		"go to **Cloud Platform** ⟶ **Kubernetes**. The pool is in the **Pool** column."
)

// workers_type is the user-facing name of the API network_type.
var mksClusterV2WorkersTypes = map[string]string{
	"CLOUD":     "STANDARD",
	"DEDICATED": "L3VPN",
}

var (
	mksClusterV2CiliumAttrTypes = map[string]attr.Type{
		"envoy_daemonset": types.BoolType,
		"hubble_relay":    types.BoolType,
	}
	mksClusterV2AuditLogsAttrTypes = map[string]attr.Type{
		"enabled":     types.BoolType,
		"secret_name": types.StringType,
	}
	mksClusterV2OIDCAttrTypes = map[string]attr.Type{
		"enabled":        types.BoolType,
		"provider_name":  types.StringType,
		"issuer_url":     types.StringType,
		"client_id":      types.StringType,
		"username_claim": types.StringType,
		"groups_claim":   types.StringType,
		"ca_certs":       mksV2TrimmedStringType{},
	}
	mksClusterV2KubeOptionsAttrTypes = map[string]attr.Type{
		"feature_gates":         types.SetType{ElemType: types.StringType},
		"admission_controllers": types.SetType{ElemType: types.StringType},
		"audit_logs":            types.ObjectType{AttrTypes: mksClusterV2AuditLogsAttrTypes},
		"oidc":                  newMKSClusterV2OIDCType(),
		"x509_ca_certificates":  types.StringType,
	}
)

type mksClusterV2Model struct {
	ID                            types.String   `tfsdk:"id"`
	Name                          types.String   `tfsdk:"name"`
	Pool                          types.String   `tfsdk:"pool"`
	ProjectID                     types.String   `tfsdk:"project_id"`
	KubeVersion                   types.String   `tfsdk:"kube_version"`
	ClusterType                   types.String   `tfsdk:"cluster_type"`
	WorkersType                   types.String   `tfsdk:"workers_type"`
	NetworkID                     types.String   `tfsdk:"network_id"`
	SubnetID                      types.String   `tfsdk:"subnet_id"`
	CloudSubnetCIDR               types.String   `tfsdk:"cloud_subnet_cidr"`
	PrivateKubeAPI                types.Bool     `tfsdk:"private_kube_api"`
	MaintenanceWindowStart        types.String   `tfsdk:"maintenance_window_start"`
	MaintenanceWindowEnd          types.String   `tfsdk:"maintenance_window_end"`
	EnableAutorepair              types.Bool     `tfsdk:"enable_autorepair"`
	EnablePatchVersionAutoUpgrade types.Bool     `tfsdk:"enable_patch_version_auto_upgrade"`
	CNIType                       types.String   `tfsdk:"cni_type"`
	CNICiliumSettings             types.Object   `tfsdk:"cni_cilium_settings"`
	KubernetesOptions             types.Object   `tfsdk:"kubernetes_options"`
	Status                        types.String   `tfsdk:"status"`
	KubeAPIIP                     types.String   `tfsdk:"kube_api_ip"`
	Timeouts                      timeouts.Value `tfsdk:"timeouts"`
}

// mksClusterV2IdentityModel identifies a cluster for import by identity.
type mksClusterV2IdentityModel struct {
	ID        types.String `tfsdk:"id"`
	ProjectID types.String `tfsdk:"project_id"`
	Pool      types.String `tfsdk:"pool"`
}

type mksClusterV2CiliumModel struct {
	EnvoyDaemonset types.Bool `tfsdk:"envoy_daemonset"`
	HubbleRelay    types.Bool `tfsdk:"hubble_relay"`
}

type mksClusterV2KubeOptionsModel struct {
	FeatureGates         types.Set             `tfsdk:"feature_gates"`
	AdmissionControllers types.Set             `tfsdk:"admission_controllers"`
	AuditLogs            types.Object          `tfsdk:"audit_logs"`
	OIDC                 mksClusterV2OIDCValue `tfsdk:"oidc"`
	X509CACertificates   types.String          `tfsdk:"x509_ca_certificates"`
}

type mksClusterV2AuditLogsModel struct {
	Enabled    types.Bool   `tfsdk:"enabled"`
	SecretName types.String `tfsdk:"secret_name"`
}

type mksClusterV2OIDCModel struct {
	Enabled       types.Bool              `tfsdk:"enabled"`
	ProviderName  types.String            `tfsdk:"provider_name"`
	IssuerURL     types.String            `tfsdk:"issuer_url"`
	ClientID      types.String            `tfsdk:"client_id"`
	UsernameClaim types.String            `tfsdk:"username_claim"`
	GroupsClaim   types.String            `tfsdk:"groups_claim"`
	CACerts       mksV2TrimmedStringValue `tfsdk:"ca_certs"`
}

type mksClusterV2Resource struct {
	mksV2Provided
}

func newMKSClusterV2Resource() resource.Resource {
	return &mksClusterV2Resource{}
}

func (r *mksClusterV2Resource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_mks_cluster_v2"
}

func (r *mksClusterV2Resource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	resp.Diagnostics.Append(r.configure(req.ProviderData)...)
}

func (r *mksClusterV2Resource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	keepString := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	replaceString := []planmodifier.String{stringplanmodifier.UseStateForUnknown(), stringplanmodifier.RequiresReplace()}
	keepBool := []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()}
	keepSet := []planmodifier.Set{setplanmodifier.UseStateForUnknown()}
	keepObject := []planmodifier.Object{objectplanmodifier.UseStateForUnknown()}

	optionalString := func(description string) schema.StringAttribute {
		return schema.StringAttribute{Optional: true, Computed: true, Description: description, PlanModifiers: keepString}
	}
	optionalBool := func(description string) schema.BoolAttribute {
		return schema.BoolAttribute{Optional: true, Computed: true, Description: description, PlanModifiers: keepBool}
	}

	resp.Schema = schema.Schema{
		Description: "Creates and manages a Managed Kubernetes cluster using API v2.",
		Attributes: mksClusterV2Docs.withFrameworkDocsHints(map[string]schema.Attribute{
			"id": mksClusterV2Docs.idFrameworkResourceSchema(),
			"name": schema.StringAttribute{
				Required: true,
				Description: "Cluster name. It is included into the names of the cluster entities: " +
					"node groups, nodes, load balancers, networks, and volumes.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"pool": mksClusterV2Docs.regionFrameworkResourceSchema(),
			"project_id": schema.StringAttribute{
				Optional:      true,
				Computed:      true,
				Description:   projectIDDescription + " " + projectIDFromProvider + " " + projectIDFromResource + " " + projectIDLearnMore,
				PlanModifiers: replaceString,
			},
			"kube_version": schema.StringAttribute{
				Required: true,
				Description: "Kubernetes version of the cluster in the `x.y.z` format. Changing it upgrades the cluster. " +
					"A patch upgrade takes the latest patch version of the current minor version, " +
					"a minor upgrade takes the next minor version; downgrades and skipping a minor version are rejected at plan. " +
					"While patch auto-upgrade moves the cluster to a newer patch version of the configured minor version, " +
					"the configured version stays in the state.",
			},
			"cluster_type": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Description: "Cluster type: `BASIC` for one master node, `HIGH_AVAILABILITY` for three master nodes in one availability zone, " +
					"`HIGH_AVAILABILITY_MULTI_AZ` for three master nodes in three availability zones. " +
					"If omitted, the API creates a `HIGH_AVAILABILITY` cluster.",
				PlanModifiers: replaceString,
				Validators: []validator.String{stringvalidator.OneOf(
					string(mksclient.ClusterCreateStructClusterTypeBASIC),
					string(mksclient.ClusterCreateStructClusterTypeHIGHAVAILABILITY),
					string(mksclient.ClusterCreateStructClusterTypeHIGHAVAILABILITYMULTIAZ),
				)},
			},
			"workers_type": schema.StringAttribute{
				Required: true,
				Description: "Type of the worker nodes the cluster accepts: `CLOUD` allows only cloud node groups, " +
					"`DEDICATED` allows only dedicated servers. Node groups of the other type cannot be added to the cluster, " +
					"and switching the type recreates the cluster with all its node groups.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:    []validator.String{stringvalidator.OneOf("CLOUD", "DEDICATED")},
			},
			"network_id": schema.StringAttribute{
				Optional:      true,
				Computed:      true,
				Description:   "Unique identifier of the network of the cluster. If omitted, the API creates a network.",
				PlanModifiers: replaceString,
			},
			"subnet_id": schema.StringAttribute{
				Optional:      true,
				Computed:      true,
				Description:   "Unique identifier of the subnet of the cluster. If omitted, the API creates a subnet.",
				PlanModifiers: replaceString,
			},
			"cloud_subnet_cidr": schema.StringAttribute{
				Optional: true,
				Description: "CIDR of the subnet the API creates for the cluster. The API does not return it, " +
					"so an imported cluster has no value.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"private_kube_api": schema.BoolAttribute{
				Optional:      true,
				Computed:      true,
				Default:       booldefault.StaticBool(false),
				Description:   "Makes Kube API available only from the cluster network instead of the Internet.",
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.RequiresReplace()},
			},
			"maintenance_window_start": optionalString(
				"Time in UTC when maintenance in the cluster starts, in the `hh:mm:ss` format."),
			"enable_autorepair": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(true),
				Description: "Allows worker nodes to be reinstalled automatically when they are unavailable or unhealthy.",
			},
			"enable_patch_version_auto_upgrade": optionalBool(
				"Allows the cluster to be upgraded to the latest patch version during the maintenance window. " +
					"If omitted, the API enables it for all cluster types except `BASIC`. Cannot be `true` when `cluster_type` is `BASIC`."),
			"cni_type": schema.StringAttribute{
				Optional:      true,
				Computed:      true,
				Description:   "Type of CNI used by the cluster: `CALICO` or `CILIUM`. If omitted, the API uses `CALICO`.",
				PlanModifiers: replaceString,
				Validators: []validator.String{stringvalidator.OneOf(
					string(mksclient.ClusterCniTypeCALICO), string(mksclient.ClusterCniTypeCILIUM),
				)},
			},
			"cni_cilium_settings": schema.SingleNestedAttribute{
				Optional:      true,
				Computed:      true,
				Description:   "Settings of the Cilium CNI. Can be set only when `cni_type` is `CILIUM`.",
				PlanModifiers: keepObject,
				Attributes: map[string]schema.Attribute{
					"envoy_daemonset": optionalBool("Enables the Envoy DaemonSet for Cilium."),
					"hubble_relay":    optionalBool("Enables Hubble Relay for Cilium."),
				},
			},
			"kubernetes_options": schema.SingleNestedAttribute{
				Optional: true,
				Computed: true,
				Description: "Kubernetes options of the cluster. The provider always sends the whole object, " +
					"because the API replaces the options entirely.",
				PlanModifiers: keepObject,
				Attributes: map[string]schema.Attribute{
					"feature_gates": schema.SetAttribute{
						Optional:      true,
						Computed:      true,
						ElementType:   types.StringType,
						Description:   "Enabled feature gates. Use the `selectel_mks_feature_gates_v2` data source to list the available ones.",
						PlanModifiers: keepSet,
					},
					"admission_controllers": schema.SetAttribute{
						Optional:    true,
						Computed:    true,
						ElementType: types.StringType,
						Description: "Enabled admission controllers. Use the `selectel_mks_admission_controllers_v2` data source " +
							"to list the available ones.",
						PlanModifiers: keepSet,
					},
					"audit_logs": schema.SingleNestedAttribute{
						Optional:      true,
						Computed:      true,
						Description:   "Collection of Kubernetes audit logs.",
						PlanModifiers: keepObject,
						Attributes: map[string]schema.Attribute{
							"enabled": optionalBool("Enables collection of audit logs."),
							"secret_name": optionalString("Name of the secret in the `kube-system` namespace " +
								"with the credentials of the logging system."),
						},
					},
					"oidc": schema.SingleNestedAttribute{
						Optional: true,
						Computed: true,
						Description: "Connection of an OpenID Connect (OIDC) provider to the cluster. " +
							"Disabling OIDC clears its other settings in the cluster.",
						CustomType:    newMKSClusterV2OIDCType(),
						PlanModifiers: keepObject,
						Attributes: map[string]schema.Attribute{
							"enabled": optionalBool("Enables authentication with OIDC."),
							"provider_name": optionalString("Name of the connection, for identification only. " +
								"The API does not apply a change of only this field."),
							"issuer_url":     optionalString("URL of the OIDC provider. It must start with `https://`."),
							"client_id":      optionalString("Client ID that all tokens must be issued for."),
							"username_claim": optionalString("JWT claim to use as the username."),
							"groups_claim":   optionalString("JWT claim to use as the user's group."),
							"ca_certs": schema.StringAttribute{
								Optional:      true,
								Computed:      true,
								CustomType:    mksV2TrimmedStringType{},
								Description:   "CA certificates of the OIDC provider in the PEM format. Leading and trailing whitespace is ignored.",
								PlanModifiers: keepString,
							},
						},
					},
					"x509_ca_certificates": schema.StringAttribute{
						Optional: true,
						Description: "Custom X509 CA certificates for the cluster components, base64-encoded. " +
							"The API does not return them, so an imported cluster has no value.",
					},
				},
			},
			"status": schema.StringAttribute{
				Computed:      true,
				Description:   "Cluster status.",
				PlanModifiers: keepString,
			},
			"kube_api_ip": schema.StringAttribute{
				Computed:      true,
				Description:   "IP address of the Kube API.",
				PlanModifiers: keepString,
			},
			"maintenance_window_end": schema.StringAttribute{
				Computed:      true,
				Description:   "Time in UTC when maintenance in the cluster ends, in the `hh:mm:ss` format.",
				PlanModifiers: keepString,
			},
		}),
		Blocks: map[string]schema.Block{
			"timeouts": timeouts.Block(ctx, timeouts.Opts{Create: true, Update: true, Delete: true}),
		},
	}
}

func (r *mksClusterV2Resource) IdentitySchema(_ context.Context, _ resource.IdentitySchemaRequest, resp *resource.IdentitySchemaResponse) {
	resp.IdentitySchema = identityschema.Schema{
		Attributes: map[string]identityschema.Attribute{
			"id":         mksClusterV2Docs.idFrameworkIdentitySchema(mksClusterV2IDHint),
			"project_id": projectIDFrameworkIdentitySchema(),
			"pool":       mksClusterV2Docs.regionFrameworkIdentitySchema(mksClusterV2PoolHint),
		},
	}
}

// setIdentity is a no-op when Terraform passed no identity.
func (m *mksClusterV2Model) setIdentity(ctx context.Context, identity *tfsdk.ResourceIdentity) diag.Diagnostics {
	if identity == nil {
		return nil
	}

	return identity.Set(ctx, mksClusterV2IdentityModel{ID: m.ID, ProjectID: m.ProjectID, Pool: m.Pool})
}

func (r *mksClusterV2Resource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var clusterType, cniType types.String
	var autoUpgrade types.Bool
	var cilium types.Object
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("cluster_type"), &clusterType)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("enable_patch_version_auto_upgrade"), &autoUpgrade)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("cni_type"), &cniType)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("cni_cilium_settings"), &cilium)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// The API stores Cilium settings only for a Cilium cluster and returns
	// null otherwise, see mk-api-v2 handlers/clusters/create.go.
	if !cilium.IsNull() && !cilium.IsUnknown() && !cniType.IsUnknown() &&
		cniType.ValueString() != string(mksclient.ClusterCniTypeCILIUM) {
		resp.Diagnostics.AddAttributeError(path.Root("cni_cilium_settings"),
			"Cilium settings need the Cilium CNI",
			"Set cni_type to CILIUM or remove cni_cilium_settings.")
	}

	// The API rejects it, see mk-api-v2 validate/cluster.go.
	if clusterType.ValueString() == string(mksclient.ClusterCreateStructClusterTypeBASIC) && autoUpgrade.ValueBool() {
		resp.Diagnostics.AddAttributeError(path.Root("enable_patch_version_auto_upgrade"),
			"Patch version auto-upgrade is not available for BASIC clusters",
			"Set enable_patch_version_auto_upgrade to false or omit it when cluster_type is BASIC.")
	}
}

func (r *mksClusterV2Resource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}

	var state, plan mksClusterV2Model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if !plan.KubeVersion.IsUnknown() && !plan.KubeVersion.Equal(state.KubeVersion) {
		err := validateMKSClusterV2KubeVersionChange(state.KubeVersion.ValueString(), plan.KubeVersion.ValueString())
		if err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("kube_version"), "Invalid Kubernetes version change", err.Error())

			return
		}
	}

	// Any update can change the status: a task-creating PATCH moves the
	// cluster to PENDING_UPGRADE_CLUSTER_CONFIG, and a finished task leaves it
	// ACTIVE or MAINTENANCE, whatever it was at refresh.
	if !req.Plan.Raw.Equal(req.State.Raw) {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("status"), types.StringUnknown())...)
	}
	// The window end follows these changes and cannot be kept from the state.
	if !plan.KubeVersion.Equal(state.KubeVersion) || !plan.MaintenanceWindowStart.Equal(state.MaintenanceWindowStart) {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("maintenance_window_end"), types.StringUnknown())...)
	}
}

func (r *mksClusterV2Resource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan mksClusterV2Model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	timeout, diags := plan.Timeouts.Create(ctx, mksClusterV2DefaultTimeout)
	resp.Diagnostics.Append(diags...)
	client, projectID, diags := r.client(ctx, plan.ProjectID, plan.Pool.ValueString())
	resp.Diagnostics.Append(diags...)
	opts, diags := expandMKSClusterV2CreateOpts(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	created, err := cluster.Create(ctx, client, opts)
	if err != nil {
		resp.Diagnostics.AddError("Error creating cluster", errCreatingObject(objectCluster, err).Error())

		return
	}

	waitErr := newMKSV2CreatedTaskWaiter(client, created.Id, "").Wait(ctx)

	// Save the cluster even when the wait failed, so Terraform taints it
	// instead of losing it. The wait may have used up ctx.
	readCtx, readCancel := mksV2ReadAfterWaitContext(ctx)
	defer readCancel()
	got, err := cluster.Get(readCtx, client, created.Id)
	if err != nil {
		resp.Diagnostics.Append(mksV2PlannedState(req.Plan, &resp.State)...)
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), created.Id)...)
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), projectID)...)
		resp.Diagnostics.AddError("Error reading cluster", errGettingObject(objectCluster, created.Id, errors.Join(waitErr, err)).Error())

		return
	}
	state := plan
	resp.Diagnostics.Append(state.fromAPI(ctx, got, plan, true)...)
	resp.Diagnostics.Append(state.setIdentity(ctx, resp.Identity)...)
	if waitErr != nil {
		resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
		resp.Diagnostics.AddError("Error waiting for the cluster to become ready", waitErr.Error())

		return
	}

	x509Err := applyMKSClusterV2X509(ctx, client, &state)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
	if x509Err != nil {
		resp.Diagnostics.AddError("Error setting X509 CA certificates of the cluster", errUpdatingObject(objectCluster, created.Id, x509Err).Error())
	}
}

// applyMKSClusterV2X509 sends the planned x509_ca_certificates of a created
// cluster: mk-api-v2 ignores them on create and stores them only on PATCH.
// The PATCH carries the whole kubernetes_options, because the API replaces
// them. On error x509_ca_certificates is null in state, since the cluster has
// none.
func applyMKSClusterV2X509(ctx context.Context, client *mksv2.ServiceClient, state *mksClusterV2Model) error {
	var options mksClusterV2KubeOptionsModel
	diags := state.KubernetesOptions.As(ctx, &options, basetypes.ObjectAsOptions{})
	if diags.HasError() {
		return fmt.Errorf("error reading kubernetes_options: %v", diags)
	}
	if options.X509CACertificates.ValueString() == "" {
		return nil
	}

	kubeOptions, diags := expandMKSClusterV2KubeOptions(ctx, state.KubernetesOptions)
	if diags.HasError() {
		return fmt.Errorf("error expanding kubernetes_options: %v", diags)
	}
	clusterID := state.ID.ValueString()
	err := mksV2CallAndWait(ctx, client, clusterID, func() error {
		_, err := cluster.Patch(ctx, client, clusterID, &mksclient.ClusterUpdateStruct{KubernetesOptions: kubeOptions})

		return err
	})
	var got *mksclient.ClusterDetailed
	if err == nil {
		got, err = cluster.Get(ctx, client, clusterID)
	}
	if err != nil {
		options.X509CACertificates = types.StringNull()
		state.KubernetesOptions, diags = types.ObjectValueFrom(ctx, mksClusterV2KubeOptionsAttrTypes, options)
		if diags.HasError() {
			return errors.Join(err, fmt.Errorf("error saving kubernetes_options: %v", diags))
		}

		return err
	}

	prior := *state
	diags = state.fromAPI(ctx, got, prior, true)
	if diags.HasError() {
		return fmt.Errorf("error reading the cluster: %v", diags)
	}

	return nil
}

func (r *mksClusterV2Resource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state mksClusterV2Model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	client, _, diags := r.client(ctx, state.ProjectID, state.Pool.ValueString())
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	got, err := cluster.Get(ctx, client, state.ID.ValueString())
	if isMKSV2NotFound(err) {
		resp.State.RemoveResource(ctx)

		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Error reading cluster", errGettingObject(objectCluster, state.ID.ValueString(), err).Error())

		return
	}

	prior := state
	resp.Diagnostics.Append(state.fromAPI(ctx, got, prior, false)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
	resp.Diagnostics.Append(state.setIdentity(ctx, resp.Identity)...)
}

func (r *mksClusterV2Resource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state mksClusterV2Model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	timeout, diags := plan.Timeouts.Update(ctx, mksClusterV2DefaultTimeout)
	resp.Diagnostics.Append(diags...)
	client, _, diags := r.client(ctx, state.ProjectID, state.Pool.ValueString())
	resp.Diagnostics.Append(diags...)
	patch, changed, diags := expandMKSClusterV2Patch(ctx, plan, state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	clusterID := state.ID.ValueString()
	if changed {
		err := mksV2CallAndWait(ctx, client, clusterID, func() error {
			_, err := cluster.Patch(ctx, client, clusterID, patch)

			return err
		})
		if err != nil {
			resp.Diagnostics.AddError("Error updating cluster", errUpdatingObject(objectCluster, clusterID, err).Error())

			return
		}
	}

	if !plan.KubeVersion.Equal(state.KubeVersion) {
		err := upgradeMKSClusterV2KubeVersion(ctx, client, clusterID, plan.KubeVersion.ValueString())
		if err != nil {
			resp.Diagnostics.AddError("Error upgrading cluster", errUpdatingObject(objectCluster, clusterID, err).Error())

			return
		}
	}

	got, err := cluster.Get(ctx, client, clusterID)
	if err != nil {
		resp.Diagnostics.AddError("Error reading cluster", errGettingObject(objectCluster, clusterID, err).Error())

		return
	}
	resp.Diagnostics.Append(plan.fromAPI(ctx, got, plan, true)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
	resp.Diagnostics.Append(plan.setIdentity(ctx, resp.Identity)...)
}

func (r *mksClusterV2Resource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state mksClusterV2Model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	timeout, diags := state.Timeouts.Delete(ctx, mksClusterV2DefaultTimeout)
	resp.Diagnostics.Append(diags...)
	client, _, diags := r.client(ctx, state.ProjectID, state.Pool.ValueString())
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	clusterID := state.ID.ValueString()
	waiter, err := newMKSV2TaskWaiter(ctx, client, clusterID, "")
	if isMKSV2NotFound(err) {
		return
	}
	if err == nil {
		err = cluster.Delete(ctx, client, clusterID)
		if isMKSV2NotFound(err) {
			return
		}
	}
	if err == nil {
		err = waiter.WaitClusterDeleted(ctx)
	}
	if err != nil {
		resp.Diagnostics.AddError("Error deleting cluster", errDeletingObject(objectCluster, clusterID, err).Error())
	}
}

// ImportState takes the project and the pool from the identity. Import by ID
// takes them from the provider configuration, because the cluster ID carries
// neither, like selectel_mks_cluster_v1.
func (r *mksClusterV2Resource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if req.ID == "" {
		var identity mksClusterV2IdentityModel
		resp.Diagnostics.Append(req.Identity.Get(ctx, &identity)...)
		if resp.Diagnostics.HasError() {
			return
		}
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), identity.ID)...)
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), identity.ProjectID)...)
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("pool"), identity.Pool)...)

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

	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), r.config.ProjectID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("pool"), r.config.Region)...)
}

// mksV2CallAndWait runs a mutating call on the cluster and waits for the
// cluster tasks it created.
func mksV2CallAndWait(ctx context.Context, client *mksv2.ServiceClient, clusterID string, call func() error) error {
	waiter, err := newMKSV2TaskWaiter(ctx, client, clusterID, "")
	if err != nil {
		return err
	}

	err = call()
	if err != nil {
		return err
	}

	return waiter.Wait(ctx)
}

// validateMKSClusterV2KubeVersionChange rejects at plan the changes no upgrade
// action performs: a major change, a downgrade, a skipped minor version.
func validateMKSClusterV2KubeVersionChange(current, desired string) error {
	currentMajor, err := kubeVersionToMajor(current)
	if err != nil {
		return err
	}
	desiredMajor, err := kubeVersionToMajor(desired)
	if err != nil {
		return err
	}
	if currentMajor != desiredMajor {
		return fmt.Errorf("current version %s can't be upgraded to version %s", current, desired)
	}

	currentMinor, err := kubeVersionToMinor(current)
	if err != nil {
		return err
	}
	desiredMinor, err := kubeVersionToMinor(desired)
	if err != nil {
		return err
	}
	currentPatch, err := kubeVersionToPatch(current)
	if err != nil {
		return err
	}
	desiredPatch, err := kubeVersionToPatch(desired)
	if err != nil {
		return err
	}

	switch {
	case desiredMinor > currentMinor+1:
		return fmt.Errorf("current version %s can't be upgraded to version %s, kubernetes versions must be upgraded one minor version at a time",
			current, desired)
	case desiredMinor < currentMinor, desiredMinor == currentMinor && desiredPatch < currentPatch:
		return fmt.Errorf("current version %s can't be downgraded to version %s", current, desired)
	}

	return nil
}

// upgradeMKSClusterV2KubeVersion follows upgradeMKSClusterV1KubeVersion, but
// starts from the version the cluster runs, which patch auto-upgrade can move
// past the state.
func upgradeMKSClusterV2KubeVersion(ctx context.Context, client *mksv2.ServiceClient, clusterID, desired string) error {
	got, err := cluster.Get(ctx, client, clusterID)
	if err != nil {
		return err
	}
	current := got.KubeVersion

	currentMinor, err := kubeVersionTrimToMinor(current)
	if err != nil {
		return err
	}
	desiredMinor, err := kubeVersionTrimToMinor(desired)
	if err != nil {
		return err
	}

	if desiredMinor == currentMinor {
		currentPatch, err := kubeVersionToPatch(current)
		if err != nil {
			return err
		}
		desiredPatch, err := kubeVersionToPatch(desired)
		if err != nil {
			return err
		}
		if desiredPatch <= currentPatch {
			// Patch auto-upgrade got there first.
			return nil
		}

		kubeVersions, err := kubeversion.List(ctx, client)
		if err != nil {
			return fmt.Errorf("error getting kube versions: %w", err)
		}
		latestPatchVersions, err := latestKubePatchVersions(mksKubeVersionsV2ToV1Views(kubeVersions))
		if err != nil {
			return err
		}
		latest, ok := latestPatchVersions[currentMinor]
		if !ok {
			return fmt.Errorf("unable to find the latest patch version for the current minor version %s", currentMinor)
		}
		if desired != latest {
			return fmt.Errorf("current version %s can't be upgraded to version %s, the latest available patch version is: %s",
				current, desired, latest)
		}

		err = mksV2CallAndWait(ctx, client, clusterID, func() error {
			_, err := cluster.UpgradePatchVersion(ctx, client, clusterID)

			return err
		})
		if err != nil {
			return fmt.Errorf("error upgrading patch version: %w", err)
		}

		return nil
	}

	nextMinor, err := kubeVersionTrimToMinorIncremented(current)
	if err != nil {
		return err
	}
	if desiredMinor != nextMinor {
		return fmt.Errorf("current version %s can't be upgraded to version %s, kubernetes versions must be upgraded one minor version at a time",
			current, desired)
	}

	err = mksV2CallAndWait(ctx, client, clusterID, func() error {
		_, err := cluster.UpgradeMinorVersion(ctx, client, clusterID)

		return err
	})
	if err != nil {
		return fmt.Errorf("error upgrading minor version: %w", err)
	}

	return nil
}

// mksClusterV2KubeVersion keeps the version from the state while the cluster
// runs a newer patch version of the same minor version: patch auto-upgrade
// moves the cluster past the configuration, which v1 hid with a diff suppress.
func mksClusterV2KubeVersion(prior, actual string) string {
	if prior == "" {
		return actual
	}

	priorMinor, err := kubeVersionTrimToMinor(prior)
	if err != nil {
		return actual
	}
	actualMinor, err := kubeVersionTrimToMinor(actual)
	if err != nil || priorMinor != actualMinor {
		return actual
	}
	priorPatch, err := kubeVersionToPatch(prior)
	if err != nil {
		return actual
	}
	actualPatch, err := kubeVersionToPatch(actual)
	if err != nil || actualPatch < priorPatch {
		return actual
	}

	return prior
}

func expandMKSClusterV2CreateOpts(ctx context.Context, plan mksClusterV2Model) (*mksclient.ClusterCreateStruct, diag.Diagnostics) {
	opts := &mksclient.ClusterCreateStruct{
		Name:        plan.Name.ValueString(),
		Pool:        plan.Pool.ValueString(),
		KubeVersion: plan.KubeVersion.ValueString(),
		// Deprecated in the API, cluster_type wins.
		Basic:                         false,
		NetworkType:                   mksClusterV2WorkersTypes[plan.WorkersType.ValueString()],
		NetworkId:                     plan.NetworkID.ValueString(),
		SubnetId:                      plan.SubnetID.ValueString(),
		CloudSubnetCidr:               plan.CloudSubnetCIDR.ValueString(),
		PrivateKubeApi:                plan.PrivateKubeAPI.ValueBool(),
		MaintenanceWindowStart:        plan.MaintenanceWindowStart.ValueString(),
		EnableAutorepair:              knownBoolPointer(plan.EnableAutorepair),
		EnablePatchVersionAutoUpgrade: knownBoolPointer(plan.EnablePatchVersionAutoUpgrade),
		CniType:                       knownStringPointer(plan.CNIType),
	}
	if !plan.ClusterType.IsNull() && !plan.ClusterType.IsUnknown() {
		clusterType := mksclient.ClusterCreateStructClusterType(plan.ClusterType.ValueString())
		opts.ClusterType = &clusterType
	}

	var diags diag.Diagnostics
	opts.CniCiliumSettings, diags = expandMKSClusterV2Cilium(ctx, plan.CNICiliumSettings)
	kubeOptions, kubeOptionsDiags := expandMKSClusterV2KubeOptions(ctx, plan.KubernetesOptions)
	diags.Append(kubeOptionsDiags...)
	opts.KubernetesOptions = kubeOptions

	return opts, diags
}

// expandMKSClusterV2Patch collects the changed in-place fields. The API
// replaces kubernetes_options entirely, so the whole plan object goes.
func expandMKSClusterV2Patch(ctx context.Context, plan, state mksClusterV2Model) (*mksclient.ClusterUpdateStruct, bool, diag.Diagnostics) {
	var diags diag.Diagnostics
	patch := &mksclient.ClusterUpdateStruct{}
	changed := false

	if !plan.MaintenanceWindowStart.IsUnknown() && !plan.MaintenanceWindowStart.Equal(state.MaintenanceWindowStart) {
		patch.MaintenanceWindowStart = plan.MaintenanceWindowStart.ValueStringPointer()
		changed = true
	}
	if !plan.EnableAutorepair.IsUnknown() && !plan.EnableAutorepair.Equal(state.EnableAutorepair) {
		patch.EnableAutorepair = plan.EnableAutorepair.ValueBoolPointer()
		changed = true
	}
	if !plan.EnablePatchVersionAutoUpgrade.IsUnknown() && !plan.EnablePatchVersionAutoUpgrade.Equal(state.EnablePatchVersionAutoUpgrade) {
		patch.EnablePatchVersionAutoUpgrade = plan.EnablePatchVersionAutoUpgrade.ValueBoolPointer()
		changed = true
	}
	if !plan.CNICiliumSettings.Equal(state.CNICiliumSettings) {
		cilium, ciliumDiags := expandMKSClusterV2Cilium(ctx, plan.CNICiliumSettings)
		diags.Append(ciliumDiags...)
		patch.CniCiliumSettings = cilium
		changed = changed || cilium != nil
	}
	if !plan.KubernetesOptions.Equal(state.KubernetesOptions) {
		kubeOptions, kubeOptionsDiags := expandMKSClusterV2KubeOptions(ctx, plan.KubernetesOptions)
		diags.Append(kubeOptionsDiags...)
		patch.KubernetesOptions = kubeOptions
		changed = changed || kubeOptions != nil
	}

	return patch, changed, diags
}

func expandMKSClusterV2Cilium(ctx context.Context, obj types.Object) (*mksclient.CNICiliumSettings, diag.Diagnostics) {
	if obj.IsNull() || obj.IsUnknown() {
		return nil, nil
	}

	var m mksClusterV2CiliumModel
	diags := obj.As(ctx, &m, basetypes.ObjectAsOptions{})

	return &mksclient.CNICiliumSettings{
		EnvoyDaemonset: knownBoolPointer(m.EnvoyDaemonset),
		HubbleRelay:    knownBoolPointer(m.HubbleRelay),
	}, diags
}

// expandMKSClusterV2KubeOptions converts the object; unknown parts, possible
// only on create, are left to the API defaults.
func expandMKSClusterV2KubeOptions(ctx context.Context, obj types.Object) (*mksclient.KubernetesOptions, diag.Diagnostics) {
	if obj.IsNull() || obj.IsUnknown() {
		return nil, nil
	}

	var m mksClusterV2KubeOptionsModel
	diags := obj.As(ctx, &m, basetypes.ObjectAsOptions{})
	if diags.HasError() {
		return nil, diags
	}

	opts := &mksclient.KubernetesOptions{
		FeatureGates:         []string{},
		AdmissionControllers: []string{},
		X509CaCertificates:   m.X509CACertificates.ValueString(),
	}
	if !m.FeatureGates.IsNull() && !m.FeatureGates.IsUnknown() {
		diags.Append(m.FeatureGates.ElementsAs(ctx, &opts.FeatureGates, false)...)
	}
	if !m.AdmissionControllers.IsNull() && !m.AdmissionControllers.IsUnknown() {
		diags.Append(m.AdmissionControllers.ElementsAs(ctx, &opts.AdmissionControllers, false)...)
	}
	if !m.AuditLogs.IsNull() && !m.AuditLogs.IsUnknown() {
		var audit mksClusterV2AuditLogsModel
		diags.Append(m.AuditLogs.As(ctx, &audit, basetypes.ObjectAsOptions{})...)
		opts.AuditLogs = mksclient.AuditLogs{Enabled: audit.Enabled.ValueBool(), SecretName: audit.SecretName.ValueString()}
	}
	if !m.OIDC.IsNull() && !m.OIDC.IsUnknown() {
		var oidc mksClusterV2OIDCModel
		diags.Append(m.OIDC.As(ctx, &oidc, basetypes.ObjectAsOptions{})...)
		opts.Oidc = mksclient.OIDC{
			Enabled:       oidc.Enabled.ValueBool(),
			ProviderName:  oidc.ProviderName.ValueString(),
			IssuerUrl:     oidc.IssuerURL.ValueString(),
			ClientId:      oidc.ClientID.ValueString(),
			UsernameClaim: oidc.UsernameClaim.ValueString(),
			GroupsClaim:   oidc.GroupsClaim.ValueString(),
			CaCerts:       oidc.CACerts.ValueString(),
		}
	}

	return opts, diags
}

// fromAPI maps the cluster onto the model. cloud_subnet_cidr and
// x509_ca_certificates are never returned, so they come from prior. After an
// apply kube_version must equal the plan, otherwise it follows
// mksClusterV2KubeVersion.
func (m *mksClusterV2Model) fromAPI(ctx context.Context, c *mksclient.ClusterDetailed, prior mksClusterV2Model, applied bool) diag.Diagnostics {
	var diags diag.Diagnostics

	m.ID = types.StringValue(c.Id)
	m.Name = types.StringValue(c.Name)
	m.Pool = types.StringValue(c.Pool)
	if c.ProjectId != "" {
		m.ProjectID = types.StringValue(c.ProjectId)
	}
	if !applied {
		m.KubeVersion = types.StringValue(mksClusterV2KubeVersion(prior.KubeVersion.ValueString(), c.KubeVersion))
	}
	m.ClusterType = types.StringValue(string(c.ClusterType))
	m.WorkersType = types.StringValue(string(c.NetworkType))
	for workersType, networkType := range mksClusterV2WorkersTypes {
		if networkType == string(c.NetworkType) {
			m.WorkersType = types.StringValue(workersType)
		}
	}
	m.NetworkID = types.StringValue(c.NetworkId)
	m.SubnetID = types.StringValue(c.SubnetId)
	m.CloudSubnetCIDR = prior.CloudSubnetCIDR
	m.PrivateKubeAPI = types.BoolValue(c.PrivateKubeApi)
	m.MaintenanceWindowStart = types.StringValue(c.MaintenanceWindowStart)
	m.MaintenanceWindowEnd = types.StringValue(c.MaintenanceWindowEnd)
	m.EnableAutorepair = types.BoolValue(c.EnableAutorepair)
	m.EnablePatchVersionAutoUpgrade = types.BoolValue(c.EnablePatchVersionAutoUpgrade)
	m.CNIType = types.StringValue(string(c.CniType))
	m.Status = types.StringValue(string(c.Status))
	m.KubeAPIIP = types.StringValue(c.KubeApiIp)

	m.CNICiliumSettings = types.ObjectNull(mksClusterV2CiliumAttrTypes)
	if c.CniCiliumSettings != nil {
		var d diag.Diagnostics
		m.CNICiliumSettings, d = types.ObjectValueFrom(ctx, mksClusterV2CiliumAttrTypes, mksClusterV2CiliumModel{
			EnvoyDaemonset: types.BoolPointerValue(c.CniCiliumSettings.EnvoyDaemonset),
			HubbleRelay:    types.BoolPointerValue(c.CniCiliumSettings.HubbleRelay),
		})
		diags.Append(d...)
	}

	x509 := types.StringNull()
	if !prior.KubernetesOptions.IsNull() && !prior.KubernetesOptions.IsUnknown() {
		var priorOptions mksClusterV2KubeOptionsModel
		diags.Append(prior.KubernetesOptions.As(ctx, &priorOptions, basetypes.ObjectAsOptions{})...)
		x509 = priorOptions.X509CACertificates
	}
	kubeOptions, d := flattenMKSClusterV2KubeOptions(ctx, c.KubernetesOptions, x509)
	diags.Append(d...)
	m.KubernetesOptions = kubeOptions

	return diags
}

func flattenMKSClusterV2KubeOptions(ctx context.Context, o mksclient.KubernetesOptions, x509 types.String) (types.Object, diag.Diagnostics) {
	var diags diag.Diagnostics

	stringSet := func(values []string) types.Set {
		// A framework set rejects duplicates.
		values = slices.Compact(slices.Sorted(slices.Values(values)))
		set, d := types.SetValueFrom(ctx, types.StringType, append([]string{}, values...))
		diags.Append(d...)

		return set
	}
	audit, d := types.ObjectValueFrom(ctx, mksClusterV2AuditLogsAttrTypes, mksClusterV2AuditLogsModel{
		Enabled:    types.BoolValue(o.AuditLogs.Enabled),
		SecretName: types.StringValue(o.AuditLogs.SecretName),
	})
	diags.Append(d...)
	oidcObject, d := types.ObjectValueFrom(ctx, mksClusterV2OIDCAttrTypes, mksClusterV2OIDCModel{
		Enabled:       types.BoolValue(o.Oidc.Enabled),
		ProviderName:  types.StringValue(o.Oidc.ProviderName),
		IssuerURL:     types.StringValue(o.Oidc.IssuerUrl),
		ClientID:      types.StringValue(o.Oidc.ClientId),
		UsernameClaim: types.StringValue(o.Oidc.UsernameClaim),
		GroupsClaim:   types.StringValue(o.Oidc.GroupsClaim),
		CACerts:       mksV2TrimmedString(o.Oidc.CaCerts),
	})
	diags.Append(d...)
	oidc := mksClusterV2OIDCValue{ObjectValue: oidcObject}

	obj, d := types.ObjectValueFrom(ctx, mksClusterV2KubeOptionsAttrTypes, mksClusterV2KubeOptionsModel{
		FeatureGates:         stringSet(o.FeatureGates),
		AdmissionControllers: stringSet(o.AdmissionControllers),
		AuditLogs:            audit,
		OIDC:                 oidc,
		X509CACertificates:   x509,
	})
	diags.Append(d...)

	return obj, diags
}

// knownBoolPointer is nil for a null or unknown value: ValueBoolPointer turns
// unknown into false.
func knownBoolPointer(v types.Bool) *bool {
	if v.IsUnknown() {
		return nil
	}

	return v.ValueBoolPointer()
}

func knownStringPointer(v types.String) *string {
	if v.IsUnknown() {
		return nil
	}

	return v.ValueStringPointer()
}

// mksV2TrimmedStringType is a string whose surrounding whitespace does not
// matter: mk-api-v2 trims oidc.ca_certs, while file("ca.pem") keeps the
// trailing newline.
type mksV2TrimmedStringType struct {
	basetypes.StringType
}

var _ basetypes.StringTypable = mksV2TrimmedStringType{}

func (t mksV2TrimmedStringType) Equal(o attr.Type) bool {
	_, ok := o.(mksV2TrimmedStringType)

	return ok
}

func (t mksV2TrimmedStringType) String() string {
	return "mksV2TrimmedStringType"
}

func (t mksV2TrimmedStringType) ValueFromString(_ context.Context, in basetypes.StringValue) (basetypes.StringValuable, diag.Diagnostics) {
	return mksV2TrimmedStringValue{StringValue: in}, nil
}

func (t mksV2TrimmedStringType) ValueFromTerraform(ctx context.Context, in tftypes.Value) (attr.Value, error) {
	value, err := t.StringType.ValueFromTerraform(ctx, in)
	if err != nil {
		return nil, err
	}
	stringValue, ok := value.(basetypes.StringValue)
	if !ok {
		return nil, fmt.Errorf("unexpected value type %T", value)
	}

	return mksV2TrimmedStringValue{StringValue: stringValue}, nil
}

func (t mksV2TrimmedStringType) ValueType(_ context.Context) attr.Value {
	return mksV2TrimmedStringValue{}
}

type mksV2TrimmedStringValue struct {
	basetypes.StringValue
}

var _ basetypes.StringValuableWithSemanticEquals = mksV2TrimmedStringValue{}

func (v mksV2TrimmedStringValue) Equal(o attr.Value) bool {
	other, ok := o.(mksV2TrimmedStringValue)

	return ok && v.StringValue.Equal(other.StringValue)
}

func (v mksV2TrimmedStringValue) Type(_ context.Context) attr.Type {
	return mksV2TrimmedStringType{}
}

func (v mksV2TrimmedStringValue) StringSemanticEquals(_ context.Context, newValuable basetypes.StringValuable) (bool, diag.Diagnostics) {
	var diags diag.Diagnostics

	newValue, ok := newValuable.(mksV2TrimmedStringValue)
	if !ok {
		diags.AddError("Semantic Equality Check Error", fmt.Sprintf("Expected mksV2TrimmedStringValue, got %T.", newValuable))

		return false, diags
	}

	return strings.TrimSpace(v.ValueString()) == strings.TrimSpace(newValue.ValueString()), diags
}

func mksV2TrimmedString(value string) mksV2TrimmedStringValue {
	return mksV2TrimmedStringValue{StringValue: types.StringValue(value)}
}

// mksClusterV2OIDCType is the oidc object. mk-api-v2 wipes every field of a
// disabled OIDC, so two disabled values are equal whatever their other fields
// hold: the fields can stay in the configuration while OIDC is off.
type mksClusterV2OIDCType struct {
	basetypes.ObjectType
}

var _ basetypes.ObjectTypable = mksClusterV2OIDCType{}

func newMKSClusterV2OIDCType() mksClusterV2OIDCType {
	return mksClusterV2OIDCType{ObjectType: basetypes.ObjectType{AttrTypes: mksClusterV2OIDCAttrTypes}}
}

func (t mksClusterV2OIDCType) Equal(o attr.Type) bool {
	other, ok := o.(mksClusterV2OIDCType)

	return ok && t.ObjectType.Equal(other.ObjectType)
}

func (t mksClusterV2OIDCType) String() string {
	return "mksClusterV2OIDCType"
}

func (t mksClusterV2OIDCType) ValueFromObject(_ context.Context, in basetypes.ObjectValue) (basetypes.ObjectValuable, diag.Diagnostics) {
	return mksClusterV2OIDCValue{ObjectValue: in}, nil
}

func (t mksClusterV2OIDCType) ValueFromTerraform(ctx context.Context, in tftypes.Value) (attr.Value, error) {
	value, err := t.ObjectType.ValueFromTerraform(ctx, in)
	if err != nil {
		return nil, err
	}
	objectValue, ok := value.(basetypes.ObjectValue)
	if !ok {
		return nil, fmt.Errorf("unexpected value type %T", value)
	}

	return mksClusterV2OIDCValue{ObjectValue: objectValue}, nil
}

func (t mksClusterV2OIDCType) ValueType(_ context.Context) attr.Value {
	return mksClusterV2OIDCValue{}
}

type mksClusterV2OIDCValue struct {
	basetypes.ObjectValue
}

var _ basetypes.ObjectValuableWithSemanticEquals = mksClusterV2OIDCValue{}

func (v mksClusterV2OIDCValue) Equal(o attr.Value) bool {
	other, ok := o.(mksClusterV2OIDCValue)

	return ok && v.ObjectValue.Equal(other.ObjectValue)
}

func (v mksClusterV2OIDCValue) Type(_ context.Context) attr.Type {
	return newMKSClusterV2OIDCType()
}

func (v mksClusterV2OIDCValue) ObjectSemanticEquals(_ context.Context, priorValuable basetypes.ObjectValuable) (bool, diag.Diagnostics) {
	var diags diag.Diagnostics

	prior, ok := priorValuable.(mksClusterV2OIDCValue)
	if !ok {
		diags.AddError("Semantic Equality Check Error", fmt.Sprintf("Expected mksClusterV2OIDCValue, got %T.", priorValuable))

		return false, diags
	}

	return v.disabled() && prior.disabled(), diags
}

func (v mksClusterV2OIDCValue) disabled() bool {
	if v.IsNull() || v.IsUnknown() {
		return false
	}
	enabled, ok := v.Attributes()["enabled"].(types.Bool)

	return ok && !enabled.IsNull() && !enabled.IsUnknown() && !enabled.ValueBool()
}
