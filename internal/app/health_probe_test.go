package app

// The health probe runs ON THE ROUTER, whose `wget` is uclient-fetch. A probe
// that passed an unsupported flag — notably `-S`, which uclient-fetch does not
// have — captured the tool's usage text instead of the API body, so a HEALTHY
// merchant classified as "unknown" and every install reported "Setup failed"
// (rc28–rc30, #134).
//
// This drives the REAL healthProbeCmd through a shell whose `wget` emulates
// uclient-fetch (it rejects `-S`), proving end to end that the probe reads the
// advertisement and merchantState calls it healthy — and that re-introducing
// `-S` breaks it. A future flag regression is caught here, not on a router.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const fakeAdvertisement = `{"kind":10021,"id":"abc","metric":"bytes","pubkey":"deadbeef"}`

func writeStub(t *testing.T, dir, name, script string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatalf("write stub %s: %v", name, err)
	}
}

// runProbeCmd runs a probe command through sh with the uclient-fetch-emulating
// stubs on PATH, returning the combined output.
func runProbeCmd(t *testing.T, dir, cmd string) string {
	t.Helper()
	c := exec.Command("sh", "-c", cmd)
	c.Env = append(os.Environ(),
		"PATH="+dir+":"+os.Getenv("PATH"),
		"FAKE_AD="+fakeAdvertisement,
	)
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("probe command failed: %v\n%s", err, out)
	}
	return string(out)
}

// uclientFetchStubs installs a `netstat` (API listening on :2121) and a `wget`
// that behaves like uclient-fetch: it has NO `-S`, so any `-S` argument is
// rejected with the exact usage error the operator saw; a plain GET prints the
// advertisement.
func uclientFetchStubs(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeStub(t, dir, "netstat", `echo "tcp 0 0 :::2121 :::* LISTEN"`)
	writeStub(t, dir, "wget", `for a in "$@"; do
  if [ "$a" = "-S" ]; then
    echo "wget: unrecognized option: S" >&2
    echo "Usage: wget [options] <URL>" >&2
    exit 1
  fi
done
printf '%s' "$FAKE_AD"`)
	return dir
}

func TestHealthProbeCmdReadsTheAdvertisementOnUclientFetch(t *testing.T) {
	dir := uclientFetchStubs(t)
	out := runProbeCmd(t, dir, healthProbeCmd)

	listening, body := parseHealthProbe(out)
	if !listening {
		t.Fatalf("listening = false; probe output:\n%s", out)
	}
	if !strings.Contains(body, `"kind"`) {
		t.Fatalf("probe body is not the advertisement against a uclient-fetch wget:\n%q", body)
	}
	if got := merchantState(body); got != merchantHealthy {
		t.Fatalf("merchantState(body) = %q, want healthy; body=%q", got, body)
	}
}

// TestHealthProbeCmdWouldBreakWithDashS is the regression guard: the exact
// `-S` variant that shipped must classify as unknown, so re-introducing it is a
// test failure rather than a silent router-side failure.
func TestHealthProbeCmdWouldBreakWithDashS(t *testing.T) {
	dir := uclientFetchStubs(t)
	buggy := strings.Replace(healthProbeCmd, "wget -qO-", "wget -S -qO-", 1)
	if buggy == healthProbeCmd {
		t.Fatal("could not construct the -S variant — healthProbeCmd no longer contains 'wget -qO-'")
	}
	out := runProbeCmd(t, dir, buggy)

	_, body := parseHealthProbe(out)
	if got := merchantState(body); got != merchantUnknown {
		t.Fatalf("the -S variant should be unknown (the bug), got %q; body=%q", got, body)
	}
	if !strings.Contains(body, "unrecognized option") {
		t.Fatalf("expected the uclient-fetch usage error in the -S variant body, got %q", body)
	}
}

