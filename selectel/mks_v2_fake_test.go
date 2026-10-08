package selectel

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	dedicated "github.com/selectel/dedicated-go/v2/pkg/v2"
	mksv2 "github.com/selectel/mks-go/v2/pkg"
	"github.com/selectel/mks-go/v2/pkg/mksclient"
	"github.com/terraform-providers/terraform-provider-selectel/version"
)

// mksV2Fake is an in-memory mk-api-v2 behind httptest.Server. Tests seed it,
// serve the provider with its clients through providerFactories and read what
// the provider sent.
type mksV2Fake struct {
	server *httptest.Server

	mu       sync.Mutex
	clusters map[string]mksclient.ClusterDetailed
	// nodegroups are keyed by their ID, which the fake numbers ng-1, ng-2...
	nodegroups           map[string]mksclient.NodegroupDetailed
	nodegroupSeq         int
	kubeconfigs          map[string]string
	kubeVersions         []mksclient.KubeVersionInfo
	featureGates         []mksclient.AvailableFeatureGates
	admissionControllers []mksclient.AvailableAdmissionControllers
	// failures maps a route pattern to the HTTP status it answers with.
	failures map[string]int
	// failureBodies maps a route pattern to the raw body its forced failure
	// answers with instead of the mk-api-v2 error.
	failureBodies map[string]mksV2FakeBody
	// callFailures maps a route pattern and a call number to the HTTP status
	// that call answers with.
	callFailures map[string]map[int]int
	// createNodegroupsStatus is the status create answers with after it has
	// stored the nodegroups, like mk-api-v2 when the task publish fails.
	createNodegroupsStatus int
	// concurrentNodegroups are created along with the next create request, as
	// if someone else created them in the meantime.
	concurrentNodegroups []mksclient.NodegroupDetailed
	tasks                []*mksV2FakeTask
	// failedTaskTypes makes new tasks of a type end in ERROR.
	failedTaskTypes map[string]bool
	// stuckTaskTypes keeps the running tasks of a type running.
	stuckTaskTypes map[string]bool
	// x509CACertificates keeps what PATCH stored; the API never returns it.
	x509CACertificates map[string]string
	// pricePlans are what the dedicated servers API lists.
	pricePlans dedicated.PricePlans
	// queries keeps the last query string of every route.
	queries map[string]string
	// bodies keeps the last request body of every route, calls counts them.
	bodies map[string][]byte
	calls  map[string]int

	// clients records every client built, userAgents every request, and
	// configUserAgent is the User-Agent of the provider Config.
	clients         []mksV2FakeClient
	userAgents      []string
	configUserAgent string
}

// mksV2FakeTask finishes on its first GET, unless stuck, and then applies
// effect when it is DONE.
// mksV2FakeBody is a raw response body and its content type.
type mksV2FakeBody struct {
	contentType string
	body        string
}

type mksV2FakeTask struct {
	task   mksclient.Task
	stuck  bool
	effect func()
}

type mksV2FakeClient struct {
	projectID string
	pool      string
	userAgent string
}

// testMKSV2KeystoneProject is a project ID the way Keystone takes it, and
// testMKSV2APIProject is the same project the way mk-api-v2 returns it.
const (
	testMKSV2KeystoneProject = "35b965e88e5948488eed5e509833033c"
	testMKSV2APIProject      = "35b965e8-8e59-4848-8eed-5e509833033c"
)

const (
	mksV2RouteCluster              = "GET /v2/clusters/{cluster_id}"
	mksV2RouteKubeconfig           = "GET /v2/clusters/{cluster_id}/kubeconfig"
	mksV2RouteKubeVersions         = "GET /v2/kubeversions"
	mksV2RouteFeatureGates         = "GET /v2/feature-gates"
	mksV2RouteAdmissionControllers = "GET /v2/admission-controllers"
	mksV2RouteCreateCluster        = "POST /v2/clusters"
	mksV2RoutePatchCluster         = "PATCH /v2/clusters/{cluster_id}"
	mksV2RouteDeleteCluster        = "DELETE /v2/clusters/{cluster_id}"
	mksV2RouteUpgradePatch         = "POST /v2/clusters/{cluster_id}/upgrade-patch-version"
	mksV2RouteUpgradeMinor         = "POST /v2/clusters/{cluster_id}/upgrade-minor-version"
	mksV2RouteTasks                = "GET /v2/clusters/{cluster_id}/tasks"
	mksV2RouteTask                 = "GET /v2/clusters/{cluster_id}/tasks/{task_id}"
	mksV2RouteNodegroups           = "GET /v2/clusters/{cluster_id}/nodegroups"
	mksV2RouteCreateNodegroups     = "POST /v2/clusters/{cluster_id}/nodegroups"
	mksV2RouteNodegroup            = "GET /v2/clusters/{cluster_id}/nodegroups/{nodegroup_id}"
	mksV2RoutePatchNodegroup       = "PATCH /v2/clusters/{cluster_id}/nodegroups/{nodegroup_id}"
	mksV2RouteDeleteNodegroup      = "DELETE /v2/clusters/{cluster_id}/nodegroups/{nodegroup_id}"
	mksV2RouteResizeNodegroup      = "POST /v2/clusters/{cluster_id}/nodegroups/{nodegroup_id}/resize"
	// mksV2RoutePricePlans is the dedicated servers API route of the price
	// plans, served by the same fake.
	mksV2RoutePricePlans = "GET /pub/plan"
)

// mksV2FakePollInterval shortens the waiter polls for every test: the fake
// answers at once. It is set once and never restored, so parallel tests do
// not race on it.
var mksV2FakePollInterval sync.Once

