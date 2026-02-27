package main

import (
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/KimMachineGun/automemlimit/memlimit"
	"github.com/spf13/cobra"
	"github.com/tinkerbell-community/nana/internal/api"
	"github.com/tinkerbell-community/nana/internal/config"
	"github.com/tinkerbell-community/nana/internal/providers"
	_ "github.com/tinkerbell-community/nana/internal/providers/jetkvm" // register jetkvm provider
	_ "github.com/tinkerbell-community/nana/internal/providers/unifi"  // register unifi provider
	"go.uber.org/automaxprocs/maxprocs"
)

func loggingMiddleware(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			wrapped := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}

			logger.Info("incoming request",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.String("remote_addr", r.RemoteAddr),
				slog.String("user_agent", r.UserAgent()),
				slog.String("device", r.Header.Get("X-Device")),
			)

			next.ServeHTTP(wrapped, r)

			duration := time.Since(start)
			logger.Info("request completed",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", wrapped.statusCode),
				slog.Duration("duration", duration),
			)
		})
	}
}

type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

// ServerState holds the reloadable server components.
type ServerState struct {
	dm         *providers.DeviceManager
	rpcSvc     api.RpcService
	redfishSvc api.RedfishService
	mu         sync.RWMutex
}

// Reload rebuilds the DeviceManager and services from the given configuration.
func (s *ServerState) Reload(newCfg *config.Config, logger *slog.Logger) {
	s.mu.Lock()
	defer s.mu.Unlock()

	logger.Info("Reloading server configuration...")

	newDM, err := buildDeviceManager(newCfg, logger)
	if err != nil {
		logger.Error("Failed to build new device manager", "error", err)
		return
	}

	rpcTimeout := time.Duration(newCfg.WebRTCTimeout) * time.Second
	s.rpcSvc = api.NewBMCService(newDM, rpcTimeout, logger)
	s.redfishSvc = api.NewRedfishService(newDM)
	s.dm = newDM

	logger.Info("Configuration reload complete", "devices", len(newCfg.Devices))
}

// RpcHandler delegates to the current RPC service instance.
func (s *ServerState) RpcHandler(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	svc := s.rpcSvc
	s.mu.RUnlock()
	svc.RpcHandler(w, r)
}

// ServiceRoot delegates to the current Redfish service.
func (s *ServerState) ServiceRoot(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	svc := s.redfishSvc
	s.mu.RUnlock()
	svc.ServiceRoot(w, r)
}

// Systems delegates to the current Redfish service.
func (s *ServerState) Systems(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	svc := s.redfishSvc
	s.mu.RUnlock()
	svc.Systems(w, r)
}

// System delegates to the current Redfish service.
func (s *ServerState) System(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	svc := s.redfishSvc
	s.mu.RUnlock()
	svc.System(w, r)
}

// SystemReset delegates to the current Redfish service.
func (s *ServerState) SystemReset(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	svc := s.redfishSvc
	s.mu.RUnlock()
	svc.SystemReset(w, r)
}

// VirtualMediaCollection delegates to the current Redfish service.
func (s *ServerState) VirtualMediaCollection(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	svc := s.redfishSvc
	s.mu.RUnlock()
	svc.VirtualMediaCollection(w, r)
}

// VirtualMedia delegates to the current Redfish service.
func (s *ServerState) VirtualMedia(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	svc := s.redfishSvc
	s.mu.RUnlock()
	svc.VirtualMedia(w, r)
}

// VirtualMediaInsert delegates to the current Redfish service.
func (s *ServerState) VirtualMediaInsert(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	svc := s.redfishSvc
	s.mu.RUnlock()
	svc.VirtualMediaInsert(w, r)
}

// VirtualMediaEject delegates to the current Redfish service.
func (s *ServerState) VirtualMediaEject(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	svc := s.redfishSvc
	s.mu.RUnlock()
	svc.VirtualMediaEject(w, r)
}

// Managers delegates to the current Redfish service.
func (s *ServerState) Managers(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	svc := s.redfishSvc
	s.mu.RUnlock()
	svc.Managers(w, r)
}

// Manager delegates to the current Redfish service.
func (s *ServerState) Manager(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	svc := s.redfishSvc
	s.mu.RUnlock()
	svc.Manager(w, r)
}

func buildDeviceManager(cfg *config.Config, logger *slog.Logger) (*providers.DeviceManager, error) {
	dm := providers.NewDeviceManager()

	return dm, buildDevices(cfg, dm, logger)
}

