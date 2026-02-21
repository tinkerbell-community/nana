package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/tinkerbell-community/nana/pkg/providers"
)

// RedfishService provides Redfish v1 REST API endpoints.
type RedfishService interface {
	// ServiceRoot handles GET /redfish/v1/
	ServiceRoot(w http.ResponseWriter, r *http.Request)
	// Systems handles GET /redfish/v1/Systems
	Systems(w http.ResponseWriter, r *http.Request)
	// System handles GET /redfish/v1/Systems/{systemId}
	System(w http.ResponseWriter, r *http.Request)
	// SystemReset handles POST /redfish/v1/Systems/{systemId}/Actions/ComputerSystem.Reset
	SystemReset(w http.ResponseWriter, r *http.Request)
	// VirtualMediaCollection handles GET /redfish/v1/Systems/{systemId}/VirtualMedia
	VirtualMediaCollection(w http.ResponseWriter, r *http.Request)
	// VirtualMedia handles GET /redfish/v1/Systems/{systemId}/VirtualMedia/1
	VirtualMedia(w http.ResponseWriter, r *http.Request)
	// VirtualMediaInsert handles POST /redfish/v1/Systems/{systemId}/VirtualMedia/1/Actions/VirtualMedia.InsertMedia
	VirtualMediaInsert(w http.ResponseWriter, r *http.Request)
	// VirtualMediaEject handles POST /redfish/v1/Systems/{systemId}/VirtualMedia/1/Actions/VirtualMedia.EjectMedia
	VirtualMediaEject(w http.ResponseWriter, r *http.Request)
	// Managers handles GET /redfish/v1/Managers
	Managers(w http.ResponseWriter, r *http.Request)
	// Manager handles GET /redfish/v1/Managers/{managerId}
	Manager(w http.ResponseWriter, r *http.Request)
}

type redfishService struct {
	dm *providers.DeviceManager
}

// NewRedfishService creates a new Redfish API service.
func NewRedfishService(dm *providers.DeviceManager) RedfishService {
	return &redfishService{dm: dm}
}

// RegisterRedfishRoutes registers all Redfish routes on the given ServeMux.
func RegisterRedfishRoutes(mux *http.ServeMux, svc RedfishService) {
	mux.HandleFunc("GET /redfish/v1/", svc.ServiceRoot)
	mux.HandleFunc("GET /redfish/v1/Systems", svc.Systems)
	mux.HandleFunc("GET /redfish/v1/Systems/{systemId}", svc.System)
	mux.HandleFunc(
		"POST /redfish/v1/Systems/{systemId}/Actions/ComputerSystem.Reset",
		svc.SystemReset,
	)
	mux.HandleFunc("GET /redfish/v1/Systems/{systemId}/VirtualMedia", svc.VirtualMediaCollection)
	mux.HandleFunc("GET /redfish/v1/Systems/{systemId}/VirtualMedia/{vmId}", svc.VirtualMedia)
	mux.HandleFunc(
		"POST /redfish/v1/Systems/{systemId}/VirtualMedia/{vmId}/Actions/VirtualMedia.InsertMedia",
		svc.VirtualMediaInsert,
	)
	mux.HandleFunc(
		"POST /redfish/v1/Systems/{systemId}/VirtualMedia/{vmId}/Actions/VirtualMedia.EjectMedia",
		svc.VirtualMediaEject,
	)
	mux.HandleFunc("GET /redfish/v1/Managers", svc.Managers)
	mux.HandleFunc("GET /redfish/v1/Managers/{managerId}", svc.Manager)
}

// --- OData / Redfish response types ---

type odataID struct {
	ODataID string `json:"@odata.id"`
}

func writeRedfishJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("OData-Version", "4.0")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		log.Printf("error encoding redfish response: %v", err)
	}
}

func writeRedfishError(w http.ResponseWriter, status int, message string) {
	body := map[string]any{
		"error": map[string]any{
			"code":    "Base.1.0.GeneralError",
			"message": message,
			"@Message.ExtendedInfo": []map[string]any{
				{
					"MessageId": "Base.1.0.GeneralError",
					"Message":   message,
					"Severity":  "Error",
				},
			},
		},
	}
	writeRedfishJSON(w, status, body)
}

// systemID returns the Redfish-safe ID for a managed device.
func systemID(d *providers.ManagedDevice) string {
	if d.Name != "" {
		return d.Name
	}
	return providers.MACToRedfishID(d.MAC)
}

