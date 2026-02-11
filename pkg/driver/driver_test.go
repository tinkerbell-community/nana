package driver

import (
	"context"
	"testing"
)

// mockDriver implements Driver + PowerController + VirtualMediaController + BMCInfoProvider for testing.
type mockDriver struct {
	name         string
	caps         []Capability
	powerState   string
	mediaState   *VirtualMediaState
	bmcVersion   string
	openErr      error
	closeErr     error
	setPowerErr  error
	mountErr     error
	unmountErr   error
}

func (m *mockDriver) Name() string                { return m.name }
func (m *mockDriver) Capabilities() []Capability   { return m.caps }
func (m *mockDriver) Open(_ context.Context) error { return m.openErr }
func (m *mockDriver) Close() error                 { return m.closeErr }

func (m *mockDriver) GetPowerState(_ context.Context) (string, error) {
	return m.powerState, nil
}

func (m *mockDriver) SetPowerState(_ context.Context, state string) error {
	if m.setPowerErr != nil {
		return m.setPowerErr
	}
	m.powerState = state
	return nil
}

func (m *mockDriver) MountMedia(_ context.Context, url, kind string) error {
	if m.mountErr != nil {
		return m.mountErr
	}
	m.mediaState = &VirtualMediaState{Inserted: true, Image: url, Kind: kind}
	return nil
}

func (m *mockDriver) UnmountMedia(_ context.Context) error {
	if m.unmountErr != nil {
		return m.unmountErr
	}
	m.mediaState = &VirtualMediaState{}
	return nil
}

func (m *mockDriver) GetMediaState(_ context.Context) (*VirtualMediaState, error) {
	if m.mediaState == nil {
		return &VirtualMediaState{}, nil
	}
	return m.mediaState, nil
}

func (m *mockDriver) GetBMCVersion(_ context.Context) (string, error) {
	return m.bmcVersion, nil
}

func TestHasCapability(t *testing.T) {
	d := &mockDriver{
		name: "test",
		caps: []Capability{CapPowerControl, CapVirtualMedia},
	}

	if !HasCapability(d, CapPowerControl) {
		t.Error("expected driver to have CapPowerControl")
	}
	if !HasCapability(d, CapVirtualMedia) {
		t.Error("expected driver to have CapVirtualMedia")
	}
	if HasCapability(d, CapBootDevice) {
		t.Error("expected driver to NOT have CapBootDevice")
	}
}

func TestManagedDevice_ID(t *testing.T) {
	tests := []struct {
		name     string
		device   ManagedDevice
		expected string
	}{
		{
			name:     "uses name when set",
			device:   ManagedDevice{Name: "server-01", MAC: "AA:BB:CC:DD:EE:FF"},
			expected: "server-01",
		},
		{
			name:     "falls back to MAC",
			device:   ManagedDevice{MAC: "AA:BB:CC:DD:EE:FF"},
			expected: "AA:BB:CC:DD:EE:FF",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.device.ID(); got != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, got)
			}
		})
	}
}

func TestManagedDevice_HasCapability(t *testing.T) {
	dev := &ManagedDevice{
		MAC: "AA:BB:CC:DD:EE:FF",
		Drivers: []Driver{
			&mockDriver{name: "a", caps: []Capability{CapPowerControl}},
			&mockDriver{name: "b", caps: []Capability{CapVirtualMedia}},
		},
	}

	if !dev.HasCapability(CapPowerControl) {
		t.Error("expected device to have CapPowerControl")
	}
	if !dev.HasCapability(CapVirtualMedia) {
		t.Error("expected device to have CapVirtualMedia")
	}
	if dev.HasCapability(CapBootDevice) {
		t.Error("expected device to NOT have CapBootDevice")
	}
}

func TestManagedDevice_MergedCapabilities(t *testing.T) {
	dev := &ManagedDevice{
		MAC: "AA:BB:CC:DD:EE:FF",
		Drivers: []Driver{
			&mockDriver{name: "a", caps: []Capability{CapPowerControl, CapVirtualMedia}},
			&mockDriver{name: "b", caps: []Capability{CapVirtualMedia, CapBMCInfo}},
		},
	}

	caps := dev.MergedCapabilities()
	if len(caps) != 3 {
		t.Errorf("expected 3 merged capabilities, got %d: %v", len(caps), caps)
	}

	expected := map[Capability]bool{CapPowerControl: true, CapVirtualMedia: true, CapBMCInfo: true}
	for _, c := range caps {
		if !expected[c] {
			t.Errorf("unexpected capability: %s", c)
		}
	}
}

func TestManagedDevice_Controllers(t *testing.T) {
	drv := &mockDriver{
		name:       "test",
		caps:       []Capability{CapPowerControl, CapVirtualMedia, CapBMCInfo},
		powerState: "on",
		bmcVersion: "1.0.0",
	}

	dev := &ManagedDevice{
		MAC:     "AA:BB:CC:DD:EE:FF",
		Drivers: []Driver{drv},
	}

	if pc := dev.PowerController(); pc == nil {
		t.Error("expected non-nil PowerController")
	}
	if vmc := dev.VirtualMediaController(); vmc == nil {
		t.Error("expected non-nil VirtualMediaController")
	}
	if bip := dev.BMCInfoProvider(); bip == nil {
		t.Error("expected non-nil BMCInfoProvider")
	}
	if bdc := dev.BootDeviceController(); bdc != nil {
		t.Error("expected nil BootDeviceController")
	}
}

