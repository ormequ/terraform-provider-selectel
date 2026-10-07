package selectel

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	dsschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/selectel/mks-go/pkg/v1/kubeoptions"
	mksv2 "github.com/selectel/mks-go/v2/pkg"
	"github.com/selectel/mks-go/v2/pkg/cluster"
	"github.com/selectel/mks-go/v2/pkg/mksclient"
	"github.com/selectel/mks-go/v2/pkg/nodegroup"
	"github.com/selectel/mks-go/v2/pkg/task"
)

// mksV2ClientFn builds the mk-api-v2 client for every _v2 resource and data
// source. Tests replace it with a client of a fake server.
var mksV2ClientFn = newMKSV2Client

func newMKSV2Client(_ context.Context, config *Config, projectID, pool string) (*mksv2.ServiceClient, error) {
	selvpcClient, err := config.GetSelVPCClientWithProjectScope(projectID)
	if err != nil {
		return nil, fmt.Errorf("can't get project-scope selvpc client for mks: %w", err)
	}

	err = validateRegion(selvpcClient, MKS, pool)
	if err != nil {
		return nil, fmt.Errorf("can't validate pool: %w", err)
	}

	endpoint, err := selvpcClient.Catalog.GetEndpoint(MKS, pool)
	if err != nil {
		return nil, fmt.Errorf("can't get endpoint to init mks client: %w", err)
	}

	baseURL, err := mksV2BaseURL(endpoint.URL)
	if err != nil {
		return nil, err
	}

	return newMKSV2ServiceClient(selvpcClient.GetXAuthToken(), baseURL, config.UserAgent)
}

// mksV2BaseURL turns the catalog endpoint of mk-api into the mk-api-v2 base
// URL: the endpoint ends with /v1, while the v2 client paths start with /v2/,
// so only the scheme and the host are kept.
func mksV2BaseURL(endpoint string) (string, error) {
	endpointURL, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("can't parse mks endpoint %q: %w", endpoint, err)
	}

	return endpointURL.Scheme + "://" + endpointURL.Host, nil
}

// newMKSV2ServiceClient is the part of newMKSV2Client that needs no Keystone.
func newMKSV2ServiceClient(token, baseURL, userAgent string) (*mksv2.ServiceClient, error) {
	client, err := mksv2.NewMKSClientV2(token, baseURL)
	if err != nil {
		return nil, fmt.Errorf("can't init mks v2 client: %w", err)
	}
	client.UserAgent = userAgent

	return client, nil
}

// mksV2ProjectID returns the project_id attribute, or the provider one when
// the attribute is not set.
func mksV2ProjectID(attr types.String, config *Config) (string, diag.Diagnostics) {
	var diags diag.Diagnostics

	if !attr.IsNull() && !attr.IsUnknown() && attr.ValueString() != "" {
		return attr.ValueString(), diags
	}
	if config != nil && config.ProjectID != "" {
		return config.ProjectID, diags
	}

	diags.AddAttributeError(path.Root("project_id"), "Missing project ID",
		"Set project_id in the resource or data source, or in the provider configuration.")

	return "", diags
}

// mksV2Provided holds the provider Config for the _v2 resources and data
// sources.
type mksV2Provided struct {
	config *Config
}

func (p *mksV2Provided) configure(providerData any) diag.Diagnostics {
	var diags diag.Diagnostics

	if providerData == nil {
		return diags
	}

	config, ok := providerData.(*Config)
	if !ok {
		diags.AddError("Unexpected provider data",
			fmt.Sprintf("Expected *Config, got %T. Please report this issue to the provider developers.", providerData))

		return diags
	}

	p.config = config

	return diags
}

// client resolves the project ID and builds the mk-api-v2 client for it.
func (p *mksV2Provided) client(ctx context.Context, projectIDAttr types.String, pool string) (*mksv2.ServiceClient, string, diag.Diagnostics) {
	projectID, diags := mksV2ProjectID(projectIDAttr, p.config)
	if diags.HasError() {
		return nil, "", diags
	}

	client, err := mksV2ClientFn(ctx, p.config, projectID, pool)
	if err != nil {
		diags.AddError("Error initializing MKS client", err.Error())

		return nil, "", diags
	}

	return client, projectID, diags
}

// mksV2DataSource holds the provider Config for the _v2 data sources.
type mksV2DataSource struct {
	mksV2Provided
}