func (s *redfishService) resolveSystem(r *http.Request) (*providers.ManagedDevice, error) {
	id := r.PathValue("systemId")
	if id == "" {
		return nil, fmt.Errorf("system ID is required")
	}
	dev := s.dm.FindDevice(id)
	if dev == nil {
		// Try converting dashes back to colons for MAC lookup.
		macID := strings.ReplaceAll(id, "-", ":")
		dev = s.dm.FindDevice(macID)
	}
	if dev == nil {
		return nil, fmt.Errorf("system not found: %s", id)
	}
	return dev, nil
}

// ServiceRoot handles GET /redfish/v1/.
func (s *redfishService) ServiceRoot(w http.ResponseWriter, _ *http.Request) {
	body := map[string]any{
		"@odata.type":    "#ServiceRoot.v1_5_0.ServiceRoot",
		"@odata.id":      "/redfish/v1/",
		"@odata.context": "/redfish/v1/$metadata#ServiceRoot.ServiceRoot",
		"Id":             "RootService",
		"Name":           "JetKVM Management API",
		"RedfishVersion": "1.6.0",
		"UUID":           "00000000-0000-0000-0000-000000000000",
		"Systems":        odataID{ODataID: "/redfish/v1/Systems"},
		"Managers":       odataID{ODataID: "/redfish/v1/Managers"},
	}
	writeRedfishJSON(w, http.StatusOK, body)
}

// Systems handles GET /redfish/v1/Systems.
func (s *redfishService) Systems(w http.ResponseWriter, _ *http.Request) {
	devices := s.dm.AllDevices()
	members := make([]odataID, 0, len(devices))
	for _, d := range devices {
		// Only include devices that have power control capability.
		if d.HasCapability(providers.CapPowerControl) ||
			d.HasCapability(providers.CapVirtualMedia) ||
			d.HasCapability(providers.CapBootDevice) {
			members = append(members, odataID{
				ODataID: fmt.Sprintf("/redfish/v1/Systems/%s", systemID(d)),
			})
		}
	}

	body := map[string]any{
		"@odata.type":         "#ComputerSystemCollection.ComputerSystemCollection",
		"@odata.id":           "/redfish/v1/Systems",
		"@odata.context":      "/redfish/v1/$metadata#ComputerSystemCollection.ComputerSystemCollection",
		"Name":                "Computer System Collection",
		"Members@odata.count": len(members),
		"Members":             members,
	}
	writeRedfishJSON(w, http.StatusOK, body)
}

// System handles GET /redfish/v1/Systems/{systemId}.
func (s *redfishService) System(w http.ResponseWriter, r *http.Request) {
	dev, err := s.resolveSystem(r)
	if err != nil {
		writeRedfishError(w, http.StatusNotFound, err.Error())
		return
	}

	sysID := systemID(dev)
	body := map[string]any{
		"@odata.type":    "#ComputerSystem.v1_5_0.ComputerSystem",
		"@odata.id":      fmt.Sprintf("/redfish/v1/Systems/%s", sysID),
		"@odata.context": "/redfish/v1/$metadata#ComputerSystem.ComputerSystem",
		"Id":             sysID,
		"Name":           dev.ID(),
		"SystemType":     "Physical",
		"MACAddress":     dev.MAC,
	}

	// Power state (if driver supports it).
	if pc := dev.PowerController(); pc != nil {
		ctx := r.Context()
		state, err := pc.GetPowerState(ctx)
		if err == nil {
			body["PowerState"] = redfishPowerState(state)
		} else {
			body["PowerState"] = "Unknown"
		}

		body["Actions"] = map[string]any{
			"#ComputerSystem.Reset": map[string]any{
				"target": fmt.Sprintf("/redfish/v1/Systems/%s/Actions/ComputerSystem.Reset", sysID),
				"ResetType@Redfish.AllowableValues": []string{
					"On", "ForceOff", "GracefulShutdown", "ForceRestart",
				},
			},
		}
	}

	// Virtual media link.
	if dev.HasCapability(providers.CapVirtualMedia) {
		body["VirtualMedia"] = odataID{
			ODataID: fmt.Sprintf("/redfish/v1/Systems/%s/VirtualMedia", sysID),
		}
	}

	// BMC version.
	if bmc := dev.BMCInfoProvider(); bmc != nil {
		ctx := r.Context()
		ver, err := bmc.GetBMCVersion(ctx)
		if err == nil {
			body["BIOSVersion"] = ver
		}
	}

	writeRedfishJSON(w, http.StatusOK, body)
}

