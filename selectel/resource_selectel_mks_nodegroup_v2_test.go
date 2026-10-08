package selectel

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/resource"
	"github.com/hashicorp/terraform-plugin-sdk/v2/terraform"
	dedicated "github.com/selectel/dedicated-go/v2/pkg/v2"
	mksv2 "github.com/selectel/mks-go/v2/pkg"
	"github.com/selectel/mks-go/v2/pkg/mksclient"
)

const (
	testMKSNodegroupV2Name = "selectel_mks_nodegroup_v2.nodegroup_tf_test_1"
	testMKSNodegroupV2ID   = testMKSV2ClusterID + "/ng-1"
)

func TestMKSNodegroupV2ResourceBasic(t *testing.T) {
	t.Parallel()
	fake := newMKSV2Fake(t)
	testMKSNodegroupV2SeedCluster(fake, mksclient.ClusterDetailedNetworkTypeSTANDARD)
	// The new nodegroup is found by the list difference, and its operations
	// must not wait for tasks out of their scope; if they did, the short
	// create timeout would fail the apply.
	fake.seedNodegroup(mksclient.NodegroupDetailed{Id: "ng-existing", ClusterId: testMKSV2ClusterID, Segment: "ru-7b"})
	fake.seedTask(testMKSV2ClusterID, "", "UPGRADE_MASTERS_CONFIG", true)
	fake.seedTask(testMKSV2ClusterID, "ng-existing", "NODE_GROUP_RESIZE", true)

	config := testMKSNodegroupV2Config(`
  nodes_count = 2
  cloud_nodegroup_config = {
    cpus            = 2
    ram_mb          = 4096
    volume_gb       = 20
    volume_type     = "fast.ru-7a"
    affinity_policy = "soft-anti-affinity"
  }
  labels = {
    env = "test"
  }
  taints = [{
    key    = "dedicated"
    value  = "db"
    effect = "NoSchedule"
  }]
  install_nvidia_device_plugin = false
  timeouts {
    create = "5s"
  }
`)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: fake.providerFactories(),
		CheckDestroy: func(_ *terraform.State) error {
			if fake.hasNodegroup("ng-1") || !fake.hasNodegroup("ng-existing") {
				return fmt.Errorf("destroy must remove ng-1 and keep ng-existing")
			}

			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(testMKSNodegroupV2Name, "id", testMKSNodegroupV2ID),
					resource.TestCheckResourceAttr(testMKSNodegroupV2Name, "cluster_id", testMKSV2ClusterID),
					resource.TestCheckResourceAttr(testMKSNodegroupV2Name, "segment", "ru-7a"),
					resource.TestCheckResourceAttr(testMKSNodegroupV2Name, "nodes_count", "2"),
					resource.TestCheckResourceAttr(testMKSNodegroupV2Name, "nodes.#", "2"),
					resource.TestCheckResourceAttr(testMKSNodegroupV2Name, "nodes.1.ip", "198.51.100.2"),
					resource.TestCheckResourceAttr(testMKSNodegroupV2Name, "nodes.1.hostname", "ng-1-node-2"),
					resource.TestCheckResourceAttr(testMKSNodegroupV2Name, "status", "ACTIVE"),
					resource.TestCheckResourceAttr(testMKSNodegroupV2Name, "nodegroup_type", "STANDARD"),
					resource.TestCheckNoResourceAttr(testMKSNodegroupV2Name, "cidr"),
					resource.TestCheckResourceAttr(testMKSNodegroupV2Name, "labels.env", "test"),
					resource.TestCheckResourceAttr(testMKSNodegroupV2Name, "taints.0.effect", "NoSchedule"),
					resource.TestCheckResourceAttr(testMKSNodegroupV2Name, "enable_autoscale", "false"),
					resource.TestCheckResourceAttr(testMKSNodegroupV2Name, "autoscale_min_nodes", "0"),
					resource.TestCheckResourceAttr(testMKSNodegroupV2Name, "preemptible", "false"),
					resource.TestCheckResourceAttr(testMKSNodegroupV2Name, "user_data", ""),
					resource.TestCheckResourceAttr(testMKSNodegroupV2Name, "cloud_nodegroup_config.flavor_id", "fake-flavor"),
					resource.TestCheckResourceAttr(testMKSNodegroupV2Name, "cloud_nodegroup_config.cpus", "2"),
					resource.TestCheckResourceAttr(testMKSNodegroupV2Name, "cloud_nodegroup_config.local_volume", "false"),
					func(_ *terraform.State) error {
						body := testMKSNodegroupV2CreateBody(t, fake)
						err := testMKSClusterV2BodyFields(body, map[string]any{
							"count": float64(2), "segment": "ru-7a", "install_nvidia_device_plugin": false,
						}, "enable_autoscale", "dedicated_nodegroup_config", "cidr")
						if err != nil {
							return err
						}

						cloud, _ := body["cloud_nodegroup_config"].(map[string]any)

						return testMKSClusterV2BodyFields(cloud, map[string]any{
							"cpus": float64(2), "ram_mb": float64(4096), "volume_gb": float64(20), "volume_type": "fast.ru-7a",
						}, "flavor_id", "local_volume")
					},
				),
			},
			{
				Config:   config,
				PlanOnly: true,
			},
		},
	})

	fake.checkClients(t, "provider-project")
	for _, pool := range fake.clientPools() {
		if pool != testMKSV2Pool {
			t.Errorf("client built for pool %q, want %q from the segment", pool, testMKSV2Pool)
		}
	}
}

func TestMKSNodegroupV2ResourceCreateTaskError(t *testing.T) {
	t.Parallel()
	fake := newMKSV2Fake(t)
	testMKSNodegroupV2SeedCluster(fake, mksclient.ClusterDetailedNetworkTypeSTANDARD)
	fake.failTasks("CLUSTER_RESIZE", true)

	config := testMKSNodegroupV2Config(testMKSNodegroupV2Flavor)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: fake.providerFactories(),
		CheckDestroy:             testMKSNodegroupV2Destroyed(fake, "ng-2"),
		Steps: []resource.TestStep{
			{
				Config: config,
				ExpectError: testMKSClusterV2Error(`task CLUSTER_RESIZE task-1 of cluster ` + testMKSV2ClusterID +
					` ended in ERROR: fake failure \(code 42\): fake details of CLUSTER_RESIZE`),
			},
			{
				// The failed nodegroup was saved tainted, so it is replaced.
				PreConfig: func() { fake.failTasks("CLUSTER_RESIZE", false) },
				Config:    config,
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(testMKSNodegroupV2Name, "id", testMKSV2ClusterID+"/ng-2"),
					func(_ *terraform.State) error {
						if fake.hasNodegroup("ng-1") {
							return fmt.Errorf("the tainted ng-1 was not deleted")
						}

						return nil
					},
				),
			},
		},
	})
}

func TestMKSNodegroupV2ResourceCreateTimeout(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// readFails makes the read after the wait fail too.
		readFails bool
		wantError string
	}{
		{name: "read after the wait succeeds", wantError: "Error waiting for the node group to become ready"},
		{name: "read after the wait fails", readFails: true, wantError: "Error reading node group"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fake := newMKSV2Fake(t)
			testMKSNodegroupV2SeedCluster(fake, mksclient.ClusterDetailedNetworkTypeSTANDARD)
			fake.stickTasks("CLUSTER_RESIZE", true)
			if tt.readFails {
				fake.fail(mksV2RouteNodegroup, http.StatusInternalServerError)
			}

			config := testMKSNodegroupV2Config(testMKSNodegroupV2Flavor + `
  timeouts {
    create = "1s"
    delete = "10s"
  }
`)
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: fake.providerFactories(),
				CheckDestroy:             testMKSNodegroupV2Destroyed(fake, "ng-2"),
				Steps: []resource.TestStep{
					{
						Config:      config,
						ExpectError: testMKSClusterV2Error(tt.wantError + `(?s:.*)context deadline exceeded`),
					},
					{
						// The timed-out nodegroup is in state, tainted: the
						// next apply deletes it and creates a new one.
						PreConfig: func() {
							calls := fake.callCount(mksV2RouteCreateNodegroups)
							if calls != 1 {
								t.Errorf("the failed apply sent %d create requests, want 1", calls)
							}
							fake.stickTasks("CLUSTER_RESIZE", false)
							fake.fail(mksV2RouteNodegroup, 0)
						},
						Config: config,
						Check: resource.ComposeTestCheckFunc(
							resource.TestCheckResourceAttr(testMKSNodegroupV2Name, "id", testMKSV2ClusterID+"/ng-2"),
							testMKSClusterV2Calls(fake, map[string]int{mksV2RouteCreateNodegroups: 2, mksV2RouteDeleteNodegroup: 1}),
							func(_ *terraform.State) error {
								if fake.hasNodegroup("ng-1") {
									return fmt.Errorf("the tainted ng-1 was not deleted")
								}

								return nil
							},
						),
					},
				},
			})
		})
	}
}

