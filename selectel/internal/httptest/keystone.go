package httptest

import (
	"encoding/json"
	"net/http"
	nethttptest "net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// Endpoint is a public endpoint of the Keystone catalog.
type Endpoint struct {
	Type   string
	Region string
	URL    string
}

// Keystone is a fake Keystone identity API v3, enough for go-selvpcclient to
// authenticate and read the catalog. It issues one token per project scope.
type Keystone struct {
	server    *nethttptest.Server
	endpoints []Endpoint

	mu sync.Mutex
	// projects maps an issued token to its project scope.
	projects map[string]string
}

// NewKeystone starts a fake whose catalog holds endpoints and its own identity
// endpoint in authRegion.
func NewKeystone(t testing.TB, authRegion string, endpoints ...Endpoint) *Keystone {
	t.Helper()

	k := &Keystone{projects: map[string]string{}}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v3/auth/tokens", k.createToken)
	mux.HandleFunc("GET /v3/auth/tokens", k.getToken)
	k.server = nethttptest.NewServer(mux)
	t.Cleanup(k.server.Close)

	k.endpoints = append([]Endpoint{{Type: "identity", Region: authRegion, URL: k.AuthURL()}}, endpoints...)

	return k
}

// AuthURL is the provider auth_url. It ends with the version, so gophercloud
// skips version discovery.
func (k *Keystone) AuthURL() string {
	return k.server.URL + "/v3/"
}

// Project returns the project scope of a token the fake issued.
func (k *Keystone) Project(token string) (string, bool) {
	k.mu.Lock()
	defer k.mu.Unlock()

	project, ok := k.projects[token]

	return project, ok
}

func (k *Keystone) createToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Auth struct {
			Scope struct {
				Project struct {
					ID string `json:"id"`
				} `json:"project"`
			} `json:"scope"`
		} `json:"auth"`
	}
	err := json.NewDecoder(r.Body).Decode(&req)
	if err != nil {
		writeKeystoneError(w, http.StatusBadRequest)

		return
	}

	// Keystone project IDs carry no dashes, so a dashed one names no project.
	project := req.Auth.Scope.Project.ID
	if strings.Contains(project, "-") {
		writeKeystoneError(w, http.StatusUnauthorized)

		return
	}

	token := "token-" + project
	k.mu.Lock()
	k.projects[token] = project
	k.mu.Unlock()

	w.Header().Set("X-Subject-Token", token)
	k.writeToken(w, http.StatusCreated, project)
}

func (k *Keystone) getToken(w http.ResponseWriter, r *http.Request) {
	project, ok := k.Project(r.Header.Get("X-Subject-Token"))
	if !ok {
		writeKeystoneError(w, http.StatusNotFound)

		return
	}

	k.writeToken(w, http.StatusOK, project)
}

func (k *Keystone) writeToken(w http.ResponseWriter, status int, project string) {
	type endpoint struct {
		ID        string `json:"id"`
		Interface string `json:"interface"`
		Region    string `json:"region"`
		RegionID  string `json:"region_id"`
		URL       string `json:"url"`
	}
	type service struct {
		ID        string     `json:"id"`
		Type      string     `json:"type"`
		Endpoints []endpoint `json:"endpoints"`
	}

	var catalog []service
	for _, e := range k.endpoints {
		i := len(catalog) - 1
		if i < 0 || catalog[i].Type != e.Type {
			catalog = append(catalog, service{ID: e.Type, Type: e.Type})
			i++
		}
		catalog[i].Endpoints = append(catalog[i].Endpoints, endpoint{
			ID: e.Type + "-" + e.Region, Interface: "public", Region: e.Region, RegionID: e.Region, URL: e.URL,
		})
	}

	body := map[string]any{"token": map[string]any{
		"expires_at": time.Now().Add(time.Hour).UTC().Format("2006-01-02T15:04:05.000000Z"),
		"methods":    []string{"password"},
		"project":    map[string]string{"id": project},
		"catalog":    catalog,
	}}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeKeystoneError(w http.ResponseWriter, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{
		"code": status, "message": http.StatusText(status), "title": http.StatusText(status),
	}})
}
