package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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

// ---------------------------------------------------------------------------
// HTTP JSON-RPC transport
// ---------------------------------------------------------------------------

const testPassword = "secret"

// fakeDevice emulates the patched JetKVM firmware surface nana talks to:
// local password auth issuing a session cookie, and POST /jsonrpc.
type fakeDevice struct {
	t *testing.T

	mu       sync.Mutex
	logins   int
	rpcCalls []JSONRPCRequest
	// reject401 causes the next N /jsonrpc requests to be rejected with 401,
	// simulating an expired session cookie.
	reject401 int

	handler func(req JSONRPCRequest) any
	srv     *httptest.Server
}

func newFakeDevice(
	t *testing.T,
	password string,
	handler func(req JSONRPCRequest) any,
) *fakeDevice {
	fd := &fakeDevice{t: t, handler: handler}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /auth/login-local", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["password"] != password {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid password"})
			return
		}
		fd.mu.Lock()
		fd.logins++
		fd.mu.Unlock()
		http.SetCookie(w, &http.Cookie{Name: "authToken", Value: "tok", Path: "/"})
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("POST /jsonrpc", func(w http.ResponseWriter, r *http.Request) {
		fd.mu.Lock()
		reject := fd.reject401 > 0
		if reject {
			fd.reject401--
		}
		fd.mu.Unlock()
		if reject {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		if password != "" {
			cookie, err := r.Cookie("authToken")
			if err != nil || cookie.Value != "tok" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
		}

		var req JSONRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		fd.mu.Lock()
		fd.rpcCalls = append(fd.rpcCalls, req)
		fd.mu.Unlock()

		_ = json.NewEncoder(w).Encode(JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  fd.handler(req),
		})
	})

	fd.srv = httptest.NewServer(mux)
	t.Cleanup(fd.srv.Close)
	return fd
}

func (fd *fakeDevice) host() string {
	return strings.TrimPrefix(fd.srv.URL, "http://")
}

func (fd *fakeDevice) client(t *testing.T, password string) *Client {
	c, err := NewClient(&Config{
		Host:     fd.host(),
		Password: password,
		Timeout:  5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

func (fd *fakeDevice) calls() []JSONRPCRequest {
	fd.mu.Lock()
	defer fd.mu.Unlock()
	return append([]JSONRPCRequest(nil), fd.rpcCalls...)
}

func TestCallAuthenticatesOnceAndSendsRPC(t *testing.T) {
	fd := newFakeDevice(t, testPassword, func(req JSONRPCRequest) any {
		if req.Method != "getActiveExtension" {
			t.Errorf("unexpected method %q", req.Method)
		}
		return "atx-power"
	})
	c := fd.client(t, testPassword)

	for range 2 {
		resp, err := c.Call(context.Background(), "getActiveExtension", nil)
		if err != nil {
			t.Fatalf("Call: %v", err)
		}
		if resp.Result != "atx-power" {
			t.Fatalf("unexpected result: %v", resp.Result)
		}
	}

	fd.mu.Lock()
	defer fd.mu.Unlock()
	if fd.logins != 1 {
		t.Fatalf("expected exactly 1 login, got %d", fd.logins)
	}
	if len(fd.rpcCalls) != 2 {
		t.Fatalf("expected 2 RPC calls, got %d", len(fd.rpcCalls))
	}
}

func TestCallNoPasswordSkipsLogin(t *testing.T) {
	fd := newFakeDevice(t, "", func(_ JSONRPCRequest) any { return "ok" })
	c := fd.client(t, "")

	if _, err := c.Call(context.Background(), "ping", nil); err != nil {
		t.Fatalf("Call: %v", err)
	}

	fd.mu.Lock()
	defer fd.mu.Unlock()
	if fd.logins != 0 {
		t.Fatalf("expected no logins in noPassword mode, got %d", fd.logins)
	}
}

func TestCallReauthenticatesAfterSessionExpiry(t *testing.T) {
	fd := newFakeDevice(t, testPassword, func(_ JSONRPCRequest) any { return "ok" })
	c := fd.client(t, testPassword)

	// Warm login, then simulate the device dropping the session.
	if _, err := c.Call(context.Background(), "ping", nil); err != nil {
		t.Fatalf("first Call: %v", err)
	}
	fd.mu.Lock()
	fd.reject401 = 1
	fd.mu.Unlock()

	if _, err := c.Call(context.Background(), "ping", nil); err != nil {
		t.Fatalf("Call after expiry: %v", err)
	}

	fd.mu.Lock()
	defer fd.mu.Unlock()
	if fd.logins != 2 {
		t.Fatalf("expected re-login after 401, got %d logins", fd.logins)
	}
}

func TestGetATXStateDecodesResult(t *testing.T) {
	fd := newFakeDevice(t, "", func(_ JSONRPCRequest) any {
		return map[string]any{"powerLED": true, "hddLED": false}
	})
	c := fd.client(t, "")

	state, err := c.GetATXState(context.Background())
	if err != nil {
		t.Fatalf("GetATXState: %v", err)
	}
	if !state.PowerLED || state.HDDLED {
		t.Fatalf("unexpected state: %+v", state)
	}
}

func TestExecuteKeyboardMacroSendsPressAndRelease(t *testing.T) {
	fd := newFakeDevice(t, "", func(_ JSONRPCRequest) any { return nil })
	c := fd.client(t, "")

	steps := []KeyboardMacroStep{
		{Keys: []string{"f12"}, Delay: 1},
	}
	if err := c.ExecuteKeyboardMacro(context.Background(), steps); err != nil {
		t.Fatalf("ExecuteKeyboardMacro: %v", err)
	}

	calls := fd.calls()
	if len(calls) != 2 {
		t.Fatalf("expected press+release (2 calls), got %d", len(calls))
	}
	for _, call := range calls {
		if call.Method != "keyboardReport" {
			t.Fatalf("unexpected method %q", call.Method)
		}
	}

	firstKey := func(call JSONRPCRequest) int {
		keys, ok := call.Params["keys"].([]any)
		if !ok || len(keys) == 0 {
			t.Fatalf("keys param has unexpected shape: %v", call.Params["keys"])
		}
		code, ok := keys[0].(float64)
		if !ok {
			t.Fatalf("key code has unexpected type: %T", keys[0])
		}
		return int(code)
	}

	if firstKey(calls[0]) != int(hidKeyMap["f12"]) {
		t.Fatalf("press should carry the F12 HID code, got %v", calls[0].Params["keys"])
	}
	if firstKey(calls[1]) != 0 {
		t.Fatalf("release should carry zeroed keys, got %v", calls[1].Params["keys"])
	}
}
