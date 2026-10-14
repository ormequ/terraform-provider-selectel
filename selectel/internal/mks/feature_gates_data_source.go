package mks

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	mksv2 "github.com/selectel/mks-go/v2/pkg"
	"github.com/selectel/mks-go/v2/pkg/kubeoptions"
)

func NewFeatureGatesDataSource() datasource.DataSource {
	return &kubeOptionsDataSource{
		typeName:    "_mks_feature_gates_v2",
		attribute:   "feature_gates",
		object:      "feature gates",
		description: "Provides a list of feature gates available in Selectel Managed Kubernetes.",
		docs: kubeOptionsDocs{
			filter:            "Values to filter available feature gates.",
			filterKubeVersion: "Kubernetes version for which you get available feature gates.",
			options:           "List of available feature gates.",
			names:             "Names of the feature gates available for the specified version.",
		},
		list: listFeatureGates,
	}
}

func listFeatureGates(ctx context.Context, client *mksv2.ServiceClient) ([]kubeOptions, error) {
	all, err := kubeoptions.ListFeatureGates(ctx, client)
	if err != nil {
		return nil, err
	}
	options := make([]kubeOptions, len(all))
	for i, item := range all {
		options[i] = kubeOptions{kubeVersion: valueOrZero(item.KubeVersionMinor), names: valueOrZero(item.Names)}
	}

	return options, nil
}
