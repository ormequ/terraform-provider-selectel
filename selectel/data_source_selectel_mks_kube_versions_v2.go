package selectel

import (
	"context"
	"slices"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/selectel/mks-go/pkg/v1/kubeversion"
	kubeversionv2 "github.com/selectel/mks-go/v2/pkg/kubeversion"
	"github.com/selectel/mks-go/v2/pkg/mksclient"
)

var mksKubeVersionsV2Docs = resourceDocs{Name: "cluster"}

var _ datasource.DataSourceWithConfigure = &mksKubeVersionsV2DataSource{}

func newMKSKubeVersionsV2DataSource() datasource.DataSource {
	return &mksKubeVersionsV2DataSource{}
}

type mksKubeVersionsV2DataSource struct {
	mksV2DataSource
}

type mksKubeVersionsV2Model struct {
	ID             types.String `tfsdk:"id"`
	ProjectID      types.String `tfsdk:"project_id"`
	Pool           types.String `tfsdk:"pool"`
	LatestVersion  types.String `tfsdk:"latest_version"`
	DefaultVersion types.String `tfsdk:"default_version"`
	Versions       types.List   `tfsdk:"versions"`
}

func (d *mksKubeVersionsV2DataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_mks_kube_versions_v2"
}

func (d *mksKubeVersionsV2DataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Provides a list of Kubernetes versions available in Managed Kubernetes using mk-api-v2.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "Checksum of the version list.",
			},
			"project_id": projectIDFrameworkDataSourceSchema(),
			"pool":       mksKubeVersionsV2Docs.regionFrameworkDataSourceSchema(),
			"latest_version": schema.StringAttribute{
				Computed:    true,
				Description: "The latest available Kubernetes version.",
			},
			"default_version": schema.StringAttribute{
				Computed:    true,
				Description: "Kubernetes version used by default.",
			},
			"versions": schema.ListAttribute{
				Computed:    true,
				ElementType: types.StringType,
				Description: "List of available Kubernetes versions.",
			},
		},
	}
}

func (d *mksKubeVersionsV2DataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data mksKubeVersionsV2Model
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	client, projectID, diags := d.client(ctx, data.ProjectID, data.Pool.ValueString())
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	kubeVersions, err := kubeversionv2.List(ctx, client)
	if err != nil {
		resp.Diagnostics.AddError("Error reading Kubernetes versions", errGettingObjects(objectKubeVersions, err).Error())

		return
	}

	// The _v1 helpers keep latest_version and default_version identical to
	// selectel_mks_kube_versions_v1.
	views := mksKubeVersionsV2ToV1Views(kubeVersions)
	latestVersion, err := parseMKSKubeVersionsV1Latest(views)
	if err != nil {
		resp.Diagnostics.AddError("Error reading Kubernetes versions", err.Error())

		return
	}
	versions := flattenMKSKubeVersionsV1(views)

	versionList, diags := types.ListValueFrom(ctx, types.StringType, versions)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// stringListChecksum sorts its argument in place.
	checksum, err := stringListChecksum(slices.Clone(versions))
	if err != nil {
		resp.Diagnostics.AddError("Error reading Kubernetes versions", err.Error())

		return
	}

	data.ID = types.StringValue(checksum)
	data.ProjectID = types.StringValue(projectID)
	data.LatestVersion = types.StringValue(latestVersion)
	data.DefaultVersion = types.StringValue(parseMKSKubeVersionsV1Default(views))
	data.Versions = versionList

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func mksKubeVersionsV2ToV1Views(kubeVersions []mksclient.KubeVersionInfo) []*kubeversion.View {
	views := make([]*kubeversion.View, len(kubeVersions))
	for i, kubeVersion := range kubeVersions {
		views[i] = &kubeversion.View{
			Version:   valueOrZero(kubeVersion.Version),
			IsDefault: valueOrZero(kubeVersion.IsDefault),
		}
	}

	return views
}
