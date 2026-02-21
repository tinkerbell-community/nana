package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"time"

	"github.com/jetkvm/cloud-api/mgmt-api/pkg/models"
	"github.com/jetkvm/cloud-api/mgmt-api/pkg/providers"
)

// RpcService defines the interface for the RPC handler service.
type RpcService interface {
	RpcHandler(w http.ResponseWriter, r *http.Request)
}

type rpcService struct {
	dm         *providers.DeviceManager
	rpcTimeout time.Duration
	logger     *slog.Logger
}

func writeError(w http.ResponseWriter, statusCode int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	err := json.NewEncoder(w).Encode(map[string]string{"error": message})
	if err != nil {
		log.Printf("error writing error response: %v", err)
	}
}

func (s *rpcService) getDevice(r *http.Request) (*providers.ManagedDevice, error) {
	// First try X-Device header.
	dev, err := models.ResolveDevice(r, s.dm)
	if err != nil {
		return nil, err
	}
	return dev, nil
}

func (s *rpcService) getDeviceByHost(host string) (*providers.ManagedDevice, error) {
	if host == "" {
		return nil, fmt.Errorf("device identifier is required")
	}
	return models.ResolveDeviceByID(host, s.dm)
}

func (s *rpcService) getPowerState(ctx context.Context, dev *providers.ManagedDevice) (string, error) {
	pc := dev.PowerController()
	if pc == nil {
		return "", fmt.Errorf("device does not support power control")
	}
	return pc.GetPowerState(ctx)
}

func (s *rpcService) setPowerState(ctx context.Context, dev *providers.ManagedDevice, state string) error {
	pc := dev.PowerController()
	if pc == nil {
		return fmt.Errorf("device does not support power control")
	}
	switch state {
	case "on", "off", "soft", "cycle", "reset":
		if state == "soft" {
			state = "off"
		}
		return pc.SetPowerState(ctx, state)
	default:
		return fmt.Errorf("invalid power state: %s", state)
	}
}

