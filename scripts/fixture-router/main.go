// fixture-router: a minimal SSH server that stands in for an OpenWrt router in
// the process-level test `scripts/test-missing-asset-fails-loudly.sh` (C2-I-02).
//
// It answers every "exec" request by running the command through /bin/sh
// (busybox, inside the alpine container), so the commands the real installer
// sends behave the way they would on a router whose filesystem has been seeded
// with the fake OpenWrt identity files (/etc/openwrt_release,
// /tmp/sysinfo/board_name).
//
// This is a TEST FIXTURE. It accepts any password and generates a fresh host
// key on every start, and it runs on port 22 inside its own container network
// namespace so it can coexist with the host's real sshd. Never run it outside a
// throwaway container.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"sync"

	"golang.org/x/crypto/ssh"
)

var (
	mu     sync.Mutex
	seenFn *os.File
)

func logCommand(cmd string) {
	mu.Lock()
	defer mu.Unlock()
	line := fmt.Sprintf("STUB-ROUTER exec: %s\n", cmd)
	os.Stdout.WriteString(line)
	if seenFn != nil {
		seenFn.WriteString(line)
	}
}

func runCommand(cmdLine string) string {
	logCommand(cmdLine)
	cmd := exec.Command("/bin/sh", "-c", cmdLine)
	out, err := cmd.CombinedOutput()
	res := string(out)
	if err != nil {
		res += fmt.Sprintf("\n[stub: exit=%v]", err)
	}
	return res
}

func handleSession(ch ssh.Channel, reqs <-chan *ssh.Request) {
	defer ch.Close()
	for req := range reqs {
		switch req.Type {
		case "exec":
			var payload struct{ Command string }
			if err := ssh.Unmarshal(req.Payload, &payload); err != nil {
				req.Reply(false, nil)
				continue
			}
			req.Reply(true, nil)
			out := runCommand(payload.Command)
			io.WriteString(ch, out)
			ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
			return
		case "shell", "pty-req":
			req.Reply(false, nil)
		default:
			req.Reply(false, nil)
		}
	}
}

func main() {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		log.Fatalf("host key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		log.Fatalf("signer: %v", err)
	}
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(meta ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			mu.Lock()
			os.Stdout.WriteString(fmt.Sprintf("STUB-ROUTER auth: user=%s password=%q\n", meta.User(), string(pw)))
			mu.Unlock()
			return nil, nil // accept everything: this is a fixture
		},
	}
	cfg.AddHostKey(signer)

	if p := os.Getenv("STUB_CMD_LOG"); p != "" {
		seenFn, _ = os.Create(p)
	}
	ln, err := net.Listen("tcp", "0.0.0.0:22")
	if err != nil {
		log.Fatalf("listen :22: %v", err)
	}
	log.Printf("stub router listening on %s (host key fingerprint %s)", ln.Addr(), ssh.FingerprintSHA256(signer.PublicKey()))
	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Fatalf("accept: %v", err)
		}
		go func() {
			sconn, chans, reqs, err := ssh.NewServerConn(conn, cfg)
			if err != nil {
				return
			}
			_ = sconn
			go ssh.DiscardRequests(reqs)
			for newChan := range chans {
				if newChan.ChannelType() != "session" {
					newChan.Reject(ssh.UnknownChannelType, "only session channels")
					continue
				}
				ch, chReqs, err := newChan.Accept()
				if err != nil {
					continue
				}
				go handleSession(ch, chReqs)
			}
		}()
	}
}