func (d *mksV2DataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	resp.Diagnostics.Append(d.configure(req.ProviderData)...)
}

// valueOrZero dereferences an optional field of an mk-api-v2 model.
func valueOrZero[T any](v *T) T {
	if v == nil {
		var zero T

		return zero
	}

	return *v
}

// mksKubeOptionsV2DataSource serves selectel_mks_feature_gates_v2 and
// selectel_mks_admission_controllers_v2, which differ only in names and the
// API call, like their _v1 counterparts.
type mksKubeOptionsV2DataSource struct {
	mksV2DataSource

	typeName    string
	attribute   string
	object      string
	description string
	docs        mksKubeOptionsV2Docs
	list        func(ctx context.Context, client *mksv2.ServiceClient) ([]*kubeoptions.View, error)
	flatten     func(views []*kubeoptions.View) []any
}

type mksKubeOptionsV2Docs struct {
	filter            string
	filterKubeVersion string
	options           string
	names             string
}

type mksKubeOptionsFilterV2Model struct {
	KubeVersion types.String `tfsdk:"kube_version"`
}

type mksKubeOptionV2Model struct {
	KubeVersion types.String `tfsdk:"kube_version"`
	Names       types.Set    `tfsdk:"names"`
}

var mksKubeOptionV2Type = types.ObjectType{AttrTypes: map[string]attr.Type{
	"kube_version": types.StringType,
	"names":        types.SetType{ElemType: types.StringType},
}}

var mksKubeOptionsV2PoolDocs = resourceDocs{Name: "cluster"}

func (d *mksKubeOptionsV2DataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + d.typeName
}

func (d *mksKubeOptionsV2DataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = dsschema.Schema{
		Description: d.description,
		Attributes: map[string]dsschema.Attribute{
			"id": dsschema.StringAttribute{
				Computed:    true,
				Description: "Checksum of the returned list.",
			},
			"project_id": projectIDFrameworkDataSourceSchema(),
			"pool":       mksKubeOptionsV2PoolDocs.regionFrameworkDataSourceSchema(),
			"filter": dsschema.SetNestedAttribute{
				Optional:    true,
				Description: d.docs.filter,
				NestedObject: dsschema.NestedAttributeObject{
					Attributes: map[string]dsschema.Attribute{
						"kube_version": dsschema.StringAttribute{
							Required:    true,
							Description: d.docs.filterKubeVersion,
						},
					},
				},
			},
			d.attribute: dsschema.SetNestedAttribute{
				Computed:    true,
				Description: d.docs.options,
				NestedObject: dsschema.NestedAttributeObject{
					Attributes: map[string]dsschema.Attribute{
						"kube_version": dsschema.StringAttribute{
							Computed:    true,
							Description: "Kubernetes version.",
						},
						"names": dsschema.SetAttribute{
							Computed:    true,
							ElementType: types.StringType,
							Description: d.docs.names,
						},
					},
				},
			},
		},
	}
}

func (d *mksKubeOptionsV2DataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var (
		projectIDAttr, pool types.String
		filterSet           types.Set
		filter              []mksKubeOptionsFilterV2Model
	)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("project_id"), &projectIDAttr)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("pool"), &pool)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("filter"), &filterSet)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(filterSet.ElementsAs(ctx, &filter, false)...)
	if resp.Diagnostics.HasError() {
		return
	}

	client, projectID, diags := d.client(ctx, projectIDAttr, pool.ValueString())
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	views, err := d.list(ctx, client)
	if err != nil {
		resp.Diagnostics.AddError("Error reading "+d.object, errGettingObjects(d.object, err).Error())

		return
	}

	options, checksum, err := d.filterViews(views, filter)
	if err != nil {
		resp.Diagnostics.AddError("Error reading "+d.object, err.Error())

		return
	}

	models := make([]mksKubeOptionV2Model, len(options))
	for i, option := range options {
		// Copy and deduplicate: a framework set rejects duplicates, an SDKv2 set drops them.
		names := append([]string{}, option.Names...)
		slices.Sort(names)
		nameSet, diags := types.SetValueFrom(ctx, types.StringType, slices.Compact(names))
		resp.Diagnostics.Append(diags...)
		models[i] = mksKubeOptionV2Model{KubeVersion: types.StringValue(option.KubeVersion), Names: nameSet}
	}
	optionSet, diags := types.SetValueFrom(ctx, mksKubeOptionV2Type, models)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), checksum)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), projectID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("pool"), pool)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("filter"), filterSet)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root(d.attribute), optionSet)...)
}

