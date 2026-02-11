package driver

import (
	"fmt"
	"strings"
	"sync"
)

// DriverConfig holds typed driver configuration from YAML.
type DriverConfig struct {
	Type     string `mapstructure:"type" yaml:"type"`
	Host     string `mapstructure:"host" yaml:"host"`
	Password string `mapstructure:"password" yaml:"password"`
}

// ToMap converts a DriverConfig to a generic map for driver factories.
func (dc *DriverConfig) ToMap() map[string]interface{} {
	m := map[string]interface{}{
		"type": dc.Type,
	}
	if dc.Host != "" {
		m["host"] = dc.Host
	}
	if dc.Password != "" {
		m["password"] = dc.Password
	}
	return m
}

// Factory creates a driver instance from configuration.
type Factory func(cfg map[string]interface{}) (Driver, error)

// Registry manages driver factories and allows creating drivers by type name.
type Registry struct {
	mu        sync.RWMutex
	factories map[string]Factory
}

var defaultRegistry = NewRegistry()

// NewRegistry creates a new driver registry.
func NewRegistry() *Registry {
	return &Registry{
		factories: make(map[string]Factory),
	}
}

// Register adds a driver factory to the registry.
func (r *Registry) Register(name string, factory Factory) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.factories[name] = factory
}

// Create instantiates a driver from the registry.
func (r *Registry) Create(name string, cfg map[string]interface{}) (Driver, error) {
	r.mu.RLock()
	factory, ok := r.factories[name]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown driver type: %s", name)
	}
	return factory(cfg)
}

// Available returns the names of all registered driver types.
func (r *Registry) Available() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.factories))
	for name := range r.factories {
		names = append(names, name)
	}
	return names
}

// Register adds a driver factory to the default registry.
func Register(name string, factory Factory) {
	defaultRegistry.Register(name, factory)
}

// Create instantiates a driver from the default registry.
func Create(name string, cfg map[string]interface{}) (Driver, error) {
	return defaultRegistry.Create(name, cfg)
}

// Available returns the available driver types from the default registry.
func Available() []string {
	return defaultRegistry.Available()
}

// ManagedDevice represents a device with one or more drivers providing BMC capabilities.
type ManagedDevice struct {
	Name    string
	MAC     string
	Drivers []Driver
}

// ID returns the preferred identifier for the device (name, falling back to MAC).
func (d *ManagedDevice) ID() string {
	if d.Name != "" {
		return d.Name
	}
	return d.MAC
}

// HasCapability returns true if any driver provides the given capability.
func (d *ManagedDevice) HasCapability(cap Capability) bool {
	for _, drv := range d.Drivers {
		if HasCapability(drv, cap) {
			return true
		}
	}
	return false
}

// PowerController returns the first driver that implements PowerController, or nil.
func (d *ManagedDevice) PowerController() PowerController {
	for _, drv := range d.Drivers {
		if pc, ok := drv.(PowerController); ok && HasCapability(drv, CapPowerControl) {
			return pc
		}
	}
	return nil
}

// VirtualMediaController returns the first driver that implements VirtualMediaController, or nil.
func (d *ManagedDevice) VirtualMediaController() VirtualMediaController {
	for _, drv := range d.Drivers {
		if vmc, ok := drv.(VirtualMediaController); ok && HasCapability(drv, CapVirtualMedia) {
			return vmc
		}
	}
	return nil
}

// BootDeviceController returns the first driver that implements BootDeviceController, or nil.
func (d *ManagedDevice) BootDeviceController() BootDeviceController {
	for _, drv := range d.Drivers {
		if bdc, ok := drv.(BootDeviceController); ok && HasCapability(drv, CapBootDevice) {
			return bdc
		}
	}
	return nil
}

// BMCInfoProvider returns the first driver that implements BMCInfoProvider, or nil.
func (d *ManagedDevice) BMCInfoProvider() BMCInfoProvider {
	for _, drv := range d.Drivers {
		if bip, ok := drv.(BMCInfoProvider); ok && HasCapability(drv, CapBMCInfo) {
			return bip
		}
	}
	return nil
}

// MergedCapabilities returns the union of capabilities across all drivers.
func (d *ManagedDevice) MergedCapabilities() []Capability {
	seen := make(map[Capability]bool)
	var caps []Capability
	for _, drv := range d.Drivers {
		for _, cap := range drv.Capabilities() {
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
