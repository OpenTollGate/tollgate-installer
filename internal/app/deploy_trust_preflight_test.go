package app

import (
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// deploy_trust_preflight_test.go pins the operator-facing fix for the flow they
// objected to. Quote: "1. Trust this router's SSH host key → confirm. 2. … 3.
// Type the Router password, press Deploy TollGate — please fix it."
//
// Those were three manual steps of MY making: the operator had to notice the
// refusal, find a separate Trust button, confirm a dialog, and only THEN press
// Deploy. The check itself is worth keeping (a password is about to be sent to
// that address), but requiring a separate errand for it is what made the
// installer feel broken — and each of the earlier defects hid inside that gap.
//
// The check now lives in the deploy path: Deploy asks ONCE, naming the
// fingerprint, and proceeds into the install. Same decision, one action.
//
// Two properties:
//   A. startDeploy confirms the host key BEFORE it starts the job, and honours a
//      refusal to confirm (a declined dialog must not deploy);
//   B. the confirmation reads the CURRENT state from the server, structurally —
//      not a cached one, and not by parsing prose.

// A. The trust confirmation is inside the deploy entry point.
func TestStartDeployConfirmsTrustBeforeStartingTheJob(t *testing.T) {
	body := jsFuncBody(t, string(indexHTML), "async function startDeploy(")
	pre := strings.Index(body, "await ensureRouterHostKeyTrusted(")
	if pre < 0 {
		t.Fatalf("startDeploy never confirms the router's host key: the operator is back to a separate Trust errand before they can deploy")
	}
	post := strings.Index(body, "fetch('/api/deploy'")
	if post < 0 {
		t.Fatalf("startDeploy no longer starts a deploy")
	}
	if pre > post {
		t.Errorf("the host-key confirmation runs AFTER the deploy request is sent — too late to protect the credential")
	}
	if !strings.Contains(body, "if (!(await ensureRouterHostKeyTrusted(") {
		t.Errorf("startDeploy ignores the confirmation's result: declining the dialog must NOT deploy")
	}
}

// B. What the confirmation is allowed to trust, and where it gets it.
func TestEnsureRouterHostKeyTrustedContract(t *testing.T) {
	src := string(indexHTML)
	if !strings.Contains(src, "async function ensureRouterHostKeyTrusted(") {
		t.Fatalf("index.html has no ensureRouterHostKeyTrusted() helper to fold trust into the deploy")
	}
	body := jsFuncBody(t, src, "async function ensureRouterHostKeyTrusted(")
	for _, want := range []struct{ frag, why string }{
		{"/api/identify", "must ask the server for the CURRENT refusal instead of trusting whatever the page cached"},
		{"ssh_refusal", "must know whether there is a refusal at all (absent = none, as the server omits it when the connect succeeds)"},
		{"ssh_fingerprint", "must read the fingerprint structurally, never out of the prose"},
		{"window.confirm", "must put the exact fingerprint in front of the operator before trusting it"},
		{"/api/trust-host-key", "must be able to perform the trust it just asked about"},
		{"return true", "a router with nothing to confirm must proceed to the deploy"},
		{"return false", "a declined confirmation, or a failed trust, must stop the deploy"},
	} {
		if !strings.Contains(body, want.frag) {
			t.Errorf("ensureRouterHostKeyTrusted: %s", want.why)
		}
	}
}

// End-to-end, over HTTP, of exactly the request sequence the new pre-flight
// performs — against the real binary and a fixture router that requires a
// password. The server half of this flow is what the UI now depends on, so it is
// pinned end to end rather than assumed.
func TestTrustPreflightSequenceOverHTTP(t *testing.T) {
	ed := hostKeySigner(t)
	rsa := rsaHostKeySigner(t)
	ln := startRouterFixture(t, ed, rsa, "hunter2")
	_, sshPort, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("fixture addr: %v", err)
	}
	bin := buildInstallerForTest(t)
	base, stop := startInstallerProcess(t, bin, filepath.Join(t.TempDir(), "known-hosts"), sshPort)
	defer stop()

	router := map[string]string{"ip": "127.0.0.1", "password": "hunter2"}

	// 1. The pre-flight's identify: the refusal AND the fingerprint, structurally.
	code, body := postJSON(t, base+"/api/identify", router)
	if code != http.StatusOK {
		t.Fatalf("identify: HTTP %d %v", code, body)
	}
	fp, _ := body["ssh_fingerprint"].(string)
	refusal, _ := body["ssh_refusal"].(string)
	if refusal == "" || fp == "" {
		t.Fatalf("an untrusted router must report BOTH ssh_refusal and ssh_fingerprint, got %v", body)
	}
	if fp != ssh.FingerprintSHA256(ed.PublicKey()) {
		t.Errorf("the fingerprint offered for confirmation (%s) is not the key the router presents", fp)
	}
	t.Logf("pre-flight identify -> refusal present, fingerprint %s", fp)

	// 2. Confirm and trust — the one action the operator now takes.
	code, body = postJSON(t, base+"/api/trust-host-key", map[string]string{"ip": "127.0.0.1", "fingerprint": fp})
	if code != http.StatusOK || body["trusted"] != true {
		t.Fatalf("trust: HTTP %d %v", code, body)
	}

	// 3. The deploy the operator was trying to run: the same connect it needs now
	//    succeeds (proved by the scan, which uses the identical sshConnect path).
	code, body = postJSON(t, base+"/api/wifi-scan", router)
	if code != http.StatusOK {
		t.Fatalf("after confirming trust, the connect the deploy needs still fails: HTTP %d %v", code, body)
	}
	ssids, _ := body["ssids"].([]any)
	if len(ssids) == 0 {
		t.Fatalf("no networks returned after trusting: %v", body)
	}
	t.Logf("no separate errand, no second button: HTTP %d with %d network(s)", code, len(ssids))
}
