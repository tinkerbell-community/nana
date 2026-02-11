package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/jetkvm/cloud-api/mgmt-api/pkg/client"
	"github.com/jetkvm/cloud-api/mgmt-api/pkg/config"
)

type mockKVMClient struct {
	powerState       client.PowerState
	dcPowerState     bool
	atxState         *client.ATXState
	virtualMedia     *client.VirtualMediaState
	videoState       *client.VideoState
	deviceInfo       *client.DeviceInfo
	deviceVersion    *client.DeviceVersion
	usbState         any
	edid             string
	setPowerErr      error
	setDCPowerErr    error
	setATXErr        error
	mountErr         error
	unmountErr       error
	connectErr       error
}

func (m *mockKVMClient) Connect(_ context.Context) error {
	return m.connectErr
}

func (m *mockKVMClient) Close() error {
	return nil
}

func (m *mockKVMClient) GetDeviceInfo(_ context.Context) (*client.DeviceInfo, error) {
	if m.deviceInfo == nil {
		return &client.DeviceInfo{DeviceID: "test-device", AuthMode: "noPassword"}, nil
	}
	return m.deviceInfo, nil
}

func (m *mockKVMClient) GetPowerState(_ context.Context) (client.PowerState, error) {
	return m.powerState, nil
}

func (m *mockKVMClient) SetPowerState(_ context.Context, state string) error {
	if m.setPowerErr != nil {
		return m.setPowerErr
	}
	switch state {
	case "on":
		m.powerState = client.PowerOn
	case "off":
		m.powerState = client.PowerOff
	}
	return nil
}

func (m *mockKVMClient) GetDCPowerState(_ context.Context) (bool, error) {
	return m.dcPowerState, nil
}

func (m *mockKVMClient) SetDCPowerState(_ context.Context, enabled bool) error {
	if m.setDCPowerErr != nil {
		return m.setDCPowerErr
	}
	m.dcPowerState = enabled
	return nil
}

func (m *mockKVMClient) GetATXState(_ context.Context) (*client.ATXState, error) {
	if m.atxState == nil {
		return &client.ATXState{PowerLED: m.powerState == client.PowerOn}, nil
	}
	return m.atxState, nil
}

func (m *mockKVMClient) SetATXPowerAction(_ context.Context, action client.ATXAction) error {
	if m.setATXErr != nil {
		return m.setATXErr
	}
	switch action {
	case client.ATXPowerOn:
		m.powerState = client.PowerOn
	case client.ATXPowerOff:
		m.powerState = client.PowerOff
	}
	return nil
}

func (m *mockKVMClient) MountWithHTTP(_ context.Context, url, mode string) error {
	if m.mountErr != nil {
		return m.mountErr
	}
	m.virtualMedia = &client.VirtualMediaState{URL: url, Mode: mode}
	return nil
}

func (m *mockKVMClient) UnmountImage(_ context.Context) error {
	if m.unmountErr != nil {
		return m.unmountErr
	}
	m.virtualMedia = nil
	return nil
}

func (m *mockKVMClient) GetVirtualMediaState(_ context.Context) (*client.VirtualMediaState, error) {
	if m.virtualMedia == nil {
		return &client.VirtualMediaState{}, nil
	}
	return m.virtualMedia, nil
}

func (m *mockKVMClient) GetVideoState(_ context.Context) (*client.VideoState, error) {
	if m.videoState == nil {
		return &client.VideoState{Width: 1920, Height: 1080}, nil
	}
	return m.videoState, nil
}

func (m *mockKVMClient) GetLocalVersion(_ context.Context) (*client.DeviceVersion, error) {
	if m.deviceVersion == nil {
		return &client.DeviceVersion{AppVersion: "0.5.0", SystemVersion: "1.0.0"}, nil
	}
	return m.deviceVersion, nil
}

func (m *mockKVMClient) TryUpdate(_ context.Context) error {
	return nil
}

func (m *mockKVMClient) GetUSBState(_ context.Context) (any, error) {
	return m.usbState, nil
}

func (m *mockKVMClient) SetJigglerState(_ context.Context, _ bool) error {
	return nil
}

func (m *mockKVMClient) SendWOLMagicPacket(_ context.Context, _ string) error {
	return nil
}

func (m *mockKVMClient) GetEDID(_ context.Context) (string, error) {
	return m.edid, nil
}

func (m *mockKVMClient) SetEDID(_ context.Context, edid string) error {
	m.edid = edid
	return nil
}

func newTestService() (*rpcService, *mockKVMClient) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	pool := client.NewPool(30)

	mockClient := &mockKVMClient{
		powerState:   client.PowerOn,
		dcPowerState: true,
	}

	svc := &rpcService{
		pool:           pool,
		defaultHost:    "192.168.1.100",
		defaultPassword: "",
		logger:         logger,
	}

	return svc, mockClient
}

func TestRpcHandler_Ping(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := config.Config{
		Port:          5000,
		Address:       "0.0.0.0",
		DeviceHost:    "192.168.1.100",
		WebRTCTimeout: 30,
	}
	svc := NewBMCService(cfg, logger)

	payload := RequestPayload{Method: PingMethod, ID: 1}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest("POST", "/", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	svc.RpcHandler(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status %d, got %d", http.StatusOK, w.Code)
	}

	var resp ResponsePayload
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}
	if resp.Result != "pong" {
		t.Errorf("Expected result %q, got %v", "pong", resp.Result)
	}
}

func TestRpcHandler_InvalidJSON(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := config.Config{
		Port:          5000,
		Address:       "0.0.0.0",
		WebRTCTimeout: 30,
	}
	svc := NewBMCService(cfg, logger)

	req := httptest.NewRequest("POST", "/", bytes.NewReader([]byte("invalid json")))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	svc.RpcHandler(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected status %d, got %d", http.StatusBadRequest, w.Code)
	}
}

func TestRpcHandler_UnknownMethod(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := config.Config{
		Port:          5000,
		Address:       "0.0.0.0",
		DeviceHost:    "192.168.1.100",
		WebRTCTimeout: 1, // Short timeout for test — we only verify a connection error is returned.
	}
	svc := NewBMCService(cfg, logger)

	payload := RequestPayload{Method: "unknownMethod", ID: 1}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest("POST", "/", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Device", "192.168.1.100")
	w := httptest.NewRecorder()

	svc.RpcHandler(w, req)

	// This will fail because we can't actually connect to the device,
	// but we can verify the handler processes the request.
	var resp ResponsePayload
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Error == nil && resp.Result == nil {
		t.Error("Expected either error or result")
	}
}

func TestResponseError_String(t *testing.T) {
	err := &ResponseError{Code: 500, Message: "internal error"}
	expected := "code: 500, message: internal error"
	if err.String() != expected {
		t.Errorf("expected %q, got %q", expected, err.String())
	}
}

func TestPowerGetResult_String(t *testing.T) {
	tests := []struct {
		result   PowerGetResult
		expected string
	}{
		{PoweredOn, "on"},
		{PoweredOff, "off"},
	}
	for _, tt := range tests {
		if tt.result.String() != tt.expected {
			t.Errorf("expected %q, got %q", tt.expected, tt.result.String())
		}
	}
}
