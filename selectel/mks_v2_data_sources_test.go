package selectel

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/resource"
	"github.com/selectel/mks-go/pkg/v1/kubeversion"
	"github.com/selectel/mks-go/v2/pkg/mksclient"
)

// TestMKSKubeVersionsV2DataSource serves selectel_mks_kube_versions_v2 through
// the provider: Configure hands it the Config, which authenticates against
// Keystone, picks the pool endpoint from the catalog and sets the User-Agent.
// The values are compared with what the _v1 helpers return for the same list.
func TestMKSKubeVersionsV2DataSource(t *testing.T) {
	t.Parallel()

	versions := []*kubeversion.View{
		{Version: "1.29.5", IsDefault: true},
		{Version: "1.31.2"},
		{Version: "1.30.1"},
	}
	wantID, err := stringListChecksum([]string{"1.29.5", "1.31.2", "1.30.1"})
	if err != nil {
		t.Fatal(err)
	}
	wantLatest, err := parseMKSKubeVersionsV1Latest(versions)
	if err != nil {
		t.Fatal(err)
	}
	wantDefault := parseMKSKubeVersionsV1Default(versions)

	name := "data.selectel_mks_kube_versions_v2.kube_versions_tf_test_1"
	tests := []struct {
		name              string
		providerProjectID string
		projectID         string
		// wantProjectID is the project_id in the state and the Keystone scope of the token.
		wantProjectID string
		wantError     *regexp.Regexp
	}{
		{name: "attribute", projectID: testMKSV2KeystoneProject, wantProjectID: testMKSV2KeystoneProject},
		{name: "attribute wins over provider", providerProjectID: "provider-project", projectID: testMKSV2KeystoneProject, wantProjectID: testMKSV2KeystoneProject},
		{name: "from provider", providerProjectID: "provider-project", wantProjectID: "providerproject"},
		{name: "from provider in the API form", providerProjectID: testMKSV2APIProject, wantProjectID: testMKSV2KeystoneProject},
		{name: "missing", wantError: regexp.MustCompile(`Missing project ID`)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fake := newMKSV2Fake(t)
			fake.seedKubeVersions(
				mksclient.KubeVersionInfo{Version: new("1.29.5"), IsDefault: new(true)},
				mksclient.KubeVersionInfo{Version: new("1.31.2"), IsDefault: new(false)},
				mksclient.KubeVersionInfo{Version: new("1.30.1")},
			)

			step := resource.TestStep{
				Config:      testMKSKubeVersionsV2Config(tt.providerProjectID, tt.projectID),
				ExpectError: tt.wantError,
			}
			if tt.wantError == nil {
				step.Check = resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(name, "id", wantID),
					resource.TestCheckResourceAttr(name, "project_id", tt.wantProjectID),
					resource.TestCheckResourceAttr(name, "pool", testMKSV2Pool),
					resource.TestCheckResourceAttr(name, "latest_version", wantLatest),
					resource.TestCheckResourceAttr(name, "default_version", wantDefault),
					resource.TestCheckResourceAttr(name, "versions.#", "3"),
					resource.TestCheckResourceAttr(name, "versions.0", "1.29.5"),
					resource.TestCheckResourceAttr(name, "versions.1", "1.31.2"),
					resource.TestCheckResourceAttr(name, "versions.2", "1.30.1"),
				)
			}
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: fake.providerFactories(),
				Steps:                    []resource.TestStep{step},
			})

			if tt.wantError == nil {
				fake.checkClients(t, tt.wantProjectID, testMKSV2Pool)
			}
		})
	}
}

// testMKSV2ProviderConfig is the provider with project_id and no region. It
// sets every other provider argument, so a test reads nothing from the
// environment and needs no t.Setenv, which t.Parallel forbids.
func testMKSV2ProviderConfig(providerProjectID string) string {
	return fmt.Sprintf(`
provider "selectel" {
  project_id  = %q
  region      = ""
  auth_url    = "test"
  auth_region = "test"
  domain_name = "test"
  username    = "test"
  password    = "test"
}
`, providerProjectID)
}

func testMKSKubeVersionsV2Config(providerProjectID, projectID string) string {
	argument := ""
	if projectID != "" {
		argument = fmt.Sprintf("project_id = %q", projectID)
	}

	return fmt.Sprintf(`
%s

data "selectel_mks_kube_versions_v2" "kube_versions_tf_test_1" {
  pool = %q
  %s
}
`, testMKSV2ProviderConfig(providerProjectID), testMKSV2Pool, argument)
}
