// Command fakerouter is a stand-in for a stock OpenWrt router's dropbear, for the
// browser (Playwright) and HTTP end-to-end tests of the setup wizard.
//
// The wizard's fixtures inside the Go tests run in-process; a browser test cannot
// use those, so this exposes the same fixture as a process: it serves BOTH host
// key types the way dropbear does, requires a root password, and answers the
// console commands the wizard runs (interface discovery, the WiFi scan chain).
//
// It prints two machine-readable lines on stdout so a driver script never has to
// guess or hard-code them:
//
//	FAKEROUTER_PORT=<port>
//	FAKEROUTER_FINGERPRINT=SHA256:<base64>   (the ed25519 key, as the wizard shows it)
//
// Nothing here is part of the shipping installer: `go build .` builds only the
// wizard, and this command exists for tests.
package main

import (
	"crypto/rand"
	"crypto/ed25519"
	"crypto/rsa"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"strings"

	"golang.org/x/crypto/ssh"
)

func mustEd25519() ssh.Signer {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		log.Fatalf("ed25519 key: %v", err)
	}
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		log.Fatalf("ed25519 signer: %v", err)
	}
	return s
}

func mustRSA() ssh.Signer {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		log.Fatalf("rsa key: %v", err)
	}
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		log.Fatalf("rsa signer: %v", err)
	}
	return s
}

func main() {
	port := flag.String("port", "0", "listen port (0 = pick a free one)")
	password := flag.String("password", "hunter2", "the root password to accept")
	flag.Parse()

	ed, rsaKey := mustEd25519(), mustRSA()
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(_ ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			if string(pw) == *password {
				return nil, nil
			}
			return nil, fmt.Errorf("fakerouter: wrong password")
		},
	}
	cfg.AddHostKey(ed)
	cfg.AddHostKey(rsaKey)

	ln, err := net.Listen("tcp", "127.0.0.1:"+*port)
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	fmt.Printf("FAKEROUTER_PORT=%d\n", ln.Addr().(*net.TCPAddr).Port)
	fmt.Printf("FAKEROUTER_FINGERPRINT=%s\n", ssh.FingerprintSHA256(ed.PublicKey()))
	_ = os.Stdout.Sync()

	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go serve(conn, cfg)
	}
}

func serve(c net.Conn, cfg *ssh.ServerConfig) {
	defer c.Close()
	_, chans, reqs, err := ssh.NewServerConn(c, cfg)
	if err != nil {
		return
	}
	go ssh.DiscardRequests(reqs)
	for newCh := range chans {
		if newCh.ChannelType() != "session" {
			_ = newCh.Reject(ssh.UnknownChannelType, "fakerouter")
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
					req.Reply(true, nil)
					continue
				}
				var payload struct{ Command string }
				if err := ssh.Unmarshal(req.Payload, &payload); err != nil {
					req.Reply(false, nil)
					return
				}
				req.Reply(true, nil)
				log.Printf("fakerouter exec: %s", payload.Command)
				_, _ = ch.Write([]byte(reply(payload.Command)))
				_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
				return
			}
		}()
	}
}

// reply is the canned console output of a stock OpenWrt router for the commands
// the wizard runs while scanning for upstream WiFi.
func reply(cmd string) string {
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
		return "phy#0\n\tInterface wlan0\n\t\tifindex 3\n\t\twdev 0x1\n" +
			"\t\taddr 00:11:22:33:44:55\n\t\ttype managed\n" +
			"\t\tchannel 6 (2437 MHz), width: 20 MHz\n"
	}
}
