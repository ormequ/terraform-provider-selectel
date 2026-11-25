package mks

import (
	"context"
	"fmt"
	"slices"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	mksv2 "github.com/selectel/mks-go/v2/pkg"
	"github.com/selectel/mks-go/v2/pkg/kubeoptions"
	"github.com/terraform-providers/terraform-provider-selectel/selectel/internal/schemadoc"
)

// kubeOptionsDataSource serves the feature gates and the admission controllers,
// which differ only in names and the API call.
type kubeOptionsDataSource struct {
	dataConnection
	attribute string
	title     string
	docsURL   string
	object    string
	list      func(ctx context.Context, client *mksv2.ServiceClient) ([]kubeOptionsDataModel, error)
}

type kubeOptionsDataModel struct {
	KubeVersion string   `tfsdk:"kube_version"`
	Names       []string `tfsdk:"names"`
}

func NewFeatureGatesDataSource() datasource.DataSource {
	return &kubeOptionsDataSource{
		attribute: "feature_gates",
		title:     "feature gates",
		docsURL:   "https://docs.selectel.ru/en/managed-kubernetes/clusters/feature-gates/",
		object:    objectFeatureGates,
		list: func(ctx context.Context, client *mksv2.ServiceClient) ([]kubeOptionsDataModel, error) {
			featureGates, err := kubeoptions.ListFeatureGates(ctx, client)
			options := make([]kubeOptionsDataModel, len(featureGates))
			for i, item := range featureGates {
				options[i] = kubeOptionsOf(item.KubeVersionMinor, item.Names)
			}

			return options, err
		},
	}
}

func NewAdmissionControllersDataSource() datasource.DataSource {
	return &kubeOptionsDataSource{
		attribute: "admission_controllers",
		title:     "admission controllers",
		docsURL:   "https://docs.selectel.ru/en/managed-kubernetes/clusters/admission-controllers/",
		object:    objectAdmissionControllers,
		list: func(ctx context.Context, client *mksv2.ServiceClient) ([]kubeOptionsDataModel, error) {
			admissionControllers, err := kubeoptions.ListAdmissionControllers(ctx, client)
			options := make([]kubeOptionsDataModel, len(admissionControllers))
			for i, item := range admissionControllers {
				options[i] = kubeOptionsOf(item.KubeVersionMinor, item.Names)
			}

			return options, err
		},
	}
}

func kubeOptionsOf(kubeVersion *string, names *[]string) kubeOptionsDataModel {
	return kubeOptionsDataModel{
		KubeVersion: valueOrZero(kubeVersion),
		// A nil slice would be a null set.
		Names: append([]string{}, valueOrZero(names)...),
	}
}

func (d *kubeOptionsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_mks_" + d.attribute + "_v2"
}

func (d *kubeOptionsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Provides a list of available " + d.title + ". " +
			"For more information about " + d.title + " in Managed Kubernetes, see the [official Selectel documentation](" + d.docsURL + ").",
		Attributes: map[string]schema.Attribute{
			"id":   schema.StringAttribute{Computed: true, Description: "Pool of the " + d.title + "."},
			"pool": dataSourceDocs.Region(),
			"project_id": dataSourceDocs.String(schema.StringAttribute{
				Optional:   true,
				Computed:   true,
				Validators: []validator.String{schemadoc.LengthAtLeast(1)},
			}, projectIDField),
			"kube_version": dataSourceDocs.String(schema.StringAttribute{
				Optional: true,
				Validators: []validator.String{
					schemadoc.DescribeString(stringvalidator.RegexMatches(kubeVersionPattern, "must be in the x.y or x.y.z form"),
						"The format is `x.y` or `x.y.z`."),
				},
			}, schemadoc.Field{
				Definition: "Kubernetes version for which you get available " + d.title + ".",
				Behavior:   "If omitted, the " + d.title + " of all versions are returned.",
				Source:     schemadoc.FromDataSource("selectel_mks_kube_versions_v2", "available versions"),
			}),
			d.attribute: schema.SetNestedAttribute{
				Computed:    true,
				Description: "Set of available " + d.title + ".",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"kube_version": schema.StringAttribute{Computed: true, Description: "Kubernetes version."},
					"names": schema.SetAttribute{
						Computed:    true,
						ElementType: types.StringType,
						Description: "Names of the " + d.title + " available for the specified Kubernetes version.",
					},
				}},
			},
		},
	}
}

func (d *kubeOptionsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var pool, projectIDAttr, kubeVersion types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("pool"), &pool)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("project_id"), &projectIDAttr)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("kube_version"), &kubeVersion)...)
	if resp.Diagnostics.HasError() {
		return
	}

	ctx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()

	client, projectID, diags := d.client(ctx, projectIDAttr, pool.ValueString())
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	options, err := d.list(ctx, client)
	if err != nil {
		resp.Diagnostics.AddError("Error reading "+d.object, errGettingObjects(d.object, err).Error())

		return
	}

	if !kubeVersion.IsNull() {
		minor, err := kubeVersionTrimToMinor(kubeVersion.ValueString())
		if err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("kube_version"), "Error reading "+d.object, err.Error())

			return
		}
		options = slices.DeleteFunc(options, func(o kubeOptionsDataModel) bool { return o.KubeVersion != minor })
		if len(options) == 0 {
			resp.Diagnostics.AddAttributeError(path.Root("kube_version"), "Error reading "+d.object,
				fmt.Sprintf("available kubernetes options for kubernetes version %q is not found", minor))

			return
		}
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), pool)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("pool"), pool)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), projectID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("kube_version"), kubeVersion)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root(d.attribute), options)...)
}
