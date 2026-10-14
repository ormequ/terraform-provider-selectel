package mks

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	mksv2 "github.com/selectel/mks-go/v2/pkg"
	"github.com/selectel/mks-go/v2/pkg/mksclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	clusterID = "6fd56a4c-a0b9-4e4b-b5f4-a3c0d3e0b1a1"
	pool      = "ru-7"

	kubeconfig = `apiVersion: v1
clusters:
- cluster:
    certificate-authority-data: Y2EtZGF0YQ==
    server: https://192.0.2.10:6443
  name: tf-v2
contexts:
- context:
    cluster: tf-v2
    user: admin
  name: admin@tf-v2
current-context: admin@tf-v2
kind: Config
users:
- name: admin
  user:
    client-certificate-data: Y2xpZW50LWNlcnQ=
    client-key-data: Y2xpZW50LWtleQ==
`
)

// testConfig hands out a prepared client and records the scope it was asked for.
type testConfig struct {
	client          *mksv2.ServiceClient
	err             error
	providerProject string

	gotProject, gotPool *string
}

func (c testConfig) MKSV2Client(_ context.Context, projectID, pool string) (*mksv2.ServiceClient, error) {
	if c.gotProject != nil {
		*c.gotProject, *c.gotPool = projectID, pool
	}

	return c.client, c.err
}

func (c testConfig) MKSV2Defaults() string { return c.providerProject }

// testClient serves handler as MKS API v2 and checks the token on every request.
func testClient(t *testing.T, handler http.HandlerFunc) *mksv2.ServiceClient {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		assert.Equal(t, "token", req.Header.Get("X-Auth-Token"))
		w.Header().Set("Content-Type", "application/json")
		handler(w, req)
	}))
	t.Cleanup(server.Close)
	client, err := mksv2.NewMKSClientV2("token", server.URL)
	require.NoError(t, err)

	return client
}

func writeJSON(t *testing.T, w http.ResponseWriter, status int, value any) {
	t.Helper()
	w.WriteHeader(status)
	assert.NoError(t, json.NewEncoder(w).Encode(value))
}

// writeError answers like MKS API v2: a 404 names the object type in the
// message and carries the ID in error.id.
func writeError(t *testing.T, w http.ResponseWriter, status int, id string) {
	t.Helper()
	if status == http.StatusNotFound {
		var body mksclient.GenericNotFoundError
		body.Error.Id = id
		body.Error.Message = "Cluster not found"
		writeJSON(t, w, status, body)

		return
	}
	var body mksclient.GenericError
	body.Error.Message = fmt.Sprintf("fake error %d", status)
	writeJSON(t, w, status, body)
}

func dataConfig(t *testing.T, source datasource.DataSource, model any) tfsdk.Config {
	t.Helper()
	var schema datasource.SchemaResponse
	source.Schema(context.Background(), datasource.SchemaRequest{}, &schema)
	state := tfsdk.State{Schema: schema.Schema}
	d := state.Set(context.Background(), model)
	require.False(t, d.HasError(), "%v", d)

	return tfsdk.Config{Schema: schema.Schema, Raw: state.Raw}
}

// read runs Read of source against config with the null state the Framework
// starts a data source read with.
func read(t *testing.T, source datasource.DataSource, config tfsdk.Config) datasource.ReadResponse {
	t.Helper()
	response := datasource.ReadResponse{State: tfsdk.State{
		Schema: config.Schema,
		Raw:    tftypes.NewValue(config.Schema.Type().TerraformType(context.Background()), nil),
	}}
	source.Read(context.Background(), datasource.ReadRequest{Config: config}, &response)

	return response
}
