package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// refusal_surface_test.go pins the RISK item of the #41 review: the host-key
// refusal text ("which fingerprint, and how to trust it") reached only 4 of the 13
// product sshConnect sites, and sshIdentify returned silently — so an operator who
// drives the wizard from the browser (the curl|bash flow writes the binary's
// stderr into a log file they never open) saw a router that simply could not be
// identified, with no way to learn what to do.
//
// These tests drive the REAL handlers over httptest against the same in-process
// fixture router the host-key contract tests use, and assert the refusal text
// arrives at the surface the browser reads.

// aimAtFixtureRouter points every router-facing port at the fixture server: the
// discovery probe (sshProbePort) and the real sshConnect path (sshDialPort).
func aimAtFixtureRouter(t *testing.T, fr *rogueRouter) {
	t.Helper()
	oldDial, oldProbe := sshDialPort, sshProbePort
	sshDialPort = fr.port()
	sshProbePort = mustAtoi(t, fr.port())
	t.Cleanup(func() {
		sshDialPort = oldDial
		sshProbePort = oldProbe
	})
}

func mustAtoi(t *testing.T, s string) int {
	t.Helper()
	n, err := strconv.Atoi(s)
	if err != nil {
		t.Fatalf("port %q is not a number: %v", s, err)
	}
	return n
}

// TestIdentifyReportsHostKeyRefusal is the browser-first operator's path: the
// wizard's router list identifies the selected router through /api/identify, and
// the refusal (fingerprint + trust instruction) must come back in the response.
func TestIdentifyReportsHostKeyRefusal(t *testing.T) {
	knownHostsStore(t)

	hostKey := hostKeySigner(t)
	router := startRogueRouter(t, hostKey)
	aimAtFixtureRouter(t, router)

	// 1. The scan/identify probe itself.
	info := probeRouterWithPassword(router.host(), "")
	if info.SSHRefusal == "" {
		t.Fatalf("probeRouterWithPassword dropped the host-key refusal (recorded refusal was %q)", lastHostKeyRefusal(router.host()))
	}
	for _, want := range []string{"--trust-host-key", ssh.FingerprintSHA256(hostKey.PublicKey())} {
		if !strings.Contains(info.SSHRefusal, want) {
			t.Errorf("the refusal a browser-first operator sees must carry %q:\n%s", want, info.SSHRefusal)
		}
	}

	// 2. The HTTP surface the UI actually reads.
	req := httptest.NewRequest(http.MethodPost, "/api/identify", bytes.NewBufferString(
		`{"ip":"`+router.host()+`","password":""}`))
	rec := httptest.NewRecorder()
	handleIdentify(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("/api/identify returned %d: %s", rec.Code, rec.Body.String())
	}
	var got RouterInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode /api/identify response %q: %v", rec.Body.String(), err)
	}
	if !strings.Contains(got.SSHRefusal, "--trust-host-key") {
		t.Errorf("/api/identify response does not carry the trust instruction — a browser-first operator cannot act on it:\n%s", rec.Body.String())
	}
}

// TestTrustedRouterCarriesNoRefusal is the other half: once the key is trusted the
// response must not keep a stale refusal, or the UI would warn about a router that
// is fine.
func TestTrustedRouterCarriesNoRefusal(t *testing.T) {
	knownHostsStore(t)

	hostKey := hostKeySigner(t)
	router := startRogueRouter(t, hostKey)
	aimAtFixtureRouter(t, router)
	withTrustedFingerprint(t, ssh.FingerprintSHA256(hostKey.PublicKey()))

	info := probeRouterWithPassword(router.host(), "")
	if info.SSHRefusal != "" {
		t.Errorf("a trusted router still carries a refusal: %q", info.SSHRefusal)
	}
}

// TestWifiTestReportsHostKeyRefusal covers the STA surfaces: /api/wifi-test runs
// testSTAConfig, whose failure text used to be "check the SSID and password" even
// when the connect never happened because the router's key was refused.
func TestWifiTestReportsHostKeyRefusal(t *testing.T) {
	knownHostsStore(t)

	hostKey := hostKeySigner(t)
	router := startRogueRouter(t, hostKey)
	aimAtFixtureRouter(t, router)

	req := httptest.NewRequest(http.MethodPost, "/api/wifi-test", bytes.NewBufferString(
		`{"ip":"`+router.host()+`","password":"`+testRouterCredential+`","ssid":"upstream","wifiPass":"pw"}`))
	rec := httptest.NewRecorder()
	handleWifiTest(rec, req)

	var got struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode /api/wifi-test response %q: %v", rec.Body.String(), err)
	}
	if got.OK {
		t.Fatalf("/api/wifi-test reported success against a host key nobody trusted: %s", rec.Body.String())
	}
	if !strings.Contains(got.Error, "--trust-host-key") {
		t.Errorf("/api/wifi-test error must carry the trust instruction, not a WiFi hint:\n%s", got.Error)
	}
	if !strings.Contains(got.Error, ssh.FingerprintSHA256(hostKey.PublicKey())) {
		t.Errorf("/api/wifi-test error must name the fingerprint it refused:\n%s", got.Error)
	}
}

// TestWifiScanReportsHostKeyRefusal covers the scan surface (/api/wifi-scan),
// which hands the operator the same refusal instead of a generic "cannot connect".
func TestWifiScanReportsHostKeyRefusal(t *testing.T) {
	knownHostsStore(t)

	router := startRogueRouter(t, hostKeySigner(t))
	aimAtFixtureRouter(t, router)

	req := httptest.NewRequest(http.MethodPost, "/api/wifi-scan", bytes.NewBufferString(
		`{"ip":"`+router.host()+`","password":"`+testRouterCredential+`"}`))
	rec := httptest.NewRecorder()
	handleWifiScan(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, "--trust-host-key") {
		t.Errorf("/api/wifi-scan response must carry the trust instruction:\n%s", body)
	}
}
