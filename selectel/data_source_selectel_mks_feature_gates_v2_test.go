package selectel

import (
	"fmt"
	"net/http"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/resource"
	"github.com/selectel/mks-go/pkg/v1/kubeoptions"
	"github.com/selectel/mks-go/v2/pkg/mksclient"
)

const (
	dataSourceFeatureGatesV2         = "selectel_mks_feature_gates_v2"
	dataSourceAdmissionControllersV2 = "selectel_mks_admission_controllers_v2"
)

func TestMKSFeatureGatesV2DataSource(t *testing.T) {
	t.Parallel()

	featureGates := []mksclient.AvailableFeatureGates{
		{KubeVersionMinor: new("1.30"), Names: &[]string{"GracefulNodeShutdown", "TopologyManager"}},
		{KubeVersionMinor: new("1.31"), Names: &[]string{"TopologyManager", "SidecarContainers", "TopologyManager"}},
	}
	noFilterID, err := interfaceListChecksum(flattenFeatureGates([]*kubeoptions.View{
		{KubeVersion: "1.30", Names: []string{"GracefulNodeShutdown", "TopologyManager"}},
		{KubeVersion: "1.31", Names: []string{"TopologyManager", "SidecarContainers", "TopologyManager"}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	filterID, err := stringListChecksum([]string{"TopologyManager", "SidecarContainers", "TopologyManager"})
	if err != nil {
		t.Fatal(err)
	}

	name := getDataSourceName(dataSourceFeatureGatesV2)
	tests := []struct {
		name              string
		providerProjectID string
		projectID         string
		filter            string
		failStatus        int
		check             resource.TestCheckFunc
		wantError         *regexp.Regexp
	}{
		{
			name:      "no filter",
			projectID: "attribute-project",
			check: resource.ComposeTestCheckFunc(
				resource.TestCheckResourceAttr(name, "id", noFilterID),
				resource.TestCheckResourceAttr(name, "project_id", "attribute-project"),
				resource.TestCheckResourceAttr(name, "pool", testMKSV2Pool),
				resource.TestCheckResourceAttr(name, "feature_gates.#", "2"),
				resource.TestCheckTypeSetElemNestedAttrs(name, "feature_gates.*", map[string]string{
					"kube_version": "1.30",
					"names.#":      "2",
				}),
				resource.TestCheckTypeSetElemNestedAttrs(name, "feature_gates.*", map[string]string{
					"kube_version": "1.31",
					"names.#":      "2",
				}),
			),
		},
		{
			name:      "filter by patch version",
			projectID: "attribute-project",
			filter:    "1.31.2",
			check: resource.ComposeTestCheckFunc(
				resource.TestCheckResourceAttr(name, "id", filterID),
				resource.TestCheckResourceAttr(name, "filter.#", "1"),
				resource.TestCheckResourceAttr(name, "feature_gates.#", "1"),
				resource.TestCheckResourceAttr(name, "feature_gates.0.kube_version", "1.31"),
				resource.TestCheckTypeSetElemAttr(name, "feature_gates.0.names.*", "SidecarContainers"),
				resource.TestCheckTypeSetElemAttr(name, "feature_gates.0.names.*", "TopologyManager"),
			),
		},
		{
			name:              "project from provider",
			providerProjectID: "provider-project",
			check:             resource.TestCheckResourceAttr(name, "project_id", mksV2KeystoneProjectID("provider-project")),
		},
		{
			name:      "unknown version",
			projectID: "attribute-project",
			filter:    "1.20.0",
			wantError: regexp.MustCompile(`available kubernetes options for kubernetes version "1.20"\s+is not found`),
		},
		{
			name:       "server error",
			projectID:  "attribute-project",
			failStatus: http.StatusInternalServerError,
			wantError:  regexp.MustCompile(`error getting feature-gates: fake error\s+500`),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fake := newMKSV2Fake(t)
			fake.seedFeatureGates(featureGates...)
			if tt.failStatus != 0 {
				fake.fail(mksV2RouteFeatureGates, tt.failStatus)
			}

			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: fake.providerFactories(),
				Steps: []resource.TestStep{
					{
						Config:      testKubeOptionsV2Config(dataSourceFeatureGatesV2, tt.providerProjectID, tt.projectID, tt.filter),
						Check:       tt.check,
						ExpectError: tt.wantError,
					},
				},
			})

			if tt.wantError == nil {
				wantProjectID := tt.projectID
				if wantProjectID == "" {
					wantProjectID = tt.providerProjectID
				}
				fake.checkClients(t, wantProjectID)
			}
		})
	}
}

// testKubeOptionsV2Config renders a feature gates or admission controllers
// data source; an empty kubeVersion means no filter.
func testKubeOptionsV2Config(dataSource, providerProjectID, projectID, kubeVersion string) string {
	filter := ""
	if kubeVersion != "" {
		filter = fmt.Sprintf("filter = [{ kube_version = %q }]", kubeVersion)
	}

	return fmt.Sprintf(`
%s

data "%s" "dt" {
  pool = %q
  %s
  %s
}
`, testMKSV2ProviderConfig(providerProjectID), dataSource, testMKSV2Pool, testMKSV2ProjectIDArgument(projectID), filter)
}
