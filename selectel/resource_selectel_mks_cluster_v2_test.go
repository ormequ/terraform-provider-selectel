package selectel

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/resource"
	"github.com/hashicorp/terraform-plugin-sdk/v2/terraform"
	"github.com/selectel/mks-go/v2/pkg/mksclient"
)

const testMKSClusterV2Name = "selectel_mks_cluster_v2.cluster_tf_test_1"

func TestMKSClusterV2ResourceBasic(t *testing.T) {
	t.Parallel()
	fake := newMKSV2Fake(t)
	// A cluster operation must not wait for nodegroup tasks; if it did, the
	// short create timeout would fail the apply.
	fake.seedTask(testMKSV2ClusterID, "ng-1", "RESIZE_NODEGROUP", true)

	config := testMKSClusterV2Config("", `
  project_id   = "attribute-project"
  kube_version = "1.30.3"
  workers_type = "CLOUD"
  cni_type     = "CILIUM"
  cni_cilium_settings = {
    hubble_relay = false
  }
  kubernetes_options = {
    feature_gates = ["TopologyAwareHints"]
    oidc = {
      enabled       = true
      provider_name = "keycloak"
      issuer_url    = "https://issuer.example.com"
      client_id     = "kubernetes"
    }
  }
  timeouts {
    create = "5s"
  }
`)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: fake.providerFactories(),
		CheckDestroy:             testMKSClusterV2Destroyed(fake),
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(testMKSClusterV2Name, "id", testMKSV2ClusterID),
					resource.TestCheckResourceAttr(testMKSClusterV2Name, "project_id", "attribute-project"),
					resource.TestCheckResourceAttr(testMKSClusterV2Name, "pool", testMKSV2Pool),
					resource.TestCheckResourceAttr(testMKSClusterV2Name, "kube_version", "1.30.3"),
					resource.TestCheckResourceAttr(testMKSClusterV2Name, "cluster_type", "HIGH_AVAILABILITY"),
					resource.TestCheckResourceAttr(testMKSClusterV2Name, "workers_type", "CLOUD"),
					resource.TestCheckResourceAttr(testMKSClusterV2Name, "network_id", "fake-network"),
					resource.TestCheckResourceAttr(testMKSClusterV2Name, "subnet_id", "fake-subnet"),
					resource.TestCheckNoResourceAttr(testMKSClusterV2Name, "cloud_subnet_cidr"),
					resource.TestCheckResourceAttr(testMKSClusterV2Name, "private_kube_api", "false"),
					resource.TestCheckResourceAttr(testMKSClusterV2Name, "enable_autorepair", "true"),
					resource.TestCheckResourceAttr(testMKSClusterV2Name, "enable_patch_version_auto_upgrade", "true"),
					resource.TestCheckResourceAttr(testMKSClusterV2Name, "maintenance_window_start", "03:00:00"),
					resource.TestCheckResourceAttr(testMKSClusterV2Name, "maintenance_window_end", "06:00:00"),
					resource.TestCheckResourceAttr(testMKSClusterV2Name, "status", "ACTIVE"),
					resource.TestCheckResourceAttr(testMKSClusterV2Name, "kube_api_ip", "192.0.2.10"),
					resource.TestCheckResourceAttr(testMKSClusterV2Name, "cni_type", "CILIUM"),
					resource.TestCheckResourceAttr(testMKSClusterV2Name, "cni_cilium_settings.envoy_daemonset", "true"),
					resource.TestCheckResourceAttr(testMKSClusterV2Name, "cni_cilium_settings.hubble_relay", "false"),
					resource.TestCheckResourceAttr(testMKSClusterV2Name, "kubernetes_options.feature_gates.#", "1"),
					resource.TestCheckResourceAttr(testMKSClusterV2Name, "kubernetes_options.feature_gates.0", "TopologyAwareHints"),
					resource.TestCheckResourceAttr(testMKSClusterV2Name, "kubernetes_options.admission_controllers.#", "0"),
					resource.TestCheckResourceAttr(testMKSClusterV2Name, "kubernetes_options.audit_logs.enabled", "false"),
					resource.TestCheckResourceAttr(testMKSClusterV2Name, "kubernetes_options.oidc.enabled", "true"),
					resource.TestCheckResourceAttr(testMKSClusterV2Name, "kubernetes_options.oidc.provider_name", "keycloak"),
					resource.TestCheckResourceAttr(testMKSClusterV2Name, "kubernetes_options.oidc.username_claim", "sub"),
					resource.TestCheckResourceAttr(testMKSClusterV2Name, "kubernetes_options.oidc.groups_claim", "groups"),
					resource.TestCheckNoResourceAttr(testMKSClusterV2Name, "basic"),
					// The omitted claims are planned and sent as the API defaults.
					testMKSClusterV2OIDCSent(t, fake, mksV2RouteCreateCluster, map[string]any{
						"enabled": true, "provider_name": "keycloak", "issuer_url": "https://issuer.example.com",
						"client_id": "kubernetes", "username_claim": "sub", "groups_claim": "groups",
					}),
					func(_ *terraform.State) error {
						body := fake.lastBody(t, mksV2RouteCreateCluster)
						return testMKSClusterV2BodyFields(body, map[string]any{
							"network_type": "STANDARD",
							"basic":        false,
							"cni_type":     "CILIUM",
						}, "enable_patch_version_auto_upgrade", "cluster_type")
					},
				),
			},
			{
				Config:   config,
				PlanOnly: true,
			},
		},
	})

	fake.checkClients(t, "attribute-project")
}

// TestMKSClusterV2ResourceProjectIDForms covers mk-api-v2 returning project_id
// dashed, while Keystone takes it without dashes: the state keeps the
// configured form, and every client is built for the un-dashed one.
func TestMKSClusterV2ResourceProjectIDForms(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// createProject is the project_id configured at create, configProject
		// the one configured after it, providerProject the provider one.
		createProject, configProject, providerProject string
		wantProject                                   string
	}{
		{
			name:          "un-dashed configured",
			createProject: testMKSV2KeystoneProject, configProject: testMKSV2KeystoneProject,
			wantProject: testMKSV2KeystoneProject,
		},
		{
			name:          "dashed configured",
			createProject: testMKSV2APIProject, configProject: testMKSV2APIProject,
			wantProject: testMKSV2APIProject,
		},
		{
			name:          "dashed state, un-dashed configured",
			createProject: testMKSV2APIProject, configProject: testMKSV2KeystoneProject,
			wantProject: testMKSV2APIProject,
		},
		{
			name:          "un-dashed state, dashed configured",
			createProject: testMKSV2KeystoneProject, configProject: testMKSV2APIProject,
			wantProject: testMKSV2KeystoneProject,
		},
		{
			name:            "un-dashed provider project",
			providerProject: testMKSV2KeystoneProject,
			wantProject:     testMKSV2KeystoneProject,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fake := newMKSV2Fake(t)
			config := func(projectID string) string {
				return testMKSClusterV2Config(testMKSClusterV2ProviderConfig(tt.providerProject, ""), `
  `+testMKSV2ProjectIDArgument(projectID)+`
  kube_version = "1.30.3"
  workers_type = "CLOUD"
`)
			}

			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: fake.providerFactories(),
				CheckDestroy:             testMKSClusterV2Destroyed(fake),
				Steps: []resource.TestStep{
					{
						Config: config(tt.createProject),
						Check: resource.ComposeTestCheckFunc(
							resource.TestCheckResourceAttr(testMKSClusterV2Name, "project_id",
								cmp.Or(tt.createProject, tt.providerProject)),
							func(_ *terraform.State) error {
								got := fake.clusterProject(testMKSV2ClusterID)
								if got != testMKSV2APIProject {
									return fmt.Errorf("the fake stored project %q, want the dashed %q", got, testMKSV2APIProject)
								}

								return nil
							},
						),
					},
					{
						// The refresh builds the client from the state.
						Config:   config(tt.configProject),
						PlanOnly: true,
					},
					{
						Config: config(tt.configProject),
						Check: resource.ComposeTestCheckFunc(
							resource.TestCheckResourceAttr(testMKSClusterV2Name, "project_id", tt.wantProject),
							testMKSClusterV2Calls(fake, map[string]int{mksV2RouteCreateCluster: 1, mksV2RouteDeleteCluster: 0}),
						),
					},
				},
			})

			fake.checkClients(t, testMKSV2KeystoneProject)
		})
	}
}