func TestMKSNodegroupV2ResourceConfigValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		attributes string
		wantError  string
	}{
		{
			name:       "flavor_id with cpus",
			attributes: "nodes_count = 1\ncloud_nodegroup_config = {\nflavor_id = \"1013\"\ncpus = 2\n}",
			wantError:  `"cloud_nodegroup_config.flavor_id" cannot be specified when "cloud_nodegroup_config.cpus" is specified`,
		},
		{
			name:       "flavor_id with ram_mb",
			attributes: "nodes_count = 1\ncloud_nodegroup_config = {\nflavor_id = \"1013\"\nram_mb = 4096\n}",
			wantError:  `"cloud_nodegroup_config.flavor_id" cannot be specified when "cloud_nodegroup_config.ram_mb" is specified`,
		},
		{
			name:       "cpus without ram_mb",
			attributes: "nodes_count = 1\ncloud_nodegroup_config = {\ncpus = 2\nvolume_gb = 20\nvolume_type = \"fast.ru-7a\"\n}",
			wantError:  `"cloud_nodegroup_config.ram_mb" must be specified when "cloud_nodegroup_config.cpus" is specified`,
		},
		{
			name:       "ram_mb without cpus",
			attributes: "nodes_count = 1\ncloud_nodegroup_config = {\nram_mb = 4096\nvolume_gb = 20\nvolume_type = \"fast.ru-7a\"\n}",
			wantError:  `"cloud_nodegroup_config.cpus" must be specified when "cloud_nodegroup_config.ram_mb" is specified`,
		},
		{
			name:       "neither flavor_id nor cpus",
			attributes: "nodes_count = 1\ncloud_nodegroup_config = {\nvolume_type = \"fast.ru-7a\"\n}",
			wantError:  `Set either flavor_id, or cpus and ram_mb`,
		},
		{
			name:       "local volume with volume_type, issue 273",
			attributes: "nodes_count = 1\ncloud_nodegroup_config = {\nflavor_id = \"1013\"\nlocal_volume = true\nvolume_type = \"fast.ru-7a\"\n}",
			wantError:  `volume_type sets a network volume and cannot be used with local_volume = true`,
		},
		{
			name:       "local volume flavor with volume_gb, issue 273",
			attributes: "nodes_count = 1\ncloud_nodegroup_config = {\nflavor_id = \"1013\"\nlocal_volume = true\nvolume_gb = 50\n}",
			wantError:  `volume_gb cannot be used with flavor_id and local_volume = true`,
		},
		{
			name:       "cidr on a cloud node group",
			attributes: "nodes_count = 1\ncidr = \"10.20.0.0/24\"\ncloud_nodegroup_config = {\nflavor_id = \"1013\"\n}",
			wantError:  `cidr applies to dedicated node groups only`,
		},
		{
			name:       "NVIDIA device plugin with cpus",
			attributes: "nodes_count = 1\ninstall_nvidia_device_plugin = true\ncloud_nodegroup_config = {\ncpus = 2\nram_mb = 4096\nvolume_gb = 20\nvolume_type = \"fast.ru-7a\"\n}",
			wantError:  `install_nvidia_device_plugin = true needs a GPU flavor`,
		},
		{
			name:       "autoscaling without autoscale_min_nodes",
			attributes: "enable_autoscale = true\nautoscale_max_nodes = 3\ncloud_nodegroup_config = {\nflavor_id = \"1013\"\n}",
			wantError:  `enable_autoscale = true requires both autoscale_min_nodes and autoscale_max_nodes`,
		},
		{
			name:       "autoscaling without autoscale_max_nodes",
			attributes: "nodes_count = 1\nenable_autoscale = true\nautoscale_min_nodes = 1\ncloud_nodegroup_config = {\nflavor_id = \"1013\"\n}",
			wantError:  `enable_autoscale = true requires both autoscale_min_nodes and autoscale_max_nodes`,
		},
		{
			name:       "NVIDIA device plugin with a flavor",
			attributes: "nodes_count = 1\ninstall_nvidia_device_plugin = true\ncloud_nodegroup_config = {\nflavor_id = \"1013\"\n}",
		},
		{
			name:       "no count without autoscaling",
			attributes: "cloud_nodegroup_config = {\nflavor_id = \"1013\"\n}",
			wantError:  `Set nodes_count, or enable autoscaling`,
		},
		{
			name:       "network volume with local_volume = false, issue 300",
			attributes: "nodes_count = 1\ncloud_nodegroup_config = {\ncpus = 2\nram_mb = 4096\nvolume_gb = 50\nvolume_type = \"fast.ru-7b\"\nlocal_volume = false\n}",
		},
		{
			name:       "local volume of a custom flavor needs volume_gb",
			attributes: "nodes_count = 1\ncloud_nodegroup_config = {\ncpus = 2\nram_mb = 4096\nvolume_gb = 50\nlocal_volume = true\n}",
		},
		{
			name:       "flavor with a local volume",
			attributes: "nodes_count = 1\ncloud_nodegroup_config = {\nflavor_id = \"1013\"\nlocal_volume = true\n}",
		},
		{
			name:       "no count with autoscaling",
			attributes: "enable_autoscale = true\nautoscale_min_nodes = 1\nautoscale_max_nodes = 3\ncloud_nodegroup_config = {\nflavor_id = \"1013\"\n}",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fake := newMKSV2Fake(t)
			testMKSNodegroupV2SeedCluster(fake, mksclient.ClusterDetailedNetworkTypeSTANDARD)

			step := resource.TestStep{
				Config:             testMKSNodegroupV2Config(tt.attributes),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			}
			if tt.wantError != "" {
				step.ExpectError = testMKSClusterV2Error(tt.wantError)
			}
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: fake.providerFactories(),
				Steps:                    []resource.TestStep{step},
			})
		})
	}
}

func TestMKSNodegroupV2ResourceDedicatedCluster(t *testing.T) {
	const wantError = `cluster ` + testMKSV2ClusterID + ` has workers_type = DEDICATED and accepts only dedicated node groups`

	t.Run("existing cluster fails at plan", func(t *testing.T) {
		t.Parallel()
		fake := newMKSV2Fake(t)
		testMKSNodegroupV2SeedCluster(fake, mksclient.ClusterDetailedNetworkTypeL3VPN)

		resource.UnitTest(t, resource.TestCase{
			ProtoV6ProviderFactories: fake.providerFactories(),
			Steps: []resource.TestStep{
				{
					Config:      testMKSNodegroupV2Config(testMKSNodegroupV2Flavor),
					PlanOnly:    true,
					ExpectError: testMKSClusterV2Error(wantError),
				},
			},
		})
		testMKSNodegroupV2NoCreate(t, fake)
	})

	t.Run("cluster in the same apply fails before any create", func(t *testing.T) {
		t.Parallel()
		fake := newMKSV2Fake(t)

		config := testMKSClusterV2Config(testMKSClusterV2ProviderConfig("provider-project", ""), `
  kube_version = "1.30.3"
  workers_type = "DEDICATED"
`) + `
resource "selectel_mks_nodegroup_v2" "nodegroup_tf_test_1" {
  cluster_id = selectel_mks_cluster_v2.cluster_tf_test_1.id
  segment    = "ru-7a"
` + testMKSNodegroupV2Flavor + `
}
`
		resource.UnitTest(t, resource.TestCase{
			ProtoV6ProviderFactories: fake.providerFactories(),
			CheckDestroy:             testMKSClusterV2Destroyed(fake),
			Steps: []resource.TestStep{
				{
					Config:      config,
					ExpectError: testMKSClusterV2Error(wantError),
				},
			},
		})
		testMKSNodegroupV2NoCreate(t, fake)
	})
}

func TestMKSNodegroupV2ResourceInPlaceUpdate(t *testing.T) {
	t.Parallel()
	fake := newMKSV2Fake(t)
	testMKSNodegroupV2SeedCluster(fake, mksclient.ClusterDetailedNetworkTypeSTANDARD)

	config := func(attributes string) string {
		return testMKSNodegroupV2Config(testMKSNodegroupV2Flavor + attributes)
	}
	patched := func(want map[string]any, absent ...string) resource.TestCheckFunc {
		return func(_ *terraform.State) error {
			body, _ := fake.lastBody(t, mksV2RoutePatchNodegroup)["nodegroup"].(map[string]any)
			for key, value := range want {
				if fmt.Sprint(body[key]) != fmt.Sprint(value) {
					return fmt.Errorf("PATCH sent %s = %v, want %v", key, body[key], value)
				}
			}

			return testMKSClusterV2BodyFields(body, nil, absent...)
		}
	}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: fake.providerFactories(),
		CheckDestroy:             testMKSNodegroupV2Destroyed(fake, "ng-1"),
		Steps: []resource.TestStep{
			{
				Config: config(`
  labels = { a = "1", b = "2" }
  taints = [{ key = "k1", value = "v1", effect = "NoSchedule" }]
`),
			},
			{
				// The fake applies labels and taints only when their tasks
				// finish, so the state proves the provider waited.
				Config: config(`
  labels = { a = "1", c = "3" }
  taints = [{ key = "k1", value = "v1", effect = "NoSchedule" }, { key = "k2", value = "v2", effect = "NoExecute" }]
`),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(testMKSNodegroupV2Name, "labels.%", "2"),
					resource.TestCheckResourceAttr(testMKSNodegroupV2Name, "labels.c", "3"),
					resource.TestCheckResourceAttr(testMKSNodegroupV2Name, "taints.#", "2"),
					resource.TestCheckResourceAttr(testMKSNodegroupV2Name, "taints.1.effect", "NoExecute"),
					patched(map[string]any{
						"labels": map[string]any{"a": "1", "c": "3"},
						"taints": []any{
							map[string]any{"key": "k1", "value": "v1", "effect": "NoSchedule"},
							map[string]any{"key": "k2", "value": "v2", "effect": "NoExecute"},
						},
					}, "enable_autoscale", "autoscale_min_nodes", "autoscale_max_nodes"),
				),
			},
			{
				// Only the autoscale fields change, and they go together.
				Config: config(`
  labels = { a = "1", c = "3" }
  taints = [{ key = "k1", value = "v1", effect = "NoSchedule" }, { key = "k2", value = "v2", effect = "NoExecute" }]
  enable_autoscale    = true
  autoscale_min_nodes = 1
  autoscale_max_nodes = 3
`),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(testMKSNodegroupV2Name, "enable_autoscale", "true"),
					resource.TestCheckResourceAttr(testMKSNodegroupV2Name, "autoscale_max_nodes", "3"),
					patched(map[string]any{"enable_autoscale": true, "autoscale_min_nodes": 1, "autoscale_max_nodes": 3},
						"labels", "taints"),
					func(_ *terraform.State) error {
						patches, resizes := fake.callCount(mksV2RoutePatchNodegroup), fake.callCount(mksV2RouteResizeNodegroup)
						if patches != 2 || resizes != 0 {
							return fmt.Errorf("PATCH %d and resize %d calls, want 2 and 0", patches, resizes)
						}

						return nil
					},
				),
			},
		},
	})
}

