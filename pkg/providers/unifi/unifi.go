// Package unifi implements a BMC provider for devices powered via UniFi switch PoE.
//
// The UniFi provider connects to a UniFi switch over SSH and controls PoE power
// on the port associated with a device's MAC address. The port is auto-discovered
// from the switch's MAC address table and cached for subsequent operations.
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
	"golang.org/x/crypto/ssh"
)

// Provider implements the providers.Provider and providers.PowerController
// interfaces for devices powered via UniFi switch PoE ports.
type Provider struct {
	sshConfig  *ssh.ClientConfig
	host       string
	sshPort    int
	mac        string // device MAC for port auto-discovery
	mu         sync.Mutex
	cachedPort int // 0 = not yet resolved
}

func init() {
	providers.Register("unifi", newProvider)
}

func newProvider(cfg map[string]any) (providers.Provider, error) {
	host, _ := cfg["host"].(string)
	if host == "" {
		return nil, fmt.Errorf("unifi provider requires 'host' config")
	}

	sshKeyPath, _ := cfg["ssh_key_path"].(string)
	if sshKeyPath == "" {
		return nil, fmt.Errorf("unifi provider requires 'ssh_key_path' config")
	}

	// Expand ~ in path.
	if strings.HasPrefix(sshKeyPath, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			sshKeyPath = home + sshKeyPath[1:]
		}
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

	mac, _ := cfg["mac"].(string)

	// Read and parse the SSH private key.
	privateKey, err := os.ReadFile(sshKeyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read SSH private key %s: %w", sshKeyPath, err)
	}

	signer, err := ssh.ParsePrivateKey(privateKey)
	if err != nil {
		return nil, fmt.Errorf("failed to parse SSH private key: %w", err)
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
		sshConfig: sshConfig,
		host:      host,
		sshPort:   sshPort,
		mac:       mac,
	}, nil
}

// Name returns the provider type identifier.
func (p *Provider) Name() string { return "unifi" }

// Capabilities returns the list of capabilities this provider offers.
func (p *Provider) Capabilities() []providers.Capability {
	return []providers.Capability{providers.CapPowerControl}
}

// Open is a no-op; SSH connections are made per-command.
func (p *Provider) Open(_ context.Context) error { return nil }

// Close is a no-op; SSH connections are made per-command.
func (p *Provider) Close() error { return nil }

// --- SSH helpers ---

// resolvePort determines the switch port for this device's MAC address.
// The result is cached after the first successful lookup.
func (p *Provider) resolvePort(ctx context.Context) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.cachedPort > 0 {
		return p.cachedPort, nil
	}

	if p.mac == "" {
		return 0, fmt.Errorf("unifi provider: no MAC address configured for port discovery")
	}

	output, err := p.executeCommand(ctx, "swctrl mac show")
	if err != nil {
		return 0, fmt.Errorf("failed to get MAC table: %w", err)
	}

	ml, err := parseMacList(output)
	if err != nil {
		return 0, fmt.Errorf("failed to parse MAC table: %w", err)
	}

	// Try normalized comparison.
	normalizedSearch := providers.NormalizeMAC(p.mac)
	for _, entry := range ml.entries {
		if providers.NormalizeMAC(entry.macAddress) == normalizedSearch {
			p.cachedPort = entry.port
			return p.cachedPort, nil
		}
	}

	return 0, fmt.Errorf("MAC address %s not found in switch MAC table", p.mac)
}

// executeCommand executes a command on the UniFi switch via SSH.
func (p *Provider) executeCommand(ctx context.Context, command string) (string, error) {
	addr := net.JoinHostPort(p.host, strconv.Itoa(p.sshPort))

	conn, err := ssh.Dial("tcp", addr, p.sshConfig)
	if err != nil {
		return "", fmt.Errorf("failed to connect to SSH server %s: %w", addr, err)
	}
	defer conn.Close()

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
