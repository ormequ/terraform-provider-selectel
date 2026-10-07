package selectel

import (
	"fmt"
	"net/http"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/resource"
	"github.com/selectel/mks-go/v2/pkg/mksclient"
)

func TestMKSKubeVersionsV2DataSourceBasic(t *testing.T) {
	t.Parallel()
	fake := newMKSV2Fake(t)
	fake.seedKubeVersions(
		mksclient.KubeVersionInfo{Version: new("1.29.5"), IsDefault: new(true)},
		mksclient.KubeVersionInfo{Version: new("1.31.2"), IsDefault: new(false)},
		mksclient.KubeVersionInfo{Version: new("1.30.1")},
	)
	wantID, err := stringListChecksum([]string{"1.29.5", "1.31.2", "1.30.1"})
	if err != nil {
		t.Fatal(err)
	}

	dataSourceName := "data.selectel_mks_kube_versions_v2.kube_versions_tf_test_1"
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: fake.providerFactories(),
		Steps: []resource.TestStep{
			{
				Config: testMKSKubeVersionsV2Basic("", "attribute-project"),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(dataSourceName, "id", wantID),
					resource.TestCheckResourceAttr(dataSourceName, "project_id", "attribute-project"),
					resource.TestCheckResourceAttr(dataSourceName, "pool", testMKSV2Pool),
					resource.TestCheckResourceAttr(dataSourceName, "latest_version", "1.31.2"),
					resource.TestCheckResourceAttr(dataSourceName, "default_version", "1.29.5"),
					resource.TestCheckResourceAttr(dataSourceName, "versions.#", "3"),
					resource.TestCheckResourceAttr(dataSourceName, "versions.0", "1.29.5"),
					resource.TestCheckResourceAttr(dataSourceName, "versions.1", "1.31.2"),
					resource.TestCheckResourceAttr(dataSourceName, "versions.2", "1.30.1"),
				),
			},
		},
	})

	fake.checkClients(t, "attribute-project")
}

func TestMKSKubeVersionsV2DataSourceProjectID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		providerProjectID string
		projectID         string
		wantProjectID     string
		wantError         *regexp.Regexp
	}{
		{
			name:              "from provider",
			providerProjectID: "provider-project",
			wantProjectID:     "provider-project",
		},
		{
			name:              "attribute wins over provider",
			providerProjectID: "provider-project",
			projectID:         "attribute-project",
			wantProjectID:     "attribute-project",
		},
		{
			name:      "missing",
			wantError: regexp.MustCompile(`Missing project ID`),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fake := newMKSV2Fake(t)
			fake.seedKubeVersions(mksclient.KubeVersionInfo{Version: new("1.31.2"), IsDefault: new(true)})

			step := resource.TestStep{
				Config:      testMKSKubeVersionsV2Basic(tt.providerProjectID, tt.projectID),
				ExpectError: tt.wantError,
			}
			if tt.wantError == nil {
				step.Check = resource.TestCheckResourceAttr(
					"data.selectel_mks_kube_versions_v2.kube_versions_tf_test_1", "project_id", tt.wantProjectID)
			}
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: fake.providerFactories(),
				Steps:                    []resource.TestStep{step},
			})

			if tt.wantError == nil {
				fake.checkClients(t, tt.wantProjectID)
			}
		})
	}
}

func TestMKSKubeVersionsV2DataSourceAPIError(t *testing.T) {
	t.Parallel()
	fake := newMKSV2Fake(t)
	fake.fail(mksV2RouteKubeVersions, http.StatusInternalServerError)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: fake.providerFactories(),
		Steps: []resource.TestStep{
			{
				Config:      testMKSKubeVersionsV2Basic("", "attribute-project"),
				ExpectError: regexp.MustCompile(`error getting kube-versions: fake error\s+500`),
			},
		},
	})
}

func testMKSKubeVersionsV2Basic(providerProjectID, projectID string) string {
	return fmt.Sprintf(`
%s

data "selectel_mks_kube_versions_v2" "kube_versions_tf_test_1" {
  pool = %q
  %s
}
`, testMKSV2ProviderConfig(providerProjectID), testMKSV2Pool, testMKSV2ProjectIDArgument(projectID))
}
