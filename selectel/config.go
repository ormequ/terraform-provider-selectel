package selectel

import (
	"context"
	"fmt"
	"sync"

	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/selectel/go-selvpcclient/v5/selvpcclient"
)

var (
	cfgSingletone *Config
	once          sync.Once
)

// Config contains all available configuration options.
type Config struct {
	Region    string
	ProjectID string

	Context        context.Context
	AuthURL        string
	AuthRegion     string
	Username       string
	Password       string
	UserDomainName string
	DomainName     string
	clientsCache   map[string]*selvpcclient.Client
	lock           sync.Mutex

	UserAgent string
}

func getConfig(d *schema.ResourceData, userAgent string) (*Config, diag.Diagnostics) {
	return newConfig(userAgent, func(key string) string { return d.Get(key).(string) }), nil
}

// newConfig is shared by the SDKv2 and the framework provider.
func newConfig(userAgent string, attr func(key string) string) *Config {
	once.Do(func() {
		cfgSingletone = &Config{
			Username:       attr("username"),
			Password:       attr("password"),
			DomainName:     attr("domain_name"),
			AuthURL:        attr("auth_url"),
			AuthRegion:     attr("auth_region"),
			UserDomainName: attr("user_domain_name"),
			ProjectID:      attr("project_id"),
			Region:         attr("region"),
			UserAgent:      userAgent,
		}
	})

	return cfgSingletone
}

func (c *Config) GetSelVPCClient() (*selvpcclient.Client, error) {
	return c.GetSelVPCClientWithProjectScope("")
}

func (c *Config) GetSelVPCClientWithProjectScope(projectID string) (*selvpcclient.Client, error) {
	c.lock.Lock()
	defer c.lock.Unlock()

	clientsCacheKey := fmt.Sprintf("client_%s", projectID)

	if client, ok := c.clientsCache[clientsCacheKey]; ok {
		return client, nil
	}

	opts := &selvpcclient.ClientOptions{
		DomainName:     c.DomainName,
		Username:       c.Username,
		Password:       c.Password,
		ProjectID:      projectID,
		AuthURL:        c.AuthURL,
		AuthRegion:     c.AuthRegion,
		UserDomainName: c.UserDomainName,
		UserAgent:      c.UserAgent,
	}

	client, err := selvpcclient.NewClient(opts)
	if err != nil {
		return nil, err
	}

	if c.clientsCache == nil {
		c.clientsCache = map[string]*selvpcclient.Client{}
	}

	c.clientsCache[clientsCacheKey] = client

	return client, nil
}
