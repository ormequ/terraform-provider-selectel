package selectel

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/resource"
	"github.com/hashicorp/terraform-plugin-sdk/v2/terraform"
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

func TestMKSNodegroupV2ResourceImportDedicated(t *testing.T) {
	t.Parallel()
	fake := newMKSV2Fake(t)
	testMKSNodegroupV2SeedCluster(fake, mksclient.ClusterDetailedNetworkTypeL3VPN)
	fake.seedNodegroup(mksclient.NodegroupDetailed{
		Id: "ng-1", ClusterId: testMKSV2ClusterID, Segment: "ru-7a", Status: "ACTIVE",
		Nodes:                    mksV2FakeNodes("ng-1", 1),
		DedicatedNodegroupConfig: &mksclient.DedicatedNodegroupConfig{},
	})

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: fake.providerFactories(),
		Steps: []resource.TestStep{
			{
				Config:        testMKSClusterV2ProviderConfig("provider-project", testMKSV2Pool) + testMKSNodegroupV2Resource(testMKSNodegroupV2Flavor),
				ResourceName:  testMKSNodegroupV2Name,
				ImportState:   true,
				ImportStateId: testMKSNodegroupV2ID,
				ExpectError: testMKSClusterV2Error(`node group ` + testMKSNodegroupV2ID +
					` is dedicated: dedicated node groups are not supported by this resource yet`),
			},
		},
	})

	if !fake.hasNodegroup("ng-1") {
		t.Errorf("the dedicated node group was deleted")
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