// newMKSV2Fake starts the fake. It touches no package state but the poll
// interval, so tests using it may call t.Parallel.
func newMKSV2Fake(t *testing.T) *mksV2Fake {
	t.Helper()

	f := &mksV2Fake{
		clusters:    map[string]mksclient.ClusterDetailed{},
		nodegroups:  map[string]mksclient.NodegroupDetailed{},
		kubeconfigs: map[string]string{},
		failures:    map[string]int{},

		failureBodies: map[string]mksV2FakeBody{},

		callFailures: map[string]map[int]int{},

		failedTaskTypes:    map[string]bool{},
		stuckTaskTypes:     map[string]bool{},
		x509CACertificates: map[string]string{},
		queries:            map[string]string{},
		bodies:             map[string][]byte{},
		calls:              map[string]int{},
	}

	mux := http.NewServeMux()
	f.handle(mux, mksV2RouteCluster, f.getCluster)
	f.handle(mux, mksV2RouteKubeconfig, f.getKubeconfig)
	f.handle(mux, mksV2RouteKubeVersions, func(w http.ResponseWriter, _ *http.Request) {
		writeMKSV2JSON(w, http.StatusOK, mksclient.KubeVersionsList{KubeVersions: &f.kubeVersions})
	})
	f.handle(mux, mksV2RouteFeatureGates, func(w http.ResponseWriter, _ *http.Request) {
		writeMKSV2JSON(w, http.StatusOK, mksclient.FeatureGatesList{FeatureGates: &f.featureGates})
	})
	f.handle(mux, mksV2RouteAdmissionControllers, func(w http.ResponseWriter, _ *http.Request) {
		writeMKSV2JSON(w, http.StatusOK, mksclient.AdmissionControllersList{AdmissionControllers: &f.admissionControllers})
	})
	f.handle(mux, mksV2RouteCreateCluster, f.createCluster)
	f.handle(mux, mksV2RoutePatchCluster, f.patchCluster)
	f.handle(mux, mksV2RouteDeleteCluster, f.deleteCluster)
	f.handle(mux, mksV2RouteUpgradePatch, f.upgradeCluster(false))
	f.handle(mux, mksV2RouteUpgradeMinor, f.upgradeCluster(true))
	f.handle(mux, mksV2RouteTasks, f.listTasks)
	f.handle(mux, mksV2RouteTask, f.getTask)
	f.handle(mux, mksV2RouteNodegroups, f.listNodegroups)
	f.handle(mux, mksV2RouteCreateNodegroups, f.createNodegroups)
	f.handle(mux, mksV2RouteNodegroup, f.getNodegroup)
	f.handle(mux, mksV2RoutePatchNodegroup, f.patchNodegroup)
	f.handle(mux, mksV2RouteDeleteNodegroup, f.deleteNodegroup)
	f.handle(mux, mksV2RouteResizeNodegroup, f.resizeNodegroup)
	f.handle(mux, mksV2RoutePricePlans, func(w http.ResponseWriter, _ *http.Request) {
		writeMKSV2JSON(w, http.StatusOK, struct {
			Result dedicated.PricePlans `json:"result"`
		}{Result: f.pricePlans})
	})
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)

	mksV2FakePollInterval.Do(func() { mksV2PollInterval = time.Millisecond })

	return f
}

// providerFactories serve the provider like testAccProtoV6ProviderFactories,
// with a Config of its own whose _v2 clients talk to the fake.
func (f *mksV2Fake) providerFactories() map[string]func() (tfprotov6.ProviderServer, error) {
	return map[string]func() (tfprotov6.ProviderServer, error){
		"selectel": func() (tfprotov6.ProviderServer, error) {
			sdk := Provider("test")
			// The framework provider takes its Config from the SDKv2 one, so a
			// per-test Config replaces the SDKv2 configure step.
			sdk.ConfigureContextFunc = func(_ context.Context, d *schema.ResourceData) (any, diag.Diagnostics) {
				return f.config(sdk.UserAgent(version.ProviderName, "test"), func(key string) string {
					v, _ := d.Get(key).(string)

					return v
				}), nil
			}
			server, err := muxProviderServer(context.Background(), sdk, testFrameworkProvider{&frameworkProvider{
				sdk: sdk, version: "test",
			}})
			if err != nil {
				return nil, err
			}

			return server(), nil
		},
	}
}

// config builds the provider Config like getConfig, but for this test only.
func (f *mksV2Fake) config(userAgent string, attr func(key string) string) *Config {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.configUserAgent = userAgent

	return &Config{
		ProjectID:    attr("project_id"),
		Region:       attr("region"),
		UserAgent:    userAgent,
		dedicatedURL: f.server.URL,
		mksV2Client: func(_ context.Context, config *Config, projectID, pool string) (*mksv2.ServiceClient, error) {
			f.mu.Lock()
			f.clients = append(f.clients, mksV2FakeClient{projectID: projectID, pool: pool, userAgent: config.UserAgent})
			f.mu.Unlock()
			// Keystone answers 401 for a project scope by the dashed form.
			if projectID != mksV2KeystoneProjectID(projectID) && mksV2FakeAPIProjectID(mksV2KeystoneProjectID(projectID)) == projectID {
				return nil, fmt.Errorf("authentication failed for project %q", projectID)
			}

			return newMKSV2ServiceClient("fake-token", f.server.URL, config.UserAgent)
		},
	}
}

// handle registers a route that records the User-Agent and answers with a
// forced failure when one is set.
func (f *mksV2Fake) handle(mux *http.ServeMux, pattern string, handler http.HandlerFunc) {
	mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()

		f.userAgents = append(f.userAgents, r.Header.Get("User-Agent"))
		f.calls[pattern]++
		f.queries[pattern] = r.URL.RawQuery
		body, _ := io.ReadAll(r.Body)
		if len(body) > 0 {
			f.bodies[pattern] = body
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		status, ok := f.failures[pattern]
		if !ok {
			status, ok = f.callFailures[pattern][f.calls[pattern]]
		}
		if raw, hasBody := f.failureBodies[pattern]; ok && hasBody {
			w.Header().Set("Content-Type", raw.contentType)
			w.WriteHeader(status)
			_, _ = io.WriteString(w, raw.body)

			return
		}
		if ok {
			objectType, id := mksV2FakeObject(r)
			writeMKSV2Error(w, status, objectType, id)

			return
		}

		handler(w, r)
	})
}