// SystemReset handles POST /redfish/v1/Systems/{systemId}/Actions/ComputerSystem.Reset.
func (s *redfishService) SystemReset(w http.ResponseWriter, r *http.Request) {
	dev, err := s.resolveSystem(r)
	if err != nil {
		writeRedfishError(w, http.StatusNotFound, err.Error())
		return
	}

	pc := dev.PowerController()
	if pc == nil {
		writeRedfishError(w, http.StatusBadRequest, "device does not support power control")
		return
	}

	var body struct {
		ResetType string `json:"ResetType"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeRedfishError(w, http.StatusBadRequest, "invalid JSON payload")
		return
	}

	state := redfishResetToDriverState(body.ResetType)
	if state == "" {
		writeRedfishError(
			w,
			http.StatusBadRequest,
			fmt.Sprintf("unsupported ResetType: %s", body.ResetType),
		)
		return
	}

	if err := pc.SetPowerState(r.Context(), state); err != nil {
		writeRedfishError(
			w,
			http.StatusInternalServerError,
			fmt.Sprintf("failed to set power state: %v", err),
		)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// VirtualMediaCollection handles GET /redfish/v1/Systems/{systemId}/VirtualMedia.
func (s *redfishService) VirtualMediaCollection(w http.ResponseWriter, r *http.Request) {
	dev, err := s.resolveSystem(r)
	if err != nil {
		writeRedfishError(w, http.StatusNotFound, err.Error())
		return
	}

	if !dev.HasCapability(providers.CapVirtualMedia) {
		writeRedfishError(w, http.StatusNotFound, "device does not support virtual media")
		return
	}

	sysID := systemID(dev)
	body := map[string]any{
		"@odata.type":         "#VirtualMediaCollection.VirtualMediaCollection",
		"@odata.id":           fmt.Sprintf("/redfish/v1/Systems/%s/VirtualMedia", sysID),
		"@odata.context":      "/redfish/v1/$metadata#VirtualMediaCollection.VirtualMediaCollection",
		"Name":                "Virtual Media Collection",
		"Members@odata.count": 1,
		"Members": []odataID{
			{ODataID: fmt.Sprintf("/redfish/v1/Systems/%s/VirtualMedia/1", sysID)},
		},
	}
	writeRedfishJSON(w, http.StatusOK, body)
}

// VirtualMedia handles GET /redfish/v1/Systems/{systemId}/VirtualMedia/{vmId}.
func (s *redfishService) VirtualMedia(w http.ResponseWriter, r *http.Request) {
	dev, err := s.resolveSystem(r)
	if err != nil {
		writeRedfishError(w, http.StatusNotFound, err.Error())
		return
	}

	vmc := dev.VirtualMediaController()
	if vmc == nil {
		writeRedfishError(w, http.StatusNotFound, "device does not support virtual media")
		return
	}

	sysID := systemID(dev)
	vmID := r.PathValue("vmId")

	state, err := vmc.GetMediaState(r.Context())
	if err != nil {
		writeRedfishError(
			w,
			http.StatusInternalServerError,
			fmt.Sprintf("failed to get media state: %v", err),
		)
		return
	}

	mediaType := "CD"
	if state != nil && state.Kind != "" {
		switch strings.ToLower(state.Kind) {
		case "floppy":
			mediaType = "Floppy"
		default:
			mediaType = "CD"
		}
	}

	body := map[string]any{
		"@odata.type":    "#VirtualMedia.v1_2_0.VirtualMedia",
		"@odata.id":      fmt.Sprintf("/redfish/v1/Systems/%s/VirtualMedia/%s", sysID, vmID),
		"@odata.context": "/redfish/v1/$metadata#VirtualMedia.VirtualMedia",
		"Id":             vmID,
		"Name":           "Virtual Media",
		"MediaTypes":     []string{"CD", "DVD"},
		"Inserted":       state != nil && state.Inserted,
		"Image":          "",
		"ConnectedVia":   "URI",
		"MediaType":      mediaType,
		"Actions": map[string]any{
			"#VirtualMedia.InsertMedia": map[string]any{
				"target": fmt.Sprintf(
					"/redfish/v1/Systems/%s/VirtualMedia/%s/Actions/VirtualMedia.InsertMedia",
					sysID,
					vmID,
				),
			},
			"#VirtualMedia.EjectMedia": map[string]any{
				"target": fmt.Sprintf(
					"/redfish/v1/Systems/%s/VirtualMedia/%s/Actions/VirtualMedia.EjectMedia",
					sysID,
					vmID,
				),
			},
		},
	}

	if state != nil && state.Image != "" {
		body["Image"] = state.Image
	}

	writeRedfishJSON(w, http.StatusOK, body)
}

// VirtualMediaInsert handles POST .../Actions/VirtualMedia.InsertMedia.
func (s *redfishService) VirtualMediaInsert(w http.ResponseWriter, r *http.Request) {
	dev, err := s.resolveSystem(r)
	if err != nil {
		writeRedfishError(w, http.StatusNotFound, err.Error())
		return
	}

	vmc := dev.VirtualMediaController()
	if vmc == nil {
		writeRedfishError(w, http.StatusBadRequest, "device does not support virtual media")
		return
	}

	var body struct {
		Image          string `json:"Image"`
		Inserted       *bool  `json:"Inserted"`
		TransferMethod string `json:"TransferMethod"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeRedfishError(w, http.StatusBadRequest, "invalid JSON payload")
		return
	}

	if body.Image == "" {
		writeRedfishError(w, http.StatusBadRequest, "Image URL is required")
		return
	}

	if err := vmc.MountMedia(r.Context(), body.Image, "cdrom"); err != nil {
		writeRedfishError(
			w,
			http.StatusInternalServerError,
			fmt.Sprintf("failed to mount media: %v", err),
		)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// VirtualMediaEject handles POST .../Actions/VirtualMedia.EjectMedia.
func (s *redfishService) VirtualMediaEject(w http.ResponseWriter, r *http.Request) {
	dev, err := s.resolveSystem(r)
	if err != nil {
		writeRedfishError(w, http.StatusNotFound, err.Error())
		return
	}

	vmc := dev.VirtualMediaController()
	if vmc == nil {
		writeRedfishError(w, http.StatusBadRequest, "device does not support virtual media")
		return
	}

	if err := vmc.UnmountMedia(r.Context()); err != nil {
		writeRedfishError(
			w,
			http.StatusInternalServerError,
			fmt.Sprintf("failed to eject media: %v", err),
		)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// Managers handles GET /redfish/v1/Managers.
func (s *redfishService) Managers(w http.ResponseWriter, _ *http.Request) {
	devices := s.dm.AllDevices()
	members := make([]odataID, 0, len(devices))
	for _, d := range devices {
		if d.HasCapability(providers.CapBMCInfo) {
			members = append(members, odataID{
				ODataID: fmt.Sprintf("/redfish/v1/Managers/%s", systemID(d)),
			})
		}
	}

	body := map[string]any{
		"@odata.type":         "#ManagerCollection.ManagerCollection",
		"@odata.id":           "/redfish/v1/Managers",
		"@odata.context":      "/redfish/v1/$metadata#ManagerCollection.ManagerCollection",
		"Name":                "Manager Collection",
		"Members@odata.count": len(members),
		"Members":             members,
	}
	writeRedfishJSON(w, http.StatusOK, body)
}

// Manager handles GET /redfish/v1/Managers/{managerId}.
func (s *redfishService) Manager(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("managerId")
	dev := s.dm.FindDevice(id)
	if dev == nil {
		macID := strings.ReplaceAll(id, "-", ":")
		dev = s.dm.FindDevice(macID)
	}
	if dev == nil {
		writeRedfishError(w, http.StatusNotFound, fmt.Sprintf("manager not found: %s", id))
		return
	}

	mgrID := systemID(dev)
	body := map[string]any{
		"@odata.type":    "#Manager.v1_5_0.Manager",
		"@odata.id":      fmt.Sprintf("/redfish/v1/Managers/%s", mgrID),
		"@odata.context": "/redfish/v1/$metadata#Manager.Manager",
		"Id":             mgrID,
		"Name":           dev.ID(),
		"ManagerType":    "BMC",
	}

	if bmc := dev.BMCInfoProvider(); bmc != nil {
		ver, err := bmc.GetBMCVersion(r.Context())
		if err == nil {
			body["FirmwareVersion"] = ver
		}
	}

	// Capabilities summary.
	caps := dev.MergedCapabilities()
	capNames := make([]string, 0, len(caps))
	for _, c := range caps {
		capNames = append(capNames, string(c))
	}
	body["Description"] = fmt.Sprintf(
		"BMC Manager (capabilities: %s)",
		strings.Join(capNames, ", "),
	)

	writeRedfishJSON(w, http.StatusOK, body)
}

// --- Helper mappings ---

func redfishPowerState(state string) string {
	switch strings.ToLower(state) {
	case "on":
		return "On"
	case "off":
		return "Off"
	default:
		return "Unknown"
	}
}

func redfishResetToDriverState(resetType string) string {
	switch resetType {
	case "On", "ForceOn":
		return "on"
	case "ForceOff", "GracefulShutdown":
		return "off"
	case "ForceRestart", "GracefulRestart":
		return "cycle"
	case "Nmi":
		return "reset"
	default:
		return ""
	}
}
