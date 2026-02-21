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
