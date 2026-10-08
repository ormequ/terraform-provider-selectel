package selectel

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/resource"
	"github.com/selectel/mks-go/v2/pkg/mksclient"
)

const (
	testMKSV2ClusterID = "6fd56a4c-a0b9-4e4b-b5f4-a3c0d3e0b1a1"
	testMKSV2Pool      = "ru-7"

	testMKSKubeconfigV2 = `apiVersion: v1
clusters:
- cluster:
    certificate-authority-data: Y2EtZGF0YQ==
    server: https://192.0.2.10:6443
  name: tf-v2
contexts:
- context:
    cluster: tf-v2
    user: admin
  name: admin@tf-v2
current-context: admin@tf-v2
kind: Config
users:
- name: admin
  user:
    client-certificate-data: Y2xpZW50LWNlcnQ=
    client-key-data: Y2xpZW50LWtleQ==
`
)

func TestMKSKubeconfigV2DataSourceBasic(t *testing.T) {
	t.Parallel()
	fake := newMKSV2Fake(t)
	fake.seedCluster(mksclient.ClusterDetailed{Id: testMKSV2ClusterID, Name: "tf-v2"}, testMKSKubeconfigV2)

	dataSourceName := "data.selectel_mks_kubeconfig_v2.kubeconfig_tf_test_1"
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: fake.providerFactories(),
		Steps: []resource.TestStep{
			{
				Config: testMKSKubeconfigV2Basic("", "attribute-project"),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(dataSourceName, "id", testMKSV2ClusterID),
					resource.TestCheckResourceAttr(dataSourceName, "project_id", "attribute-project"),
					resource.TestCheckResourceAttr(dataSourceName, "pool", testMKSV2Pool),
					resource.TestCheckResourceAttr(dataSourceName, "raw_config", testMKSKubeconfigV2),
					resource.TestCheckResourceAttr(dataSourceName, "server", "https://192.0.2.10:6443"),
					resource.TestCheckResourceAttr(dataSourceName, "cluster_ca_cert", "Y2EtZGF0YQ=="),
					resource.TestCheckResourceAttr(dataSourceName, "client_cert", "Y2xpZW50LWNlcnQ="),
					resource.TestCheckResourceAttr(dataSourceName, "client_key", "Y2xpZW50LWtleQ=="),
				),
			},
		},
	})

	fake.checkClients(t, "attribute-project")
}

func TestMKSKubeconfigV2DataSourceProviderProjectID(t *testing.T) {
	t.Parallel()

	// The dashed provider project is the form mk-api-v2 returns: the data
	// source keeps it, the client takes it without dashes.
	for _, providerProject := range []string{"provider-project", testMKSV2APIProject} {
		t.Run(providerProject, func(t *testing.T) {
			t.Parallel()
			fake := newMKSV2Fake(t)
			fake.seedCluster(mksclient.ClusterDetailed{Id: testMKSV2ClusterID}, testMKSKubeconfigV2)

			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: fake.providerFactories(),
				Steps: []resource.TestStep{
					{
						Config: testMKSKubeconfigV2Basic(providerProject, ""),
						Check: resource.TestCheckResourceAttr(
							"data.selectel_mks_kubeconfig_v2.kubeconfig_tf_test_1", "project_id", providerProject),
					},
				},
			})

			fake.checkClients(t, mksV2KeystoneProjectID(providerProject))
		})
	}
}

func TestMKSKubeconfigV2DataSourceAPIErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setup     func(f *mksV2Fake)
		wantError *regexp.Regexp
	}{
		{
			name:      "cluster not found",
			setup:     func(*mksV2Fake) {},
			wantError: regexp.MustCompile(`error getting cluster '` + testMKSV2ClusterID + `': Cluster\s+not\s+found`),
		},
		{
			name: "kubeconfig server error",
			setup: func(f *mksV2Fake) {
				f.seedCluster(mksclient.ClusterDetailed{Id: testMKSV2ClusterID}, testMKSKubeconfigV2)
				f.fail(mksV2RouteKubeconfig, http.StatusInternalServerError)
			},
			wantError: regexp.MustCompile(`error getting kubeconfig '` + testMKSV2ClusterID + `': fake error\s+500`),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fake := newMKSV2Fake(t)
			tt.setup(fake)

			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: fake.providerFactories(),
				Steps: []resource.TestStep{
					{
						Config:      testMKSKubeconfigV2Basic("", "attribute-project"),
						ExpectError: tt.wantError,
					},
				},
			})
		})
	}
}

// testMKSV2ProviderConfig is the provider with project_id and no region.
func testMKSV2ProviderConfig(providerProjectID string) string {
	return testMKSClusterV2ProviderConfig(providerProjectID, "")
}

// testMKSV2ProjectIDArgument renders the project_id argument of a data source.
func testMKSV2ProjectIDArgument(projectID string) string {
	if projectID == "" {
		return ""
	}

	return fmt.Sprintf("project_id = %q", projectID)
}

func testMKSKubeconfigV2Basic(providerProjectID, projectID string) string {
	return fmt.Sprintf(`
%s

data "selectel_mks_kubeconfig_v2" "kubeconfig_tf_test_1" {
  cluster_id = %q
  pool       = %q
  %s
}
`, testMKSV2ProviderConfig(providerProjectID), testMKSV2ClusterID, testMKSV2Pool, testMKSV2ProjectIDArgument(projectID))
}

func TestMKSKubeconfigV2DataSourceSensitive(t *testing.T) {
	setTestProviderEnv(t)

	server, err := testAccProtoV6ProviderFactories["selectel"]()
	if err != nil {
		t.Fatal(err)
	}
	resp, err := server.GetProviderSchema(context.Background(), &tfprotov6.GetProviderSchemaRequest{})
	if err != nil {
		t.Fatal(err)
	}
	schema, ok := resp.DataSourceSchemas["selectel_mks_kubeconfig_v2"]
	if !ok {
		t.Fatal("no selectel_mks_kubeconfig_v2 schema")
	}

	sensitive := map[string]bool{}
	for _, attribute := range schema.Block.Attributes {
		sensitive[attribute.Name] = attribute.Sensitive
	}
	for _, name := range []string{"raw_config", "server", "cluster_ca_cert", "client_cert", "client_key"} {
		if !sensitive[name] {
			t.Errorf("attribute %s is not Sensitive", name)
		}
	}
}
