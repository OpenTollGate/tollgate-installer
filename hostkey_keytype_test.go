package main

import (
	"net"
	"os"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// TestUnstoredKeyTypeIsNotReportedAsAnUnknownHost pins the 2026-10-02 defect.
//
// OpenWrt's dropbear serves one host key PER ALGORITHM, and the installer
// negotiates ed25519-first. x/crypto's checker only offers the keys it holds for
// the algorithm just presented (knownhosts.go indexes knownKeys by key type), so
// a host the operator HAD trusted under ed25519, presenting its rsa key, produced
// an empty KeyError.Want — which the naive code reported as "This host is not in
// the trust store". That is false, and it sends the operator hunting for an
// impostor instead of at the key their own store already held.
//
// The refusal must therefore distinguish "unknown host" from "known host, key
// type I do not hold yet", name the type it does hold, and still refuse.
func TestUnstoredKeyTypeIsNotReportedAsAnUnknownHost(t *testing.T) {
	store := knownHostsStore(t)

	// The operator already trusted this router — over its ed25519 key.
	trusted := hostKeySigner(t)
	// This time the handshake presents the router's rsa key.
	presented := rsaHostKeySigner(t)
	router := startRogueRouter(t, presented)

	// The store must name the DIALED address (host:port) — a line without the
	// port only covers port 22 and would let the checker treat the host as
	// unknown for the wrong reason, hiding the case under test.
	addr := net.JoinHostPort(router.host(), router.port())
	line := knownhosts.Line([]string{knownhosts.Normalize(addr)}, trusted.PublicKey())
	if err := os.WriteFile(store, []byte(line+"\n"), 0o600); err != nil {
		t.Fatalf("seed trust store: %v", err)
	}
	if got := storeKeyEntriesForHost(store, addr); len(got) != 1 {
		t.Fatalf("precondition: store must hold exactly the trusted ed25519 entry, got %v", got)
	}

	client := dialRogueRouter(t, router)
	if client != nil {
		client.Close()
		t.Fatal("a key type absent from the store must be refused, never accepted")
	}
	if seen := router.credentialsSeen(); len(seen) != 0 {
		t.Fatalf("refusing an unstored key type still leaked credentials %q", seen)
	}

	// recordHostKeyRefusal is keyed by the router's IP (no port); the store line
	// above is keyed by the dialed address. Two different keys, deliberately.
	refusal := lastHostKeyRefusal(router.host())
	if refusal == "" {
		t.Fatal("expected a recorded host-key refusal")
	}
	if strings.Contains(refusal, "not in the trust store") {
		t.Errorf("refusal must not claim the host is unknown when the store holds its other key type:\n%s", refusal)
	}
	for _, want := range []string{
		"ed25519", // the type the store holds
		"rsa",     // the type presented
		ssh.FingerprintSHA256(trusted.PublicKey()),   // the fingerprint the operator already trusted
		ssh.FingerprintSHA256(presented.PublicKey()), // the fingerprint presented now
		"different key TYPE",
	} {
		if !strings.Contains(refusal, want) {
			t.Errorf("refusal must contain %q so the operator can tell a key TYPE change from an impostor\n%s", want, refusal)
		}
	}
}

// TestWrongKeyTypePinNamesBothKeyTypes pins the other half of the same loop: a
// pin taken for the RSA key can never satisfy a handshake that presents ed25519.
// The operator was shown two unlabelled fingerprints and no statement that the
// PIN itself was the wrong key type — which is what made the loop unresolvable
// without outside knowledge.
func TestWrongKeyTypePinNamesBothKeyTypes(t *testing.T) {
	knownHostsStore(t)

	router := startRogueRouter(t, hostKeySigner(t)) // presents ed25519
	rsa := rsaHostKeySigner(t)

	// A typed pin for the router's OTHER key: correct router, wrong key type.
	withTrustedFingerprint(t, "rsa "+ssh.FingerprintSHA256(rsa.PublicKey()))

	client := dialRogueRouter(t, router)
	if client != nil {
		client.Close()
		t.Fatal("a pin for the wrong key type must be refused, never treated as consent")
	}
	if seen := router.credentialsSeen(); len(seen) != 0 {
		t.Fatalf("a wrong-type pin still leaked credentials %q", seen)
	}

	refusal := lastHostKeyRefusal(router.host())
	for _, want := range []string{"mismatch", "ed25519", "rsa", "two different keys"} {
		if !strings.Contains(refusal, want) {
			t.Errorf("wrong-type pin refusal must contain %q so the loop resolves in one glance\n%s", want, refusal)
		}
	}
}