func (f *mksV2Fake) getCluster(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("cluster_id")
	c, ok := f.clusters[id]
	if !ok {
		writeMKSV2Error(w, http.StatusNotFound, mksV2ObjectCluster, id)

		return
	}

	writeMKSV2JSON(w, http.StatusOK, mksclient.ClusterResp{Cluster: &c})
}

func (f *mksV2Fake) getKubeconfig(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("cluster_id")
	kubeconfig, ok := f.kubeconfigs[id]
	if !ok {
		writeMKSV2Error(w, http.StatusNotFound, mksV2ObjectCluster, id)

		return
	}

	w.Header().Set("Content-Type", "application/yaml")
	_, _ = w.Write([]byte(kubeconfig))
}

// createCluster fills what the API defaults and starts CREATE_CLUSTER. The
// cluster gets testMKSV2ClusterID and the project of the last client, dashed
// like mk-api-v2 returns it.
func (f *mksV2Fake) createCluster(w http.ResponseWriter, r *http.Request) {
	var body mksclient.ClusterCreateBody
	err := json.NewDecoder(r.Body).Decode(&body)
	if err != nil || body.Cluster == nil {
		writeMKSV2Error(w, http.StatusBadRequest, mksV2ObjectCluster, "")

		return
	}
	opts := body.Cluster

	clusterType := mksclient.HIGHAVAILABILITY
	if opts.ClusterType != nil {
		clusterType = mksclient.ClusterDetailedClusterType(*opts.ClusterType)
	}
	cniType := mksclient.ClusterDetailedCniType(mksclient.ClusterCniTypeCALICO)
	if opts.CniType != nil {
		cniType = mksclient.ClusterDetailedCniType(*opts.CniType)
	}
	// Like validate/cluster.go, the name is stored in lower case.
	c := mksclient.ClusterDetailed{
		Id:                            testMKSV2ClusterID,
		Name:                          strings.ToLower(opts.Name),
		Pool:                          opts.Pool,
		ProjectId:                     mksV2FakeAPIProjectID(f.clients[len(f.clients)-1].projectID),
		KubeVersion:                   opts.KubeVersion,
		ClusterType:                   clusterType,
		Basic:                         clusterType == mksclient.BASIC,
		NetworkType:                   mksclient.ClusterDetailedNetworkType(opts.NetworkType),
		NetworkId:                     cmp.Or(opts.NetworkId, "fake-network"),
		SubnetId:                      cmp.Or(opts.SubnetId, "fake-subnet"),
		PrivateKubeApi:                opts.PrivateKubeApi,
		EnableAutorepair:              valueOr(opts.EnableAutorepair, true),
		EnablePatchVersionAutoUpgrade: valueOr(opts.EnablePatchVersionAutoUpgrade, clusterType != mksclient.BASIC),
		CniType:                       cniType,
		KubeApiIp:                     "192.0.2.10",
		Status:                        "CREATING",
	}
	setMKSV2FakeWindow(&c, cmp.Or(opts.MaintenanceWindowStart, "03:00:00"))
	if cniType == mksclient.ClusterDetailedCniType(mksclient.ClusterCniTypeCILIUM) {
		cilium := valueOrZero(opts.CniCiliumSettings)
		c.CniCiliumSettings = &mksclient.CNICiliumSettings{
			EnvoyDaemonset: new(valueOr(cilium.EnvoyDaemonset, true)),
			HubbleRelay:    new(valueOr(cilium.HubbleRelay, true)),
		}
	}
	if opts.KubernetesOptions != nil {
		c.KubernetesOptions = *opts.KubernetesOptions
		c.KubernetesOptions.Oidc = normaliseMKSV2FakeOIDC(c.KubernetesOptions.Oidc)
		// Create ignores them, see mk-api-v2 protoadapter/create_cluster.go.
		c.KubernetesOptions.X509CaCertificates = ""
	}
	f.clusters[c.Id] = c

	f.addTask(c.Id, "CREATE_CLUSTER", f.activateCluster(c.Id))
	writeMKSV2JSON(w, http.StatusCreated, mksclient.ClusterResp{Cluster: &c})
}

// patchCluster applies the sent fields like mk-api-v2 handlers/clusters/
// partial_update.go: a kube options or Cilium change starts a task and moves
// the cluster to PENDING_UPGRADE_CLUSTER_CONFIG until the task is DONE.
func (f *mksV2Fake) patchCluster(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("cluster_id")
	c, ok := f.clusters[id]
	if !ok {
		writeMKSV2Error(w, http.StatusNotFound, mksV2ObjectCluster, id)

		return
	}
	var body mksclient.ClusterUpdateBody
	err := json.NewDecoder(r.Body).Decode(&body)
	if err != nil || body.Cluster == nil {
		writeMKSV2Error(w, http.StatusBadRequest, mksV2ObjectCluster, id)

		return
	}
	opts := body.Cluster

	if opts.MaintenanceWindowStart != nil {
		setMKSV2FakeWindow(&c, *opts.MaintenanceWindowStart)
	}
	c.EnableAutorepair = valueOr(opts.EnableAutorepair, c.EnableAutorepair)
	c.EnablePatchVersionAutoUpgrade = valueOr(opts.EnablePatchVersionAutoUpgrade, c.EnablePatchVersionAutoUpgrade)
	if opts.CniCiliumSettings != nil {
		cilium := valueOrZero(c.CniCiliumSettings)
		cilium.EnvoyDaemonset = cmp.Or(opts.CniCiliumSettings.EnvoyDaemonset, cilium.EnvoyDaemonset)
		cilium.HubbleRelay = cmp.Or(opts.CniCiliumSettings.HubbleRelay, cilium.HubbleRelay)
		c.CniCiliumSettings = &cilium
		c.Status = "PENDING_UPGRADE_CLUSTER_CONFIG"
		f.addTask(id, "UPGRADE_CLUSTER_CONFIG", f.activateCluster(id))
	}
	if opts.KubernetesOptions != nil && f.patchKubeOptions(&c, *opts.KubernetesOptions) {
		c.Status = "PENDING_UPGRADE_CLUSTER_CONFIG"
		f.addTask(id, "UPGRADE_MASTERS_CONFIG", f.activateCluster(id))
	}
	f.clusters[id] = c

	writeMKSV2JSON(w, http.StatusOK, mksclient.ClusterResp{Cluster: &c})
}