func TestMKSClusterV2ResourceWorkersType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		workersType string
		networkType string
	}{
		{workersType: "CLOUD", networkType: "STANDARD"},
		{workersType: "DEDICATED", networkType: "L3VPN"},
	}

	for _, tt := range tests {
		t.Run(tt.workersType, func(t *testing.T) {
			t.Parallel()
			fake := newMKSV2Fake(t)

			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: fake.providerFactories(),
				CheckDestroy:             testMKSClusterV2Destroyed(fake),
				Steps: []resource.TestStep{
					{
						Config: testMKSClusterV2Config("", fmt.Sprintf(`
  project_id   = "attribute-project"
  kube_version = "1.30.3"
  workers_type = %q
`, tt.workersType)),
						Check: resource.ComposeTestCheckFunc(
							resource.TestCheckResourceAttr(testMKSClusterV2Name, "workers_type", tt.workersType),
							func(_ *terraform.State) error {
								return testMKSClusterV2BodyFields(fake.lastBody(t, mksV2RouteCreateCluster),
									map[string]any{"network_type": tt.networkType})
							},
						),
					},
				},
			})
		})
	}
}

// TestMKSClusterV2ResourceAutorepairDefault covers an omitted or false
// enable_autorepair: mk-api-v2 turns auto repair off for a DEDICATED (L3VPN)
// cluster at create, so the plan must match what it stores.
func TestMKSClusterV2ResourceAutorepairDefault(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		workersType string
		autorepair  string
		want        bool
	}{
		{name: "dedicated unset", workersType: "DEDICATED", want: false},
		{name: "cloud unset", workersType: "CLOUD", want: true},
		{name: "cloud false", workersType: "CLOUD", autorepair: "enable_autorepair = false", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fake := newMKSV2Fake(t)

			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: fake.providerFactories(),
				CheckDestroy:             testMKSClusterV2Destroyed(fake),
				Steps: []resource.TestStep{
					{
						Config: testMKSClusterV2Config("", fmt.Sprintf(`
  project_id   = "attribute-project"
  kube_version = "1.30.3"
  workers_type = %q
  %s
`, tt.workersType, tt.autorepair)),
						Check: resource.ComposeTestCheckFunc(
							resource.TestCheckResourceAttr(testMKSClusterV2Name, "enable_autorepair", fmt.Sprint(tt.want)),
							func(_ *terraform.State) error {
								return testMKSClusterV2BodyFields(fake.lastBody(t, mksV2RouteCreateCluster),
									map[string]any{"enable_autorepair": tt.want})
							},
						),
					},
				},
			})
		})
	}
}

// TestMKSClusterV2ResourceDedicatedAutorepair checks that auto repair for
// dedicated workers fails the plan on create and on update, and that a switch
// to CLOUD plans the CLOUD default for the new cluster.
func TestMKSClusterV2ResourceDedicatedAutorepair(t *testing.T) {
	t.Parallel()
	fake := newMKSV2Fake(t)

	config := func(workersType, autorepair string) string {
		return testMKSClusterV2Config("", fmt.Sprintf(`
  project_id   = "attribute-project"
  kube_version = "1.30.3"
  workers_type = %q
  %s
`, workersType, autorepair))
	}
	wantError := testMKSClusterV2Error(`Auto repair is not available for dedicated workers`)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: fake.providerFactories(),
		CheckDestroy:             testMKSClusterV2Destroyed(fake),
		Steps: []resource.TestStep{
			{
				Config:      config("DEDICATED", "enable_autorepair = true"),
				PlanOnly:    true,
				ExpectError: wantError,
			},
			{
				Config: config("DEDICATED", ""),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(testMKSClusterV2Name, "enable_autorepair", "false"),
					testMKSClusterV2Calls(fake, map[string]int{mksV2RouteCreateCluster: 1}),
				),
			},
			{
				Config:      config("DEDICATED", "enable_autorepair = true"),
				PlanOnly:    true,
				ExpectError: wantError,
			},
			{
				// The replacement is planned as a create, so the state value
				// of the old cluster is not kept.
				Config: config("CLOUD", ""),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(testMKSClusterV2Name, "enable_autorepair", "true"),
					func(_ *terraform.State) error {
						return testMKSClusterV2BodyFields(fake.lastBody(t, mksV2RouteCreateCluster),
							map[string]any{"network_type": "STANDARD", "enable_autorepair": true})
					},
				),
			},
		},
	})

	calls := fake.callCount(mksV2RoutePatchCluster)
	if calls != 0 {
		t.Errorf("the provider sent %d patch requests, want none", calls)
	}
}

func TestMKSClusterV2ResourceCreateTaskError(t *testing.T) {
	t.Parallel()
	fake := newMKSV2Fake(t)
	fake.failTasks("CREATE_CLUSTER", true)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: fake.providerFactories(),
		CheckDestroy:             testMKSClusterV2Destroyed(fake),
		Steps: []resource.TestStep{
			{
				Config: testMKSClusterV2Config("", `
  project_id   = "attribute-project"
  kube_version = "1.30.3"
  workers_type = "CLOUD"
`),
				ExpectError: testMKSClusterV2Error(`task CREATE_CLUSTER task-1 of cluster ` + testMKSV2ClusterID +
					` ended in ERROR: fake failure \(code 42\): fake details of CREATE_CLUSTER`),
			},
		},
	})
}

func TestMKSClusterV2ResourceCreateUndeclaredStatus(t *testing.T) {
	t.Parallel()
	// The mk-api-v2 swagger declares only 500 for the create, so these
	// statuses reach the provider as the raw body.
	tests := []struct {
		name        string
		status      int
		contentType string
		body        string
		wantErr     string
	}{
		{
			name:        "400 with the mk-api-v2 error",
			status:      http.StatusBadRequest,
			contentType: "application/json",
			body:        `{"error":{"message":"invalid kube_version: 1.30.3"}}`,
			wantErr:     `error creating cluster: 400 Bad Request: invalid kube_version: 1.30.3`,
		},
		{
			name:        "403 with a JSON error",
			status:      http.StatusForbidden,
			contentType: "application/json",
			body:        `{"error":{"message":"access denied for the project"}}`,
			wantErr:     `error creating cluster: 403 Forbidden: access denied for the project`,
		},
		{
			name:        "422 with plain text",
			status:      http.StatusUnprocessableEntity,
			contentType: "text/plain",
			body:        "kube_version 1.30.3 is not supported\n",
			wantErr:     `error creating cluster: 422 Unprocessable Entity: kube_version 1.30.3 is not supported`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fake := newMKSV2Fake(t)
			fake.failWithBody(mksV2RouteCreateCluster, tt.status, tt.contentType, tt.body)

			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: fake.providerFactories(),
				CheckDestroy:             testMKSClusterV2Destroyed(fake),
				Steps: []resource.TestStep{
					{
						Config: testMKSClusterV2Config("", `
  project_id   = "attribute-project"
  kube_version = "1.30.3"
  workers_type = "CLOUD"
`),
						ExpectError: testMKSClusterV2Error(regexp.QuoteMeta(tt.wantErr)),
					},
				},
			})
		})
	}
}

func TestMKSClusterV2ResourceKubernetesOptionsSentWhole(t *testing.T) {
	t.Parallel()
	fake := newMKSV2Fake(t)

	config := func(admissionControllers string) string {
		return testMKSClusterV2Config("", fmt.Sprintf(`
  project_id   = "attribute-project"
  kube_version = "1.30.3"
  workers_type = "CLOUD"
  kubernetes_options = {
    feature_gates         = ["TopologyAwareHints"]
    admission_controllers = [%s]
    audit_logs = {
      enabled     = true
      secret_name = "siem"
    }
    oidc = {
      enabled       = true
      provider_name = "keycloak"
      issuer_url    = "https://issuer.example.com"
      client_id     = "kubernetes"
    }
  }
`, admissionControllers))
	}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: fake.providerFactories(),
		CheckDestroy:             testMKSClusterV2Destroyed(fake),
		Steps: []resource.TestStep{
			{
				Config: config(`"NodeRestriction"`),
			},
			{
				Config: config(`"NodeRestriction", "PodNodeSelector"`),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(testMKSClusterV2Name, "kubernetes_options.admission_controllers.#", "2"),
					func(_ *terraform.State) error {
						body, _ := fake.lastBody(t, mksV2RoutePatchCluster)["cluster"].(map[string]any)
						if len(body) != 1 || body["kubernetes_options"] == nil {
							return fmt.Errorf("PATCH sent %v, want only kubernetes_options", body)
						}
						options, _ := body["kubernetes_options"].(map[string]any)
						audit, _ := options["audit_logs"].(map[string]any)
						oidc, _ := options["oidc"].(map[string]any)
						if fmt.Sprint(options["feature_gates"]) != "[TopologyAwareHints]" ||
							audit["enabled"] != true || audit["secret_name"] != "siem" ||
							oidc["enabled"] != true || oidc["provider_name"] != "keycloak" {
							return fmt.Errorf("PATCH dropped sibling kubernetes_options: %v", options)
						}
						if fake.callCount(mksV2RouteTask) == 0 {
							return fmt.Errorf("the provider did not poll the UPGRADE_MASTERS_CONFIG task")
						}

						return nil
					},
				),
			},
		},
	})
}

