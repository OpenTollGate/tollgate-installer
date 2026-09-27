package main

import (
	"strings"
	"testing"
)

// Hermetic tests for the wifi-ENABLE step (main.go: wifiEnableCmd,
// enableWifiPoll, wifiInterfaceReady, parseWirelessStatus). No hardware: the
// box is modelled as a state machine, because the defect IS a state transition
// that the pre-fix code failed to trigger.
//
// Hardware transcript these fixtures encode (freshly flashed stock OpenWrt
// 25.12.5, GL-MT3000, mediatek/filogic — measured 2026-09-27, quoted from the
// operator's session, not re-derived here):
//
//	config wifi-device 'radio0'          option disabled '0'
//	config wifi-iface 'default_radio0'   option ssid 'OpenWrt'
//	                                     option disabled '1'
//	(same for radio1/default_radio1)
//
//	before: `ubus call network.wireless status` → both radios "up": true,
//	        "interfaces": []  AND  `iw dev` prints NOTHING (no SSID broadcast)
//	after : only `uci set wireless.default_radio{0,1}.disabled='0'; uci commit
//	        wireless; wifi up` → phy0-ap0 (ch 1) + phy1-ap0 (ch 36), ubus
//	        interfaces non-empty, `iwinfo phy0-ap0 scan` returns real cells
//	        (e.g. ESSID: "Vodafone-823182")

const (
	// ifaceEnableMarker appears ONLY in the fixed step's wifi-iface loop; the
	// tests use it to detect whether the interfaces were enabled at all.
	ifaceEnableMarker = "=wifi-iface$"

	// stockUbusRadiosUpNoVaps — BEFORE: radios up, ZERO interfaces. This is
	// the state the pre-fix poll accepted as "wifi is ready".
	stockUbusRadiosUpNoVaps = `{"radio0":{"up":true,"pending":false,"disabled":false,"interfaces":[]},` +
		`"radio1":{"up":true,"pending":false,"disabled":false,"interfaces":[]}}`

	// stockUbusRadiosUpWithVaps — AFTER the iface-enable step: same radios, now
	// one VAP each (the measured phy0-ap0 / phy1-ap0).
	stockUbusRadiosUpWithVaps = `{"radio0":{"up":true,"pending":false,"disabled":false,"interfaces":[` +
		`{"section":"default_radio0","ifname":"phy0-ap0","config":{"mode":"ap","ssid":"OpenWrt","disabled":false}}]},` +
		`"radio1":{"up":true,"pending":false,"disabled":false,"interfaces":[` +
		`{"section":"default_radio1","ifname":"phy1-ap0","config":{"mode":"ap","ssid":"OpenWrt","disabled":false}}]}}`
)

// stockBoxRouter models the box's STATE TRANSITION, not two canned answers:
// before the wifi-iface enable runs there is no VAP and `iw dev` is empty; the
// moment that command is issued the box exposes phy0-ap0/phy1-ap1. A static
// fixture would make the pre-fix logic look healthy — which is precisely the
// defect under test.
func stockBoxRouter() (*fakeRouter, scanRunner) {
	r := &fakeRouter{}
	return r, func(cmd string) string {
		r.run(cmd) // record every command
		ifacesEnabled := r.calledWith(ifaceEnableMarker)
		switch {
		case strings.Contains(cmd, "ubus call network.wireless status"):
			if ifacesEnabled {
				return stockUbusRadiosUpWithVaps
			}
			return stockUbusRadiosUpNoVaps
		case strings.Contains(cmd, "iw dev 2>&1"):
			if ifacesEnabled {
				return iwDevMainline
			}
			return "" // stock: `iw dev` prints NOTHING — no wireless netdev exists
		}
		return ""
	}
}