// filterViews applies the filter the way the _v1 data sources do and returns
// the same checksum they use as the ID.
func (d *mksKubeOptionsV2DataSource) filterViews(views []*kubeoptions.View, filter []mksKubeOptionsFilterV2Model) ([]*kubeoptions.View, string, error) {
	if len(filter) == 0 {
		checksum, err := interfaceListChecksum(d.flatten(views))

		return views, checksum, err
	}

	kubeVersion := filter[0].KubeVersion.ValueString()
	if kubeVersion == "" {
		return nil, "", errors.New("kubernetes version is not set")
	}
	kubeMinorVersion, err := kubeVersionTrimToMinor(kubeVersion)
	if err != nil {
		return nil, "", err
	}

	names, err := filterKubeOptionsByKubeVersion(views, kubeMinorVersion)
	if err != nil {
		return nil, "", err
	}

	// stringListChecksum sorts its argument in place.
	checksum, err := stringListChecksum(slices.Clone(names))
	if err != nil {
		return nil, "", err
	}

	return []*kubeoptions.View{{KubeVersion: kubeMinorVersion, Names: names}}, checksum, nil
}

// mksV2PollInterval is how often the _v2 waiters poll mk-api-v2. Tests shorten it.
var mksV2PollInterval = 10 * time.Second

// mksV2ReadAfterWaitTimeout bounds the read that saves a created object after
// its wait failed, which can be a timeout or an interrupt.
const mksV2ReadAfterWaitTimeout = 2 * time.Minute

// mksV2ReadAfterWaitContext outlives ctx, so a created object can still be
// read and saved when the wait for it ran out of time or was interrupted.
func mksV2ReadAfterWaitContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), mksV2ReadAfterWaitTimeout)
}

// mksV2PlannedState sets state to the plan with every unknown value null. It
// saves a created object that could not be read, so Terraform keeps it as
// tainted instead of losing it.
func mksV2PlannedState(plan tfsdk.Plan, state *tfsdk.State) diag.Diagnostics {
	var diags diag.Diagnostics

	raw, err := tftypes.Transform(plan.Raw, func(_ *tftypes.AttributePath, v tftypes.Value) (tftypes.Value, error) {
		if !v.IsKnown() {
			return tftypes.NewValue(v.Type(), nil), nil
		}

		return v, nil
	})
	if err != nil {
		diags.AddError("Error saving the planned state", err.Error())

		return diags
	}
	state.Raw = raw

	return diags
}

// isMKSV2NotFound reports whether mk-api-v2 answered 404.
func isMKSV2NotFound(err error) bool {
	var mksErr *mksclient.MKSError

	return errors.As(err, &mksErr) && mksErr.StatusCode == http.StatusNotFound
}

// mksV2TaskWaiter waits for the tasks one mutating mk-api-v2 call created.
// The API returns no task ID: its handlers insert the task rows in the same
// transaction before they respond, so the call's tasks are the ones listed
// after it and absent before it.
type mksV2TaskWaiter struct {
	client    *mksv2.ServiceClient
	clusterID string
	// nodegroupID is the scope: an empty one counts only the cluster tasks,
	// which have no nodegroup_id, any other only the tasks of that nodegroup.
	nodegroupID string
	before      map[string]struct{}
}

// newMKSV2TaskWaiter remembers the tasks in scope before a mutating call.
func newMKSV2TaskWaiter(ctx context.Context, client *mksv2.ServiceClient, clusterID, nodegroupID string) (*mksV2TaskWaiter, error) {
	w := newMKSV2CreatedTaskWaiter(client, clusterID, nodegroupID)

	tasks, err := w.list(ctx)
	if err != nil {
		return nil, err
	}
	for _, t := range tasks {
		w.before[t.Id] = struct{}{}
	}

	return w, nil
}

// newMKSV2CreatedTaskWaiter counts every task in scope, for an object that did
// not exist before the call.
func newMKSV2CreatedTaskWaiter(client *mksv2.ServiceClient, clusterID, nodegroupID string) *mksV2TaskWaiter {
	return &mksV2TaskWaiter{
		client:      client,
		clusterID:   clusterID,
		nodegroupID: nodegroupID,
		before:      map[string]struct{}{},
	}
}

