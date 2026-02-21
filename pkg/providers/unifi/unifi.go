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
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jetkvm/cloud-api/mgmt-api/pkg/providers"
	"github.com/ubiquiti-community/go-unifi/unifi"
	"golang.org/x/crypto/ssh"
)

// API client registry: one shared client per controller URL.
var (
	clientsMu sync.Mutex
	clients   = make(map[string]*unifi.ApiClient)
)

func getOrCreateClient(ctx context.Context, apiURL, apiKey string) (*unifi.ApiClient, error) {
	clientsMu.Lock()
	defer clientsMu.Unlock()

	if c, ok := clients[apiURL]; ok {
		return c, nil
	}

	c, err := unifi.New(ctx, &unifi.Config{
		BaseURL: apiURL,
		APIKey:  apiKey,
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
	apiURL       string
	apiKey       string
	site         string
	mac          string
	sshConfig    *ssh.ClientConfig
	sshPort      int
	provisionSSH bool // whether to provision SSH key via API

	mu     sync.Mutex
	client *unifi.ApiClient
	uplink *uplinkInfo
}

func init() {
	providers.Register("unifi", newProvider)
}

func newProvider(cfg map[string]any) (providers.Provider, error) {
	apiURL, _ := cfg["api_url"].(string)
	if apiURL == "" {
		return nil, fmt.Errorf("unifi provider requires 'api_url' config")
	}

	apiKey, _ := cfg["api_key"].(string)
	if apiKey == "" {
		return nil, fmt.Errorf("unifi provider requires 'api_key' config")
	}

	mac, _ := cfg["mac"].(string)
	if mac == "" {
		return nil, fmt.Errorf("unifi provider requires 'mac' config")
	}

	username, _ := cfg["ssh_username"].(string)
	if username == "" {
		username = "root"
	}

	sshPort := 22
	if p, ok := cfg["ssh_port"].(int); ok && p > 0 {
		sshPort = p
	}
	// Viper may deserialize YAML ints as float64 through map[string]interface{}.
	if p, ok := cfg["ssh_port"].(float64); ok && p > 0 {
		sshPort = int(p)
	}

	site, _ := cfg["site"].(string)
	if site == "" {
		site = "default"
	}

	sshKeyPath, _ := cfg["ssh_key_path"].(string)

	var signer ssh.Signer
	provisionSSH := false

	switch {
	case sshKeyPath != "":
		// Expand ~ in path.
		if strings.HasPrefix(sshKeyPath, "~/") {
			home, err := os.UserHomeDir()
			if err == nil {
				sshKeyPath = home + sshKeyPath[1:]
			}
		}

		privateKey, err := os.ReadFile(sshKeyPath)
		if err != nil {
			return nil, fmt.Errorf("failed to read SSH private key %s: %w", sshKeyPath, err)
		}

		signer, err = ssh.ParsePrivateKey(privateKey)
		if err != nil {
			return nil, fmt.Errorf("failed to parse SSH private key: %w", err)
		}

	default:
		// Generate SSH key from API key.
		privatePEM, _, err := generateKeyFromAPI(apiKey)
		if err != nil {
			return nil, fmt.Errorf("failed to generate SSH key from API key: %w", err)
		}
		signer, err = ssh.ParsePrivateKey(privatePEM)
		if err != nil {
			return nil, fmt.Errorf("failed to parse generated SSH private key: %w", err)
		}
		provisionSSH = true
	}

	sshConfig := &ssh.ClientConfig{
		User: username,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         30 * time.Second,
	}

	return &Provider{
		apiURL:       apiURL,
		apiKey:       apiKey,
		site:         site,
		mac:          mac,
		sshConfig:    sshConfig,
		sshPort:      sshPort,
		provisionSSH: provisionSSH,
	}, nil
}

// Name returns the provider type identifier.
func (p *Provider) Name() string { return "unifi" }

// Capabilities returns the list of capabilities this provider offers.
func (p *Provider) Capabilities() []providers.Capability {
	return []providers.Capability{providers.CapPowerControl}
}

// Open initializes the shared UniFi API client and provisions the SSH key if needed.
func (p *Provider) Open(ctx context.Context) error {
	client, err := getOrCreateClient(ctx, p.apiURL, p.apiKey)
	if err != nil {
		return fmt.Errorf("failed to create UniFi API client: %w", err)
	}
	p.client = client

	if p.provisionSSH {
		_, publicAuthorizedKey, err := generateKeyFromAPI(p.apiKey)
		if err != nil {
			return fmt.Errorf("failed to generate SSH public key: %w", err)
		}

		if err := ensureSSHKey(ctx, p.client, p.site, publicAuthorizedKey); err != nil {
			return fmt.Errorf("failed to ensure SSH key on UniFi device: %w", err)
		}
	}

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

	// Coalesce uplink MAC: prefer current, fall back to last known.
	uplinkMAC := clientInfo.UplinkMac
	if uplinkMAC == "" {
		uplinkMAC = clientInfo.LastUplinkMac
	}
	if uplinkMAC == "" {
		return nil, fmt.Errorf("no uplink MAC found for device %s", p.mac)
	}

	// Coalesce switch port: prefer current, fall back to last known.
	var switchPort int
	if clientInfo.SwPort != nil {
		switchPort = int(*clientInfo.SwPort)
	} else if clientInfo.LastUplinkRemotePort != nil {
		switchPort = int(*clientInfo.LastUplinkRemotePort)
	}
	if switchPort == 0 {
		return nil, fmt.Errorf("no switch port found for device %s", p.mac)
	}

	device, err := p.client.GetDeviceByMAC(ctx, p.site, uplinkMAC)
	if err != nil {
		return nil, fmt.Errorf("failed to get uplink device %s: %w", uplinkMAC, err)
	}

	if device.ConfigNetwork == nil || device.ConfigNetwork.IP == "" {
		return nil, fmt.Errorf("no IP configured for uplink device %s", uplinkMAC)
	}

	info := &uplinkInfo{
		switchIP: device.ConfigNetwork.IP,
		port:     switchPort,
	}

	p.mu.Lock()
	p.uplink = info
	p.mu.Unlock()

	return info, nil
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
	defer session.Close()

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
func (p *Provider) executeOnSwitch(ctx context.Context, buildCmd func(port int) string) (string, int, error) {
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
