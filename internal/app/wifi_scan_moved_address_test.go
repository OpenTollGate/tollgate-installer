package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The operator's case, measured 2026-10-07: the wizard held the address it had
// deployed to (10.60.32.1), the install renumbered the router to 192.168.1.1,
// and the next SSH-dependent call — the WiFi scan — answered "502 ... cannot
// connect to router via SSH — nothing is accepting SSH at 10.60.32.1:22, check
// the router's address". Every word of that is true and none of it is useful:
// the router was on the same LAN, at the address the install had just given it.
//
// A deploy that moves the management address is the COMMON cause of this
// failure, so the refusal has to name where the router actually answers.

const (
	// wantedIP is the address the wizard holds when the scan runs; the dial to
	// it is refused, which is what a renamed router looks like from here.
	wantedIP = "127.0.0.1"
	// movedIP is where the same router answers after the renumber.
	movedIP = "192.168.1.1"
	// movedMAC identifies that router across the renumber — the one signal that
	// says "this is YOUR router at a new address" rather than "some router".
	movedMAC = "94:83:c4:47:1b:ac"
)

// stubDiscovery replaces the LAN scan so the test never touches the network.
// The seam mirrors the package's existing injectable funcs (feedHTTPGet,
// wirelessRollback, persistableDiskAsset).
func stubDiscovery(t *testing.T, routers ...RouterInfo) *int {
	t.Helper()
	calls := 0
	prev := discoverRoutersFn
	discoverRoutersFn = func() []RouterInfo { calls++; return routers }
	t.Cleanup(func() { discoverRoutersFn = prev })
	return &calls
}

// refuseTheDial points every dial at loopback port 1: nothing there accepts, so
// the failure is a plain "connection refused" — a reachability class, which is
// the class that may mean the address moved. No fixture, no timeout, no host
// key involved, so the test stays fast and hermetic.
func refuseTheDial(t *testing.T) {
	t.Helper()
	prev := sshDialPort
	sshDialPort = "1"
	t.Cleanup(func() { sshDialPort = prev })
}

func postWifiScan(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/wifi-scan", bytes.NewBufferString(body))
	handleWifiScan(rec, req)
	return rec
}

func errorPayload(t *testing.T, rec *httptest.ResponseRecorder) map[string]string {
	t.Helper()
	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("the error payload must stay a JSON object (%v): %s", err, rec.Body.String())
	}
	return got
}

// TestWifiScanNamesTheAddressTheRouterMovedTo: the dial to the held address is
// refused, the same router (same MAC) answers at a new one — the response must
// say so, and carry the address in a field the UI can act on.
func TestWifiScanNamesTheAddressTheRouterMovedTo(t *testing.T) {
	refuseTheDial(t)
	stubDiscovery(t, RouterInfo{IP: movedIP, MAC: movedMAC, SSH: true})

	rec := postWifiScan(t, `{"ip":"`+wantedIP+`","password":"x","mac":"`+strings.ToUpper(movedMAC)+`"}`)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("a refused dial is still a 502 to the operator; got %d: %s", rec.Code, rec.Body.String())
	}
	got := errorPayload(t, rec)
	if got["suggestedIp"] != movedIP {
		t.Errorf("suggestedIp = %q, want %q — the UI can only offer the new address if the response carries it: %s",
			got["suggestedIp"], movedIP, rec.Body.String())
	}
	if !strings.Contains(got["error"], movedIP) {
		t.Errorf("the message must name the address the router answers at, or the field is unusable on its own:\n%s", got["error"])
	}
}

// TestWifiScanDoesNotInventAnAddress: nothing answers elsewhere, so the response
// must stay the plain failure. A guessed address is worse than no address.
func TestWifiScanDoesNotInventAnAddress(t *testing.T) {
	refuseTheDial(t)
	stubDiscovery(t)

	rec := postWifiScan(t, `{"ip":"`+wantedIP+`","password":"x","mac":"`+movedMAC+`"}`)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("want 502, got %d: %s", rec.Code, rec.Body.String())
	}
	got := errorPayload(t, rec)
	if v, ok := got["suggestedIp"]; ok {
		t.Errorf("suggestedIp = %q, want the key absent when no candidate answers", v)
	}
	if strings.Contains(got["error"], "answers at") {
		t.Errorf("no candidate answered, so the message must not claim one does:\n%s", got["error"])
	}
}

// TestWifiScanDoesNotOfferAnotherRouter: a DIFFERENT router answering elsewhere
// is not "your router moved". Offering it would send the operator to deploy onto
// the wrong device — the failure this gate exists to prevent.
func TestWifiScanDoesNotOfferAnotherRouter(t *testing.T) {
	refuseTheDial(t)
	stubDiscovery(t, RouterInfo{IP: "192.168.8.1", MAC: "aa:bb:cc:dd:ee:ff", SSH: true})

	rec := postWifiScan(t, `{"ip":"`+wantedIP+`","password":"x","mac":"`+movedMAC+`"}`)

	got := errorPayload(t, rec)
	if v, ok := got["suggestedIp"]; ok {
		t.Errorf("suggestedIp = %q, want the key absent: that MAC is a different router", v)
	}
}

// TestWifiScanKeepsAHostKeyRefusalIntact: an untrusted host key is NOT a moved
// address, and rediscovery must not run — the refusal names a fingerprint and an
// exact trust instruction, and it is the only thing that can fix that state.
func TestWifiScanKeepsAHostKeyRefusalIntact(t *testing.T) {
	knownHostsStore(t)
	router := startRogueRouter(t, hostKeySigner(t))
	aimAtFixtureRouter(t, router)
	// A router answering elsewhere must not turn an untrusted key into a "your
	// router moved" hint: this router is answering, and it is refusing on
	// purpose. The scan is not even consulted.
	calls := stubDiscovery(t, RouterInfo{IP: movedIP, MAC: movedMAC, SSH: true})

	rec := postWifiScan(t, `{"ip":"`+router.host()+`","password":"test","mac":"`+movedMAC+`"}`)

	got := errorPayload(t, rec)
	if !strings.Contains(got["error"], "--trust-host-key") {
		t.Errorf("the refusal must survive verbatim:\n%s", got["error"])
	}
	if v, ok := got["suggestedIp"]; ok {
		t.Errorf("suggestedIp = %q, want the key absent: a host-key refusal is not a moved address", v)
	}
	if *calls != 0 {
		t.Errorf("the LAN was scanned %d time(s) for a router that had just answered with a refusal", *calls)
	}
}
