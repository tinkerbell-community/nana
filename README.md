# JetKVM Management API

[![Go Reference](https://pkg.go.dev/badge/github.com/tinkerbell-community/nana.svg)](https://pkg.go.dev/github.com/tinkerbell-community/nana)
[![go.mod](https://img.shields.io/github/go-mod/go-version/jetkvm/cloud-api)](go.mod)

A BMC-compatible management API server for local [JetKVM](https://github.com/jetkvm/kvm) devices, providing power management, virtual media control, and device administration via JSON-RPC over HTTP.

## Overview

This server provides BMC-style management capabilities for JetKVM devices through their local WebRTC-based JSON-RPC interface. It's designed to integrate with Tinkerbell's hardware provisioning system and follows bmclib's RPC provider patterns.

## Key Features

- **BMC Compatible**: Standard `getPowerState`/`setPowerState`/`setVirtualMedia` methods for Tinkerbell/bmclib integration
- **WebRTC Backend**: Communicates with JetKVM devices using their native WebRTC data channel protocol
- **Multi-Device**: Manage multiple JetKVM devices from a single API instance via `X-Device` header routing
- **Connection Pooling**: Persistent WebRTC connections to devices are pooled and reused
- **ATX & DC Power**: Full support for ATX power actions (on/off/cycle/reset) and DC power control
- **Virtual Media**: Mount ISOs and images from HTTP URLs onto managed machines
- **JetKVM Extensions**: Access JetKVM-specific features (EDID, WOL, jiggler, video state, USB)

## Architecture

```
┌─────────────────┐       ┌─────────────────┐       ┌──────────────┐
│  Tinkerbell /   │ HTTP  │   JetKVM        │ WebRTC│   JetKVM     │
│  bmclib client  │──────▶│   Management    │──────▶│   Device     │
│                 │       │   API           │       │   (local)    │
└─────────────────┘       └─────────────────┘       └──────────────┘
                            │ X-Device: IP
                            │ JSON-RPC body
```

### Header-Based Device Routing

```
POST /
Headers:
  X-Device: 192.168.1.100
  X-Device-Password: optional-password
Body:
  {"method": "getPowerState", "id": 1}
```

The server translates BMC RPC calls into JetKVM JSON-RPC commands sent over WebRTC:

| BMC Method | JetKVM RPC | Description |
|---|---|---|
| `getPowerState` | `getATXState` / `getDCPowerState` | Get machine power state |
| `setPowerState(on)` | `setATXPowerAction("power-on")` | Power on the machine |
| `setPowerState(off)` | `setATXPowerAction("power-off")` | Power off the machine |
| `setPowerState(cycle)` | `setATXPowerAction("power-cycle")` | Power cycle the machine |
| `setPowerState(reset)` | `setATXPowerAction("reset")` | Reset the machine |
| `setVirtualMedia` | `mountWithHTTP` / `unmountImage` | Mount/unmount ISO images |
| `setBootDevice` | _(acknowledged)_ | Not directly supported; use virtual media |
| `ping` | _(local)_ | Health check |

### JetKVM-Specific Methods

| Method | Description |
|---|---|
| `getDeviceInfo` | Get device ID, auth mode |
| `getVersion` | Get firmware version |
| `tryUpdate` | Trigger OTA update |
| `getVideoState` | Get video capture state |
| `getUSBState` | Get USB emulation state |
| `mountMedia` | Mount image with URL and mode |
| `unmountMedia` | Unmount current media |
| `getMediaState` | Get virtual media state |
| `sendWOL` | Send Wake-on-LAN packet |
| `setJiggler` | Enable/disable mouse jiggler |
| `getEDID` / `setEDID` | Get/set EDID |
| `getATXState` | Get ATX power LEDs |
| `setATXPower` | Send ATX power action |
| `getDCPowerState` | Get DC power state |
| `setDCPowerState` | Set DC power state |

## Installation

```bash
go build -o jetkvm-api ./cmd/nana
```

## Configuration

Configuration is handled through command line flags, environment variables, or a YAML config file.

### Environment Variables

All environment variables are prefixed with `JETKVM_API_`:

| Environment Variable | Default | Description |
|---|---|---|
| `JETKVM_API_PORT` | `5000` | Port to listen on |
| `JETKVM_API_ADDRESS` | `0.0.0.0` | Address to listen on |
| `JETKVM_API_DEVICE_HOST` | _(optional)_ | Default JetKVM device IP/hostname |
| `JETKVM_API_DEVICE_PASSWORD` | _(optional)_ | Default device password |
| `JETKVM_API_WEBRTC_TIMEOUT` | `30` | WebRTC connection timeout (seconds) |

### Command Line Flags

| Flag | Default | Description |
|---|---|---|
| `--port` | `5000` | Port to listen on |
| `--address` | `0.0.0.0` | Address to listen on |
| `--device-host` | _(optional)_ | Default JetKVM device IP/hostname |
| `--device-password` | _(optional)_ | Default device password |
| `--webrtc-timeout` | `30` | WebRTC connection timeout (seconds) |
| `--config` | | Config file path (optional) |
| `--help` | | Show help message |

### Configuration Examples

**Using environment variables:**
```bash
export JETKVM_API_DEVICE_HOST="192.168.1.100"
export JETKVM_API_PORT="8080"
./jetkvm-api
```

**Using command line flags:**
```bash
./jetkvm-api \
  --device-host="192.168.1.100" \
  --port=8080
```

**Using config file (jetkvm-api.yaml):**
```yaml
port: 5000
address: "0.0.0.0"
device_host: "192.168.1.100"
device_password: ""
webrtc_timeout: 30
```

Then run:
```bash
./jetkvm-api --config=jetkvm-api.yaml
```

## Usage

### Start the Server

```bash
# With default device
./jetkvm-api --device-host="192.168.1.100"

# Without default device (X-Device header required per request)
./jetkvm-api

# Show help
./jetkvm-api --help
```

### BMC-Compatible Requests

```bash
# Get power status
curl -X POST http://localhost:5000/ \
  -H "X-Device: 192.168.1.100" \
  -H "Content-Type: application/json" \
  -d '{"method": "getPowerState", "id": 1}'

# Response:
# {"id":1,"result":"on"}

# Power on
curl -X POST http://localhost:5000/ \
  -H "X-Device: 192.168.1.100" \
  -H "Content-Type: application/json" \
  -d '{"method": "setPowerState", "params": {"state": "on"}, "id": 2}'

# Response:
# {"id":2,"result":"ok"}

# Power cycle
curl -X POST http://localhost:5000/ \
  -H "X-Device: 192.168.1.100" \
  -H "Content-Type: application/json" \
  -d '{"method": "setPowerState", "params": {"state": "cycle"}, "id": 3}'

# Mount ISO
curl -X POST http://localhost:5000/ \
  -H "X-Device: 192.168.1.100" \
  -H "Content-Type: application/json" \
  -d '{"method": "setVirtualMedia", "params": {"mediaUrl": "http://example.com/boot.iso", "kind": "cdrom"}, "id": 4}'

# Health check
curl -X POST http://localhost:5000/ \
  -H "Content-Type: application/json" \
  -d '{"method": "ping", "id": 5}'

# Response:
# {"id":5,"result":"pong"}
```

### JetKVM-Specific Requests

```bash
# Get device info
curl -X POST http://localhost:5000/ \
  -H "X-Device: 192.168.1.100" \
  -H "Content-Type: application/json" \
  -d '{"method": "getDeviceInfo", "id": 1}'

# Get firmware version
curl -X POST http://localhost:5000/ \
  -H "X-Device: 192.168.1.100" \
  -H "Content-Type: application/json" \
  -d '{"method": "getVersion", "id": 2}'

# Get video state
curl -X POST http://localhost:5000/ \
  -H "X-Device: 192.168.1.100" \
  -H "Content-Type: application/json" \
  -d '{"method": "getVideoState", "id": 3}'

# Mount media directly
curl -X POST http://localhost:5000/ \
  -H "X-Device: 192.168.1.100" \
  -H "Content-Type: application/json" \
  -d '{"method": "mountMedia", "params": {"url": "http://example.com/boot.iso", "mode": "cdrom"}, "id": 4}'

# Send Wake-on-LAN
curl -X POST http://localhost:5000/ \
  -H "X-Device: 192.168.1.100" \
  -H "Content-Type: application/json" \
  -d '{"method": "sendWOL", "params": {"macAddress": "aa:bb:cc:dd:ee:ff"}, "id": 5}'

# Set ATX power action
curl -X POST http://localhost:5000/ \
  -H "X-Device: 192.168.1.100" \
  -H "Content-Type: application/json" \
  -d '{"method": "setATXPower", "params": {"action": "power-cycle"}, "id": 6}'
```

## Tinkerbell Integration

This server is designed to work with Tinkerbell's BMC provider system:

```yaml
apiVersion: tinkerbell.org/v1alpha1
kind: Hardware
spec:
  bmcRef:
    apiVersion: bmc.tinkerbell.org/v1alpha1
    kind: Machine
    name: jetkvm-server-1
---
apiVersion: bmc.tinkerbell.org/v1alpha1
kind: Machine
metadata:
  name: jetkvm-server-1
spec:
  connection:
    host: jetkvm-api-server:5000
    providerOptions:
      rpc:
        consumerURL: http://jetkvm-api-server:5000
        request:
          staticHeaders:
            X-Device: ["192.168.1.100"]
```

## bmclib Integration

Compatible with bmclib's RPC provider:

```go
import "github.com/bmc-toolbox/bmclib/v2"

client := bmclib.NewClient("jetkvm-api-server", "", "",
    bmclib.WithRPCOpt(rpc.Provider{
        ConsumerURL: "http://jetkvm-api-server:5000",
        Opts: rpc.Opts{
            Request: rpc.RequestOpts{
                StaticHeaders: http.Header{
                    "X-Device": []string{"192.168.1.100"},
                },
            },
        },
    }),
)

// Power on the machine managed by the JetKVM at 192.168.1.100
err := client.SetPowerState("on")
```

## How It Works

1. **Request arrives** at the management API with an `X-Device` header identifying the target JetKVM device
2. **Connection pooling** checks for an existing WebRTC session to the device, or establishes a new one:
   - Authenticates with the JetKVM device via HTTP (`POST /auth/login-local`)
   - Connects to the WebSocket signaling endpoint (`/webrtc/signaling/client`)
   - Exchanges WebRTC SDP offer/answer to establish a peer connection
   - Opens a data channel for JSON-RPC communication
3. **JSON-RPC command** is sent over the WebRTC data channel to the JetKVM device
4. **Response** is received on the data channel and returned to the caller

## Development

### Running Tests

```bash
go test ./...
```

### Building

```bash
go build -o jetkvm-api ./cmd/nana
```

### Cross-Compiling

```bash
make cross-compile
```

## References

- [JetKVM upstream firmware](https://github.com/jetkvm/kvm) - The KVM device firmware with JSON-RPC handler definitions
- [bmclib](https://github.com/bmc-toolbox/bmclib) - BMC library with RPC provider support
- [Tinkerbell](https://tinkerbell.org/) - Bare metal provisioning engine

## License

See [LICENSE](../LICENSE) for details.
