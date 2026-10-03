package main

import (
	"strings"
	"testing"
)

// trust_banner_clear_test.go covers the THIRD half of /api/trust-host-key: what
// the page must do to the refusal banner once the refusal stops being true.
//
// The defect this covers (reported 2026-10-02: "why is this still happening?",
// with the wizard still showing "router SSH host key is not trusted: … presents
// a ed25519 key SHA256:CSPcG8Gu…" and the Trust button, immediately after a
// trust that returned 200 {"trusted":true}):
//
// The refusal is rendered from the CACHED detected-router entry. refreshRouterName
// merges the identify response with Object.assign, which does NOT delete keys the
// response omits — and the server omits ssh_refusal precisely when the connect
// SUCCEEDED (sshConnect calls forgetHostKeyRefusal at the start of every attempt).
// So a successful identify could never clear a stale refusal: the same text, the
// same fingerprint and the same button were re-rendered verbatim, which to the
// operator is indistinguishable from the failure it reports. They had already
// trusted the router; the screen said otherwise forever.
//
// Three properties:
//   C. the identify merge clears a refusal the response does not carry;
//   D. a successful trust clears the banner ITSELF, before any follow-up request
//      that may legitimately fail (auth), so feedback never depends on it;
//   E. clearing is ONE helper, so no call site can half-clear it (banner hidden
//      while the Trust button is left on screen).

// C. A successful identify must not leave the old refusal on the entry.
func TestIdentifySuccessClearsAStaleRefusal(t *testing.T) {
	body := jsFuncBody(t, string(indexHTML), "async function refreshRouterName(")
	if !strings.Contains(body, "info.ssh_refusal") {
		t.Errorf("refreshRouterName never consults the response's ssh_refusal, so it cannot tell \"the server reported no refusal\" from \"the server said nothing\"; a successful identify leaves the stale refusal rendered forever")
	}
	if !strings.Contains(body, "delete r.ssh_refusal") {
		t.Errorf("refreshRouterName does not delete a stale ssh_refusal: Object.assign keeps keys the response omits, so the refusal banner survives the connect that disproved it")
	}
}

// D. Trust succeeded => the banner goes, before anything else can fail.
func TestSuccessfulTrustClearsTheRefusalBanner(t *testing.T) {
	body := jsFuncBody(t, string(indexHTML), "async function trustHostKey(")
	clear := strings.Index(body, "clearTrustHint()")
	if clear < 0 {
		t.Fatalf("the success path of trustHostKey never clears the refusal banner: a click that returned 200 left the operator staring at the same \"not trusted\" text and the same button")
	}
	refresh := strings.Index(body, "await refreshRouterName()")
	if refresh < 0 {
		t.Fatalf("trustHostKey no longer re-identifies after a successful trust")
	}
	if clear > refresh {
		t.Errorf("clearTrustHint() runs AFTER await refreshRouterName(): the follow-up identify can fail on auth, and it must not be what stands between a successful trust and the operator's feedback")
	}
}

// F. The refusal is a DECISION WAITING ON THE OPERATOR, not a failure.
//
// Operator report, 2026-10-03, watching the recorded UI run: "I see the ssh key
// error messages in the playwright videos of the installer you shared. How did
// you get to the next step despite the error?" — it was not an error (the wizard
// refuses to send the password until the key is trusted, and pressing Deploy IS
// the trust step), but the panel rendered the server's full prose as one long
// grey paragraph, so it read as a failure the wizard then appeared to ignore.
func TestTrustHintReadsAsAPendingDecisionNotAFailure(t *testing.T) {
	body := jsFuncBody(t, string(indexHTML), "function showTrustHint(")
	for _, want := range []string{"hint-action", "hint-lead", "hint-fp", "hint-raw", "details"} {
		if !strings.Contains(body, want) {
			t.Errorf("showTrustHint does not render %q: the refusal reads as a bare failure instead of a decision the wizard is waiting on", want)
		}
	}
	if !strings.Contains(body, "ssh_refusal") {
		t.Error("showTrustHint no longer renders the server's refusal: the operator would lose the fingerprint and the --trust-host-key instruction")
	}
	if !strings.Contains(body, "not trusted yet") {
		t.Error("the panel must say the key is not trusted YET and that nothing has been sent — otherwise it reads as a failure")
	}
	if strings.Contains(body, "innerHTML") {
		t.Error("showTrustHint must never use innerHTML: the refusal carries router-supplied strings")
	}
	if !strings.Contains(string(indexHTML), ".hint-action") {
		t.Error("index.html has no .hint-action styling: the class is a no-op and the panel is indistinguishable from an error")
	}
}

// E. One helper clears both the text and the button.
func TestTrustHintClearingIsOneHelper(t *testing.T) {
	src := string(indexHTML)
	if !strings.Contains(src, "function clearTrustHint()") {
		t.Fatalf("index.html has no clearTrustHint() helper: the banner is cleared ad hoc, so a site can hide the text and leave the Trust button on screen")
	}
	body := jsFuncBody(t, src, "function clearTrustHint()")
	for _, want := range []string{"trust-hint", "trust-btn"} {
		if !strings.Contains(body, want) {
			t.Errorf("clearTrustHint() does not clear %q — a half-cleared banner is the defect it exists to remove", want)
		}
	}
	if !strings.Contains(body, "_trustFingerprint") {
		t.Errorf("clearTrustHint() leaves window._trustFingerprint set: the next prompt would offer a fingerprint for a router that no longer needs one")
	}
}
