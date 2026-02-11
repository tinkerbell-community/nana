package models

import (
	"context"
	"net/http"
	"testing"

	"github.com/jetkvm/cloud-api/mgmt-api/pkg/provider"
)

// stubDriver is a minimal driver for testing device resolution.
type stubProvider struct{}

func (s *stubProvider) Name() string                       { return "stub" }
func (s *stubProvider) Capabilities() []provider.Capability   { return []provider.Capability{provider.CapPowerControl} }
func (s *stubProvider) Open(_ context.Context) error        { return nil }
func (s *stubProvider) Close() error                        { return nil }

func newTestDeviceManager() *provider.DeviceManager {
	dm := provider.NewDeviceManager()
	dm.AddDevice(&provider.ManagedDevice{
		Name:      "server-01",
		MAC:       "AA:BB:CC:DD:EE:FF",
		Providers: []provider.Provider{&stubProvider{}},
	})
	dm.AddDevice(&provider.ManagedDevice{
		MAC:       "11:22:33:44:55:66",
		Providers: []provider.Provider{&stubProvider{}},
	})
	return dm
}

func TestResolveDevice(t *testing.T) {
	dm := newTestDeviceManager()

	tests := []struct {
		name        string
		header      string
		expectedID  string
		expectError bool
	}{
		{
			name:       "resolve by name",
			header:     "server-01",
			expectedID: "server-01",
		},
		{
			name:       "resolve by MAC",
			header:     "AA:BB:CC:DD:EE:FF",
			expectedID: "server-01",
		},
		{
			name:       "resolve by MAC (different format)",
			header:     "aa-bb-cc-dd-ee-ff",
			expectedID: "server-01",
		},
		{
			name:       "resolve device without name by MAC",
			header:     "11:22:33:44:55:66",
			expectedID: "11:22:33:44:55:66",
		},
		{
			name:        "missing header",
			header:      "",
			expectError: true,
		},
		{
			name:        "device not found",
			header:      "nonexistent",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, _ := http.NewRequest("POST", "/", nil)
			if tt.header != "" {
				req.Header.Set("X-Device", tt.header)
			}

			dev, err := ResolveDevice(req, dm)
			if tt.expectError {
				if err == nil {
					t.Error("expected error but got none")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if dev.ID() != tt.expectedID {
				t.Errorf("expected ID %q, got %q", tt.expectedID, dev.ID())
			}
		})
	}
}

func TestResolveDeviceByID(t *testing.T) {
	dm := newTestDeviceManager()

	dev, err := ResolveDeviceByID("server-01", dm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dev.ID() != "server-01" {
		t.Errorf("expected ID 'server-01', got %q", dev.ID())
	}

	_, err = ResolveDeviceByID("nonexistent", dm)
	if err == nil {
		t.Error("expected error for nonexistent device")
	}
}
