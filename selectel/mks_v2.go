package selectel

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	dsschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/selectel/mks-go/pkg/v1/kubeoptions"
	mksv2 "github.com/selectel/mks-go/v2/pkg"
)

// mksV2ClientFn builds the mk-api-v2 client for every _v2 resource and data
// source. Tests replace it with a client of a fake server.
var mksV2ClientFn = newMKSV2Client

func newMKSV2Client(_ context.Context, config *Config, projectID, pool string) (*mksv2.ServiceClient, error) {
	selvpcClient, err := config.GetSelVPCClientWithProjectScope(projectID)
	if err != nil {
		return nil, fmt.Errorf("can't get project-scope selvpc client for mks: %w", err)
	}

	err = validateRegion(selvpcClient, MKS, pool)
	if err != nil {
		return nil, fmt.Errorf("can't validate pool: %w", err)
	}

	endpoint, err := selvpcClient.Catalog.GetEndpoint(MKS, pool)
	if err != nil {
		return nil, fmt.Errorf("can't get endpoint to init mks client: %w", err)
	}

	endpointURL, err := url.Parse(endpoint.URL)
	if err != nil {
		return nil, fmt.Errorf("can't parse mks endpoint %q: %w", endpoint.URL, err)
	}

	// The catalog endpoint ends with /v1, while the v2 client paths start with /v2/.
	baseURL := endpointURL.Scheme + "://" + endpointURL.Host

	return newMKSV2ServiceClient(selvpcClient.GetXAuthToken(), baseURL, config.UserAgent)
}

// newMKSV2ServiceClient is the part of newMKSV2Client that needs no Keystone.
func newMKSV2ServiceClient(token, baseURL, userAgent string) (*mksv2.ServiceClient, error) {
	client, err := mksv2.NewMKSClientV2(token, baseURL)
	if err != nil {
		return nil, fmt.Errorf("can't init mks v2 client: %w", err)
	}
	client.UserAgent = userAgent

	return client, nil
}

// mksV2ProjectID returns the project_id attribute, or the provider one when
// the attribute is not set.
func mksV2ProjectID(attr types.String, config *Config) (string, diag.Diagnostics) {
	var diags diag.Diagnostics

	if !attr.IsNull() && !attr.IsUnknown() && attr.ValueString() != "" {
		return attr.ValueString(), diags
	}
	if config != nil && config.ProjectID != "" {
		return config.ProjectID, diags
	}

	diags.AddAttributeError(path.Root("project_id"), "Missing project ID",
		"Set project_id in the data source or in the provider configuration.")

	return "", diags
}

// mksV2DataSource holds the provider Config for the _v2 data sources.
type mksV2DataSource struct {
	config *Config
}

func (d *mksV2DataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	config, ok := req.ProviderData.(*Config)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data",
			fmt.Sprintf("Expected *Config, got %T. Please report this issue to the provider developers.", req.ProviderData))

		return
	}

	d.config = config
}

// client resolves the project ID and builds the mk-api-v2 client for it.
func (d *mksV2DataSource) client(ctx context.Context, projectIDAttr types.String, pool string) (*mksv2.ServiceClient, string, diag.Diagnostics) {
	projectID, diags := mksV2ProjectID(projectIDAttr, d.config)
	if diags.HasError() {
		return nil, "", diags
	}

	client, err := mksV2ClientFn(ctx, d.config, projectID, pool)
	if err != nil {
		diags.AddError("Error initializing MKS client", err.Error())

		return nil, "", diags
	}

	return client, projectID, diags
}

// valueOrZero dereferences an optional field of an mk-api-v2 model.
func valueOrZero[T any](v *T) T {
	if v == nil {
		var zero T

		return zero
	}

	return *v
}

// mksKubeOptionsV2DataSource serves selectel_mks_feature_gates_v2 and
// selectel_mks_admission_controllers_v2, which differ only in names and the
// API call, like their _v1 counterparts.
type mksKubeOptionsV2DataSource struct {
	mksV2DataSource

	typeName    string
	attribute   string
	object      string
	description string
	docs        mksKubeOptionsV2Docs
	list        func(ctx context.Context, client *mksv2.ServiceClient) ([]*kubeoptions.View, error)
	flatten     func(views []*kubeoptions.View) []any
}

type mksKubeOptionsV2Docs struct {
	filter            string
	filterKubeVersion string
	options           string
	names             string
}