// patchKubeOptions stores the changed options the way the API does and
// reports whether anything changed: audit_logs only with enabled, OIDC only
// with a field other than provider_name, x509 whenever it is not empty.
func (f *mksV2Fake) patchKubeOptions(c *mksclient.ClusterDetailed, requested mksclient.KubernetesOptions) bool {
	current := &c.KubernetesOptions
	requested.Oidc = normaliseMKSV2FakeOIDC(requested.Oidc)
	changed := false

	if !slices.Equal(current.FeatureGates, requested.FeatureGates) {
		current.FeatureGates = requested.FeatureGates
		changed = true
	}
	if !slices.Equal(current.AdmissionControllers, requested.AdmissionControllers) {
		current.AdmissionControllers = requested.AdmissionControllers
		changed = true
	}
	if current.AuditLogs.Enabled != requested.AuditLogs.Enabled {
		current.AuditLogs = requested.AuditLogs
		changed = true
	}
	oidcChanged := current.Oidc
	oidcChanged.ProviderName = requested.Oidc.ProviderName
	if oidcChanged != requested.Oidc {
		current.Oidc = requested.Oidc
		changed = true
	}
	if requested.X509CaCertificates != "" {
		f.x509CACertificates[c.Id] = requested.X509CaCertificates
		changed = true
	}

	return changed
}

// normaliseMKSV2FakeOIDC stores OIDC like mk-api-v2 daladapter/
// kubernetes_options.go: ca_certs trimmed, a disabled OIDC wiped.
func normaliseMKSV2FakeOIDC(oidc mksclient.OIDC) mksclient.OIDC {
	if !oidc.Enabled {
		return mksclient.OIDC{}
	}
	oidc.CaCerts = strings.TrimSpace(oidc.CaCerts)

	return oidc
}

// activateCluster is the effect of a finished cluster task.
func (f *mksV2Fake) activateCluster(id string) func() {
	return func() {
		c, ok := f.clusters[id]
		if ok {
			c.Status = "ACTIVE"
			f.clusters[id] = c
		}
	}
}

// deleteCluster removes the cluster when DELETE_CLUSTER is DONE.
func (f *mksV2Fake) deleteCluster(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("cluster_id")
	_, ok := f.clusters[id]
	if !ok {
		writeMKSV2Error(w, http.StatusNotFound, mksV2ObjectCluster, id)

		return
	}

	f.addTask(id, "DELETE_CLUSTER", func() { delete(f.clusters, id) })
	w.WriteHeader(http.StatusNoContent)
}

// upgradeCluster moves the cluster to the latest patch version of its minor
// version, or of the next one, when the task is DONE.
func (f *mksV2Fake) upgradeCluster(minor bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("cluster_id")
		c, ok := f.clusters[id]
		if !ok {
			writeMKSV2Error(w, http.StatusNotFound, mksV2ObjectCluster, id)

			return
		}

		target, taskType := kubeVersionTrimToMinor, "UPGRADE_PATCH_VERSION"
		if minor {
			target, taskType = kubeVersionTrimToMinorIncremented, "UPGRADE_MINOR_VERSION"
		}
		targetMinor, err := target(c.KubeVersion)
		if err != nil {
			writeMKSV2Error(w, http.StatusBadRequest, mksV2ObjectCluster, id)

			return
		}
		latest, err := latestKubePatchVersions(mksKubeVersionsV2ToV1Views(f.kubeVersions))
		if err != nil || latest[targetMinor] == "" {
			writeMKSV2Error(w, http.StatusBadRequest, mksV2ObjectCluster, id)

			return
		}

		f.addTask(id, taskType, func() {
			c := f.clusters[id]
			c.KubeVersion = latest[targetMinor]
			f.clusters[id] = c
		})
		writeMKSV2JSON(w, http.StatusOK, mksclient.ClusterResp{Cluster: &c})
	}
}

// listTasks pages the cluster tasks newest first, like the API.
func (f *mksV2Fake) listTasks(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("cluster_id")
	if _, ok := f.clusters[id]; !ok {
		writeMKSV2Error(w, http.StatusNotFound, mksV2ObjectCluster, id)

		return
	}

	var tasks []mksclient.Task
	for _, t := range slices.Backward(f.tasks) {
		if t.task.ClusterId == id {
			tasks = append(tasks, t.task)
		}
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	tasks = tasks[min(offset, len(tasks)):]
	if limit > 0 {
		tasks = tasks[:min(limit, len(tasks))]
	}

	writeMKSV2JSON(w, http.StatusOK, mksclient.TaskList{Count: int64(len(tasks)), Tasks: tasks})
}

// getTask finishes a running task: DONE with its effect, or ERROR for a type
// set by failTasks.
func (f *mksV2Fake) getTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("cluster_id")
	taskID := r.PathValue("task_id")
	i := slices.IndexFunc(f.tasks, func(t *mksV2FakeTask) bool { return t.task.Id == taskID && t.task.ClusterId == id })
	_, clusterExists := f.clusters[id]
	if !clusterExists {
		writeMKSV2Error(w, http.StatusNotFound, mksV2ObjectCluster, id)

		return
	}
	if i < 0 {
		writeMKSV2Error(w, http.StatusNotFound, mksV2ObjectTask, taskID)

		return
	}

	t := f.tasks[i]
	running := t.task.Status == mksclient.INQUEUE || t.task.Status == mksclient.INPROGRESS
	switch {
	case !running || t.stuck || f.stuckTaskTypes[t.task.Type]:
	case f.failedTaskTypes[t.task.Type]:
		t.task.Status = mksclient.ERROR
		t.task.ErrorDetails = &mksclient.TaskErrorDetails{
			Code: 42, Name: "fake failure", Details: new("fake details of " + t.task.Type),
		}
	default:
		t.task.Status = mksclient.DONE
		if t.effect != nil {
			t.effect()
		}
	}

	writeMKSV2JSON(w, http.StatusOK, mksclient.TaskResp{Task: t.task})
}

