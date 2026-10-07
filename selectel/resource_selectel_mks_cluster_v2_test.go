package selectel

import (
	"context"
	"errors"
	"fmt"
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
	useMKSV2TestConfig(t)
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
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
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
					resource.TestCheckResourceAttr(testMKSClusterV2Name, "kubernetes_options.oidc.username_claim", ""),
					resource.TestCheckNoResourceAttr(testMKSClusterV2Name, "basic"),
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

func TestMKSClusterV2ResourceWorkersType(t *testing.T) {
	tests := []struct {
		workersType string
		networkType string
	}{
		{workersType: "CLOUD", networkType: "STANDARD"},
		{workersType: "DEDICATED", networkType: "L3VPN"},
	}

	for _, tt := range tests {
		t.Run(tt.workersType, func(t *testing.T) {
			useMKSV2TestConfig(t)
			fake := newMKSV2Fake(t)

			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
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

func TestMKSClusterV2ResourceCreateTaskError(t *testing.T) {
	useMKSV2TestConfig(t)
	fake := newMKSV2Fake(t)
	fake.failTasks("CREATE_CLUSTER", true)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
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

func TestMKSClusterV2ResourceKubernetesOptionsSentWhole(t *testing.T) {
	useMKSV2TestConfig(t)
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
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
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
	useMKSV2TestConfig(t)
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
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
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
	useMKSV2TestConfig(t)
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
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
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
	useMKSV2TestConfig(t)
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
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
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
				Config:      config("1.30.5"),
				PlanOnly:    true,
				ExpectError: testMKSClusterV2Error(`current version 1\.31\.4 can't be downgraded to version 1\.30\.5`),
			},
			{
				Config:      config("1.33.1"),
				PlanOnly:    true,
				ExpectError: testMKSClusterV2Error(`must be upgraded one minor version at a time`),
			},
		},
	})
}

func TestMKSClusterV2ResourceImport(t *testing.T) {
	useMKSV2TestConfig(t)
	t.Setenv("INFRA_REGION", "")
	fake := newMKSV2Fake(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testMKSClusterV2Destroyed(fake),
		Steps: []resource.TestStep{
			{
				Config: testMKSClusterV2Config(testMKSClusterV2ProviderConfig("provider-project", testMKSV2Pool), `
  kube_version      = "1.30.3"
  workers_type      = "DEDICATED"
  cloud_subnet_cidr = "10.10.0.0/16"
  kubernetes_options = {
    feature_gates        = ["TopologyAwareHints"]
    x509_ca_certificates = "Y2VydA=="
  }
`),
				Check: resource.TestCheckResourceAttr(testMKSClusterV2Name, "project_id", "provider-project"),
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

	fake.checkClients(t, "provider-project")
}

func TestMKSClusterV2ResourceImportNeedsProviderConfig(t *testing.T) {
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
			useMKSV2TestConfig(t)
			t.Setenv("INFRA_REGION", "")
			newMKSV2Fake(t)

			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
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
	useMKSV2TestConfig(t)
	fake := newMKSV2Fake(t)

	config := testMKSClusterV2Config("", `
  project_id   = "attribute-project"
  kube_version = "1.30.3"
  workers_type = "CLOUD"
`)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
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
	useMKSV2TestConfig(t)
	fake := newMKSV2Fake(t)

	config := testMKSClusterV2Config("", `
  project_id   = "attribute-project"
  kube_version = "1.30.3"
  workers_type = "CLOUD"
`)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
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
		{name: "other minor is drift", prior: "1.30.5", actual: "1.31.1", want: "1.31.1"},
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

func testMKSClusterV2ProviderConfig(projectID, region string) string {
	return fmt.Sprintf(`
provider "selectel" {
  project_id = %q
  region     = %q
}
`, projectID, region)
}

func testMKSClusterV2Config(providerConfig, attributes string) string {
	return fmt.Sprintf(`
%s

resource "selectel_mks_cluster_v2" "cluster_tf_test_1" {
  name = "tf-v2"
  pool = %q
%s
}
`, providerConfig, testMKSV2Pool, attributes)
}
