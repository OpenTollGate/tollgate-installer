package app

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"net"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// dualKeyServer stands up an in-process SSH server offering BOTH ed25519 and
// rsa host keys, with rsa added FIRST. That is the worst case for a client
// that does not pin a host-key preference, and it reproduces the 2026-09-28
// field bug hermetically: an OpenWrt box serves both, the operator verified
// ed25519 on the console, and the unconstrained client negotiated rsa.
func dualKeyServer(t *testing.T) (string, func()) {
	t.Helper()

	_, edPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519 keygen: %v", err)
	}
	edSigner, err := ssh.NewSignerFromKey(edPriv)
	if err != nil {
		t.Fatalf("ed25519 signer: %v", err)
	}
	rsaPriv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa keygen: %v", err)
	}
	rsaSigner, err := ssh.NewSignerFromKey(rsaPriv)
	if err != nil {
		t.Fatalf("rsa signer: %v", err)
	}

	cfg := &ssh.ServerConfig{NoClientAuth: true}
	cfg.AddHostKey(rsaSigner) // rsa FIRST: maximum temptation to pick wrong
	cfg.AddHostKey(edSigner)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				ssh.NewServerConn(conn, cfg)
			}()
		}
	}()
	return ln.Addr().String(), func() { ln.Close() }
}

// TestHostKeyPreferenceNegotiatesEd25519 is the regression guard for the bug
// that cost a full day: an unconstrained client picked rsa and could never
// match an ed25519 pin.
func TestHostKeyPreferenceNegotiatesEd25519(t *testing.T) {
	addr, stop := dualKeyServer(t)
	defer stop()

	var gotType string
	cfg := &ssh.ClientConfig{
		User:              "root",
		HostKeyAlgorithms: sshHostKeyAlgorithms, // the code under test
		Timeout:           5 * time.Second,
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			gotType = key.Type()
			return nil
		},
	}
	client, err := ssh.Dial("tcp", addr, cfg)
	if err == nil {
		client.Close()
	} else if gotType == "" {
		t.Fatalf("dial failed before host key was seen: %v", err)
	}

	if gotType != ssh.KeyAlgoED25519 {
		t.Fatalf("negotiated host key type = %q, want %q "+
			"(an unconstrained client picks rsa; the operator's console check reports ed25519)",
			gotType, ssh.KeyAlgoED25519)
	}
}

// TestHostKeyPreferenceIsED25519First pins the order itself, so a future edit
// cannot quietly demote ed25519 and reintroduce the mismatch.
func TestHostKeyPreferenceIsED25519First(t *testing.T) {
	if len(sshHostKeyAlgorithms) == 0 {
		t.Fatal("sshHostKeyAlgorithms must not be empty")
	}
	if sshHostKeyAlgorithms[0] != ssh.KeyAlgoED25519 {
		t.Fatalf("first preference = %q, want %q", sshHostKeyAlgorithms[0], ssh.KeyAlgoED25519)
	}
}

// TestUnconstrainedNegotiationPicksRSA documents the PRE-FIX bug shape AND
// guards the fixture itself: with no HostKeyAlgorithms the client negotiates
// rsa. If this ever stops holding, the dual-key fixture no longer reproduces
// the field bug and the pinned-preference test above proves nothing.
func TestUnconstrainedNegotiationPicksRSA(t *testing.T) {
	addr, stop := dualKeyServer(t)
	defer stop()

	var gotType string
	cfg := &ssh.ClientConfig{
		User:    "root",
		Timeout: 5 * time.Second,
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			gotType = key.Type()
			return nil
		},
	}
	if client, err := ssh.Dial("tcp", addr, cfg); err == nil {
		client.Close()
	}
	if gotType == "" {
		t.Fatal("no host key observed during handshake")
	}
	if gotType == ssh.KeyAlgoED25519 {
		t.Errorf("unconstrained client negotiated ed25519; the fixture no longer " +
			"reproduces the 2026-09-28 bug, so the preference test above is vacuous")
	}
	t.Logf("documented pre-fix behaviour: unconstrained negotiation -> %s", gotType)
}