// addTask starts a task the way an API handler does before it responds.
func (f *mksV2Fake) addTask(clusterID, taskType string, effect func()) {
	f.tasks = append(f.tasks, &mksV2FakeTask{
		task: mksclient.Task{
			Id:        fmt.Sprintf("task-%d", len(f.tasks)+1),
			ClusterId: clusterID,
			Type:      taskType,
			Status:    mksclient.INPROGRESS,
		},
		effect: effect,
	})
}

// seedTask adds a task of a nodegroup, or of the cluster when nodegroupID is
// empty; a stuck one never finishes.
func (f *mksV2Fake) seedTask(clusterID, nodegroupID, taskType string, stuck bool) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.addTask(clusterID, taskType, nil)
	t := f.tasks[len(f.tasks)-1]
	t.stuck = stuck
	if nodegroupID != "" {
		t.task.NodegroupId = &nodegroupID
	}
}

// stickTasks keeps the running tasks of taskType running, or lets them finish
// again when stuck is false.
func (f *mksV2Fake) stickTasks(taskType string, stuck bool) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.stuckTaskTypes[taskType] = stuck
}

// failTasks makes the tasks of taskType started from now on end in ERROR, or
// finish normally again when failed is false.
func (f *mksV2Fake) failTasks(taskType string, failed bool) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.failedTaskTypes[taskType] = failed
}

// lastBody decodes the last request body of the route.
func (f *mksV2Fake) lastBody(t *testing.T, route string) map[string]any {
	t.Helper()

	f.mu.Lock()
	defer f.mu.Unlock()

	var body map[string]any
	err := json.Unmarshal(f.bodies[route], &body)
	if err != nil {
		t.Fatalf("decode the last %s body %q: %s", route, f.bodies[route], err)
	}

	return body
}

func (f *mksV2Fake) callCount(route string) int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.calls[route]
}

// clusterCount is the number of clusters the fake still has.
func (f *mksV2Fake) clusterCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return len(f.clusters)
}

// updateCluster changes a cluster behind the provider's back.
func (f *mksV2Fake) updateCluster(id string, update func(c *mksclient.ClusterDetailed)) {
	f.mu.Lock()
	defer f.mu.Unlock()

	c := f.clusters[id]
	update(&c)
	f.clusters[id] = c
}

// removeCluster deletes a cluster behind the provider's back.
func (f *mksV2Fake) removeCluster(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	delete(f.clusters, id)
}

// setMKSV2FakeWindow sets a three-hour maintenance window.
func setMKSV2FakeWindow(c *mksclient.ClusterDetailed, start string) {
	c.MaintenanceWindowStart = start
	parsed, err := time.Parse(time.TimeOnly, start)
	if err != nil {
		c.MaintenanceWindowEnd = ""

		return
	}
	c.MaintenanceWindowEnd = parsed.Add(3 * time.Hour).Format(time.TimeOnly)
}

func valueOr[T any](v *T, fallback T) T {
	if v == nil {
		return fallback
	}

	return *v
}

// seedCluster adds a cluster and the kubeconfig the API returns for it.
// clusterProject returns the project_id the fake returns for the cluster.
func (f *mksV2Fake) clusterProject(id string) string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.clusters[id].ProjectId
}

func (f *mksV2Fake) seedCluster(c mksclient.ClusterDetailed, kubeconfig string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.clusters[c.Id] = c
	f.kubeconfigs[c.Id] = kubeconfig
}

func (f *mksV2Fake) seedKubeVersions(versions ...mksclient.KubeVersionInfo) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.kubeVersions = versions
}

func (f *mksV2Fake) seedFeatureGates(featureGates ...mksclient.AvailableFeatureGates) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.featureGates = featureGates
}

func (f *mksV2Fake) seedAdmissionControllers(admissionControllers ...mksclient.AvailableAdmissionControllers) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.admissionControllers = admissionControllers
}

// fail makes the route answer with status until the test ends, or answer
// normally again when status is 0.
func (f *mksV2Fake) fail(pattern string, status int) {
	f.mu.Lock()
	defer f.mu.Unlock()

	delete(f.failureBodies, pattern)
	if status == 0 {
		delete(f.failures, pattern)

		return
	}
	f.failures[pattern] = status
}

// failWithBody makes the route answer with status and the raw body until the
// test ends, like a gateway or a status the mk-api-v2 swagger does not declare.
func (f *mksV2Fake) failWithBody(pattern string, status int, contentType, body string) {
	f.fail(pattern, status)

	f.mu.Lock()
	defer f.mu.Unlock()

	f.failureBodies[pattern] = mksV2FakeBody{contentType: contentType, body: body}
}

// failCalls makes the given calls of the route, counted from 1 over the whole
// test, answer with status.
func (f *mksV2Fake) failCalls(pattern string, status int, calls ...int) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.callFailures[pattern] == nil {
		f.callFailures[pattern] = map[int]int{}
	}
	for _, call := range calls {
		f.callFailures[pattern][call] = status
	}
}

// lastQuery returns the query string of the last request to the route.
func (f *mksV2Fake) lastQuery(route string) string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.queries[route]
}

// storedX509 returns the X509 CA certificates PATCH stored for the cluster.
func (f *mksV2Fake) storedX509(id string) string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.x509CACertificates[id]
}

// mksV2FakeAPIProjectID returns a Keystone project ID the way mk-api-v2
// returns it, as a dashed UUID (apiadapter/clusters.go). An ID that is not 32
// characters long stays as is.
func mksV2FakeAPIProjectID(projectID string) string {
	if len(projectID) != 32 {
		return projectID
	}

	return projectID[:8] + "-" + projectID[8:12] + "-" + projectID[12:16] + "-" + projectID[16:20] + "-" + projectID[20:]
}

