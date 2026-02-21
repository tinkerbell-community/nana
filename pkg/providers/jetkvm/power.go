package jetkvm

import (
	"context"
	"log/slog"
)

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
	if err := p.kvmClient.SetPowerState(ctx, state); err != nil {
		return err
	}

	if state == "on" {
		p.sendWakeOnLan(ctx)
	}

	return nil
}

// sendWakeOnLan retrieves configured WOL devices and sends magic packets for each.
func (p *Provider) sendWakeOnLan(ctx context.Context) {
	devices, err := p.kvmClient.GetWakeOnLanDevices(ctx)
	if err != nil {
		p.logger.Warn(
			"failed to get WOL devices",
			slog.String("host", p.host),
			slog.String("error", err.Error()),
		)
		return
	}

	for _, dev := range devices {
		if err := p.kvmClient.SendWOLMagicPacket(ctx, dev.MacAddress); err != nil {
			p.logger.Warn("failed to send WOL packet",
				slog.String("host", p.host),
				slog.String("mac", dev.MacAddress),
				slog.String("name", dev.Name),
				slog.String("error", err.Error()),
			)
		} else {
			p.logger.Info("sent WOL magic packet",
				slog.String("host", p.host),
				slog.String("mac", dev.MacAddress),
				slog.String("name", dev.Name),
			)
		}
	}
}
