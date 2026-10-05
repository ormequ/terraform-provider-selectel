package selectel

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/identityschema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// PoC stub: the schema, docs, identity and MoveState are real, CRUD is not.

var mksClusterV2Docs = resourceDocs{Name: "cluster"}

var (
	_ resource.ResourceWithIdentity    = &mksClusterV2Resource{}
	_ resource.ResourceWithImportState = &mksClusterV2Resource{}
	_ resource.ResourceWithMoveState   = &mksClusterV2Resource{}
)

type mksClusterV2Resource struct{}

func newMKSClusterV2Resource() resource.Resource {
	return &mksClusterV2Resource{}
}

type mksClusterV2Model struct {
	ID                     types.String `tfsdk:"id"`
	ProjectID              types.String `tfsdk:"project_id"`
	Region                 types.String `tfsdk:"region"`
	Name                   types.String `tfsdk:"name"`
	KubeVersion            types.String `tfsdk:"kube_version"`
	EnableAutorepair       types.Bool   `tfsdk:"enable_autorepair"`
	MaintenanceWindowStart types.String `tfsdk:"maintenance_window_start"`
	OIDC                   types.Object `tfsdk:"oidc"`
}

type mksClusterV2IdentityModel struct {
	ID        types.String `tfsdk:"id"`
	ProjectID types.String `tfsdk:"project_id"`
	Region    types.String `tfsdk:"region"`
}

var mksClusterV2OIDCAttrTypes = map[string]attr.Type{
	"enabled":        types.BoolType,
	"issuer_url":     types.StringType,
	"username_claim": types.StringType,
}

func (r *mksClusterV2Resource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_mks_cluster_v2"
}

func (r *mksClusterV2Resource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Creates and manages a Managed Kubernetes cluster using the V2 public API. PoC stub built on terraform-plugin-framework.",
		Attributes: mksClusterV2Docs.withFrameworkDocsHints(map[string]schema.Attribute{
			"id":         mksClusterV2Docs.idResourceAttribute(),
			"project_id": projectIDResourceAttribute(),
			"region":     mksClusterV2Docs.regionResourceAttribute(),
			"name": schema.StringAttribute{
				Required:      true,
				Description:   "Cluster name.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"kube_version": schema.StringAttribute{
				Required:    true,
				Description: "Kubernetes version of the cluster.",
			},
			"enable_autorepair": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(true),
				Description: "Enables (`true`) or disables (`false`) node auto-repair.",
			},
			"maintenance_window_start": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString(""),
				Description: "Start time of the maintenance window in UTC, for example, `01:00:00`.",
			},
			"oidc": schema.SingleNestedAttribute{
				Optional:    true,
				Description: "OIDC authentication settings of the Kubernetes API.",
				Attributes: map[string]schema.Attribute{
					"enabled": schema.BoolAttribute{
						Required:    true,
						Description: "Enables (`true`) or disables (`false`) OIDC authentication.",
					},
					"issuer_url": schema.StringAttribute{
						Optional:    true,
						Description: "URL of the OIDC provider.",
					},
					"username_claim": schema.StringAttribute{
						Optional:    true,
						Computed:    true,
						Default:     stringdefault.StaticString("sub"),
						Description: "JWT claim to use as the user name.",
					},
				},
			},
		}),
	}
}

func (r *mksClusterV2Resource) IdentitySchema(_ context.Context, _ resource.IdentitySchemaRequest, resp *resource.IdentitySchemaResponse) {
	resp.IdentitySchema = identityschema.Schema{
		Attributes: map[string]identityschema.Attribute{
			"id": mksClusterV2Docs.idIdentityAttribute(
				"To get the cluster ID, in the [Control panel](https://my.selectel.ru/vpc/mks/), go to **Cloud Platform** ⟶ **Kubernetes** ⟶ copy the ID of the cluster.",
			),
			"project_id": projectIDIdentityAttribute(),
			"region": mksClusterV2Docs.regionIdentityAttribute(
				"To get information about the pool, in the [Control panel](https://my.selectel.ru/vpc/mks/), go to **Cloud Platform** ⟶ **Kubernetes**. The pool is in the **Pool** column.",
			),
		},
	}
}

func (r *mksClusterV2Resource) Create(_ context.Context, _ resource.CreateRequest, resp *resource.CreateResponse) {
	resp.Diagnostics.AddError("Not implemented", "selectel_mks_cluster_v2 is a PoC stub")
}

