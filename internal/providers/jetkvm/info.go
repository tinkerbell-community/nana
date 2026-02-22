package jetkvm

import (
	"context"
	"fmt"
)

// GetBMCVersion returns the JetKVM firmware version string.
func (p *Provider) GetBMCVersion(ctx context.Context) (string, error) {
	if err := p.ensureConnected(ctx); err != nil {
		return "", fmt.Errorf("failed to connect to JetKVM: %w", err)
	}
	version, err := p.c.GetLocalVersion(ctx)
	if err != nil {
		return "", err
	}
	return version.AppVersion, nil
}