func TestMKSClusterV2ResourceAutoUpgradeFalse(t *testing.T) {
	t.Parallel()
	fake := newMKSV2Fake(t)

	config := func(autoUpgrade bool) string {
		return testMKSClusterV2Config("", fmt.Sprintf(`
  project_id   = "attribute-project"
  kube_version = "1.30.3"
  workers_type = "CLOUD"
  enable_patch_version_auto_upgrade = %t
`, autoUpgrade))
	}
	sent := func(route string, want bool) resource.TestCheckFunc {
		return func(_ *terraform.State) error {
			return testMKSClusterV2BodyFields(fake.lastBody(t, route), map[string]any{"enable_patch_version_auto_upgrade": want})
		}
	}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: fake.providerFactories(),
		CheckDestroy:             testMKSClusterV2Destroyed(fake),
		Steps: []resource.TestStep{
			{
				Config: config(false),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(testMKSClusterV2Name, "enable_patch_version_auto_upgrade", "false"),
					sent(mksV2RouteCreateCluster, false),
				),
			},
			{
				Config: config(true),
				Check:  sent(mksV2RoutePatchCluster, true),
			},
			{
				Config: config(false),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(testMKSClusterV2Name, "enable_patch_version_auto_upgrade", "false"),
					sent(mksV2RoutePatchCluster, false),
				),
			},
		},
	})
}

func TestMKSClusterV2ResourceBasicClusterAutoUpgrade(t *testing.T) {
	t.Parallel()
	fake := newMKSV2Fake(t)

	config := func(autoUpgrade string) string {
		return testMKSClusterV2Config("", `
  project_id   = "attribute-project"
  kube_version = "1.30.3"
  workers_type = "CLOUD"
  cluster_type = "BASIC"
`+autoUpgrade)
	}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: fake.providerFactories(),
		CheckDestroy:             testMKSClusterV2Destroyed(fake),
		Steps: []resource.TestStep{
			{
				Config:      config("  enable_patch_version_auto_upgrade = true\n"),
				PlanOnly:    true,
				ExpectError: testMKSClusterV2Error(`Patch version auto-upgrade is not available for BASIC clusters`),
			},
			{
				Config: config(""),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(testMKSClusterV2Name, "cluster_type", "BASIC"),
					resource.TestCheckResourceAttr(testMKSClusterV2Name, "enable_patch_version_auto_upgrade", "false"),
					func(_ *terraform.State) error {
						return testMKSClusterV2BodyFields(fake.lastBody(t, mksV2RouteCreateCluster),
							map[string]any{"cluster_type": "BASIC", "basic": false}, "enable_patch_version_auto_upgrade")
					},
				),
			},
		},
	})
}

func TestMKSClusterV2ResourceKubeVersionUpgrade(t *testing.T) {
	t.Parallel()
	fake := newMKSV2Fake(t)
	fake.seedKubeVersions(
		mksclient.KubeVersionInfo{Version: new("1.30.3")},
		mksclient.KubeVersionInfo{Version: new("1.30.5")},
		mksclient.KubeVersionInfo{Version: new("1.31.2")},
		mksclient.KubeVersionInfo{Version: new("1.31.4"), IsDefault: new(true)},
	)

	config := func(kubeVersion string) string {
		return testMKSClusterV2Config("", fmt.Sprintf(`
  project_id   = "attribute-project"
  kube_version = %q
  workers_type = "CLOUD"
`, kubeVersion))
	}
	calls := func(patch, minor int) resource.TestCheckFunc {
		return func(_ *terraform.State) error {
			gotPatch, gotMinor := fake.callCount(mksV2RouteUpgradePatch), fake.callCount(mksV2RouteUpgradeMinor)
			if gotPatch != patch || gotMinor != minor {
				return fmt.Errorf("upgrade calls: patch %d, minor %d, want %d and %d", gotPatch, gotMinor, patch, minor)
			}

			return nil
		}
	}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: fake.providerFactories(),
		CheckDestroy:             testMKSClusterV2Destroyed(fake),
		Steps: []resource.TestStep{
			{
				Config: config("1.30.3"),
				Check:  calls(0, 0),
			},
			{
				Config:      config("1.30.4"),
				ExpectError: testMKSClusterV2Error(`the latest available patch version is: 1\.30\.5`),
			},
			{
				Config: config("1.30.5"),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(testMKSClusterV2Name, "kube_version", "1.30.5"),
					resource.TestCheckResourceAttr(testMKSClusterV2Name, "status", "ACTIVE"),
					calls(1, 0),
				),
			},
			{
				Config: config("1.31.4"),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(testMKSClusterV2Name, "kube_version", "1.31.4"),
					calls(1, 1),
				),
			},
			{
				// Patch auto-upgrade moves the cluster past the configuration.
				PreConfig: func() {
					fake.updateCluster(testMKSV2ClusterID, func(c *mksclient.ClusterDetailed) { c.KubeVersion = "1.31.6" })
				},
				Config:   config("1.31.4"),
				PlanOnly: true,
			},
			{
				// A lower version, of the same or a lower minor version, is
				// accepted with no change, like in v1.
				Config:   config("1.31.2"),
				PlanOnly: true,
			},
			{
				Config: config("1.30.5"),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(testMKSClusterV2Name, "kube_version", "1.31.4"),
					calls(1, 1),
				),
			},
			{
				Config:      config("1.33.1"),
				PlanOnly:    true,
				ExpectError: testMKSClusterV2Error(`must be upgraded one minor version at a time`),
			},
		},
	})
}

// A minor upgrade outside Terraform is read as is, so the next one-minor
// upgrade is checked against the version the cluster runs.
func TestMKSClusterV2ResourceKubeVersionUpgradedOutside(t *testing.T) {
	t.Parallel()
	fake := newMKSV2Fake(t)
	fake.seedKubeVersions(
		mksclient.KubeVersionInfo{Version: new("1.30.5")},
		mksclient.KubeVersionInfo{Version: new("1.31.2")},
		mksclient.KubeVersionInfo{Version: new("1.32.0"), IsDefault: new(true)},
	)

	config := func(kubeVersion string) string {
		return testMKSClusterV2Config("", fmt.Sprintf(`
  project_id   = "attribute-project"
  kube_version = %q
  workers_type = "CLOUD"
`, kubeVersion))
	}
	calls := func(patch, minor int) resource.TestCheckFunc {
		return func(_ *terraform.State) error {
			gotPatch, gotMinor := fake.callCount(mksV2RouteUpgradePatch), fake.callCount(mksV2RouteUpgradeMinor)
			if gotPatch != patch || gotMinor != minor {
				return fmt.Errorf("upgrade calls: patch %d, minor %d, want %d and %d", gotPatch, gotMinor, patch, minor)
			}

			return nil
		}
	}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: fake.providerFactories(),
		CheckDestroy:             testMKSClusterV2Destroyed(fake),
		Steps: []resource.TestStep{
			{
				Config: config("1.30.5"),
				Check:  calls(0, 0),
			},
			{
				// Upgraded in the panel: the old configuration plans nothing.
				PreConfig: func() {
					fake.updateCluster(testMKSV2ClusterID, func(c *mksclient.ClusterDetailed) { c.KubeVersion = "1.31.2" })
				},
				Config:   config("1.30.5"),
				PlanOnly: true,
			},
			{
				Config: config("1.32.0"),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(testMKSClusterV2Name, "kube_version", "1.32.0"),
					calls(0, 1),
				),
			},
		},
	})
}

