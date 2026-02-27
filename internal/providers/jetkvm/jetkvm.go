// Package jetkvm implements a BMC provider for JetKVM devices.
//
// The JetKVM provider connects to a JetKVM device via WebRTC and provides
// power control, virtual media, and BMC info capabilities.
package jetkvm

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/tinkerbell-community/nana/internal/providers"
	"github.com/tinkerbell-community/nana/internal/providers/jetkvm/client"
)

// MacroStep defines a single keyboard input step within a boot device macro.
type MacroStep struct {
	Keys      []string
	Modifiers []string
	Delay     time.Duration
}

// BootDeviceConfig defines the keyboard macro sequence for a boot device option.
type BootDeviceConfig struct {
	Device string
	Delay  time.Duration
	Steps  []MacroStep
}

// Provider implements the providers.Provider interface for JetKVM devices.
type Provider struct {
	c           *client.Client
	host        string
	password    string
	timeout     time.Duration
	logger      slog.Logger
	bootDevices map[string]*BootDeviceConfig // keyed by device name (e.g. "pxe")

	queueMu sync.Mutex
	queue   map[string][]func(ctx context.Context) error // keyed by power state

	bgWg sync.WaitGroup // tracks background post-power-on goroutines
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

	logger := slog.Default().With("provider", "jetkvm", "host", host)

	bootDevices := parseBootConfig(cfg)

	return &Provider{
		c:           c,
		host:        host,
		password:    password,
		timeout:     timeout,
		logger:      *logger,
		bootDevices: bootDevices,
		queue:       make(map[string][]func(ctx context.Context) error),
	}, nil
}

// Name returns the provider type identifier.
func (p *Provider) Name() string { return "jetkvm" }

// Capabilities returns the list of capabilities this provider offers.
func (p *Provider) Capabilities() []providers.Capability {
	caps := []providers.Capability{
		providers.CapPowerControl,
		providers.CapVirtualMedia,
		providers.CapBMCInfo,
	}
	if len(p.bootDevices) > 0 {
		caps = append(caps, providers.CapBootDevice)
	}
	return caps
}

// Open initializes the WebRTC connection to the JetKVM device.
func (p *Provider) Open(ctx context.Context) error {
	return p.c.Connect(ctx)
}

// ensureConnected lazily establishes the WebRTC connection if not already open.
// It is safe to call on every operation — Connect is idempotent when connected.
func (p *Provider) ensureConnected(ctx context.Context) error {
	return p.c.Connect(ctx)
}

// Close releases the WebRTC connection and waits for background tasks to finish.
func (p *Provider) Close() error {
	p.bgWg.Wait()
	return p.c.Close()
}

// enqueueTask adds a task to execute after a specific power state transition.
func (p *Provider) enqueueTask(state string, task func(ctx context.Context) error) {
	p.queueMu.Lock()
	defer p.queueMu.Unlock()
	p.queue[state] = append(p.queue[state], task)
}

// drainQueue executes and removes all queued tasks for the given power state.
func (p *Provider) drainQueue(ctx context.Context, state string) {
	p.queueMu.Lock()
	tasks := p.queue[state]
	delete(p.queue, state)
	p.queueMu.Unlock()

	for _, task := range tasks {
		if err := task(ctx); err != nil {
			p.logger.Warn("queued task failed",
				slog.String("host", p.host),
				slog.String("state", state),
				slog.String("error", err.Error()),
			)
		}
	}
}

// parseBootConfig extracts boot device configurations from the provider config map.
func parseBootConfig(cfg map[string]any) map[string]*BootDeviceConfig {
	bootList, ok := cfg["boot"].([]any)
	if !ok || len(bootList) == 0 {
		return nil
	}

	devices := make(map[string]*BootDeviceConfig, len(bootList))
	for _, entry := range bootList {
		m, ok := entry.(map[string]any)
		if !ok {
			continue
		}

		device, _ := m["device"].(string)
		if device == "" {
			continue
		}

		bc := &BootDeviceConfig{Device: device}

		if d, ok := m["delay"].(string); ok {
			if parsed, err := time.ParseDuration(d); err == nil {
				bc.Delay = parsed
			}
		}

		if stepsList, ok := m["steps"].([]any); ok {
			for _, stepEntry := range stepsList {
				sm, ok := stepEntry.(map[string]any)
				if !ok {
					continue
				}

				step := MacroStep{}

				if keys, ok := sm["keys"].([]any); ok {
					for _, k := range keys {
						if s, ok := k.(string); ok {
							step.Keys = append(step.Keys, s)
						}
					}
				}

				if mods, ok := sm["modifiers"].([]any); ok {
					for _, mod := range mods {
						if s, ok := mod.(string); ok {
							step.Modifiers = append(step.Modifiers, s)
						}
					}
				}

				if d, ok := sm["delay"].(string); ok {
					if parsed, err := time.ParseDuration(d); err == nil {
						step.Delay = parsed
					}
				}

				bc.Steps = append(bc.Steps, step)
			}
		}

		devices[device] = bc
	}

	if len(devices) == 0 {
		return nil
	}
	return devices
}

// Compile-time interface checks.
var (
	_ providers.Provider               = (*Provider)(nil)
	_ providers.PowerController        = (*Provider)(nil)
	_ providers.VirtualMediaController = (*Provider)(nil)
	_ providers.BMCInfoProvider        = (*Provider)(nil)
	_ providers.BootDeviceController   = (*Provider)(nil)
)
