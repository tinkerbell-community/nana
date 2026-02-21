package jetkvm

import (
	"context"
	"fmt"

	"github.com/jetkvm/cloud-api/mgmt-api/pkg/providers"
)

// MountMedia mounts an image from a URL. Kind is "cdrom" or "floppy".
func (p *Provider) MountMedia(ctx context.Context, url, kind string) error {
	if err := p.ensureConnected(ctx); err != nil {
		return fmt.Errorf("failed to connect to JetKVM: %w", err)
	}
	if kind == "" {
		kind = "cdrom"
	}
	return p.kvmClient.MountWithHTTP(ctx, url, kind)
}

// UnmountMedia unmounts any currently mounted virtual media.
func (p *Provider) UnmountMedia(ctx context.Context) error {
	if err := p.ensureConnected(ctx); err != nil {
		return fmt.Errorf("failed to connect to JetKVM: %w", err)
	}
	return p.kvmClient.UnmountImage(ctx)
}

// GetMediaState returns the current virtual media state.
func (p *Provider) GetMediaState(ctx context.Context) (*providers.VirtualMediaState, error) {
	if err := p.ensureConnected(ctx); err != nil {
		return nil, fmt.Errorf("failed to connect to JetKVM: %w", err)
	}
	state, err := p.kvmClient.GetVirtualMediaState(ctx)
	if err != nil {
		return nil, err
	}
	return &providers.VirtualMediaState{
		Inserted: state.URL != "",
		Image:    state.URL,
		Kind:     state.Mode,
	}, nil
}
