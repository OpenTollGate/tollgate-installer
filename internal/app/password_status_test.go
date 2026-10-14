package app

// passwordVerdict must measure the candidate password IN ISOLATION, never from a
// connect that a fallback could have authenticated.
//
// The `rogueRouter` fixture (hostkey_test.go) is the FALLBACK FIXTURE: it accepts
// an EMPTY password — exactly like a fresh OpenWrt/dropbear — and records every
// credential it is offered. Any empty-password retry, keyboard-interactive-"",
// or default-key fallback would therefore "succeed" against it. The old
// connect-outcome verdict read `accepted` for a WRONG typed password in that
// situation; these tests pin that it no longer can, and that the candidate is
// measured in isolation.
//
// A second fixture (startProtectedFixture) models a router with a REAL root
// hash: it REFUSES an empty password and accepts only the credential — the
// "password required" shape the fallback fixture cannot express.

import (
	"errors"
	"net"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// startProtectedFixture starts the same in-process SSH fixture as rogueRouter but
// with a router that has a real root hash: an empty password is REFUSED and only
// testRouterCredential is accepted. Returns the fixture and its host-key signer
// (so a test can pin the fingerprint).
func startProtectedFixture(t *testing.T) (*rogueRouter, ssh.Signer) {
	t.Helper()
	signer := hostKeySigner(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	fr := &rogueRouter{t: t, ln: ln}
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(_ ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			fr.mu.Lock()
			fr.authSeen = append(fr.authSeen, string(pw))
			fr.mu.Unlock()
			if string(pw) == testRouterCredential {
				return nil, nil
			}
			return nil, errors.New("password rejected")
		},
	}
	cfg.AddHostKey(signer)
	go fr.serve(cfg)
	t.Cleanup(func() { ln.Close() })
	return fr, signer
}

// trustFixture points the trust store at a private file, pins the fixture's host
// key, and aims the dial at its port — so passwordVerdict's dials reach the auth
// stage.
func trustFixture(t *testing.T, fr *rogueRouter, signer ssh.Signer) {
	t.Helper()
	knownHostsStore(t)
	withTrustedFingerprint(t, ssh.FingerprintSHA256(signer.PublicKey()))
	old := sshDialPort
	sshDialPort = fr.port()
	t.Cleanup(func() { sshDialPort = old })
}

// TestPasswordVerdictDoesNotAcceptAWrongPasswordViaAFallback is THE regression.
//
// Against the fallback fixture (which accepts an empty password), a connect-outcome
// verdict — the shape that shipped in rc28 — reads `accepted` for a WRONG typed
// password, because the empty-password fallback authenticates. The isolated
// verdict must instead report `not_required` (the router needs no password at
// all) and NEVER `accepted`.
func TestPasswordVerdictDoesNotAcceptAWrongPasswordViaAFallback(t *testing.T) {
	fr := trustedProofRouter(t) // fallback fixture: accepts empty, records creds

	for _, candidate := range []string{testRouterCredential, "not-the-credential"} {
		got := passwordVerdict(fr.host(), candidate)
		if got == "accepted" {
			t.Fatalf("passwordVerdict(%q) = accepted — the fixture accepts an empty password, so 'accepted' is the false-proof bug (a wrong password authenticated via a fallback)", candidate)
		}
		if got != "not_required" {
			t.Fatalf("passwordVerdict(%q) = %q, want not_required (a router that accepts a passwordless login needs no password)", candidate, got)
		}
	}
	for _, pw := range fr.credentialsSeen() {
		if pw != "" {
			t.Fatalf("the verdict offered %q — against a passwordless router it must offer only the empty (passwordless) candidate, never the typed password or a key", pw)
		}
	}
}

// TestPasswordVerdictMeasuresTheCandidateInIsolation: on a router that REQUIRES a
// password, the verdict is accepted for the credential the router takes and
// refused for one it rejects — measured with the candidate only (no fallback).
func TestPasswordVerdictMeasuresTheCandidateInIsolation(t *testing.T) {
	fr, signer := startProtectedFixture(t)
	trustFixture(t, fr, signer)

	if got := passwordVerdict(fr.host(), testRouterCredential); got != "accepted" {
		t.Fatalf("the credential the router accepts read %q, want accepted", got)
	}
	if got := passwordVerdict(fr.host(), "not-the-credential"); got != "refused" {
		t.Fatalf("a WRONG password read %q, want refused", got)
	}
	if got := passwordVerdict(fr.host(), ""); got != "missing" {
		t.Fatalf("no password on a protected router read %q, want missing", got)
	}

	// Every credential offered must be a candidate the test actually supplied
	// (the empty probe, or a typed candidate) — no key-based dial leaks in.
	want := map[string]bool{"": true, testRouterCredential: true, "not-the-credential": true}
	for _, pw := range fr.credentialsSeen() {
		if !want[pw] {
			t.Fatalf("the verdict offered an unexpected credential %q — it must offer only the candidates", pw)
		}
	}
}

// TestPasswordVerdictSendsNothingToAnUntrustedHost: the verdict carries the
// candidate, so an untrusted host key must abort the handshake with no credential
// sent.
func TestPasswordVerdictSendsNothingToAnUntrustedHost(t *testing.T) {
	knownHostsStore(t) // empty store, nothing pinned
	fr, signer := startProtectedFixture(t)
	_ = signer
	old := sshDialPort
	sshDialPort = fr.port()
	t.Cleanup(func() { sshDialPort = old })

	if got := passwordVerdict(fr.host(), testRouterCredential); got != "untrusted" {
		t.Fatalf("an untrusted host read %q, want untrusted", got)
	}
	if seen := fr.credentialsSeen(); len(seen) != 0 {
		t.Fatalf("a credential was transmitted to an untrusted host: %q", seen)
	}
}

// TestPasswordVerdictOffersNoFallbackAuth is the structural guard: the verdict's
// dial path must not be able to reach the fallbacks sshConnect() carries.
func TestPasswordVerdictOffersNoFallbackAuth(t *testing.T) {
	for _, name := range []string{"passwordVerdict", "classifyPasswordDial"} {
		body := goFuncBody(t, "hostkey.go", name)
		for _, forbidden := range []string{"sshConnect(", "tryDefaultKeys", "ssh.PublicKeys", `Password("")`} {
			if strings.Contains(body, forbidden) {
				t.Errorf("%s must not use %s — the verdict must measure the candidate in isolation\n%s", name, forbidden, body)
			}
		}
	}
	if !strings.Contains(goFuncBody(t, "hostkey.go", "passwordVerdict"), "isolatedPasswordDial(") {
		t.Error("passwordVerdict does not use isolatedPasswordDial — it must not derive the verdict from a fallback-capable connect")
	}
}

