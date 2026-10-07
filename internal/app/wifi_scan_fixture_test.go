package app

import (
	"fmt"
	"net"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// ---- a dropbear-like router fixture that REQUIRES a password ----------------
//
// The existing fixtures complete handshakes without needing auth (the trust dial
// authenticates nothing). This one is the state the operator's router is in: both
// host key types, and a root password that must be presented. Without it the
// password path cannot be exercised at all.

// fixtureReply is the canned console output of a stock OpenWrt router for the
// commands the WiFi scan path runs.
func fixtureReply(cmd string) string {
	switch {
	case strings.Contains(cmd, "scan"):
		return "Cell 01 - Address: AA:BB:CC:DD:EE:FF\n" +
			"          ESSID: \"TollGate-Test-Net\"\n" +
			"          Mode: Master\n" +
			"          Channel: 6\n" +
			"          Signal: -42 dBm\n" +
			"          Encryption: WPA2 PSK (CCMP)\n"
	case strings.Contains(cmd, "ubus"):
		return `{"radio0":{"up":true,"interfaces":[{"ifname":"wlan0","config":{"mode":"ap"}}]}}` + "\n"
	case strings.Contains(cmd, "ieee80211"):
		return "phy0\n"
	default:
		// An `iw dev` block. The default matters: it answers every "is there a
		// wireless interface?" probe, so enableWifiAndWait does not burn its
		// 10 x 1.5s budget in a test.
		return "phy#0\n\tInterface wlan0\n\t\tifindex 3\n\t\twdev 0x1\n" +
			"\t\taddr 00:11:22:33:44:55\n\t\ttype managed\n" +
			"\t\tchannel 6 (2437 MHz), width: 20 MHz\n"
	}
}

// startRouterFixture serves BOTH host key types the way stock OpenWrt dropbear
// does, accepts ONLY the given password, and answers the WiFi scan command chain.
func startRouterFixture(t *testing.T, ed, rsa ssh.Signer, accept string) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("fixture listen: %v", err)
	}
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(_ ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			if accept != "" && string(pw) == accept {
				return nil, nil
			}
			return nil, fmt.Errorf("fixture: password rejected")
		},
	}
	cfg.AddHostKey(ed)
	cfg.AddHostKey(rsa)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go serveFixtureConn(conn, cfg)
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return ln
}

func serveFixtureConn(c net.Conn, cfg *ssh.ServerConfig) {
	defer c.Close()
	_, chans, reqs, err := ssh.NewServerConn(c, cfg)
	if err != nil {
		return
	}
	go ssh.DiscardRequests(reqs)
	for newCh := range chans {
		if newCh.ChannelType() != "session" {
			_ = newCh.Reject(ssh.UnknownChannelType, "fixture")
			continue
		}
		ch, chReqs, err := newCh.Accept()
		if err != nil {
			continue
		}
		go func() {
			defer ch.Close()
			for req := range chReqs {
				if req.Type != "exec" {
					req.Reply(true, nil) // pty-req / env are answered, not errors
					continue
				}
				var payload struct{ Command string }
				if err := ssh.Unmarshal(req.Payload, &payload); err != nil {
					req.Reply(false, nil)
					return
				}
				req.Reply(true, nil)
				_, _ = ch.Write([]byte(fixtureReply(payload.Command)))
				_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
				return
			}
		}()
	}
}

// trustFixtureFor puts the fixture's host key in the store and points the dial
// port at it — the state production is in AFTER a successful Trust click. This
// is deliberate: without it every connect is refused at the HOST-KEY stage and
// the password path under test would never be reached.
func trustFixtureFor(t *testing.T, ln net.Listener, key ssh.PublicKey) (ip string) {
	t.Helper()
	knownHostsStore(t)
	host, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("fixture addr: %v", err)
	}
	old := sshDialPort
	sshDialPort = port
	t.Cleanup(func() { sshDialPort = old })
	if err := rememberHostKey(net.JoinHostPort(host, port), key); err != nil {
		t.Fatalf("seed trust store: %v", err)
	}
	return host
}

// TestPasswordFailureIsNamed is the regression test for the operator report of
//
//	XHR POST http://localhost:8099/api/wifi-scan  [HTTP/1.1 502 Bad Gateway]
//	{"error":"cannot connect to router via SSH"}
//
// sshConnect returns only a *ssh.Client, so the dial/auth error was discarded and
// every non-host-key failure — wrong password, nothing listening, no route —
// collapsed into that one sentence. The operator could not tell a missing root
// password from a dead network, and the wizard's automatic WiFi scan (fired when
// the Repeater tab is selected, before a password is typed) produced it every
// time on any router that has a root password.
func TestPasswordFailureIsNamed(t *testing.T) {
	ed := hostKeySigner(t)
	rsa := rsaHostKeySigner(t)
	ln := startRouterFixture(t, ed, rsa, "hunter2")
	ip := trustFixtureFor(t, ln, ed.PublicKey())

	const fallback = "cannot connect to router via SSH"

	// 1. No password at all — exactly what the automatic scan sends.
	if c := sshConnect(ip, ""); c != nil {
		c.Close()
		t.Fatalf("connected with no password to a router that requires one")
	}
	got := sshConnectFailureMessage(ip, fallback)
	if !strings.Contains(got, "root password") {
		t.Errorf("the failure does not name the root password, so the operator has nothing to act on:\n  %q", got)
	}
	if !strings.Contains(got, fallback) {
		t.Errorf("the cause must EXTEND the existing sentence, not replace it:\n  %q", got)
	}

	// 2. A rejected password must not read as a network problem either.
	if c := sshConnect(ip, "wrong-password"); c != nil {
		c.Close()
		t.Fatalf("connected with a wrong password")
	}
	if got := sshConnectFailureMessage(ip, fallback); !strings.Contains(got, "root password") {
		t.Errorf("a rejected password is not named:\n  %q", got)
	}

	// 3. Control, same fixture: the correct password connects and leaves no
	//    recorded failure, so the classifier cannot fire on a healthy connect.
	c := sshConnect(ip, "hunter2")
	if c == nil {
		t.Fatalf("the correct password did not connect")
	}
	c.Close()
	if got := sshConnectFailureMessage(ip, fallback); got != fallback {
		t.Errorf("a successful connect must leave the plain fallback, got:\n  %q", got)
	}
}
