// Package client provides a Go client for communicating with local JetKVM devices.
//
// JetKVM devices expose a WebSocket signaling endpoint that establishes WebRTC
// peer connections. JSON-RPC commands are sent over the WebRTC data channel to
// control the device (power management, virtual media, keyboard/mouse, etc.).
//
// Reference: https://github.com/jetkvm/kvm
package client

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/pion/webrtc/v4"
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

// wsMessage is the envelope for WebSocket signaling messages.
type wsMessage struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

// Client manages a connection to a local JetKVM device via WebRTC.
type Client struct {
	config     *Config
	httpClient *http.Client // REST API calls (DisableKeepAlives for JetKVM compat)
	wsClient   *http.Client // WebSocket upgrades (needs persistent connections)
	logger     *slog.Logger

	mu     sync.Mutex
	pc     *webrtc.PeerConnection
	dc     *webrtc.DataChannel
	closed bool

	nextID    atomic.Int64
	pending   map[int64]chan *JSONRPCResponse
	pendingMu sync.Mutex
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
		// Separate HTTP client for WebSocket upgrades: no DisableKeepAlives
		// (which would send Connection:close conflicting with Connection:Upgrade)
		// and no timeout (WebSocket connections are long-lived).
		wsClient: &http.Client{
			Jar: jar,
		},
		logger:  slog.Default(),
		pending: make(map[int64]chan *JSONRPCResponse),
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

	c.logger.Info("authenticated with JetKVM device", slog.String("host", c.config.Host))
	return nil
}

