package main

// "Don't re-key a passwordless router until the install is actually verified."
//
// Today `ensureRootCredential` GENERATES and SETS a root password on a router
// whose root hash is empty — at step 4, before the package download (step 6),
// the branding, the portal, the LNURL config, the service restarts and the
// health check. Every one of those can fail, and if one does, the operator's
// router has been re-keyed by a deploy that did not finish: its only previous
// access (an empty root password) is gone, replaced by a value they may never
// have seen. Persisting the credential (PR #70) makes that recoverable; not
// re-keying until the work has actually succeeded makes it not happen at all.
//
// New contract:
//   * empty root hash -> generate, PERSIST, and DEFER. No `passwd`, no
//     surrender. Later deploy steps keep using the empty credential that got
//     them in, so nothing downstream changes.
//   * the deferred credential is applied ONCE, by the finalization call that
//     runs after the health check has passed — and only then is it surrendered
//     to the operator (so the failure view's "already been generated and set"
//     claim can never be shown for a credential that was never set).
//   * a deploy that fails before that point leaves the router's access as it
//     found it.
//
// RED against the tree this file was written for: `passwd root` runs inside
// ensureRootCredential.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// deferTestRouter returns a fake router with NO root credential and points the
// recovery log at a temp file, so a test can never touch the operator's real
// ~/.tollgate-root-credentials.
func deferTestRouter(t *testing.T) *fakeCredentialRouter {
	t.Helper()
	t.Setenv("TOLLGATE_CREDENTIAL_FILE", filepath.Join(t.TempDir(), "creds"))
	return &fakeCredentialRouter{hash: rootHashEmpty, passwdOut: "passwd: password changed\n"}
}

// TestEmptyRootPasswordIsNotSetUntilTheInstallIsVerified is the core contract.
func TestEmptyRootPasswordIsNotSetUntilTheInstallIsVerified(t *testing.T) {
	fr := deferTestRouter(t)
	job := newJob("192.168.23.1")

	pw, ok := ensureRootCredential(job, fr.run, fr.proveLogin, "")
	if !ok {
		t.Fatalf("ensureRootCredential failed: %s", job.Error)
	}
	if fr.ranPasswd() {
		t.Fatalf("`passwd root` ran at step 4 on a router with no root password: the router is re-keyed BEFORE the package download, branding, portal, LNURL, service restarts and health check — any of which can fail, leaving a router whose only prior access was an empty root password. Defer the set until the work has succeeded.\ncommands: %v", fr.cmds)
	}
	if fr.hash != rootHashEmpty {
		t.Fatalf("router root hash = %q after step 4, want it untouched (%q)", fr.hash, rootHashEmpty)
	}
	if pw != "" {
		t.Fatalf("ensureRootCredential returned %q as the password later steps must reconnect with; nothing was set, so the empty credential that got the deploy in must be what it returns", pw)
	}
	// Nothing may be surrendered yet: the failure view tells the operator the
	// password "has already been generated and set". Until it is set, that
	// sentence would be false.
	job.mu.Lock()
	surrendered := job.generatedPassword
	job.mu.Unlock()
	if surrendered != "" {
		t.Fatalf("a credential that was never set is already armed for the one-shot failure view (%q)", surrendered)
	}
	// It must still have been persisted — that is what makes the whole path safe.
	if b, err := os.ReadFile(os.Getenv("TOLLGATE_CREDENTIAL_FILE")); err != nil {
		t.Fatalf("the deferred credential was not persisted: %v", err)
	} else if !strings.Contains(string(b), "\t192.168.23.1\t") {
		t.Fatalf("the recovery record does not name the router; file:\n%s", b)
	}
}

