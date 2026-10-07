package app

import (
	"net"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// startDualKeyRouter serves BOTH an ed25519 and an rsa host key, the way an
// OpenWrt dropbear does. Which one a given handshake uses is decided by the
// CLIENT's HostKeyAlgorithms preference (the server honours the client's order),
// which is exactly the property under test.
func startDualKeyRouter(t *testing.T, ed, rsa ssh.Signer) *rogueRouter {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	fr := &rogueRouter{t: t, ln: ln}
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(_ ssh.ConnMetadata, _ []byte) (*ssh.Permissions, error) {
			return nil, nil
		},
	}
	cfg.AddHostKey(ed)
	cfg.AddHostKey(rsa)
	go fr.serve(cfg)
	t.Cleanup(func() { ln.Close() })
	return fr
}

// TestTrustButtonDialNegotiatesTheKeyTheScanReported pins the 2026-10-02 defect
// that made the Trust button unusable, reported by an operator as a repeating
// `502 Bad Gateway` on POST /api/trust-host-key.
//
// The wizard hands the operator the fingerprint the SCAN path observed, and
// sshHostKeyAlgorithms is ed25519-first — so on a router serving both key types
// that is the ED25519 fingerprint, which the confirm dialog even tells them how
// to verify (`ssh-keygen -lf /etc/dropbear/dropbear_ed25519_host_key.pub`). The
// Trust action then dials through trustHostKeyForHost, which set NO
// HostKeyAlgorithms: x/crypto fell back to its own preference, negotiated the
// router's RSA key, compared it against the ed25519 fingerprint it had just been
// handed, and refused —
//
//	router SSH host key mismatch: … presents SHA256:rsKww… but SHA256:CSPcG8Gu… was supplied
//
// — returning 502 to the browser. No click could ever succeed on any dropbear
// router serving both key types, which is every stock OpenWrt box.
//
// sshHostKeyAlgorithms' own docstring already claims it "pins the ORDERED
// host-key preference for every SSH dial in this package"; this test is what
// makes that true rather than aspirational.
func TestTrustButtonDialNegotiatesTheKeyTheScanReported(t *testing.T) {
	knownHostsStore(t)

	ed := hostKeySigner(t)
	rsa := rsaHostKeySigner(t)
	router := startDualKeyRouter(t, ed, rsa)

	old := sshDialPort
	sshDialPort = router.port()
	t.Cleanup(func() { sshDialPort = old })

	// The fingerprint the operator is shown — and clicks Trust on — is the one
	// the scan observed: ed25519, because sshHostKeyAlgorithms is ed25519-first.
	want := ssh.FingerprintSHA256(ed.PublicKey())

	key, err := trustHostKeyForHost(router.host(), want)
	if err != nil {
		t.Fatalf("the Trust action must negotiate the same host key the scan reported (otherwise the operator is shown one fingerprint and the button verifies another): %v", err)
	}
	if got := ssh.FingerprintSHA256(key); got != want {
		t.Fatalf("trust dial negotiated %s, want the %s the scan reported", got, want)
	}

	// The verified key must be remembered, and remembered as the type that was
	// actually negotiated — a store entry of the wrong type would leave the next
	// pin-less connect refusing (see the key-type store handling).
	addr := net.JoinHostPort(router.host(), sshDialPort)
	entries := storeKeyEntriesForHost(knownHostsPath(), addr)
	if len(entries) != 1 || !strings.Contains(entries[0], "ed25519") {
		t.Fatalf("the trusted key must be remembered as a single ed25519 entry, got %v", entries)
	}
}
