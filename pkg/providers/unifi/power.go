package unifi

import (
	"context"
	"fmt"
	"strings"
)

// GetPowerState returns the current PoE power state of the device's switch port.
func (p *Provider) GetPowerState(ctx context.Context) (string, error) {
	portID, err := p.resolvePort(ctx)
	if err != nil {
		return "", err
	}

	output, err := p.executeCommand(ctx, fmt.Sprintf("swctrl poe show id %d", portID))
	if err != nil {
		return "", fmt.Errorf("failed to get PoE status: %w", err)
	}

	status, err := parsePoEStatus(output)
	if err != nil {
		return "", fmt.Errorf("failed to parse PoE status: %w", err)
	}

	for _, port := range status.ports {
		if port.port == portID {
			switch strings.ToLower(port.poePwr) {
			case "on":
				return "on", nil
			case "off":
				return "off", nil
			default:
				return "unknown", nil
			}
		}
	}

	return "unknown", fmt.Errorf("port %d not found in PoE status", portID)
}

// SetPowerState sets the PoE power state. Valid values: "on", "off", "cycle", "reset".
func (p *Provider) SetPowerState(ctx context.Context, state string) error {
	portID, err := p.resolvePort(ctx)
	if err != nil {
		return err
	}

	var command string
	switch state {
	case "on":
		command = fmt.Sprintf("swctrl poe set auto id %d", portID)
	case "off":
		command = fmt.Sprintf("swctrl poe set off id %d", portID)
	case "cycle", "reset":
		command = fmt.Sprintf("swctrl poe restart id %d", portID)
	default:
		return fmt.Errorf("unsupported power state: %s", state)
	}

	_, err = p.executeCommand(ctx, command)
	if err != nil {
		return fmt.Errorf("failed to set power state: %w", err)
	}

	return nil
}
