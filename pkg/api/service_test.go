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
	"time"

	"github.com/tinkerbell-community/nana/pkg/providers"
)

// mockTestDriver implements all provider capability interfaces for testing.
type mockTestDriver struct {
	powerState  string
	mediaState  *providers.VirtualMediaState
	bmcVersion  string
	bootDevice  string
	caps        []providers.Capability
	setPowerErr error
}

func (m *mockTestDriver) Name() string                         { return "mock" }
func (m *mockTestDriver) Capabilities() []providers.Capability { return m.caps }
func (m *mockTestDriver) Open(_ context.Context) error         { return nil }
func (m *mockTestDriver) Close() error                         { return nil }

func (m *mockTestDriver) GetPowerState(_ context.Context) (string, error) {
	return m.powerState, nil
}

func (m *mockTestDriver) SetPowerState(_ context.Context, state string) error {
	if m.setPowerErr != nil {
		return m.setPowerErr
	}
	m.powerState = state
	return nil
}

func (m *mockTestDriver) MountMedia(_ context.Context, url, kind string) error {
	m.mediaState = &providers.VirtualMediaState{Inserted: true, Image: url, Kind: kind}
	return nil
}

func (m *mockTestDriver) UnmountMedia(_ context.Context) error {
	m.mediaState = &providers.VirtualMediaState{}
	return nil
}

func (m *mockTestDriver) GetMediaState(_ context.Context) (*providers.VirtualMediaState, error) {
	if m.mediaState == nil {
		return &providers.VirtualMediaState{}, nil
	}
	return m.mediaState, nil
}

func (m *mockTestDriver) GetBMCVersion(_ context.Context) (string, error) {
	return m.bmcVersion, nil
}

func (m *mockTestDriver) SetBootDevice(
	_ context.Context,
	device string,
	persistent, efiBoot bool,
) error {
	m.bootDevice = device
	return nil
}

func newTestDeviceManager() (*providers.DeviceManager, *mockTestDriver) {
	dm := providers.NewDeviceManager()

	mockDrv := &mockTestDriver{
		powerState: "on",
		bmcVersion: "1.0.0",
		caps: []providers.Capability{
			providers.CapPowerControl,
			providers.CapVirtualMedia,
			providers.CapBMCInfo,
			providers.CapBootDevice,
		},
	}

	dm.AddDevice(&providers.ManagedDevice{
		Name:      "server-01",
		MAC:       "AA:BB:CC:DD:EE:FF",
		Providers: []providers.Provider{mockDrv},
	})

	return dm, mockDrv
}

func newTestRPCService() (RpcService, *mockTestDriver) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	dm, mockDrv := newTestDeviceManager()
	svc := NewBMCService(dm, 30*time.Second, logger)
	return svc, mockDrv
}

func doRPC(
	t *testing.T,
	svc RpcService,
	device string,
	payload RequestPayload,
) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest("POST", "/rpc", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if device != "" {
		req.Header.Set("X-Device", device)
	}
	w := httptest.NewRecorder()
	svc.RpcHandler(w, req)
	return w
}

func TestRpcHandler_Ping(t *testing.T) {
	svc, _ := newTestRPCService()

	payload := RequestPayload{Method: PingMethod, ID: 1}
	w := doRPC(t, svc, "", payload)

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
	svc, _ := newTestRPCService()

	req := httptest.NewRequest("POST", "/rpc", bytes.NewReader([]byte("invalid json")))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	svc.RpcHandler(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected status %d, got %d", http.StatusBadRequest, w.Code)
	}
}

func TestRpcHandler_GetPowerState(t *testing.T) {
	svc, _ := newTestRPCService()

	payload := RequestPayload{Method: PowerGetMethod, ID: 1}
	w := doRPC(t, svc, "server-01", payload)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status %d, got %d", http.StatusOK, w.Code)
	}

	var resp ResponsePayload
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Result != "on" {
		t.Errorf("Expected power state 'on', got %v", resp.Result)
	}
}

