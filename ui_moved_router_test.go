package main

import (
	"strings"
	"testing"
)

// ui_moved_router_test.go covers what the PAGE must do when the router answers at
// a different address than the one the wizard holds — the operator-visible half of
// the 2026-10-07 report:
//
//	XHR POST /api/wifi-scan [HTTP/1.1 502 Bad Gateway] — after flashing pre24 the
//	router had renumbered itself to 192.168.1.1 while the wizard still held
//	10.60.32.1.
//
// The server half (rediscover.go) names the address the router moved to. That is
// worthless if the page then appends "— check router password and try Rescan.",
// which is what it did for every non-host-key failure: the one hint it offered
// pointed at the credential, and the router was answering a LAN away. These
// assertions are on the page's own logic, so they fail if that fallback creeps
// back into the moved-address branch.

// TestScanSendsTheMACSoTheServerCanIdentifyTheRouter: without the MAC the server
// cannot tell "YOUR router, renumbered" from "some router on the LAN", and
// movedRouterMessage refuses to guess in that case — so the recovery would never
// trigger. The UI already holds the MAC in the discovery list it rendered.
func TestScanSendsTheMACSoTheServerCanIdentifyTheRouter(t *testing.T) {
	body := jsFuncBody(t, string(indexHTML), "async function wifiScan(")
	if !strings.Contains(body, "mac:") || !strings.Contains(body, "selectedRouterInfo()") {
		t.Errorf("the WiFi-scan request does not carry the selected router's MAC:\n%s", body)
	}
}

// TestScanOffersTheMovedAddressInsteadOfBlamingThePassword: the moved-address
// response must be handled BEFORE the generic fallback, and the fallback's
// password sentence must stay out of it.
func TestScanOffersTheMovedAddressInsteadOfBlamingThePassword(t *testing.T) {
	body := jsFuncBody(t, string(indexHTML), "async function wifiScan(")

	suggested := strings.Index(body, "data.suggestedIp")
	if suggested < 0 {
		t.Fatalf("the scan never looks at data.suggestedIp, so the address the server found is dropped on the floor:\n%s", body)
	}
	passwordHint := strings.Index(body, "check router password")
	if passwordHint < 0 {
		t.Fatalf("the generic password hint disappeared — this test can no longer tell whether it was kept out of the moved-address branch")
	}
	if suggested > passwordHint {
		t.Errorf("the moved-address branch sits AFTER the generic password fallback, so it can never be reached:\n%s", body)
	}
	if !strings.Contains(body, "showMovedRouterHint(") {
		t.Errorf("the moved-address branch does not render the recovery:\n%s", body)
	}
}

// TestMovedRouterHintIsRecoverableAndHonest: the hint must name the address with
// textContent (never innerHTML — the address is server-derived data), re-list the
// routers rather than silently switching the target, and only select the address
// when the fresh scan actually lists it.
func TestMovedRouterHintIsRecoverableAndHonest(t *testing.T) {
	body := jsFuncBody(t, string(indexHTML), "function showMovedRouterHint(")

	if !strings.Contains(body, "textContent") {
		t.Errorf("the hint does not write the address with textContent:\n%s", body)
	}
	if strings.Contains(body, "innerHTML") {
		t.Errorf("the hint interpolates into innerHTML; server-derived text must go through textContent/createTextNode:\n%s", body)
	}
	if !strings.Contains(body, "scan()") {
		t.Errorf("the hint offers no way to re-list the routers, so the operator cannot act on it:\n%s", body)
	}
	if !strings.Contains(body, "router-select") {
		t.Errorf("the hint never reaches the router selector, so the suggested address can never become the target:\n%s", body)
	}
	if !strings.Contains(body, "matching") {
		t.Errorf("the hint selects the address without checking the fresh scan lists it — a suggestion from a stale scan must not silently become the deploy target:\n%s", body)
	}
}