func (r *mksClusterV2Resource) Read(_ context.Context, _ resource.ReadRequest, resp *resource.ReadResponse) {
	resp.Diagnostics.AddError("Not implemented", "selectel_mks_cluster_v2 is a PoC stub")
}

func (r *mksClusterV2Resource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError("Not implemented", "selectel_mks_cluster_v2 is a PoC stub")
}

func (r *mksClusterV2Resource) Delete(_ context.Context, _ resource.DeleteRequest, resp *resource.DeleteResponse) {
	resp.Diagnostics.AddError("Not implemented", "selectel_mks_cluster_v2 is a PoC stub")
}

func (r *mksClusterV2Resource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughWithIdentity(ctx, path.Root("id"), path.Root("id"), req, resp)
}

// mksClusterV1RawState is the part of a selectel_mks_cluster_v1 state (SDKv2,
// schema version 0) that the move maps.
type mksClusterV1RawState struct {
	ID                     string `json:"id"`
	ProjectID              string `json:"project_id"`
	Region                 string `json:"region"`
	Name                   string `json:"name"`
	KubeVersion            string `json:"kube_version"`
	EnableAutorepair       bool   `json:"enable_autorepair"`
	MaintenanceWindowStart string `json:"maintenance_window_start"`
	OIDC                   []struct {
		Enabled       bool   `json:"enabled"`
		IssuerURL     string `json:"issuer_url"`
		UsernameClaim string `json:"username_claim"`
	} `json:"oidc"`
}

func (r *mksClusterV2Resource) MoveState(_ context.Context) []resource.StateMover {
	return []resource.StateMover{{StateMover: moveFromMKSClusterV1}}
}

// SDKv2 stores an unset optional string as "". Moved into the framework schema,
// such a value becomes the attribute's Default when it has one and null when it
// is Optional without a Default; Required attributes keep the value as is.
func emptyStringToDefault(v, def string) types.String {
	if v == "" {
		return types.StringValue(def)
	}

	return types.StringValue(v)
}

func emptyStringToNull(v string) types.String {
	if v == "" {
		return types.StringNull()
	}

	return types.StringValue(v)
}

func moveFromMKSClusterV1(ctx context.Context, req resource.MoveStateRequest, resp *resource.MoveStateResponse) {
	if req.SourceTypeName != "selectel_mks_cluster_v1" || req.SourceSchemaVersion != 0 ||
		!strings.HasSuffix(req.SourceProviderAddress, "selectel/selectel") {
		return
	}

	var src mksClusterV1RawState
	err := json.Unmarshal(req.SourceRawState.JSON, &src)
	if err != nil {
		resp.Diagnostics.AddError("Unable to read selectel_mks_cluster_v1 state", err.Error())
		return
	}

	oidc := types.ObjectNull(mksClusterV2OIDCAttrTypes)
	if len(src.OIDC) > 0 {
		var diags diag.Diagnostics
		oidc, diags = types.ObjectValue(mksClusterV2OIDCAttrTypes, map[string]attr.Value{
			"enabled":        types.BoolValue(src.OIDC[0].Enabled),
			"issuer_url":     emptyStringToNull(src.OIDC[0].IssuerURL),
			"username_claim": emptyStringToDefault(src.OIDC[0].UsernameClaim, "sub"),
		})
		resp.Diagnostics.Append(diags...)
	}

	resp.Diagnostics.Append(resp.TargetState.Set(ctx, mksClusterV2Model{
		ID:                     types.StringValue(src.ID),
		ProjectID:              types.StringValue(src.ProjectID),
		Region:                 types.StringValue(src.Region),
		Name:                   types.StringValue(src.Name),
		KubeVersion:            types.StringValue(src.KubeVersion),
		EnableAutorepair:       types.BoolValue(src.EnableAutorepair),
		MaintenanceWindowStart: emptyStringToDefault(src.MaintenanceWindowStart, ""),
		OIDC:                   oidc,
	})...)

	if resp.TargetIdentity != nil {
		resp.Diagnostics.Append(resp.TargetIdentity.Set(ctx, mksClusterV2IdentityModel{
			ID:        types.StringValue(src.ID),
			ProjectID: types.StringValue(src.ProjectID),
			Region:    types.StringValue(src.Region),
		})...)
	}
}