func TestMKSNodegroupV2ResourceResize(t *testing.T) {
	t.Parallel()
	fake := newMKSV2Fake(t)
	testMKSNodegroupV2SeedCluster(fake, mksclient.ClusterDetailedNetworkTypeSTANDARD)

	config := func(count int) string {
		return testMKSNodegroupV2Config(fmt.Sprintf(`
  nodes_count = %d
  cloud_nodegroup_config = {
    flavor_id = "1013"
  }
`, count))
	}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: fake.providerFactories(),
		CheckDestroy:             testMKSNodegroupV2Destroyed(fake, "ng-1"),
		Steps: []resource.TestStep{
			{
				Config: config(2),
			},
			{
				// The fake adds the nodes only when NODE_GROUP_RESIZE
				// finishes, so nodes.# proves the provider waited.
				Config: config(3),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(testMKSNodegroupV2Name, "id", testMKSNodegroupV2ID),
					resource.TestCheckResourceAttr(testMKSNodegroupV2Name, "nodes_count", "3"),
					resource.TestCheckResourceAttr(testMKSNodegroupV2Name, "nodes.#", "3"),
					func(_ *terraform.State) error {
						err := testMKSClusterV2BodyFields(fake.lastBody(t, mksV2RouteResizeNodegroup)["nodegroup"].(map[string]any),
							map[string]any{"desired": float64(3)})
						if err != nil {
							return err
						}
						if fake.callCount(mksV2RoutePatchNodegroup) != 0 {
							return fmt.Errorf("a count change sent a PATCH")
						}

						return nil
					},
				),
			},
			{
				// Nodes removed outside are drift with autoscaling off.
				PreConfig: func() {
					fake.updateNodegroup("ng-1", func(ng *mksclient.NodegroupDetailed) { ng.Nodes = mksV2FakeNodes("ng-1", 1) })
				},
				Config:             config(3),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

func TestMKSNodegroupV2ResourceAutoscaleIgnoresCount(t *testing.T) {
	t.Parallel()
	fake := newMKSV2Fake(t)
	testMKSNodegroupV2SeedCluster(fake, mksclient.ClusterDetailedNetworkTypeSTANDARD)

	config := func(count int) string {
		return testMKSNodegroupV2Config(fmt.Sprintf(`
  nodes_count               = %d
  enable_autoscale    = true
  autoscale_min_nodes = 1
  autoscale_max_nodes = 5
  cloud_nodegroup_config = {
    flavor_id = "1013"
  }
`, count))
	}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: fake.providerFactories(),
		CheckDestroy:             testMKSNodegroupV2Destroyed(fake, "ng-1"),
		Steps: []resource.TestStep{
			{
				Config: config(2),
				Check:  resource.TestCheckResourceAttr(testMKSNodegroupV2Name, "nodes_count", "2"),
			},
			{
				// The autoscaler adds nodes.
				PreConfig: func() {
					fake.updateNodegroup("ng-1", func(ng *mksclient.NodegroupDetailed) { ng.Nodes = mksV2FakeNodes("ng-1", 4) })
				},
				Config:   config(2),
				PlanOnly: true,
			},
			{
				Config:   config(3),
				PlanOnly: true,
			},
			{
				Config: config(3),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(testMKSNodegroupV2Name, "nodes_count", "4"),
					func(_ *terraform.State) error {
						if fake.callCount(mksV2RouteResizeNodegroup) != 0 {
							return fmt.Errorf("a count change with autoscaling on sent a resize")
						}

						return nil
					},
				),
			},
		},
	})
}

func TestMKSNodegroupV2ResourceStatusFromAPI(t *testing.T) {
	t.Parallel()
	fake := newMKSV2Fake(t)
	testMKSNodegroupV2SeedCluster(fake, mksclient.ClusterDetailedNetworkTypeSTANDARD)

	status := func(status mksclient.NodegroupDetailedStatus) resource.TestStep {
		return resource.TestStep{
			PreConfig: func() {
				fake.updateNodegroup("ng-1", func(ng *mksclient.NodegroupDetailed) { ng.Status = status })
			},
			RefreshState: true,
			Check:        resource.TestCheckResourceAttr(testMKSNodegroupV2Name, "status", string(status)),
		}
	}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: fake.providerFactories(),
		CheckDestroy:             testMKSNodegroupV2Destroyed(fake, "ng-1"),
		Steps: []resource.TestStep{
			{
				Config: testMKSNodegroupV2Config(testMKSNodegroupV2Flavor),
			},
			status(mksclient.NodegroupDetailedStatusPENDINGNODEREINSTALL),
			// A status the client does not know yet passes through as well.
			status("PENDING_SOMETHING_NEW"),
		},
	})
}

func TestMKSNodegroupV2ResourceImport(t *testing.T) {
	t.Parallel()
	fake := newMKSV2Fake(t)
	testMKSNodegroupV2SeedCluster(fake, mksclient.ClusterDetailedNetworkTypeSTANDARD)

	config := testMKSClusterV2ProviderConfig("provider-project", testMKSV2Pool) + testMKSNodegroupV2Resource(`
  nodes_count  = 2
  labels = { a = "1" }
  cloud_nodegroup_config = {
    cpus        = 2
    ram_mb      = 4096
    volume_gb   = 20
    volume_type = "fast.ru-7a"
  }
`)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: fake.providerFactories(),
		CheckDestroy:             testMKSNodegroupV2Destroyed(fake, "ng-1"),
		Steps: []resource.TestStep{
			{
				Config: config,
			},
			{
				ResourceName:      testMKSNodegroupV2Name,
				ImportState:       true,
				ImportStateVerify: true,
				// The API never returns them.
				ImportStateVerifyIgnore: []string{"cloud_nodegroup_config.cpus", "cloud_nodegroup_config.ram_mb"},
			},
		},
	})

	fake.checkClients(t, "provider-project")
}

func TestMKSNodegroupV2ResourceImportSetsConfiguredValues(t *testing.T) {
	t.Parallel()
	fake := newMKSV2Fake(t)
	testMKSNodegroupV2SeedCluster(fake, mksclient.ClusterDetailedNetworkTypeSTANDARD)
	fake.seedNodegroup(mksclient.NodegroupDetailed{
		Id: "ng-1", ClusterId: testMKSV2ClusterID, Segment: "ru-7a", Status: "ACTIVE",
		Nodes:                mksV2FakeNodes("ng-1", 1),
		CloudNodegroupConfig: &mksclient.CloudNodegroupConfigInfo{FlavorId: "fake-flavor", VolumeGb: 20, VolumeType: "fast.ru-7a"},
	})

	config := func(affinityPolicy string) string {
		return testMKSClusterV2ProviderConfig("provider-project", testMKSV2Pool) + testMKSNodegroupV2Resource(`
  nodes_count = 1
  cloud_nodegroup_config = {
    cpus         = 2
    ram_mb       = 4096
    volume_gb    = 20
    volume_type  = "fast.ru-7a"
`+affinityPolicy+`
  }
`)
	}
	calls := func(creates, deletes int) resource.TestCheckFunc {
		return func(_ *terraform.State) error {
			gotCreates, gotDeletes := fake.callCount(mksV2RouteCreateNodegroups), fake.callCount(mksV2RouteDeleteNodegroup)
			if gotCreates != creates || gotDeletes != deletes {
				return fmt.Errorf("nodegroup creates %d and deletes %d, want %d and %d", gotCreates, gotDeletes, creates, deletes)
			}

			return nil
		}
	}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: fake.providerFactories(),
		CheckDestroy:             testMKSNodegroupV2Destroyed(fake, "ng-1"),
		Steps: []resource.TestStep{
			{
				Config:             config(""),
				ResourceName:       testMKSNodegroupV2Name,
				ImportState:        true,
				ImportStateId:      testMKSNodegroupV2ID,
				ImportStatePersist: true,
			},
			{
				// The imported state has none of the values the API never
				// returns; adding them from the configuration must not
				// recreate the nodegroup.
				Config: config(""),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(testMKSNodegroupV2Name, "cloud_nodegroup_config.cpus", "2"),
					resource.TestCheckResourceAttr(testMKSNodegroupV2Name, "cloud_nodegroup_config.ram_mb", "4096"),
					calls(0, 0),
				),
			},
			{
				// That apply cleared the import mark: a value still missing
				// from the state recreates the nodegroup when it is set.
				Config: config(`    affinity_policy = "soft-affinity"`),
				Check:  calls(1, 1),
			},
		},
	})
}

