package mks

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	mksv2 "github.com/selectel/mks-go/v2/pkg"
	"github.com/selectel/mks-go/v2/pkg/kubeoptions"
)

func NewAdmissionControllersDataSource() datasource.DataSource {
	return &kubeOptionsDataSource{
		typeName:    "_mks_admission_controllers_v2",
		attribute:   "admission_controllers",
		object:      "admission controllers",
		description: "Provides a list of admission controllers available in Selectel Managed Kubernetes.",
		docs: kubeOptionsDocs{
			filter:            "Values to filter available admission controllers.",
			filterKubeVersion: "Kubernetes version for which you get available admission controllers.",
			options:           "List of available admission controllers.",
			names:             "Names of the admission controllers available for the specified version.",
		},
		list: listAdmissionControllers,
	}
}

func listAdmissionControllers(ctx context.Context, client *mksv2.ServiceClient) ([]kubeOptions, error) {
	all, err := kubeoptions.ListAdmissionControllers(ctx, client)
	if err != nil {
		return nil, err
	}
	options := make([]kubeOptions, len(all))
	for i, item := range all {
		options[i] = kubeOptions{kubeVersion: valueOrZero(item.KubeVersionMinor), names: valueOrZero(item.Names)}
	}

	return options, nil
}
