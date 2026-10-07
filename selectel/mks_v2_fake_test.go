package selectel

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	mksv2 "github.com/selectel/mks-go/v2/pkg"
	"github.com/selectel/mks-go/v2/pkg/mksclient"
)

// mksV2Fake is an in-memory mk-api-v2 behind httptest.Server. Tests seed it,
// install it in place of the real client and read what the provider sent.
type mksV2Fake struct {
	server *httptest.Server

	mu                   sync.Mutex
	clusters             map[string]mksclient.ClusterDetailed
	kubeconfigs          map[string]string
	kubeVersions         []mksclient.KubeVersionInfo
	featureGates         []mksclient.AvailableFeatureGates
	admissionControllers []mksclient.AvailableAdmissionControllers
	// failures maps a route pattern to the HTTP status it answers with.
	failures map[string]int
	tasks    []*mksV2FakeTask
	// failedTaskTypes makes new tasks of a type end in ERROR.
	failedTaskTypes map[string]bool
	// bodies keeps the last request body of every route, calls counts them.
	bodies map[string][]byte
	calls  map[string]int

	// clients records every mksV2ClientFn call, userAgents every request.
	clients    []mksV2FakeClient
	userAgents []string
}

// mksV2FakeTask finishes on its first GET, unless stuck, and then applies
// effect when it is DONE.
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
)

// newMKSV2Fake starts the fake and makes mksV2ClientFn return clients of it
// until the test ends. Tests using it must not call t.Parallel.
func newMKSV2Fake(t *testing.T) *mksV2Fake {
	t.Helper()

	f := &mksV2Fake{
		clusters:    map[string]mksclient.ClusterDetailed{},
		kubeconfigs: map[string]string{},
		failures:    map[string]int{},

		failedTaskTypes: map[string]bool{},
		bodies:          map[string][]byte{},
		calls:           map[string]int{},
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
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)

	previousInterval := mksV2PollInterval
	mksV2PollInterval = time.Millisecond
	t.Cleanup(func() { mksV2PollInterval = previousInterval })

	previous := mksV2ClientFn
	mksV2ClientFn = func(_ context.Context, config *Config, projectID, pool string) (*mksv2.ServiceClient, error) {
		f.mu.Lock()
		f.clients = append(f.clients, mksV2FakeClient{projectID: projectID, pool: pool, userAgent: config.UserAgent})
		f.mu.Unlock()

		return newMKSV2ServiceClient("fake-token", f.server.URL, config.UserAgent)
	}
	t.Cleanup(func() { mksV2ClientFn = previous })

	return f
}

// handle registers a route that records the User-Agent and answers with a
// forced failure when one is set.
func (f *mksV2Fake) handle(mux *http.ServeMux, pattern string, handler http.HandlerFunc) {
	mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()

		f.userAgents = append(f.userAgents, r.Header.Get("User-Agent"))
		f.calls[pattern]++
		body, _ := io.ReadAll(r.Body)
		if len(body) > 0 {
			f.bodies[pattern] = body
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		status, ok := f.failures[pattern]
		if ok {
			writeMKSV2Error(w, status, r.PathValue("cluster_id"))

			return
		}

		handler(w, r)
	})
}

func (f *mksV2Fake) getCluster(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("cluster_id")
	c, ok := f.clusters[id]
	if !ok {
		writeMKSV2Error(w, http.StatusNotFound, id)

		return
	}

	writeMKSV2JSON(w, http.StatusOK, mksclient.ClusterResp{Cluster: &c})
}

func (f *mksV2Fake) getKubeconfig(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("cluster_id")
	kubeconfig, ok := f.kubeconfigs[id]
	if !ok {
		writeMKSV2Error(w, http.StatusNotFound, id)

		return
	}

	w.Header().Set("Content-Type", "application/yaml")
	_, _ = w.Write([]byte(kubeconfig))
}