func TestMKSNodegroupV2ResourceAddedAffinityPolicyRecreates(t *testing.T) {
	t.Parallel()
	fake := newMKSV2Fake(t)
	testMKSNodegroupV2SeedCluster(fake, mksclient.ClusterDetailedNetworkTypeSTANDARD)

	config := func(affinityPolicy string) string {
		return testMKSNodegroupV2Config(`
  nodes_count = 1
  cloud_nodegroup_config = {
    flavor_id = "1013"
` + affinityPolicy + `
  }
`)
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: fake.providerFactories(),
		CheckDestroy:             testMKSNodegroupV2Destroyed(fake, "ng-2"),
		Steps: []resource.TestStep{
			{
				Config: config(""),
				Check:  resource.TestCheckNoResourceAttr(testMKSNodegroupV2Name, "cloud_nodegroup_config.affinity_policy"),
			},
			{
				// Without an import, a value missing from the state is a
				// value the nodegroup was created without.
				Config: config(`    affinity_policy = "soft-anti-affinity"`),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(testMKSNodegroupV2Name, "id", testMKSV2ClusterID+"/ng-2"),
					resource.TestCheckResourceAttr(testMKSNodegroupV2Name, "cloud_nodegroup_config.affinity_policy", "soft-anti-affinity"),
				),
			},
		},
	})
}

func TestMKSNodegroupV2ResourceNeedsProviderConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		providerConfig string
		importState    bool
		wantError      string
	}{
		{
			name:           "create without project",
			providerConfig: testMKSClusterV2ProviderConfig("", testMKSV2Pool),
			wantError:      "takes the project from the provider",
		},
		{
			name:           "import without project",
			providerConfig: testMKSClusterV2ProviderConfig("", testMKSV2Pool),
			importState:    true,
			wantError:      "INFRA_PROJECT_ID must be set for the resource import",
		},
		{
			name:           "import without pool",
			providerConfig: testMKSClusterV2ProviderConfig("provider-project", ""),
			importState:    true,
			wantError:      "INFRA_REGION must be set for the resource import",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fake := newMKSV2Fake(t)
			testMKSNodegroupV2SeedCluster(fake, mksclient.ClusterDetailedNetworkTypeSTANDARD)

			step := resource.TestStep{
				Config:      tt.providerConfig + testMKSNodegroupV2Resource(testMKSNodegroupV2Flavor),
				ExpectError: testMKSClusterV2Error(tt.wantError),
			}
			if tt.importState {
				step.ResourceName = testMKSNodegroupV2Name
				step.ImportState = true
				step.ImportStateId = testMKSNodegroupV2ID
			}
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: fake.providerFactories(),
				Steps:                    []resource.TestStep{step},
			})
			testMKSNodegroupV2NoCreate(t, fake)
		})
	}
}

func TestMKSNodegroupV2ResourceRemovedOutside(t *testing.T) {
	t.Parallel()
	fake := newMKSV2Fake(t)
	testMKSNodegroupV2SeedCluster(fake, mksclient.ClusterDetailedNetworkTypeSTANDARD)

	config := testMKSNodegroupV2Config(testMKSNodegroupV2Flavor)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: fake.providerFactories(),
		CheckDestroy:             testMKSNodegroupV2Destroyed(fake, "ng-1"),
		Steps: []resource.TestStep{
			{
				Config: config,
			},
			{
				PreConfig:          func() { fake.removeNodegroup("ng-1") },
				Config:             config,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

func TestMKSNodegroupV2ResourceDeleteTaskError(t *testing.T) {
	t.Parallel()
	fake := newMKSV2Fake(t)
	testMKSNodegroupV2SeedCluster(fake, mksclient.ClusterDetailedNetworkTypeSTANDARD)

	config := testMKSNodegroupV2Config(testMKSNodegroupV2Flavor)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: fake.providerFactories(),
		CheckDestroy:             testMKSNodegroupV2Destroyed(fake, "ng-1"),
		Steps: []resource.TestStep{
			{
				Config: config,
			},
			{
				PreConfig:   func() { fake.failTasks("CLUSTER_RESIZE", true) },
				Config:      config,
				Destroy:     true,
				ExpectError: testMKSClusterV2Error(`task CLUSTER_RESIZE task-\d+ of cluster ` + testMKSV2ClusterID + ` ended in ERROR`),
			},
			{
				// The failed delete left the nodegroup in place.
				PreConfig: func() { fake.failTasks("CLUSTER_RESIZE", false) },
				Config:    config,
				PlanOnly:  true,
			},
		},
	})
}

func TestMKSNodegroupV2Pool(t *testing.T) {
	tests := map[string]string{"ru-3a": "ru-3", "ru-7b": "ru-7", "gis-1a": "gis-1", "ru-3": "ru-3"}

	for segment, want := range tests {
		got := mksNodegroupV2Pool(segment)
		if got != want {
			t.Errorf("mksNodegroupV2Pool(%q) = %q, want %q", segment, got, want)
		}
	}
}

func TestMKSNodegroupV2DefaultTimeout(t *testing.T) {
	tests := []struct {
		name   string
		config types.Object
		want   time.Duration
	}{
		{name: "cloud", config: types.ObjectNull(mksNodegroupV2DedicatedConfigAttrTypes), want: mksClusterV2DefaultTimeout},
		{
			name:   "dedicated",
			config: types.ObjectValueMust(mksNodegroupV2DedicatedConfigAttrTypes, testMKSNodegroupV2DedicatedAttrs()),
			want:   mksNodegroupV2DedicatedCreateTimeout,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := mksNodegroupV2Model{DedicatedNodegroupConfig: tt.config}
			got := m.defaultTimeout()
			if got != tt.want {
				t.Errorf("defaultTimeout() = %s, want %s for create, update and delete", got, tt.want)
			}
		})
	}
}

func testMKSNodegroupV2DedicatedAttrs() map[string]attr.Value {
	return map[string]attr.Value{
		"service_uuid":             types.StringValue(testMKSNodegroupV2ServiceUUID),
		"price_plan_name":          types.StringValue("1 day"),
		"price_plan_uuid":          types.StringValue(testMKSNodegroupV2PricePlanDay),
		"root_size_gb":             types.Int64Value(100),
		"create_storage_partition": types.BoolValue(true),
		"currency":                 types.StringValue("main"),
	}
}

// testMKSNodegroupV2Private is a private state of plain keys.
type testMKSNodegroupV2Private map[string][]byte

func (p testMKSNodegroupV2Private) GetKey(_ context.Context, key string) ([]byte, diag.Diagnostics) {
	return p[key], nil
}

func (p testMKSNodegroupV2Private) SetKey(_ context.Context, key string, value []byte) diag.Diagnostics {
	p[key] = value

	return nil
}

func TestMKSNodegroupV2NodegroupClientPool(t *testing.T) {
	var built []string
	r := &mksNodegroupV2Resource{mksV2Provided{config: &Config{
		ProjectID: "provider-project",
		Region:    "ru-3",
		mksV2Client: func(_ context.Context, config *Config, _, pool string) (*mksv2.ServiceClient, error) {
			built = append(built, pool)

			return newMKSV2ServiceClient("fake-token", "http://127.0.0.1", config.UserAgent)
		},
	}}}

	tests := []struct {
		name    string
		segment string
		private testMKSNodegroupV2Private
		want    string
	}{
		{name: "pool segment", segment: "ru-7a", private: testMKSNodegroupV2Private{mksNodegroupV2PoolKey: []byte(`"ru-9"`)}, want: "ru-7"},
		// Terraform before 1.12 sends no identity, so the saved pool alone
		// keeps a dedicated node group where it was created.
		{name: "saved pool over the provider", segment: "SPB-5", private: testMKSNodegroupV2Private{mksNodegroupV2PoolKey: []byte(`"ru-7"`)}, want: "ru-7"},
		{name: "nothing saved", segment: "SPB-5", private: testMKSNodegroupV2Private{}, want: "ru-3"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			built = nil
			_, pool, diags := r.nodegroupClient(t.Context(), types.StringValue(tt.segment), nil, tt.private)
			if diags.HasError() {
				t.Fatalf("nodegroupClient: %v", diags)
			}
			if pool != tt.want || !slices.Equal(built, []string{tt.want}) {
				t.Errorf("nodegroupClient returned pool %q and built clients for %v, want %q", pool, built, tt.want)
			}
		})
	}
}

