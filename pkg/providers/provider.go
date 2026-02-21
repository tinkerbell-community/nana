// Package provider defines the capability-based provider interfaces for BMC management.
//
// Each provider implements a subset of capabilities (power control, virtual media,
// boot device, BMC info). Multiple providers can be composed for a single device,
// and the management API merges their capabilities to expose unified Redfish and
// RPC endpoints.
//
// This design is inspired by github.com/bmc-toolbox/bmclib, where different
// providers implement different BMC operations.
package providers

import (
	"context"
	"slices"
)

// Capability represents a BMC management capability that a provider can offer.
type Capability string

const (
	CapPowerControl Capability = "power_control"
	CapVirtualMedia Capability = "virtual_media"
	CapBootDevice   Capability = "boot_device"
	CapBMCInfo      Capability = "bmc_info"
)

// VirtualMediaState represents the state of virtual media on a managed system.
type VirtualMediaState struct {
	Inserted bool   `json:"inserted"`
	Image    string `json:"image"`
	Kind     string `json:"kind"`
}

// Provider is the base interface for all BMC management providers.
type Provider interface {
	// Name returns the provider type identifier (e.g., "jetkvm", "unifi").
	Name() string

	// Capabilities returns the list of capabilities this provider offers.
	Capabilities() []Capability

	// Open initializes the provider connection to the BMC/device.
	Open(ctx context.Context) error

	// Close releases provider resources and connections.
	Close() error
}

// PowerController provides power management operations.
// Providers that support CapPowerControl should implement this interface.
type PowerController interface {
	// GetPowerState returns the current power state ("on", "off", "unknown").
	GetPowerState(ctx context.Context) (string, error)

	// SetPowerState sets the power state. Valid values: "on", "off", "cycle", "reset".
	SetPowerState(ctx context.Context, state string) error
}

// VirtualMediaController provides virtual media mount/unmount operations.
// Providers that support CapVirtualMedia should implement this interface.
type VirtualMediaController interface {
	// MountMedia mounts an image from a URL. Kind is "cdrom" or "floppy".
	MountMedia(ctx context.Context, url, kind string) error

	// UnmountMedia unmounts any currently mounted virtual media.
	UnmountMedia(ctx context.Context) error

	// GetMediaState returns the current virtual media state.
	GetMediaState(ctx context.Context) (*VirtualMediaState, error)
}

// BootDeviceController provides boot device configuration.
// Providers that support CapBootDevice should implement this interface.
type BootDeviceController interface {
	// SetBootDevice sets the next boot device (e.g., "pxe", "disk", "cdrom").
	SetBootDevice(ctx context.Context, device string, persistent, efiBoot bool) error
}

// BMCInfoProvider provides BMC version and identification information.
// Providers that support CapBMCInfo should implement this interface.
type BMCInfoProvider interface {
	// GetBMCVersion returns the BMC firmware version string.
	GetBMCVersion(ctx context.Context) (string, error)
}

// HasCapability checks if a provider offers a specific capability.
func HasCapability(p Provider, c Capability) bool {
	return slices.Contains(p.Capabilities(), c)
}