// TestWifiEnableStepEnablesInterfacesOnStockBox is the regression test for the
// field defect: on a stock image whose radios are already enabled and whose AP
// ifaces are disabled='1', the step must enable the IFACE sections too, and the
// wait must then see a real interface.
func TestWifiEnableStepEnablesInterfacesOnStockBox(t *testing.T) {
	r, run := stockBoxRouter()

	if reason := enableWifiPoll(run, 1, 0); reason != "" {
		t.Fatalf("enableWifiPoll reported failure on the stock box: %s", reason)
	}

	// Both halves of the UCI step must be present.
	for _, want := range []string{"=wifi-device$", ifaceEnableMarker, "uci commit wireless", "wifi up"} {
		if !r.calledWith(want) {
			t.Errorf("the enable step never issued %q; calls = %q", want, r.calls)
		}
	}
	// ...and a station iface must be left alone: an upstream connection is not
	// ours to (re)start during a scan.
	if !r.calledWith(`[ "$(uci -q get wireless.$i.mode)" = "sta" ]`) {
		t.Errorf("the enable step enables wifi-iface sections with no `mode sta` exclusion; calls = %q", r.calls)
	}

	// The wait must have accepted the post-enable state on an INTERFACE, not on
	// a radio flag.
	ready, detail := wifiInterfaceReady(run)
	if !ready {
		t.Fatalf("wifiInterfaceReady = false after the enable step; detail = %s", detail)
	}
	if !strings.Contains(detail, "phy0-ap0") {
		t.Errorf("detail does not name the created interface: %s", detail)
	}
}

// TestWifiEnableWaitRejectsRadiosUpWithoutInterface is the non-vacuity CONTROL:
// it pins the exact state that made the old code report success while the box
// had nothing to scan with — and proves the NEW predicate rejects it.
//
// If this ever stops failing at the `ready` check, the fix has been weakened
// back to a radio-level poll.
func TestWifiEnableWaitRejectsRadiosUpWithoutInterface(t *testing.T) {
	_, run := stockBoxRouter()

	// The fixture really is the operator's box: radios up, zero interfaces.
	before := run("ubus call network.wireless status 2>/dev/null")
	if !allRadiosUp(before) {
		t.Fatal("fixture is stale: the stock before-state no longer satisfies the pre-fix predicate")
	}
	if n := len(iwInterfaceNames(run("iw dev 2>&1"))); n != 0 {
		t.Fatalf("fixture is stale: `iw dev` lists %d interfaces on the stock box, want 0", n)
	}

	// So: `"up": true` is satisfied while NO interface exists. The old poll
	// cannot tell that apart from readiness; the new one must.
	ready, detail := wifiInterfaceReady(run)
	if ready {
		t.Fatalf("the new wait accepted a box with ZERO wireless interfaces (radios up=true) — detail = %s", detail)
	}
	if !strings.Contains(detail, "radio0 up=true ifaces=0") || !strings.Contains(detail, "radio1 up=true ifaces=0") {
		t.Errorf("detail does not expose the radios-up-but-no-VAP state: %s", detail)
	}
	if !strings.Contains(detail, "iw dev: -") {
		t.Errorf("detail does not report that `iw dev` listed nothing: %s", detail)
	}
}

// TestLegacyWifiEnableStepDeclaresSuccessWithoutInterface drives the PRE-FIX
// step through the SAME fixture. It must declare success (that is the bug) and
// must never touch the iface sections — the mechanical proof that the control
// fixture still defeats the old logic.
func TestLegacyWifiEnableStepDeclaresSuccessWithoutInterface(t *testing.T) {
	r, run := stockBoxRouter()

	if reason := legacyEnableWifiAndWaitWith(run); reason != "" {
		t.Fatalf("the pre-fix step returned failure %q — it is supposed to declare success vacuously", reason)
	}
	if r.calledWith(ifaceEnableMarker) {
		t.Fatalf("the pre-fix step is not the pre-fix step: it enabled wifi-iface sections; calls = %q", r.calls)
	}
	// The box is still unusable, which is what the operator saw.
	if ready, detail := wifiInterfaceReady(run); ready {
		t.Fatalf("the stock box looks ready although nothing enabled its ifaces; detail = %s", detail)
	}
}

