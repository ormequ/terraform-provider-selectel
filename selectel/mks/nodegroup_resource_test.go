package mks

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/selectel/mks-go/v2/pkg/mksclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func labelsMap(labels map[string]string) types.Map {
	values := map[string]attr.Value{}
	for k, v := range labels {
		values[k] = types.StringValue(v)
	}

	return types.MapValueMust(types.StringType, values)
}

func taintsList(effects ...string) types.List {
	taints := make([]attr.Value, 0, len(effects))
	for _, effect := range effects {
		taints = append(taints, types.ObjectValueMust(nodegroupTaintAttrTypes, map[string]attr.Value{
			"key": types.StringValue("dedicated"), "value": types.StringValue("gpu"), "effect": types.StringValue(effect),
		}))
	}

	return types.ListValueMust(types.ObjectType{AttrTypes: nodegroupTaintAttrTypes}, taints)
}

func TestNodegroupCreateSendsFields(t *testing.T) {
	fake, r := fakeNodegroupResource(t)
	fake.seedTask(testClusterID, "", "UPGRADE_CLUSTER_CONFIG", true)
	fake.seedTask(testClusterID, "nodegroup-0", "RESIZE_NODEGROUP", true)

	plan := nodegroupPlan()
	plan.Labels = labelsMap(map[string]string{"role": "web"})
	plan.Taints = taintsList("NoSchedule")
	plan.UserData = types.StringValue("IyEvYmluL3NoCg==")
	plan.CloudNodegroupConfig = cloudConfigObject(map[string]attr.Value{
		"affinity_policy": types.StringValue("soft-anti-affinity"),
	})
	got := mustCreateNodegroup(t, r, plan)

	body := fake.lastBody(routeCreateNodegroup)["nodegroups"].([]any)[0].(map[string]any)
	assert.Equal(t, map[string]any{
		"count": float64(2), "segment": "ru-7a", "enable_autoscale": false, "user_data": "IyEvYmluL3NoCg==",
		"labels": map[string]any{"role": "web"},
		"taints": []any{map[string]any{"key": "dedicated", "value": "gpu", "effect": "NoSchedule"}},
		"cloud_nodegroup_config": map[string]any{
			"flavor_id": "flavor-1", "volume_gb": float64(32), "volume_type": "fast.ru-7a", "affinity_policy": "soft-anti-affinity",
		},
	}, body)

	assert.Equal(t, types.StringValue(testNodegroupID), got.ID)
	assert.Equal(t, types.StringValue(keystoneProject), got.ProjectID)
	assert.Equal(t, types.StringValue("ACTIVE"), got.Status)
	assert.Equal(t, nodesList("nodegroup-1", 2), got.Nodes)
	assert.Equal(t, plan.Labels, got.Labels)
	assert.Equal(t, plan.Taints, got.Taints)
	assert.Equal(t, plan.CloudNodegroupConfig, got.CloudNodegroupConfig)
	assert.Equal(t, []string{"ru-7"}, r.config.(*testProviderConfig).gotPool)
}

func TestNodegroupProjectID(t *testing.T) {
	for _, tt := range []struct {
		name      string
		planProj  types.String
		wantState string
	}{
		{name: "omitted takes the provider project", planProj: types.StringUnknown(), wantState: keystoneProject},
		{name: "configured dashed stays as configured", planProj: types.StringValue(apiProject), wantState: apiProject},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, r := fakeNodegroupResource(t)

			plan := nodegroupPlan()
			plan.ProjectID = tt.planProj
			got := mustCreateNodegroup(t, r, plan)

			assert.Equal(t, types.StringValue(tt.wantState), got.ProjectID)
			assert.Equal(t, []string{keystoneProject}, r.config.(*testProviderConfig).gotProject)
		})
	}
}