// checkClients fails the test unless every client was built for the Keystone
// form of projectID and every request carried the User-Agent of the provider
// Config.
func (f *mksV2Fake) checkClients(t *testing.T, projectID string) {
	t.Helper()

	projectID = mksV2KeystoneProjectID(projectID)

	f.mu.Lock()
	defer f.mu.Unlock()

	if len(f.clients) == 0 || len(f.userAgents) == 0 {
		t.Fatalf("the fake mk-api-v2 got %d clients and %d requests, want both > 0", len(f.clients), len(f.userAgents))
	}
	for _, c := range f.clients {
		if c.projectID != projectID {
			t.Errorf("client built for project %q, want %q", c.projectID, projectID)
		}
		if c.userAgent == "" || c.userAgent != f.configUserAgent {
			t.Errorf("client built with User-Agent %q, want the Config one %q", c.userAgent, f.configUserAgent)
		}
	}
	for _, userAgent := range f.userAgents {
		if userAgent != f.configUserAgent {
			t.Errorf("request sent with User-Agent %q, want %q", userAgent, f.configUserAgent)
		}
	}
}

func writeMKSV2JSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// The object types of mk-api-v2 not-found errors, see its
// models/objects/objects.go.
const (
	mksV2ObjectCluster   = "Cluster"
	mksV2ObjectNodegroup = "Nodegroup"
	mksV2ObjectTask      = "Task"
)

// mksV2FakeObject names the most specific object of the request path.
func mksV2FakeObject(r *http.Request) (string, string) {
	if id := r.PathValue("task_id"); id != "" {
		return mksV2ObjectTask, id
	}
	if id := r.PathValue("nodegroup_id"); id != "" {
		return mksV2ObjectNodegroup, id
	}

	return mksV2ObjectCluster, r.PathValue("cluster_id")
}

// writeMKSV2Error answers like mk-api-v2 handlers/common/errors.go: a 404
// names the object type in the message and carries the ID in error.id.
func writeMKSV2Error(w http.ResponseWriter, status int, objectType, id string) {
	if status == http.StatusNotFound {
		var body mksclient.GenericNotFoundError
		body.Error.Id = id
		body.Error.Message = objectType + " not found"
		writeMKSV2JSON(w, status, body)

		return
	}

	var body mksclient.GenericError
	body.Error.Message = fmt.Sprintf("fake error %d", status)
	writeMKSV2JSON(w, status, body)
}

// listNodegroups lists the nodegroups of the cluster.
func (f *mksV2Fake) listNodegroups(w http.ResponseWriter, r *http.Request) {
	clusterID := r.PathValue("cluster_id")
	if _, ok := f.clusters[clusterID]; !ok {
		writeMKSV2Error(w, http.StatusNotFound, mksV2ObjectCluster, clusterID)

		return
	}

	items := []mksclient.NodegroupListItem{}
	for _, id := range slices.Sorted(maps.Keys(f.nodegroups)) {
		ng := f.nodegroups[id]
		if ng.ClusterId != clusterID {
			continue
		}
		items = append(items, mksclient.NodegroupListItem{
			Id:                       ng.Id,
			ClusterId:                ng.ClusterId,
			Segment:                  ng.Segment,
			Status:                   mksclient.NodegroupListItemStatus(ng.Status),
			Nodes:                    ng.Nodes,
			Labels:                   ng.Labels,
			CloudNodegroupConfig:     ng.CloudNodegroupConfig,
			DedicatedNodegroupConfig: ng.DedicatedNodegroupConfig,
		})
	}

	writeMKSV2JSON(w, http.StatusOK, mksclient.NodegroupList{Nodegroups: items})
}

// mksV2FakeFlavors are the flavors whose specs the fake applies like mk-api-v2
// validate/flavor.go: a local disk forces local_volume, a volume spec
// replaces volume_gb. Other flavor IDs have neither.
var mksV2FakeFlavors = map[string]struct {
	localDisk bool
	volumeGB  int64
}{
	"local-1013":  {localDisk: true},
	"volume-1013": {volumeGB: 50},
}

