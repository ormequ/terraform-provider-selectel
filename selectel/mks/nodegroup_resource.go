package mks

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	mksv2 "github.com/selectel/mks-go/v2/pkg"
	"github.com/selectel/mks-go/v2/pkg/mksclient"
	"github.com/selectel/mks-go/v2/pkg/nodegroup"
	"github.com/terraform-providers/terraform-provider-selectel/selectel/internal/mutexkv"
	"github.com/terraform-providers/terraform-provider-selectel/selectel/internal/schemadoc"
)

const objectNodegroup = "nodegroup"

var nodegroupTimeouts = schemadoc.Timeouts{
	Create: 60 * time.Minute,
	Update: 60 * time.Minute,
	Delete: 60 * time.Minute,
}

// nodegroupLocks serialises the node group creates of one cluster, so the list
// before and after a create tells this provider's node groups apart.
var nodegroupLocks = mutexkv.NewMutexKV()

var (
	_ resource.ResourceWithConfigure      = &nodegroupResource{}
	_ resource.ResourceWithImportState    = &nodegroupResource{}
	_ resource.ResourceWithModifyPlan     = &nodegroupResource{}
	_ resource.ResourceWithValidateConfig = &nodegroupResource{}
)

type nodegroupResource struct {
	connection
	pollInterval time.Duration
}

func NewNodegroupResource() resource.Resource {
	return &nodegroupResource{pollInterval: 10 * time.Second}
}

func (r *nodegroupResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_mks_nodegroup_v2"
}

func (r *nodegroupResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	resp.Diagnostics.Append(r.configure(req.ProviderData)...)
}

func (r *nodegroupResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan nodegroupModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	timeout, diags := plan.Timeouts.Create(ctx, nodegroupTimeouts.Create)
	resp.Diagnostics.Append(diags...)
	client, projectID, diags := r.client(ctx, plan.ProjectID, plan.pool())
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	opts, diags := expandNodegroupCreateOpts(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.ProjectID = types.StringValue(projectID)

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	clusterID := plan.ClusterID.ValueString()
	nodegroupLocks.Lock(clusterID)
	before, err := nodegroup.List(ctx, client, clusterID)
	if err == nil {
		err = nodegroup.Create(ctx, client, clusterID, []mksclient.NodegroupCreateStruct{opts})
	}
	var after []mksclient.NodegroupListItem
	if err == nil {
		after, err = nodegroup.List(ctx, client, clusterID)
		if err != nil {
			err = fmt.Errorf("the node group was created, but listing the node groups of cluster %s failed, so its ID is unknown: "+
				"import it as <cluster_id>/<nodegroup_id>: %w", clusterID, err)
		}
	}
	nodegroupLocks.Unlock(clusterID)
	var nodegroupID string
	if err == nil {
		nodegroupID, err = createdNodegroupID(before, after, opts)
	}
	if err != nil {
		resp.Diagnostics.AddError("Error creating node group", errCreatingObject(objectNodegroup, err).Error())

		return
	}

	id := clusterID + "/" + nodegroupID
	waitErr := newCreatedTaskWaiter(client, clusterID, nodegroupID, r.pollInterval).Wait(ctx)
	// Save the node group even when the wait failed, which can be ctx ending, so Terraform taints it instead of losing it.
	readCtx, readCancel := context.WithTimeout(context.WithoutCancel(ctx), readAfterWaitTimeout)
	defer readCancel()
	got, err := nodegroup.Get(readCtx, client, clusterID, nodegroupID)
	if err != nil {
		resp.Diagnostics.Append(plannedState(req.Plan, &resp.State)...)
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), projectID)...)
		resp.Diagnostics.AddError("Error reading node group", errGettingObject(objectNodegroup, id, errors.Join(waitErr, err)).Error())

		return
	}
	state := plan
	resp.Diagnostics.Append(state.fromAPI(ctx, got, plan, true)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
	if waitErr != nil {
		resp.Diagnostics.AddError("Error waiting for the node group to become ready", waitErr.Error())
	}
	resp.Diagnostics.Append(checkFlavorVolume(ctx, plan.CloudNodegroupConfig, got.CloudNodegroupConfig)...)
}

