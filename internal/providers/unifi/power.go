package unifi

import (
	"context"
	"fmt"
	"strings"
)

// GetPowerState returns the current PoE power state of the device's switch port.
func (p *Provider) GetPowerState(ctx context.Context) (string, error) {
	output, portID, err := p.executeOnSwitch(ctx, func(port int) string {
		return fmt.Sprintf("swctrl poe show id %d", port)
	})
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
	var cmdFmt string
	switch state {
	case "on":
		cmdFmt = "swctrl poe set auto id %d"
	case "off":
		cmdFmt = "swctrl poe set off id %d"
	case "cycle", "reset":
		cmdFmt = "swctrl poe restart id %d"
	default:
		return fmt.Errorf("unsupported power state: %s", state)
	}

	_, _, err := p.executeOnSwitch(ctx, func(port int) string {
		return fmt.Sprintf(cmdFmt, port)
	})
	if err != nil {
		return fmt.Errorf("failed to set power state: %w", err)
	}

	return nil
}