// Wait polls the new tasks until all of them are DONE. No new tasks means the
// call was synchronous.
func (w *mksV2TaskWaiter) Wait(ctx context.Context) error {
	pending, err := w.newTasks(ctx)
	if err != nil {
		return err
	}

	return mksV2Poll(ctx, func() (bool, error) {
		pending, err = w.advance(ctx, pending)

		return len(pending) == 0, err
	})
}

// WaitClusterDeleted polls until the cluster is gone and fails when one of the
// new tasks fails first.
func (w *mksV2TaskWaiter) WaitClusterDeleted(ctx context.Context) error {
	return w.waitDeleted(ctx, func() error {
		_, err := cluster.Get(ctx, w.client, w.clusterID)

		return err
	})
}

// WaitNodegroupDeleted polls until the nodegroup of the waiter scope is gone
// and fails when one of the new tasks fails first.
func (w *mksV2TaskWaiter) WaitNodegroupDeleted(ctx context.Context) error {
	return w.waitDeleted(ctx, func() error {
		_, err := nodegroup.Get(ctx, w.client, w.clusterID, w.nodegroupID)

		return err
	})
}

// waitDeleted polls until get answers 404. A 404 on the tasks means the
// cluster, and everything in it, is gone.
func (w *mksV2TaskWaiter) waitDeleted(ctx context.Context, get func() error) error {
	pending, err := w.newTasks(ctx)
	if isMKSV2NotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}

	return mksV2Poll(ctx, func() (bool, error) {
		pending, err = w.advance(ctx, pending)
		if isMKSV2NotFound(err) {
			return true, nil
		}
		if err != nil {
			return false, err
		}

		err = get()
		if isMKSV2NotFound(err) {
			return true, nil
		}

		return false, err
	})
}

// list returns every task of the cluster in the waiter scope. It takes them
// in one call: the API orders tasks only by started_at, which tasks of one
// request share, so LIMIT/OFFSET pages could skip or repeat a task.
func (w *mksV2TaskWaiter) list(ctx context.Context) ([]mksclient.Task, error) {
	// The API turns limit 0 into no LIMIT.
	tasks, err := task.List(ctx, w.client, w.clusterID, 0, 0)
	if err != nil {
		return nil, fmt.Errorf("error listing tasks of cluster %s: %w", w.clusterID, err)
	}

	return slices.DeleteFunc(tasks, func(t mksclient.Task) bool {
		return valueOrZero(t.NodegroupId) != w.nodegroupID
	}), nil
}

func (w *mksV2TaskWaiter) newTasks(ctx context.Context) ([]mksclient.Task, error) {
	tasks, err := w.list(ctx)
	if err != nil {
		return nil, err
	}

	return slices.DeleteFunc(tasks, func(t mksclient.Task) bool {
		_, ok := w.before[t.Id]

		return ok
	}), nil
}

// advance refreshes the pending tasks and returns the ones still running.
func (w *mksV2TaskWaiter) advance(ctx context.Context, pending []mksclient.Task) ([]mksclient.Task, error) {
	var running []mksclient.Task
	for _, p := range pending {
		t, err := task.Get(ctx, w.client, w.clusterID, p.Id)
		if err != nil {
			return pending, fmt.Errorf("error getting task %s of cluster %s: %w", p.Id, w.clusterID, err)
		}

		switch t.Status {
		case mksclient.DONE:
		case mksclient.ERROR, mksclient.CANCELED:
			return nil, mksV2TaskError(t)
		case mksclient.INQUEUE, mksclient.INPROGRESS:
			running = append(running, *t)
		default:
			// A status newer than the client: keep polling.
			running = append(running, *t)
		}
	}

	return running, nil
}

func mksV2TaskError(t *mksclient.Task) error {
	msg := fmt.Sprintf("task %s %s of cluster %s ended in %s", t.Type, t.Id, t.ClusterId, t.Status)
	if t.ErrorDetails != nil {
		msg += fmt.Sprintf(": %s (code %d)", t.ErrorDetails.Name, t.ErrorDetails.Code)
		details := valueOrZero(t.ErrorDetails.Details)
		if details != "" {
			msg += ": " + details
		}
	}

	return errors.New(msg)
}

// mksV2Poll calls check every mksV2PollInterval until it reports done, fails,
// or ctx ends.
func mksV2Poll(ctx context.Context, check func() (bool, error)) error {
	for {
		done, err := check()
		if err != nil || done {
			return err
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout while waiting: %w", ctx.Err())
		case <-time.After(mksV2PollInterval):
		}
	}
}