// TestWifiEnableReportsReasonWhenNoInterfaceAppears covers the honest-failure
// direction: when the timeout expires without an interface, the step must say
// so and say what it saw — never proceed silently, which is how "radios up, no
// VAP" was misread as an empty airspace.
func TestWifiEnableReportsReasonWhenNoInterfaceAppears(t *testing.T) {
	// A box whose radios come up but whose driver never creates a VAP.
	run := func(cmd string) string {
		switch {
		case strings.Contains(cmd, "ubus call network.wireless status"):
			return stockUbusRadiosUpNoVaps
		case strings.Contains(cmd, "iw dev 2>&1"):
			return ""
		}
		return ""
	}

	reason := enableWifiPoll(run, 2, 0)
	if reason == "" {
		t.Fatal("enableWifiPoll returned success on a box with zero wireless interfaces")
	}
	for _, want := range []string{
		"no wireless interface",
		"radio0 up=true ifaces=0",
		"radio1 up=true ifaces=0",
		"ubus VAPs=0",
		"so no scan can run",
	} {
		if !strings.Contains(reason, want) {
			t.Errorf("reason = %q, want it to contain %q", reason, want)
		}
	}
}

// TestParseWirelessStatus covers the parser the readiness predicate rests on.
// The `interfaces` ARRAY (not the `up` flag) is the field that decides.
func TestParseWirelessStatus(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantVAP int
		wantOK  bool
	}{
		{"stock box: radios up, no VAPs", stockUbusRadiosUpNoVaps, 0, true},
		{"after enabling the ifaces", stockUbusRadiosUpWithVaps, 2, true},
		{"one radio up, one pending", `{"radio0":{"up":true,"interfaces":[{}]},"radio1":{"up":false,"pending":true,"interfaces":[]}}`, 1, true},
		{"empty object", `{}`, 0, false},
		{"empty output", "", 0, false},
		{"ubus refusal", "Failed to parse message", 0, false},
		{"array not object", `["radio0"]`, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			radios, ok := parseWirelessStatus(tc.in)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			vaps := 0
			for _, r := range radios {
				vaps += r.interfaces
			}
			if vaps != tc.wantVAP {
				t.Errorf("VAPs = %d, want %d", vaps, tc.wantVAP)
			}
		})
	}

	// Radio order is stable (name-sorted), so the reason string is reproducible.
	radios, _ := parseWirelessStatus(stockUbusRadiosUpWithVaps)
	if len(radios) != 2 || radios[0].name != "radio0" || radios[1].name != "radio1" {
		t.Errorf("radios = %+v, want radio0 then radio1", radios)
	}
}

// legacyWifiEnableCmd is the PRE-FIX UCI step, verbatim (main.go @ b406eeb):
// wifi-DEVICE sections only.
const legacyWifiEnableCmd = `for r in $(uci -q show wireless 2>/dev/null | sed -n "s/^wireless\.\([^.]*\)=wifi-device$/\1/p"); do uci -q set wireless.$r.disabled='0'; done` +
	` && uci commit wireless` +
	` && (wifi up 2>/dev/null || wifi 2>/dev/null || true)`

// legacyEnableWifiAndWaitWith is the PRE-FIX enable-and-wait, kept in the test
// file ONLY as the control's oracle (same convention as legacyScanChain). The
// command string is verbatim; the loop is the same logic minus the production
// sleeps.
func legacyEnableWifiAndWaitWith(run scanRunner) string {
	run(legacyWifiEnableCmd)
	for i := 0; i < 3; i++ {
		if allRadiosUp(run("ubus call network.wireless status 2>/dev/null")) {
			return ""
		}
	}
	return "no radio came up"
}