// GetDeviceInfo retrieves basic device information via the HTTP REST API.
func (c *Client) GetDeviceInfo(ctx context.Context) (*DeviceInfo, error) {
	if err := c.Login(ctx); err != nil {
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

// Connect establishes a WebRTC connection to the JetKVM device.
// This creates a data channel for sending JSON-RPC commands.
func (c *Client) Connect(ctx context.Context) error {
	c.mu.Lock()
	if c.pc != nil {
		c.mu.Unlock()
		return nil // already connected
	}
	c.closed = false

	// Authenticate first.
	if err := c.Login(ctx); err != nil {
		c.mu.Unlock()
		return fmt.Errorf("authentication failed: %w", err)
	}

	// Create WebRTC peer connection.
	peerConnection, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		c.mu.Unlock()
		return fmt.Errorf("failed to create peer connection: %w", err)
	}

	// Create a data channel for JSON-RPC messages.
	// The JetKVM device will accept the data channel and process RPC messages on it.
	dc, err := peerConnection.CreateDataChannel("rpc", nil)
	if err != nil {
		c.mu.Unlock()
		_ = peerConnection.Close()
		return fmt.Errorf("failed to create data channel: %w", err)
	}

	dcReady := make(chan struct{})

	dc.OnOpen(func() {
		c.logger.Info("data channel opened", slog.String("host", c.config.Host))
		c.mu.Lock()
		c.dc = dc
		c.mu.Unlock()
		close(dcReady)
	})

	dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		c.handleDataChannelMessage(msg)
	})

	dc.OnClose(func() {
		c.logger.Info("data channel closed", slog.String("host", c.config.Host))
		c.mu.Lock()
		c.dc = nil
		oldPC := c.pc
		c.pc = nil
		c.mu.Unlock()
		c.failPendingRequests("data channel closed")
		if oldPC != nil {
			_ = oldPC.Close()
		}
	})

	dc.OnError(func(err error) {
		c.logger.Error(
			"data channel error",
			slog.String("host", c.config.Host),
			slog.String("error", err.Error()),
		)
	})

	peerConnection.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		c.logger.Debug("ICE connection state changed",
			slog.String("host", c.config.Host),
			slog.String("state", state.String()),
		)
	})

	// Release the mutex before network operations; we only needed it for state checks and callback setup.
	c.mu.Unlock()

	// Create offer.
	offer, err := peerConnection.CreateOffer(nil)
	if err != nil {
		_ = peerConnection.Close()
		return fmt.Errorf("failed to create offer: %w", err)
	}

	if err := peerConnection.SetLocalDescription(offer); err != nil {
		_ = peerConnection.Close()
		return fmt.Errorf("failed to set local description: %w", err)
	}

	// Wait for ICE gathering to complete.
	gatherComplete := webrtc.GatheringCompletePromise(peerConnection)
	select {
	case <-gatherComplete:
	case <-ctx.Done():
		_ = peerConnection.Close()
		return fmt.Errorf("ICE gathering timed out: %w", ctx.Err())
	}

	// Connect to WebSocket signaling endpoint.
	wsURL := fmt.Sprintf("ws://%s/webrtc/signaling/client", c.config.Host)

	// Build WebSocket dial options with cookies from the HTTP client.
	wsDialOpts := &websocket.DialOptions{
		HTTPClient: c.wsClient,
	}

	ws, _, err := websocket.Dial(ctx, wsURL, wsDialOpts)
	if err != nil {
		_ = peerConnection.Close()
		return fmt.Errorf("failed to connect to WebSocket: %w", err)
	}

	// Read the initial device-metadata message.
	var metadataMsg wsMessage
	if err := wsjson.Read(ctx, ws, &metadataMsg); err != nil {
		_ = ws.Close(websocket.StatusNormalClosure, "")
		_ = peerConnection.Close()
		return fmt.Errorf("failed to read device metadata: %w", err)
	}
	c.logger.Debug("received device metadata", slog.String("type", metadataMsg.Type))

	// Send the offer via WebSocket.
	// JetKVM expects the SDP as a base64-encoded JSON SessionDescription.
	localDesc := peerConnection.LocalDescription()
	localDescJSON, err := json.Marshal(localDesc)
	if err != nil {
		_ = ws.Close(websocket.StatusNormalClosure, "")
		_ = peerConnection.Close()
		return fmt.Errorf("failed to marshal local description: %w", err)
	}
	sdpB64 := base64.StdEncoding.EncodeToString(localDescJSON)
	offerMsg := map[string]any{
		"type": "offer",
		"data": map[string]any{
			"sd": sdpB64,
		},
	}
	if err := wsjson.Write(ctx, ws, offerMsg); err != nil {
		_ = ws.Close(websocket.StatusNormalClosure, "")
		_ = peerConnection.Close()
		return fmt.Errorf("failed to send offer: %w", err)
	}

	// Read messages from WebSocket to get the answer and ICE candidates.
	answerReceived := false
	for !answerReceived {
		var msg wsMessage
		if err := wsjson.Read(ctx, ws, &msg); err != nil {
			_ = ws.Close(websocket.StatusNormalClosure, "")
			_ = peerConnection.Close()
			return fmt.Errorf("failed to read WebSocket message: %w", err)
		}

		switch msg.Type {
		case "answer":
			// The answer data is a base64-encoded JSON SessionDescription string.
			var answerB64 string
			if err := json.Unmarshal(msg.Data, &answerB64); err != nil {
				_ = ws.Close(websocket.StatusNormalClosure, "")
				_ = peerConnection.Close()
				return fmt.Errorf("failed to parse answer data: %w", err)
			}

			answerJSON, err := base64.StdEncoding.DecodeString(answerB64)
			if err != nil {
				_ = ws.Close(websocket.StatusNormalClosure, "")
				_ = peerConnection.Close()
				return fmt.Errorf("failed to base64-decode answer: %w", err)
			}

			var answer webrtc.SessionDescription
			if err := json.Unmarshal(answerJSON, &answer); err != nil {
				_ = ws.Close(websocket.StatusNormalClosure, "")
				_ = peerConnection.Close()
				return fmt.Errorf("failed to parse answer SDP: %w", err)
			}

			if err := peerConnection.SetRemoteDescription(answer); err != nil {
				_ = ws.Close(websocket.StatusNormalClosure, "")
				_ = peerConnection.Close()
				return fmt.Errorf("failed to set remote description: %w", err)
			}
			answerReceived = true

		case "new-ice-candidate":
			var candidate webrtc.ICECandidateInit
			if err := json.Unmarshal(msg.Data, &candidate); err != nil {
				c.logger.Warn("failed to parse ICE candidate", slog.String("error", err.Error()))
				continue
			}
			if candidate.Candidate == "" {
				continue
			}
			if err := peerConnection.AddICECandidate(candidate); err != nil {
				c.logger.Warn("failed to add ICE candidate", slog.String("error", err.Error()))
			}
		}
	}

	// Start a goroutine to handle ongoing ICE candidates from WebSocket.
	go func() {
		defer func() { _ = ws.Close(websocket.StatusNormalClosure, "") }()
		for {
			var msg wsMessage
			if err := wsjson.Read(context.Background(), ws, &msg); err != nil {
				return
			}
			if msg.Type == "new-ice-candidate" {
				var candidate webrtc.ICECandidateInit
				if err := json.Unmarshal(msg.Data, &candidate); err != nil {
					continue
				}
				if candidate.Candidate != "" {
					_ = peerConnection.AddICECandidate(candidate)
				}
			}
		}
	}()

	c.mu.Lock()
	c.pc = peerConnection
	c.mu.Unlock()

	// Wait for data channel to be ready.
	select {
	case <-dcReady:
		c.logger.Info("WebRTC connection established", slog.String("host", c.config.Host))
		return nil
	case <-ctx.Done():
		c.mu.Lock()
		_ = peerConnection.Close()
		c.pc = nil
		c.mu.Unlock()
		return fmt.Errorf("data channel open timed out: %w", ctx.Err())
	}
}

