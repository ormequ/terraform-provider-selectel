package mks

import (
	"context"
	"fmt"
	"regexp"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/selectel/mks-go/v2/pkg/cluster"
	"github.com/terraform-providers/terraform-provider-selectel/selectel/internal/schemadoc"
)

type kubeconfigDataSource struct{ dataConnection }

func NewKubeconfigDataSource() datasource.DataSource { return &kubeconfigDataSource{} }

type kubeconfigDataModel struct {
	ID            types.String `tfsdk:"id"`
	ClusterID     types.String `tfsdk:"cluster_id"`
	Pool          types.String `tfsdk:"pool"`
	ProjectID     types.String `tfsdk:"project_id"`
	RawConfig     types.String `tfsdk:"raw_config"`
	Server        types.String `tfsdk:"server"`
	ClusterCACert types.String `tfsdk:"cluster_ca_cert"`
	ClientCert    types.String `tfsdk:"client_cert"`
	ClientKey     types.String `tfsdk:"client_key"`
}

func (d *kubeconfigDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_mks_kubeconfig_v2"
}

func (d *kubeconfigDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	sensitive := func(description string) schema.StringAttribute {
		return schema.StringAttribute{Computed: true, Sensitive: true, Description: description}
	}

	resp.Schema = schema.Schema{
		MarkdownDescription: "Provides a kubeconfig file and its fields in Managed Kubernetes. " +
			"For more information about connecting to a cluster, see the [official Selectel documentation](https://docs.selectel.ru/en/managed-kubernetes/clusters/connect-to-cluster/).",
		Attributes: map[string]schema.Attribute{
			"id": dataSourceDocs.ID(),
			"cluster_id": dataSourceDocs.String(schema.StringAttribute{
				Required:   true,
				Validators: []validator.String{schemadoc.NonEmptyTrimmed()},
			}, schemadoc.Field{
				Definition: "Unique identifier of the cluster.",
				Source:     schemadoc.FromResource("selectel_mks_cluster_v2", "cluster ID"),
			}),
			"pool":            dataSourceDocs.Region(),
			"project_id":      dataSourceProjectID,
			"raw_config":      sensitive("Raw content of a kubeconfig file."),
			"server":          sensitive("IP address and port for a Kube API server."),
			"cluster_ca_cert": sensitive("CA certificate of the cluster."),
			"client_cert":     sensitive("Client certificate for authorization."),
			"client_key":      sensitive("Client key for authorization."),
		},
	}
}

func (d *kubeconfigDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config kubeconfigDataModel
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

	clusterID := config.ClusterID.ValueString()
	raw, err := cluster.GetKubeconfig(ctx, client, clusterID)
	if err != nil {
		resp.Diagnostics.AddError("Error reading "+objectKubeConfig, errGettingObject(objectKubeConfig, clusterID, err).Error())

		return
	}

	for _, field := range []struct {
		name  string
		value *types.String
	}{
		{"certificate-authority-data", &config.ClusterCACert},
		{"server", &config.Server},
		{"client-certificate-data", &config.ClientCert},
		{"client-key-data", &config.ClientKey},
	} {
		match := regexp.MustCompile(`(?m)^[ \t]*` + field.name + `:[ \t]*(\S+)`).FindSubmatch(raw)
		if match == nil {
			err = fmt.Errorf("unable to find %s field in kubeconfig", field.name)
			resp.Diagnostics.AddError("Error reading "+objectKubeConfig, errGettingObject(objectKubeConfig, clusterID, err).Error())

			return
		}
		*field.value = types.StringValue(string(match[1]))
	}

	config.ID = config.ClusterID
	config.ProjectID = types.StringValue(projectID)
	config.RawConfig = types.StringValue(string(raw))
	resp.Diagnostics.Append(resp.State.Set(ctx, config)...)
}
