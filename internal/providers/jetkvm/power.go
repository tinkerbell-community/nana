package jetkvm

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// postPowerOnTimeout is the maximum time allowed for post-power-on tasks
// (WoL, device-ready wait, boot macros) that run in the background.
const postPowerOnTimeout = 5 * time.Minute

// GetPowerState returns the current power state of the JetKVM-managed device.
func (p *Provider) GetPowerState(ctx context.Context) (string, error) {
	if err := p.ensureConnected(ctx); err != nil {
		return "", fmt.Errorf("failed to connect to JetKVM: %w", err)
	}
	state, err := p.c.GetPowerState(ctx)
	if err != nil {
		return "", err
	}
	return state.String(), nil
}

// SetPowerState sets the power state. Valid values: "on", "off", "cycle", "reset".
// The power command is sent synchronously. Post-power-on tasks (WoL, device-ready
// wait, boot macros) run in a background goroutine with a separate timeout so they
// don't block the caller's context.
func (p *Provider) SetPowerState(ctx context.Context, state string) error {
	if err := p.ensureConnected(ctx); err != nil {
		return fmt.Errorf("failed to connect to JetKVM: %w", err)
	}

	// Send the power command using the caller's context (fast operation).
	if err := p.c.SendPowerAction(ctx, state); err != nil {
		return err
	}

	// Run post-power-on tasks in the background with a dedicated timeout.
	hasWoL := state == "on"
	hasQueued := p.hasQueuedTasks(state)

	if hasWoL || hasQueued {
		p.bgWg.Add(1)
		go func() {
			defer p.bgWg.Done()
			p.postPowerOnTasks(state, hasQueued)
		}()
	}

	return nil
}

// postPowerOnTasks handles WoL, device-ready waiting, and boot macro execution
// after a power state change. It runs with its own timeout context.
func (p *Provider) postPowerOnTasks(state string, hasQueued bool) {
	ctx, cancel := context.WithTimeout(context.Background(), postPowerOnTimeout)
	defer cancel()

	if err := p.ensureConnected(ctx); err != nil {
		p.logger.Warn("failed to connect for post-power-on tasks",
			slog.String("host", p.host),
			slog.String("error", err.Error()),
		)
		return
	}

	if state == "on" {
		if err := p.sendWakeOnLan(ctx); err != nil {
			p.logger.Warn("wake-on-LAN failed",
				slog.String("host", p.host),
				slog.String("error", err.Error()),
			)
		}
	}

	if hasQueued {
		p.waitForDeviceReady(ctx)
		p.drainQueue(ctx, state)
	}
}

// sendWakeOnLan retrieves configured WOL devices and sends magic packets for each.
// All send errors are collected and returned as a combined error.
func (p *Provider) sendWakeOnLan(ctx context.Context) error {
	devices, err := p.c.GetWakeOnLanDevices(ctx)
	if err != nil {
		return fmt.Errorf("failed to get WOL devices: %w", err)
	}

	var errs []error
	for _, dev := range devices {
		if err := p.c.SendWOLMagicPacket(ctx, dev.MacAddress); err != nil {
			p.logger.Warn("failed to send WOL packet",
				slog.String("host", p.host),
				slog.String("mac", dev.MacAddress),
				slog.String("name", dev.Name),
				slog.String("error", err.Error()),
			)
			errs = append(
				errs,
				fmt.Errorf("WOL packet to %s (%s): %w", dev.Name, dev.MacAddress, err),
			)
		} else {
			p.logger.Info("sent WOL magic packet",
				slog.String("host", p.host),
				slog.String("mac", dev.MacAddress),
				slog.String("name", dev.Name),
			)
		}
	}

	return errors.Join(errs...)
}

// hasQueuedTasks reports whether any tasks are queued for the given power state.
func (p *Provider) hasQueuedTasks(state string) bool {
	p.queueMu.Lock()
	defer p.queueMu.Unlock()
	return len(p.queue[state]) > 0
}

// readyPollInterval is the polling interval when waiting for device readiness.
const readyPollInterval = 1 * time.Second

// waitForDeviceReady polls the JetKVM device until video is ready and USB is
// configured, or the context deadline is reached. This ensures the managed
// machine's BIOS/UEFI is up and accepting keyboard input before we send macros.
func (p *Provider) waitForDeviceReady(ctx context.Context) {
	p.logger.Info("waiting for display and USB to become ready",
		slog.String("host", p.host),
	)

	ticker := time.NewTicker(readyPollInterval)
	defer ticker.Stop()

	for {
		videoReady, usbReady := p.checkDeviceReady(ctx)

		if videoReady && usbReady {
			p.logger.Info("display and USB are ready",
				slog.String("host", p.host),
			)
			return
		}

		select {
		case <-ctx.Done():
			p.logger.Warn("timed out waiting for device readiness, proceeding anyway",
				slog.String("host", p.host),
				slog.Bool("videoReady", videoReady),
				slog.Bool("usbReady", usbReady),
			)
			return
		case <-ticker.C:
		}
	}
}

// checkDeviceReady returns the current video and USB readiness state.
func (p *Provider) checkDeviceReady(ctx context.Context) (videoReady, usbReady bool) {
	vs, err := p.c.GetVideoState(ctx)
	if err != nil {
		p.logger.Debug("failed to get video state",
			slog.String("host", p.host),
			slog.String("error", err.Error()),
		)
	} else {
		videoReady = vs.Ready
	}

	usbState, err := p.c.GetUSBState(ctx)
	if err != nil {
		p.logger.Debug("failed to get USB state",
			slog.String("host", p.host),
			slog.String("error", err.Error()),
		)
	} else {
		usbReady = usbState == "configured"
	}

	if !videoReady || !usbReady {
		p.logger.Debug("device not ready yet",
			slog.String("host", p.host),
			slog.Bool("videoReady", videoReady),
			slog.Bool("usbReady", usbReady),
		)
	}

	return videoReady, usbReady
}
