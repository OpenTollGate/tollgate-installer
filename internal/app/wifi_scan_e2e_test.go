package app

import (
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

// waitForWizard blocks until the installer's HTTP server answers, so the test
// never races process startup.
func waitForWizard(t *testing.T, base string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		res, err := (&http.Client{Timeout: 2 * time.Second}).Get(base + "/api/config")
		if err == nil {
			res.Body.Close()
			if res.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("the installer never started serving on %s", base)
}

// TestWifiScanEndToEnd drives the REAL binary — built here, started as a process,
// spoken to over HTTP exactly as the browser does — through the whole WiFi
// Repeater path against a dropbear-like fixture router that requires a root
// password.
//
// It is the end-to-end proof for the operator's report:
//
//	XHR POST http://localhost:8099/api/wifi-scan  [HTTP/1.1 502 Bad Gateway]
//	{"error":"cannot connect to router via SSH"}
//
// Three properties, in the order the operator experiences them:
//
//  1. Trust succeeds for the fingerprint the scan reported (the earlier fix, kept
//     under test here so a regression cannot hide behind this one).
//  2. An automatic scan with NO password names the password — the failure that
//     used to be a dead end.
//  3. WITH the router password, the scan returns real networks.
func TestWifiScanEndToEnd(t *testing.T) {
	ed := hostKeySigner(t)
	rsa := rsaHostKeySigner(t)
	ln := startRouterFixture(t, ed, rsa, "hunter2")
	_, fixturePort, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("fixture addr: %v", err)
	}

	bin := filepath.Join(t.TempDir(), "installer-e2e")
	// "../.." is the module root: this package (internal/app) is one level below
	// the root main package that carries func main.
	if out, err := exec.Command("go", "build", "-o", bin, "../..").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	httpPort := freePort(t)
	cmd := exec.Command(bin, "-port", httpPort, "-ssh-port", fixturePort)
	cmd.Env = append(os.Environ(),
		"TOLLGATE_KNOWN_HOSTS="+filepath.Join(t.TempDir(), "known-hosts"))
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start installer: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	base := "http://127.0.0.1:" + httpPort
	waitForWizard(t, base)
	router := map[string]string{"ip": "127.0.0.1"}

	// 1. Trust the fingerprint the scan reported (ed25519, as dropbear shows).
	fp := ssh.FingerprintSHA256(ed.PublicKey())
	code, body := postJSON(t, base+"/api/trust-host-key", map[string]string{
		"ip": router["ip"], "fingerprint": fp,
	})
	if code != http.StatusOK || body["trusted"] != true {
		t.Fatalf("trusting %s failed: HTTP %d %v", fp, code, body)
	}
	t.Logf("trusted %s -> HTTP %d store=%v", fp, code, body["store"])

	// 2. The Repeater tab scans before any password is typed. This must say what
	//    is wrong instead of "cannot connect to router via SSH".
	code, body = postJSON(t, base+"/api/wifi-scan", map[string]string{
		"ip": router["ip"], "password": "",
	})
	msg, _ := body["error"].(string)
	t.Logf("passwordless scan -> HTTP %d error=%q", code, msg)
	if code != http.StatusBadGateway {
		t.Errorf("a passwordless scan against a password-protected router must be refused, got HTTP %d", code)
	}
	if !strings.Contains(msg, "root password") {
		t.Errorf("the refusal does not name the root password — the operator is back to a dead end:\n  %q", msg)
	}

	// 3. With the router password, the scan runs and returns the networks.
	code, body = postJSON(t, base+"/api/wifi-scan", map[string]string{
		"ip": router["ip"], "password": "hunter2",
	})
	if code != http.StatusOK {
		t.Fatalf("wifi-scan with the correct password: HTTP %d %v", code, body)
	}
	ssids, _ := body["ssids"].([]any)
	if len(ssids) == 0 {
		t.Fatalf("the scan returned no networks: %v", body)
	}
	first, _ := ssids[0].(map[string]any)
	if first["name"] != "TollGate-Test-Net" {
		t.Errorf("wrong SSID parsed: %v", first)
	}
	t.Logf("scan with password -> HTTP %d strategy=%v ssids=%v", code, body["strategy"], ssids)
}