func TestNodegroupCreateWithAutoscaleStartsFromTheMinimum(t *testing.T) {
	fake, r := fakeNodegroupResource(t)

	plan := nodegroupPlan()
	plan.NodesCount = types.Int64Unknown()
	plan.EnableAutoscale = types.BoolValue(true)
	plan.AutoscaleMinNodes = types.Int64Value(3)
	plan.AutoscaleMaxNodes = types.Int64Value(5)
	got := mustCreateNodegroup(t, r, plan)

	body := fake.lastBody(routeCreateNodegroup)["nodegroups"].([]any)[0].(map[string]any)
	assert.Equal(t, float64(3), body["count"])
	assert.Equal(t, types.Int64Value(3), got.NodesCount)
	assert.Equal(t, types.Int64Value(3), got.AutoscaleMinNodes)
	assert.Equal(t, types.Int64Value(5), got.AutoscaleMaxNodes)
}

func TestCreatedNodegroupID(t *testing.T) {
	listed := func(id, segment, flavorID string, labels map[string]string) mksclient.NodegroupListItem {
		return mksclient.NodegroupListItem{
			Id: id, Segment: segment, Labels: labels,
			CloudNodegroupConfig: &mksclient.CloudNodegroupConfigInfo{FlavorId: flavorID},
		}
	}
	opts := mksclient.NodegroupCreateStruct{
		Segment: "ru-7a", Labels: &map[string]string{"role": "web"},
		CloudNodegroupConfig: &mksclient.CloudNodegroupConfig{FlavorId: "flavor-1"},
	}
	mine := listed("mine", "ru-7a", "flavor-1", map[string]string{"role": "web"})
	old := listed("old", "ru-7a", "flavor-1", map[string]string{"role": "web"})
	for _, tt := range []struct {
		name    string
		after   []mksclient.NodegroupListItem
		want    string
		wantErr string
	}{
		{name: "a node group listed before is not new", after: []mksclient.NodegroupListItem{old, mine}, want: "mine"},
		{name: "another segment is not a match", after: []mksclient.NodegroupListItem{old, listed("other", "ru-7b", "flavor-1", map[string]string{"role": "web"}), mine}, want: "mine"},
		{name: "other labels are not a match", after: []mksclient.NodegroupListItem{listed("other", "ru-7a", "flavor-1", nil), mine}, want: "mine"},
		{name: "another flavor is not a match", after: []mksclient.NodegroupListItem{listed("other", "ru-7a", "flavor-2", map[string]string{"role": "web"}), mine}, want: "mine"},
		{
			name: "two matches fail", after: []mksclient.NodegroupListItem{mine, listed("twin", "ru-7a", "flavor-1", map[string]string{"role": "web"})},
			wantErr: "2 of the new node groups [mine twin] match the configuration, want 1: import the right one as <cluster_id>/<nodegroup_id>",
		},
		{name: "no match fails", after: []mksclient.NodegroupListItem{old}, wantErr: "0 of the new node groups [] match"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := createdNodegroupID([]mksclient.NodegroupListItem{old}, tt.after, opts)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)

				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestNodegroupCreateAmongConcurrentCreates(t *testing.T) {
	t.Run("a node group created elsewhere with another segment is skipped", func(t *testing.T) {
		fake, r := fakeNodegroupResource(t)
		fake.afterCreate = func() {
			fake.nodegroups["nodegroup-2"] = mksclient.NodegroupDetailed{Id: "nodegroup-2", ClusterId: testClusterID, Segment: "ru-7b"}
		}

		got := mustCreateNodegroup(t, r, nodegroupPlan())
		assert.Equal(t, types.StringValue(testNodegroupID), got.ID)
	})
	t.Run("a twin created elsewhere fails the create and advises import", func(t *testing.T) {
		fake, r := fakeNodegroupResource(t)
		fake.afterCreate = func() {
			fake.nodegroups["nodegroup-2"] = mksclient.NodegroupDetailed{
				Id: "nodegroup-2", ClusterId: testClusterID, Segment: "ru-7a",
				CloudNodegroupConfig: &mksclient.CloudNodegroupConfigInfo{FlavorId: "flavor-1"},
			}
		}

		state, diags := createNodegroup(t, r, nodegroupPlan())
		assertErrorContains(t, diags, "Error creating node group", "2 of the new node groups [nodegroup-1 nodegroup-2]", "import the right one")
		assert.True(t, state.Raw.IsNull())
	})
	t.Run("a failed list after the create advises import", func(t *testing.T) {
		fake, r := fakeNodegroupResource(t)
		fake.afterCreate = func() { fake.failNext[routeNodegroups] = fakeFailure{status: http.StatusForbidden, left: 1} }

		state, diags := createNodegroup(t, r, nodegroupPlan())
		assertErrorContains(t, diags, "Error creating node group", "the node group was created, but listing the node groups of cluster",
			"import it as <cluster_id>/<nodegroup_id>")
		assert.True(t, state.Raw.IsNull())
	})
}

func TestNodegroupCreateKeepsTheNodegroupWhenTheWaitFails(t *testing.T) {
	t.Run("a failed task saves the node group and reports the task", func(t *testing.T) {
		fake, r := fakeNodegroupResource(t)
		fake.failTasks("CREATE_NODEGROUP", true)

		state, diags := createNodegroup(t, r, nodegroupPlan())
		assertErrorContains(t, diags, "Error waiting for the node group to become ready", "task CREATE_NODEGROUP", "fake details of CREATE_NODEGROUP")
		got := readNodegroupModel(t, state)
		assert.Equal(t, types.StringValue(testNodegroupID), got.ID)
		assert.Equal(t, types.StringValue("PENDING_CREATE"), got.Status)
	})
	t.Run("a failed read after the wait saves the planned node group", func(t *testing.T) {
		fake, r := fakeNodegroupResource(t)
		fake.failTasks("CREATE_NODEGROUP", true)
		fake.fail(routeNodegroup, http.StatusForbidden)

		state, diags := createNodegroup(t, r, nodegroupPlan())
		assertErrorContains(t, diags, "Error reading node group", "task CREATE_NODEGROUP", "fake error 403")
		got := readNodegroupModel(t, state)
		assert.Equal(t, types.StringValue(testNodegroupID), got.ID)
		assert.Equal(t, types.StringValue(keystoneProject), got.ProjectID)
		assert.Equal(t, types.StringValue("ru-7a"), got.Segment)
		assert.True(t, got.Status.IsNull())
	})
}

func TestNodegroupFlavorWithALocalDisk(t *testing.T) {
	t.Run("a configured volume replaced by the flavor fails after the node group is saved", func(t *testing.T) {
		fake, r := fakeNodegroupResource(t)
		fake.flavorDisks["flavor-1"] = 100

		state, diags := createNodegroup(t, r, nodegroupPlan())
		assertErrorContains(t, diags, "Volume replaced by the flavor",
			"Flavor flavor-1 has a local disk, so the node group got local_volume = true instead of false and volume_gb = 100 instead of 32")
		got := readNodegroupModel(t, state)
		assert.Equal(t, types.StringValue(testNodegroupID), got.ID)
	})
	t.Run("an omitted volume takes the flavor values", func(t *testing.T) {
		fake, r := fakeNodegroupResource(t)
		fake.flavorDisks["flavor-1"] = 100

		plan := nodegroupPlan()
		plan.CloudNodegroupConfig = cloudConfigObject(map[string]attr.Value{
			"volume_gb": types.Int64Unknown(), "volume_type": types.StringUnknown(), "local_volume": types.BoolUnknown(),
		})
		got := mustCreateNodegroup(t, r, plan)
		assert.Equal(t, cloudConfigObject(map[string]attr.Value{
			"volume_gb": types.Int64Value(100), "volume_type": types.StringValue(""), "local_volume": types.BoolValue(true),
		}), got.CloudNodegroupConfig)
	})
}

func TestNodegroupUpdate(t *testing.T) {
	for _, tt := range []struct {
		name       string
		prior      func(m *nodegroupModel)
		mutate     func(m *nodegroupModel)
		wantPatch  map[string]any
		wantResize map[string]any
	}{
		{name: "changed labels go alone, with no resize", mutate: func(m *nodegroupModel) {
			m.Labels = labelsMap(map[string]string{"role": "db"})
		}, wantPatch: map[string]any{"labels": map[string]any{"role": "db"}}},
		{name: "removed labels and taints are cleared", prior: func(m *nodegroupModel) {
			m.Labels, m.Taints = labelsMap(map[string]string{"role": "web"}), taintsList("NoSchedule")
		}, mutate: func(m *nodegroupModel) {
			m.Labels, m.Taints = types.MapNull(types.StringType), types.ListNull(types.ObjectType{AttrTypes: nodegroupTaintAttrTypes})
		}, wantPatch: map[string]any{"labels": map[string]any{}, "taints": []any{}}},
		{name: "a changed count resizes", mutate: func(m *nodegroupModel) {
			m.NodesCount = types.Int64Value(3)
		}, wantResize: map[string]any{"desired": float64(3)}},
		{name: "the autoscale fields go together", mutate: func(m *nodegroupModel) {
			m.EnableAutoscale, m.AutoscaleMinNodes, m.AutoscaleMaxNodes = types.BoolValue(true), types.Int64Value(1), types.Int64Value(4)
		}, wantPatch: map[string]any{"enable_autoscale": true, "autoscale_min_nodes": float64(1), "autoscale_max_nodes": float64(4)}},
		{name: "a count changed under autoscaling is not sent", prior: func(m *nodegroupModel) {
			m.EnableAutoscale, m.AutoscaleMinNodes, m.AutoscaleMaxNodes = types.BoolValue(true), types.Int64Value(1), types.Int64Value(4)
		}, mutate: func(m *nodegroupModel) {
			m.NodesCount = types.Int64Value(3)
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fake, r := fakeNodegroupResource(t)
			plan := nodegroupPlan()
			if tt.prior != nil {
				tt.prior(&plan)
			}
			prior := mustCreateNodegroup(t, r, plan)
			plan = prior
			tt.mutate(&plan)
			plan.Status = types.StringUnknown()
			plan.Nodes = types.ListUnknown(types.ObjectType{AttrTypes: nodegroupNodeAttrTypes})

			got, diags := updateNodegroup(t, r, prior, plan)
			require.False(t, diags.HasError(), "%v", diags)

			assert.Equal(t, len(tt.wantPatch) > 0, fake.callCount(routePatchNodegroup) == 1)
			if tt.wantPatch != nil {
				assert.Equal(t, map[string]any{"nodegroup": tt.wantPatch}, fake.lastBody(routePatchNodegroup))
			}
			assert.Equal(t, len(tt.wantResize) > 0, fake.callCount(routeResizeNodegroup) == 1)
			if tt.wantResize != nil {
				assert.Equal(t, map[string]any{"nodegroup": tt.wantResize}, fake.lastBody(routeResizeNodegroup))
			}
			assert.Equal(t, plan.NodesCount, got.NodesCount)
			assert.Equal(t, plan.Labels, got.Labels)
			assert.Equal(t, plan.Taints, got.Taints)
			assert.Equal(t, types.StringValue("ACTIVE"), got.Status)
		})
	}
}

func TestNodegroupUpdateFailsOnAnyFailedTask(t *testing.T) {
	fake, r := fakeNodegroupResource(t)
	prior := mustCreateNodegroup(t, r, nodegroupPlan())
	fake.failTasks("UPDATE_NODEGROUP_TAINTS", true)

	plan := prior
	plan.Labels = labelsMap(map[string]string{"role": "db"})
	plan.Taints = taintsList("NoExecute")
	_, diags := updateNodegroup(t, r, prior, plan)

	assertErrorContains(t, diags, "Error updating node group", "task UPDATE_NODEGROUP_TAINTS", "ended in ERROR")
	assert.Equal(t, mksclient.NodegroupDetailedStatusACTIVE, fake.nodegroup("nodegroup-1").Status)
}

func TestNodegroupRead(t *testing.T) {
	t.Run("a node group removed outside Terraform leaves the state", func(t *testing.T) {
		_, r := fakeNodegroupResource(t)

		state, diags := readNodegroup(t, r, nodegroupState())
		require.False(t, diags.HasError(), "%v", diags)
		assert.True(t, state.Raw.IsNull())
	})
	t.Run("an API error keeps the state", func(t *testing.T) {
		fake, r := fakeNodegroupResource(t)
		fake.fail(routeNodegroup, http.StatusForbidden)

		state, diags := readNodegroup(t, r, nodegroupState())
		assertErrorContains(t, diags, "Error reading node group", "fake error 403")
		assert.False(t, state.Raw.IsNull())
	})
	t.Run("the count follows the nodes and empty collections stay as configured", func(t *testing.T) {
		fake, r := fakeNodegroupResource(t)
		plan := nodegroupPlan()
		plan.Labels = labelsMap(nil)
		plan.Taints = taintsList()
		prior := mustCreateNodegroup(t, r, plan)
		fake.updateNodegroup("nodegroup-1", func(ng *mksclient.NodegroupDetailed) { setFakeNodes(ng, 4) })

		state, diags := readNodegroup(t, r, prior)
		require.False(t, diags.HasError(), "%v", diags)
		got := readNodegroupModel(t, state)
		assert.Equal(t, types.Int64Value(4), got.NodesCount)
		assert.Equal(t, labelsMap(nil), got.Labels)
		assert.Equal(t, taintsList(), got.Taints)
	})
	t.Run("autoscale limits the API answers as 0 while off keep the state", func(t *testing.T) {
		_, r := fakeNodegroupResource(t)
		prior := mustCreateNodegroup(t, r, nodegroupPlan())

		state, diags := readNodegroup(t, r, prior)
		require.False(t, diags.HasError(), "%v", diags)
		got := readNodegroupModel(t, state)
		assert.True(t, got.AutoscaleMinNodes.IsNull())
		assert.True(t, got.AutoscaleMaxNodes.IsNull())
		assert.Equal(t, prior, got)
	})
}

func TestNodegroupDelete(t *testing.T) {
	t.Run("waits until the node group is gone", func(t *testing.T) {
		fake, r := fakeNodegroupResource(t)
		prior := mustCreateNodegroup(t, r, nodegroupPlan())
		getsBefore := fake.callCount(routeNodegroup)

		diags := deleteNodegroup(t, r, prior)
		require.False(t, diags.HasError(), "%v", diags)
		assert.Equal(t, 1, fake.callCount(routeDeleteNodegroup))
		assert.Equal(t, 1, fake.callCount(routeNodegroup)-getsBefore)
		assert.Empty(t, fake.nodegroups)
	})
	t.Run("a busy node group fails with the API text", func(t *testing.T) {
		fake, r := fakeNodegroupResource(t)
		prior := mustCreateNodegroup(t, r, nodegroupPlan())
		fake.updateNodegroup("nodegroup-1", func(ng *mksclient.NodegroupDetailed) { ng.Status = "PENDING_SCALE_UP" })

		diags := deleteNodegroup(t, r, prior)
		assertErrorContains(t, diags, "Error deleting node group", "nodegroup has invalid status to perform this operation")
	})
	t.Run("a node group or cluster already gone is deleted", func(t *testing.T) {
		fake, r := fakeNodegroupResource(t)
		require.False(t, deleteNodegroup(t, r, nodegroupState()).HasError())
		fake.removeCluster(testClusterID)
		require.False(t, deleteNodegroup(t, r, nodegroupState()).HasError())
	})
	t.Run("a failed delete task fails", func(t *testing.T) {
		fake, r := fakeNodegroupResource(t)
		prior := mustCreateNodegroup(t, r, nodegroupPlan())
		fake.failTasks("DELETE_NODEGROUP", true)

		assertErrorContains(t, deleteNodegroup(t, r, prior), "Error deleting node group", "task DELETE_NODEGROUP")
	})
}

func TestNodegroupImport(t *testing.T) {
	importNodegroup := func(t *testing.T, r *nodegroupResource, id string) resource.ImportStateResponse {
		t.Helper()
		resp := resource.ImportStateResponse{State: emptyNodegroupState(t, r)}
		r.ImportState(context.Background(), resource.ImportStateRequest{ID: id}, &resp)

		return resp
	}

	t.Run("takes the project and the pool from the provider", func(t *testing.T) {
		_, r := fakeNodegroupResource(t)
		created := mustCreateNodegroup(t, r, nodegroupPlan())

		resp := importNodegroup(t, r, testNodegroupID)
		require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
		imported := readNodegroupModel(t, resp.State)
		assert.Equal(t, types.StringValue(testClusterID), imported.ClusterID)
		assert.Equal(t, types.StringValue(keystoneProject), imported.ProjectID)

		state, diags := readNodegroup(t, r, imported)
		require.False(t, diags.HasError(), "%v", diags)
		got := readNodegroupModel(t, state)
		assert.Equal(t, "ru-1", r.config.(*testProviderConfig).gotPool[1])
		assert.Equal(t, created.Segment, got.Segment)
		assert.Equal(t, created.Nodes, got.Nodes)
	})
	for _, id := range []string{testClusterID, testClusterID + "/", "/nodegroup-1"} {
		t.Run("rejects "+id, func(t *testing.T) {
			_, r := fakeNodegroupResource(t)

			resp := importNodegroup(t, r, id)
			assertErrorContains(t, resp.Diagnostics, "Invalid import ID", "want <cluster_id>/<nodegroup_id>")
		})
	}
	t.Run("needs the provider project and pool", func(t *testing.T) {
		r := &nodegroupResource{connection: connection{config: &testProviderConfig{}}}

		resp := importNodegroup(t, r, testNodegroupID)
		assertErrorContains(t, resp.Diagnostics, "Missing project ID")
	})
}

func TestNodegroupCRUDFailsWithoutAClient(t *testing.T) {
	fake, r := fakeNodegroupResource(t)
	r.config.(*testProviderConfig).clientErr = errors.New("no such auth token")

	relabeled := nodegroupState()
	relabeled.Labels = labelsMap(map[string]string{"role": "db"})
	for _, call := range []func() diag.Diagnostics{
		func() diag.Diagnostics { _, d := createNodegroup(t, r, nodegroupPlan()); return d },
		func() diag.Diagnostics { _, d := readNodegroup(t, r, nodegroupState()); return d },
		func() diag.Diagnostics { _, d := updateNodegroup(t, r, nodegroupState(), relabeled); return d },
		func() diag.Diagnostics { return deleteNodegroup(t, r, nodegroupState()) },
	} {
		assertErrorContains(t, call(), "Error initializing MKS client", "connect to Managed Kubernetes")
	}
	assert.Equal(t, 0, fake.callCount(routeNodegroups)+fake.callCount(routeNodegroup)+fake.callCount(routeTasks))
}

func TestNodegroupUpdateErrors(t *testing.T) {
	t.Run("a rejected patch fails with the API text", func(t *testing.T) {
		fake, r := fakeNodegroupResource(t)
		prior := mustCreateNodegroup(t, r, nodegroupPlan())
		fake.failWithBody(routePatchNodegroup, http.StatusConflict, "application/json",
			`{"error":{"message":"cluster or one of nodegroups has invalid status to perform this operation"}}`)

		plan := prior
		plan.Labels = labelsMap(map[string]string{"role": "db"})
		_, diags := updateNodegroup(t, r, prior, plan)
		assertErrorContains(t, diags, "Error updating node group", "'"+testNodegroupID+"'", "invalid status to perform this operation")
		assert.Equal(t, 0, fake.callCount(routeResizeNodegroup))
	})
	t.Run("a failed read after the update fails", func(t *testing.T) {
		fake, r := fakeNodegroupResource(t)
		prior := mustCreateNodegroup(t, r, nodegroupPlan())
		fake.fail(routeNodegroup, http.StatusForbidden)

		plan := prior
		plan.NodesCount = types.Int64Value(3)
		_, diags := updateNodegroup(t, r, prior, plan)
		assertErrorContains(t, diags, "Error reading node group", "fake error 403")
		assert.Equal(t, 1, fake.callCount(routeResizeNodegroup))
	})
}
