package daemon

import (
	"log/slog"

	"github.com/charlie0129/batt/pkg/compatibility"
	"github.com/charlie0129/batt/pkg/smc"
)

// isManagedChargeControl reports whether Apple, not batt, decides when to
// charge. The firmware and native backends only need the configured limit to
// be kept in sync; they never toggle charging themselves.
func (d *Daemon) isManagedChargeControl() bool {
	switch d.getCapabilities().ChargeControlMode {
	case compatibility.ChargeControlFirmware, compatibility.ChargeControlNative:
		return true
	default:
		return false
	}
}

// ensureManagedChargeLimit applies the configured limits to the active
// Apple-managed backend and reports whether anything had to be written.
func (d *Daemon) ensureManagedChargeLimit(lower, upper int) (bool, error) {
	switch d.getCapabilities().ChargeControlMode {
	case compatibility.ChargeControlFirmware:
		return d.smc.EnsureFirmwareChargeLimit(lower, upper)
	case compatibility.ChargeControlNative:
		return d.ensureNativeChargeLimit(upper)
	default:
		return false, smc.ErrNoChargingCapability
	}
}

// ensureManagedChargeLimitDisabled turns the Apple-managed limit off and
// reports whether it was on.
func (d *Daemon) ensureManagedChargeLimitDisabled() (bool, error) {
	switch d.getCapabilities().ChargeControlMode {
	case compatibility.ChargeControlFirmware:
		return d.smc.EnsureFirmwareChargeLimitDisabled()
	case compatibility.ChargeControlNative:
		return d.ensureNativeChargeLimitDisabled()
	default:
		return false, smc.ErrNoChargingCapability
	}
}

// ensureNativeChargeLimit keeps the macOS charge limit at the configured
// upper limit. Older native backends only offer a fixed set of slider values;
// macOS 27's ceiling policy accepts exact percentages. The supported-value
// check is a safety net that never lowers the user's requested target.
func (d *Daemon) ensureNativeChargeLimit(upper int) (bool, error) {
	caps := d.getCapabilities()
	if !caps.SupportsLimit(upper) {
		snapped := caps.NearestSupportedLimit(upper)
		slog.Debug("charge limit is not offered by macOS, using the next supported limit", "configured", upper, "limit", snapped)
		upper = snapped
	}
	if upper >= 100 {
		return d.ensureNativeChargeLimitDisabled()
	}

	current, enabled, err := d.native.Limit()
	if err != nil {
		return false, err
	}
	if enabled && current == upper {
		return false, nil
	}
	if err := d.native.SetLimit(upper); err != nil {
		return false, err
	}
	return true, nil
}

func (d *Daemon) ensureNativeChargeLimitDisabled() (bool, error) {
	_, enabled, err := d.native.Limit()
	if err != nil {
		return false, err
	}
	if !enabled {
		return false, nil
	}
	return enabled, d.native.Disable()
}

// resetChargeControl restores the platform's default charging behavior. It is
// used when the daemon exits.
func (d *Daemon) resetChargeControl() error {
	switch d.getCapabilities().ChargeControlMode {
	case compatibility.ChargeControlNative:
		_, err := d.ensureNativeChargeLimitDisabled()
		return err
	case compatibility.ChargeControlAdapter:
		// Restore wall power so the Mac is not left running from the battery.
		return d.enableAdapter()
	default:
		return d.smc.ResetChargeControl()
	}
}

// stopChargeControl waits for an in-flight maintenance pass, prevents queued
// passes from reapplying the limit, then releases charging for daemon shutdown.
func (d *Daemon) stopChargeControl() error {
	d.transitionMu.Lock()
	defer d.transitionMu.Unlock()
	d.loopMu.Lock()
	defer d.loopMu.Unlock()
	d.chargeControlStopped = true
	if !d.getCapabilities().ChargingControl {
		return nil
	}
	return d.resetChargeControl()
}
