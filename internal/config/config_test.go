package config

import (
	"testing"

	"github.com/spf13/viper"
)

func clearViper() {
	viper.Reset()
}

func TestLoadConfig(t *testing.T) {
	tests := []struct {
		name        string
		setup       func()
		expectError bool
		errorMsg    string
	}{
		{
			name: "valid config with defaults",
			setup: func() {
				clearViper()
				viper.SetDefault("port", 5000)
				viper.SetDefault("address", "0.0.0.0")
				viper.SetDefault("webrtc_timeout", 30)
			},
			expectError: false,
		},
		{
			name: "valid config with all settings",
			setup: func() {
				clearViper()
				viper.Set("port", 8080)
				viper.Set("address", "127.0.0.1")
				viper.Set("webrtc_timeout", 60)
			},
			expectError: false,
		},
		{
			name: "invalid server port - too high",
			setup: func() {
				clearViper()
				viper.Set("port", 70000)
				viper.Set("webrtc_timeout", 30)
			},
			expectError: true,
			errorMsg:    "invalid server port",
		},
		{
			name: "invalid server port - zero",
			setup: func() {
				clearViper()
				viper.Set("port", 0)
				viper.Set("webrtc_timeout", 30)
			},
			expectError: true,
			errorMsg:    "invalid server port",
		},
		{
			name: "invalid webrtc timeout",
			setup: func() {
				clearViper()
				viper.Set("port", 5000)
				viper.Set("webrtc_timeout", 0)
			},
			expectError: true,
			errorMsg:    "webrtc_timeout",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setup()
			_, err := LoadConfig()
			if tt.expectError {
				if err == nil {
					t.Error("expected error but got none")
				} else if tt.errorMsg != "" && !contains(err.Error(), tt.errorMsg) {
					t.Errorf("expected error containing %q, got %q", tt.errorMsg, err.Error())
				}
			} else if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestValidateConfig(t *testing.T) {
	tests := []struct {
		name        string
		config      *Config
		expectError bool
		errorMsg    string
	}{
		{
			name: "valid config no devices",
			config: &Config{
				Port:          5000,
				Address:       "0.0.0.0",
				WebRTCTimeout: 30,
			},
			expectError: false,
		},
		{
			name: "valid config with devices",
			config: &Config{
				Port:          5000,
				Address:       "0.0.0.0",
				WebRTCTimeout: 30,
				Devices: []DeviceConfig{
					{
						Name: "server-01",
						MAC:  "AA:BB:CC:DD:EE:FF",
						Providers: []ProviderConfig{
							{Type: "jetkvm", Host: "192.168.1.100"},
						},
					},
				},
			},
			expectError: false,
		},
		{
			name: "device missing MAC",
			config: &Config{
				Port:          5000,
				WebRTCTimeout: 30,
				Devices: []DeviceConfig{
					{
						Name: "server-01",
						Providers: []ProviderConfig{
							{Type: "jetkvm", Host: "192.168.1.100"},
						},
					},
				},
			},
			expectError: true,
			errorMsg:    "mac address is required",
		},
		{
			name: "device missing providers",
			config: &Config{
				Port:          5000,
				WebRTCTimeout: 30,
				Devices: []DeviceConfig{
					{
						MAC: "AA:BB:CC:DD:EE:FF",
					},
				},
			},
			expectError: true,
			errorMsg:    "at least one provider is required",
		},
		{
			name: "provider missing type",
			config: &Config{
				Port:          5000,
				WebRTCTimeout: 30,
				Devices: []DeviceConfig{
					{
						MAC: "AA:BB:CC:DD:EE:FF",
						Providers: []ProviderConfig{
							{Host: "192.168.1.100"},
						},
					},
				},
			},
			expectError: true,
			errorMsg:    "provider type is required",
		},
		{
			name: "invalid port - negative",
			config: &Config{
				Port:          -1,
				WebRTCTimeout: 30,
			},
			expectError: true,
		},
		{
			name: "invalid port - too high",
			config: &Config{
				Port:          99999,
				WebRTCTimeout: 30,
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateConfig(tt.config)
			if tt.expectError && err == nil {
				t.Error("expected error but got none")
			}
			if !tt.expectError && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if tt.expectError && tt.errorMsg != "" && err != nil &&
				!contains(err.Error(), tt.errorMsg) {
				t.Errorf("expected error containing %q, got %q", tt.errorMsg, err.Error())
			}
		})
	}
}

func contains(str, substr string) bool {
	return len(str) >= len(substr) && containsSubstring(str, substr)
}

func containsSubstring(str, substr string) bool {
	for i := 0; i <= len(str)-len(substr); i++ {
		if str[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestProviderConfigWithDefaults(t *testing.T) {
	tests := []struct {
		name     string
		device   ProviderConfig
		defaults ProviderConfig
		expected ProviderConfig
	}{
		{
			name:     "empty device inherits all defaults",
			device:   ProviderConfig{Type: "jetkvm"},
			defaults: ProviderConfig{Type: "jetkvm", Host: "10.0.0.1", Password: "secret", APIKey: "key1", Site: "site1"},
			expected: ProviderConfig{Type: "jetkvm", Host: "10.0.0.1", Password: "secret", APIKey: "key1", Site: "site1"},
		},
		{
			name:     "device values override defaults",
			device:   ProviderConfig{Type: "jetkvm", Host: "192.168.1.1", Password: "mypass"},
			defaults: ProviderConfig{Type: "jetkvm", Host: "10.0.0.1", Password: "secret", Site: "default"},
			expected: ProviderConfig{Type: "jetkvm", Host: "192.168.1.1", Password: "mypass", Site: "default"},
		},
		{
			name:     "device boot overrides default boot",
			device:   ProviderConfig{Type: "jetkvm", Boot: []BootDeviceConfig{{Device: "pxe"}}},
			defaults: ProviderConfig{Type: "jetkvm", Boot: []BootDeviceConfig{{Device: "disk"}}},
			expected: ProviderConfig{Type: "jetkvm", Boot: []BootDeviceConfig{{Device: "pxe"}}},
		},
		{
			name:     "empty device boot inherits default boot",
			device:   ProviderConfig{Type: "jetkvm"},
			defaults: ProviderConfig{Type: "jetkvm", Boot: []BootDeviceConfig{{Device: "pxe"}}},
			expected: ProviderConfig{Type: "jetkvm", Boot: []BootDeviceConfig{{Device: "pxe"}}},
		},
		{
			name:     "no defaults changes nothing",
			device:   ProviderConfig{Type: "unifi", APIKey: "abc"},
			defaults: ProviderConfig{Type: "unifi"},
			expected: ProviderConfig{Type: "unifi", APIKey: "abc"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.device.WithDefaults(tt.defaults)
			if result.Host != tt.expected.Host {
				t.Errorf("Host: got %q, want %q", result.Host, tt.expected.Host)
			}
			if result.Password != tt.expected.Password {
				t.Errorf("Password: got %q, want %q", result.Password, tt.expected.Password)
			}
			if result.APIKey != tt.expected.APIKey {
				t.Errorf("APIKey: got %q, want %q", result.APIKey, tt.expected.APIKey)
			}
			if result.Site != tt.expected.Site {
				t.Errorf("Site: got %q, want %q", result.Site, tt.expected.Site)
			}
			if len(result.Boot) != len(tt.expected.Boot) {
				t.Errorf("Boot length: got %d, want %d", len(result.Boot), len(tt.expected.Boot))
			}
		})
	}
}

func TestConfigDefaultProvider(t *testing.T) {
	cfg := &Config{
		Port:          5000,
		WebRTCTimeout: 30,
		Providers: []ProviderConfig{
			{Type: "jetkvm", Host: "10.0.0.1", Password: "default-pass"},
			{Type: "unifi", APIKey: "global-key", Site: "default"},
		},
	}

	t.Run("found jetkvm", func(t *testing.T) {
		p, ok := cfg.DefaultProvider("jetkvm")
		if !ok {
			t.Fatal("expected to find jetkvm defaults")
		}
		if p.Host != "10.0.0.1" {
			t.Errorf("Host: got %q, want %q", p.Host, "10.0.0.1")
		}
		if p.Password != "default-pass" {
			t.Errorf("Password: got %q, want %q", p.Password, "default-pass")
		}
	})

	t.Run("found unifi", func(t *testing.T) {
		p, ok := cfg.DefaultProvider("unifi")
		if !ok {
			t.Fatal("expected to find unifi defaults")
		}
		if p.APIKey != "global-key" {
			t.Errorf("APIKey: got %q, want %q", p.APIKey, "global-key")
		}
	})

	t.Run("not found", func(t *testing.T) {
		_, ok := cfg.DefaultProvider("nonexistent")
		if ok {
			t.Error("expected not to find nonexistent provider")
		}
	})
}

func TestValidateConfigGlobalProviders(t *testing.T) {
	tests := []struct {
		name        string
		config      *Config
		expectError bool
		errorMsg    string
	}{
		{
			name: "valid global providers",
			config: &Config{
				Port:          5000,
				WebRTCTimeout: 30,
				Providers: []ProviderConfig{
					{Type: "jetkvm", Host: "10.0.0.1"},
				},
			},
			expectError: false,
		},
		{
			name: "global provider missing type",
			config: &Config{
				Port:          5000,
				WebRTCTimeout: 30,
				Providers: []ProviderConfig{
					{Host: "10.0.0.1"},
				},
			},
			expectError: true,
			errorMsg:    "providers[0]: provider type is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateConfig(tt.config)
			if tt.expectError && err == nil {
				t.Error("expected error but got none")
			}
			if !tt.expectError && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if tt.expectError && tt.errorMsg != "" && err != nil &&
				!contains(err.Error(), tt.errorMsg) {
				t.Errorf("expected error containing %q, got %q", tt.errorMsg, err.Error())
			}
		})
	}
}