func TestMKSNodegroupV2SavePool(t *testing.T) {
	tests := []struct {
		name      string
		dedicated bool
		prior     testMKSNodegroupV2Private
		want      string
	}{
		{name: "dedicated create", dedicated: true, want: `"ru-7"`},
		{name: "dedicated read without a saved pool", dedicated: true, prior: testMKSNodegroupV2Private{}, want: `"ru-7"`},
		{name: "dedicated read keeps the saved pool", dedicated: true, prior: testMKSNodegroupV2Private{mksNodegroupV2PoolKey: []byte(`"ru-9"`)}},
		{name: "cloud", dedicated: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			next := testMKSNodegroupV2Private{}
			var prior mksNodegroupV2PrivateState
			if tt.prior != nil {
				prior = tt.prior
			}
			diags := mksNodegroupV2SavePool(t.Context(), tt.dedicated, "ru-7", prior, next)
			if diags.HasError() {
				t.Fatalf("mksNodegroupV2SavePool: %v", diags)
			}
			got := string(next[mksNodegroupV2PoolKey])
			if got != tt.want {
				t.Errorf("saved pool %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMKSNodegroupV2ResourceDedicatedClusterNotFound(t *testing.T) {
	t.Parallel()
	fake := newMKSV2Fake(t)
	// The cluster is in another pool than the provider one, so mk-api-v2
	// answers 404, as the fake does for a cluster it lacks.
	fake.seedPricePlans(&dedicated.PricePlan{UUID: testMKSNodegroupV2PricePlanDay, Name: "1 day"})

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: fake.providerFactories(),
		Steps: []resource.TestStep{
			{
				Config:      testMKSNodegroupV2DedicatedConfig("  nodes_count = 1\n", ""),
				ExpectError: testMKSClusterV2Error(`looked up in pool ` + testMKSV2Pool + `: a dedicated node group takes the pool from the provider region`),
			},
		},
	})
	testMKSNodegroupV2NoCreate(t, fake)
}

const testMKSNodegroupV2Flavor = `
  nodes_count = 1
  cloud_nodegroup_config = {
    flavor_id = "1013"
  }
`

func testMKSNodegroupV2SeedCluster(fake *mksV2Fake, networkType mksclient.ClusterDetailedNetworkType) {
	fake.seedCluster(mksclient.ClusterDetailed{Id: testMKSV2ClusterID, Pool: testMKSV2Pool, NetworkType: networkType}, "")
}

// testMKSNodegroupV2Config is a nodegroup in the seeded cluster with the
// project of the provider and no provider pool.
func testMKSNodegroupV2Config(attributes string) string {
	return testMKSClusterV2ProviderConfig("provider-project", "") + testMKSNodegroupV2Resource(attributes)
}

func testMKSNodegroupV2Resource(attributes string) string {
	return fmt.Sprintf(`
resource "selectel_mks_nodegroup_v2" "nodegroup_tf_test_1" {
  cluster_id = %q
  segment    = "ru-7a"
%s
}
`, testMKSV2ClusterID, attributes)
}

// testMKSNodegroupV2Destroyed checks that destroy waited until the nodegroup
// was gone.
func testMKSNodegroupV2Destroyed(fake *mksV2Fake, id string) resource.TestCheckFunc {
	return func(_ *terraform.State) error {
		if fake.hasNodegroup(id) {
			return fmt.Errorf("the fake still has nodegroup %s after destroy", id)
		}

		return nil
	}
}

// testMKSNodegroupV2CreateBody returns the only nodegroup of the last create
// request.
func testMKSNodegroupV2CreateBody(t *testing.T, fake *mksV2Fake) map[string]any {
	t.Helper()

	nodegroups, _ := fake.lastBody(t, mksV2RouteCreateNodegroups)["nodegroups"].([]any)
	if len(nodegroups) != 1 {
		t.Fatalf("create request carries %d nodegroups, want 1", len(nodegroups))
	}
	body, _ := nodegroups[0].(map[string]any)

	return body
}

func testMKSNodegroupV2NoCreate(t *testing.T, fake *mksV2Fake) {
	t.Helper()

	calls := fake.callCount(mksV2RouteCreateNodegroups)
	if calls != 0 {
		t.Errorf("the provider sent %d nodegroup create requests, want none", calls)
	}
	if slices.Contains(fake.clientPools(), "") {
		t.Errorf("a client was built without a pool")
	}
}

func TestMKSNodegroupV2ResourceListAfterCreateFails(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// failures is the number of failed lists after the create request.
		failures  int
		wantError string
	}{
		{name: "once, the retry finds it", failures: 1},
		{
			name:     "twice, the error names what is known",
			failures: 2,
			wantError: `the node group was created in cluster ` + testMKSV2ClusterID + `, but listing the node groups failed, ` +
				`so its ID is unknown: it is the node group not among ng-existing; import it`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fake := newMKSV2Fake(t)
			testMKSNodegroupV2SeedCluster(fake, mksclient.ClusterDetailedNetworkTypeSTANDARD)
			fake.seedNodegroup(mksclient.NodegroupDetailed{Id: "ng-existing", ClusterId: testMKSV2ClusterID, Segment: "ru-7b"})

			step := resource.TestStep{
				PreConfig: func() {
					// The list before the create request comes next, then
					// the ones after it.
					next := fake.callCount(mksV2RouteNodegroups) + 2
					calls := []int{next}
					if tt.failures == 2 {
						calls = append(calls, next+1)
					}
					fake.failCalls(mksV2RouteNodegroups, http.StatusInternalServerError, calls...)
				},
				Config: testMKSNodegroupV2Config(testMKSNodegroupV2Flavor),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(testMKSNodegroupV2Name, "id", testMKSNodegroupV2ID),
					resource.TestCheckResourceAttr(testMKSNodegroupV2Name, "status", "ACTIVE"),
				),
			}
			checkDestroy := testMKSNodegroupV2Destroyed(fake, "ng-1")
			if tt.wantError != "" {
				step.Check = nil
				step.ExpectError = testMKSClusterV2Error(tt.wantError)
				// The node group is not in state; the error tells the user
				// to import or delete it.
				checkDestroy = nil
			}
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: fake.providerFactories(),
				CheckDestroy:             checkDestroy,
				Steps:                    []resource.TestStep{step},
			})

			calls := fake.callCount(mksV2RouteCreateNodegroups)
			if calls != 1 {
				t.Errorf("sent %d create requests, want 1", calls)
			}
		})
	}
}

func TestMKSNodegroupV2ResourceCreatedElsewhere(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		other mksclient.NodegroupDetailed
		// wantError is empty when the create options tell the node groups
		// apart.
		wantError string
	}{
		{
			name:  "other segment",
			other: mksclient.NodegroupDetailed{Id: "ng-other", ClusterId: testMKSV2ClusterID, Segment: "ru-7b"},
		},
		{
			name: "other flavor",
			other: mksclient.NodegroupDetailed{
				Id: "ng-other", ClusterId: testMKSV2ClusterID, Segment: "ru-7a",
				CloudNodegroupConfig: &mksclient.CloudNodegroupConfigInfo{FlavorId: "2026"},
			},
		},
		{
			name: "other labels",
			other: mksclient.NodegroupDetailed{
				Id: "ng-other", ClusterId: testMKSV2ClusterID, Segment: "ru-7a", Labels: map[string]string{"env": "prod"},
				CloudNodegroupConfig: &mksclient.CloudNodegroupConfigInfo{FlavorId: "1013"},
			},
		},
		{
			name: "same options",
			other: mksclient.NodegroupDetailed{
				Id: "ng-other", ClusterId: testMKSV2ClusterID, Segment: "ru-7a", Labels: map[string]string{"env": "test"},
				CloudNodegroupConfig: &mksclient.CloudNodegroupConfigInfo{FlavorId: "1013"},
			},
			wantError: `can't find the created node group in cluster ` + testMKSV2ClusterID +
				`: 2 new node groups listed match it, want 1 \(candidates: ng-1, ng-other\)`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fake := newMKSV2Fake(t)
			testMKSNodegroupV2SeedCluster(fake, mksclient.ClusterDetailedNetworkTypeSTANDARD)
			fake.createConcurrently(tt.other)

			step := resource.TestStep{
				Config: testMKSNodegroupV2Config(testMKSNodegroupV2Flavor + `  labels = { env = "test" }` + "\n"),
				Check:  resource.TestCheckResourceAttr(testMKSNodegroupV2Name, "id", testMKSNodegroupV2ID),
			}
			checkDestroy := testMKSNodegroupV2Destroyed(fake, "ng-1")
			if tt.wantError != "" {
				step.Check = nil
				step.ExpectError = testMKSClusterV2Error(tt.wantError)
				checkDestroy = nil
			}
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: fake.providerFactories(),
				CheckDestroy:             checkDestroy,
				Steps:                    []resource.TestStep{step},
			})

			if !fake.hasNodegroup("ng-other") {
				t.Errorf("the provider deleted the node group created elsewhere")
			}
		})
	}
}

func TestMKSNodegroupV2ResourceCreateErrorAfterStore(t *testing.T) {
	t.Parallel()
	fake := newMKSV2Fake(t)
	testMKSNodegroupV2SeedCluster(fake, mksclient.ClusterDetailedNetworkTypeSTANDARD)
	// Like mk-api-v2 when the task publish fails after the commit.
	fake.failCreateNodegroupsAfterStore(http.StatusInternalServerError)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: fake.providerFactories(),
		Steps: []resource.TestStep{
			{
				Config: testMKSNodegroupV2Config(testMKSNodegroupV2Flavor),
				ExpectError: testMKSClusterV2Error(`fake error 500; cluster ` + testMKSV2ClusterID +
					` now has new node groups ng-1 that may be this one: import it as <cluster_id>/<nodegroup_id> or delete it`),
			},
		},
	})
}

func TestMKSNodegroupV2ResourceCreateUndeclaredStatus(t *testing.T) {
	t.Parallel()
	fake := newMKSV2Fake(t)
	testMKSNodegroupV2SeedCluster(fake, mksclient.ClusterDetailedNetworkTypeSTANDARD)
	// The mk-api-v2 swagger does not declare 403 for the create.
	fake.failWithBody(mksV2RouteCreateNodegroups, http.StatusForbidden, "application/json",
		`{"error":{"message":"access denied for the project"}}`)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: fake.providerFactories(),
		Steps: []resource.TestStep{
			{
				Config:      testMKSNodegroupV2Config(testMKSNodegroupV2Flavor),
				ExpectError: testMKSClusterV2Error(`error creating nodegroup: 403 Forbidden: access denied for the project`),
			},
		},
	})
}