// createNodegroups fills what the API defaults and starts a CLUSTER_RESIZE
// task per nodegroup, which adds the nodes. Like the API it answers 204 with
// no body, never returns the CIDR of a cloud nodegroup, and takes cloud
// nodegroups only in STANDARD clusters, dedicated ones only in L3VPN ones.
func (f *mksV2Fake) createNodegroups(w http.ResponseWriter, r *http.Request) {
	clusterID := r.PathValue("cluster_id")
	if _, ok := f.clusters[clusterID]; !ok {
		writeMKSV2Error(w, http.StatusNotFound, mksV2ObjectCluster, clusterID)

		return
	}
	var body mksclient.NodegroupsCreateBody
	err := json.NewDecoder(r.Body).Decode(&body)
	if err != nil || len(body.Nodegroups) == 0 {
		writeMKSV2Error(w, http.StatusBadRequest, mksV2ObjectCluster, clusterID)

		return
	}

	l3vpn := f.clusters[clusterID].NetworkType == mksclient.ClusterDetailedNetworkTypeL3VPN
	for _, opts := range body.Nodegroups {
		cloud := valueOrZero(opts.CloudNodegroupConfig)
		switch {
		case (opts.CloudNodegroupConfig == nil) == (opts.DedicatedNodegroupConfig == nil),
			// validate/nodegroup.go:97-106.
			opts.DedicatedNodegroupConfig != nil && (!l3vpn || opts.DedicatedNodegroupConfig.PricePlanUuid == ""),
			// validate/nodegroup.go:309, :317.
			opts.CloudNodegroupConfig != nil && l3vpn,
			// validate/nodegroup.go:391.
			cloud.FlavorId == "" && opts.CloudNodegroupConfig != nil && valueOr(opts.InstallNvidiaDevicePlugin, false):
			// validate/flavor.go ErrorInstallNDPWithoutFlavor.
			writeMKSV2Error(w, http.StatusBadRequest, mksV2ObjectNodegroup, "")

			return
		}
	}
	for _, ng := range f.concurrentNodegroups {
		f.nodegroups[ng.Id] = ng
	}
	f.concurrentNodegroups = nil

	for _, opts := range body.Nodegroups {
		f.nodegroupSeq++
		id := fmt.Sprintf("ng-%d", f.nodegroupSeq)
		cloud := valueOrZero(opts.CloudNodegroupConfig)
		if cloud.FlavorId != "" {
			flavor := mksV2FakeFlavors[cloud.FlavorId]
			cloud.LocalVolume = cloud.LocalVolume || flavor.localDisk
			cloud.VolumeGb = cmp.Or(flavor.volumeGB, cloud.VolumeGb)
		}
		ng := mksclient.NodegroupDetailed{
			Id:                        id,
			ClusterId:                 clusterID,
			Segment:                   opts.Segment,
			Status:                    mksclient.NodegroupDetailedStatusPENDINGCREATE,
			NodegroupType:             mksclient.NodegroupDetailedNodegroupTypeSTANDARD,
			EnableAutoscale:           valueOr(opts.EnableAutoscale, false),
			AutoscaleMinNodes:         new(valueOr(opts.AutoscaleMinNodes, 0)),
			AutoscaleMaxNodes:         new(valueOr(opts.AutoscaleMaxNodes, 0)),
			InstallNvidiaDevicePlugin: valueOr(opts.InstallNvidiaDevicePlugin, false),
			Preemptible:               opts.Preemptible,
			Labels:                    valueOr(opts.Labels, map[string]string{}),
			Taints:                    valueOr(opts.Taints, []mksclient.NodegroupTaint{}),
			UserData:                  opts.UserData,
			Nodes:                     []mksclient.Node{},
			CloudNodegroupConfig: &mksclient.CloudNodegroupConfigInfo{
				FlavorId:    cmp.Or(cloud.FlavorId, "fake-flavor"),
				VolumeGb:    cloud.VolumeGb,
				VolumeType:  cloud.VolumeType,
				LocalVolume: cloud.LocalVolume,
			},
		}
		if config := opts.DedicatedNodegroupConfig; config != nil {
			// daladapter/worker_group.go:52-68 and validate/nodegroup.go:351-357;
			// the currency is never returned (apiadapter/nodegroups.go:76-85).
			ng.NodegroupType = mksclient.NodegroupDetailedNodegroupTypeDEDICATED
			ng.EnableAutoscale, ng.AutoscaleMinNodes, ng.AutoscaleMaxNodes = false, new(int64(0)), new(int64(0))
			ng.Preemptible = false
			ng.CloudNodegroupConfig = nil
			ng.DedicatedNodegroupConfig = &mksclient.DedicatedNodegroupConfig{
				ServiceUuid:            config.ServiceUuid,
				PricePlanUuid:          config.PricePlanUuid,
				RootSizeGb:             new(valueOr(config.RootSizeGb, 100)),
				CreateStoragePartition: new(valueOr(config.CreateStoragePartition, true)),
			}
			if opts.Cidr != "" {
				ng.Cidr = new(opts.Cidr)
			}
		}
		f.nodegroups[id] = ng
		count := opts.Count
		f.addNodegroupTask(clusterID, id, "CLUSTER_RESIZE", func() {
			ng := f.nodegroups[id]
			if ng.DedicatedNodegroupConfig != nil && ng.Cidr == nil {
				// mk-conductor stores the subnet mk-cluster-bm allocated
				// (internal/pkg/msg/subscribe/mkclusterbm.go:1535, :1551-1552),
				// and the API returns it (apiadapter/nodegroups.go:80-82).
				ng.Cidr = new(mksV2FakeAllocatedCIDR)
			}
			ng.Status = mksclient.NodegroupDetailedStatusACTIVE
			ng.Nodes = mksV2FakeNodes(id, count)
			f.nodegroups[id] = ng
		})
	}

	if f.createNodegroupsStatus != 0 {
		writeMKSV2Error(w, f.createNodegroupsStatus, mksV2ObjectCluster, clusterID)

		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// failCreateNodegroupsAfterStore makes create store the nodegroups and then
// answer with status, or answer normally again when status is 0.
func (f *mksV2Fake) failCreateNodegroupsAfterStore(status int) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.createNodegroupsStatus = status
}

// createConcurrently makes the next create request add ng as well.
func (f *mksV2Fake) createConcurrently(ng mksclient.NodegroupDetailed) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.concurrentNodegroups = append(f.concurrentNodegroups, ng)
}

// nodegroup returns the nodegroup of the request path, or answers 404.
func (f *mksV2Fake) nodegroup(w http.ResponseWriter, r *http.Request) (mksclient.NodegroupDetailed, bool) {
	clusterID, id := r.PathValue("cluster_id"), r.PathValue("nodegroup_id")
	ng, ok := f.nodegroups[id]
	if _, clusterExists := f.clusters[clusterID]; !clusterExists {
		writeMKSV2Error(w, http.StatusNotFound, mksV2ObjectCluster, clusterID)

		return ng, false
	}
	if !ok || ng.ClusterId != clusterID {
		writeMKSV2Error(w, http.StatusNotFound, mksV2ObjectNodegroup, id)

		return ng, false
	}

	return ng, true
}

// getNodegroup returns the segment as availability_zone only, like mk-api-v2;
// the list returns it as segment.
func (f *mksV2Fake) getNodegroup(w http.ResponseWriter, r *http.Request) {
	ng, ok := f.nodegroup(w, r)
	if ok {
		segment := ng.Segment
		ng.Segment, ng.AvailabilityZone = "", &segment
		writeMKSV2JSON(w, http.StatusOK, mksclient.NodegroupResp{Nodegroup: ng})
	}
}

