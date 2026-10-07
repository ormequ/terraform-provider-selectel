package selectel

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

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

	// clients records every mksV2ClientFn call, userAgents every request.
	clients    []mksV2FakeClient
	userAgents []string
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
)

// newMKSV2Fake starts the fake and makes mksV2ClientFn return clients of it
// until the test ends. Tests using it must not call t.Parallel.
func newMKSV2Fake(t *testing.T) *mksV2Fake {
	t.Helper()

	f := &mksV2Fake{
		clusters:    map[string]mksclient.ClusterDetailed{},
		kubeconfigs: map[string]string{},
		failures:    map[string]int{},
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
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)

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