func (r *nodegroupResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state nodegroupModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	pool := state.pool()
	if pool == "" {
		// The read of an import, which knows no segment yet.
		_, pool, _ = r.importScope()
	}
	client, _, diags := r.client(ctx, state.ProjectID, pool)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	got, err := nodegroup.Get(ctx, client, state.ClusterID.ValueString(), state.nodegroupID())
	if isNotFound(err) {
		resp.State.RemoveResource(ctx)

		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Error reading node group", errGettingObject(objectNodegroup, state.ID.ValueString(), err).Error())

		return
	}

	prior := state
	resp.Diagnostics.Append(state.fromAPI(ctx, got, prior, false)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *nodegroupResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state nodegroupModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	timeout, diags := plan.Timeouts.Update(ctx, nodegroupTimeouts.Update)
	resp.Diagnostics.Append(diags...)
	client, _, diags := r.client(ctx, state.ProjectID, state.pool())
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	patch, changed, diags := expandNodegroupPatch(ctx, plan, state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	clusterID, nodegroupID := state.ClusterID.ValueString(), state.nodegroupID()
	var err error
	if changed {
		err = nodegroupCallAndWait(ctx, client, clusterID, nodegroupID, r.pollInterval, func() error {
			return nodegroup.Patch(ctx, client, clusterID, nodegroupID, patch)
		})
	}
	// A resize to the current count is an API error, and under autoscaling the autoscaler owns the count.
	if err == nil && !plan.NodesCount.Equal(state.NodesCount) && !plan.EnableAutoscale.ValueBool() {
		err = nodegroupCallAndWait(ctx, client, clusterID, nodegroupID, r.pollInterval, func() error {
			return nodegroup.Resize(ctx, client, clusterID, nodegroupID, plan.NodesCount.ValueInt64())
		})
	}
	if err != nil {
		resp.Diagnostics.AddError("Error updating node group", errUpdatingObject(objectNodegroup, state.ID.ValueString(), err).Error())

		return
	}

	got, err := nodegroup.Get(ctx, client, clusterID, nodegroupID)
	if err != nil {
		resp.Diagnostics.AddError("Error reading node group", errGettingObject(objectNodegroup, state.ID.ValueString(), err).Error())

		return
	}
	resp.Diagnostics.Append(plan.fromAPI(ctx, got, plan, true)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *nodegroupResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state nodegroupModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	timeout, diags := state.Timeouts.Delete(ctx, nodegroupTimeouts.Delete)
	resp.Diagnostics.Append(diags...)
	client, _, diags := r.client(ctx, state.ProjectID, state.pool())
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	clusterID, nodegroupID := state.ClusterID.ValueString(), state.nodegroupID()
	err := nodegroupCallAndWait(ctx, client, clusterID, nodegroupID, r.pollInterval, func() error {
		return nodegroup.Delete(ctx, client, clusterID, nodegroupID)
	})
	if err == nil {
		err = poll(ctx, r.pollInterval, func() (string, bool, error) {
			got, err := nodegroup.Get(ctx, client, clusterID, nodegroupID)
			if err != nil {
				return "", isNotFound(err), err
			}

			return fmt.Sprintf("node group %s is in %s", got.Id, got.Status), false, nil
		})
	}
	// A 404 from the tasks or the node group means it is gone, with its cluster or alone.
	if err != nil && !isNotFound(err) {
		resp.Diagnostics.AddError("Error deleting node group", errDeletingObject(objectNodegroup, state.ID.ValueString(), err).Error())
	}
}

// ImportState takes the project and the pool from the provider configuration,
// because the ID carries neither, like selectel_mks_nodegroup_v1.
func (r *nodegroupResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	clusterID, nodegroupID, _ := strings.Cut(req.ID, "/")
	if clusterID == "" || nodegroupID == "" {
		resp.Diagnostics.AddError("Invalid import ID", fmt.Sprintf("got %q, want <cluster_id>/<nodegroup_id>", req.ID))

		return
	}
	projectID, _, diags := r.importScope()
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("cluster_id"), clusterID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), projectID)...)
}

// nodegroupCallAndWait runs a mutating call on the node group and waits for all
// the node group tasks it created: labels and taints, for one, are two tasks.
func nodegroupCallAndWait(ctx context.Context, client *mksv2.ServiceClient, clusterID, nodegroupID string, interval time.Duration, call func() error) error {
	waiter, err := newTaskWaiter(ctx, client, clusterID, nodegroupID, interval)
	if err != nil {
		return err
	}

	err = call()
	if err != nil {
		return err
	}

	return waiter.Wait(ctx)
}