func TestMKSNodegroupV2ResourceFlavorReplacesVolume(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		cloud     string
		wantError string
		// wantLocal is the local_volume the state gets when nothing fails.
		wantLocal string
	}{
		{
			name:      "local disk flavor with local_volume = false",
			cloud:     "flavor_id = \"local-1013\"\nlocal_volume = false",
			wantError: `Flavor local-1013 gives local_volume = true, while the configuration sets false. Omit local_volume and volume_gb`,
		},
		{
			name:      "volume flavor with another volume_gb",
			cloud:     "flavor_id = \"volume-1013\"\nvolume_gb = 20",
			wantError: `Flavor volume-1013 gives volume_gb = 50, while the configuration sets 20. Omit local_volume and volume_gb`,
		},
		{
			name:      "local disk flavor with both omitted",
			cloud:     "flavor_id = \"local-1013\"",
			wantLocal: "true",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fake := newMKSV2Fake(t)
			testMKSNodegroupV2SeedCluster(fake, mksclient.ClusterDetailedNetworkTypeSTANDARD)

			config := testMKSNodegroupV2Config("  nodes_count = 1\n  cloud_nodegroup_config = {\n" + tt.cloud + "\n  }\n")
			step := resource.TestStep{
				Config: config,
				Check:  resource.TestCheckResourceAttr(testMKSNodegroupV2Name, "cloud_nodegroup_config.local_volume", tt.wantLocal),
			}
			if tt.wantError != "" {
				step.Check = nil
				step.ExpectError = testMKSClusterV2Error(tt.wantError)
			}
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: fake.providerFactories(),
				// The failed node group is saved tainted and destroyed.
				CheckDestroy: testMKSNodegroupV2Destroyed(fake, "ng-1"),
				Steps:        []resource.TestStep{step},
			})
		})
	}
}

// testMKSNodegroupV2SeedDedicatedNodegroup seeds dedicated node group ng-1
// with the price plan pricePlanUUID, as created outside Terraform.
func testMKSNodegroupV2SeedDedicatedNodegroup(fake *mksV2Fake, pricePlanUUID string) {
	fake.seedNodegroup(mksclient.NodegroupDetailed{
		Id: "ng-1", ClusterId: testMKSV2ClusterID, Segment: "SPB-5", Status: "ACTIVE",
		NodegroupType:     mksclient.NodegroupDetailedNodegroupTypeDEDICATED,
		AutoscaleMinNodes: new(int64(0)), AutoscaleMaxNodes: new(int64(0)),
		Nodes: mksV2FakeNodes("ng-1", 2),
		Cidr:  new("10.20.30.0/24"),
		DedicatedNodegroupConfig: &mksclient.DedicatedNodegroupConfig{
			ServiceUuid: testMKSNodegroupV2ServiceUUID, PricePlanUuid: pricePlanUUID,
			RootSizeGb: new(int64(100)), CreateStoragePartition: new(true),
		},
	})
}

func TestMKSNodegroupV2ResourceDedicatedImport(t *testing.T) {
	t.Parallel()
	fake := newMKSV2Fake(t)
	testMKSNodegroupV2SeedDedicated(fake)
	testMKSNodegroupV2SeedDedicatedNodegroup(fake, testMKSNodegroupV2PricePlanDay)

	config := testMKSNodegroupV2DedicatedConfig(`
  nodes_count = 2
  cidr        = "10.20.30.0/24"
`, "")
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: fake.providerFactories(),
		CheckDestroy:             testMKSNodegroupV2Destroyed(fake, "ng-1"),
		Steps: []resource.TestStep{
			{
				Config:             config,
				ResourceName:       testMKSNodegroupV2Name,
				ImportState:        true,
				ImportStateId:      testMKSNodegroupV2ID,
				ImportStatePersist: true,
			},
			{
				// Read maps the dedicated configuration back and names the
				// price plan by its UUID, so the plan is empty.
				Config:   config,
				PlanOnly: true,
			},
			{
				Config: config,
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(testMKSNodegroupV2Name, "segment", "SPB-5"),
					resource.TestCheckResourceAttr(testMKSNodegroupV2Name, "nodegroup_type", "DEDICATED"),
					resource.TestCheckNoResourceAttr(testMKSNodegroupV2Name, "cloud_nodegroup_config"),
					resource.TestCheckResourceAttr(testMKSNodegroupV2Name, "dedicated_nodegroup_config.price_plan_name", "1 day"),
					resource.TestCheckResourceAttr(testMKSNodegroupV2Name, "dedicated_nodegroup_config.price_plan_uuid",
						testMKSNodegroupV2PricePlanDay),
					resource.TestCheckResourceAttr(testMKSNodegroupV2Name, "dedicated_nodegroup_config.service_uuid",
						testMKSNodegroupV2ServiceUUID),
					resource.TestCheckResourceAttr(testMKSNodegroupV2Name, "dedicated_nodegroup_config.root_size_gb", "100"),
					testMKSClusterV2Calls(fake, map[string]int{
						mksV2RouteCreateNodegroups: 0, mksV2RouteDeleteNodegroup: 0, mksV2RoutePatchNodegroup: 0,
					}),
				),
			},
		},
	})
}

func TestMKSNodegroupV2ResourceDedicatedImportUnknownPricePlan(t *testing.T) {
	t.Parallel()
	fake := newMKSV2Fake(t)
	testMKSNodegroupV2SeedDedicated(fake)
	// A retired plan, or one outside the public list.
	const retired = "5f4e3d2c-1b0a-4f9e-8d7c-6b5a4f3e2d1c"
	testMKSNodegroupV2SeedDedicatedNodegroup(fake, retired)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: fake.providerFactories(),
		Steps: []resource.TestStep{
			{
				// A null price_plan_name would make the configured one
				// replace the node group, so the import fails instead.
				Config: testMKSNodegroupV2DedicatedConfig(`
  nodes_count = 2
  cidr        = "10.20.30.0/24"
`, ""),
				ResourceName:  testMKSNodegroupV2Name,
				ImportState:   true,
				ImportStateId: testMKSNodegroupV2ID,
				ExpectError:   testMKSClusterV2Error(`price plan ` + retired + ` of the node group is not among the price plans`),
			},
		},
	})
	if !fake.hasNodegroup("ng-1") {
		t.Error("the failed import deleted the node group")
	}
}

func TestMKSNodegroupV2ResourceImportMarkExpires(t *testing.T) {
	t.Parallel()
	fake := newMKSV2Fake(t)
	testMKSNodegroupV2SeedCluster(fake, mksclient.ClusterDetailedNetworkTypeSTANDARD)
	fake.seedNodegroup(mksclient.NodegroupDetailed{
		Id: "ng-1", ClusterId: testMKSV2ClusterID, Segment: "ru-7a", Status: "ACTIVE",
		Nodes:                mksV2FakeNodes("ng-1", 1),
		CloudNodegroupConfig: &mksclient.CloudNodegroupConfigInfo{FlavorId: "1013", VolumeGb: 20, VolumeType: "fast.ru-7a"},
	})

	config := func(affinityPolicy string) string {
		return testMKSClusterV2ProviderConfig("provider-project", testMKSV2Pool) + testMKSNodegroupV2Resource(`
  nodes_count = 1
  cloud_nodegroup_config = {
    flavor_id = "1013"
`+affinityPolicy+`
  }
`)
	}
	calls := func(creates, deletes int) resource.TestCheckFunc {
		return testMKSClusterV2Calls(fake, map[string]int{
			mksV2RouteCreateNodegroups: creates, mksV2RouteDeleteNodegroup: deletes, mksV2RoutePatchNodegroup: 0,
		})
	}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: fake.providerFactories(),
		CheckDestroy:             testMKSNodegroupV2Destroyed(fake, "ng-1"),
		Steps: []resource.TestStep{
			{
				Config:             config(""),
				ResourceName:       testMKSNodegroupV2Name,
				ImportState:        true,
				ImportStateId:      testMKSNodegroupV2ID,
				ImportStatePersist: true,
			},
			{
				// Nothing to update, so no Update clears the mark; the
				// refreshes of this apply and the next one do.
				Config: config(""),
				Check:  calls(0, 0),
			},
			{
				Config: config(""),
				Check:  calls(0, 0),
			},
			{
				// The fake numbers the new node group ng-1 again, so the
				// calls tell the replacement apart.
				Config: config(`    affinity_policy = "soft-affinity"`),
				Check:  calls(1, 1),
			},
		},
	})
}

func TestMKSNodegroupV2ResourceUpdateTaskError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		taskType string
		// before and after are the attributes of the two configurations.
		before, after string
		check         resource.TestCheckFunc
	}{
		{
			name:     "labels",
			taskType: "UPDATE_NODEGROUP_LABELS",
			before:   `labels = { env = "test" }`,
			after:    `labels = { env = "prod" }`,
			check:    resource.TestCheckResourceAttr(testMKSNodegroupV2Name, "labels.env", "prod"),
		},
		{
			name:     "taints",
			taskType: "UPDATE_NODEGROUP_TAINTS",
			before:   `taints = []`,
			after:    `taints = [{ key = "k1", value = "v1", effect = "NoSchedule" }]`,
			check:    resource.TestCheckResourceAttr(testMKSNodegroupV2Name, "taints.0.key", "k1"),
		},
		{
			name:     "resize",
			taskType: "NODE_GROUP_RESIZE",
			before:   ``,
			after:    `nodes_count = 2`,
			check:    resource.TestCheckResourceAttr(testMKSNodegroupV2Name, "nodes.#", "2"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fake := newMKSV2Fake(t)
			testMKSNodegroupV2SeedCluster(fake, mksclient.ClusterDetailedNetworkTypeSTANDARD)

			config := func(attributes string) string {
				count := "  nodes_count = 1\n"
				if tt.name == "resize" && attributes != "" {
					count = ""
				}

				return testMKSNodegroupV2Config(count + "  cloud_nodegroup_config = {\n    flavor_id = \"1013\"\n  }\n  " + attributes + "\n")
			}
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: fake.providerFactories(),
				CheckDestroy:             testMKSNodegroupV2Destroyed(fake, "ng-1"),
				Steps: []resource.TestStep{
					{
						Config: config(tt.before),
					},
					{
						PreConfig: func() { fake.failTasks(tt.taskType, true) },
						Config:    config(tt.after),
						ExpectError: testMKSClusterV2Error(`task ` + tt.taskType + ` task-\d+ of cluster ` +
							testMKSV2ClusterID + ` ended in ERROR`),
					},
					{
						// The state kept the prior values, so the next plan
						// repeats the change.
						Config:             config(tt.after),
						PlanOnly:           true,
						ExpectNonEmptyPlan: true,
					},
					{
						PreConfig: func() { fake.failTasks(tt.taskType, false) },
						Config:    config(tt.after),
						Check:     tt.check,
					},
				},
			})
		})
	}
}

