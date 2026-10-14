package mks

import (
	"context"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/selectel/mks-go/v2/pkg/kubeversion"
)

type kubeVersionsDataSource struct{ dataConnection }

func NewKubeVersionsDataSource() datasource.DataSource { return &kubeVersionsDataSource{} }

func (d *kubeVersionsDataSource) Metadata(
	_ context.Context,
	req datasource.MetadataRequest,
	resp *datasource.MetadataResponse,
) {
	resp.TypeName = req.ProviderTypeName + "_mks_kube_versions_v2"
}

type kubeVersionsDataModel struct {
	ID             types.String `tfsdk:"id"`
	ProjectID      types.String `tfsdk:"project_id"`
	Pool           types.String `tfsdk:"pool"`
	LatestVersion  types.String `tfsdk:"latest_version"`
	DefaultVersion types.String `tfsdk:"default_version"`
	Versions       types.List   `tfsdk:"versions"`
}

func (d *kubeVersionsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Provides a list of Kubernetes versions supported in a Selectel Managed Kubernetes cluster.",
		Attributes: map[string]schema.Attribute{
			"id":         schema.StringAttribute{Computed: true, Description: "Checksum of the version list."},
			"project_id": clusterDocs.OptionalProjectID(),
			"pool":       clusterDocs.Region(),
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

func (d *kubeVersionsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config kubeVersionsDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	projectID, diags := d.projectID(config.ProjectID)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	client, err := d.client(ctx, projectID, config.Pool.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Connect to Managed Kubernetes", err.Error())
		return
	}
	all, err := kubeversion.List(ctx, client)
	if err != nil {
		resp.Diagnostics.AddError("Read Kubernetes versions", err.Error())
		return
	}
	versions := make([]string, len(all))
	defaultVersion := ""
	for i, item := range all {
		versions[i] = valueOrZero(item.Version)
		if valueOrZero(item.IsDefault) {
			defaultVersion = versions[i]
		}
	}
	latest, err := latestVersion(versions)
	if err != nil {
		resp.Diagnostics.AddError("Find the latest Kubernetes version", err.Error())
		return
	}
	config.ID = types.StringValue(sortedChecksum(versions))
	config.ProjectID = types.StringValue(projectID)
	config.LatestVersion = types.StringValue(latest)
	config.DefaultVersion = types.StringValue(defaultVersion)
	config.Versions, diags = types.ListValueFrom(ctx, types.StringType, versions)
	resp.Diagnostics.Append(diags...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}

// valueOrZero dereferences an optional field of an MKS API v2 model.
func valueOrZero[T any](v *T) T {
	if v == nil {
		var zero T

		return zero
	}

	return *v
}
