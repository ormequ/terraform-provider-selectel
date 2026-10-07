package selectel

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/selectel/mks-go/v2/pkg/cluster"
)

var mksKubeconfigV2Docs = resourceDocs{Name: "cluster"}

var _ datasource.DataSourceWithConfigure = &mksKubeconfigV2DataSource{}

func newMKSKubeconfigV2DataSource() datasource.DataSource {
	return &mksKubeconfigV2DataSource{}
}

type mksKubeconfigV2DataSource struct {
	mksV2DataSource
}

type mksKubeconfigV2Model struct {
	ID            types.String `tfsdk:"id"`
	ProjectID     types.String `tfsdk:"project_id"`
	Pool          types.String `tfsdk:"pool"`
	ClusterID     types.String `tfsdk:"cluster_id"`
	RawConfig     types.String `tfsdk:"raw_config"`
	Server        types.String `tfsdk:"server"`
	ClusterCACert types.String `tfsdk:"cluster_ca_cert"`
	ClientCert    types.String `tfsdk:"client_cert"`
	ClientKey     types.String `tfsdk:"client_key"`
}

func (d *mksKubeconfigV2DataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_mks_kubeconfig_v2"
}

func (d *mksKubeconfigV2DataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Provides a kubeconfig file and its fields for a Managed Kubernetes cluster using mk-api-v2.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: mksKubeconfigV2Docs.idDescription() + ".",
			},
			"project_id": projectIDFrameworkDataSourceSchema(),
			"pool":       mksKubeconfigV2Docs.regionFrameworkDataSourceSchema(),
			"cluster_id": schema.StringAttribute{
				Required:    true,
				Description: mksKubeconfigV2Docs.idDescription() + ".",
			},
			"raw_config": schema.StringAttribute{
				Computed:    true,
				Sensitive:   true,
				Description: "Raw content of a kubeconfig file.",
			},
			"server": schema.StringAttribute{
				Computed:    true,
				Sensitive:   true,
				Description: "IP address and port for a Kube API server.",
			},
			"cluster_ca_cert": schema.StringAttribute{
				Computed:    true,
				Sensitive:   true,
				Description: "CA certificate of the cluster.",
			},
			"client_cert": schema.StringAttribute{
				Computed:    true,
				Sensitive:   true,
				Description: "Client certificate for authorization.",
			},
			"client_key": schema.StringAttribute{
				Computed:    true,
				Sensitive:   true,
				Description: "Client key for authorization.",
			},
		},
	}
}

func (d *mksKubeconfigV2DataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data mksKubeconfigV2Model
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	client, projectID, diags := d.client(ctx, data.ProjectID, data.Pool.ValueString())
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	clusterID := data.ClusterID.ValueString()

	mksCluster, err := cluster.Get(ctx, client, clusterID)
	if err != nil {
		resp.Diagnostics.AddError("Error reading kubeconfig", errGettingObject(objectCluster, clusterID, err).Error())

		return
	}

	rawKubeconfig, err := cluster.GetKubeconfig(ctx, client, mksCluster.Id)
	if err != nil {
		resp.Diagnostics.AddError("Error reading kubeconfig", errGettingObject(objectKubeConfig, clusterID, err).Error())

		return
	}

	kubeconfig, err := parseMKSKubeconfigV2(rawKubeconfig)
	if err != nil {
		resp.Diagnostics.AddError("Error reading kubeconfig", errGettingObject(objectKubeConfig, clusterID, err).Error())

		return
	}

	data.ID = types.StringValue(clusterID)
	data.ProjectID = types.StringValue(projectID)
	data.RawConfig = types.StringValue(kubeconfig.raw)
	data.Server = types.StringValue(kubeconfig.server)
	data.ClusterCACert = types.StringValue(kubeconfig.clusterCA)
	data.ClientCert = types.StringValue(kubeconfig.clientCert)
	data.ClientKey = types.StringValue(kubeconfig.clientKey)

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

type mksKubeconfigV2Fields struct {
	raw        string
	server     string
	clusterCA  string
	clientCert string
	clientKey  string
}

// parseMKSKubeconfigV2 extracts the fields the way mks-go v1
// cluster.GetParsedKubeconfig does, so _v1 and _v2 return the same values.
func parseMKSKubeconfigV2(kubeconfig []byte) (mksKubeconfigV2Fields, error) {
	fields := mksKubeconfigV2Fields{raw: string(kubeconfig)}

	targets := []struct {
		name  string
		value *string
	}{
		{"certificate-authority-data", &fields.clusterCA},
		{"server", &fields.server},
		{"client-certificate-data", &fields.clientCert},
		{"client-key-data", &fields.clientKey},
	}
	for _, target := range targets {
		value, err := mksKubeconfigV2Field(kubeconfig, target.name)
		if err != nil {
			return mksKubeconfigV2Fields{}, err
		}
		*target.value = value
	}

	return fields, nil
}

func mksKubeconfigV2Field(kubeconfig []byte, fieldName string) (string, error) {
	s := regexp.MustCompile(regexp.QuoteMeta(fieldName) + ".*").FindString(string(kubeconfig))
	if s == "" {
		return "", fmt.Errorf("unable to find %s field in kubeconfig", fieldName)
	}

	parts := strings.Split(s, " ")
	if len(parts) < 2 {
		return "", fmt.Errorf("invalid %s field in the kubeconfig", fieldName)
	}

	return parts[1], nil
}
