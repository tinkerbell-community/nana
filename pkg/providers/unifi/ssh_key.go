package unifi

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/pem"
	"fmt"
	"strings"

	"github.com/ubiquiti-community/go-unifi/unifi"
	"github.com/ubiquiti-community/go-unifi/unifi/settings"
	"golang.org/x/crypto/ssh"
)

// GenerateKeyFromAPI takes the UniFi API key and returns the SSH private and public keys.
func GenerateKeyFromAPI(apiKey string) ([]byte, []byte, error) {
	// 1. Add a static salt for domain separation.
	// This ensures the hash is completely unique to this specific SSH use-case.
	salt := "unifi-swctrl-ssh-seed-v1"
	seedMaterial := apiKey + salt

	// 2. Hash the combined string to create the strict 32-byte seed
	hash := sha256.Sum256([]byte(seedMaterial))

	// 3. Generate the Ed25519 keypair
	privateKey := ed25519.NewKeyFromSeed(hash[:])
	publicKey := privateKey.Public().(ed25519.PublicKey)

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
	baseURL, apiKey, site string,
	publicAuthorizedKey []byte,
) error {
	client, err := unifi.New(ctx, &unifi.Config{
		BaseURL: baseURL,
		APIKey:  apiKey,
	})
	if err != nil {
		return fmt.Errorf("failed to create UniFi client: %w", err)
	}

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
		KeyType: "ssh-ed25519",
		Comment: "auto-provisioned by mgmt-api",
	})

	if err := client.UpdateSetting(ctx, site, mgmt); err != nil {
		return fmt.Errorf("failed to update mgmt settings: %w", err)
	}

	return nil
}
