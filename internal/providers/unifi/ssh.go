package unifi

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/pem"
	"fmt"
	"strings"
	"sync"

	"github.com/ubiquiti-community/go-unifi/unifi"
	"github.com/ubiquiti-community/go-unifi/unifi/settings"
	"golang.org/x/crypto/ssh"
)

var connPool = &sshConnectionPool{conns: make(map[string]*ssh.Client)}

// sshConnectionPool manages reusable SSH connections keyed by address (host:port).
// Multiple providers sharing the same upstream switch reuse the same connection.
type sshConnectionPool struct {
	mu    sync.Mutex
	conns map[string]*ssh.Client
}

// get returns an existing connection for the address or dials a new one.
func (p *sshConnectionPool) get(addr string, config *ssh.ClientConfig) (*ssh.Client, error) {
	p.mu.Lock()
	if c, ok := p.conns[addr]; ok {
		p.mu.Unlock()
		return c, nil
	}
	p.mu.Unlock()

	c, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		return nil, err
	}

	p.mu.Lock()
	if existing, ok := p.conns[addr]; ok {
		p.mu.Unlock()
		err = c.Close()
		return existing, err
	}
	p.conns[addr] = c
	p.mu.Unlock()

	return c, nil
}

// remove closes and removes a connection from the pool.
func (p *sshConnectionPool) remove(addr string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if c, ok := p.conns[addr]; ok {
		_ = c.Close()
		delete(p.conns, addr)
	}
}

// generateSSHKey takes the UniFi API key and returns the SSH private and public keys.
func generateSSHKey(apiKey string) ([]byte, []byte, error) {
	// 1. Add a static salt for domain separation.
	// This ensures the hash is completely unique to this specific SSH use-case.
	salt := "unifi-swctrl-ssh-seed-v1"
	seedMaterial := apiKey + salt

	// 2. Hash the combined string to create the strict 32-byte seed
	hash := sha256.Sum256([]byte(seedMaterial))

	// 3. Generate the Ed25519 keypair
	privateKey := ed25519.NewKeyFromSeed(hash[:])
	publicKey, ok := privateKey.Public().(ed25519.PublicKey)
	if !ok {
		return nil, nil, fmt.Errorf("failed to derive public key from private key")
	}

	// 4. Marshal the keys (PEM and authorized_keys formats)
	privBlock, err := ssh.MarshalPrivateKey(privateKey, "")
	if err != nil {
		return nil, nil, err
	}
	privatePEM := pem.EncodeToMemory(privBlock)

	sshPubKey, err := ssh.NewPublicKey(publicKey)
	if err != nil {
		return nil, nil, err
	}
	publicAuthorizedKey := ssh.MarshalAuthorizedKey(sshPubKey)

	return privatePEM, publicAuthorizedKey, nil
}

// ensureSSHKey uses the UniFi API to ensure the generated SSH public key is
// present in the device's mgmt settings.
// ensureSSHKeyResult holds the SSH username along with any error from provisioning.
type ensureSSHKeyResult struct {
	username string
}

func ensureSSHKey(
	ctx context.Context,
	client *unifi.ApiClient,
	site string,
	publicAuthorizedKey []byte,
) (*ensureSSHKeyResult, error) {
	_, mgmt, err := unifi.GetSetting[*settings.Mgmt](client, ctx, site)
	if err != nil {
		return nil, fmt.Errorf("failed to get mgmt settings: %w", err)
	}

	// Use the SSH username from the controller's management settings.
	// UniFi switches authenticate with this username, not necessarily "root".
	username := mgmt.SSHUsername
	if username == "" {
		username = "root"
	}

	pubKeyStr := strings.TrimSpace(string(publicAuthorizedKey))

	// The UniFi controller stores the key type separately and reconstructs
	// the authorized_keys line as "{type} {key}" when provisioning to
	// switches. Strip the algorithm prefix so it doesn't get doubled.
	if parts := strings.SplitN(pubKeyStr, " ", 3); len(parts) >= 2 {
		pubKeyStr = parts[1] // just the base64-encoded key material
	}

	// Check if the key already exists (correctly formatted).
	for _, existing := range mgmt.SSHKeys {
		if strings.TrimSpace(existing.Key) == pubKeyStr {
			return &ensureSSHKeyResult{username: username}, nil
		}
	}

	// Remove any previously provisioned keys with our name that may have the
	// wrong format (e.g. full authorized_keys line instead of base64 only).
	cleaned := mgmt.SSHKeys[:0]
	for _, k := range mgmt.SSHKeys {
		if k.Name != "mgmt-api" {
			cleaned = append(cleaned, k)
		}
	}

	mgmt.SSHEnabled = true
	mgmt.SSHKeys = append(cleaned, settings.SettingMgmtSSHKeys{
		Name:    "mgmt-api",
		Key:     pubKeyStr,
		KeyType: ssh.KeyAlgoED25519,
		Comment: "auto-provisioned by mgmt-api",
	})

	if err := client.UpdateSetting(ctx, site, mgmt); err != nil {
		return nil, fmt.Errorf("failed to update mgmt settings: %w", err)
	}

	return &ensureSSHKeyResult{username: username}, nil
}
