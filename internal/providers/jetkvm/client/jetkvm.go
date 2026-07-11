// Package client provides a Go client for communicating with local JetKVM devices.
//
// Commands are sent as JSON-RPC 2.0 requests to the device's local HTTP
// endpoint (POST /jsonrpc) to control the device — power management, virtual
// media, keyboard input, etc. — authenticated via the device's local password
// session cookie.
//
// The /jsonrpc endpoint requires firmware that exposes the JSON-RPC dispatcher
// over local HTTP; stock firmware serves JSON-RPC only on its WebRTC data
// channel. Unlike a WebRTC session, HTTP calls never take over the device's
// single interactive session, so automation does not kick a human operator
// off the KVM console.
//
// Reference: https://github.com/jetkvm/kvm
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// PowerState represents the power state of a device managed by JetKVM.
type PowerState int

const (
	PowerOff PowerState = iota
	PowerOn
	PowerUnknown
)

var powerStateName = map[PowerState]string{
	PowerOff:     "off",
	PowerOn:      "on",
	PowerUnknown: "unknown",
}

// String returns the string representation of a PowerState.
func (ps PowerState) String() string {
	return powerStateName[ps]
}

// ATXAction represents an ATX power action supported by JetKVM.
type ATXAction string

const (
	ATXPowerOn    ATXAction = "power-on"
	ATXPowerOff   ATXAction = "power-off"
	ATXPowerCycle ATXAction = "power-cycle"
	ATXReset      ATXAction = "reset"
)

// ATXState represents the ATX power state returned by JetKVM.
type ATXState struct {
	PowerLED bool `json:"powerLED"`
	HDDLED   bool `json:"hddLED"`
}

// DCPowerState represents the DC power state returned by JetKVM.
type DCPowerState struct {
	IsOn         bool    `json:"isOn"`
	Voltage      float64 `json:"voltage"`
	Current      float64 `json:"current"`
	Power        float64 `json:"power"`
	RestoreState int     `json:"restoreState"`
}