// createCluster fills what the API defaults and starts CREATE_CLUSTER. The
// cluster gets testMKSV2ClusterID and the project of the last client.
func (f *mksV2Fake) createCluster(w http.ResponseWriter, r *http.Request) {
	var body mksclient.ClusterCreateBody
	err := json.NewDecoder(r.Body).Decode(&body)
	if err != nil || body.Cluster == nil {
		writeMKSV2Error(w, http.StatusBadRequest, "")

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
	c := mksclient.ClusterDetailed{
		Id:                            testMKSV2ClusterID,
		Name:                          opts.Name,
		Pool:                          opts.Pool,
		ProjectId:                     f.clients[len(f.clients)-1].projectID,
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
		// The API never returns them.
		c.KubernetesOptions.X509CaCertificates = ""
	}
	f.clusters[c.Id] = c

	f.addTask(c.Id, "CREATE_CLUSTER", func() {
		c := f.clusters[testMKSV2ClusterID]
		c.Status = "ACTIVE"
		f.clusters[c.Id] = c
	})
	writeMKSV2JSON(w, http.StatusCreated, mksclient.ClusterResp{Cluster: &c})
}

// patchCluster applies the sent fields. Like the API, only a kubernetes_options
// change starts a task.
func (f *mksV2Fake) patchCluster(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("cluster_id")
	c, ok := f.clusters[id]
	if !ok {
		writeMKSV2Error(w, http.StatusNotFound, id)

		return
	}
	var body mksclient.ClusterUpdateBody
	err := json.NewDecoder(r.Body).Decode(&body)
	if err != nil || body.Cluster == nil {
		writeMKSV2Error(w, http.StatusBadRequest, id)

		return
	}
	opts := body.Cluster

	if opts.MaintenanceWindowStart != nil {
		setMKSV2FakeWindow(&c, *opts.MaintenanceWindowStart)
	}
	c.EnableAutorepair = valueOr(opts.EnableAutorepair, c.EnableAutorepair)
	c.EnablePatchVersionAutoUpgrade = valueOr(opts.EnablePatchVersionAutoUpgrade, c.EnablePatchVersionAutoUpgrade)
	if opts.CniCiliumSettings != nil {
		c.CniCiliumSettings = opts.CniCiliumSettings
	}
	if opts.KubernetesOptions != nil {
		c.KubernetesOptions = *opts.KubernetesOptions
		c.KubernetesOptions.X509CaCertificates = ""
		f.addTask(id, "UPGRADE_MASTERS_CONFIG", nil)
	}
	f.clusters[id] = c

	writeMKSV2JSON(w, http.StatusOK, mksclient.ClusterResp{Cluster: &c})
}

// deleteCluster removes the cluster when DELETE_CLUSTER is DONE.
func (f *mksV2Fake) deleteCluster(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("cluster_id")
	_, ok := f.clusters[id]
	if !ok {
		writeMKSV2Error(w, http.StatusNotFound, id)

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
			writeMKSV2Error(w, http.StatusNotFound, id)

			return
		}

		target, taskType := kubeVersionTrimToMinor, "UPGRADE_PATCH_VERSION"
		if minor {
			target, taskType = kubeVersionTrimToMinorIncremented, "UPGRADE_MINOR_VERSION"
		}
		targetMinor, err := target(c.KubeVersion)
		if err != nil {
			writeMKSV2Error(w, http.StatusBadRequest, id)

			return
		}
		latest, err := latestKubePatchVersions(mksKubeVersionsV2ToV1Views(f.kubeVersions))
		if err != nil || latest[targetMinor] == "" {
			writeMKSV2Error(w, http.StatusBadRequest, id)

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
		writeMKSV2Error(w, http.StatusNotFound, id)

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
	if i < 0 || !clusterExists {
		writeMKSV2Error(w, http.StatusNotFound, taskID)

		return
	}

	t := f.tasks[i]
	running := t.task.Status == mksclient.INQUEUE || t.task.Status == mksclient.INPROGRESS
	switch {
	case !running || t.stuck:
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

// fail makes the route answer with status until the test ends.
func (f *mksV2Fake) fail(pattern string, status int) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.failures[pattern] = status
}

// checkClients fails the test unless every client was built for projectID and
// every request carried the User-Agent of the provider Config.
func (f *mksV2Fake) checkClients(t *testing.T, projectID string) {
	t.Helper()

	f.mu.Lock()
	defer f.mu.Unlock()

	if len(f.clients) == 0 || len(f.userAgents) == 0 {
		t.Fatalf("the fake mk-api-v2 got %d clients and %d requests, want both > 0", len(f.clients), len(f.userAgents))
	}
	for _, c := range f.clients {
		if c.projectID != projectID {
			t.Errorf("client built for project %q, want %q", c.projectID, projectID)
		}
		if c.userAgent == "" || c.userAgent != cfgSingletone.UserAgent {
			t.Errorf("client built with User-Agent %q, want the Config one %q", c.userAgent, cfgSingletone.UserAgent)
		}
	}
	for _, userAgent := range f.userAgents {
		if userAgent != cfgSingletone.UserAgent {
			t.Errorf("request sent with User-Agent %q, want %q", userAgent, cfgSingletone.UserAgent)
		}
	}
}

func writeMKSV2JSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeMKSV2Error(w http.ResponseWriter, status int, id string) {
	if status == http.StatusNotFound {
		var body mksclient.GenericNotFoundError
		body.Error.Id = id
		body.Error.Message = fmt.Sprintf("object %s not found", id)
		writeMKSV2JSON(w, status, body)

		return
	}

	var body mksclient.GenericError
	body.Error.Message = fmt.Sprintf("fake error %d", status)
	writeMKSV2JSON(w, status, body)
}

// useMKSV2TestConfig rebuilds the provider Config from the test configuration:
// newConfig builds it once per process, so an earlier test would otherwise
// decide its project_id. The next test gets a fresh one as well.
func useMKSV2TestConfig(t *testing.T) {
	t.Helper()

	setTestProviderEnv(t)
	t.Setenv("INFRA_PROJECT_ID", "")
	once = sync.Once{}
	cfgSingletone = nil
	t.Cleanup(func() {
		once = sync.Once{}
		cfgSingletone = nil
	})
}