func TestRpcHandler_SetPowerState(t *testing.T) {
	svc, mockDrv := newTestRPCService()

	payload := RequestPayload{
		Method: PowerSetMethod,
		ID:     2,
		Params: map[string]any{"state": "off"},
	}
	w := doRPC(t, svc, "server-01", payload)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status %d, got %d", http.StatusOK, w.Code)
	}

	if mockDrv.powerState != "off" {
		t.Errorf("Expected power state 'off', got %q", mockDrv.powerState)
	}
}

func TestRpcHandler_SetVirtualMedia(t *testing.T) {
	svc, mockDrv := newTestRPCService()

	payload := RequestPayload{
		Method: VirtualMediaMethod,
		ID:     3,
		Params: map[string]any{"mediaUrl": "http://example.com/boot.iso", "kind": "cdrom"},
	}
	w := doRPC(t, svc, "server-01", payload)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status %d, got %d", http.StatusOK, w.Code)
	}

	if mockDrv.mediaState == nil || mockDrv.mediaState.Image != "http://example.com/boot.iso" {
		t.Error("Expected media to be mounted")
	}
}

func TestRpcHandler_SetVirtualMedia_Unmount(t *testing.T) {
	svc, mockDrv := newTestRPCService()
	mockDrv.mediaState = &providers.VirtualMediaState{
		Inserted: true,
		Image:    "http://example.com/boot.iso",
	}

	payload := RequestPayload{
		Method: VirtualMediaMethod,
		ID:     4,
		Params: map[string]any{"mediaUrl": ""},
	}
	w := doRPC(t, svc, "server-01", payload)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status %d, got %d", http.StatusOK, w.Code)
	}
}

func TestRpcHandler_BootDevice(t *testing.T) {
	svc, mockDrv := newTestRPCService()

	payload := RequestPayload{
		Method: BootDeviceMethod,
		ID:     5,
		Params: map[string]any{"device": "pxe", "persistent": false, "efiBoot": true},
	}
	w := doRPC(t, svc, "server-01", payload)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status %d, got %d", http.StatusOK, w.Code)
	}

	if mockDrv.bootDevice != "pxe" {
		t.Errorf("Expected boot device 'pxe', got %q", mockDrv.bootDevice)
	}
}

func TestRpcHandler_UnknownMethod(t *testing.T) {
	svc, _ := newTestRPCService()

	payload := RequestPayload{Method: "unknownMethod", ID: 6}
	w := doRPC(t, svc, "server-01", payload)

	var resp ResponsePayload
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Error == nil {
		t.Error("Expected error for unknown method")
	}
}

func TestRpcHandler_DeviceNotFound(t *testing.T) {
	svc, _ := newTestRPCService()

	payload := RequestPayload{Method: PowerGetMethod, ID: 7}
	w := doRPC(t, svc, "nonexistent", payload)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected status %d, got %d", http.StatusBadRequest, w.Code)
	}
}

func TestRpcHandler_FallbackToHostField(t *testing.T) {
	svc, _ := newTestRPCService()

	// No X-Device header, but host field in body.
	payload := RequestPayload{
		Method: PowerGetMethod,
		ID:     8,
		Host:   "server-01",
	}

	body, _ := json.Marshal(payload)
	req := httptest.NewRequest("POST", "/rpc", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	svc.RpcHandler(w, req)

	var resp ResponsePayload
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Result != "on" {
		t.Errorf("Expected power state 'on', got %v (error: %v)", resp.Result, resp.Error)
	}
}

func TestRpcHandler_GetVersion(t *testing.T) {
	svc, _ := newTestRPCService()

	payload := RequestPayload{Method: GetVersionMethod, ID: 9}
	w := doRPC(t, svc, "server-01", payload)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status %d, got %d", http.StatusOK, w.Code)
	}

	var resp ResponsePayload
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Result != "1.0.0" {
		t.Errorf("Expected version '1.0.0', got %v", resp.Result)
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
