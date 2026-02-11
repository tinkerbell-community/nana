package client

import (
	"testing"
	"time"
)

func TestNewClient(t *testing.T) {
	tests := []struct {
		name        string
		config      *Config
		expectError bool
		errorMsg    string
	}{
		{
			name:        "missing host",
			config:      &Config{Password: "test"},
			expectError: true,
			errorMsg:    "host is required",
		},
		{
			name:        "valid config with host only",
			config:      &Config{Host: "192.168.1.100"},
			expectError: false,
		},
		{
			name: "valid config with all fields",
			config: &Config{
				Host:     "192.168.1.100",
				Password: "password",
				Timeout:  60 * time.Second,
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, err := NewClient(tt.config)
			if tt.expectError {
				if err == nil {
					t.Error("expected error but got none")
				} else if tt.errorMsg != "" && err.Error() != tt.errorMsg {
					t.Errorf("expected error %q, got %q", tt.errorMsg, err.Error())
				}
				if client != nil {
					t.Error("expected nil client on error")
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if client == nil {
					t.Error("expected non-nil client")
				}
			}
		})
	}
}

func TestNewClient_DefaultTimeout(t *testing.T) {
	client, err := NewClient(&Config{Host: "192.168.1.100"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if client.config.Timeout != 30*time.Second {
		t.Errorf("expected default timeout of 30s, got %v", client.config.Timeout)
	}
}

func TestPowerState_String(t *testing.T) {
	tests := []struct {
		state    PowerState
		expected string
	}{
		{PowerOff, "off"},
		{PowerOn, "on"},
		{PowerUnknown, "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			if tt.state.String() != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, tt.state.String())
			}
		})
	}
}

func TestPool(t *testing.T) {
	pool := NewPool(30 * time.Second)
	if pool == nil {
		t.Fatal("expected non-nil pool")
	}

	hosts := pool.ConnectedHosts()
	if len(hosts) != 0 {
		t.Errorf("expected empty hosts, got %v", hosts)
	}

	// Test GetOrCreate.
	client, err := pool.GetOrCreate("192.168.1.100", "password")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if client == nil {
		t.Fatal("expected non-nil client")
	}

	hosts = pool.ConnectedHosts()
	if len(hosts) != 1 {
		t.Errorf("expected 1 host, got %d", len(hosts))
	}

	// Test idempotent GetOrCreate.
	client2, err := pool.GetOrCreate("192.168.1.100", "password")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if client2 != client {
		t.Error("expected same client instance")
	}

	// Test Remove.
	pool.Remove("192.168.1.100")
	hosts = pool.ConnectedHosts()
	if len(hosts) != 0 {
		t.Errorf("expected empty hosts after remove, got %v", hosts)
	}

	// Test CloseAll.
	_, _ = pool.GetOrCreate("192.168.1.100", "")
	_, _ = pool.GetOrCreate("192.168.1.101", "")
	pool.CloseAll()
	hosts = pool.ConnectedHosts()
	if len(hosts) != 0 {
		t.Errorf("expected empty hosts after close all, got %v", hosts)
	}
}
