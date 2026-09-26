package main

// The fresh-login proof itself (#46 follow-up, finding 1) and its negative
// controls.
//
// These tests drive the REAL proveRootPassword against a REAL in-process SSH
// server, so "the router accepted the credential" is measured rather than
// asserted about a fake — the same technique hostkey_test.go uses for the
// host-key trust contract. What they pin:
//
//  1. only the credential the router actually accepts is reported as accepted,
//     so a set→set passwd that left the old hash in place fails the deploy;
//  2. the proof offers NOTHING but the candidate. The fixture accepts an empty
//     password (exactly like a fresh OpenWrt/dropbear), so any empty-password,
//     keyboard-interactive-"" or default-key fallback would have "succeeded" —
//     the test would catch it as a false proof AND in the recorded credential
//     log;
//  3. an empty candidate is refused without even dialling;
//  4. the candidate is never sent to a host whose key is not trusted.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// dialProofAt points the SSH dial port at the fixture server for the duration of
// the test (production always dials 22; see sshDialPort).
func dialProofAt(t *testing.T, fr *rogueRouter) {
	t.Helper()
	old := sshDialPort
	sshDialPort = fr.port()
	t.Cleanup(func() { sshDialPort = old })
}

// trustedProofRouter starts a fixture router whose host key is pinned for this
// test, so the dial reaches the auth stage.
func trustedProofRouter(t *testing.T) *rogueRouter {
	t.Helper()
	knownHostsStore(t)
	signer := hostKeySigner(t)
	withTrustedFingerprint(t, ssh.FingerprintSHA256(signer.PublicKey()))
	fr := startRogueRouter(t, signer)
	dialProofAt(t, fr)
	return fr
}

// TestProveRootPasswordOnlyAcceptsTheCredentialTheRouterTakes is the core
// soundness test: the proof must fail for a credential the router rejects, and
// it must offer nothing but the candidate.
func TestProveRootPasswordOnlyAcceptsTheCredentialTheRouterTakes(t *testing.T) {
	fr := trustedProofRouter(t)

	if !proveRootPassword(fr.host(), testRouterCredential) {
		t.Fatalf("a fresh login with the credential the router accepts was reported as refused")
	}
	if proveRootPassword(fr.host(), "not-the-credential") {
		t.Fatal("a fresh login with a credential the router REJECTS was reported as accepted — that is the hash-existence bug in another form")
	}

	seen := fr.credentialsSeen()
	want := []string{testRouterCredential, "not-the-credential"}
	if len(seen) != len(want) {
		t.Fatalf("the router was offered %q, want exactly %q — the proof must offer NOTHING but the candidate (this fixture accepts an empty password, so any fallback would have authenticated and produced a false proof)", seen, want)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("the router was offered %q, want exactly %q", seen, want)
		}
	}

	// An empty candidate is not a proof of anything, and must not be sent.
	if proveRootPassword(fr.host(), "") {
		t.Fatal("an EMPTY candidate was reported as a successful proof")
	}
	if got := fr.credentialsSeen(); len(got) != len(want) {
		t.Fatalf("an empty candidate was dialled anyway (the router was offered %q): a fresh OpenWrt accepts an empty password, so it proves nothing", got)
	}
}

// TestProveRootPasswordSendsNothingToAnUntrustedHost: the proof carries the
// router's root password, so it must obey the same host-key trust rule as the
// deploy session — no fingerprint trust, no credential sent.
func TestProveRootPasswordSendsNothingToAnUntrustedHost(t *testing.T) {
	knownHostsStore(t) // an empty store, and nothing pinned
	fr := startRogueRouter(t, hostKeySigner(t))
	dialProofAt(t, fr)

	if proveRootPassword(fr.host(), testRouterCredential) {
		t.Fatal("the proof authenticated to a host whose key the operator has not trusted")
	}
	if got := fr.credentialsSeen(); len(got) != 0 {
		t.Fatalf("the candidate password was transmitted to an untrusted host: %q", got)
	}
}

// TestRootPasswordProofOffersNoFallbackAuth is the structural half of (2): the
// proof must not be able to reach the fallbacks sshConnect() carries — an
// empty-password retry or a default SSH key would report success for a
// credential the router never adopted. Behaviour is pinned by the test above;
// this pins the code path so a future "just reuse sshConnect" refactor cannot
// quietly reintroduce them.
func TestRootPasswordProofOffersNoFallbackAuth(t *testing.T) {
	body := goFuncBody(t, "ssh.go", "proveRootPassword")
	for _, forbidden := range []string{"sshConnect(", "reconnectSSH(", "tryDefaultKeys", "ssh.PublicKeys", `Password("")`} {
		if strings.Contains(body, forbidden) {
			t.Errorf("proveRootPassword must not use %s — that would authenticate without the candidate password\n%s", forbidden, body)
		}
	}
	if !strings.Contains(body, "ssh.Password(password)") {
		t.Errorf("proveRootPassword does not offer the candidate as a password\n%s", body)
	}
	if !strings.Contains(body, "routerHostKeyCallback(") {
		t.Errorf("proveRootPassword does not verify the router's host key before offering the credential\n%s", body)
	}
}

// goFuncBody returns the body source of the named function in file. (funcBodyText
// in wireless_rollback_test.go is bound to deploy.go's source; this reads any
// file's own bytes.)
func goFuncBody(t *testing.T, file, name string) string {
	t.Helper()
	src, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, src, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Name.Name != name || fd.Body == nil {
			continue
		}
		return string(src[fset.Position(fd.Body.Pos()).Offset:fset.Position(fd.Body.End()).Offset])
	}
	t.Fatalf("%s declares no function %s", file, name)
	return ""
}