func TestMKSNodegroupV2FakeRejectsSameCountResize(t *testing.T) {
	fake := newMKSV2Fake(t)
	testMKSNodegroupV2SeedCluster(fake, mksclient.ClusterDetailedNetworkTypeSTANDARD)
	fake.seedNodegroup(mksclient.NodegroupDetailed{Id: "ng-1", ClusterId: testMKSV2ClusterID, Nodes: mksV2FakeNodes("ng-1", 2)})

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		fake.server.URL+"/v2/clusters/"+testMKSV2ClusterID+"/nodegroups/ng-1/resize",
		strings.NewReader(`{"nodegroup":{"desired":2}}`))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := fake.server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("resize to the current count answered %d, want 400 like mk-api-v2", resp.StatusCode)
	}
}

const (
	testMKSNodegroupV2ServiceUUID   = "0b5c7a6e-4f2d-4c1a-9a8e-3d2f1e0c9b8a"
	testMKSNodegroupV2PricePlanDay  = "1d8f3c2a-5b6e-4a7d-8c9b-0e1f2a3b4c5d"
	testMKSNodegroupV2PricePlanYear = "9e8d7c6b-5a4f-4e3d-2c1b-0a9f8e7d6c5b"
)

// testMKSNodegroupV2SeedDedicated seeds a cluster with workers_type =
// DEDICATED and the price plans.
func testMKSNodegroupV2SeedDedicated(fake *mksV2Fake) {
	testMKSNodegroupV2SeedCluster(fake, mksclient.ClusterDetailedNetworkTypeL3VPN)
	fake.seedPricePlans(
		&dedicated.PricePlan{UUID: testMKSNodegroupV2PricePlanDay, Name: "1 day"},
		&dedicated.PricePlan{UUID: testMKSNodegroupV2PricePlanYear, Name: "12 month"},
	)
}

// testMKSNodegroupV2DedicatedConfig is a dedicated nodegroup in location
// SPB-5 of the seeded cluster. The provider pool is the cluster one: a
// dedicated location tells no pool.
func testMKSNodegroupV2DedicatedConfig(attributes, dedicatedAttributes string) string {
	return testMKSClusterV2ProviderConfig("provider-project", testMKSV2Pool) +
		testMKSNodegroupV2DedicatedResource(fmt.Sprintf("%q", testMKSV2ClusterID), attributes, dedicatedAttributes)
}

// testMKSNodegroupV2DedicatedResource is the node group of
// testMKSNodegroupV2DedicatedConfig in the cluster clusterID refers to.
func testMKSNodegroupV2DedicatedResource(clusterID, attributes, dedicatedAttributes string) string {
	return fmt.Sprintf(`
resource "selectel_mks_nodegroup_v2" "nodegroup_tf_test_1" {
  cluster_id = %s
  segment    = "SPB-5"
%s
  dedicated_nodegroup_config = {
    service_uuid    = %q
    price_plan_name = "1 day"
%s
  }
}
`, clusterID, attributes, testMKSNodegroupV2ServiceUUID, dedicatedAttributes)
}

func TestMKSNodegroupV2ResourceDedicatedBasic(t *testing.T) {
	t.Parallel()
	fake := newMKSV2Fake(t)
	testMKSNodegroupV2SeedDedicated(fake)

	config := func(attributes string) string {
		return testMKSNodegroupV2DedicatedConfig(`
  nodes_count = 2
  cidr        = "10.20.30.0/24"
`+attributes, "")
	}
	const resized = `
  labels = { a = "1", b = "2" }
  taints = [{ key = "k1", value = "v1", effect = "NoSchedule" }]
`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: fake.providerFactories(),
		CheckDestroy:             testMKSNodegroupV2Destroyed(fake, "ng-1"),
		Steps: []resource.TestStep{
			{
				Config: config(`  labels = { a = "1" }` + "\n"),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(testMKSNodegroupV2Name, "id", testMKSNodegroupV2ID),
					resource.TestCheckResourceAttr(testMKSNodegroupV2Name, "segment", "SPB-5"),
					resource.TestCheckResourceAttr(testMKSNodegroupV2Name, "nodes.#", "2"),
					resource.TestCheckResourceAttr(testMKSNodegroupV2Name, "nodegroup_type", "DEDICATED"),
					resource.TestCheckResourceAttr(testMKSNodegroupV2Name, "cidr", "10.20.30.0/24"),
					resource.TestCheckResourceAttr(testMKSNodegroupV2Name, "enable_autoscale", "false"),
					resource.TestCheckResourceAttr(testMKSNodegroupV2Name, "preemptible", "false"),
					resource.TestCheckNoResourceAttr(testMKSNodegroupV2Name, "cloud_nodegroup_config"),
					resource.TestCheckResourceAttr(testMKSNodegroupV2Name, "dedicated_nodegroup_config.service_uuid",
						testMKSNodegroupV2ServiceUUID),
					resource.TestCheckResourceAttr(testMKSNodegroupV2Name, "dedicated_nodegroup_config.price_plan_name", "1 day"),
					resource.TestCheckResourceAttr(testMKSNodegroupV2Name, "dedicated_nodegroup_config.price_plan_uuid",
						testMKSNodegroupV2PricePlanDay),
					resource.TestCheckResourceAttr(testMKSNodegroupV2Name, "dedicated_nodegroup_config.root_size_gb", "100"),
					resource.TestCheckResourceAttr(testMKSNodegroupV2Name, "dedicated_nodegroup_config.create_storage_partition", "true"),
					resource.TestCheckResourceAttr(testMKSNodegroupV2Name, "dedicated_nodegroup_config.currency", "main"),
					func(_ *terraform.State) error {
						body := testMKSNodegroupV2CreateBody(t, fake)
						err := testMKSClusterV2BodyFields(body, map[string]any{
							"count": float64(2), "segment": "SPB-5", "cidr": "10.20.30.0/24",
						}, "cloud_nodegroup_config", "enable_autoscale", "autoscale_min_nodes", "autoscale_max_nodes")
						if err != nil {
							return err
						}
						config, _ := body["dedicated_nodegroup_config"].(map[string]any)

						return testMKSClusterV2BodyFields(config, map[string]any{
							"service_uuid": testMKSNodegroupV2ServiceUUID, "price_plan_uuid": testMKSNodegroupV2PricePlanDay,
						}, "root_size_gb", "create_storage_partition", "currency")
					},
				),
			},
			{
				Config:   config(`  labels = { a = "1" }` + "\n"),
				PlanOnly: true,
			},
			{
				// Labels and taints change in place, with no autoscale field:
				// the API rejects any of them for a dedicated node group.
				Config: config(resized),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(testMKSNodegroupV2Name, "labels.b", "2"),
					resource.TestCheckResourceAttr(testMKSNodegroupV2Name, "taints.0.key", "k1"),
					testMKSClusterV2Calls(fake, map[string]int{
						mksV2RouteCreateNodegroups: 1, mksV2RoutePatchNodegroup: 1, mksV2RouteDeleteNodegroup: 0,
					}),
					func(_ *terraform.State) error {
						body, _ := fake.lastBody(t, mksV2RoutePatchNodegroup)["nodegroup"].(map[string]any)

						return testMKSClusterV2BodyFields(body, nil, "enable_autoscale", "autoscale_min_nodes", "autoscale_max_nodes")
					},
				),
			},
			{
				// A resize is the other in-place change of a dedicated node
				// group: one resize call, no new PATCH, no replacement.
				Config: strings.Replace(config(resized), "nodes_count = 2", "nodes_count = 3", 1),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(testMKSNodegroupV2Name, "nodes_count", "3"),
					resource.TestCheckResourceAttr(testMKSNodegroupV2Name, "nodes.#", "3"),
					testMKSClusterV2Calls(fake, map[string]int{
						mksV2RouteCreateNodegroups: 1, mksV2RoutePatchNodegroup: 1, mksV2RouteResizeNodegroup: 1,
						mksV2RouteDeleteNodegroup: 0,
					}),
					func(_ *terraform.State) error {
						body, _ := fake.lastBody(t, mksV2RouteResizeNodegroup)["nodegroup"].(map[string]any)

						return testMKSClusterV2BodyFields(body, map[string]any{"desired": float64(3)})
					},
				),
			},
			{
				// The provider moves to another pool: the node group keeps the
				// pool it was created in, and destroy runs with this
				// configuration too.
				Config: strings.Replace(strings.Replace(config(resized), "nodes_count = 2", "nodes_count = 3", 1),
					fmt.Sprintf("region      = %q", testMKSV2Pool), `region      = "ru-3"`, 1),
				PlanOnly: true,
			},
		},
	})

	fake.checkClients(t, "provider-project")
	for _, pool := range fake.clientPools() {
		if pool != testMKSV2Pool {
			t.Errorf("client built for pool %q, want %q where the node group was created", pool, testMKSV2Pool)
		}
	}
}

