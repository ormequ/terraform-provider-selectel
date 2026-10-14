package mks

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/terraform-providers/terraform-provider-selectel/selectel/internal/schemadoc"
)

// dataConnection provides the identical configuration boundary to the data
// sources; each keeps its own schema, model and state write.
type dataConnection struct{ connection }

func (d *dataConnection) Configure(
	_ context.Context,
	req datasource.ConfigureRequest,
	resp *datasource.ConfigureResponse,
) {
	resp.Diagnostics.Append(d.configure(req.ProviderData)...)
}

// clusterDocs describes the shared attributes. clusterLocation is the Managed
// Kubernetes section of the product availability page.
var clusterDocs = schemadoc.DataSource{Name: "cluster", Location: clusterLocation}

const clusterLocation = "managed-kubernetes"