// RpcHandler handles BMC-compatible JSON-RPC requests.
//
// The handler supports standard BMC methods (getPowerState, setPowerState,
// setBootDevice, setVirtualMedia, ping) for Tinkerbell/bmclib compatibility.
//
// Device identification is done via the X-Device header (device name or MAC),
// or the "host" field in the JSON-RPC request body.
func (s *rpcService) RpcHandler(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), s.rpcTimeout)
	defer cancel()

	req := RequestPayload{}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid JSON payload")
		return
	}

	rp := ResponsePayload{
		ID:   req.ID,
		Host: req.Host,
	}

	// Methods that don't require a device connection.
	if req.Method == PingMethod {
		rp.Result = "pong"
		writeResponse(w, rp)
		return
	}

	// Resolve device: try X-Device header first, then host field from request.
	var dev *providers.ManagedDevice
	var err error

	dev, err = s.getDevice(r)
	if err != nil && req.Host != "" {
		// Fall back to host field in request body.
		dev, err = s.getDeviceByHost(req.Host)
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	s.logger.Info("handling RPC request",
		slog.String("method", string(req.Method)),
		slog.String("device", dev.ID()),
		slog.Int64("id", req.ID),
	)

	switch req.Method {
	case PowerGetMethod:
		state, err := s.getPowerState(ctx, dev)
		if err != nil {
			rp.Error = &ResponseError{
				Code:    http.StatusInternalServerError,
				Message: fmt.Sprintf("error getting power state: %v", err),
			}
		} else {
			rp.Result = state
		}

	case PowerSetMethod:
		paramsJSON, err := json.Marshal(req.Params)
		if err != nil {
			rp.Error = &ResponseError{
				Code:    http.StatusBadRequest,
				Message: fmt.Sprintf("error marshaling params: %v", err),
			}
			break
		}

		var p PowerSetParams
		if err := json.Unmarshal(paramsJSON, &p); err != nil {
			rp.Error = &ResponseError{
				Code:    http.StatusBadRequest,
				Message: fmt.Sprintf("error parsing PowerSetParams: %v", err),
			}
			break
		}

		if err := s.setPowerState(ctx, dev, p.State); err != nil {
			rp.Error = &ResponseError{
				Code:    http.StatusInternalServerError,
				Message: fmt.Sprintf("error setting power state: %v", err),
			}
		} else {
			rp.Result = "ok"
		}

	case VirtualMediaMethod:
		paramsJSON, err := json.Marshal(req.Params)
		if err != nil {
			rp.Error = &ResponseError{
				Code:    http.StatusBadRequest,
				Message: fmt.Sprintf("error marshaling params: %v", err),
			}
			break
		}

		var p VirtualMediaParams
		if err := json.Unmarshal(paramsJSON, &p); err != nil {
			rp.Error = &ResponseError{
				Code:    http.StatusBadRequest,
				Message: fmt.Sprintf("error parsing VirtualMediaParams: %v", err),
			}
			break
		}

		vmc := dev.VirtualMediaController()
		if vmc == nil {
			rp.Error = &ResponseError{
				Code:    http.StatusBadRequest,
				Message: "device does not support virtual media",
			}
			break
		}

		if p.MediaURL == "" {
			if err := vmc.UnmountMedia(ctx); err != nil {
				rp.Error = &ResponseError{
					Code:    http.StatusInternalServerError,
					Message: fmt.Sprintf("error unmounting media: %v", err),
				}
			} else {
				rp.Result = "ok"
			}
		} else {
			kind := p.Kind
			if kind == "" {
				kind = "cdrom"
			}
			if err := vmc.MountMedia(ctx, p.MediaURL, kind); err != nil {
				rp.Error = &ResponseError{
					Code:    http.StatusInternalServerError,
					Message: fmt.Sprintf("error mounting media: %v", err),
				}
			} else {
				rp.Result = "ok"
			}
		}

	case BootDeviceMethod:
		paramsJSON, err := json.Marshal(req.Params)
		if err != nil {
			rp.Error = &ResponseError{
				Code:    http.StatusBadRequest,
				Message: fmt.Sprintf("error marshaling params: %v", err),
			}
			break
		}

		var p BootDeviceParams
		if err := json.Unmarshal(paramsJSON, &p); err != nil {
			rp.Error = &ResponseError{
				Code:    http.StatusBadRequest,
				Message: fmt.Sprintf("error parsing BootDeviceParams: %v", err),
			}
			break
		}

		bdc := dev.BootDeviceController()
		if bdc != nil {
			if err := bdc.SetBootDevice(ctx, p.Device, p.Persistent, p.EFIBoot); err != nil {
				rp.Error = &ResponseError{
					Code:    http.StatusInternalServerError,
					Message: fmt.Sprintf("error setting boot device: %v", err),
				}
			} else {
				rp.Result = "ok"
			}
		} else {
			// Acknowledge without action (JetKVM doesn't directly support boot device).
			rp.Result = map[string]any{
				"acknowledged": true,
				"device":       p.Device,
				"persistent":   p.Persistent,
				"efiBoot":      p.EFIBoot,
				"message":      "boot device setting not supported by configured providers; use virtual media for PXE boot",
			}
		}

	case GetVersionMethod:
		bip := dev.BMCInfoProvider()
		if bip == nil {
			rp.Error = &ResponseError{
				Code:    http.StatusBadRequest,
				Message: "device does not support BMC info",
			}
		} else {
			version, err := bip.GetBMCVersion(ctx)
			if err != nil {
				rp.Error = &ResponseError{
					Code:    http.StatusInternalServerError,
					Message: fmt.Sprintf("error getting version: %v", err),
				}
			} else {
				rp.Result = version
			}
		}

	case GetMediaStateMethod:
		vmc := dev.VirtualMediaController()
		if vmc == nil {
			rp.Error = &ResponseError{
				Code:    http.StatusBadRequest,
				Message: "device does not support virtual media",
			}
		} else {
			state, err := vmc.GetMediaState(ctx)
			if err != nil {
				rp.Error = &ResponseError{
					Code:    http.StatusInternalServerError,
					Message: fmt.Sprintf("error getting media state: %v", err),
				}
			} else {
				rp.Result = state
			}
		}

	case MountMediaMethod:
		paramsJSON, err := json.Marshal(req.Params)
		if err != nil {
			rp.Error = &ResponseError{
				Code:    http.StatusBadRequest,
				Message: fmt.Sprintf("error marshaling params: %v", err),
			}
			break
		}

		var p MountMediaParams
		if err := json.Unmarshal(paramsJSON, &p); err != nil {
			rp.Error = &ResponseError{
				Code:    http.StatusBadRequest,
				Message: fmt.Sprintf("error parsing MountMediaParams: %v", err),
			}
			break
		}

		vmc := dev.VirtualMediaController()
		if vmc == nil {
			rp.Error = &ResponseError{
				Code:    http.StatusBadRequest,
				Message: "device does not support virtual media",
			}
			break
		}

		if err := vmc.MountMedia(ctx, p.URL, p.Mode); err != nil {
			rp.Error = &ResponseError{
				Code:    http.StatusInternalServerError,
				Message: fmt.Sprintf("error mounting media: %v", err),
			}
		} else {
			rp.Result = "ok"
		}

	case UnmountMediaMethod:
		vmc := dev.VirtualMediaController()
		if vmc == nil {
			rp.Error = &ResponseError{
				Code:    http.StatusBadRequest,
				Message: "device does not support virtual media",
			}
		} else {
			if err := vmc.UnmountMedia(ctx); err != nil {
				rp.Error = &ResponseError{
					Code:    http.StatusInternalServerError,
					Message: fmt.Sprintf("error unmounting media: %v", err),
				}
			} else {
				rp.Result = "ok"
			}
		}

	default:
		rp.Error = &ResponseError{
			Code:    http.StatusNotFound,
			Message: fmt.Sprintf("unknown method: %s", req.Method),
		}
	}

	writeResponse(w, rp)
}

func writeResponse(w http.ResponseWriter, rp ResponsePayload) {
	w.Header().Set("Content-Type", "application/json")
	if rp.Error != nil {
		w.WriteHeader(rp.Error.Code)
	} else {
		w.WriteHeader(http.StatusOK)
	}

	if err := json.NewEncoder(w).Encode(rp); err != nil {
		log.Printf("error encoding response: %v", err)
	}
}

// NewBMCService creates a new RPC service for BMC-compatible management.
func NewBMCService(dm *providers.DeviceManager, rpcTimeout time.Duration, logger *slog.Logger) RpcService {
	if rpcTimeout == 0 {
		rpcTimeout = 30 * time.Second
	}

	return &rpcService{
		dm:         dm,
		rpcTimeout: rpcTimeout,
		logger:     logger,
	}
}
