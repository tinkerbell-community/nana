package jetkvm

import "context"

// BootDeviceSet is a no-op here.
func (p *Provider) BootDeviceSet(
	ctx context.Context,
	bootDevice string,
	setPersistent, efiBoot bool,
) (ok bool, err error) {
	p.logger.Info(
		"BootDeviceSet is not implemented for Home Assistant provider; no operation performed",
		"bootDevice",
		bootDevice,
		"setPersistent",
		setPersistent,
		"efiBoot",
		efiBoot,
	)
	return true, nil
}
