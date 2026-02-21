package jetkvm

import (
	"context"

	"github.com/jetkvm/cloud-api/mgmt-api/pkg/providers"
)

// MountMedia mounts an image from a URL. Kind is "cdrom" or "floppy".
func (p *Provider) MountMedia(ctx context.Context, url, kind string) error {
	if kind == "" {
		kind = "cdrom"
	}
	return p.kvmClient.MountWithHTTP(ctx, url, kind)
}

// UnmountMedia unmounts any currently mounted virtual media.
func (p *Provider) UnmountMedia(ctx context.Context) error {
	return p.kvmClient.UnmountImage(ctx)
}

// GetMediaState returns the current virtual media state.
func (p *Provider) GetMediaState(ctx context.Context) (*providers.VirtualMediaState, error) {
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
