# Configuration Reload & Refactoring Instructions

This document provides detailed instructions for implementing full configuration reload support for the `nana` project and refactoring the `internal/config` package to better leverage Go structs and `viper`.

## Objective

1.  **Full Config Reload:** Enable the application to detect changes to the configuration file at runtime and apply them without restarting the process.
2.  **Type-Safe Configuration:** Refactor `internal/config` to minimize manual `map[string]any` casting and use proper struct-based unmarshalling.

## References

*   **Kubernetes Controllers:** Use of `Informer` patterns and event handlers.
*   **Prometheus:** Configuration reloading via `SIGHUP` or HTTP API (we will focus on file watching here, which is more "cloud-native" for local config maps).
*   **CNCF/Viper:** Standard patterns for `WatchConfig` and `OnConfigChange`.

---

## Step 1: Refactor `internal/config`

The current implementation exposes a global `cfg` and requires manual casting in consumers. We will move to a **Thread-Safe Singleton** or **Dependency Injection** pattern with helper methods for map conversion.

### 1.1 Add `ToMap` Helpers
Instead of manually building maps in `main.go`, add methods to `ProviderConfig` in `internal/config/config.go` to handle the conversion. This keeps the logic encapsulated.

```go
// internal/config/config.go

import "github.com/mitchellh/mapstructure"

// ... inside ProviderConfig struct ...

// ToMap converts the typed ProviderConfig into a map[string]any
// suitable for the providers.Factory interface.
func (p *ProviderConfig) ToMap() (map[string]any, error) {
    var result map[string]any
    // use mapstructure to safely convert struct to map
    decoder, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
        TagName: "mapstructure",
        Result:  &result,
    })
    if err != nil {
        return nil, err
    }
    if err := decoder.Decode(p); err != nil {
        return nil, err
    }
    
    // Manual overrides or cleanups if necessary (e.g. handling nested structs if mapstructure doesn't flatten them as the provider expects)
    // For specific provider fields that might be generic in the struct but need specific keys:
    result["type"] = p.Type
    // ...
    return result, nil
}
```

*Note: If `mapstructure` decode into map is not behaving as expected (it usually goes Map -> Struct), a simple manual helper is often cleaner and faster than reflection for small structs.*

**Recommended Manual Helper (Performance & Clarity):**

```go
func (p *ProviderConfig) ToMap() map[string]any {
    m := map[string]any{
        "type":      p.Type,
        "host":      p.Host,
        "password":  p.Password,
        "api_key":   p.APIKey,
        "site":      p.Site,
    }
    // Handle complex nested types like Boot if necessary, 
    // or let the provider parse the raw structure if possible.
    if len(p.Boot) > 0 {
        // ... conversion logic for boot steps ...
        m["boot"] = p.Boot 
    }
    return m
}
```

### 1.2 Implement Thread-Safe Config Access
Replace the global `cfg` variable with a thread-safe manager or use `sync.RWMutex` around the global variable.

```go
var (
    cfgMu sync.RWMutex
    cfg   *Config
)

func GetConfig() *Config {
    cfgMu.RLock()
    defer cfgMu.RUnlock()
    return cfg
}

// UpdateConfig updates the global configuration in a thread-safe manner.
func UpdateConfig(newCfg *Config) {
    cfgMu.Lock()
    defer cfgMu.Unlock()
    cfg = newCfg
}
```

## Step 2: Implement Config Watching

Use `viper.WatchConfig()` to monitor the filesystem.

### 2.1 Add Watcher to `InitConfig`

Modify `internal/config/config.go`:

