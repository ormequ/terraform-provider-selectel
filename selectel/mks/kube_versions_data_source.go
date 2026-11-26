package mks

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/selectel/mks-go/v2/pkg/kubeversion"
)

type kubeVersionsDataSource struct{ dataConnection }

func NewKubeVersionsDataSource() datasource.DataSource { return &kubeVersionsDataSource{} }

type kubeVersionsDataModel struct {
	ID             types.String `tfsdk:"id"`
	Pool           types.String `tfsdk:"pool"`
	ProjectID      types.String `tfsdk:"project_id"`
	LatestVersion  types.String `tfsdk:"latest_version"`
	DefaultVersion types.String `tfsdk:"default_version"`
	Versions       types.List   `tfsdk:"versions"`
}

func (d *kubeVersionsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_mks_kube_versions_v2"
}

func (d *kubeVersionsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Provides a list of supported Kubernetes versions in Managed Kubernetes. " +
			"For more information about Managed Kubernetes, see the [official Selectel documentation](https://docs.selectel.ru/en/managed-kubernetes/about/about-managed-kubernetes/).",
		Attributes: map[string]schema.Attribute{
			"id":              schema.StringAttribute{Computed: true, Description: "Pool of the Kubernetes versions."},
			"pool":            dataSourceDocs.Region(),
			"project_id":      dataSourceProjectID,
			"latest_version":  schema.StringAttribute{Computed: true, Description: "Most recent version."},
			"default_version": schema.StringAttribute{Computed: true, Description: "Kubernetes version used by default."},
			"versions": schema.ListAttribute{
				Computed:    true,
				ElementType: types.StringType,
				Description: "List of the supported versions.",
			},
		},
	}
}

func (d *kubeVersionsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config kubeVersionsDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	ctx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()

	client, projectID, diags := d.client(ctx, config.ProjectID, config.Pool.ValueString())
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	kubeVersions, err := kubeversion.List(ctx, client)
	if err != nil {
		resp.Diagnostics.AddError("Error reading "+objectKubeVersions, errGettingObjects(objectKubeVersions, err).Error())

		return
	}

	versions := make([]string, len(kubeVersions))
	var latestVersion, defaultVersion string
	for i, kubeVersion := range kubeVersions {
		versions[i] = valueOrZero(kubeVersion.Version)
		if valueOrZero(kubeVersion.IsDefault) {
			defaultVersion = versions[i]
		}
		if i == 0 {
			latestVersion = versions[i]
		}
		order, err := compareKubeVersions(versions[i], latestVersion)
		if err != nil {
			resp.Diagnostics.AddError("Error reading "+objectKubeVersions, errGettingObjects(objectKubeVersions, err).Error())

			return
		}
		if order > 0 {
			latestVersion = versions[i]
		}
	}

	config.ID = config.Pool
	config.ProjectID = types.StringValue(projectID)
	config.LatestVersion = types.StringValue(latestVersion)
	config.DefaultVersion = types.StringValue(defaultVersion)
	config.Versions, diags = types.ListValueFrom(ctx, types.StringType, versions)
	resp.Diagnostics.Append(diags...)
	resp.Diagnostics.Append(resp.State.Set(ctx, config)...)
}
