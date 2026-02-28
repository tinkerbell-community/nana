// Package unifi implements a BMC provider for devices powered via UniFi switch PoE.
//
// The UniFi provider uses the UniFi controller API to discover which switch and
// port a device is connected to, then controls PoE power over SSH. The upstream
// switch IP and port are cached and re-discovered on SSH errors.
package unifi

import (
	"context"
	"encoding/csv"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tinkerbell-community/nana/internal/providers"
	"github.com/ubiquiti-community/go-unifi/unifi"
	"golang.org/x/crypto/ssh"
)

// API client registry: one shared client per controller URL.
var (
	clientsMu sync.Mutex
	clients   = make(map[string]*unifi.ApiClient)
)

func getOrCreateClient(
	ctx context.Context,
	logger *slog.Logger,
	apiURL, apiKey string,
) (*unifi.ApiClient, error) {
	clientsMu.Lock()
	defer clientsMu.Unlock()

	if c, ok := clients[apiURL]; ok {
		return c, nil
	}

	c, err := unifi.New(ctx, &unifi.Config{
		BaseURL:       apiURL,
		APIKey:        apiKey,
		Logger:        logger,
		AllowInsecure: true,
	})
	if err != nil {
		return nil, err
	}

	clients[apiURL] = c
	return c, nil
}

// uplinkInfo holds cached information about the upstream switch for a device.
type uplinkInfo struct {
	switchIP string
	port     int
}

// Provider implements the providers.Provider and providers.PowerController
// interfaces for devices powered via UniFi switch PoE ports.
type Provider struct {
	apiURL string
	apiKey string
	site   string
	mac    string
	signer ssh.Signer

	mu        sync.Mutex
	client    *unifi.ApiClient
	uplink    *uplinkInfo
	sshConfig *ssh.ClientConfig
	sshPort   int

	logger *slog.Logger
}

func init() {
	providers.Register("unifi", newProvider)
}

func newProvider(cfg map[string]any) (providers.Provider, error) {
	apiURL, _ := cfg["host"].(string)
	if apiURL == "" {
		return nil, fmt.Errorf("unifi provider requires 'host' config")
	}

	apiKey, _ := cfg["api_key"].(string)
	if apiKey == "" {
		return nil, fmt.Errorf("unifi provider requires 'api_key' config")
	}

	mac, _ := cfg["mac"].(string)
	if mac == "" {
		return nil, fmt.Errorf("unifi provider requires 'mac' config")
	}

	site, _ := cfg["site"].(string)
	if site == "" {
		site = "default"
	}

	privatePEM, _, err := generateSSHKey(apiKey)
	if err != nil {
		return nil, fmt.Errorf("failed to generate SSH key from API key: %w", err)
	}
	signer, err := ssh.ParsePrivateKey(privatePEM)
	if err != nil {
		return nil, fmt.Errorf("failed to parse generated SSH private key: %w", err)
	}

	logger := slog.Default().With("provider", "unifi", "host", apiURL, "site", site, "mac", mac)

	return &Provider{
		apiURL:  apiURL,
		apiKey:  apiKey,
		site:    site,
		mac:     mac,
		signer:  signer,
		sshPort: 22,
		logger:  logger,
	}, nil
}

// Name returns the provider type identifier.
func (p *Provider) Name() string { return "unifi" }

// Capabilities returns the list of capabilities this provider offers.
func (p *Provider) Capabilities() []providers.Capability {
	return []providers.Capability{providers.CapPowerControl}
}

// ensureClient lazily initializes the UniFi API client if it has not been
// opened yet. It is safe to call before every operation — Open is idempotent.
func (p *Provider) ensureClient(ctx context.Context) error {
	p.mu.Lock()
	initialized := p.client != nil
	p.mu.Unlock()

	if initialized {
		return nil
	}

	return p.Open(ctx)
}

// Open initializes the shared UniFi API client and provisions the SSH key if needed.
func (p *Provider) Open(ctx context.Context) error {
	client, err := getOrCreateClient(ctx, p.logger, p.apiURL, p.apiKey)
	if err != nil {
		return fmt.Errorf("failed to create UniFi API client: %w", err)
	}

	_, publicAuthorizedKey, err := generateSSHKey(p.apiKey)
	if err != nil {
		return fmt.Errorf("failed to generate SSH public key: %w", err)
	}

	result, err := ensureSSHKey(ctx, client, p.site, publicAuthorizedKey)
	if err != nil {
		return fmt.Errorf("failed to ensure SSH key on UniFi device: %w", err)
	}

	p.logger.Info("SSH key provisioned", slog.String("ssh_user", result.username))

	// Only cache the client after all initialization succeeds.
	p.mu.Lock()
	p.client = client
	p.sshConfig = &ssh.ClientConfig{
		User: result.username,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(p.signer),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         30 * time.Second,
	}
	p.mu.Unlock()

	return nil
}

