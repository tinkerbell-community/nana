package jetkvm

import "context"

// GetBMCVersion returns the JetKVM firmware version string.
func (p *Provider) GetBMCVersion(ctx context.Context) (string, error) {
	version, err := p.kvmClient.GetLocalVersion(ctx)
	if err != nil {
		return "", err
	}
	return version.AppVersion, nil
}
