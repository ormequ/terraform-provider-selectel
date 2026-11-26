package mks

import (
	"context"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/terraform-providers/terraform-provider-selectel/selectel/internal/schemadoc"
)

// readTimeout bounds a data source read, which has no configurable timeout.
const readTimeout = 2 * time.Minute

const (
	objectKubeConfig           = "kubeconfig"
	objectKubeVersions         = "kube-versions"
	objectFeatureGates         = "feature-gates"
	objectAdmissionControllers = "admission-controllers"
)

var dataSourceDocs = schemadoc.DataSource{Name: "cluster", Location: "managed-kubernetes"}

var dataSourceProjectID = dataSourceDocs.String(schema.StringAttribute{
	Optional:   true,
	Computed:   true,
	Validators: []validator.String{schemadoc.LengthAtLeast(1)},
}, projectIDField)

// dataConnection provides the configuration boundary to the data sources.
type dataConnection struct{ connection }

func (d *dataConnection) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	resp.Diagnostics.Append(d.configure(req.ProviderData)...)
}