// Close clears cached state.
func (p *Provider) Close() error {
	p.mu.Lock()
	p.uplink = nil
	p.mu.Unlock()
	return nil
}

// --- Uplink discovery ---

// resolveUplink discovers or returns the cached upstream switch IP and port.
func (p *Provider) resolveUplink(ctx context.Context) (*uplinkInfo, error) {
	if err := p.ensureClient(ctx); err != nil {
		return nil, fmt.Errorf("failed to initialize UniFi client: %w", err)
	}

	p.mu.Lock()
	if p.uplink != nil {
		info := *p.uplink
		p.mu.Unlock()
		return &info, nil
	}
	p.mu.Unlock()

	clientInfo, err := p.client.GetClientInfo(ctx, p.site, p.mac)
	if err != nil {
		return nil, fmt.Errorf("failed to get client info for MAC %s: %w", p.mac, err)
	}

	// If the live client info lacks usable port data (common for offline devices),
	// try the client history endpoint which retains port info after disconnect.
	// The API may return sw_port=0 for offline devices, so check the value too.
	hasPort := (clientInfo.SwPort != nil && *clientInfo.SwPort > 0) ||
		(clientInfo.LastUplinkRemotePort != nil && *clientInfo.LastUplinkRemotePort > 0)
	if !hasPort {
		p.logger.Info("live client info has no port data, trying history endpoint")
		hist, histErr := p.client.ListClientHistory(ctx, p.site, 0)
		if histErr != nil {
			p.logger.Warn("client history lookup failed", slog.String("error", histErr.Error()))
		} else {
			normalizedMAC := strings.ToLower(p.mac)
			for i := range hist {
				if strings.ToLower(hist[i].Mac) == normalizedMAC {
					clientInfo = &hist[i]
					break
				}
			}
		}
	}

	// Coalesce uplink MAC: prefer current, fall back to last known.
	uplinkMAC := clientInfo.UplinkMac
	if uplinkMAC == "" {
		uplinkMAC = clientInfo.LastUplinkMac
	}
	if uplinkMAC == "" {
		return nil, fmt.Errorf(
			"no uplink MAC found for device %s (display_name=%q, status=%q)",
			p.mac,
			clientInfo.DisplayName,
			clientInfo.Status,
		)
	}

	device, err := p.client.GetDeviceByMAC(ctx, p.site, uplinkMAC)
	if err != nil {
		return nil, fmt.Errorf("failed to get uplink device %s: %w", uplinkMAC, err)
	}

	// Resolve the switch management IP. Prefer the runtime IP from the
	// stat/device response (works for both DHCP and static), fall back to
	// the configured static IP in config_network.
	switchIP := device.IP
	if switchIP == "" && device.ConfigNetwork != nil {
		switchIP = device.ConfigNetwork.IP
	}
	if switchIP == "" {
		return nil, fmt.Errorf("no IP found for uplink device %s", uplinkMAC)
	}

	// Determine the switch port. First try the client info fields from the
	// v2 API, then fall back to searching the switch's own port table for a
	// port whose last-connected MAC matches the device.
	var switchPort int
	if clientInfo.SwPort != nil && *clientInfo.SwPort > 0 {
		switchPort = int(*clientInfo.SwPort)
	} else if clientInfo.LastUplinkRemotePort != nil && *clientInfo.LastUplinkRemotePort > 0 {
		switchPort = int(*clientInfo.LastUplinkRemotePort)
	} else {
		switchPort = findPortByMAC(device.PortTable, p.mac)
	}
	if switchPort == 0 {
		return nil, fmt.Errorf(
			"no switch port found for device %s (display_name=%q, status=%q, uplink_mac=%q)",
			p.mac,
			clientInfo.DisplayName,
			clientInfo.Status,
			uplinkMAC,
		)
	}

	info := &uplinkInfo{
		switchIP: switchIP,
		port:     switchPort,
	}

	p.mu.Lock()
	p.uplink = info
	p.mu.Unlock()

	return info, nil
}

// findPortByMAC searches a switch's port table for a port whose last-connected
// client MAC matches the given MAC address. Returns 0 if no match is found.
func findPortByMAC(portTable []unifi.DevicePortTable, mac string) int {
	normalized := strings.ToLower(strings.ReplaceAll(mac, "-", ":"))
	for _, pt := range portTable {
		if strings.EqualFold(pt.LastConnection.MAC, normalized) && pt.PortIdx > 0 {
			return int(pt.PortIdx)
		}
	}
	return 0
}

// invalidateUplink clears the cached uplink information.
func (p *Provider) invalidateUplink() {
	p.mu.Lock()
	p.uplink = nil
	p.mu.Unlock()
}

// --- SSH execution ---