// TestMerchantStateReadsTheStartingRefusal pins the module's own explicit
// startup-gate refusal (src/startup_gate.go) to merchantStarting: this is what
// tells a slow-but-fine install apart from a dead one.
func TestMerchantStateReadsTheStartingRefusal(t *testing.T) {
	body := `{"code":"starting","status":"error","error":"tollgate is starting up"}`
	if got := merchantState(body); got != merchantStarting {
		t.Fatalf("merchantState(starting refusal) = %q, want starting; body=%q", got, body)
	}
}

// TestMerchantStateReadsTheRateLimitRefusal pins the module's rate limiter body
// (src/main.go RateLimitMiddleware: `429 {"error":"rate limit exceeded"}`) to
// merchantRateLimited. The installer's own 3 s poll exceeds the API's default
// 10 req/min for 127.0.0.1, so this body is EXPECTED during a health check and
// must never be read as "the money path is not answering".
func TestMerchantStateReadsTheRateLimitRefusal(t *testing.T) {
	body := `{"error":"rate limit exceeded"}`
	if got := merchantState(body); got != merchantRateLimited {
		t.Fatalf("merchantState(rate-limit refusal) = %q, want rate-limited; body=%q", got, body)
	}
	// The phrase the API actually logs/answers must also classify, so a future
	// wording change to "Too Many Requests" does not silently regress.
	if got := merchantState("Too Many Requests"); got != merchantRateLimited {
		t.Fatalf("merchantState('Too Many Requests') = %q, want rate-limited", got)
	}
}

// TestMerchantStateEmptyIsUnknownNotStarting keeps the crash-loop discriminator
// intact: a genuinely empty body (a dead/hung service) is neither healthy nor
// starting nor rate-limited. Whether that becomes a hard failure is the CALLER's
// decision — and only when the merchant was never once seen starting.
func TestMerchantStateEmptyIsUnknownNotStarting(t *testing.T) {
	if got := merchantState(""); got != merchantUnknown {
		t.Fatalf("merchantState(empty) = %q, want unknown", got)
	}
}

// TestMerchantStartVerdictSlowStartIsPendingNotFailed is the heart of the fix:
// the exact router incident (2026-10-16) in a table.
func TestMerchantStartVerdictSlowStartIsPendingNotFailed(t *testing.T) {
	cases := []struct {
		name        string
		state       string
		sawStarting bool
		listening   bool
		noInternet  bool
		want        string
	}{
		{"healthy beats everything", merchantHealthy, false, true, false, "healthy"},
		{"last probe empty but starting was seen -> pending (the bug)", merchantUnknown, true, true, false, "pending"},
		{"last probe empty, no internet -> pending", merchantUnknown, false, true, true, "pending"},
		{"last probe rate-limited -> pending", merchantRateLimited, false, true, false, "pending"},
		{"last probe starting -> pending", merchantStarting, false, true, false, "pending"},
		{"never started, empty, has internet -> fail (crash-loop)", merchantUnknown, false, true, false, "fail"},
		{"not listening, never started -> fail", merchantUnknown, false, false, false, "fail"},
		{"not listening but no internet -> fail (nothing to wait on)", merchantUnknown, false, false, true, "fail"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := merchantStartVerdict(c.state, c.sawStarting, c.listening, c.noInternet); got != c.want {
				t.Fatalf("merchantStartVerdict(%q, saw=%v, listening=%v, noInternet=%v) = %q, want %q",
					c.state, c.sawStarting, c.listening, c.noInternet, got, c.want)
			}
		})
	}
}

// TestRouterInternetProbeCmdContract pins the shared no-internet probe to the
// exact body the diagnostics block prints, so the classifier and the printed
// evidence cannot drift apart.
func TestRouterInternetProbeCmdContract(t *testing.T) {
	if !strings.Contains(routerInternetProbeCmd, "no internet") {
		t.Fatalf("routerInternetProbeCmd must emit the literal 'no internet' on failure: %q", routerInternetProbeCmd)
	}
	if !strings.Contains(routerInternetProbeCmd, "online") {
		t.Fatalf("routerInternetProbeCmd must emit 'online' on success: %q", routerInternetProbeCmd)
	}
}
