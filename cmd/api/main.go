package main

import (
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/jetkvm/cloud-api/mgmt-api/pkg/api"
	"github.com/jetkvm/cloud-api/mgmt-api/pkg/config"
	"github.com/jetkvm/cloud-api/mgmt-api/pkg/providers"
	_ "github.com/jetkvm/cloud-api/mgmt-api/pkg/providers/jetkvm" // register jetkvm provider
	_ "github.com/jetkvm/cloud-api/mgmt-api/pkg/providers/unifi"  // register unifi provider
	"github.com/spf13/cobra"
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

func buildDeviceManager(cfg *config.Config, logger *slog.Logger) (*providers.DeviceManager, error) {
	dm := providers.NewDeviceManager()

	for i, devCfg := range cfg.Devices {
		var p []providers.Provider
		for j, prvCfg := range devCfg.Providers {
			prvMap := map[string]any{
				"type": prvCfg.Type,
				"mac":  devCfg.MAC,
			}
			if prvCfg.Host != "" {
				prvMap["host"] = prvCfg.Host
			}
			if prvCfg.Password != "" {
				prvMap["password"] = prvCfg.Password
			}
			if prvCfg.SSHPort > 0 {
				prvMap["ssh_port"] = prvCfg.SSHPort
			}
			if prvCfg.SSHUsername != "" {
				prvMap["ssh_username"] = prvCfg.SSHUsername
			}
			if prvCfg.APIKey != "" {
				prvMap["api_key"] = prvCfg.APIKey
			}
			if prvCfg.Site != "" {
				prvMap["site"] = prvCfg.Site
			}
			// Provider-level ssh_key_path overrides global.
			sshKeyPath := prvCfg.SSHKeyPath
			if sshKeyPath == "" {
				sshKeyPath = cfg.SSHKeyPath
			}
			if sshKeyPath != "" {
				prvMap["ssh_key_path"] = sshKeyPath
			}

			prv, err := providers.Create(prvCfg.Type, prvMap)
			if err != nil {
				return nil, fmt.Errorf("device[%d].providers[%d] (%s): %w", i, j, prvCfg.Type, err)
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

	return dm, nil
}

var rootCmd = &cobra.Command{
	Use:   "jetkvm-api",
	Short: "JetKVM Management API - BMC-compatible Redfish and RPC server",
	Long: `JetKVM Management API provides BMC-compatible Redfish and RPC endpoints
for managing devices through pluggable providers.

Configuration is provided via a YAML config file that defines managed devices,
each with a MAC address, optional name, and one or more providers.

Endpoints:
  POST /rpc                           - bmclib-compatible JSON-RPC
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
  jetkvm-api --config=config.yaml`,
	Run: func(cmd *cobra.Command, args []string) {
		logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
			Level: slog.LevelInfo,
		}))
		slog.SetDefault(logger)

		cfg, err := config.LoadConfig()
		if err != nil {
			log.Fatalf("Error loading configuration: %v", err)
		}

		dm, err := buildDeviceManager(cfg, logger)
		if err != nil {
			log.Fatalf("Error building device manager: %v", err)
		}

		rpcTimeout := time.Duration(cfg.WebRTCTimeout) * time.Second
		rpcSvc := api.NewBMCService(dm, rpcTimeout, logger)
		redfishSvc := api.NewRedfishService(dm)

		mux := http.NewServeMux()

		// RPC endpoint (bmclib-compatible).
		mux.HandleFunc("POST /rpc", rpcSvc.RpcHandler)

		// Redfish endpoints.
		api.RegisterRedfishRoutes(mux, redfishSvc)

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
		fmt.Printf("  POST /rpc                  - bmclib-compatible JSON-RPC\n")
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