func buildDevices(cfg *config.Config, dm *providers.DeviceManager, logger *slog.Logger) error {
	for i, devCfg := range cfg.Devices {
		var p []providers.Provider
		for j, prvCfg := range devCfg.Providers {
			if defaults, ok := cfg.DefaultProvider(prvCfg.Type); ok {
				prvCfg = prvCfg.WithDefaults(defaults)
			}
			prvMap := prvCfg.ToMap()
			prvMap["mac"] = devCfg.MAC

			prv, err := providers.Create(prvCfg.Type, prvMap)
			if err != nil {
				return fmt.Errorf("device[%d].providers[%d] (%s): %w", i, j, prvCfg.Type, err)
			}
			p = append(p, prv)

			logger.Info("registered provider",
				slog.String("device", devCfg.MAC),
				slog.String("name", devCfg.Name),
				slog.String("provider", prvCfg.Type),
				slog.String("host", prvCfg.Host),
			)
		}

		dev := &providers.ManagedDevice{
			Name:      devCfg.Name,
			MAC:       devCfg.MAC,
			Providers: p,
		}
		dm.AddDevice(dev)

		caps := dev.MergedCapabilities()
		capNames := make([]string, len(caps))
		for k, c := range caps {
			capNames[k] = string(c)
		}
		logger.Info("device registered",
			slog.String("id", dev.ID()),
			slog.String("mac", dev.MAC),
			slog.Any("capabilities", capNames),
		)
	}

	return nil
}

var rootCmd = &cobra.Command{
	Use:   "nana",
	Short: "JetKVM Management API - BMC-compatible Redfish and RPC server",
	Long: `JetKVM Management API provides BMC-compatible Redfish and RPC endpoints
for managing devices through pluggable providers.

Configuration is provided via a YAML config file that defines managed devices,
each with a MAC address, optional name, and one or more providers.

Endpoints:
  POST /                              - bmclib-compatible JSON-RPC
  GET  /redfish/v1/                   - Redfish Service Root
  GET  /redfish/v1/Systems            - Computer System Collection
  GET  /redfish/v1/Systems/{id}       - Computer System
  POST /redfish/v1/Systems/{id}/Actions/ComputerSystem.Reset
  GET  /redfish/v1/Systems/{id}/VirtualMedia
  GET  /redfish/v1/Managers           - Manager Collection

Device Identification:
  RPC:     X-Device header (name or MAC) or "host" field in request body
  Redfish: System/Manager ID in URL path (name or MAC)

Example:
  nana --config=config.yaml`,
	Run: func(cmd *cobra.Command, args []string) {
		cfg, err := config.LoadConfig()
		if err != nil {
			log.Fatalf("Error loading configuration: %v", err)
		}

		var logLevel slog.Level
		if err := logLevel.UnmarshalText([]byte(cfg.LogLevel)); err != nil {
			log.Fatalf("invalid log_level %q: %v", cfg.LogLevel, err)
		}
		logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
			Level: logLevel,
		}))
		slog.SetDefault(logger)

		// Set Go runtime parameters before we get too far into initialization.
		if cfg.MaxprocsEnable {
			l := func(format string, a ...any) {
				logger.Info(
					fmt.Sprintf(strings.TrimPrefix(format, "maxprocs: "), a...),
					"component",
					"automaxprocs",
				)
			}
			if _, err := maxprocs.Set(maxprocs.Logger(l)); err != nil {
				logger.Warn(
					"Failed to set GOMAXPROCS automatically",
					"component",
					"automaxprocs",
					"err",
					err,
				)
			}
		}

		if cfg.MemlimitEnable {
			if _, err := memlimit.SetGoMemLimitWithOpts(
				memlimit.WithRatio(cfg.MemlimitRatio),
				memlimit.WithProvider(
					memlimit.ApplyFallback(
						memlimit.FromCgroup,
						memlimit.FromSystem,
					),
				),
				memlimit.WithLogger(slog.Default().With("component", "automemlimit")),
			); err != nil {
				logger.Warn(
					"Failed to set GOMEMLIMIT automatically",
					"component",
					"automemlimit",
					"err",
					err,
				)
			}
		}

		// Initialize reloadable server state.
		state := &ServerState{}
		state.Reload(cfg, logger)

		// Start watching the config file for changes.
		config.WatchConfig(logger, func(newCfg *config.Config) {
			state.Reload(newCfg, logger)
		})

		mux := http.NewServeMux()

		// RPC endpoint (bmclib-compatible).
		mux.HandleFunc("POST /", state.RpcHandler)

		// Redfish endpoints (delegates to current service via ServerState).
		api.RegisterRedfishRoutes(mux, state)

		// Health check endpoint.
		mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		})

		handler := loggingMiddleware(logger)(mux)

		fmt.Printf("JetKVM Management API is running on http://%s:%d\n", cfg.Address, cfg.Port)
		fmt.Printf("Registered %d device(s)\n", len(cfg.Devices))
		fmt.Printf("Available providers: %v\n", providers.Available())
		fmt.Printf("Endpoints:\n")
		fmt.Printf(
			"  POST /											- bmclib-compatible JSON-RPC\n",
		)
		fmt.Printf("  GET  /redfish/v1/           - Redfish Service Root\n")
		fmt.Printf("  GET  /redfish/v1/Systems    - Computer System Collection\n")
		fmt.Printf("  GET  /healthz               - Health check\n")

		logger.Info("starting server",
			slog.String("address", cfg.Address),
			slog.Int("port", cfg.Port),
			slog.Int("devices", len(cfg.Devices)),
		)

		err = http.ListenAndServe(fmt.Sprintf("%s:%d", cfg.Address, cfg.Port), handler)
		if err != nil {
			log.Fatalf("error starting server: %v", err)
		}
	},
}

func init() {
	cobra.OnInitialize(config.InitConfig)
	config.InitFlags(rootCmd)
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
