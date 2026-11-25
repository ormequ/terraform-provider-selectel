package mks

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
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

// dataConnection provides the identical configuration boundary to the data
// sources; each keeps its own schema, model and state write.
type dataConnection struct{ connection }

func (d *dataConnection) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	resp.Diagnostics.Append(d.configure(req.ProviderData)...)
}

// errGettingObjects repeats the error text of package selectel, as errGettingObject does.
func errGettingObjects(object string, err error) error {
	return fmt.Errorf("error getting %s: %s", object, err)
}