func TestMKSClusterV2ResourceImport(t *testing.T) {
	t.Parallel()

	// The dashed provider project is the form mk-api-v2 returns: the state
	// and the clients take it without dashes, like _v1.
	for _, providerProject := range []string{"provider-project", testMKSV2APIProject} {
		t.Run(providerProject, func(t *testing.T) {
			t.Parallel()
			fake := newMKSV2Fake(t)

			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: fake.providerFactories(),
				CheckDestroy:             testMKSClusterV2Destroyed(fake),
				Steps: []resource.TestStep{
					{
						Config: testMKSClusterV2Config(testMKSClusterV2ProviderConfig(providerProject, testMKSV2Pool), `
  kube_version      = "1.30.3"
  workers_type      = "DEDICATED"
  cloud_subnet_cidr = "10.10.0.0/16"
  kubernetes_options = {
    feature_gates        = ["TopologyAwareHints"]
    x509_ca_certificates = "Y2VydA=="
  }
`),
						Check: resource.TestCheckResourceAttr(testMKSClusterV2Name, "project_id", mksV2KeystoneProjectID(providerProject)),
					},
					{
						ResourceName:      testMKSClusterV2Name,
						ImportState:       true,
						ImportStateVerify: true,
						// The API never returns them.
						ImportStateVerifyIgnore: []string{"cloud_subnet_cidr", "kubernetes_options.x509_ca_certificates"},
					},
				},
			})

			fake.checkClients(t, mksV2KeystoneProjectID(providerProject))
		})
	}
}

// TestMKSClusterV2ResourceImportDashedProject imports by ID a cluster that
// mk-api-v2 returns with a dashed project: the state takes it without dashes,
// like _v1, and the plan of an un-dashed or no project_id is empty.
func TestMKSClusterV2ResourceImportDashedProject(t *testing.T) {
	testMKSV2TerraformAtLeast(t, 1, 5)
	t.Parallel()

	tests := []struct{ name, projectID string }{
		{name: "un-dashed project_id", projectID: testMKSV2KeystoneProject},
		{name: "no project_id"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fake := newMKSV2Fake(t)
			fake.seedCluster(mksclient.ClusterDetailed{
				Id: testMKSV2ClusterID, Name: "tf-v2", Pool: testMKSV2Pool, ProjectId: testMKSV2APIProject, KubeVersion: "1.30.3",
				ClusterType: mksclient.HIGHAVAILABILITY, NetworkType: mksclient.ClusterDetailedNetworkTypeSTANDARD,
				NetworkId: "net-1", SubnetId: "subnet-1", EnableAutorepair: true, EnablePatchVersionAutoUpgrade: true,
				MaintenanceWindowStart: "01:00:00", MaintenanceWindowEnd: "03:00:00",
				CniType: mksclient.ClusterDetailedCniType(mksclient.ClusterCniTypeCALICO), KubeApiIp: "192.0.2.10", Status: "ACTIVE",
			}, "")

			config := fmt.Sprintf(`
import {
  to = %s
  id = %q
}
`, testMKSClusterV2Name, testMKSV2ClusterID) + testMKSClusterV2Config(testMKSClusterV2ProviderConfig(testMKSV2APIProject, testMKSV2Pool), `
  `+testMKSV2ProjectIDArgument(tt.projectID)+`
  kube_version             = "1.30.3"
  workers_type             = "CLOUD"
  maintenance_window_start = "01:00:00"
`)

			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: fake.providerFactories(),
				CheckDestroy:             testMKSClusterV2Destroyed(fake),
				Steps: []resource.TestStep{
					{
						Config: config,
						Check: resource.ComposeTestCheckFunc(
							resource.TestCheckResourceAttr(testMKSClusterV2Name, "project_id", testMKSV2KeystoneProject),
							testMKSClusterV2Calls(fake, map[string]int{mksV2RouteCreateCluster: 0, mksV2RoutePatchCluster: 0}),
						),
					},
					{
						Config:   config,
						PlanOnly: true,
					},
				},
			})

			fake.checkClients(t, testMKSV2KeystoneProject)
		})
	}
}

func TestMKSClusterV2ResourceImportNeedsProviderConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		providerConfig string
		wantError      string
	}{
		{
			name:           "no project",
			providerConfig: testMKSClusterV2ProviderConfig("", testMKSV2Pool),
			wantError:      "INFRA_PROJECT_ID must be set for the resource import",
		},
		{
			name:           "no pool",
			providerConfig: testMKSClusterV2ProviderConfig("provider-project", ""),
			wantError:      "INFRA_REGION must be set for the resource import",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fake := newMKSV2Fake(t)

			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: fake.providerFactories(),
				Steps: []resource.TestStep{
					{
						Config: testMKSClusterV2Config(tt.providerConfig, `
  project_id   = "attribute-project"
  kube_version = "1.30.3"
  workers_type = "CLOUD"
`),
						ResourceName:  testMKSClusterV2Name,
						ImportState:   true,
						ImportStateId: testMKSV2ClusterID,
						ExpectError:   testMKSClusterV2Error(tt.wantError),
					},
				},
			})
		})
	}
}

func TestMKSClusterV2ResourceRemovedOutside(t *testing.T) {
	t.Parallel()
	fake := newMKSV2Fake(t)

	config := testMKSClusterV2Config("", `
  project_id   = "attribute-project"
  kube_version = "1.30.3"
  workers_type = "CLOUD"
`)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: fake.providerFactories(),
		CheckDestroy:             testMKSClusterV2Destroyed(fake),
		Steps: []resource.TestStep{
			{
				Config: config,
			},
			{
				PreConfig:          func() { fake.removeCluster(testMKSV2ClusterID) },
				Config:             config,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

func TestMKSClusterV2ResourceDeleteTaskError(t *testing.T) {
	t.Parallel()
	fake := newMKSV2Fake(t)

	config := testMKSClusterV2Config("", `
  project_id   = "attribute-project"
  kube_version = "1.30.3"
  workers_type = "CLOUD"
`)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: fake.providerFactories(),
		CheckDestroy:             testMKSClusterV2Destroyed(fake),
		Steps: []resource.TestStep{
			{
				Config: config,
			},
			{
				PreConfig:   func() { fake.failTasks("DELETE_CLUSTER", true) },
				Config:      config,
				Destroy:     true,
				ExpectError: testMKSClusterV2Error(`task DELETE_CLUSTER task-\d+ of cluster ` + testMKSV2ClusterID + ` ended in ERROR`),
			},
			{
				// The failed delete left the cluster in place.
				PreConfig: func() { fake.failTasks("DELETE_CLUSTER", false) },
				Config:    config,
				PlanOnly:  true,
			},
		},
	})
}

func TestMKSV2TaskWaiterScope(t *testing.T) {
	tests := []struct {
		name        string
		nodegroupID string
		wantTimeout bool
	}{
		{
			name: "cluster scope waits for cluster tasks only",
		},
		{
			name:        "nodegroup scope waits for its nodegroup tasks only",
			nodegroupID: "ng-a",
		},
		{
			name:        "a stuck task in scope times out",
			nodegroupID: "ng-stuck",
			wantTimeout: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := newMKSV2Fake(t)
			fake.seedCluster(mksclient.ClusterDetailed{Id: testMKSV2ClusterID}, "")
			client, err := newMKSV2ServiceClient("fake-token", fake.server.URL, "test")
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()

			// Tasks that existed before the call never count, even stuck in scope.
			fake.seedTask(testMKSV2ClusterID, "", "OLD", true)
			fake.seedTask(testMKSV2ClusterID, "ng-a", "OLD", true)
			waiter, err := newMKSV2TaskWaiter(ctx, client, testMKSV2ClusterID, tt.nodegroupID)
			if err != nil {
				t.Fatal(err)
			}
			// The call: one finishing task in the scope, stuck ones outside it.
			for _, nodegroupID := range []string{"", "ng-a", "ng-b", "ng-stuck"} {
				stuck := nodegroupID != tt.nodegroupID || nodegroupID == "ng-stuck"
				fake.seedTask(testMKSV2ClusterID, nodegroupID, "NEW", stuck)
			}

			err = waiter.Wait(ctx)
			if errors.Is(err, context.DeadlineExceeded) != tt.wantTimeout || (err != nil && !tt.wantTimeout) {
				t.Fatalf("Wait() = %v, want timeout %t", err, tt.wantTimeout)
			}
		})
	}
}