func TestMKSNodegroupV2ResourceDedicatedPricePlan(t *testing.T) {
	t.Parallel()

	t.Run("unknown name fails at plan", func(t *testing.T) {
		t.Parallel()
		fake := newMKSV2Fake(t)
		testMKSNodegroupV2SeedDedicated(fake)

		resource.UnitTest(t, resource.TestCase{
			ProtoV6ProviderFactories: fake.providerFactories(),
			Steps: []resource.TestStep{
				{
					Config:      strings.Replace(testMKSNodegroupV2DedicatedConfig("  nodes_count = 1\n", ""), `"1 day"`, `"1 week"`, 1),
					PlanOnly:    true,
					ExpectError: testMKSClusterV2Error(`price plan 1 week not found`),
				},
			},
		})
		testMKSNodegroupV2NoCreate(t, fake)
	})

	t.Run("name known only at apply", func(t *testing.T) {
		t.Parallel()

		tests := []struct {
			name      string
			plan      string
			wantError string
		}{
			{name: "resolved before the create", plan: "12 month"},
			{name: "unknown fails before the create", plan: "1 week", wantError: `price plan 1 week not found`},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				fake := newMKSV2Fake(t)
				testMKSNodegroupV2SeedDedicated(fake)

				config := strings.Replace(testMKSNodegroupV2DedicatedConfig("  nodes_count = 1\n", ""),
					`price_plan_name = "1 day"`, `price_plan_name = terraform_data.price_plan.output`, 1) + fmt.Sprintf(`
resource "terraform_data" "price_plan" {
  input = %q
}
`, tt.plan)
				step := resource.TestStep{
					Config: config,
					Check: resource.ComposeTestCheckFunc(
						resource.TestCheckResourceAttr(testMKSNodegroupV2Name, "dedicated_nodegroup_config.price_plan_name", "12 month"),
						resource.TestCheckResourceAttr(testMKSNodegroupV2Name, "dedicated_nodegroup_config.price_plan_uuid",
							testMKSNodegroupV2PricePlanYear),
						// No cidr was configured, so the state takes the allocated one.
						resource.TestCheckResourceAttr(testMKSNodegroupV2Name, "cidr", mksV2FakeAllocatedCIDR),
					),
				}
				checkDestroy := testMKSNodegroupV2Destroyed(fake, "ng-1")
				if tt.wantError != "" {
					step.Check = nil
					step.ExpectError = testMKSClusterV2Error(tt.wantError)
				}
				resource.UnitTest(t, resource.TestCase{
					ProtoV6ProviderFactories: fake.providerFactories(),
					CheckDestroy:             checkDestroy,
					Steps:                    []resource.TestStep{step},
				})
				if tt.wantError != "" {
					testMKSNodegroupV2NoCreate(t, fake)
				}
			})
		}
	})
}

func TestMKSNodegroupV2ResourceDedicatedConfigValidation(t *testing.T) {
	t.Parallel()

	both := `
  nodes_count = 1
  cloud_nodegroup_config = {
    flavor_id = "1013"
  }
`
	tests := []struct {
		name string
		// attributes go into the node group, dedicated into its
		// dedicated_nodegroup_config; the switch below rewrites the
		// configuration of some cases.
		attributes, dedicated string
		wantError             string
	}{
		{name: "both configs", attributes: both, wantError: `Exactly one of these attributes must be configured`},
		{name: "neither config", wantError: `Exactly one of these attributes must be configured`},
		{
			name: "autoscaling", attributes: "nodes_count = 1\nenable_autoscale = true\nautoscale_min_nodes = 1\nautoscale_max_nodes = 3",
			wantError: `Dedicated node groups do not support autoscaling: remove enable_autoscale`,
		},
		{
			name: "autoscaling disabled explicitly", attributes: "nodes_count = 1\nenable_autoscale = false",
			wantError: `Dedicated node groups do not support autoscaling: remove enable_autoscale`,
		},
		{
			name: "autoscale_max_nodes", attributes: "nodes_count = 1\nautoscale_max_nodes = 3",
			wantError: `Dedicated node groups do not support autoscaling: remove autoscale_max_nodes`,
		},
		{name: "no count", wantError: `Set nodes_count: dedicated node groups do not support autoscaling`},
		{name: "no nodes", attributes: "nodes_count = 0", wantError: `Dedicated node groups should have at least 1 nodes`},
		{
			name: "preemptible", attributes: "nodes_count = 1\npreemptible = true",
			wantError: `Dedicated node groups cannot be preemptible`,
		},
		{name: "pool segment", attributes: "nodes_count = 1", wantError: `takes a dedicated server location such as`},
		{name: "cidr of another size", attributes: "nodes_count = 1\ncidr = \"10.20.0.0/16\"", wantError: `must be a /24 network`},
		{name: "public cidr", attributes: "nodes_count = 1\ncidr = \"203.0.113.0/24\"", wantError: `must be a private network`},
		{name: "not a cidr", attributes: "nodes_count = 1\ncidr = \"10.20.30.0\"", wantError: `is not a CIDR`},
		{
			name: "cidr with host bits", attributes: "nodes_count = 1\ncidr = \"10.20.30.5/24\"",
			wantError: `has host bits set: use the network address 10.20.30.0/24`,
		},
		{name: "service_uuid not a UUID", attributes: "nodes_count = 1", wantError: `must be a UUID`},
		{name: "small root", attributes: "nodes_count = 1", dedicated: "root_size_gb = 20", wantError: `must be at least 30`},
		{name: "currency", attributes: "nodes_count = 1", dedicated: `currency = "RUB"`, wantError: `value must be one of`},
		{name: "cloud node group in a dedicated location", wantError: `A cloud node group takes a pool segment such as`},
		{
			name: "all optional values", attributes: "nodes_count = 1\ncidr = \"192.168.10.0/24\"\ninstall_nvidia_device_plugin = true",
			dedicated: "root_size_gb = 30\ncreate_storage_partition = false\ncurrency = \"bonus\"",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fake := newMKSV2Fake(t)
			testMKSNodegroupV2SeedDedicated(fake)

			config := testMKSNodegroupV2DedicatedConfig(tt.attributes, tt.dedicated)
			switch tt.name {
			case "pool segment":
				config = strings.Replace(config, `"SPB-5"`, `"ru-7a"`, 1)
			case "service_uuid not a UUID":
				config = strings.Replace(config, testMKSNodegroupV2ServiceUUID, "dqo.24.256", 1)
			case "neither config":
				config = testMKSClusterV2ProviderConfig("provider-project", testMKSV2Pool) + testMKSNodegroupV2Resource("  nodes_count = 1\n")
			case "cloud node group in a dedicated location":
				config = strings.Replace(testMKSNodegroupV2Config(testMKSNodegroupV2Flavor), `"ru-7a"`, `"SPB-5"`, 1)
			}
			step := resource.TestStep{
				Config:             config,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			}
			if tt.wantError != "" {
				step.ExpectError = testMKSClusterV2Error(tt.wantError)
			}
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: fake.providerFactories(),
				Steps:                    []resource.TestStep{step},
			})
		})
	}
}

func TestMKSNodegroupV2ResourceDedicatedOnCloudCluster(t *testing.T) {
	const wantError = `cluster ` + testMKSV2ClusterID + ` has workers_type = CLOUD and accepts only cloud node groups`

	t.Run("existing cluster fails at plan", func(t *testing.T) {
		t.Parallel()
		fake := newMKSV2Fake(t)
		testMKSNodegroupV2SeedDedicated(fake)
		fake.updateCluster(testMKSV2ClusterID, func(c *mksclient.ClusterDetailed) {
			c.NetworkType = mksclient.ClusterDetailedNetworkTypeSTANDARD
		})

		resource.UnitTest(t, resource.TestCase{
			ProtoV6ProviderFactories: fake.providerFactories(),
			Steps: []resource.TestStep{
				{
					Config:      testMKSNodegroupV2DedicatedConfig("  nodes_count = 1\n", ""),
					PlanOnly:    true,
					ExpectError: testMKSClusterV2Error(wantError),
				},
			},
		})
		testMKSNodegroupV2NoCreate(t, fake)
	})

	t.Run("cluster in the same apply fails before any create", func(t *testing.T) {
		t.Parallel()
		fake := newMKSV2Fake(t)
		fake.seedPricePlans(&dedicated.PricePlan{UUID: testMKSNodegroupV2PricePlanDay, Name: "1 day"})

		config := testMKSClusterV2Config(testMKSClusterV2ProviderConfig("provider-project", testMKSV2Pool), `
  kube_version = "1.30.3"
  workers_type = "CLOUD"
`) + testMKSNodegroupV2DedicatedResource("selectel_mks_cluster_v2.cluster_tf_test_1.id", "  nodes_count = 1\n", "")
		resource.UnitTest(t, resource.TestCase{
			ProtoV6ProviderFactories: fake.providerFactories(),
			CheckDestroy:             testMKSClusterV2Destroyed(fake),
			Steps: []resource.TestStep{
				{
					Config:      config,
					ExpectError: testMKSClusterV2Error(wantError),
				},
			},
		})
		testMKSNodegroupV2NoCreate(t, fake)
	})
}
