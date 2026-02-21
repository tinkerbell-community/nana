// Package jetkvm implements a BMC provider for JetKVM devices.
//
// The JetKVM provider connects to a JetKVM device via WebRTC and provides
// power control, virtual media, and BMC info capabilities.
package jetkvm

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jetkvm/cloud-api/mgmt-api/pkg/providers"
	"github.com/jetkvm/cloud-api/mgmt-api/pkg/providers/jetkvm/client"
)

// Provider implements the providers.Provider interface for JetKVM devices.
type Provider struct {
	kvmClient *client.Client
	host      string
	password  string
	timeout   time.Duration
	logger    *slog.Logger
}

func init() {
	providers.Register("jetkvm", newProvider)
}

func newProvider(cfg map[string]any) (providers.Provider, error) {
	host, _ := cfg["host"].(string)
	if host == "" {
		return nil, fmt.Errorf("jetkvm provider requires 'host' config")
	}
	password, _ := cfg["password"].(string)

	timeout := 30 * time.Second
	if t, ok := cfg["timeout"].(int); ok && t > 0 {
		timeout = time.Duration(t) * time.Second
	}

	c, err := client.NewClient(&client.Config{
		Host:     host,
		Password: password,
		Timeout:  timeout,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create JetKVM client: %w", err)
	}

	return &Provider{
		kvmClient: c,
		host:      host,
		password:  password,
		timeout:   timeout,
		logger:    slog.Default(),
	}, nil
}

// Name returns the provider type identifier.
func (p *Provider) Name() string { return "jetkvm" }

// Capabilities returns the list of capabilities this provider offers.
func (p *Provider) Capabilities() []providers.Capability {
	return []providers.Capability{
		providers.CapPowerControl,
		providers.CapVirtualMedia,
		providers.CapBMCInfo,
	}
}

// Open initializes the WebRTC connection to the JetKVM device.
func (p *Provider) Open(ctx context.Context) error {
	return p.kvmClient.Connect(ctx)
}

// Close releases the WebRTC connection.
func (p *Provider) Close() error {
	return p.kvmClient.Close()
}

// Compile-time interface checks.
var (
	_ providers.Provider               = (*Provider)(nil)
	_ providers.PowerController        = (*Provider)(nil)
	_ providers.VirtualMediaController = (*Provider)(nil)
	_ providers.BMCInfoProvider        = (*Provider)(nil)
)
