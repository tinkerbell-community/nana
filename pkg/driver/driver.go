// Package driver defines the capability-based driver interfaces for BMC management.
//
// Each driver implements a subset of capabilities (power control, virtual media,
// boot device, BMC info). Multiple drivers can be composed for a single device,
// and the management API merges their capabilities to expose unified Redfish and
// RPC endpoints.
//
// This design is inspired by github.com/bmc-toolbox/bmclib, where different
// providers implement different BMC operations.
package driver

import "context"

// Capability represents a BMC management capability that a driver can provide.
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

// Driver is the base interface for all BMC management drivers.
type Driver interface {
	// Name returns the driver type identifier (e.g., "jetkvm").
	Name() string

	// Capabilities returns the list of capabilities this driver provides.
	Capabilities() []Capability

	// Open initializes the driver connection to the BMC/device.
	Open(ctx context.Context) error

	// Close releases driver resources and connections.
	Close() error
}

// PowerController provides power management operations.
// Drivers that support CapPowerControl should implement this interface.
type PowerController interface {
	// GetPowerState returns the current power state ("on", "off", "unknown").
	GetPowerState(ctx context.Context) (string, error)

	// SetPowerState sets the power state. Valid values: "on", "off", "cycle", "reset".
	SetPowerState(ctx context.Context, state string) error
}

// VirtualMediaController provides virtual media mount/unmount operations.
// Drivers that support CapVirtualMedia should implement this interface.
type VirtualMediaController interface {
	// MountMedia mounts an image from a URL. Kind is "cdrom" or "floppy".
	MountMedia(ctx context.Context, url, kind string) error

	// UnmountMedia unmounts any currently mounted virtual media.
	UnmountMedia(ctx context.Context) error

	// GetMediaState returns the current virtual media state.
	GetMediaState(ctx context.Context) (*VirtualMediaState, error)
}

// BootDeviceController provides boot device configuration.
// Drivers that support CapBootDevice should implement this interface.
type BootDeviceController interface {
	// SetBootDevice sets the next boot device (e.g., "pxe", "disk", "cdrom").
	SetBootDevice(ctx context.Context, device string, persistent, efiBoot bool) error
}

// BMCInfoProvider provides BMC version and identification information.
// Drivers that support CapBMCInfo should implement this interface.
type BMCInfoProvider interface {
	// GetBMCVersion returns the BMC firmware version string.
	GetBMCVersion(ctx context.Context) (string, error)
}

// HasCapability checks if a driver provides a specific capability.
func HasCapability(d Driver, cap Capability) bool {
	for _, c := range d.Capabilities() {
		if c == cap {
			return true
		}
	}
	return false
}
