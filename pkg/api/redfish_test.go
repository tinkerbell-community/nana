package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jetkvm/cloud-api/mgmt-api/pkg/providers"
)

func newTestRedfishService() (RedfishService, *mockTestDriver) {
	dm, mockDrv := newTestDeviceManager()

	// Add a second device with no name (MAC-only).
	dm.AddDevice(&providers.ManagedDevice{
		MAC: "11:22:33:44:55:66",
		Providers: []providers.Provider{
			&mockTestDriver{
				powerState: "off",
				caps:       []providers.Capability{providers.CapPowerControl},
			},
		},
	})

	svc := NewRedfishService(dm)
	return svc, mockDrv
}

func TestRedfish_ServiceRoot(t *testing.T) {
	svc, _ := newTestRedfishService()

	req := httptest.NewRequest("GET", "/redfish/v1/", nil)
	w := httptest.NewRecorder()
	svc.ServiceRoot(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected %d, got %d", http.StatusOK, w.Code)
	}

	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body["Id"] != "RootService" {
		t.Errorf("expected Id 'RootService', got %v", body["Id"])
	}
	if body["@odata.type"] != "#ServiceRoot.v1_5_0.ServiceRoot" {
		t.Errorf("unexpected @odata.type: %v", body["@odata.type"])
	}
}

func TestRedfish_Systems(t *testing.T) {
	svc, _ := newTestRedfishService()

	req := httptest.NewRequest("GET", "/redfish/v1/Systems", nil)
	w := httptest.NewRecorder()
	svc.Systems(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected %d, got %d", http.StatusOK, w.Code)
	}

	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if members, ok := body["Members"].([]any); ok {
		if len(members) != 2 {
			t.Errorf("expected 2 members, got %d", len(members))
		}
	} else {
		t.Errorf("Members field missing or of wrong type")
	}
}

func TestRedfish_System_ByName(t *testing.T) {
	svc, _ := newTestRedfishService()

	mux := http.NewServeMux()
	RegisterRedfishRoutes(mux, svc)

	req := httptest.NewRequest("GET", "/redfish/v1/Systems/server-01", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected %d, got %d", http.StatusOK, w.Code)
	}

	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body["Id"] != "server-01" {
		t.Errorf("expected Id 'server-01', got %v", body["Id"])
	}
	if body["PowerState"] != "On" {
		t.Errorf("expected PowerState 'On', got %v", body["PowerState"])
	}
}

func TestRedfish_System_ByMAC(t *testing.T) {
	svc, _ := newTestRedfishService()

	mux := http.NewServeMux()
	RegisterRedfishRoutes(mux, svc)

	req := httptest.NewRequest("GET", "/redfish/v1/Systems/11-22-33-44-55-66", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected %d, got %d", http.StatusOK, w.Code)
	}

	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body["PowerState"] != "Off" {
		t.Errorf("expected PowerState 'Off', got %v", body["PowerState"])
	}
}

func TestRedfish_System_NotFound(t *testing.T) {
	svc, _ := newTestRedfishService()

	mux := http.NewServeMux()
	RegisterRedfishRoutes(mux, svc)

	req := httptest.NewRequest("GET", "/redfish/v1/Systems/nonexistent", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected %d, got %d", http.StatusNotFound, w.Code)
	}
}

func TestRedfish_SystemReset(t *testing.T) {
	svc, mockDrv := newTestRedfishService()

	mux := http.NewServeMux()
	RegisterRedfishRoutes(mux, svc)

	payload, _ := json.Marshal(map[string]string{"ResetType": "ForceOff"})
	req := httptest.NewRequest(
		"POST",
		"/redfish/v1/Systems/server-01/Actions/ComputerSystem.Reset",
		bytes.NewReader(payload),
	)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("expected %d, got %d: %s", http.StatusNoContent, w.Code, w.Body.String())
	}

	if mockDrv.powerState != "off" {
		t.Errorf("expected power state 'off', got %q", mockDrv.powerState)
	}
}

func TestRedfish_SystemReset_InvalidType(t *testing.T) {
	svc, _ := newTestRedfishService()

	mux := http.NewServeMux()
	RegisterRedfishRoutes(mux, svc)

	payload, _ := json.Marshal(map[string]string{"ResetType": "InvalidType"})
	req := httptest.NewRequest(
		"POST",
		"/redfish/v1/Systems/server-01/Actions/ComputerSystem.Reset",
		bytes.NewReader(payload),
	)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected %d, got %d", http.StatusBadRequest, w.Code)
	}
}