// patchNodegroup applies autoscale settings at once and labels and taints
// with their tasks, like the API.
func (f *mksV2Fake) patchNodegroup(w http.ResponseWriter, r *http.Request) {
	ng, ok := f.nodegroup(w, r)
	if !ok {
		return
	}
	var body mksclient.NodegroupUpdateBody
	err := json.NewDecoder(r.Body).Decode(&body)
	if err != nil {
		writeMKSV2Error(w, http.StatusBadRequest, mksV2ObjectNodegroup, ng.Id)

		return
	}
	opts := body.Nodegroup
	if ng.DedicatedNodegroupConfig != nil &&
		(opts.EnableAutoscale != nil || opts.AutoscaleMinNodes != nil || opts.AutoscaleMaxNodes != nil) {
		// validate/nodegroup.go:257-263.
		writeMKSV2Error(w, http.StatusBadRequest, mksV2ObjectNodegroup, ng.Id)

		return
	}

	ng.EnableAutoscale = valueOr(opts.EnableAutoscale, ng.EnableAutoscale)
	if opts.AutoscaleMinNodes != nil {
		ng.AutoscaleMinNodes = new(int64(*opts.AutoscaleMinNodes))
	}
	if opts.AutoscaleMaxNodes != nil {
		ng.AutoscaleMaxNodes = new(int64(*opts.AutoscaleMaxNodes))
	}
	f.nodegroups[ng.Id] = ng

	id := ng.Id
	if opts.Labels != nil {
		f.addNodegroupTask(ng.ClusterId, id, "UPDATE_NODEGROUP_LABELS", func() {
			ng := f.nodegroups[id]
			ng.Labels = *opts.Labels
			f.nodegroups[id] = ng
		})
	}
	if opts.Taints != nil {
		f.addNodegroupTask(ng.ClusterId, id, "UPDATE_NODEGROUP_TAINTS", func() {
			ng := f.nodegroups[id]
			ng.Taints = *opts.Taints
			f.nodegroups[id] = ng
		})
	}

	w.WriteHeader(http.StatusNoContent)
}

func (f *mksV2Fake) resizeNodegroup(w http.ResponseWriter, r *http.Request) {
	ng, ok := f.nodegroup(w, r)
	if !ok {
		return
	}
	var body mksclient.NodegroupResizeBody
	err := json.NewDecoder(r.Body).Decode(&body)
	if err != nil {
		writeMKSV2Error(w, http.StatusBadRequest, mksV2ObjectNodegroup, ng.Id)

		return
	}

	if int(body.Nodegroup.Desired) == len(ng.Nodes) {
		// handlers/nodegroups/resize.go ErrorNodegroupResizeSameCount.
		writeMKSV2Error(w, http.StatusBadRequest, mksV2ObjectNodegroup, ng.Id)

		return
	}

	id := ng.Id
	f.addNodegroupTask(ng.ClusterId, id, "NODE_GROUP_RESIZE", func() {
		ng := f.nodegroups[id]
		ng.Nodes = mksV2FakeNodes(id, body.Nodegroup.Desired)
		f.nodegroups[id] = ng
	})
	w.WriteHeader(http.StatusNoContent)
}

// deleteNodegroup removes the nodegroup when its CLUSTER_RESIZE task is DONE.
func (f *mksV2Fake) deleteNodegroup(w http.ResponseWriter, r *http.Request) {
	ng, ok := f.nodegroup(w, r)
	if !ok {
		return
	}

	ng.Status = mksclient.NodegroupDetailedStatusPENDINGDELETE
	f.nodegroups[ng.Id] = ng
	id := ng.Id
	f.addNodegroupTask(ng.ClusterId, id, "CLUSTER_RESIZE", func() { delete(f.nodegroups, id) })
	w.WriteHeader(http.StatusNoContent)
}

func (f *mksV2Fake) addNodegroupTask(clusterID, nodegroupID, taskType string, effect func()) {
	f.addTask(clusterID, taskType, effect)
	f.tasks[len(f.tasks)-1].task.NodegroupId = &nodegroupID
}

// mksV2FakeAllocatedCIDR is the cidr the fake allocates to a dedicated node
// group created without one.
const mksV2FakeAllocatedCIDR = "10.10.0.0/24"

func mksV2FakeNodes(nodegroupID string, count int64) []mksclient.Node {
	nodes := make([]mksclient.Node, count)
	for i := range nodes {
		nodes[i] = mksclient.Node{
			Id:          fmt.Sprintf("%s-node-%d", nodegroupID, i+1),
			Ip:          fmt.Sprintf("198.51.100.%d", i+1),
			Hostname:    fmt.Sprintf("%s-node-%d", nodegroupID, i+1),
			NodegroupId: nodegroupID,
		}
	}

	return nodes
}

// seedPricePlans sets what the dedicated servers API lists.
func (f *mksV2Fake) seedPricePlans(plans ...*dedicated.PricePlan) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.pricePlans = plans
}

// seedNodegroup adds a nodegroup the provider did not create.
func (f *mksV2Fake) seedNodegroup(ng mksclient.NodegroupDetailed) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.nodegroups[ng.Id] = ng
}

// updateNodegroup changes a nodegroup behind the provider's back.
func (f *mksV2Fake) updateNodegroup(id string, update func(ng *mksclient.NodegroupDetailed)) {
	f.mu.Lock()
	defer f.mu.Unlock()

	ng := f.nodegroups[id]
	update(&ng)
	f.nodegroups[id] = ng
}

// removeNodegroup deletes a nodegroup behind the provider's back.
func (f *mksV2Fake) removeNodegroup(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	delete(f.nodegroups, id)
}

// clientPools returns the pool of every client built so far.
func (f *mksV2Fake) clientPools() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	pools := make([]string, len(f.clients))
	for i, c := range f.clients {
		pools[i] = c.pool
	}

	return pools
}

// hasNodegroup reports whether the fake still has the nodegroup.
func (f *mksV2Fake) hasNodegroup(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()

	_, ok := f.nodegroups[id]

	return ok
}
