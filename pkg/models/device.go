// Package models provides device identification and routing for management requests.
package models

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/jetkvm/cloud-api/mgmt-api/pkg/driver"
)

// ResolveDevice identifies the target managed device from an HTTP request.
//
// The device is identified by (in priority order):
//  1. X-Device header value (matched by name or MAC)
//  2. "host" field in the RPC request body (caller should pass it explicitly)
//
// Returns an error if no device can be resolved.
func ResolveDevice(r *http.Request, dm *driver.DeviceManager) (*driver.ManagedDevice, error) {
	id := strings.TrimSpace(r.Header.Get("X-Device"))
	if id == "" {
		return nil, fmt.Errorf("X-Device header is required to identify target device")
	}

	device := dm.FindDevice(id)
	if device == nil {
		return nil, fmt.Errorf("device not found: %s", id)
	}
	return device, nil
}

// ResolveDeviceByID looks up a device by name or MAC address.
func ResolveDeviceByID(id string, dm *driver.DeviceManager) (*driver.ManagedDevice, error) {
	device := dm.FindDevice(id)
	if device == nil {
		return nil, fmt.Errorf("device not found: %s", id)
	}
	return device, nil
}
