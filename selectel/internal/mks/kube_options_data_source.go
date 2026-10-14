package mks

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
	mksv2 "github.com/selectel/mks-go/v2/pkg"
)

// kubeOptionsDataSource serves selectel_mks_feature_gates_v2 and
// selectel_mks_admission_controllers_v2, which differ only in names and the
// API call, like their _v1 counterparts.
type kubeOptionsDataSource struct {
	dataConnection

	typeName    string
	attribute   string
	object      string
	description string
	docs        kubeOptionsDocs
	list        func(ctx context.Context, client *mksv2.ServiceClient) ([]kubeOptions, error)
}

type kubeOptionsDocs struct {
	filter            string
	filterKubeVersion string
	options           string
	names             string
}

// kubeOptions are the option names available for a Kubernetes minor version.
type kubeOptions struct {
	kubeVersion string
	names       []string
}

type kubeOptionsFilterModel struct {
	KubeVersion types.String `tfsdk:"kube_version"`
}

type kubeOptionsModel struct {
	KubeVersion types.String `tfsdk:"kube_version"`
	Names       types.Set    `tfsdk:"names"`
}

var kubeOptionsType = types.ObjectType{AttrTypes: map[string]attr.Type{
	"kube_version": types.StringType,
	"names":        types.SetType{ElemType: types.StringType},
}}

func (d *kubeOptionsDataSource) Metadata(
	_ context.Context,
	req datasource.MetadataRequest,
	resp *datasource.MetadataResponse,
) {
	resp.TypeName = req.ProviderTypeName + d.typeName
}

func (d *kubeOptionsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: d.description,
		Attributes: map[string]schema.Attribute{
			"id":         schema.StringAttribute{Computed: true, Description: "Checksum of the returned list."},
			"project_id": clusterDocs.OptionalProjectID(),
			"pool":       clusterDocs.Region(),
			"filter": schema.SetNestedAttribute{
				Optional:    true,
				Description: d.docs.filter,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"kube_version": schema.StringAttribute{
							Required:    true,
							Description: d.docs.filterKubeVersion,
						},
					},
				},
			},
			d.attribute: schema.SetNestedAttribute{
				Computed:    true,
				Description: d.docs.options,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"kube_version": schema.StringAttribute{
							Computed:    true,
							Description: "Kubernetes version.",
						},
						"names": schema.SetAttribute{
							Computed:    true,
							ElementType: types.StringType,
							Description: d.docs.names,
						},
					},
				},
			},
		},
	}
}

func (d *kubeOptionsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var (
		projectIDAttr, pool types.String
		filterSet           types.Set
		filter              []kubeOptionsFilterModel
	)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("project_id"), &projectIDAttr)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("pool"), &pool)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("filter"), &filterSet)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(filterSet.ElementsAs(ctx, &filter, false)...)
	if resp.Diagnostics.HasError() {
		return
	}
	projectID, diags := d.projectID(projectIDAttr)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	client, err := d.client(ctx, projectID, pool.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Connect to Managed Kubernetes", err.Error())
		return
	}
	all, err := d.list(ctx, client)
	if err != nil {
		resp.Diagnostics.AddError("Read "+d.object, err.Error())
		return
	}
	options, id, err := filterKubeOptions(all, filter)
	if err != nil {
		resp.Diagnostics.AddError("Filter "+d.object, err.Error())
		return
	}
	models := make([]kubeOptionsModel, len(options))
	for i, option := range options {
		// Copy and deduplicate: a framework set rejects duplicates, an SDKv2 set drops them.
		names := slices.Compact(slices.Sorted(slices.Values(option.names)))
		nameSet, diags := types.SetValueFrom(ctx, types.StringType, names)
		resp.Diagnostics.Append(diags...)
		models[i] = kubeOptionsModel{KubeVersion: types.StringValue(option.kubeVersion), Names: nameSet}
	}
	optionSet, diags := types.SetValueFrom(ctx, kubeOptionsType, models)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), projectID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("pool"), pool)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("filter"), filterSet)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root(d.attribute), optionSet)...)
}

// filterKubeOptions applies the filter the way the _v1 data sources do and
// returns the same checksum they use as the ID.
func filterKubeOptions(all []kubeOptions, filter []kubeOptionsFilterModel) ([]kubeOptions, string, error) {
	if len(filter) == 0 {
		items := make([]string, len(all))
		for i, option := range all {
			items[i] = fmt.Sprint(map[string]any{"kube_version": option.kubeVersion, "names": option.names})
		}

		return all, checksum(items...), nil
	}
	if filter[0].KubeVersion.ValueString() == "" {
		return nil, "", errors.New("kubernetes version is not set")
	}
	kubeVersion, err := minorVersion(filter[0].KubeVersion.ValueString())
	if err != nil {
		return nil, "", err
	}
	for _, option := range all {
		if option.kubeVersion == kubeVersion {
			return []kubeOptions{option}, sortedChecksum(option.names), nil
		}
	}

	return nil, "", fmt.Errorf("available kubernetes options for kubernetes version %q is not found", kubeVersion)
}