// VideoState represents the video capture state of JetKVM.
type VideoState struct {
	Ready  bool   `json:"ready"`
	Error  string `json:"error"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Source string `json:"source"`
}

// DeviceInfo represents basic device information from JetKVM.
type DeviceInfo struct {
	AuthMode     string `json:"authMode"`
	DeviceID     string `json:"deviceId"`
	LoopbackOnly bool   `json:"loopbackOnly"`
}

// DeviceVersion represents version information from JetKVM.
type DeviceVersion struct {
	AppVersion    string `json:"appVersion"`
	SystemVersion string `json:"systemVersion"`
}

// VirtualMediaState represents the current virtual media state.
type VirtualMediaState struct {
	Source   string `json:"source"`
	Mode     string `json:"mode"`
	Filename string `json:"filename"`
	URL      string `json:"url"`
}

// StorageSpace represents storage space information on the JetKVM device.
type StorageSpace struct {
	TotalBytes int64 `json:"totalBytes"`
	UsedBytes  int64 `json:"usedBytes"`
	FreeBytes  int64 `json:"freeBytes"`
}

// WakeOnLanDevice represents a device configured for Wake-on-LAN on the JetKVM.
type WakeOnLanDevice struct {
	Name       string `json:"name"`
	MacAddress string `json:"macAddress"`
}

// Config holds the connection parameters for a JetKVM device.
type Config struct {
	Host     string
	Password string
	Timeout  time.Duration
}

// JSONRPCRequest matches the JetKVM JSON-RPC 2.0 request format.
type JSONRPCRequest struct {
	JSONRPC string         `json:"jsonrpc"`
	Method  string         `json:"method"`
	Params  map[string]any `json:"params,omitempty"`
	ID      int64          `json:"id,omitempty"`
}

// JSONRPCResponse matches the JetKVM JSON-RPC 2.0 response format.
type JSONRPCResponse struct {
	JSONRPC string `json:"jsonrpc"`
	Result  any    `json:"result,omitempty"`
	Error   any    `json:"error,omitempty"`
	ID      int64  `json:"id"`
}

// Client communicates with a local JetKVM device over its HTTP JSON-RPC
// endpoint (POST /jsonrpc).
type Client struct {
	config     *Config
	httpClient *http.Client // cookie jar carries the local auth session
	logger     *slog.Logger

	mu       sync.Mutex
	loggedIn bool

	nextID atomic.Int64
}

// NewClient creates a new JetKVM client with the given configuration.
func NewClient(config *Config) (*Client, error) {
	if config.Host == "" {
		return nil, fmt.Errorf("host is required")
	}
	if config.Timeout == 0 {
		config.Timeout = 30 * time.Second
	}

	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create cookie jar: %w", err)
	}

	return &Client{
		config: config,
		httpClient: &http.Client{
			Timeout: config.Timeout,
			Jar:     jar,
			Transport: &http.Transport{
				DisableKeepAlives: true,
			},
		},
		logger: slog.Default(),
	}, nil
}

// SetLogger sets a custom logger for the client.
func (c *Client) SetLogger(logger *slog.Logger) {
	c.logger = logger
}

// Login authenticates with the JetKVM device using a password.
// For devices in "noPassword" mode, this step can be skipped.
func (c *Client) Login(ctx context.Context) error {
	if c.config.Password == "" {
		return nil // noPassword mode
	}

	loginURL := fmt.Sprintf("http://%s/auth/login-local", c.config.Host)
	payload, _ := json.Marshal(map[string]string{"password": c.config.Password})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, loginURL, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("failed to create login request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("login request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		var errResp map[string]string
		_ = json.NewDecoder(resp.Body).Decode(&errResp)
		return fmt.Errorf("login failed (HTTP %d): %s", resp.StatusCode, errResp["error"])
	}

	c.mu.Lock()
	c.loggedIn = true
	c.mu.Unlock()

	c.logger.Info("authenticated with JetKVM device", slog.String("host", c.config.Host))
	return nil
}

// ensureAuthenticated logs in once per client lifetime; Reconnect (or an auth
// rejection in Call) resets the state. Devices in noPassword mode skip this.
func (c *Client) ensureAuthenticated(ctx context.Context) error {
	if c.config.Password == "" {
		return nil // noPassword mode
	}

	c.mu.Lock()
	loggedIn := c.loggedIn
	c.mu.Unlock()
	if loggedIn {
		return nil
	}

	return c.Login(ctx)
}

// GetDeviceInfo retrieves basic device information via the HTTP REST API.
func (c *Client) GetDeviceInfo(ctx context.Context) (*DeviceInfo, error) {
	if err := c.ensureAuthenticated(ctx); err != nil {
		return nil, err
	}

	deviceURL := fmt.Sprintf("http://%s/device", c.config.Host)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, deviceURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create device request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("device info request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("device info request failed with HTTP %d", resp.StatusCode)
	}

	var info DeviceInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, fmt.Errorf("failed to decode device info: %w", err)
	}

	return &info, nil
}

// Connect verifies connectivity by authenticating with the device when a
// password is configured. It is cheap and idempotent; the name is kept for
// compatibility with the provider's Open/ensureConnected flow.
func (c *Client) Connect(ctx context.Context) error {
	return c.ensureAuthenticated(ctx)
}

// Close is retained for interface compatibility. The HTTP transport holds no
// long-lived connection state to tear down.
func (c *Client) Close() error {
	return nil
}

// Reconnect drops the authenticated session and logs in again.
func (c *Client) Reconnect(ctx context.Context) error {
	c.mu.Lock()
	c.loggedIn = false
	c.mu.Unlock()
	return c.ensureAuthenticated(ctx)
}

// errUnauthorized signals that the device rejected the auth session cookie.
var errUnauthorized = errors.New("device rejected session credentials")

// Call sends a JSON-RPC 2.0 request to the device's local /jsonrpc endpoint
// and returns the decoded response. If the device rejects the session cookie
// (e.g. it expired or the device rebooted), it re-authenticates and retries
// once.
func (c *Client) Call(
	ctx context.Context,
	method string,
	params map[string]any,
) (*JSONRPCResponse, error) {
	if err := c.ensureAuthenticated(ctx); err != nil {
		return nil, err
	}

	resp, err := c.doCall(ctx, method, params)
	if !errors.Is(err, errUnauthorized) {
		return resp, err
	}

	// Session cookie no longer valid — log in again and retry once.
	if err := c.Reconnect(ctx); err != nil {
		return nil, err
	}
	return c.doCall(ctx, method, params)
}

// doCall performs a single JSON-RPC round trip over HTTP.
func (c *Client) doCall(
	ctx context.Context,
	method string,
	params map[string]any,
) (*JSONRPCResponse, error) {
	req := JSONRPCRequest{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
		ID:      c.nextID.Add(1),
	}

	payload, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	rpcURL := fmt.Sprintf("http://%s/jsonrpc", c.config.Host)
	httpReq, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		rpcURL,
		bytes.NewReader(payload),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create RPC request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("RPC request failed: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	switch {
	case httpResp.StatusCode == http.StatusUnauthorized ||
		httpResp.StatusCode == http.StatusForbidden:
		return nil, errUnauthorized
	case httpResp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("RPC request failed with HTTP %d", httpResp.StatusCode)
	}

	var rpcResp JSONRPCResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&rpcResp); err != nil {
		return nil, fmt.Errorf("failed to decode RPC response: %w", err)
	}

	return &rpcResp, nil
}

// --- High-Level Power Management ---

// GetDCPowerState returns the DC power state from the JetKVM device.
func (c *Client) GetDCPowerState(ctx context.Context) (*DCPowerState, error) {
	resp, err := c.Call(ctx, "getDCPowerState", nil)
	if err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("RPC error: %v", resp.Error)
	}

	data, err := json.Marshal(resp.Result)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal result: %w", err)
	}

	var state DCPowerState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("failed to parse DC power state: %w", err)
	}
	return &state, nil
}

// SetDCPowerState sets the DC power state on the JetKVM device.
func (c *Client) SetDCPowerState(ctx context.Context, enabled bool) error {
	resp, err := c.Call(ctx, "setDCPowerState", map[string]any{"enabled": enabled})
	if err != nil {
		return err
	}
	if resp.Error != nil {
		return fmt.Errorf("RPC error: %v", resp.Error)
	}
	return nil
}

// GetATXState returns the ATX power state (power LED, HDD LED).
func (c *Client) GetATXState(ctx context.Context) (*ATXState, error) {
	resp, err := c.Call(ctx, "getATXState", nil)
	if err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("RPC error: %v", resp.Error)
	}

	data, err := json.Marshal(resp.Result)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal result: %w", err)
	}

	var state ATXState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("failed to parse ATX state: %w", err)
	}
	return &state, nil
}

// SetATXPowerAction sends an ATX power action to the device.
func (c *Client) SetATXPowerAction(ctx context.Context, action ATXAction) error {
	resp, err := c.Call(ctx, "setATXPowerAction", map[string]any{"action": string(action)})
	if err != nil {
		return err
	}
	if resp.Error != nil {
		return fmt.Errorf("RPC error: %v", resp.Error)
	}
	return nil
}

// GetActiveExtension returns the currently active extension on the JetKVM device.
// Possible values: "atx-power", "dc-power", or "" (no extension).
func (c *Client) GetActiveExtension(ctx context.Context) (string, error) {
	resp, err := c.Call(ctx, "getActiveExtension", nil)
	if err != nil {
		return "", err
	}
	if resp.Error != nil {
		return "", fmt.Errorf("RPC error: %v", resp.Error)
	}

	ext, _ := resp.Result.(string)
	return ext, nil
}

// GetWakeOnLanDevices returns the list of devices configured for Wake-on-LAN.
func (c *Client) GetWakeOnLanDevices(ctx context.Context) ([]WakeOnLanDevice, error) {
	resp, err := c.Call(ctx, "getWakeOnLanDevices", nil)
	if err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("RPC error: %v", resp.Error)
	}

	data, err := json.Marshal(resp.Result)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal result: %w", err)
	}

	var devices []WakeOnLanDevice
	if err := json.Unmarshal(data, &devices); err != nil {
		return nil, fmt.Errorf("failed to parse WOL devices: %w", err)
	}
	return devices, nil
}

// GetPowerState returns the power state based on the active extension.
func (c *Client) GetPowerState(ctx context.Context) (PowerState, error) {
	ext, err := c.GetActiveExtension(ctx)
	if err != nil {
		return PowerUnknown, fmt.Errorf("failed to get active extension: %w", err)
	}

	switch ext {
	case "atx-power":
		atxState, err := c.GetATXState(ctx)
		if err != nil {
			return PowerUnknown, fmt.Errorf("failed to get ATX state: %w", err)
		}
		if atxState.PowerLED {
			return PowerOn, nil
		}
		return PowerOff, nil

	case "dc-power":
		dcState, err := c.GetDCPowerState(ctx)
		if err != nil {
			return PowerUnknown, fmt.Errorf("failed to get DC power state: %w", err)
		}
		if dcState.IsOn {
			return PowerOn, nil
		}
		return PowerOff, nil

	default:
		return PowerUnknown, fmt.Errorf("no supported power extension active: %q", ext)
	}
}

// powerStatePollInterval is how often SetPowerState polls for the desired state.
const powerStatePollInterval = 1 * time.Second

// desiredPowerState maps a power action string to the expected final PowerState.
func desiredPowerState(action string) PowerState {
	switch action {
	case "on", "cycle", "reset":
		return PowerOn
	case "off":
		return PowerOff
	default:
		return PowerUnknown
	}
}

// SetPowerState sets the power state based on the active extension and waits
// for the state to transition to the desired value.
func (c *Client) SetPowerState(ctx context.Context, state string) error {
	if err := c.SendPowerAction(ctx, state); err != nil {
		return err
	}

	return c.WaitForPowerState(ctx, desiredPowerState(state))
}

// SendPowerAction sends the power command without waiting for the state to
// transition. Use this when the caller manages its own confirmation logic
// or needs a fast, non-blocking power action.
func (c *Client) SendPowerAction(ctx context.Context, state string) error {
	ext, err := c.GetActiveExtension(ctx)
	if err != nil {
		return fmt.Errorf("failed to get active extension: %w", err)
	}

	switch ext {
	case "atx-power":
		return c.setATXPowerState(ctx, state)
	case "dc-power":
		return c.setDCPowerState(ctx, state)
	default:
		return fmt.Errorf("no supported power extension active: %q", ext)
	}
}

// WaitForPowerState polls GetPowerState until it matches the desired state
// or the context is cancelled.
func (c *Client) WaitForPowerState(ctx context.Context, desired PowerState) error {
	if desired == PowerUnknown {
		return nil
	}

	ticker := time.NewTicker(powerStatePollInterval)
	defer ticker.Stop()

	for {
		current, err := c.GetPowerState(ctx)
		if err != nil {
			c.logger.Debug("failed to poll power state", slog.String("error", err.Error()))
		} else if current == desired {
			return nil
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for power state %s: %w", desired, ctx.Err())
		case <-ticker.C:
		}
	}
}

func (c *Client) setATXPowerState(ctx context.Context, state string) error {
	switch state {
	case "on":
		return c.SetATXPowerAction(ctx, ATXPowerOn)
	case "off":
		return c.SetATXPowerAction(ctx, ATXPowerOff)
	case "cycle":
		return c.SetATXPowerAction(ctx, ATXPowerCycle)
	case "reset":
		return c.SetATXPowerAction(ctx, ATXReset)
	default:
		return fmt.Errorf("invalid power state: %s", state)
	}
}

func (c *Client) setDCPowerState(ctx context.Context, state string) error {
	switch state {
	case "on":
		return c.SetDCPowerState(ctx, true)
	case "off":
		return c.SetDCPowerState(ctx, false)
	case "cycle", "reset":
		if err := c.SetDCPowerState(ctx, false); err != nil {
			return err
		}
		return c.SetDCPowerState(ctx, true)
	default:
		return fmt.Errorf("DC power only supports on/off, got: %s", state)
	}
}

// --- Virtual Media ---

// MountWithHTTP mounts an ISO/image from a URL.
func (c *Client) MountWithHTTP(ctx context.Context, imageURL, mode string) error {
	resp, err := c.Call(ctx, "mountWithHTTP", map[string]any{"url": imageURL, "mode": mode})
	if err != nil {
		return err
	}
	if resp.Error != nil {
		return fmt.Errorf("RPC error: %v", resp.Error)
	}
	return nil
}

// UnmountImage unmounts any currently mounted virtual media.
func (c *Client) UnmountImage(ctx context.Context) error {
	resp, err := c.Call(ctx, "unmountImage", nil)
	if err != nil {
		return err
	}
	if resp.Error != nil {
		return fmt.Errorf("RPC error: %v", resp.Error)
	}
	return nil
}

// GetVirtualMediaState returns the current virtual media state.
func (c *Client) GetVirtualMediaState(ctx context.Context) (*VirtualMediaState, error) {
	resp, err := c.Call(ctx, "getVirtualMediaState", nil)
	if err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("RPC error: %v", resp.Error)
	}

	data, err := json.Marshal(resp.Result)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal result: %w", err)
	}

	var state VirtualMediaState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("failed to parse virtual media state: %w", err)
	}
	return &state, nil
}

// --- Video ---

// GetVideoState returns the current video capture state.
func (c *Client) GetVideoState(ctx context.Context) (*VideoState, error) {
	resp, err := c.Call(ctx, "getVideoState", nil)
	if err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("RPC error: %v", resp.Error)
	}

	data, err := json.Marshal(resp.Result)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal result: %w", err)
	}

	var state VideoState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("failed to parse video state: %w", err)
	}
	return &state, nil
}

// --- System ---

// GetLocalVersion returns the JetKVM device firmware version.
func (c *Client) GetLocalVersion(ctx context.Context) (*DeviceVersion, error) {
	resp, err := c.Call(ctx, "getLocalVersion", nil)
	if err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("RPC error: %v", resp.Error)
	}

	data, err := json.Marshal(resp.Result)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal result: %w", err)
	}

	var version DeviceVersion
	if err := json.Unmarshal(data, &version); err != nil {
		return nil, fmt.Errorf("failed to parse version: %w", err)
	}
	return &version, nil
}

// TryUpdate triggers an OTA update on the JetKVM device.
func (c *Client) TryUpdate(ctx context.Context) error {
	resp, err := c.Call(ctx, "tryUpdate", nil)
	if err != nil {
		return err
	}
	if resp.Error != nil {
		return fmt.Errorf("RPC error: %v", resp.Error)
	}
	return nil
}

// --- USB ---

// GetUSBState returns the current USB emulation state as a string.
// Possible values: "configured", "attached", "not attached", "suspended", "addressed".
func (c *Client) GetUSBState(ctx context.Context) (string, error) {
	resp, err := c.Call(ctx, "getUSBState", nil)
	if err != nil {
		return "", err
	}
	if resp.Error != nil {
		return "", fmt.Errorf("RPC error: %v", resp.Error)
	}
	state, _ := resp.Result.(string)
	return state, nil
}

// SetJigglerState enables or disables the mouse jiggler.
func (c *Client) SetJigglerState(ctx context.Context, enabled bool) error {
	resp, err := c.Call(ctx, "setJigglerState", map[string]any{"enabled": enabled})
	if err != nil {
		return err
	}
	if resp.Error != nil {
		return fmt.Errorf("RPC error: %v", resp.Error)
	}
	return nil
}

// --- Wake-on-LAN ---

// SendWOLMagicPacket sends a Wake-on-LAN magic packet to the specified MAC address.
func (c *Client) SendWOLMagicPacket(ctx context.Context, macAddress string) error {
	resp, err := c.Call(ctx, "sendWOLMagicPacket", map[string]any{"macAddress": macAddress})
	if err != nil {
		return err
	}
	if resp.Error != nil {
		return fmt.Errorf("RPC error: %v", resp.Error)
	}
	return nil
}

// --- Keyboard Macros ---

// KeyboardMacroStep defines a single step in a keyboard macro.
// Keys and Modifiers are string names (e.g. "enter", "ctrl", "f12").
// Delay is the pause in milliseconds after this step is sent.
type KeyboardMacroStep struct {
	Keys      []string `json:"keys"`
	Modifiers []string `json:"modifiers"`
	Delay     int      `json:"delay"`
}

// hidKeyMap maps human-readable key names to USB HID key codes.
var hidKeyMap = map[string]byte{
	// Letters
	"a": 0x04, "b": 0x05, "c": 0x06, "d": 0x07, "e": 0x08,
	"f": 0x09, "g": 0x0A, "h": 0x0B, "i": 0x0C, "j": 0x0D,
	"k": 0x0E, "l": 0x0F, "m": 0x10, "n": 0x11, "o": 0x12,
	"p": 0x13, "q": 0x14, "r": 0x15, "s": 0x16, "t": 0x17,
	"u": 0x18, "v": 0x19, "w": 0x1A, "x": 0x1B, "y": 0x1C,
	"z": 0x1D,
	// Numbers
	"1": 0x1E, "2": 0x1F, "3": 0x20, "4": 0x21, "5": 0x22,
	"6": 0x23, "7": 0x24, "8": 0x25, "9": 0x26, "0": 0x27,
	// Common keys
	"enter": 0x28, "return": 0x28,
	"escape": 0x29, "esc": 0x29,
	"backspace": 0x2A,
	"tab":       0x2B,
	"space":     0x2C,
	"minus":     0x2D, "-": 0x2D,
	"equal": 0x2E, "=": 0x2E,
	// Function keys
	"f1": 0x3A, "f2": 0x3B, "f3": 0x3C, "f4": 0x3D,
	"f5": 0x3E, "f6": 0x3F, "f7": 0x40, "f8": 0x41,
	"f9": 0x42, "f10": 0x43, "f11": 0x44, "f12": 0x45,
	// Navigation
	"insert": 0x49, "home": 0x4A, "pageup": 0x4B,
	"delete": 0x4C, "end": 0x4D, "pagedown": 0x4E,
	"right": 0x4F, "arrowright": 0x4F,
	"left": 0x50, "arrowleft": 0x50,
	"down": 0x51, "arrowdown": 0x51,
	"up": 0x52, "arrowup": 0x52,
}

// hidModifierMap maps modifier name strings to their USB HID bitmask values.
var hidModifierMap = map[string]byte{
	"ctrl": 0x01, "control": 0x01, "lctrl": 0x01, "leftctrl": 0x01,
	"shift": 0x02, "lshift": 0x02, "leftshift": 0x02,
	"alt": 0x04, "lalt": 0x04, "leftalt": 0x04,
	"meta": 0x08, "gui": 0x08, "windows": 0x08, "lgui": 0x08,
	"rctrl": 0x10, "rightctrl": 0x10,
	"rshift": 0x20, "rightshift": 0x20,
	"ralt": 0x40, "rightalt": 0x40, "altgr": 0x40,
	"rgui": 0x80, "rightgui": 0x80,
}

// hidKeyBufferSize matches the fixed key buffer size on the JetKVM device.
const hidKeyBufferSize = 6

// KeyboardReport sends a raw HID keyboard report to the device.
// modifier is the modifier bitmask byte; keys are the HID key codes (max 6).
// Call with modifier=0 and empty keys to release all keys.
func (c *Client) KeyboardReport(ctx context.Context, modifier byte, keys []byte) error {
	keysParam := make([]int, hidKeyBufferSize)
	for i, k := range keys {
		if i >= hidKeyBufferSize {
			break
		}
		keysParam[i] = int(k)
	}

	resp, err := c.Call(ctx, "keyboardReport", map[string]any{
		"modifier": int(modifier),
		"keys":     keysParam,
	})
	if err != nil {
		return err
	}
	if resp.Error != nil {
		return fmt.Errorf("RPC error: %v", resp.Error)
	}
	return nil
}

// ExecuteKeyboardMacro runs a keyboard macro client-side using keyboardReport RPC calls.
// Each step presses the specified keys/modifiers, then immediately releases them,
// then waits for the step's delay before proceeding to the next step.
func (c *Client) ExecuteKeyboardMacro(ctx context.Context, steps []KeyboardMacroStep) error {
	for _, step := range steps {
		// Build modifier byte from modifier name strings.
		var modifier byte
		for _, mod := range step.Modifiers {
			if bit, ok := hidModifierMap[strings.ToLower(mod)]; ok {
				modifier |= bit
			}
		}

		// Build key codes from key name strings (up to hidKeyBufferSize).
		var keys []byte
		for _, keyName := range step.Keys {
			lower := strings.ToLower(keyName)
			if code, ok := hidKeyMap[lower]; ok {
				keys = append(keys, code)
				if len(keys) >= hidKeyBufferSize {
					break
				}
			}
		}

		// Press the keys.
		if err := c.KeyboardReport(ctx, modifier, keys); err != nil {
			return fmt.Errorf("keyboardReport press failed: %w", err)
		}

		// Release all keys immediately.
		if err := c.KeyboardReport(ctx, 0, nil); err != nil {
			return fmt.Errorf("keyboardReport release failed: %w", err)
		}

		// Wait the step delay.
		if step.Delay > 0 {
			select {
			case <-time.After(time.Duration(step.Delay) * time.Millisecond):
			case <-ctx.Done():
				return fmt.Errorf("keyboard macro cancelled: %w", ctx.Err())
			}
		}
	}
	return nil
}

// --- EDID ---

// GetEDID returns the current EDID string.
func (c *Client) GetEDID(ctx context.Context) (string, error) {
	resp, err := c.Call(ctx, "getEDID", nil)
	if err != nil {
		return "", err
	}
	if resp.Error != nil {
		return "", fmt.Errorf("RPC error: %v", resp.Error)
	}

	edid, ok := resp.Result.(string)
	if !ok {
		return "", fmt.Errorf("unexpected result type: %T", resp.Result)
	}
	return edid, nil
}

// SetEDID sets a custom EDID string on the JetKVM device.
func (c *Client) SetEDID(ctx context.Context, edid string) error {
	resp, err := c.Call(ctx, "setEDID", map[string]any{"edid": edid})
	if err != nil {
		return err
	}
	if resp.Error != nil {
		return fmt.Errorf("RPC error: %v", resp.Error)
	}
	return nil
}

// --- Network ---

// RenewDHCPLease triggers a DHCP lease renewal on the JetKVM device.
func (c *Client) RenewDHCPLease(ctx context.Context) error {
	resp, err := c.Call(ctx, "renewDHCPLease", nil)
	if err != nil {
		return err
	}
	if resp.Error != nil {
		return fmt.Errorf("RPC error: %v", resp.Error)
	}
	return nil
}

// --- Connection Pool ---

// Pool manages a pool of JetKVM device connections.
type Pool struct {
	clients map[string]*Client
	mu      sync.RWMutex
	timeout time.Duration
}

// NewPool creates a new connection pool.
func NewPool(timeout time.Duration) *Pool {
	return &Pool{
		clients: make(map[string]*Client),
		timeout: timeout,
	}
}

// GetOrCreate returns an existing client for the device or creates a new one.
func (p *Pool) GetOrCreate(host, password string) (*Client, error) {
	p.mu.RLock()
	client, exists := p.clients[host]
	p.mu.RUnlock()

	if exists {
		return client, nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	// Double-check after acquiring write lock.
	if client, exists = p.clients[host]; exists {
		return client, nil
	}

	client, err := NewClient(&Config{
		Host:     host,
		Password: password,
		Timeout:  p.timeout,
	})
	if err != nil {
		return nil, err
	}

	p.clients[host] = client
	return client, nil
}

// Remove removes and closes a client from the pool.
func (p *Pool) Remove(host string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if client, exists := p.clients[host]; exists {
		_ = client.Close()
		delete(p.clients, host)
	}
}

// CloseAll closes all pooled connections.
func (p *Pool) CloseAll() {
	p.mu.Lock()
	defer p.mu.Unlock()

	for host, client := range p.clients {
		_ = client.Close()
		delete(p.clients, host)
	}
}

// ConnectedHosts returns the list of currently connected device hosts.
func (p *Pool) ConnectedHosts() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()

	hosts := make([]string, 0, len(p.clients))
	for host := range p.clients {
		hosts = append(hosts, host)
	}
	return hosts
}

// EnsureConnected connects to a device and returns the client, ready for RPC calls.
func (p *Pool) EnsureConnected(ctx context.Context, host, password string) (*Client, error) {
	client, err := p.GetOrCreate(host, password)
	if err != nil {
		return nil, err
	}

	if err := client.Connect(ctx); err != nil {
		p.Remove(host)
		return nil, fmt.Errorf("failed to connect to %s: %w", host, err)
	}

	return client, nil
}
