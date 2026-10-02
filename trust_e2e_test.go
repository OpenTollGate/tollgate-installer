package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// startDropbearLikeFixture serves BOTH host key types on an ephemeral port, the
// way a stock OpenWrt dropbear does, and completes handshakes without requiring
// auth (the trust dial authenticates nothing — it only verifies the host key).
func startDropbearLikeFixture(t *testing.T, ed, rsa ssh.Signer) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("fixture listen: %v", err)
	}
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(ssh.ConnMetadata, []byte) (*ssh.Permissions, error) {
			return nil, nil
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
			go func(c net.Conn) {
				defer c.Close()
				_, chans, reqs, err := ssh.NewServerConn(c, cfg)
				if err != nil {
					return
				}
				go ssh.DiscardRequests(reqs)
				for ch := range chans {
					_ = ch.Reject(ssh.UnknownChannelType, "fixture")
				}
			}(conn)
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return ln
}

func postJSON(t *testing.T, url string, body any) (int, map[string]any) {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(string(b)))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	if out == nil {
		out = map[string]any{"_raw": string(raw)}
	}
	return res.StatusCode, out
}

// freePort returns a TCP port nothing else can hold: bind, read the number, close.
func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("pick port: %v", err)
	}
	defer ln.Close()
	return fmt.Sprint(ln.Addr().(*net.TCPAddr).Port)
}

// TestTrustButtonEndToEnd is the regression test for the operator-reported
// repeating `502 Bad Gateway` on POST /api/trust-host-key.
//
// It drives the REAL binary — built here, started as a process, spoken to over
// HTTP exactly as the browser does — against a fixture that serves both an
// ed25519 and an rsa host key, like dropbear. The property under test is the one
// that broke: **the fingerprint the wizard tells the operator to verify must be
// the same key the Trust action then negotiates.** When trustHostKeyForHost had
// no HostKeyAlgorithms it negotiated the rsa key, compared it against the ed25519
// fingerprint the scan had just reported, and refused — so the button 502'd
// forever, on every stock router, no matter how often it was clicked.
//
// `-ssh-port` exists so this can run anywhere: without it the binary would dial
// a privileged port 22 that a test host cannot bind.
func TestTrustButtonEndToEnd(t *testing.T) {
	ed := hostKeySigner(t)
	rsa := rsaHostKeySigner(t)
	ln := startDropbearLikeFixture(t, ed, rsa)
	_, fixturePort, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("fixture addr: %v", err)
	}

	bin := filepath.Join(t.TempDir(), "installer-e2e")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	httpPort := freePort(t)

	store := filepath.Join(t.TempDir(), "known-hosts")
	base := "http://127.0.0.1:" + httpPort

	startServer := func() *exec.Cmd {
		t.Helper()
		cmd := exec.Command(bin, "-port", httpPort, "-ssh-port", fixturePort)
		cmd.Env = append(os.Environ(), "TOLLGATE_KNOWN_HOSTS="+store)
		cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
		if err := cmd.Start(); err != nil {
			t.Fatalf("start installer: %v", err)
		}
		t.Cleanup(func() {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		})
		// readiness: the wizard serves its index
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			res, err := (&http.Client{Timeout: 2 * time.Second}).Get(base + "/")
			if err == nil {
				res.Body.Close()
				return cmd
			}
			time.Sleep(150 * time.Millisecond)
		}
		t.Fatal("installer HTTP server did not come up")
		return nil
	}
	startServer()

	routerIP := "127.0.0.1"

	// 1. The scan: the wizard reports the fingerprint it observed. It dials
	//    ed25519-first, so on a dual-key router that is the ed25519 key.
	wantFP := ssh.FingerprintSHA256(ed.PublicKey())
	code, ident := postJSON(t, base+"/api/identify", map[string]any{"ip": routerIP})
	if code != http.StatusOK {
		t.Fatalf("identify: HTTP %d (%v)", code, ident)
	}
	gotFP, _ := ident["ssh_fingerprint"].(string)
	if gotFP != wantFP {
		t.Fatalf("the wizard must report the ed25519 key it negotiates ed25519-first, got %q want %q", gotFP, wantFP)
	}

	// 2. THE REGRESSION. Clicking Trust with the fingerprint the operator was
	//    just shown must succeed. Before the fix this returned 502 with
	//    "presents SHA256:… (rsa) but SHA256:… (ed25519) was supplied".
	code, body := postJSON(t, base+"/api/trust-host-key", map[string]any{"ip": routerIP, "fingerprint": wantFP})
	if code != http.StatusOK {
		t.Fatalf("Trust with the fingerprint the wizard itself reported failed: HTTP %d %v\n"+
			"this is the 2026-10-02 defect: the Trust dial negotiated a DIFFERENT host key than the scan", code, body)
	}
	if ok, _ := body["trusted"].(bool); !ok {
		t.Fatalf("trust response did not confirm the key was trusted: %v", body)
	}

	// 3. It must be remembered as the negotiated type, and the next scan must be
	//    clean — otherwise the operator is stuck in the refuse/trust loop.
	entries := storeKeyEntriesForHost(store, net.JoinHostPort(routerIP, fixturePort))
	if len(entries) != 1 || !strings.Contains(entries[0], "ed25519") {
		t.Fatalf("the trusted key must be remembered as one ed25519 entry, got %v", entries)
	}
	_, identAgain := postJSON(t, base+"/api/identify", map[string]any{"ip": routerIP})
	if _, refused := identAgain["ssh_refusal"]; refused {
		t.Fatalf("a router trusted in this store must not be refused on the next scan: %v", identAgain)
	}

	// 4. Control: a fingerprint the router does NOT present must never be
	//    accepted, or "trust" would be a rubber stamp.
	fresh := filepath.Join(t.TempDir(), "known-hosts-2")
	httpPort2 := freePort(t)
	cmd := exec.Command(bin, "-port", httpPort2, "-ssh-port", fixturePort)
	cmd.Env = append(os.Environ(), "TOLLGATE_KNOWN_HOSTS="+fresh)
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatalf("start second installer: %v", err)
	}
	defer func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() }()
	second := "http://127.0.0.1:" + httpPort2
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if res, err := (&http.Client{Timeout: 2 * time.Second}).Get(second + "/"); err == nil {
			res.Body.Close()
			break
		}
		time.Sleep(150 * time.Millisecond)
	}
	code, body = postJSON(t, second+"/api/trust-host-key", map[string]any{"ip": routerIP, "fingerprint": ssh.FingerprintSHA256(rsa.PublicKey())})
	if code == http.StatusOK {
		t.Fatalf("a host key the router does not present must be refused, got HTTP 200 %v", body)
	}
	if len(storeKeyEntriesForHost(fresh, net.JoinHostPort(routerIP, fixturePort))) != 0 {
		t.Fatal("a refused fingerprint must not be written to the trust store")
	}
}
