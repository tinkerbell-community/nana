// Package api provides the BMC-compatible RPC request/response types.
//
// These types implement the same JSON-RPC protocol used by the bmclib RPC provider
// (github.com/bmc-toolbox/bmclib), enabling drop-in compatibility with tools like
// Tinkerbell that communicate with BMCs via this RPC interface.
package api

import "fmt"

// Method represents a supported BMC RPC method.
type Method string

const (
	// BMC-compatible methods (bmclib/Tinkerbell).
	BootDeviceMethod   Method = "setBootDevice"
	PowerSetMethod     Method = "setPowerState"
	PowerGetMethod     Method = "getPowerState"
	VirtualMediaMethod Method = "setVirtualMedia"
	PingMethod         Method = "ping"

	// Extended methods.
	GetVersionMethod    Method = "getVersion"
	MountMediaMethod    Method = "mountMedia"
	UnmountMediaMethod  Method = "unmountMedia"
	GetMediaStateMethod Method = "getMediaState"
)

// RequestPayload represents a BMC-compatible JSON-RPC request.
type RequestPayload struct {
	ID     int64  `json:"id"`
	Host   string `json:"host"`
	Method Method `json:"method"`
	Params any    `json:"params,omitempty"`
}

// BootDeviceParams contains parameters for the setBootDevice method.
type BootDeviceParams struct {
	Device     string `json:"device"`
	Persistent bool   `json:"persistent"`
	EFIBoot    bool   `json:"efiBoot"`
}

// PowerSetParams contains parameters for the setPowerState method.
type PowerSetParams struct {
	State string `json:"state"`
}

// VirtualMediaParams contains parameters for the setVirtualMedia method.
type VirtualMediaParams struct {
	MediaURL string `json:"mediaUrl"`
	Kind     string `json:"kind"`
}

// MountMediaParams contains parameters for the mountMedia method.
type MountMediaParams struct {
	URL  string `json:"url"`
	Mode string `json:"mode"`
}

// ResponsePayload represents a BMC-compatible JSON-RPC response.
type ResponsePayload struct {
	ID     int64          `json:"id"`
	Host   string         `json:"host"`
	Result any            `json:"result,omitempty"`
	Error  *ResponseError `json:"error,omitempty"`
}

// ResponseError represents an error in the RPC response.
type ResponseError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// PowerGetResult represents the result of a power state query.
type PowerGetResult string

const (
	PoweredOn  PowerGetResult = "on"
	PoweredOff PowerGetResult = "off"
)

// String returns the string representation of a PowerGetResult.
func (p PowerGetResult) String() string {
	return string(p)
}

// String returns a formatted string representation of a ResponseError.
func (r *ResponseError) String() string {
	return fmt.Sprintf("code: %v, message: %v", r.Code, r.Message)
}
