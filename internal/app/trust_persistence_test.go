package app

import (
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// trust_persistence_test.go pins the property the operator's three-step
// instruction actually promises: "trust this router once, and it is REMEMBERED".
//
// Every other test works within ONE process, where a trust that is written and
// then read back cannot show whether it survives a restart. If it does not, the
// operator performs step 1 on every single run — which is indistinguishable from
// the button never working, and is exactly the loop the operator reported
// repeatedly with the same fingerprint (CSPcG8Gu…) over separate sessions.
//
// Three phases, in the order the operator lives them:
//   1. a fresh installer refuses the untrusted host, and trust then succeeds;
//   2. a BRAND NEW installer process, same store, must NOT ask again;
//   3. control: a brand new installer process with an EMPTY store must refuse
//      again — so phase 2 can only pass because of the store.

// buildInstallerForTest builds the real binary once per test.
func buildInstallerForTest(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "installer-e2e")
	// "../.." is the module root: this package (internal/app) is one level below
	// the root main package that carries func main.
	if out, err := exec.Command("go", "build", "-o", bin, "../..").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

// startInstallerProcess runs the real binary against the fixture SSH port with
// the given trust store, and returns its base URL plus a stop function.
func startInstallerProcess(t *testing.T, bin, store, sshPort string) (string, func()) {
	t.Helper()
	httpPort := freePort(t)
	cmd := exec.Command(bin, "-port", httpPort, "-ssh-port", sshPort)
	cmd.Env = append(os.Environ(), "TOLLGATE_KNOWN_HOSTS="+store)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start installer: %v", err)
	}
	base := "http://127.0.0.1:" + httpPort
	waitForWizard(t, base)
	return base, func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}
}

func TestTrustSurvivesANewInstallerProcess(t *testing.T) {
	ed := hostKeySigner(t)
	rsa := rsaHostKeySigner(t)
	ln := startRouterFixture(t, ed, rsa, "hunter2")
	_, sshPort, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("fixture addr: %v", err)
	}
	bin := buildInstallerForTest(t)
	fp := ssh.FingerprintSHA256(ed.PublicKey())
	scan := map[string]string{"ip": "127.0.0.1", "password": "hunter2"}

	// --- phase 1: refused, then trusted -------------------------------------
	store := filepath.Join(t.TempDir(), "known-hosts")
	base1, stop1 := startInstallerProcess(t, bin, store, sshPort)
	code, body := postJSON(t, base1+"/api/wifi-scan", scan)
	msg, _ := body["error"].(string)
	if code != http.StatusBadGateway || !strings.Contains(msg, "not trusted") {
		t.Fatalf("phase 1: expected the untrusted-host refusal first, got HTTP %d %v", code, body)
	}
	t.Logf("phase 1: refused as expected: %s", firstSentence(msg))

	code, body = postJSON(t, base1+"/api/trust-host-key", map[string]string{
		"ip": "127.0.0.1", "fingerprint": fp,
	})
	if code != http.StatusOK || body["trusted"] != true {
		t.Fatalf("phase 1: trust failed: HTTP %d %v", code, body)
	}
	storePath, _ := body["store"].(string)
	t.Logf("phase 1: trusted %s -> HTTP %d store=%s", fp, code, storePath)
	if storePath != store {
		t.Errorf("the operator is told the key went to %q but this run reads %q — a store they cannot find is a store they cannot trust", storePath, store)
	}
	stop1()

	// --- phase 2: a brand new process, same store: must NOT ask again -------
	base2, stop2 := startInstallerProcess(t, bin, store, sshPort)
	defer stop2()
	code, body = postJSON(t, base2+"/api/wifi-scan", scan)
	if code != http.StatusOK {
		t.Fatalf("phase 2: the trusted key did NOT survive a new installer process (HTTP %d %v) — the operator is asked to trust the same router on every run, which is the reported loop",
			code, body)
	}
	t.Logf("phase 2: no re-prompt; scan returned HTTP %d", code)

	// --- phase 3: control — an empty store must refuse again ----------------
	base3, stop3 := startInstallerProcess(t, bin, filepath.Join(t.TempDir(), "empty"), sshPort)
	defer stop3()
	code, body = postJSON(t, base3+"/api/wifi-scan", scan)
	if code != http.StatusBadGateway {
		t.Errorf("control: a fresh store must still refuse the untrusted host, got HTTP %d %v", code, body)
	} else {
		t.Logf("control: an empty store is refused again, so phase 2 passed because of the store")
	}
}

// firstSentence keeps logs readable.
func firstSentence(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 160 {
		return s[:160] + "…"
	}
	return s
}
