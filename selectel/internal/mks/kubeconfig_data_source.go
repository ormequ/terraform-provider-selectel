package mks

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/selectel/mks-go/v2/pkg/cluster"
	"github.com/terraform-providers/terraform-provider-selectel/selectel/internal/schemadoc"
)

type kubeconfigDataSource struct{ dataConnection }

func NewKubeconfigDataSource() datasource.DataSource { return &kubeconfigDataSource{} }

func (d *kubeconfigDataSource) Metadata(
	_ context.Context,
	req datasource.MetadataRequest,
	resp *datasource.MetadataResponse,
) {
	resp.TypeName = req.ProviderTypeName + "_mks_kubeconfig_v2"
}

type kubeconfigDataModel struct {
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

func (d *kubeconfigDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Provides a kubeconfig file and its fields for a Selectel Managed Kubernetes cluster.",
		Attributes: map[string]schema.Attribute{
			"id":         clusterDocs.ID(),
			"project_id": clusterDocs.OptionalProjectID(),
			"pool":       clusterDocs.Region(),
			"cluster_id": clusterDocs.String(schema.StringAttribute{Required: true}, schemadoc.Field{
				Definition: "Unique identifier of the cluster.",
			}),
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

func (d *kubeconfigDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config kubeconfigDataModel
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
	found, err := cluster.Get(ctx, client, config.ClusterID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Read cluster", err.Error())
		return
	}
	raw, err := cluster.GetKubeconfig(ctx, client, found.Id)
	if err != nil {
		resp.Diagnostics.AddError("Read kubeconfig", err.Error())
		return
	}
	fields, err := parseKubeconfig(raw)
	if err != nil {
		resp.Diagnostics.AddError("Parse kubeconfig", err.Error())
		return
	}
	config.ID = config.ClusterID
	config.ProjectID = types.StringValue(projectID)
	config.RawConfig = types.StringValue(fields.raw)
	config.Server = types.StringValue(fields.server)
	config.ClusterCACert = types.StringValue(fields.clusterCA)
	config.ClientCert = types.StringValue(fields.clientCert)
	config.ClientKey = types.StringValue(fields.clientKey)
	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}

type kubeconfigFields struct {
	raw        string
	server     string
	clusterCA  string
	clientCert string
	clientKey  string
}

// parseKubeconfig extracts the fields the way mks-go v1
// cluster.GetParsedKubeconfig does, so _v1 and _v2 return the same values.
func parseKubeconfig(kubeconfig []byte) (kubeconfigFields, error) {
	fields := kubeconfigFields{raw: string(kubeconfig)}
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
		value, err := kubeconfigField(kubeconfig, target.name)
		if err != nil {
			return kubeconfigFields{}, err
		}
		*target.value = value
	}

	return fields, nil
}

func kubeconfigField(kubeconfig []byte, fieldName string) (string, error) {
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
