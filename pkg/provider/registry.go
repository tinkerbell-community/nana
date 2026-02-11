package provider

import (
	"fmt"
	"strings"
	"sync"
)

// ProviderConfig holds typed provider configuration from YAML.
type ProviderConfig struct {
	Type     string `mapstructure:"type" yaml:"type"`
	Host     string `mapstructure:"host" yaml:"host"`
	Password string `mapstructure:"password" yaml:"password"`
}

// ToMap converts a ProviderConfig to a generic map for provider factories.
func (pc *ProviderConfig) ToMap() map[string]interface{} {
	m := map[string]interface{}{
		"type": pc.Type,
	}
	if pc.Host != "" {
		m["host"] = pc.Host
	}
	if pc.Password != "" {
		m["password"] = pc.Password
	}
	return m
}

// Factory creates a provider instance from configuration.
type Factory func(cfg map[string]interface{}) (Provider, error)

// Registry manages provider factories and allows creating providers by type name.
type Registry struct {
	mu        sync.RWMutex
	factories map[string]Factory
}

var defaultRegistry = NewRegistry()

// NewRegistry creates a new provider registry.
func NewRegistry() *Registry {
	return &Registry{
		factories: make(map[string]Factory),
	}
}

// Register adds a provider factory to the registry.
func (r *Registry) Register(name string, factory Factory) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.factories[name] = factory
}

// Create instantiates a provider from the registry.
func (r *Registry) Create(name string, cfg map[string]interface{}) (Provider, error) {
	r.mu.RLock()
	factory, ok := r.factories[name]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown provider type: %s", name)
	}
	return factory(cfg)
}

// Available returns the names of all registered provider types.
func (r *Registry) Available() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.factories))
	for name := range r.factories {
		names = append(names, name)
	}
	return names
}

// Register adds a provider factory to the default registry.
func Register(name string, factory Factory) {
	defaultRegistry.Register(name, factory)
}

// Create instantiates a provider from the default registry.
func Create(name string, cfg map[string]interface{}) (Provider, error) {
	return defaultRegistry.Create(name, cfg)
}

// Available returns the available provider types from the default registry.
func Available() []string {
	return defaultRegistry.Available()
}

// ManagedDevice represents a device with one or more providers offering BMC capabilities.
type ManagedDevice struct {
	Name      string
	MAC       string
	Providers []Provider
}

// ID returns the preferred identifier for the device (name, falling back to MAC).
func (d *ManagedDevice) ID() string {
	if d.Name != "" {
		return d.Name
	}
	return d.MAC
}

// HasCapability returns true if any provider offers the given capability.
func (d *ManagedDevice) HasCapability(cap Capability) bool {
	for _, p := range d.Providers {
		if HasCapability(p, cap) {
			return true
		}
	}
	return false
}

// PowerController returns the first provider that implements PowerController, or nil.
func (d *ManagedDevice) PowerController() PowerController {
	for _, p := range d.Providers {
		if pc, ok := p.(PowerController); ok && HasCapability(p, CapPowerControl) {
			return pc
		}
	}
	return nil
}

// VirtualMediaController returns the first provider that implements VirtualMediaController, or nil.
func (d *ManagedDevice) VirtualMediaController() VirtualMediaController {
	for _, p := range d.Providers {
		if vmc, ok := p.(VirtualMediaController); ok && HasCapability(p, CapVirtualMedia) {
			return vmc
		}
	}
	return nil
}

// BootDeviceController returns the first provider that implements BootDeviceController, or nil.
func (d *ManagedDevice) BootDeviceController() BootDeviceController {
	for _, p := range d.Providers {
		if bdc, ok := p.(BootDeviceController); ok && HasCapability(p, CapBootDevice) {
			return bdc
		}
	}
	return nil
}

// BMCInfoProvider returns the first provider that implements BMCInfoProvider, or nil.
func (d *ManagedDevice) BMCInfoProvider() BMCInfoProvider {
	for _, p := range d.Providers {
		if bip, ok := p.(BMCInfoProvider); ok && HasCapability(p, CapBMCInfo) {
			return bip
		}
	}
	return nil
}

// MergedCapabilities returns the union of capabilities across all providers.
func (d *ManagedDevice) MergedCapabilities() []Capability {
	seen := make(map[Capability]bool)
	var caps []Capability
	for _, p := range d.Providers {
		for _, cap := range p.Capabilities() {
			if !seen[cap] {
				seen[cap] = true
				caps = append(caps, cap)
			}
		}
	}
	return caps
}

// DeviceManager manages the set of configured devices and provides lookup by name or MAC.
type DeviceManager struct {
	mu      sync.RWMutex
	devices []*ManagedDevice
	byMAC   map[string]*ManagedDevice // normalized MAC -> device
	byName  map[string]*ManagedDevice // lower-case name -> device
}

// NewDeviceManager creates a new DeviceManager.
func NewDeviceManager() *DeviceManager {
	return &DeviceManager{
		byMAC:  make(map[string]*ManagedDevice),
		byName: make(map[string]*ManagedDevice),
	}
}

// AddDevice registers a managed device.
func (dm *DeviceManager) AddDevice(d *ManagedDevice) {
	dm.mu.Lock()
	defer dm.mu.Unlock()
	dm.devices = append(dm.devices, d)
	mac := NormalizeMAC(d.MAC)
	dm.byMAC[mac] = d
	if d.Name != "" {
		dm.byName[strings.ToLower(d.Name)] = d
	}
}

// FindDevice looks up a device by name or MAC address.
// Name lookup is tried first, then MAC.
func (dm *DeviceManager) FindDevice(id string) *ManagedDevice {
	dm.mu.RLock()
	defer dm.mu.RUnlock()

	// Try by name (case-insensitive).
	if d, ok := dm.byName[strings.ToLower(id)]; ok {
		return d
	}

	// Try by MAC (any format).
	mac := NormalizeMAC(id)
	if d, ok := dm.byMAC[mac]; ok {
		return d
	}

	return nil
}

// AllDevices returns all managed devices.
func (dm *DeviceManager) AllDevices() []*ManagedDevice {
	dm.mu.RLock()
	defer dm.mu.RUnlock()
	result := make([]*ManagedDevice, len(dm.devices))
	copy(result, dm.devices)
	return result
}

// NormalizeMAC converts a MAC address to a canonical lower-case, colon-separated format.
func NormalizeMAC(mac string) string {
	// Remove all separators.
	cleaned := strings.Map(func(r rune) rune {
		if r == ':' || r == '-' || r == '.' {
			return -1
		}
		return r
	}, strings.ToLower(mac))

	if len(cleaned) != 12 {
		return strings.ToLower(mac) // return as-is if not a valid MAC
	}

	// Reformat as xx:xx:xx:xx:xx:xx
	parts := make([]string, 6)
	for i := 0; i < 6; i++ {
		parts[i] = cleaned[i*2 : i*2+2]
	}
	return strings.Join(parts, ":")
}

// MACToRedfishID converts a MAC address to a Redfish-safe ID (uses dashes).
func MACToRedfishID(mac string) string {
	cleaned := strings.Map(func(r rune) rune {
		if r == ':' || r == '-' || r == '.' {
			return -1
		}
		return r
	}, strings.ToUpper(mac))

	if len(cleaned) != 12 {
		return strings.ToUpper(mac)
	}

	parts := make([]string, 6)
	for i := 0; i < 6; i++ {
		parts[i] = cleaned[i*2 : i*2+2]
	}
	return strings.Join(parts, "-")
}
