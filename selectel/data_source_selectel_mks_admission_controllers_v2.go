package selectel

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/selectel/mks-go/pkg/v1/kubeoptions"
	mksv2 "github.com/selectel/mks-go/v2/pkg"
	kubeoptionsv2 "github.com/selectel/mks-go/v2/pkg/kubeoptions"
)

func newMKSAdmissionControllersV2DataSource() datasource.DataSource {
	return &mksKubeOptionsV2DataSource{
		typeName:    "_mks_admission_controllers_v2",
		attribute:   "admission_controllers",
		object:      objectAdmissionControllers,
		description: "Provides a list of admission controllers available in Managed Kubernetes using mk-api-v2.",
		docs: mksKubeOptionsV2Docs{
			filter:            "Values to filter available admission controllers.",
			filterKubeVersion: "Kubernetes version for which you get available admission controllers.",
			options:           "List of available admission controllers.",
			names:             "Names of the admission controllers available for the specified version.",
		},
		list:    listMKSAdmissionControllersV2,
		flatten: flattenAdmissionControllers,
	}
}

func listMKSAdmissionControllersV2(ctx context.Context, client *mksv2.ServiceClient) ([]*kubeoptions.View, error) {
	admissionControllers, err := kubeoptionsv2.ListAdmissionControllers(ctx, client)
	if err != nil {
		return nil, err
	}

	views := make([]*kubeoptions.View, len(admissionControllers))
	for i, admissionController := range admissionControllers {
		views[i] = &kubeoptions.View{
			KubeVersion: valueOrZero(admissionController.KubeVersionMinor),
			Names:       valueOrZero(admissionController.Names),
		}
	}

	return views, nil
}