```go
import (
    "github.com/fsnotify/fsnotify"
    "log/slog"
)

// OnChangeCallback defines a function to be called when config updates.
type OnChangeCallback func(newConfig *Config)

func WatchConfig(logger *slog.Logger, onChange OnChangeCallback) {
    viper.OnConfigChange(func(e fsnotify.Event) {
        logger.Info("Config file changed", "name", e.Name)
        
        // Re-read and unmarshal
        var newCfg Config
        if err := viper.Unmarshal(&newCfg); err != nil {
            logger.Error("Failed to unmarshal new config", "error", err)
            return
        }
        
        if err := validateConfig(&newCfg); err != nil {
             logger.Error("Invalid new configuration, keeping old config", "error", err)
             return
        }

        // Update global state
        UpdateConfig(&newCfg)

        // Notify subscribers
        if onChange != nil {
            onChange(&newCfg)
        }
    })
    viper.WatchConfig()
}
```

## Step 3: Implement Reload Logic in `main.go`

Refactor `main.go` to handle the dynamic nature of the `DeviceManager`.

### 3.1 Extract Device Manager Reconstruction
Move the logic that builds the `DeviceManager` from `cfg` into a reloadable function.

```go
// cmd/nana/main.go

type ServerState struct {
    dm         *providers.DeviceManager
    rpcSvc     *api.BMCService
    redfishSvc *api.RedfishService
    mu         sync.RWMutex
}

func (s *ServerState) Reload(newCfg *config.Config, logger *slog.Logger) {
    s.mu.Lock()
    defer s.mu.Unlock()

    logger.Info("Reloading server configuration...")

    // 1. Build new DeviceManager
    newDM, err := buildDeviceManager(newCfg, logger)
    if err != nil {
        logger.Error("Failed to build new device manager", "error", err)
        return
    }

    // 2. Update Services
    // If services hold references to DM, they need to be updated.
    // Ideally, services should take a "DeviceManagerProvider" interface 
    // or we simply update the pointer they use if possible.
    // 
    // A simpler approach for this app: Re-instantiate services.
    rpcTimeout := time.Duration(newCfg.WebRTCTimeout) * time.Second
    
    // NOTE: If api.NewBMCService stores state, we might lose it. 
    // If it's stateless (other than config), this is fine.
    s.rpcSvc = api.NewBMCService(newDM, rpcTimeout, logger)
    s.redfishSvc = api.NewRedfishService(newDM)
    s.dm = newDM
    
    logger.Info("Configuration reload complete", "devices", len(newCfg.Devices))
}
```

### 3.2 Update HTTP Handlers to use Dynamic State
Since standard `http.Handler`s are registered once, your handlers need to look up the *current* service instance at request time, or the Services themselves need to be thread-safe wrappers that swap their internal implementation.

**Recommended Pattern: Delegate Handler**

```go
// In main.go

// Delegate to the current service instance
func (s *ServerState) RpcHandler(w http.ResponseWriter, r *http.Request) {
    s.mu.RLock()
    svc := s.rpcSvc
    s.mu.RUnlock()
    svc.RpcHandler(w, r)
}
```

### 3.3 Wire it up in `main`

```go
func main() {
    // ... init config ...
    
    state := &ServerState{}
    
    // Initial load
    cfg := config.GetConfig()
    state.Reload(cfg, logger)

    // Start watcher
    config.WatchConfig(logger, func(newCfg *config.Config) {
        state.Reload(newCfg, logger)
    })

    // ... setup HTTP server using state.RpcHandler ...
}
```

## Summary of Tasks

1.  **Refactor `internal/config`**:
    *   Protect `cfg` with `sync.RWMutex`.
    *   Add `WatchConfig` with `fsnotify`.
    *   Add `ToMap` methods to struct to clean up `main.go`.
2.  **Refactor `cmd/nana/main.go`**:
    *   Create a `ServerState` struct to hold reloadable components (`DeviceManager`, Services).
    *   Implement `Reload(cfg)` method.
    *   Create wrapper handlers that lock `ServerState` and call the current service.
    *   Register the watcher callback.

This approach ensures that `nana` behaves like a mature daemon (e.g., Prometheus, Nginx) where config changes are seamless.