func TestNormalizeMAC(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"AA:BB:CC:DD:EE:FF", "aa:bb:cc:dd:ee:ff"},
		{"aa:bb:cc:dd:ee:ff", "aa:bb:cc:dd:ee:ff"},
		{"AA-BB-CC-DD-EE-FF", "aa:bb:cc:dd:ee:ff"},
		{"AABBCCDDEEFF", "aa:bb:cc:dd:ee:ff"},
		{"aabb.ccdd.eeff", "aa:bb:cc:dd:ee:ff"},
		{"invalid", "invalid"},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := NormalizeMAC(tt.input); got != tt.expected {
				t.Errorf("NormalizeMAC(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestMACToRedfishID(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"AA:BB:CC:DD:EE:FF", "AA-BB-CC-DD-EE-FF"},
		{"aa:bb:cc:dd:ee:ff", "AA-BB-CC-DD-EE-FF"},
		{"AA-BB-CC-DD-EE-FF", "AA-BB-CC-DD-EE-FF"},
		{"invalid", "INVALID"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := MACToRedfishID(tt.input); got != tt.expected {
				t.Errorf("MACToRedfishID(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestDeviceManager_FindDevice(t *testing.T) {
	dm := NewDeviceManager()

	dev1 := &ManagedDevice{
		Name: "server-01",
		MAC:  "AA:BB:CC:DD:EE:FF",
		Drivers: []Driver{
			&mockDriver{name: "test", caps: []Capability{CapPowerControl}},
		},
	}
	dev2 := &ManagedDevice{
		MAC: "11:22:33:44:55:66",
		Drivers: []Driver{
			&mockDriver{name: "test", caps: []Capability{CapVirtualMedia}},
		},
	}

	dm.AddDevice(dev1)
	dm.AddDevice(dev2)

	// Find by name.
	if d := dm.FindDevice("server-01"); d != dev1 {
		t.Error("expected to find dev1 by name")
	}

	// Find by name (case-insensitive).
	if d := dm.FindDevice("Server-01"); d != dev1 {
		t.Error("expected to find dev1 by name (case-insensitive)")
	}

	// Find by MAC.
	if d := dm.FindDevice("AA:BB:CC:DD:EE:FF"); d != dev1 {
		t.Error("expected to find dev1 by MAC")
	}

	// Find by MAC (different format).
	if d := dm.FindDevice("aa-bb-cc-dd-ee-ff"); d != dev1 {
		t.Error("expected to find dev1 by MAC (dash format)")
	}

	// Find dev2 by MAC.
	if d := dm.FindDevice("11:22:33:44:55:66"); d != dev2 {
		t.Error("expected to find dev2 by MAC")
	}

	// Not found.
	if d := dm.FindDevice("nonexistent"); d != nil {
		t.Error("expected nil for nonexistent device")
	}
}

func TestDeviceManager_AllDevices(t *testing.T) {
	dm := NewDeviceManager()
	dm.AddDevice(&ManagedDevice{MAC: "AA:BB:CC:DD:EE:FF"})
	dm.AddDevice(&ManagedDevice{MAC: "11:22:33:44:55:66"})

	devices := dm.AllDevices()
	if len(devices) != 2 {
		t.Errorf("expected 2 devices, got %d", len(devices))
	}
}

func TestRegistry(t *testing.T) {
	reg := NewRegistry()

	reg.Register("mock", func(cfg map[string]interface{}) (Driver, error) {
		return &mockDriver{name: "mock", caps: []Capability{CapPowerControl}}, nil
	})

	avail := reg.Available()
	if len(avail) != 1 || avail[0] != "mock" {
		t.Errorf("expected [mock], got %v", avail)
	}

	drv, err := reg.Create("mock", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if drv.Name() != "mock" {
		t.Errorf("expected driver name 'mock', got %q", drv.Name())
	}

	_, err = reg.Create("nonexistent", nil)
	if err == nil {
		t.Error("expected error for nonexistent driver")
	}
}

func TestDriverConfig_ToMap(t *testing.T) {
	cfg := &DriverConfig{
		Type:     "jetkvm",
		Host:     "192.168.1.100",
		Password: "secret",
	}

	m := cfg.ToMap()
	if m["type"] != "jetkvm" {
		t.Errorf("expected type 'jetkvm', got %v", m["type"])
	}
	if m["host"] != "192.168.1.100" {
		t.Errorf("expected host '192.168.1.100', got %v", m["host"])
	}
	if m["password"] != "secret" {
		t.Errorf("expected password 'secret', got %v", m["password"])
	}
}

func TestDriverConfig_ToMap_Empty(t *testing.T) {
	cfg := &DriverConfig{Type: "mock"}
	m := cfg.ToMap()
	if _, ok := m["host"]; ok {
		t.Error("expected no 'host' key for empty host")
	}
	if _, ok := m["password"]; ok {
		t.Error("expected no 'password' key for empty password")
	}
}
