package jetkvm

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/tinkerbell-community/nana/internal/providers/jetkvm/client"
)

// SetBootDevice queues a keyboard macro to select the requested boot device.
// The macro runs after the next power-on transition completes.
func (p *Provider) SetBootDevice(
	ctx context.Context,
	bootDevice string,
	setPersistent, efiBoot bool,
) error {
	bc, ok := p.bootDevices[bootDevice]
	if !ok {
		p.logger.Warn("no boot device config found, ignoring",
			slog.String("host", p.host),
			slog.String("bootDevice", bootDevice),
		)
		return nil
	}

	p.logger.Info("queuing boot device macro",
		slog.String("host", p.host),
		slog.String("bootDevice", bootDevice),
		slog.Int("steps", len(bc.Steps)),
	)

	// Build the keyboard macro steps from the boot device config.
	steps := make([]client.KeyboardMacroStep, len(bc.Steps))
	for i, s := range bc.Steps {
		steps[i] = client.KeyboardMacroStep{
			Keys:      s.Keys,
			Modifiers: s.Modifiers,
			Delay:     int(s.Delay / time.Millisecond),
		}
	}

	initialDelay := bc.Delay

	// Enqueue the macro execution to run after the "on" power state transition.
	p.enqueueTask("on", func(ctx context.Context) error {
		if initialDelay > 0 {
			p.logger.Info("waiting before boot device macro",
				slog.String("host", p.host),
				slog.String("bootDevice", bootDevice),
				slog.Duration("delay", initialDelay),
			)
			select {
			case <-time.After(initialDelay):
			case <-ctx.Done():
				return fmt.Errorf("boot device macro cancelled: %w", ctx.Err())
			}
		}

		if err := p.ensureConnected(ctx); err != nil {
			return fmt.Errorf("failed to connect for boot device macro: %w", err)
		}

		p.logger.Info("executing boot device macro",
			slog.String("host", p.host),
			slog.String("bootDevice", bootDevice),
		)
		return p.c.ExecuteKeyboardMacro(ctx, steps)
	})

	return nil
}
