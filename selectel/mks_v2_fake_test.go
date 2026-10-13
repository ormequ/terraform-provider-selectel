package selectel

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/selectel/mks-go/v2/pkg/mksclient"
	ihttptest "github.com/terraform-providers/terraform-provider-selectel/selectel/internal/httptest"
	"github.com/terraform-providers/terraform-provider-selectel/version"
)

// mksV2FakePools are the pools the fake Keystone catalog holds an MKS API v2
// endpoint for, and mksV2FakeAuthRegion is its own identity region. The other
// pool comes first, so a client that takes the first catalog endpoint hits the
// wrong server.
var mksV2FakePools = []string{"ru-1", testMKSV2Pool}

const mksV2FakeAuthRegion = "test"

// mksV2FakePoolKey is the request context key of the pool a server answers for.
type mksV2FakePoolKey struct{}

// mksV2Fake is an in-memory MKS API v2 behind httptest.Server, one per pool,
// and a fake Keystone that lists them in its catalog. Tests seed it, serve the
// provider through providerFactories, so that the provider authenticates and
// finds the endpoint like in production, and read what the provider sent.
type mksV2Fake struct {
	keystone *ihttptest.Keystone

	mu                   sync.Mutex
	clusters             map[string]mksclient.ClusterDetailed
	kubeconfigs          map[string]string
	kubeVersions         []mksclient.KubeVersionInfo
	featureGates         []mksclient.AvailableFeatureGates
	admissionControllers []mksclient.AvailableAdmissionControllers
	// failures maps a route pattern to the HTTP status it answers with.
	failures map[string]int

	// requests records every request, and configUserAgent is the User-Agent
	// of the provider Config.
	requests        []mksV2FakeRequest
	configUserAgent string
}

// mksV2FakeRequest is a request to MKS API v2 with the project its token was
// issued for and the pool of the server that got it.
type mksV2FakeRequest struct {
	projectID string
	pool      string
	userAgent string
}

// testMKSV2KeystoneProject is a project ID the way Keystone takes it, and
// testMKSV2APIProject is the same project the way MKS API v2 returns it.
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
)

// newMKSV2Fake starts the fake. It touches no package state, so tests using it
// may call t.Parallel.
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

	endpoints := make([]ihttptest.Endpoint, 0, len(mksV2FakePools))
	for _, pool := range mksV2FakePools {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mux.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), mksV2FakePoolKey{}, pool)))
		}))
		t.Cleanup(server.Close)
		endpoints = append(endpoints, ihttptest.Endpoint{Type: MKSV2, Region: pool, URL: server.URL + "/v2"})
	}
	f.keystone = ihttptest.NewKeystone(t, mksV2FakeAuthRegion, endpoints...)

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

// config builds the provider Config like getConfig, but for this test only
// and with the fake Keystone as the auth URL.
func (f *mksV2Fake) config(userAgent string, attr func(key string) string) *Config {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.configUserAgent = userAgent

	return &Config{
		ProjectID:  attr("project_id"),
		Region:     attr("region"),
		UserAgent:  userAgent,
		AuthURL:    f.keystone.AuthURL(),
		AuthRegion: mksV2FakeAuthRegion,
		DomainName: "test",
		Username:   "test",
		Password:   "test",
	}
}

// handle registers a route that records the request, answers 401 to a token
// the fake Keystone did not issue, and answers with a forced failure when one
// is set.
func (f *mksV2Fake) handle(mux *http.ServeMux, pattern string, handler http.HandlerFunc) {
	mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()

		projectID, authorized := f.keystone.Project(r.Header.Get("X-Auth-Token"))
		pool, _ := r.Context().Value(mksV2FakePoolKey{}).(string)
		f.requests = append(f.requests, mksV2FakeRequest{
			projectID: projectID,
			pool:      pool,
			userAgent: r.Header.Get("User-Agent"),
		})
		if !authorized {
			writeMKSV2Error(w, http.StatusUnauthorized, "", "")

			return
		}

		status, ok := f.failures[pattern]
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

	if status == 0 {
		delete(f.failures, pattern)

		return
	}
	f.failures[pattern] = status
}

// checkClients fails the test unless there was a request and every request
// reached the server of pool, carried a token issued for the Keystone form of
// projectID and the User-Agent of the provider Config.
func (f *mksV2Fake) checkClients(t *testing.T, projectID, pool string) {
	t.Helper()

	projectID = mksV2KeystoneProjectID(projectID)

	f.mu.Lock()
	defer f.mu.Unlock()

	if len(f.requests) == 0 {
		t.Fatal("the fake MKS API v2 got no requests, want > 0")
	}
	for _, r := range f.requests {
		if r.projectID != projectID {
			t.Errorf("request sent with a token for project %q, want %q", r.projectID, projectID)
		}
		if r.pool != pool {
			t.Errorf("request sent to the server of pool %q, want %q", r.pool, pool)
		}
		if r.userAgent != f.configUserAgent {
			t.Errorf("request sent with User-Agent %q, want the Config one %q", r.userAgent, f.configUserAgent)
		}
	}
}

func writeMKSV2JSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// The object types of MKS API v2 not-found errors.
const (
	mksV2ObjectCluster = "Cluster"
)

// mksV2FakeObject names the most specific object of the request path.
func mksV2FakeObject(r *http.Request) (string, string) {
	return mksV2ObjectCluster, r.PathValue("cluster_id")
}

// writeMKSV2Error answers like MKS API v2: a 404
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