type mksKubeOptionsFilterV2Model struct {
	KubeVersion types.String `tfsdk:"kube_version"`
}

type mksKubeOptionV2Model struct {
	KubeVersion types.String `tfsdk:"kube_version"`
	Names       types.Set    `tfsdk:"names"`
}

var mksKubeOptionV2Type = types.ObjectType{AttrTypes: map[string]attr.Type{
	"kube_version": types.StringType,
	"names":        types.SetType{ElemType: types.StringType},
}}

var mksKubeOptionsV2PoolDocs = resourceDocs{Name: "cluster"}

func (d *mksKubeOptionsV2DataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + d.typeName
}

func (d *mksKubeOptionsV2DataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = dsschema.Schema{
		Description: d.description,
		Attributes: map[string]dsschema.Attribute{
			"id": dsschema.StringAttribute{
				Computed:    true,
				Description: "Checksum of the returned list.",
			},
			"project_id": projectIDFrameworkDataSourceSchema(),
			"pool":       mksKubeOptionsV2PoolDocs.regionFrameworkDataSourceSchema(),
			"filter": dsschema.SetNestedAttribute{
				Optional:    true,
				Description: d.docs.filter,
				NestedObject: dsschema.NestedAttributeObject{
					Attributes: map[string]dsschema.Attribute{
						"kube_version": dsschema.StringAttribute{
							Required:    true,
							Description: d.docs.filterKubeVersion,
						},
					},
				},
			},
			d.attribute: dsschema.SetNestedAttribute{
				Computed:    true,
				Description: d.docs.options,
				NestedObject: dsschema.NestedAttributeObject{
					Attributes: map[string]dsschema.Attribute{
						"kube_version": dsschema.StringAttribute{
							Computed:    true,
							Description: "Kubernetes version.",
						},
						"names": dsschema.SetAttribute{
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

func (d *mksKubeOptionsV2DataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var (
		projectIDAttr, pool types.String
		filterSet           types.Set
		filter              []mksKubeOptionsFilterV2Model
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

	client, projectID, diags := d.client(ctx, projectIDAttr, pool.ValueString())
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	views, err := d.list(ctx, client)
	if err != nil {
		resp.Diagnostics.AddError("Error reading "+d.object, errGettingObjects(d.object, err).Error())

		return
	}

	options, checksum, err := d.filterViews(views, filter)
	if err != nil {
		resp.Diagnostics.AddError("Error reading "+d.object, err.Error())

		return
	}

	models := make([]mksKubeOptionV2Model, len(options))
	for i, option := range options {
		// Copy and deduplicate: a framework set rejects duplicates, an SDKv2 set drops them.
		names := append([]string{}, option.Names...)
		slices.Sort(names)
		nameSet, diags := types.SetValueFrom(ctx, types.StringType, slices.Compact(names))
		resp.Diagnostics.Append(diags...)
		models[i] = mksKubeOptionV2Model{KubeVersion: types.StringValue(option.KubeVersion), Names: nameSet}
	}
	optionSet, diags := types.SetValueFrom(ctx, mksKubeOptionV2Type, models)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), checksum)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), projectID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("pool"), pool)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("filter"), filterSet)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root(d.attribute), optionSet)...)
}

// filterViews applies the filter the way the _v1 data sources do and returns
// the same checksum they use as the ID.
func (d *mksKubeOptionsV2DataSource) filterViews(views []*kubeoptions.View, filter []mksKubeOptionsFilterV2Model) ([]*kubeoptions.View, string, error) {
	if len(filter) == 0 {
		checksum, err := interfaceListChecksum(d.flatten(views))

		return views, checksum, err
	}

	kubeVersion := filter[0].KubeVersion.ValueString()
	if kubeVersion == "" {
		return nil, "", errors.New("kubernetes version is not set")
	}
	kubeMinorVersion, err := kubeVersionTrimToMinor(kubeVersion)
	if err != nil {
		return nil, "", err
	}

	names, err := filterKubeOptionsByKubeVersion(views, kubeMinorVersion)
	if err != nil {
		return nil, "", err
	}

	// stringListChecksum sorts its argument in place.
	checksum, err := stringListChecksum(slices.Clone(names))
	if err != nil {
		return nil, "", err
	}

	return []*kubeoptions.View{{KubeVersion: kubeMinorVersion, Names: names}}, checksum, nil
}
