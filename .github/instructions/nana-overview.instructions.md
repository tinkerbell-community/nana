---
applyTo: "**"
---

# Nana — AI Agent Instructions

## Project Identity

**Nana** is a device management service that provides BMC (Baseboard Management Controller) emulation and stitching for hardware that lacks traditional BMC support. It is a companion service to the [Tinkerbell](https://tinkerbell.org/) bare metal provisioning engine.

- **Repository:** `github.com/tinkerbell-community/nana`
- **Module:** `github.com/tinkerbell-community/nana`
- **Language:** Go 1.24+
- **Binary entrypoint:** `cmd/nana/main.go`
- **Binary name:** `nana` (historically `jetkvm-api`)

## Two Core Functions

1. **BMC Emulation & Stitching** — Compose capabilities from multiple pluggable providers into a unified Redfish and JSON-RPC BMC interface. Currently implemented and the primary feature.
2. **Device Discovery** — Automated device inventory population via network discovery, provider APIs, OUI matching, and HookOS integration. Currently planned/in progress.

## Architecture Overview

### Component Map

```
cmd/nana/main.go          → CLI entrypoint (Cobra), HTTP server, provider registration
internal/api/payload.go   → JSON-RPC types, method constants
internal/api/service.go   → bmclib-compatible RPC handler
internal/api/redfish.go   → Redfish v1 REST API (OData-compliant)
internal/config/config.go → YAML/env/flag config via Viper
internal/providers/
  providers.go            → Capability interfaces (Provider, PowerController, etc.)
  registry.go             → Factory pattern, DeviceManager, ManagedDevice
  jetkvm/                 → JetKVM provider (WebRTC, power, media, boot macros, info)
  unifi/                  → UniFi provider (PoE power via SSH swctrl)
```

### Key Abstractions

| Type | Package | Purpose |
|---|---|---|
| `Provider` | `providers` | Base interface: Name, Capabilities, Open, Close |
| `PowerController` | `providers` | GetPowerState, SetPowerState |
| `VirtualMediaController` | `providers` | MountMedia, UnmountMedia, GetMediaState |
| `BootDeviceController` | `providers` | SetBootDevice |
| `BMCInfoProvider` | `providers` | GetBMCVersion |
| `ManagedDevice` | `providers` | Holds MAC, name, and slice of Providers; merges capabilities |
| `DeviceManager` | `providers` | Thread-safe device registry; lookup by name or MAC |
| `Registry` | `providers` | Factory registry for provider types |
| `Config` | `config` | Top-level config struct with server + device settings |
| `RequestPayload` / `ResponsePayload` | `api` | JSON-RPC wire format |

### Capability System

Providers declare capabilities via `Capabilities() []Capability`. Four capabilities exist:

- `power_control` — Get/set power state (on, off, cycle, reset)
- `virtual_media` — Mount/unmount ISO images via HTTP URL
- `boot_device` — Set next boot device (boot macros for JetKVM)
- `bmc_info` — Get BMC firmware version

A `ManagedDevice` can have multiple providers. Calling e.g. `dev.PowerController()` returns the **first** provider implementing `PowerController` with `CapPowerControl`. Order in config determines priority.

### API Dual-Protocol

Nana exposes the same capabilities through two APIs:

1. **JSON-RPC** (`POST /rpc`) — bmclib-compatible, device identified by `X-Device` header or `host` body field
2. **Redfish v1** (`/redfish/v1/*`) — DMTF standard REST API, device identified by system ID in URL path

Both APIs resolve devices through `DeviceManager.FindDevice()` which does case-insensitive name lookup then normalized MAC lookup.

## Provider Implementation Guide

### Adding a New Provider

1. Create a package under `internal/providers/<name>/`
2. Implement `providers.Provider` interface (Name, Capabilities, Open, Close)
3. Implement capability interfaces needed (PowerController, VirtualMediaController, etc.)
4. Add compile-time checks: `var _ providers.Provider = (*Provider)(nil)`
5. Register via `init()`: `providers.Register("<name>", factoryFunc)`
6. Import with blank identifier in `cmd/nana/main.go`: `_ "github.com/tinkerbell-community/nana/internal/providers/<name>"`
7. Add any new config fields to `config.ProviderConfig`
8. Update the factory to parse provider-specific config from `map[string]any`

### Provider Factory Pattern

All providers are created via factory functions registered in the global `Registry`:

```go
func init() {
    providers.Register("myprovider", func(cfg map[string]any) (providers.Provider, error) {
        host, _ := cfg["host"].(string)
        // validate, construct, return
        return &Provider{host: host}, nil
    })
}
```

The `cfg` map is built in `cmd/nana/main.go:buildDevices()` from the YAML provider config. Keys include: `type`, `host`, `password`, `mac`, `api_key`, `site`, `boot`.

### Currently Supported Providers

| Provider | Capabilities | Connection Method | Key Deps |
|---|---|---|---|
| `jetkvm` | power, media, boot, info | WebRTC data channel (JSON-RPC) | `pion/webrtc`, `coder/websocket` |
| `unifi` | power | UniFi API + SSH to switches | `go-unifi`, `x/crypto/ssh` |

## Coding Conventions

### Architecture Rules

- **No global state** — Configuration flows through function parameters. Exception: provider `init()` registration is allowed via the global `Registry`.
- **`context.Context` as first parameter** — All provider methods and handlers take `ctx`.
- **Accept interfaces, return structs** — Provider interfaces are consumed; concrete types are returned.
- **Internal packages** — All non-CLI code lives under `internal/`. The `internal/api`, `internal/config`, and `internal/providers` packages are not meant for external import.

### Error Handling

- Return errors from providers and handlers; never call `os.Exit` or `log.Fatal` outside `main.go`.
- Wrap errors with context: `fmt.Errorf("context: %w", err)`.
- The RPC handler writes JSON error responses; the Redfish handler writes OData-compliant error responses.

### Naming

- Packages: single word, lowercase (`jetkvm`, `unifi`, `api`, `config`)
- Provider struct: `Provider` (each in its own package)
- Factory function: `newProvider` (unexported, registered in `init()`)
- Capability constants: `Cap` prefix (`CapPowerControl`, `CapVirtualMedia`, etc.)

### Logging

- Use `log/slog` structured logging everywhere.
- Logger is created in `main.go` and passed through or available via `slog.Default()`.
- JSON handler for production; log level configurable via `log_level` config.
- All log messages should include contextual attributes (provider, host, device, method).

### Configuration

- Viper for config file + env var merging; Cobra for CLI flags.
- Env prefix: `JETKVM_API_`
- Config file: YAML format, searched at `./jetkvm-api.yaml` and `$HOME/jetkvm-api.yaml`
- Device config is a list of `DeviceConfig` structs, each with MAC (required), name (optional), and providers list.
- Validation is explicit in `validateConfig()` — check port range, non-empty MAC, provider type present.

## Testing Patterns

### Table-Driven Tests

All test files follow the standard Go table-driven pattern:

```go
func TestFoo(t *testing.T) {
    tests := []struct {
        name     string
        input    X
        expected Y
    }{...}
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            // ...
        })
    }
}
```

### Mock Providers for API Tests

Tests in `internal/api/` use mock drivers that implement all capability interfaces:
- `mockTestDriver` in `service_test.go` implements Provider, PowerController, VirtualMediaController, BMCInfoProvider, BootDeviceController
- Tests create a `DeviceManager` with mock devices, then exercise the handlers via `httptest`

### Test Files

| File | Tests |
|---|---|
| `cmd/nana/main_test.go` | Response writer |
| `internal/api/service_test.go` | All RPC methods, device resolution, error cases |
| `internal/api/redfish_test.go` | Redfish endpoints, power mapping, reset mapping |
| `internal/config/config_test.go` | Config loading, validation |
| `internal/providers/providers_test.go` | Capabilities, device manager, MAC normalization, registry |
| `internal/providers/jetkvm/client/jetkvm_test.go` | JetKVM client |
| `internal/providers/unifi/unifi_test.go` | UniFi provider, PoE parsing |

### Running Tests

```bash
make test             # Full test suite with race detector + coverage
go test ./...         # Quick run
go test -run TestName ./internal/api/   # Specific test
```

## Build System

| Target | Command |
|---|---|
| Build | `make build` → `dist/jetkvm-api` |
| Cross-compile | `make cross-compile` → linux/amd64 + linux/arm64 |
| Test | `make test` → race + coverage |
| Lint | `make lint` → golangci-lint |
| Clean | `make clean` |
| Run | `make run` |
| Module tidy | `make mod` |

Docker: multi-stage build from `Dockerfile`, outputs a scratch-based image.

## JetKVM Provider Internals

### WebRTC Connection Lifecycle

1. HTTP POST `/auth/login-local` with password → session cookie
2. WebSocket dial to `ws://{host}/webrtc/signaling/client`
3. Read device metadata message from WebSocket
4. Create WebRTC peer connection + data channel "rpc"
5. Create SDP offer → set local description → wait for ICE gathering
6. Send base64-encoded SDP offer via WebSocket
7. Read SDP answer from WebSocket → set remote description
8. Exchange ICE candidates via WebSocket
9. Data channel opens → ready for JSON-RPC calls

The `Client.Connect()` call is idempotent — calling it when already connected is a no-op. `Call()` auto-reconnects if the data channel is nil.

### Power State Flow

```
SetPowerState("on")
  → GetActiveExtension() → "atx-power" or "dc-power"
  → SetATXPowerAction("power-on") or SetDCPowerState(true)
  → waitForPowerState(PowerOn) — polls until state matches
  → sendWakeOnLan() — sends WoL magic packets if configured
  → drainQueue("on") — executes boot macros if queued
```

### Boot Device Macros

`SetBootDevice()` does NOT immediately send keystrokes. It **enqueues** a task that executes after the next `SetPowerState("on")` completes. The flow:

1. `SetBootDevice("pxe", ...)` → enqueue keyboard macro to "on" queue
2. `SetPowerState("on")` → power on device
3. `waitForDeviceReady()` → poll video state + USB state until ready
4. `drainQueue("on")` → execute queued keyboard macro
5. Macro sends HID keyboard reports via `keyboardReport` RPC

### Keyboard Macro System

- Maps human-readable key names to USB HID key codes (`hidKeyMap`)
- Maps modifier names to HID bitmask values (`hidModifierMap`)
- Each step: press keys → immediately release → wait delay
- Max 6 simultaneous keys per report (`hidKeyBufferSize`)

## UniFi Provider Internals

### Uplink Discovery

Given a device MAC:
1. `GetClientInfo(site, mac)` → returns `uplinkMAC` and `swPort`
2. `GetDeviceByMAC(site, uplinkMAC)` → returns switch IP
3. SSH to switch IP → execute `swctrl poe` commands

Uplink info is cached; invalidated on SSH error and re-discovered on retry.

### SSH Key Derivation

The UniFi API key is used to deterministically derive an Ed25519 SSH key:
1. Concatenate API key + static salt `"unifi-swctrl-ssh-seed-v1"`
2. SHA-256 hash → 32-byte seed
3. `ed25519.NewKeyFromSeed(seed)` → deterministic keypair

The public key is auto-provisioned into the UniFi controller's management SSH keys via the admin API.

### SSH Connection Pool

`sshConnectionPool` maintains persistent SSH connections keyed by `host:port`. Connections are shared across providers targeting the same switch. On error, the connection is removed and a new one is dialed.

## Config File Structure

```yaml
# Server
port: 5000
address: "0.0.0.0"
log_level: "info"
webrtc_timeout: 30
maxprocs_enable: true
memlimit_enable: true
memlimit_ratio: 0.9

# Devices (list)
devices:
  - name: "optional-name"     # Becomes Redfish system ID
    mac: "AA:BB:CC:DD:EE:FF"  # Required, canonical identifier
    providers:
      - type: "jetkvm"        # Required
        host: "192.168.1.100" # Provider-specific
        password: ""
        boot:                  # Optional boot macros
          - device: "pxe"
            delay: 2s
            steps:
              - keys: ["f12"]
                delay: 2s
      - type: "unifi"         # Second provider (capabilities merged)
        api_key: "..."
        site: "default"
```

## Redfish ID Mapping

- If device has `name` → Redfish system ID = name (e.g., `server-01`)
- If device has no name → Redfish system ID = MAC with colons → dashes, uppercase (e.g., `AA-BB-CC-DD-EE-FF`)
- Lookup tries name first (case-insensitive), then MAC (any format, normalized)

## API Method → Provider Mapping

| RPC Method | Required Interface | Fallback |
|---|---|---|
| `getPowerState` | `PowerController` | Error: not supported |
| `setPowerState` | `PowerController` | Error: not supported |
| `setVirtualMedia` | `VirtualMediaController` | Error: not supported |
| `setBootDevice` | `BootDeviceController` | Acknowledged with message |
| `getVersion` | `BMCInfoProvider` | Error: not supported |
| `mountMedia` | `VirtualMediaController` | Error: not supported |
| `unmountMedia` | `VirtualMediaController` | Error: not supported |
| `getMediaState` | `VirtualMediaController` | Error: not supported |
| `ping` | None | Always returns "pong" |

## Dependencies

| Dependency | Purpose |
|---|---|
| `spf13/cobra` | CLI command framework |
| `spf13/viper` | Config file / env / flag merging |
| `pion/webrtc/v4` | WebRTC for JetKVM communication |
| `coder/websocket` | WebSocket for JetKVM signaling |
| `ubiquiti-community/go-unifi` | UniFi controller API client |
| `x/crypto/ssh` | SSH connections to UniFi switches |
| `automaxprocs` | Auto-set GOMAXPROCS in containers |
| `automemlimit` | Auto-set GOMEMLIMIT in containers |

## Common Tasks

### Adding a new RPC method
1. Add the method constant to `internal/api/payload.go`
2. Add params struct if needed
3. Add handler case in `rpcService.RpcHandler()` switch in `internal/api/service.go`
4. Add corresponding Redfish endpoint if applicable in `internal/api/redfish.go`
5. Write tests in the corresponding `_test.go` files

### Adding a new capability
1. Add `Capability` constant in `internal/providers/providers.go`
2. Define the interface in `internal/providers/providers.go`
3. Add accessor method on `ManagedDevice` in `internal/providers/registry.go`
4. Implement in relevant providers
5. Wire up in API handlers

### Adding a new config field
1. Add field to relevant struct in `internal/config/config.go` with `mapstructure` and `yaml` tags
2. Add CLI flag binding in `InitFlags()` if needed
3. Add defaults in `InitConfig()` via `viper.SetDefault()`
4. Add validation in `validateConfig()` if needed
5. Map to provider config in `buildDevices()` in `cmd/nana/main.go`