// TestFinalizeAppliesTheDeferredCredentialOnce pins the other half: the
// credential IS applied, exactly once, at finalization.
func TestFinalizeAppliesTheDeferredCredentialOnce(t *testing.T) {
	fr := deferTestRouter(t)
	job := newJob("192.168.23.1")

	if _, ok := ensureRootCredential(job, fr.run, fr.proveLogin, ""); !ok {
		t.Fatalf("ensureRootCredential failed: %s", job.Error)
	}
	job.mu.Lock()
	deferred := job.pendingRootPassword
	job.mu.Unlock()
	if deferred == "" {
		t.Fatal("nothing was deferred, so finalization has nothing to apply")
	}

	if !finalizeRootCredential(job, fr.run, fr.proveLogin) {
		t.Fatalf("finalizeRootCredential failed: %s", job.Error)
	}
	if !fr.ranPasswd() {
		t.Fatal("`passwd root` never ran — the router was left with NO root password after a SUCCESSFUL deploy, which is worse than the lockout this change exists to prevent")
	}
	if fr.hash != rootHashSet {
		t.Fatalf("root hash = %q after finalization, want %q", fr.hash, rootHashSet)
	}
	if fr.live != deferred {
		t.Fatalf("the router accepts %q but the deploy generated %q — the operator would be handed a credential the router rejects", fr.live, deferred)
	}

	// Now, and only now, the value is surrendered to the operator.
	job.mu.Lock()
	armed, pending := job.generatedPassword, job.pendingRootPassword
	job.Status = "done"
	job.mu.Unlock()
	if armed != deferred {
		t.Fatalf("the applied credential was not armed for the one-shot view (armed %q, applied %q)", armed, deferred)
	}
	if pending != "" {
		t.Fatalf("the pending credential was not cleared after being applied (%q) — a later finalization could set it twice", pending)
	}
	const jobID = "finalize-credential-job"
	registerStatusJob(t, jobID, job)
	if got := readStatus(t, jobID)["generated_password"]; got != deferred {
		t.Fatalf("a completed deploy did not surrender the credential it set (got %v, want %q)", got, deferred)
	}
}

// TestAFailedDeployLeavesTheRoutersAccessUntouched is the point of the change.
func TestAFailedDeployLeavesTheRoutersAccessUntouched(t *testing.T) {
	fr := deferTestRouter(t)
	job := newJob("192.168.23.1")

	if _, ok := ensureRootCredential(job, fr.run, fr.proveLogin, ""); !ok {
		t.Fatalf("ensureRootCredential failed: %s", job.Error)
	}
	// ...step 6 (package download) fails; finalization is never reached.
	job.mu.Lock()
	job.Status = "failed"
	job.Error = "step 6: could not download the tollgate-wrt package"
	job.mu.Unlock()

	if fr.ranPasswd() {
		t.Fatal("a FAILED deploy still re-keyed the router")
	}
	if fr.hash != rootHashEmpty {
		t.Fatalf("a FAILED deploy changed the router's root hash to %q — the operator's empty-password access is gone", fr.hash)
	}
	const jobID = "failed-deferred-job"
	registerStatusJob(t, jobID, job)
	if got := readStatus(t, jobID)["generated_password"]; got != nil && got != "" {
		t.Fatalf("the failed view was handed a credential (%v) that was never set on the router, while its copy says the password \"had already been generated and set\"", got)
	}
}

// TestSuppliedPasswordIsStillAppliedImmediately is the control: the deferral is
// ONLY for the generated case. A password the operator supplied must still be
// applied at step 4, because every later step may need to reconnect with it and
// the operator already knows the value — deferring it would change the meaning
// of a field they explicitly filled in.
func TestSuppliedPasswordIsStillAppliedImmediately(t *testing.T) {
	t.Setenv("TOLLGATE_CREDENTIAL_FILE", filepath.Join(t.TempDir(), "creds"))
	fr := &fakeCredentialRouter{hash: rootHashSet, passwdOut: "passwd: password changed\n"}
	job := newJob("192.168.23.1")

	pw, ok := ensureRootCredential(job, fr.run, fr.proveLogin, "SuppliedPw1234567890")
	if !ok {
		t.Fatalf("ensureRootCredential failed: %s", job.Error)
	}
	if pw != "SuppliedPw1234567890" {
		t.Fatalf("returned password = %q, want the supplied one", pw)
	}
	if !fr.ranPasswd() {
		t.Fatal("a SUPPLIED password was deferred; it must be applied immediately")
	}
	job.mu.Lock()
	armed := job.generatedPassword
	job.mu.Unlock()
	if armed != "" {
		t.Fatalf("a supplied password was armed as a GENERATED one-shot credential (%q) — it is not a secret the wizard created and must not be re-served", armed)
	}
}
