package selectel

import (
	"net/http"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/resource"
	"github.com/selectel/mks-go/pkg/v1/kubeoptions"
	"github.com/selectel/mks-go/v2/pkg/mksclient"
)

func TestMKSAdmissionControllersV2DataSource(t *testing.T) {
	admissionControllers := []mksclient.AvailableAdmissionControllers{
		{KubeVersionMinor: new("1.30"), Names: &[]string{"NodeRestriction", "PodSecurity"}},
		{KubeVersionMinor: new("1.31"), Names: &[]string{"NodeRestriction", "PodSecurity", "AlwaysPullImages"}},
	}
	noFilterID, err := interfaceListChecksum(flattenAdmissionControllers([]*kubeoptions.View{
		{KubeVersion: "1.30", Names: []string{"NodeRestriction", "PodSecurity"}},
		{KubeVersion: "1.31", Names: []string{"NodeRestriction", "PodSecurity", "AlwaysPullImages"}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	filterID, err := stringListChecksum([]string{"NodeRestriction", "PodSecurity"})
	if err != nil {
		t.Fatal(err)
	}

	name := getDataSourceName(dataSourceAdmissionControllersV2)
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
				resource.TestCheckResourceAttr(name, "admission_controllers.#", "2"),
				resource.TestCheckTypeSetElemNestedAttrs(name, "admission_controllers.*", map[string]string{
					"kube_version": "1.31",
					"names.#":      "3",
				}),
			),
		},
		{
			name:      "filter by minor version",
			projectID: "attribute-project",
			filter:    "1.30",
			check: resource.ComposeTestCheckFunc(
				resource.TestCheckResourceAttr(name, "id", filterID),
				resource.TestCheckResourceAttr(name, "admission_controllers.#", "1"),
				resource.TestCheckResourceAttr(name, "admission_controllers.0.kube_version", "1.30"),
				resource.TestCheckTypeSetElemAttr(name, "admission_controllers.0.names.*", "PodSecurity"),
			),
		},
		{
			name:              "project from provider",
			providerProjectID: "provider-project",
			check:             resource.TestCheckResourceAttr(name, "project_id", "provider-project"),
		},
		{
			name:       "server error",
			projectID:  "attribute-project",
			failStatus: http.StatusInternalServerError,
			wantError:  regexp.MustCompile(`error getting admission-controllers: fake error\s+500`),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			useMKSV2TestConfig(t)
			fake := newMKSV2Fake(t)
			fake.seedAdmissionControllers(admissionControllers...)
			if tt.failStatus != 0 {
				fake.fail(mksV2RouteAdmissionControllers, tt.failStatus)
			}

			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{
					{
						Config:      testKubeOptionsV2Config(dataSourceAdmissionControllersV2, tt.providerProjectID, tt.projectID, tt.filter),
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
