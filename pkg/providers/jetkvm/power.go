package jetkvm

import "context"

// GetPowerState returns the current power state of the JetKVM-managed device.
func (p *Provider) GetPowerState(ctx context.Context) (string, error) {
	state, err := p.kvmClient.GetPowerState(ctx)
	if err != nil {
		return "", err
	}
	return state.String(), nil
}

// SetPowerState sets the power state. Valid values: "on", "off", "cycle", "reset".
func (p *Provider) SetPowerState(ctx context.Context, state string) error {
	return p.kvmClient.SetPowerState(ctx, state)
}