func TestMKSClusterV2KubeVersion(t *testing.T) {
	tests := []struct {
		name   string
		prior  string
		actual string
		want   string
	}{
		{name: "no prior, after import", prior: "", actual: "1.30.5", want: "1.30.5"},
		{name: "same version", prior: "1.30.3", actual: "1.30.3", want: "1.30.3"},
		{name: "newer patch keeps the configured one", prior: "1.30.3", actual: "1.30.5", want: "1.30.3"},
		{name: "older patch is drift", prior: "1.30.5", actual: "1.30.3", want: "1.30.3"},
		{name: "newer minor is drift", prior: "1.30.5", actual: "1.31.1", want: "1.31.1"},
		{name: "newer major is drift", prior: "1.30.5", actual: "2.0.1", want: "2.0.1"},
		{name: "older minor is drift", prior: "1.31.1", actual: "1.30.5", want: "1.30.5"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mksClusterV2KubeVersion(tt.prior, tt.actual)
			if got != tt.want {
				t.Errorf("mksClusterV2KubeVersion(%q, %q) = %q, want %q", tt.prior, tt.actual, got, tt.want)
			}
		})
	}
}

func TestMKSClusterV2ResourceCreateTimeout(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// readFails makes the read after the wait fail too.
		readFails bool
		wantError string
	}{
		{name: "read after the wait succeeds", wantError: "Error waiting for the cluster to become ready"},
		{name: "read after the wait fails", readFails: true, wantError: "Error reading cluster"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fake := newMKSV2Fake(t)
			fake.stickTasks("CREATE_CLUSTER", true)
			if tt.readFails {
				fake.fail(mksV2RouteCluster, http.StatusInternalServerError)
			}

			config := testMKSClusterV2Config("", `
  project_id   = "attribute-project"
  kube_version = "1.30.3"
  workers_type = "CLOUD"
  timeouts {
    create = "1s"
  }
`)
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: fake.providerFactories(),
				CheckDestroy:             testMKSClusterV2Destroyed(fake),
				Steps: []resource.TestStep{
					{
						Config:      config,
						ExpectError: testMKSClusterV2Error(tt.wantError + `(?s:.*)context deadline exceeded`),
					},
					{
						// The timed-out cluster is in state, tainted: the
						// next apply deletes it and creates a new one. The
						// create goes on in the API, so the delete waits until
						// the cluster leaves PENDING_CREATE instead of getting
						// 409.
						PreConfig: func() {
							calls := fake.callCount(mksV2RouteCreateCluster)
							if calls != 1 {
								t.Errorf("the failed apply sent %d create requests, want 1", calls)
							}
							fake.stickTasks("CREATE_CLUSTER", false)
							fake.settleAfter(testMKSV2ClusterID, 3, "ACTIVE")
							fake.fail(mksV2RouteCluster, 0)
						},
						Config: config,
						Check: resource.ComposeTestCheckFunc(
							resource.TestCheckResourceAttr(testMKSClusterV2Name, "id", testMKSV2ClusterID),
							resource.TestCheckResourceAttr(testMKSClusterV2Name, "status", "ACTIVE"),
							testMKSClusterV2Calls(fake, map[string]int{mksV2RouteCreateCluster: 2, mksV2RouteDeleteCluster: 1}),
						),
					},
				},
			})
		})
	}
}

func TestMKSClusterV2ResourceDeleteWaitsForStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status string
		// settle is the status the cluster reaches on its own after a few
		// GETs; an empty one removes the cluster. Without it the status stays.
		settle    *string
		wantError string
		// wantDeletes is the number of DELETE requests the destroy sends.
		wantDeletes int
	}{
		{name: "pending create ends", status: "PENDING_CREATE", settle: new("ACTIVE"), wantDeletes: 1},
		{name: "error deletes at once", status: "ERROR", wantDeletes: 1},
		{name: "maintenance deletes at once", status: "MAINTENANCE", wantDeletes: 1},
		{name: "gone while waiting", status: "PENDING_CREATE", settle: new("")},
		{
			name:      "still pending at the timeout",
			status:    "PENDING_CREATE",
			wantError: `Error deleting cluster(?s:.*)cluster status PENDING_CREATE does not allow the delete yet(?s:.*)context deadline exceeded`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fake := newMKSV2Fake(t)

			config := testMKSClusterV2Config("", `
  project_id   = "attribute-project"
  kube_version = "1.30.3"
  workers_type = "CLOUD"
  timeouts {
    delete = "1s"
  }
`)
			steps := []resource.TestStep{
				{Config: config},
				{
					PreConfig: func() {
						fake.updateCluster(testMKSV2ClusterID, func(c *mksclient.ClusterDetailed) {
							c.Status = mksclient.ClusterDetailedStatus(tt.status)
						})
						if tt.settle != nil {
							fake.settleAfter(testMKSV2ClusterID, 3, *tt.settle)
						}
					},
					Config:  config,
					Destroy: true,
				},
			}
			if tt.wantError != "" {
				steps[1].ExpectError = testMKSClusterV2Error(tt.wantError)
				// The cluster stays in state; the post-test destroy deletes
				// it once it is ACTIVE.
				steps = append(steps, resource.TestStep{
					PreConfig: func() {
						calls := fake.callCount(mksV2RouteDeleteCluster)
						if calls != 0 {
							t.Errorf("the timed-out destroy sent %d delete requests, want 0", calls)
						}
						fake.updateCluster(testMKSV2ClusterID, func(c *mksclient.ClusterDetailed) { c.Status = "ACTIVE" })
					},
					Config: config,
				})
				tt.wantDeletes = 1
			}

			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: fake.providerFactories(),
				CheckDestroy: resource.ComposeTestCheckFunc(
					testMKSClusterV2Destroyed(fake),
					testMKSClusterV2Calls(fake, map[string]int{mksV2RouteDeleteCluster: tt.wantDeletes}),
				),
				Steps: steps,
			})
		})
	}
}

func TestMKSClusterV2ResourceUpdateTimeout(t *testing.T) {
	t.Parallel()
	fake := newMKSV2Fake(t)

	config := func(featureGates string) string {
		return testMKSClusterV2Config("", fmt.Sprintf(`
  project_id   = "attribute-project"
  kube_version = "1.30.3"
  workers_type = "CLOUD"
  kubernetes_options = {
    feature_gates = [%s]
  }
  timeouts {
    update = "1s"
  }
`, featureGates))
	}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: fake.providerFactories(),
		CheckDestroy:             testMKSClusterV2Destroyed(fake),
		Steps: []resource.TestStep{
			{
				Config: config(""),
			},
			{
				PreConfig:   func() { fake.stickTasks("UPGRADE_MASTERS_CONFIG", true) },
				Config:      config(`"TopologyAwareHints"`),
				ExpectError: testMKSClusterV2Error(`Error updating cluster(?s:.*)context deadline exceeded`),
			},
			{
				// The API stored the options before its task, so the refresh
				// reads them and the plan is empty. The task ends on its own.
				PreConfig: func() {
					fake.stickTasks("UPGRADE_MASTERS_CONFIG", false)
					fake.settleAfter(testMKSV2ClusterID, 1, "ACTIVE")
				},
				Config:   config(`"TopologyAwareHints"`),
				PlanOnly: true,
			},
		},
	})
}

func TestMKSClusterV2ResourceDeleteTimeout(t *testing.T) {
	t.Parallel()
	fake := newMKSV2Fake(t)

	config := testMKSClusterV2Config("", `
  project_id   = "attribute-project"
  kube_version = "1.30.3"
  workers_type = "CLOUD"
  timeouts {
    delete = "1s"
  }
`)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: fake.providerFactories(),
		CheckDestroy: resource.ComposeTestCheckFunc(
			testMKSClusterV2Destroyed(fake),
			// The second destroy waits for the first delete instead of
			// sending another one, which the API would refuse with 409.
			testMKSClusterV2Calls(fake, map[string]int{mksV2RouteDeleteCluster: 1}),
		),
		Steps: []resource.TestStep{
			{
				Config: config,
			},
			{
				PreConfig:   func() { fake.stickTasks("DELETE_CLUSTER", true) },
				Config:      config,
				Destroy:     true,
				ExpectError: testMKSClusterV2Error(`Error deleting cluster(?s:.*)context deadline exceeded`),
			},
			{
				// The cluster stays in state, PENDING_DELETE, until the
				// delete ends in the API.
				PreConfig: func() { fake.settleAfter(testMKSV2ClusterID, 3, "") },
				Config:    config,
				Destroy:   true,
			},
		},
	})
}

