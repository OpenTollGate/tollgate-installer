package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// trust_api_test.go pins /api/trust-host-key, the browser wizard's trust action
// for a freshly re-keyed router (the C2-I-01 refusal is correct; the operator
// just needs a way to act on the fingerprint the wizard shows them).
//
// The tests drive the REAL handler against the same in-process fixture router as
// the host-key contract tests, so "the key was trusted" is measured by a later
// pin-less sshConnect succeeding, and "no credential was offered" is measured by
// the fixture's credential log — not asserted about a fake.

// dialAtFixtureRouter points the real sshConnect / trust dial path at the
// fixture server (production always dials 22; see sshDialPort).
func dialAtFixtureRouter(t *testing.T, fr *rogueRouter) {
	t.Helper()
	old := sshDialPort
	sshDialPort = fr.port()
	t.Cleanup(func() { sshDialPort = old })
}

// postTrust POSTs a raw body to /api/trust-host-key and returns the recorder.
func postTrust(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/trust-host-key", bytes.NewBufferString(body))
	handleTrustHostKey(rr, req)
	return rr
}

// TestTrustHostKeyEndpointAcceptsAndRemembers: a correct fingerprint is accepted,
// the key is written to the store, a later run needs no pin, and the trust dial
// itself offers the router nothing.
func TestTrustHostKeyEndpointAcceptsAndRemembers(t *testing.T) {
	knownHostsStore(t)

	signer := hostKeySigner(t)
	fr := startRogueRouter(t, signer)
	dialAtFixtureRouter(t, fr)

	want := ssh.FingerprintSHA256(signer.PublicKey())
	rr := postTrust(t, `{"ip":"`+fr.host()+`","fingerprint":"`+want+`"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("/api/trust-host-key returned %d: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"trusted":true`) {
		t.Errorf("the response does not report the key as trusted: %s", rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), want) {
		t.Errorf("the response does not echo the trusted fingerprint: %s", rr.Body.String())
	}

	// No pin: the store must now resolve the router on its own.
	withTrustedFingerprint(t, "")
	client := sshConnect(fr.host(), testRouterCredential)
	if client == nil {
		t.Fatalf("the key was not remembered — a later pin-less connect was refused: %s", lastHostKeyRefusal(fr.host()))
	}
	client.Close()

	// The trust dial must not have offered a credential; the ONLY password the
	// fixture may have seen is the one sshConnect just sent.
	seen := fr.credentialsSeen()
	if len(seen) != 1 || seen[0] != testRouterCredential {
		t.Fatalf("the trust endpoint offered credentials to the router: %q", seen)
	}
}

// TestTrustHostKeyEndpointRefusesMismatch: a fingerprint the router does not
// present is refused and NOTHING is persisted, so the refusal still protects the
// address.
func TestTrustHostKeyEndpointRefusesMismatch(t *testing.T) {
	knownHostsStore(t)

	signer := hostKeySigner(t)
	fr := startRogueRouter(t, signer)
	dialAtFixtureRouter(t, fr)

	wrong := ssh.FingerprintSHA256(hostKeySigner(t).PublicKey())
	rr := postTrust(t, `{"ip":"`+fr.host()+`","fingerprint":"`+wrong+`"}`)
	if rr.Code == http.StatusOK {
		t.Fatalf("a fingerprint the router does not present was accepted: %s", rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), ssh.FingerprintSHA256(signer.PublicKey())) {
		t.Errorf("the refusal must name the key the router actually presents, so the operator can retry:\n%s", rr.Body.String())
	}

	// Nothing pinned: a fresh connect must still be refused, and no credential
	// may have reached a host whose key did not match.
	if client := sshConnect(fr.host(), testRouterCredential); client != nil {
		client.Close()
		t.Fatal("a mismatched trust decision was persisted")
	}
	if got := fr.credentialsSeen(); len(got) != 0 {
		t.Fatalf("a credential reached a host whose key did not match the fingerprint: %q", got)
	}
}

// TestTrustHostKeyEndpointRejectsMalformed: bad input fails before any dial, so
// an empty or junk fingerprint can never be treated as "trust anything".
func TestTrustHostKeyEndpointRejectsMalformed(t *testing.T) {
	knownHostsStore(t)

	for _, body := range []string{
		`not json`,
		`{"ip":"127.0.0.1"}`,
		`{"ip":"127.0.0.1","fingerprint":""}`,
		`{"ip":"127.0.0.1","fingerprint":"not-a-fingerprint"}`,
		`{"fingerprint":"SHA256:AbCdEf"}`,
	} {
		if rr := postTrust(t, body); rr.Code != http.StatusBadRequest {
			t.Errorf("body %q returned %d, want 400: %s", body, rr.Code, rr.Body.String())
		}
	}
}

// TestTrustHostKeyEndpointRejectsGet pins the method check.
func TestTrustHostKeyEndpointRejectsGet(t *testing.T) {
	rr := httptest.NewRecorder()
	handleTrustHostKey(rr, httptest.NewRequest(http.MethodGet, "/api/trust-host-key", nil))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET returned %d, want 405", rr.Code)
	}
}

// TestTrustDialOffersNoAuth is the structural half of the "nothing was offered"
// assertion: the verifier must not be able to reach any auth method, so a future
// "just reuse sshConnect" refactor cannot turn a trust check into a credential
// offer to a host whose key was never verified.
func TestTrustDialOffersNoAuth(t *testing.T) {
	body := goFuncBody(t, "hostkey.go", "trustHostKeyForHost")
	for _, forbidden := range []string{"ssh.Password", "keyboardInteractiveAuth", "tryDefaultKeys", "sshConnect(", "ssh.PublicKeys"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("trustHostKeyForHost must not offer %s — it verifies the host key, it does not authenticate\n%s", forbidden, body)
		}
	}
	if !strings.Contains(body, "HostKeyCallback") {
		t.Errorf("trustHostKeyForHost does not verify the host key\n%s", body)
	}
	if !strings.Contains(body, "rememberHostKey(") {
		t.Errorf("trustHostKeyForHost does not persist the verified key\n%s", body)
	}
}