func TestRedfish_VirtualMediaCollection(t *testing.T) {
	svc, _ := newTestRedfishService()

	mux := http.NewServeMux()
	RegisterRedfishRoutes(mux, svc)

	req := httptest.NewRequest("GET", "/redfish/v1/Systems/server-01/VirtualMedia", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected %d, got %d", http.StatusOK, w.Code)
	}

	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if count, ok := body["Members@odata.count"].(float64); ok && count != 1 {
		t.Errorf("expected 1 member, got %v", count)
	}
}

func TestRedfish_VirtualMedia_Instance(t *testing.T) {
	svc, _ := newTestRedfishService()

	mux := http.NewServeMux()
	RegisterRedfishRoutes(mux, svc)

	req := httptest.NewRequest("GET", "/redfish/v1/Systems/server-01/VirtualMedia/1", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected %d, got %d", http.StatusOK, w.Code)
	}
}

func TestRedfish_VirtualMedia_InsertEject(t *testing.T) {
	svc, mockDrv := newTestRedfishService()

	mux := http.NewServeMux()
	RegisterRedfishRoutes(mux, svc)

	// Insert media.
	payload, _ := json.Marshal(map[string]any{
		"Image":    "http://example.com/boot.iso",
		"Inserted": true,
	})
	req := httptest.NewRequest(
		"POST",
		"/redfish/v1/Systems/server-01/VirtualMedia/1/Actions/VirtualMedia.InsertMedia",
		bytes.NewReader(payload),
	)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("expected %d, got %d: %s", http.StatusNoContent, w.Code, w.Body.String())
	}
	if mockDrv.mediaState == nil || !mockDrv.mediaState.Inserted {
		t.Error("expected media to be inserted")
	}

	// Eject media.
	req = httptest.NewRequest(
		"POST",
		"/redfish/v1/Systems/server-01/VirtualMedia/1/Actions/VirtualMedia.EjectMedia",
		nil,
	)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("expected %d, got %d: %s", http.StatusNoContent, w.Code, w.Body.String())
	}
}

func TestRedfish_Managers(t *testing.T) {
	svc, _ := newTestRedfishService()

	req := httptest.NewRequest("GET", "/redfish/v1/Managers", nil)
	w := httptest.NewRecorder()
	svc.Managers(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected %d, got %d", http.StatusOK, w.Code)
	}

	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if members, ok := body["Members"].([]any); ok {
		// Only server-01 has BMCInfo capability.
		if len(members) != 1 {
			t.Errorf("expected 1 manager, got %d", len(members))
		}
	}
}

func TestRedfish_Manager(t *testing.T) {
	svc, _ := newTestRedfishService()

	mux := http.NewServeMux()
	RegisterRedfishRoutes(mux, svc)

	req := httptest.NewRequest("GET", "/redfish/v1/Managers/server-01", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected %d, got %d", http.StatusOK, w.Code)
	}

	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body["ManagerType"] != "BMC" {
		t.Errorf("expected ManagerType 'BMC', got %v", body["ManagerType"])
	}
	if body["FirmwareVersion"] != "1.0.0" {
		t.Errorf("expected FirmwareVersion '1.0.0', got %v", body["FirmwareVersion"])
	}
}

func TestRedfishPowerStateMapping(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"on", "On"},
		{"off", "Off"},
		{"unknown", "Unknown"},
		{"", "Unknown"},
	}
	for _, tt := range tests {
		if got := redfishPowerState(tt.input); got != tt.expected {
			t.Errorf("redfishPowerState(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestRedfishResetMapping(t *testing.T) {
	tests := []struct {
		resetType string
		expected  string
	}{
		{"On", "on"},
		{"ForceOff", "off"},
		{"GracefulShutdown", "off"},
		{"ForceRestart", "cycle"},
		{"Nmi", "reset"},
		{"Invalid", ""},
	}
	for _, tt := range tests {
		if got := redfishResetToDriverState(tt.resetType); got != tt.expected {
			t.Errorf("redfishResetToDriverState(%q) = %q, want %q", tt.resetType, got, tt.expected)
		}
	}
}