func TestMKSClusterV2ResourceX509OnCreate(t *testing.T) {
	t.Parallel()
	fake := newMKSV2Fake(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: fake.providerFactories(),
		CheckDestroy:             testMKSClusterV2Destroyed(fake),
		Steps: []resource.TestStep{
			{
				Config: testMKSClusterV2Config("", `
  project_id   = "attribute-project"
  kube_version = "1.30.3"
  workers_type = "CLOUD"
  kubernetes_options = {
    feature_gates        = ["TopologyAwareHints"]
    x509_ca_certificates = "Y2VydA=="
  }
`),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(testMKSClusterV2Name, "kubernetes_options.x509_ca_certificates", "Y2VydA=="),
					resource.TestCheckResourceAttr(testMKSClusterV2Name, "status", "ACTIVE"),
					testMKSClusterV2Calls(fake, map[string]int{mksV2RouteCreateCluster: 1, mksV2RoutePatchCluster: 1}),
					func(_ *terraform.State) error {
						// Create ignores x509, so it must come with a PATCH of
						// the whole kubernetes_options.
						body, _ := fake.lastBody(t, mksV2RoutePatchCluster)["cluster"].(map[string]any)
						options, _ := body["kubernetes_options"].(map[string]any)
						if len(body) != 1 || options["x509_ca_certificates"] != "Y2VydA==" ||
							fmt.Sprint(options["feature_gates"]) != "[TopologyAwareHints]" {
							return fmt.Errorf("PATCH sent %v, want the whole kubernetes_options with x509", body)
						}
						stored := fake.storedX509(testMKSV2ClusterID)
						if stored != "Y2VydA==" {
							return fmt.Errorf("the API stored x509 %q, want Y2VydA==", stored)
						}

						return nil
					},
				),
			},
		},
	})
}

func TestMKSClusterV2ResourceUpperCaseName(t *testing.T) {
	t.Parallel()
	fake := newMKSV2Fake(t)

	config := strings.Replace(testMKSClusterV2Config("", `
  project_id   = "attribute-project"
  kube_version = "1.30.3"
  workers_type = "CLOUD"
`), `name = "tf-v2"`, `name = "TF-v2"`, 1)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: fake.providerFactories(),
		CheckDestroy:             testMKSClusterV2Destroyed(fake),
		Steps: []resource.TestStep{
			{
				// The API stores tf-v2; the state keeps the configured case,
				// so the result is consistent and the next plan empty.
				Config: config,
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(testMKSClusterV2Name, "name", "TF-v2"),
					func(_ *terraform.State) error {
						body, _ := fake.lastBody(t, mksV2RouteCreateCluster)["cluster"].(map[string]any)
						if body["name"] != "TF-v2" {
							return fmt.Errorf("create sent name %v, want TF-v2", body["name"])
						}

						return nil
					},
				),
			},
			{
				// Only the case changes: no replacement.
				Config:   strings.Replace(config, `name = "TF-v2"`, `name = "tf-V2"`, 1),
				PlanOnly: true,
			},
			{
				Config:             strings.Replace(config, `name = "TF-v2"`, `name = "tf-v3"`, 1),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

func TestMKSClusterV2ResourceX509OnCreateFails(t *testing.T) {
	t.Parallel()
	fake := newMKSV2Fake(t)
	// Like a cluster that entered its maintenance window during the create:
	// mk-api-v2 PATCH accepts only ACTIVE clusters.
	fake.fail(mksV2RoutePatchCluster, http.StatusConflict)

	config := testMKSClusterV2Config("", `
  project_id   = "attribute-project"
  kube_version = "1.30.3"
  workers_type = "CLOUD"
  kubernetes_options = {
    feature_gates        = ["TopologyAwareHints"]
    x509_ca_certificates = "Y2VydA=="
  }
`)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: fake.providerFactories(),
		CheckDestroy:             testMKSClusterV2Destroyed(fake),
		Steps: []resource.TestStep{
			{
				// The create succeeds with a warning; the refresh after it
				// finds no x509 on the cluster, so the plan shows them.
				Config:             config,
				ExpectNonEmptyPlan: true,
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(testMKSClusterV2Name, "id", testMKSV2ClusterID),
					testMKSClusterV2Calls(fake, map[string]int{mksV2RouteCreateCluster: 1, mksV2RoutePatchCluster: 1}),
				),
			},
			{
				// The plan after it shows only the x509 in kubernetes_options
				// and the status every update makes unknown.
				RefreshState:       true,
				ExpectNonEmptyPlan: true,
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckNoResourceAttr(testMKSClusterV2Name, "kubernetes_options.x509_ca_certificates"),
					resource.TestCheckResourceAttr(testMKSClusterV2Name, "kubernetes_options.feature_gates.0", "TopologyAwareHints"),
				),
			},
			{
				Config:             config,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{
				// The cluster was not tainted: the next apply only PATCHes
				// the x509.
				PreConfig: func() { fake.fail(mksV2RoutePatchCluster, 0) },
				Config:    config,
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(testMKSClusterV2Name, "kubernetes_options.x509_ca_certificates", "Y2VydA=="),
					testMKSClusterV2Calls(fake, map[string]int{
						mksV2RouteCreateCluster: 1, mksV2RouteDeleteCluster: 0, mksV2RoutePatchCluster: 2,
					}),
					func(_ *terraform.State) error {
						body, _ := fake.lastBody(t, mksV2RoutePatchCluster)["cluster"].(map[string]any)
						options, _ := body["kubernetes_options"].(map[string]any)
						if len(body) != 1 || options["x509_ca_certificates"] != "Y2VydA==" {
							return fmt.Errorf("PATCH sent %v, want only kubernetes_options with x509", body)
						}
						stored := fake.storedX509(testMKSV2ClusterID)
						if stored != "Y2VydA==" {
							return fmt.Errorf("the API stored x509 %q, want Y2VydA==", stored)
						}

						return nil
					},
				),
			},
		},
	})
}

func TestMKSClusterV2ResourceOIDCCACertsWhitespace(t *testing.T) {
	t.Parallel()
	fake := newMKSV2Fake(t)

	config := func(caCerts string) string {
		return testMKSClusterV2Config("", fmt.Sprintf(`
  project_id   = "attribute-project"
  kube_version = "1.30.3"
  workers_type = "CLOUD"
  kubernetes_options = {
    oidc = {
      enabled       = true
      provider_name = "keycloak"
      issuer_url    = "https://issuer.example.com"
      client_id     = "kubernetes"
      ca_certs      = %q
    }
  }
`, caCerts))
	}
	stored := func(want string) resource.TestCheckFunc {
		return func(_ *terraform.State) error {
			var got string
			fake.updateCluster(testMKSV2ClusterID, func(c *mksclient.ClusterDetailed) { got = c.KubernetesOptions.Oidc.CaCerts })
			if got != want {
				return fmt.Errorf("the API stored ca_certs %q, want %q", got, want)
			}

			return nil
		}
	}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: fake.providerFactories(),
		CheckDestroy:             testMKSClusterV2Destroyed(fake),
		Steps: []resource.TestStep{
			{
				// Like file("ca.pem"): the API trims the trailing newline.
				Config: config("cert-1\n"),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(testMKSClusterV2Name, "kubernetes_options.oidc.ca_certs", "cert-1\n"),
					stored("cert-1"),
				),
			},
			{
				Config: config("  cert-2\n"),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(testMKSClusterV2Name, "kubernetes_options.oidc.ca_certs", "  cert-2\n"),
					stored("cert-2"),
				),
			},
			{
				// Like an import, where the state has the API value: a change of
				// only the whitespace plans nothing.
				Config:   config("cert-2"),
				PlanOnly: true,
			},
		},
	})
}

func TestMKSClusterV2ResourceOIDCClaimDefaults(t *testing.T) {
	t.Parallel()
	fake := newMKSV2Fake(t)

	enabled := func(claims string) string {
		return testMKSClusterV2OIDCConfig(testMKSClusterV2EnabledOIDC + claims)
	}
	claims := func(route, username, groups string) resource.TestCheckFunc {
		return resource.ComposeTestCheckFunc(
			resource.TestCheckResourceAttr(testMKSClusterV2Name, "kubernetes_options.oidc.username_claim", username),
			resource.TestCheckResourceAttr(testMKSClusterV2Name, "kubernetes_options.oidc.groups_claim", groups),
			testMKSClusterV2OIDCSent(t, fake, route, map[string]any{
				"enabled": true, "provider_name": "keycloak", "issuer_url": "https://issuer.example.com",
				"client_id": "kubernetes", "username_claim": username, "groups_claim": groups,
			}),
		)
	}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: fake.providerFactories(),
		CheckDestroy:             testMKSClusterV2Destroyed(fake),
		Steps: []resource.TestStep{
			{
				Config: testMKSClusterV2OIDCConfig(`
      enabled = false`),
			},
			{
				// Enabling it with the claims omitted plans the API defaults,
				// so the apply is consistent with what the API stores.
				Config: enabled(""),
				Check:  claims(mksV2RoutePatchCluster, "sub", "groups"),
			},
			{
				Config: enabled(`
      username_claim = "email"
      groups_claim   = "roles"`),
				Check: claims(mksV2RoutePatchCluster, "email", "roles"),
			},
			{
				// Removing the configured claims returns them to the defaults.
				Config: enabled(""),
				Check:  claims(mksV2RoutePatchCluster, "sub", "groups"),
			},
		},
	})
}