// sshExec runs a command on the given host via SSH using the connection pool.
func (p *Provider) sshExec(ctx context.Context, host, command string) (string, error) {
	addr := net.JoinHostPort(host, strconv.Itoa(p.sshPort))

	conn, err := connPool.get(addr, p.sshConfig)
	if err != nil {
		return "", fmt.Errorf("failed to connect to SSH server %s: %w", addr, err)
	}

	session, err := conn.NewSession()
	if err != nil {
		return "", fmt.Errorf("failed to create SSH session: %w", err)
	}
	defer func() { _ = session.Close() }()

	done := make(chan error, 1)
	var output []byte

	go func() {
		var cmdErr error
		output, cmdErr = session.CombinedOutput(command)
		done <- cmdErr
	}()

	select {
	case <-ctx.Done():
		return "", fmt.Errorf("command execution cancelled: %w", ctx.Err())
	case err := <-done:
		if err != nil {
			return "", fmt.Errorf("command execution failed: %w", err)
		}
	}

	return string(output), nil
}

// executeOnSwitch resolves the uplink, executes a command on the upstream switch,
// and retries once on SSH failure after invalidating the cache.
func (p *Provider) executeOnSwitch(
	ctx context.Context,
	buildCmd func(port int) string,
) (string, int, error) {
	uplink, err := p.resolveUplink(ctx)
	if err != nil {
		return "", 0, err
	}

	command := buildCmd(uplink.port)
	output, err := p.sshExec(ctx, uplink.switchIP, command)
	if err != nil {
		// Remove stale SSH connection and invalidate uplink cache.
		addr := net.JoinHostPort(uplink.switchIP, strconv.Itoa(p.sshPort))
		connPool.remove(addr)
		p.invalidateUplink()

		// Re-discover and retry once.
		uplink, err = p.resolveUplink(ctx)
		if err != nil {
			return "", 0, err
		}

		command = buildCmd(uplink.port)
		output, err = p.sshExec(ctx, uplink.switchIP, command)
		if err != nil {
			return "", 0, err
		}
	}

	return output, uplink.port, nil
}

type poePortStatus struct {
	port   int
	poePwr string
}

type poeStatus struct {
	ports []poePortStatus
}

func parsePoEStatus(output string) (*poeStatus, error) {
	lines := strings.Split(output, "\n")
	if len(lines) < 2 {
		return nil, fmt.Errorf("insufficient output lines")
	}

	status := &poeStatus{}

	// Find the data section (after the header separator line).
	dataStartIdx := -1
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "----") {
			dataStartIdx = i + 1
			break
		}
	}

	if dataStartIdx == -1 || dataStartIdx >= len(lines) {
		return nil, fmt.Errorf("could not find data section in output")
	}

	for i := dataStartIdx; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}

		reader := csv.NewReader(strings.NewReader(line))
		reader.Comma = ' '
		reader.LazyQuotes = true
		reader.TrimLeadingSpace = true
		reader.FieldsPerRecord = -1

		records, err := reader.ReadAll()
		if err != nil || len(records) == 0 {
			continue
		}

		fields := records[0]
		var nonEmpty []string
		for _, f := range fields {
			if f != "" {
				nonEmpty = append(nonEmpty, f)
			}
		}

		if len(nonEmpty) < 7 {
			continue
		}

		ps := poePortStatus{}
		if port, err := strconv.Atoi(nonEmpty[0]); err == nil {
			ps.port = port
		}

		// PoEPwr field position depends on whether Class is two words.
		classIdx := 4
		if classIdx < len(nonEmpty) && strings.HasPrefix(nonEmpty[classIdx], "Class") &&
			classIdx+1 < len(nonEmpty) {
			classIdx++
		}
		if classIdx+1 < len(nonEmpty) {
			ps.poePwr = nonEmpty[classIdx+1]
		}

		status.ports = append(status.ports, ps)
	}

	if len(status.ports) == 0 {
		return nil, fmt.Errorf("no port data found in output")
	}

	return status, nil
}

type macEntry struct {
	port       int
	macAddress string
}

type macList struct {
	entries []macEntry
}

func parseMacList(output string) (*macList, error) {
	lines := strings.Split(output, "\n")
	if len(lines) < 2 {
		return nil, fmt.Errorf("insufficient output lines")
	}

	ml := &macList{}

	dataStartIdx := -1
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "----") {
			dataStartIdx = i + 1
			break
		}
	}

	if dataStartIdx == -1 || dataStartIdx >= len(lines) {
		return nil, fmt.Errorf("could not find data section in output")
	}

	for i := dataStartIdx; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}

		if strings.HasPrefix(line, "Total number of entries:") {
			break
		}

		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}

		port, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}

		entry := macEntry{
			port:       port,
			macAddress: fields[2],
		}
		ml.entries = append(ml.entries, entry)
	}

	return ml, nil
}

// Compile-time interface checks.
var (
	_ providers.Provider        = (*Provider)(nil)
	_ providers.PowerController = (*Provider)(nil)
)
