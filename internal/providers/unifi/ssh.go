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
func ensureSSHKey(
	ctx context.Context,
	client *unifi.ApiClient,
	site string,
	publicAuthorizedKey []byte,
) error {
	_, mgmt, err := unifi.GetSetting[*settings.Mgmt](client, ctx, site)
	if err != nil {
		return fmt.Errorf("failed to get mgmt settings: %w", err)
	}

	pubKeyStr := strings.TrimSpace(string(publicAuthorizedKey))

	// Check if the key already exists.
	for _, existing := range mgmt.SSHKeys {
		if strings.TrimSpace(existing.Key) == pubKeyStr {
			return nil // already present
		}
	}

	mgmt.SSHEnabled = true
	mgmt.SSHKeys = append(mgmt.SSHKeys, settings.SettingMgmtSSHKeys{
		Name:    "mgmt-api",
		Key:     pubKeyStr,
		KeyType: ssh.KeyAlgoED25519,
		Comment: "auto-provisioned by mgmt-api",
	})

	if err := client.UpdateSetting(ctx, site, mgmt); err != nil {
		return fmt.Errorf("failed to update mgmt settings: %w", err)
	}

	return nil
}
