package selectel

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/selectel/mks-go/pkg/v1/kubeoptions"
	mksv2 "github.com/selectel/mks-go/v2/pkg"
	kubeoptionsv2 "github.com/selectel/mks-go/v2/pkg/kubeoptions"
)

var _ datasource.DataSourceWithConfigure = &mksKubeOptionsV2DataSource{}

func newMKSFeatureGatesV2DataSource() datasource.DataSource {
	return &mksKubeOptionsV2DataSource{
		typeName:    "_mks_feature_gates_v2",
		attribute:   "feature_gates",
		object:      objectFeatureGates,
		description: "Provides a list of feature gates available in Managed Kubernetes using mk-api-v2.",
		docs: mksKubeOptionsV2Docs{
			filter:            "Values to filter available feature gates.",
			filterKubeVersion: "Kubernetes version for which you get available feature gates.",
			options:           "List of available feature gates.",
			names:             "Names of the feature gates available for the specified version.",
		},
		list:    listMKSFeatureGatesV2,
		flatten: flattenFeatureGates,
	}
}

func listMKSFeatureGatesV2(ctx context.Context, client *mksv2.ServiceClient) ([]*kubeoptions.View, error) {
	featureGates, err := kubeoptionsv2.ListFeatureGates(ctx, client)
	if err != nil {
		return nil, err
	}

	views := make([]*kubeoptions.View, len(featureGates))
	for i, featureGate := range featureGates {
		views[i] = &kubeoptions.View{
			KubeVersion: valueOrZero(featureGate.KubeVersionMinor),
			Names:       valueOrZero(featureGate.Names),
		}
	}

	return views, nil
}