func TestMKSClusterV2ResourceOIDCDisable(t *testing.T) {
	t.Parallel()
	fake := newMKSV2Fake(t)

	enabledOIDC := testMKSClusterV2EnabledOIDC + `
      ca_certs      = "cert"`
	disabledSent := map[string]any{
		"enabled": false, "provider_name": "", "issuer_url": "", "client_id": "", "username_claim": "", "groups_claim": "",
	}
	cleared := resource.ComposeTestCheckFunc(
		resource.TestCheckResourceAttr(testMKSClusterV2Name, "kubernetes_options.oidc.enabled", "false"),
		resource.TestCheckResourceAttr(testMKSClusterV2Name, "kubernetes_options.oidc.provider_name", ""),
		resource.TestCheckResourceAttr(testMKSClusterV2Name, "kubernetes_options.oidc.issuer_url", ""),
		resource.TestCheckResourceAttr(testMKSClusterV2Name, "kubernetes_options.oidc.client_id", ""),
		resource.TestCheckResourceAttr(testMKSClusterV2Name, "kubernetes_options.oidc.username_claim", ""),
		resource.TestCheckResourceAttr(testMKSClusterV2Name, "kubernetes_options.oidc.groups_claim", ""),
		resource.TestCheckResourceAttr(testMKSClusterV2Name, "kubernetes_options.oidc.ca_certs", ""),
		testMKSClusterV2OIDCSent(t, fake, mksV2RoutePatchCluster, disabledSent),
		func(_ *terraform.State) error {
			var got mksclient.OIDC
			fake.updateCluster(testMKSV2ClusterID, func(c *mksclient.ClusterDetailed) { got = c.KubernetesOptions.Oidc })
			if got != (mksclient.OIDC{}) {
				return fmt.Errorf("the API kept OIDC %+v, want it cleared", got)
			}

			return nil
		},
	)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: fake.providerFactories(),
		CheckDestroy:             testMKSClusterV2Destroyed(fake),
		Steps: []resource.TestStep{
			{
				Config: testMKSClusterV2OIDCConfig(enabledOIDC),
			},
			{
				// The state still holds the parameters, but the PATCH sends
				// them empty, which is the only form the API accepts.
				Config: testMKSClusterV2OIDCConfig(`
      enabled = false`),
				Check: cleared,
			},
			{
				// Enabling it again sends the configured parameters.
				Config: testMKSClusterV2OIDCConfig(enabledOIDC),
				Check: testMKSClusterV2OIDCSent(t, fake, mksV2RoutePatchCluster, map[string]any{
					"enabled": true, "provider_name": "keycloak", "issuer_url": "https://issuer.example.com",
					"client_id": "kubernetes", "username_claim": "sub", "groups_claim": "groups", "ca_certs": "cert",
				}),
			},
			{
				Config:      testMKSClusterV2OIDCConfig(strings.Replace(enabledOIDC, "true", "false", 1)),
				ExpectError: testMKSClusterV2Error(`(?s)OIDC parameter set while OIDC is disabled.*oidc parameters cannot be configured when it is disabled`),
			},
			{
				// Empty parameters are no parameters to the API.
				Config: testMKSClusterV2OIDCConfig(`
      enabled        = false
      provider_name  = ""
      issuer_url     = ""
      client_id      = ""
      username_claim = ""
      groups_claim   = ""
      ca_certs       = ""`),
				Check: cleared,
			},
			{
				// Re-enabling with the empty claims kept would never converge:
				// the API stores the defaults instead.
				Config: testMKSClusterV2OIDCConfig(testMKSClusterV2EnabledOIDC + `
      username_claim = ""
      groups_claim   = ""`),
				ExpectError: testMKSClusterV2Error("(?s)Empty OIDC claim while OIDC is enabled.*(username|groups)_claim cannot be empty while OIDC is enabled: omit it to get `(sub|groups)`"),
			},
			{
				Config: testMKSClusterV2OIDCConfig(testMKSClusterV2EnabledOIDC),
				Check: testMKSClusterV2OIDCSent(t, fake, mksV2RoutePatchCluster, map[string]any{
					"enabled": true, "provider_name": "keycloak", "issuer_url": "https://issuer.example.com",
					"client_id": "kubernetes", "username_claim": "sub", "groups_claim": "groups",
				}),
			},
		},
	})
}

func TestMKSClusterV2ResourceOIDCUnknownWhileDisabled(t *testing.T) {
	t.Parallel()
	fake := newMKSV2Fake(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: fake.providerFactories(),
		CheckDestroy:             testMKSClusterV2Destroyed(fake),
		Steps: []resource.TestStep{
			{
				Config: testMKSClusterV2OIDCConfig(testMKSClusterV2EnabledOIDC),
			},
			{
				// issuer_url is unknown until terraform_data is created, so the
				// plan keeps it unknown and the apply-time plan clears it.
				Config: testMKSClusterV2OIDCConfig(`
      enabled    = false
      issuer_url = terraform_data.empty.output`) + `
resource "terraform_data" "empty" {
  input = ""
}
`,
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(testMKSClusterV2Name, "kubernetes_options.oidc.enabled", "false"),
					resource.TestCheckResourceAttr(testMKSClusterV2Name, "kubernetes_options.oidc.issuer_url", ""),
					testMKSClusterV2OIDCSent(t, fake, mksV2RoutePatchCluster, map[string]any{
						"enabled": false, "provider_name": "", "issuer_url": "", "client_id": "", "username_claim": "", "groups_claim": "",
					}),
				),
			},
		},
	})
}

// testMKSClusterV2EnabledOIDC is the body of an enabled oidc object with the
// required parameters only.
const testMKSClusterV2EnabledOIDC = `
      enabled       = true
      provider_name = "keycloak"
      issuer_url    = "https://issuer.example.com"
      client_id     = "kubernetes"`

// testMKSClusterV2OIDCConfig is a cluster whose kubernetes_options hold only
// the oidc object with the given body.
func testMKSClusterV2OIDCConfig(oidc string) string {
	return testMKSClusterV2Config("", fmt.Sprintf(`
  project_id   = "attribute-project"
  kube_version = "1.30.3"
  workers_type = "CLOUD"
  kubernetes_options = {
    oidc = {%s
    }
  }
`, oidc))
}

// testMKSClusterV2OIDCSent checks the oidc object of the last request to the
// route.
func testMKSClusterV2OIDCSent(t *testing.T, fake *mksV2Fake, route string, want map[string]any) resource.TestCheckFunc {
	t.Helper()

	return func(_ *terraform.State) error {
		cluster, _ := fake.lastBody(t, route)["cluster"].(map[string]any)
		options, _ := cluster["kubernetes_options"].(map[string]any)
		oidc, _ := options["oidc"].(map[string]any)
		if !reflect.DeepEqual(oidc, want) {
			return fmt.Errorf("%s sent oidc %v, want %v", route, oidc, want)
		}

		return nil
	}
}

