package app

import (
	"net/http"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// trust_ui_test.go pins the OTHER half of /api/trust-host-key: what the browser
// can do with a refusal.
//
// The defect this covers (reported 2026-10-01, "the trust host key button does
// not work"): trustHostKeyForHost records the fingerprint the router ACTUALLY
// presents, but handleTrustHostKey only returned it inside the prose error
// string, and index.html's failure branch refreshed the hint text without
// updating the value it caches in window._trustFingerprint. So a click that was
// refused re-posted the same stale fingerprint on the next click, and every
// click after that failed identically — an unbreakable loop, with the operator
// correctly told to "correct the value and retry" and given no way to do it.
//
// Two properties, one test each:
//   A. the refusal carries the presented fingerprint STRUCTURALLY, so the UI can
//      act on it without parsing prose;
//   B. the UI refreshes its cached fingerprint from that field — and does NOT
//      auto-trust it: a changed key must still be re-verified and re-confirmed,
//      or one click on a stale prompt would bless a re-keyed or impostor host.

// jsFuncBody returns the body of a JS function delimited by sig, brace-matched,
// so an assertion about what a branch does cannot be satisfied by text elsewhere
// in the page (the same intent as goFuncBody for Go sources).
func jsFuncBody(t *testing.T, src, sig string) string {
	t.Helper()
	i := strings.Index(src, sig)
	if i < 0 {
		t.Fatalf("index.html: %q not found", sig)
	}
	open := strings.IndexByte(src[i:], '{')
	if open < 0 {
		t.Fatalf("index.html: %q has no body", sig)
	}
	open += i
	depth := 0
	for j := open; j < len(src); j++ {
		switch src[j] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return src[open : j+1]
			}
		}
	}
	t.Fatalf("index.html: %q body is unbalanced", sig)
	return ""
}

// A. The refusal must name the presented fingerprint in a field the UI can read.
func TestTrustRefusalCarriesPresentedFingerprint(t *testing.T) {
	knownHostsStore(t)

	signer := hostKeySigner(t)
	fr := startRogueRouter(t, signer)
	dialAtFixtureRouter(t, fr)

	// A fingerprint that is well-formed but is NOT what the router presents:
	// this is the operator's second router, or the same router re-keyed.
	supplied := ssh.FingerprintSHA256(hostKeySigner(t).PublicKey())
	presented := ssh.FingerprintSHA256(signer.PublicKey())

	rr := postTrust(t, `{"ip":"`+fr.host()+`","fingerprint":"`+supplied+`"}`)
	if rr.Code == http.StatusOK {
		t.Fatalf("a fingerprint the router does not present was accepted: %s", rr.Body.String())
	}
	body := rr.Body.String()

	// The prose already names it (that behaviour is pinned elsewhere); this test
	// is about the STRUCTURED field, which is what the UI can use.
	if !strings.Contains(body, `"ssh_fingerprint":"`+presented+`"`) {
		t.Errorf("the refusal does not carry the presented fingerprint as ssh_fingerprint, so no UI can act on it:\n%s", body)
	}
	// It must be the PRESENTED one, never the supplied (wrong) one.
	if strings.Contains(body, `"ssh_fingerprint":"`+supplied+`"`) {
		t.Errorf("the refusal echoes the SUPPLIED fingerprint as the presented one — trusting that would bless the wrong key:\n%s", body)
	}
}

// B. The UI must refresh its cached fingerprint from the refusal, and must not
// silently trust the refreshed value.
func TestTrustButtonRefreshesCachedFingerprintWithoutAutoTrusting(t *testing.T) {
	body := jsFuncBody(t, string(indexHTML), "async function trustHostKey(")

	failBranch := ""
	if i := strings.Index(body, "if (!resp.ok)"); i >= 0 {
		failBranch = body[i:]
	}
	if failBranch == "" {
		t.Fatalf("trustHostKey() has no !resp.ok branch:\n%s", body)
	}

	if !strings.Contains(failBranch, "window._trustFingerprint") {
		t.Errorf("trustHostKey()'s failure branch does not refresh window._trustFingerprint, so a refused click re-posts the same stale value forever (the reported 'button does not work' loop):\n%s", failBranch)
	}
	if !strings.Contains(failBranch, "ssh_fingerprint") {
		t.Errorf("trustHostKey()'s failure branch does not read ssh_fingerprint from the response:\n%s", failBranch)
	}

	// Safety: refreshing the cached value must NOT be accompanied by an automatic
	// retry inside the failure branch — the operator has to confirm the new
	// fingerprint explicitly (a changed key is the impersonation case).
	if strings.Contains(failBranch, "trustHostKey(") || strings.Contains(failBranch, "await fetch('/api/trust-host-key'") {
		t.Errorf("the failure branch retries trust automatically; a changed host key must be re-verified and explicitly re-confirmed, never auto-trusted:\n%s", failBranch)
	}
}