// Close closes the WebRTC connection to the JetKVM device.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.closed = true

	if c.dc != nil {
		_ = c.dc.Close()
		c.dc = nil
	}
	if c.pc != nil {
		err := c.pc.Close()
		c.pc = nil
		return err
	}
	return nil
}

// Reconnect forces a close and re-establishes the WebRTC connection.
func (c *Client) Reconnect(ctx context.Context) error {
	c.failPendingRequests("reconnecting")
	_ = c.Close()
	return c.Connect(ctx)
}

// Call sends a JSON-RPC request over the WebRTC data channel and waits for the response.
// If the data channel is closed, it attempts to reconnect before failing.
func (c *Client) Call(
	ctx context.Context,
	method string,
	params map[string]any,
) (*JSONRPCResponse, error) {
	c.mu.Lock()
	dc := c.dc
	c.mu.Unlock()

	if dc == nil {
		c.logger.Info("data channel nil, attempting reconnect", slog.String("host", c.config.Host))
		if err := c.Reconnect(ctx); err != nil {
			return nil, fmt.Errorf("reconnect failed: %w", err)
		}
		c.mu.Lock()
		dc = c.dc
		c.mu.Unlock()
		if dc == nil {
			return nil, fmt.Errorf("not connected: data channel is nil after reconnect")
		}
	}

	id := c.nextID.Add(1)

	req := JSONRPCRequest{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
		ID:      id,
	}

	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Register a response channel for this request ID.
	respCh := make(chan *JSONRPCResponse, 1)
	c.pendingMu.Lock()
	c.pending[id] = respCh
	c.pendingMu.Unlock()

	defer func() {
		c.pendingMu.Lock()
		delete(c.pending, id)
		c.pendingMu.Unlock()
	}()

	// Send the request.
	if err := dc.SendText(string(data)); err != nil {
		return nil, fmt.Errorf("failed to send RPC request: %w", err)
	}

	// Wait for the response.
	select {
	case resp := <-respCh:
		return resp, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("RPC call timed out: %w", ctx.Err())
	}
}

// failPendingRequests sends error responses to all pending RPC calls.
// This is called when the data channel closes unexpectedly so callers
// don't hang until their context times out.
func (c *Client) failPendingRequests(reason string) {
	c.pendingMu.Lock()
	pending := c.pending
	c.pending = make(map[int64]chan *JSONRPCResponse)
	c.pendingMu.Unlock()

	for _, ch := range pending {
		ch <- &JSONRPCResponse{
			JSONRPC: "2.0",
			Error:   reason,
		}
	}
}

// handleDataChannelMessage processes incoming messages on the data channel.
func (c *Client) handleDataChannelMessage(msg webrtc.DataChannelMessage) {
	var resp JSONRPCResponse
	if err := json.Unmarshal(msg.Data, &resp); err != nil {
		c.logger.Warn("failed to parse data channel message",
			slog.String("error", err.Error()),
			slog.String("data", string(msg.Data)),
		)
		return
	}

	c.pendingMu.Lock()
	ch, ok := c.pending[resp.ID]
	c.pendingMu.Unlock()

	if ok {
		ch <- &resp
	} else {
		// This may be an event/notification from the device (no matching request).
		c.logger.Debug("received unmatched response",
			slog.Int64("id", resp.ID),
			slog.String("data", string(msg.Data)),
		)
	}
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
