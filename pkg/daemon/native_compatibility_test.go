package daemon

import (
	"errors"
	"net/http"
	"slices"
	"strconv"
	"testing"

	"github.com/charlie0129/batt/pkg/compatibility"
)

func TestNativeChargeNowReappliesTargetOnce(t *testing.T) {
	for _, target := range []int{67, 99} {
		t.Run(strconv.Itoa(target), func(t *testing.T) {
			f := newExactNativeFixture(t, withCharge(target-1))
			f.file.SetUpperLimit(target)
			lower := f.file.LowerLimit()
			f.native.limit, f.native.enabled = target, true
			response := f.post(chargeOnceLimitPath)
			want := []int{target}
			if response.Code != http.StatusCreated || f.file.ChargeOnceTarget() != target ||
				!slices.Equal(f.native.setCalls, want) || f.native.limit != target || !f.native.enabled {
				t.Fatalf("native reapply failed: %d %s native=%+v", response.Code, response.Body.String(), f.native)
			}
			reloaded := f.reloadFile()
			if reloaded.UpperLimit() != target || reloaded.LowerLimit() != lower || reloaded.ChargeOnceTarget() != target {
				t.Fatal("native reapply changed the saved charge limits")
			}
			if !f.d.maintainLoopForced() || !slices.Equal(f.native.setCalls, want) {
				t.Fatal("maintenance reapplied an unchanged target", f.native.setCalls)
			}
			f.smc.setCharge(t, target)
			if !f.d.completeChargeOnce() || !f.d.maintainLoopForced() || f.native.limit != target || !slices.Equal(f.native.setCalls, want) {
				t.Fatal("completion did not retain the original ceiling", f.native.setCalls)
			}
		})
	}
}

func TestNativeChargeNowWriteFailureRollsBackAction(t *testing.T) {
	f := newExactNativeFixture(t, withCharge(62))
	f.file.SetUpperLimit(67)
	f.native.limit, f.native.enabled, f.native.err = 67, true, errors.New("native write failed")
	response := f.post(chargeOnceLimitPath)
	if response.Code != http.StatusInternalServerError || f.file.ChargeOnceTarget() != 0 || f.file.UpperLimit() != 67 || f.native.limit != 67 || !f.native.enabled {
		t.Fatalf("failed reapply changed the target or left a running action: %d %s native=%+v", response.Code, response.Body.String(), f.native)
	}
}

func TestNativeChargeNowAfterShutdownDoesNotWrite(t *testing.T) {
	f := newExactNativeFixture(t, withCharge(62))
	f.file.SetUpperLimit(67)
	if err := f.d.stopChargeControl(); err != nil {
		t.Fatal(err)
	}
	response := f.post(chargeOnceLimitPath)
	if response.Code != http.StatusInternalServerError || len(f.native.setCalls) != 0 || f.file.ChargeOnceTarget() != 0 {
		t.Fatalf("shutdown accepted or applied a one-time charge: %d %s writes=%v", response.Code, response.Body.String(), f.native.setCalls)
	}
}

func TestExactNativeChargeOnceRestartRecovery(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target int
	}{
		{name: "charge now is reapplied", target: 55},
		{name: "charge full is preserved", target: 100},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newExactNativeFixture(t, withCharge(52))
			f.file.SetUpperLimit(55)
			f.file.SetChargeOnceTarget(tc.target)
			if err := f.file.Save(); err != nil {
				t.Fatal(err)
			}

			f.d.conf = f.reloadFile()
			if err := f.d.start(f.dir); err != nil {
				t.Fatal(err)
			}
			if f.d.conf.ChargeOnceTarget() != tc.target || f.reloadFile().ChargeOnceTarget() != tc.target {
				t.Fatal("restart discarded the saved one-time target")
			}
			if !f.d.maintainLoopForced() || f.native.enabled != (tc.target < 100) ||
				(tc.target < 100 && (f.native.limit != tc.target || !slices.Equal(f.native.setCalls, []int{tc.target}))) {
				t.Fatalf("restart did not apply the saved one-time target: target=%d native=%+v", tc.target, f.native)
			}
		})
	}
}

func TestNativeRestartDoesNotReapplyCompletedOrUnknownCharge(t *testing.T) {
	for _, tc := range []struct {
		name       string
		saveErr    error
		readErr    error
		wantTarget int
	}{
		{name: "completed"},
		{name: "save failed", saveErr: errors.New("save failed"), wantTarget: 67},
		{name: "read failed", readErr: errors.New("charge unavailable"), wantTarget: 67},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conf := newMemConfig()
			conf.SetUpperLimit(67)
			conf.SetChargeOnceTarget(67)
			conf.failSave(tc.saveErr)
			f := newExactNativeFixture(t, withConfig(conf), withCharge(67))
			f.native.limit, f.native.enabled = 67, true
			f.smc.fail("GetBatteryCharge", tc.readErr)
			f.d.resumeNativeChargeOnce()
			if conf.ChargeOnceTarget() != tc.wantTarget || len(f.native.setCalls) != 0 {
				t.Fatalf("restart target=%d, native writes=%v", conf.ChargeOnceTarget(), f.native.setCalls)
			}
		})
	}
}

func TestNativeFullChargeRestoresTargetAt100Percent(t *testing.T) {
	f := newExactNativeFixture(t, withCharge(52))
	f.file.SetUpperLimit(55)
	response := f.post("/charge-once/full")
	if response.Code != http.StatusCreated || f.file.ChargeOnceTarget() != 100 || f.native.enabled {
		t.Fatalf("full charge failed: %d %s", response.Code, response.Body.String())
	}
	f.smc.setCharge(t, 100)
	if !f.d.completeChargeOnce() || !f.d.maintainLoopForced() || !f.native.enabled || f.native.limit != 55 {
		t.Fatal("reaching 100% did not restore the configured target")
	}
}

func TestEarlierNativeBackendKeepsPreviousPaths(t *testing.T) {
	f := newFixture(t, firmwareGatedNoAdapter, withFileConfig(), withNative(&fakeNativeLimit{limit: 100}),
		withCapabilities(nativeCaps(80, 85, 90, 95, 100)))
	f.file.SetUpperLimit(80)
	if !f.d.maintainLoopForced() || f.native.limit != 80 {
		t.Fatal("earlier native backend did not use its slider target")
	}
	if response := f.put("/lower-limit-delta", "5"); response.Code != http.StatusConflict {
		t.Fatal("earlier native backend gained an unsupported lower-limit command")
	}
	f.file.SetUpperLimit(100)
	f.native.limit, f.native.enabled, f.native.registered = 100, false, true
	if _, err := f.d.ensureNativeChargeLimitDisabled(); err != nil || f.native.disables != 0 {
		t.Fatal("earlier backend entered the macOS 27 registration cleanup path")
	}
}

func TestNativeFeatureCompatibility(t *testing.T) {
	caps := compatibility.Capabilities{
		ChargingControl: true, ChargeControlMode: compatibility.ChargeControlNative,
		NativeExactTargets: true, AdapterControl: true, Calibration: true,
	}
	for feature, want := range map[compatibility.Feature]bool{
		compatibility.FeatureChargingControl: true, compatibility.FeatureLowerLimit: false,
		compatibility.FeatureAdapterControl: true, compatibility.FeatureCalibration: true,
		compatibility.FeatureSleepHooks: false, compatibility.FeatureMagSafeLED: false,
	} {
		if got := caps.Supports(feature); got != want {
			t.Errorf("%s supported=%t want=%t", feature, got, want)
		}
	}
}
