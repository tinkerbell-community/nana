package jetkvm

import (
	"context"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"regexp"
	"strings"
	"time"
)

// bootMacroTimeout is the maximum time allowed for background boot macro tasks
// (device-ready wait, keyboard macros) that run after power-on + WoL complete.
const bootMacroTimeout = 5 * time.Minute

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
// The power command and Wake-on-LAN (for "on") run synchronously within the
// caller's context — WoL must complete before the response is sent because it
// is the only mechanism to power on the NUCs. Boot macros, which require
// waiting for the BIOS/UEFI, run in a background goroutine with a separate timeout.
func (p *Provider) SetPowerState(ctx context.Context, state string) error {
	if err := p.ensureConnected(ctx); err != nil {
		return fmt.Errorf("failed to connect to JetKVM: %w", err)
	}

	// Send the power command using the caller's context (fast operation).
	if err := p.c.SendPowerAction(ctx, state); err != nil {
		return err
	}

	// Synchronous: poll device readiness and send WoL packets on each interval
	// until the device boots or the attempt limit is reached.
	if state == "on" {
		p.sendWakeOnLanUntilReady(ctx)
	}

	// Background: boot macros need to wait for BIOS, so they run with
	// a dedicated timeout that outlives the request context.
	if p.hasQueuedTasks(state) {
		p.bgWg.Add(1)
		go func() {
			defer p.bgWg.Done()
			p.runBootMacros(state)
		}()
	}

	return nil
}

// runBootMacros waits for the device to be ready and executes queued boot
// macros. It runs in a background goroutine with its own timeout since
// waiting for BIOS/UEFI can take minutes.
func (p *Provider) runBootMacros(state string) {
	ctx, cancel := context.WithTimeout(context.Background(), bootMacroTimeout)
	defer cancel()

	if err := p.ensureConnected(ctx); err != nil {
		p.logger.Warn("failed to connect for boot macros",
			slog.String("host", p.host),
			slog.String("error", err.Error()),
		)
		return
	}

	p.waitForDeviceReady(ctx)
	p.drainQueue(ctx, state)
}

// macSeparator matches common MAC address separator characters.
var macSeparator = regexp.MustCompile(`[:\-.]`)

// buildMagicPacket constructs a Wake-on-LAN magic packet for the given MAC address.
// The packet is 102 bytes: 6×0xFF followed by the 6-byte MAC repeated 16 times.
func buildMagicPacket(mac string) ([]byte, error) {
	clean := macSeparator.ReplaceAllString(strings.ToLower(mac), "")
	if len(clean) != 12 {
		return nil, fmt.Errorf("invalid MAC address: %q", mac)
	}
	hw, err := hex.DecodeString(clean)
	if err != nil {
		return nil, fmt.Errorf("invalid MAC address %q: %w", mac, err)
	}

	pkt := make([]byte, 6+16*6)
	for i := range 6 {
		pkt[i] = 0xFF
	}
	for i := range 16 {
		copy(pkt[6+i*6:], hw)
	}
	return pkt, nil
}

// sendWakeOnLan broadcasts a Wake-on-LAN magic packet directly from nana via UDP.
// This avoids relying on the JetKVM device to relay the packet.
func (p *Provider) sendWakeOnLan(ctx context.Context) error {
	if p.mac == "" {
		p.logger.Warn("skipping WoL: no MAC address configured",
			slog.String("host", p.host),
		)
		return nil
	}

	pkt, err := buildMagicPacket(p.mac)
	if err != nil {
		return err
	}

	p.logger.Info("sending WoL magic packet",
		slog.String("host", p.host),
		slog.String("mac", p.mac),
	)

	// Use a UDP connection so that ctx cancellation is honoured via deadline.
	var d net.Dialer
	conn, err := d.DialContext(ctx, "udp", "255.255.255.255:9")
	if err != nil {
		return fmt.Errorf("WoL dial: %w", err)
	}
	defer conn.Close() //nolint:errcheck

	if deadline, ok := ctx.Deadline(); ok {
		conn.SetDeadline(deadline) //nolint:errcheck
	}

	if _, err := conn.Write(pkt); err != nil {
		return fmt.Errorf("WOL packet to %s: %w", p.mac, err)
	}

	p.logger.Info("sent WoL magic packet",
		slog.String("host", p.host),
		slog.String("mac", p.mac),
	)

	return nil
}

// sendWakeOnLanUntilReady sends WoL packets on p.wolInterval until checkDeviceReady
// returns true or p.wolMaxAttempts is exhausted. It is called synchronously so
// that the API response is only sent after the device has come up (or we give up).
func (p *Provider) sendWakeOnLanUntilReady(ctx context.Context) {
	if p.mac == "" {
		p.logger.Warn("skipping WoL: no MAC address configured",
			slog.String("host", p.host),
		)
		return
	}

	for attempt := range p.wolMaxAttempts {
		if err := p.sendWakeOnLan(ctx); err != nil {
			p.logger.Warn("WoL packet failed",
				slog.String("host", p.host),
				slog.Int("attempt", attempt+1),
				slog.String("error", err.Error()),
			)
		}

		videoReady, usbReady := p.checkDeviceReady(ctx)
		if videoReady && usbReady {
			p.logger.Info("device ready after WoL",
				slog.String("host", p.host),
				slog.Int("attempts", attempt+1),
			)
			return
		}

		if attempt < p.wolMaxAttempts-1 {
			select {
			case <-time.After(p.wolInterval):
			case <-ctx.Done():
				return
			}
		}
	}

	p.logger.Warn("device not ready after max WoL attempts",
		slog.String("host", p.host),
		slog.Int("maxAttempts", p.wolMaxAttempts),
	)
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
