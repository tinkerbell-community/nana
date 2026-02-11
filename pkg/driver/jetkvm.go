package driver

import (
	"context"
	"fmt"
	"time"

	"github.com/jetkvm/cloud-api/mgmt-api/pkg/client"
)

// JetKVMDriver implements the Driver, PowerController, VirtualMediaController,
// and BMCInfoProvider interfaces for JetKVM devices.
type JetKVMDriver struct {
	kvmClient *client.Client
	host      string
	password  string
	timeout   time.Duration
}

func init() {
	Register("jetkvm", newJetKVMDriver)
}

func newJetKVMDriver(cfg map[string]interface{}) (Driver, error) {
	host, _ := cfg["host"].(string)
	if host == "" {
		return nil, fmt.Errorf("jetkvm driver requires 'host' config")
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

	return &JetKVMDriver{
		kvmClient: c,
		host:      host,
		password:  password,
		timeout:   timeout,
	}, nil
}

// --- Driver interface ---

func (d *JetKVMDriver) Name() string { return "jetkvm" }

func (d *JetKVMDriver) Capabilities() []Capability {
	return []Capability{CapPowerControl, CapVirtualMedia, CapBMCInfo}
}

func (d *JetKVMDriver) Open(ctx context.Context) error {
	return d.kvmClient.Connect(ctx)
}

func (d *JetKVMDriver) Close() error {
	return d.kvmClient.Close()
}

// --- PowerController interface ---

func (d *JetKVMDriver) GetPowerState(ctx context.Context) (string, error) {
	state, err := d.kvmClient.GetPowerState(ctx)
	if err != nil {
		return "", err
	}
	return state.String(), nil
}

func (d *JetKVMDriver) SetPowerState(ctx context.Context, state string) error {
	return d.kvmClient.SetPowerState(ctx, state)
}

// --- VirtualMediaController interface ---

func (d *JetKVMDriver) MountMedia(ctx context.Context, url, kind string) error {
	if kind == "" {
		kind = "cdrom"
	}
	return d.kvmClient.MountWithHTTP(ctx, url, kind)
}

func (d *JetKVMDriver) UnmountMedia(ctx context.Context) error {
	return d.kvmClient.UnmountImage(ctx)
}

func (d *JetKVMDriver) GetMediaState(ctx context.Context) (*VirtualMediaState, error) {
	state, err := d.kvmClient.GetVirtualMediaState(ctx)
	if err != nil {
		return nil, err
	}
	return &VirtualMediaState{
		Inserted: state.URL != "",
		Image:    state.URL,
		Kind:     state.Mode,
	}, nil
}

// --- BMCInfoProvider interface ---

func (d *JetKVMDriver) GetBMCVersion(ctx context.Context) (string, error) {
	version, err := d.kvmClient.GetLocalVersion(ctx)
	if err != nil {
		return "", err
	}
	return version.AppVersion, nil
}

// Compile-time interface checks.
var (
	_ Driver                 = (*JetKVMDriver)(nil)
	_ PowerController        = (*JetKVMDriver)(nil)
	_ VirtualMediaController = (*JetKVMDriver)(nil)
	_ BMCInfoProvider        = (*JetKVMDriver)(nil)
)