func TestMKSClusterV2ResourceStatusOnUpdate(t *testing.T) {
	t.Parallel()
	fake := newMKSV2Fake(t)

	config := func(admissionControllers string, hubbleRelay bool) string {
		return testMKSClusterV2Config("", fmt.Sprintf(`
  project_id   = "attribute-project"
  kube_version = "1.30.3"
  workers_type = "CLOUD"
  cni_type     = "CILIUM"
  cni_cilium_settings = {
    hubble_relay = %t
  }
  kubernetes_options = {
    admission_controllers = [%s]
  }
`, hubbleRelay, admissionControllers))
	}
	// The refresh sees the cluster in its maintenance window; the finished
	// task leaves it ACTIVE.
	inMaintenance := func() {
		fake.updateCluster(testMKSV2ClusterID, func(c *mksclient.ClusterDetailed) { c.Status = "MAINTENANCE" })
	}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: fake.providerFactories(),
		CheckDestroy:             testMKSClusterV2Destroyed(fake),
		Steps: []resource.TestStep{
			{
				Config: config(`"NodeRestriction"`, true),
			},
			{
				PreConfig: inMaintenance,
				Config:    config(`"NodeRestriction", "PodNodeSelector"`, true),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(testMKSClusterV2Name, "status", "ACTIVE"),
					testMKSClusterV2Calls(fake, map[string]int{mksV2RoutePatchCluster: 1}),
				),
			},
			{
				PreConfig: inMaintenance,
				Config:    config(`"NodeRestriction", "PodNodeSelector"`, false),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(testMKSClusterV2Name, "status", "ACTIVE"),
					resource.TestCheckResourceAttr(testMKSClusterV2Name, "cni_cilium_settings.hubble_relay", "false"),
					testMKSClusterV2Calls(fake, map[string]int{mksV2RoutePatchCluster: 2}),
				),
			},
		},
	})
}

func TestMKSClusterV2ResourceCiliumSettingsNeedCilium(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cniType string
	}{
		{name: "cni_type unset"},
		{name: "cni_type CALICO", cniType: `cni_type = "CALICO"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fake := newMKSV2Fake(t)

			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: fake.providerFactories(),
				Steps: []resource.TestStep{
					{
						Config: testMKSClusterV2Config("", `
  project_id   = "attribute-project"
  kube_version = "1.30.3"
  workers_type = "CLOUD"
  `+tt.cniType+`
  cni_cilium_settings = {
    hubble_relay = false
  }
`),
						PlanOnly:    true,
						ExpectError: testMKSClusterV2Error(`Cilium settings need the Cilium CNI`),
					},
				},
			})
			calls := fake.callCount(mksV2RouteCreateCluster)
			if calls != 0 {
				t.Errorf("the provider sent %d create requests, want none", calls)
			}
		})
	}
}

func TestMKSClusterV2ResourceUpdateTaskError(t *testing.T) {
	t.Parallel()
	fake := newMKSV2Fake(t)
	fake.seedKubeVersions(
		mksclient.KubeVersionInfo{Version: new("1.30.3")},
		mksclient.KubeVersionInfo{Version: new("1.30.5")},
	)

	config := func(kubeVersion, admissionControllers string) string {
		return testMKSClusterV2Config("", fmt.Sprintf(`
  project_id   = "attribute-project"
  kube_version = %q
  workers_type = "CLOUD"
  kubernetes_options = {
    admission_controllers = [%s]
  }
`, kubeVersion, admissionControllers))
	}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: fake.providerFactories(),
		CheckDestroy:             testMKSClusterV2Destroyed(fake),
		Steps: []resource.TestStep{
			{
				Config: config("1.30.3", `"NodeRestriction"`),
			},
			{
				PreConfig: func() { fake.failTasks("UPGRADE_PATCH_VERSION", true) },
				Config:    config("1.30.5", `"NodeRestriction"`),
				ExpectError: testMKSClusterV2Error(`task UPGRADE_PATCH_VERSION task-\d+ of cluster ` + testMKSV2ClusterID +
					` ended in ERROR`),
			},
			{
				// The cluster stays in state with the prior version, so the
				// upgrade is planned again.
				PreConfig:          func() { fake.failTasks("UPGRADE_PATCH_VERSION", false) },
				Config:             config("1.30.5", `"NodeRestriction"`),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{
				Config: config("1.30.5", `"NodeRestriction"`),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(testMKSClusterV2Name, "kube_version", "1.30.5"),
					// Upgraded in place, not recreated.
					testMKSClusterV2Calls(fake, map[string]int{mksV2RouteUpgradePatch: 2, mksV2RouteCreateCluster: 1}),
				),
			},
			{
				PreConfig:   func() { fake.failTasks("UPGRADE_MASTERS_CONFIG", true) },
				Config:      config("1.30.5", `"NodeRestriction", "PodNodeSelector"`),
				ExpectError: testMKSClusterV2Error(`task UPGRADE_MASTERS_CONFIG task-\d+ of cluster ` + testMKSV2ClusterID + ` ended in ERROR`),
			},
			{
				// The cluster stays in state. The API stored the requested
				// options before the task ran, so the refresh reads them and
				// the plan is empty.
				PreConfig: func() { fake.failTasks("UPGRADE_MASTERS_CONFIG", false) },
				Config:    config("1.30.5", `"NodeRestriction", "PodNodeSelector"`),
				PlanOnly:  true,
			},
		},
	})
}

func TestMKSV2TaskWaiterListsAllTasks(t *testing.T) {
	fake := newMKSV2Fake(t)
	fake.seedCluster(mksclient.ClusterDetailed{Id: testMKSV2ClusterID}, "")
	client, err := newMKSV2ServiceClient("fake-token", fake.server.URL, "test")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	// More tasks than one page of the old paging; every old one is stuck, so
	// the wait times out if the snapshot misses any of them.
	const oldTasks = 250
	for range oldTasks {
		fake.seedTask(testMKSV2ClusterID, "", "OLD", true)
	}
	waiter, err := newMKSV2TaskWaiter(ctx, client, testMKSV2ClusterID, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(waiter.before) != oldTasks {
		t.Fatalf("the snapshot has %d tasks, want %d", len(waiter.before), oldTasks)
	}
	fake.seedTask(testMKSV2ClusterID, "", "NEW", false)

	err = waiter.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait() = %v, want nil", err)
	}
	// One call per listing: the snapshot and the new tasks.
	calls, query := fake.callCount(mksV2RouteTasks), fake.lastQuery(mksV2RouteTasks)
	if calls != 2 || !strings.Contains(query, "limit=0") {
		t.Errorf("task list calls %d with query %q, want 2 with limit=0", calls, query)
	}
}

// testMKSClusterV2Calls checks how many requests the fake got per route.
func testMKSClusterV2Calls(fake *mksV2Fake, want map[string]int) resource.TestCheckFunc {
	return func(_ *terraform.State) error {
		for route, count := range want {
			got := fake.callCount(route)
			if got != count {
				return fmt.Errorf("%s got %d requests, want %d", route, got, count)
			}
		}

		return nil
	}
}

// testMKSClusterV2Destroyed checks that destroy waited until the cluster was
// gone.
func testMKSClusterV2Destroyed(fake *mksV2Fake) resource.TestCheckFunc {
	return func(_ *terraform.State) error {
		count := fake.clusterCount()
		if count != 0 {
			return fmt.Errorf("the fake still has %d clusters after destroy", count)
		}

		return nil
	}
}

// testMKSClusterV2BodyFields checks fields of a request body, or of its
// "cluster" object when it has one, and that the absent ones were not sent.
func testMKSClusterV2BodyFields(body map[string]any, want map[string]any, absent ...string) error {
	cluster, ok := body["cluster"].(map[string]any)
	if ok {
		body = cluster
	}
	for key, value := range want {
		got, ok := body[key]
		if !ok || got != value {
			return fmt.Errorf("request sent %s = %v (present %t), want %v", key, got, ok, value)
		}
	}
	for _, key := range absent {
		got, ok := body[key]
		if ok {
			return fmt.Errorf("request sent %s = %v, want it omitted", key, got)
		}
	}

	return nil
}

// testMKSClusterV2Error matches pattern in a diagnostic Terraform may have
// wrapped at any space.
func testMKSClusterV2Error(pattern string) *regexp.Regexp {
	return regexp.MustCompile(strings.ReplaceAll(pattern, " ", `\s+`))
}

// testMKSClusterV2ProviderConfig sets every provider argument, so a test reads
// nothing from the environment and needs no t.Setenv, which t.Parallel
// forbids.
func testMKSClusterV2ProviderConfig(projectID, region string) string {
	return fmt.Sprintf(`
provider "selectel" {
  project_id  = %q
  region      = %q
  auth_url    = "test"
  auth_region = "test"
  domain_name = "test"
  username    = "test"
  password    = "test"
}
`, projectID, region)
}

// testMKSClusterV2Config is the cluster with providerConfig, or with a
// provider without project and region when it is empty.
func testMKSClusterV2Config(providerConfig, attributes string) string {
	if providerConfig == "" {
		providerConfig = testMKSClusterV2ProviderConfig("", "")
	}

	return fmt.Sprintf(`
%s

resource "selectel_mks_cluster_v2" "cluster_tf_test_1" {
  name = "tf-v2"
  pool = %q
%s
}
`, providerConfig, testMKSV2Pool, attributes)
}
