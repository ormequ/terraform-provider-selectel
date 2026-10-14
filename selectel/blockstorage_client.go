package selectel

import (
	"context"
	"fmt"

	blockstorage "github.com/selectel/blockstorage-go/pkg/v1"
	"github.com/selectel/go-selvpcclient/v5/selvpcclient"
)

// BlockStorageDefaults supplies the scope for UUID-based imports.
func (c *Config) BlockStorageDefaults() (string, string) { return c.ProjectID, c.Region }

// BlockStorageClient authenticates for each operation. blockstorage-go takes a
// token string that it cannot renew, so the cached selvpc client is not reused.
func (c *Config) BlockStorageClient(ctx context.Context, projectID, region string) (*blockstorage.Client, error) {
	endpoint, token, err := c.projectEndpoint(ctx, projectID, region, BlockStorage)
	if err != nil {
		return nil, err
	}

	return blockstorage.NewClient(blockstorage.Config{Endpoint: endpoint, Token: token, UserAgent: c.UserAgent})
}

// projectEndpoint returns the public endpoint of a service and a fresh token
// scoped to the project.
func (c *Config) projectEndpoint(ctx context.Context, projectID, region, serviceType string) (string, string, error) {
	client, err := c.projectClient(ctx, projectID)
	if err != nil {
		return "", "", err
	}
	endpoint, err := client.Catalog.GetEndpoint(serviceType, region)
	if err != nil {
		return "", "", err
	}

	return endpoint.URL, client.GetXAuthToken(), nil
}

// projectClient authenticates a fresh selvpc client scoped to the project.
func (c *Config) projectClient(ctx context.Context, projectID string) (*selvpcclient.Client, error) {
	opts := c.selvpcClientOptions(projectID)
	opts.Context = ctx
	client, err := selvpcclient.NewClient(opts)
	if err != nil {
		return nil, fmt.Errorf("authenticate project %s: %w", projectID, err)
	}

	return client, nil
}
