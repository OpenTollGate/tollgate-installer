package main

// Follow-up tests on OpenTollGate/tollgate-installer#46 (merged head acf8a64c;
// re-review t_d84d4928): "applyRootPassword proves hash-existence, not that THIS
// password took".
//
// applyRootPassword used to accept "the re-read of /etc/shadow reports a real
// hash" as success. On a set→set transition — the router already had a root
// password and the operator supplied a new one — a passwd that fails silently
// leaves the OLD hash in place, and the re-probe then sees exactly the `set`
// state it treats as success: the wizard reported a password nothing on the
// router accepts, and every later step (STA reconnect, re-auth) carried the
// same dead credential.
//
// The tests below drive the deploy path with a fake router that models BOTH
// channels: the command channel (shadow probe + passwd, where "live" moves only
// when passwd really takes) and a fresh-login channel that reproduces the
// router's real authentication, the empty-hash premise included. The real SSH
// proof (proveRootPassword) is measured against a real in-process SSH server in
// password_ssh_proof_test.go.
//
// The two RED-at-base cases are the set→set ones
// (TestSuppliedPasswordSetToSetIsProvenOnAFreshLogin,
// TestSuppliedPasswordSetToSetThatTakesIsAccepted): at the base commit the deploy
// either reports success on a router that rejects the new password, or accepts
// the transition without ever proving the credential.

import (
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// ─── the production call site ────────────────────────────────────

// TestRunDeploymentWiresTheFreshLoginProof pins the single production call site
// of the proof channel. The proof is only worth anything if the deploy hands
// ensureRootCredential a channel that dials the OPERATOR'S router with the
// candidate: a nil channel fails closed, so every supplied-password deploy on a
// router that already had a credential would fail with "could not be proven"
// while the code still looked right.
func TestRunDeploymentWiresTheFreshLoginProof(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "deploy.go", deployGoSrc, 0)
	if err != nil {
		t.Fatalf("parse deploy.go: %v", err)
	}
	body := funcBodyText(t, fset, f, "runDeployment")
	if !strings.Contains(body, "ensureRootCredential(job,") {
		t.Fatalf("runDeployment no longer calls ensureRootCredential\n%s", body)
	}
	if !strings.Contains(body, "proveRootPassword(req.IP") {
		t.Errorf("runDeployment does not wire the fresh-login proof to the router address it is deploying to (expected proveRootPassword(req.IP, …))\n%s", body)
	}
}

// ─── the proof obligation on a set→set transition ────────────────

// TestSuppliedPasswordSetToSetIsProvenOnAFreshLogin is the #46 follow-up: the
// router ALREADY had a real hash and the operator supplied a new password, but
// passwd silently left the old one in place. A hash-existence check cannot tell
// that world apart from a successful change, so the deploy must fail — the
// router authenticates the OLD credential, and the wizard's own later steps
// (and the operator) would be using the new one.
func TestSuppliedPasswordSetToSetIsProvenOnAFreshLogin(t *testing.T) {
	job := newJob("192.168.8.1")
	fr := &fakeCredentialRouter{
		hash:         rootHashSet, // the router already had a credential...
		live:         "the-old-password",
		refusePasswd: true, // ...and passwd "succeeds" without replacing it
		passwdOut:    "passwd: password changed\n",
	}

	if _, ok := ensureRootCredential(job, fr.run, fr.proveLogin, "the-new-password"); ok {
		t.Fatalf("ensureRootCredential reported success, but the router still authenticates %q, not the supplied password — "+
			"a non-empty shadow hash is not proof that the password the wizard set is the one in it", fr.live)
	}
	if job.Status != "failed" {
		t.Fatalf("job status = %q, want failed (the router holds a credential the wizard did not set)", job.Status)
	}
	if job.Steps[4].Status != "failed" {
		t.Fatalf("step 4 status = %q, want failed", job.Steps[4].Status)
	}

	// The proof must have been ATTEMPTED, with the supplied password.
	offered := fr.offeredOnFreshLogin()
	if len(offered) == 0 {
		t.Fatal("no fresh-login proof was attempted on a set→set transition: a shadow re-read cannot prove which password the hash holds")
	}
	if last := offered[len(offered)-1]; last != "the-new-password" {
		t.Fatalf("fresh-login proof offered %q, want the supplied password", last)
	}

	// And the failure must say what happened, so an operator can act on it.
	if !strings.Contains(strings.ToUpper(job.Error), "FRESH SSH LOGIN") {
		t.Fatalf("the failure does not name the fresh-login proof, so the operator cannot tell what went wrong; error: %s", job.Error)
	}
	if !strings.Contains(job.Error, "REFUSED") {
		t.Fatalf("the failure does not say the router refused the new password; error: %s", job.Error)
	}
	if logText := jobLogText(job); strings.Contains(logText, "the-new-password") {
		t.Fatalf("the supplied password is written to the deploy log:\n%s", logText)
	}
}

// TestSuppliedPasswordSetToSetThatTakesIsAccepted: the happy set→set path must
// keep working — the new credential is set, and the deploy PROVES it on a fresh
// login rather than trusting the hash re-read. The proof must not be skipped on a
// router that already had a credential.
func TestSuppliedPasswordSetToSetThatTakesIsAccepted(t *testing.T) {
	job := newJob("192.168.8.1")
	fr := &fakeCredentialRouter{
		hash:      rootHashSet,
		live:      "the-old-password",
		passwdOut: "passwd: password changed\n",
	}

	pw, ok := ensureRootCredential(job, fr.run, fr.proveLogin, "the-new-password")
	if !ok {
		t.Fatalf("ensureRootCredential failed on a set→set transition that took: %s", job.Error)
	}
	if pw != "the-new-password" {
		t.Fatalf("returned password = %q, want the supplied one", pw)
	}
	if fr.live != "the-new-password" {
		t.Fatalf("router live credential = %q, want the supplied password (passwd did run)", fr.live)
	}
	offered := fr.offeredOnFreshLogin()
	if len(offered) == 0 {
		t.Fatal("the set→set path accepted the password WITHOUT proving it on a fresh login — the hash re-read alone is what the #46 follow-up finding calls insufficient")
	}
	if last := offered[len(offered)-1]; last != "the-new-password" {
		t.Fatalf("fresh-login proof offered %q, want the supplied password", last)
	}
	if job.generatedPassword != "" {
		t.Fatalf("a supplied password must not be reported as generated (got %q)", job.generatedPassword)
	}
	if job.Status == "failed" {
		t.Fatalf("deploy failed: %s", job.Error)
	}
	if got := job.Steps[4].Status; got != "done" {
		t.Fatalf("step 4 status = %q, want done", got)
	}
}

// TestSuppliedPasswordThatIsAlreadyTheLiveCredentialIsAccepted pins what the
// proof IS: "the router accepts this credential", not "the hash changed". A
// passwd that no-ops because the router already authenticates the supplied
// password is a benign no-op — the credential the wizard promises is the one
// that works — so it must not fail the deploy. (An implementation that compared
// the shadow hash before/after would fail here.)
func TestSuppliedPasswordThatIsAlreadyTheLiveCredentialIsAccepted(t *testing.T) {
	job := newJob("192.168.8.1")
	fr := &fakeCredentialRouter{
		hash:         rootHashSet,
		live:         "the-same-password",
		refusePasswd: true, // passwd changed nothing...
		passwdOut:    "passwd: password changed\n",
	}

	if _, ok := ensureRootCredential(job, fr.run, fr.proveLogin, "the-same-password"); !ok {
		t.Fatalf("re-supplying the credential the router already accepts failed the deploy: %s", job.Error)
	}
	if len(fr.offeredOnFreshLogin()) == 0 {
		t.Fatal("the transition was accepted without a fresh-login proof")
	}
}

// TestSetToSetWithoutAProofChannelFailsClosed: "there was no way to prove it"
// must never be the same thing as "it is proven". The proof channel is always
// supplied by runDeployment; a missing one is a fail-closed condition, not a
// licence to fall back to the hash re-read.
func TestSetToSetWithoutAProofChannelFailsClosed(t *testing.T) {
	job := newJob("192.168.8.1")
	fr := &fakeCredentialRouter{hash: rootHashSet, passwdOut: "passwd: password changed\n"}

	if _, ok := ensureRootCredential(job, fr.run, nil, "the-new-password"); ok {
		t.Fatal("without a fresh-login proof channel the deploy reported success on a router that already had a credential")
	}
	if job.Status != "failed" || job.Steps[4].Status != "failed" {
		t.Fatalf("want a failed deploy, got status=%q step4=%q", job.Status, job.Steps[4].Status)
	}
	if !strings.Contains(job.Error, "no way to prove") {
		t.Fatalf("the failure does not explain the missing proof; error: %s", job.Error)
	}
}

// ─── the empty-hash premise (why the proof is conditional) ───────

// TestEmptyHashRouterDoesNotAcceptAFreshLoginAsProof pins the author's caveat:
// on a router whose root hash is EMPTY, rpcd's rpc_login_test_password() and
// dropbear accept ANY password, so a successful login there is not evidence that
// the generated credential landed. The deploy must still fail on the shadow
// re-read — even though the fresh-login channel would happily report success.
func TestEmptyHashRouterDoesNotAcceptAFreshLoginAsProof(t *testing.T) {
	job := newJob("192.168.8.1")
	fr := &fakeCredentialRouter{
		hash:         rootHashEmpty,
		refusePasswd: true, // passwd reports success, the hash stays empty
		passwdOut:    "passwd: password changed\n",
	}
	// The fake models the real premise: an empty hash accepts any candidate.
	if !fr.proveLogin("anything-at-all") {
		t.Fatal("fixture is wrong: an empty-hash router must accept any password")
	}

	if _, ok := ensureRootCredential(job, fr.run, fr.proveLogin, ""); ok {
		t.Fatal("the deploy walked past a router whose root password hash is still EMPTY (a fresh OpenWrt accepts any password, so an auth attempt proves nothing)")
	}
	if job.Status != "failed" || job.Steps[4].Status != "failed" {
		t.Fatalf("want a failed deploy, got status=%q step4=%q", job.Status, job.Steps[4].Status)
	}
	if len(fr.offeredOnFreshLogin()) != 1 {
		t.Fatalf("fresh-login attempts = %d, want exactly 1 (the fixture's own sanity check): the deploy must rely on the shadow hash in the empty state", len(fr.offeredOnFreshLogin()))
	}
}

// TestGeneratedCredentialOnAnEmptyRouterNeedsNoLoginProof: the empty→set path
// (the credential the wizard GENERATES and shows once) is the state where an
// auth attempt proves nothing, so no login proof is required or performed —
// part 1 (the hash re-read) is the proof there. Pinning the count also stops a
// "just always require a login" fix from reintroducing the empty-hash premise.
func TestGeneratedCredentialOnAnEmptyRouterNeedsNoLoginProof(t *testing.T) {
	job := newJob("192.168.8.1")
	fr := &fakeCredentialRouter{hash: rootHashEmpty, passwdOut: "passwd: password changed\n"}

	pw, ok := ensureRootCredential(job, fr.run, fr.proveLogin, "")
	if !ok {
		t.Fatalf("ensureRootCredential failed on a fresh router: %s", job.Error)
	}
	if pw == "" || len(pw) != rootPasswordLength {
		t.Fatalf("generated credential = %q, want %d characters", pw, rootPasswordLength)
	}
	if fr.hash != rootHashSet {
		t.Fatalf("router hash = %q, want %q", fr.hash, rootHashSet)
	}
	if got := fr.offeredOnFreshLogin(); len(got) != 0 {
		t.Fatalf("the empty→set path ran a login proof (%q): on an empty-hash router EVERY password authenticates, so a login is not a proof of anything and must not be relied on", got)
	}
	if job.generatedPassword != pw {
		t.Fatalf("generated_password = %q, want the password that was set (%q)", job.generatedPassword, pw)
	}
}

// TestLockedRouterSuppliedPasswordIsProvenOnAFreshLogin: a LOCKED account
// ('!'/'*') cannot be logged into with a password at all, so the operator who
// supplies one is asking for a password LOGIN — the same fresh-login proof
// applies, and a passwd that leaves the account locked must fail the deploy
// rather than be reported as a set password.
func TestLockedRouterSuppliedPasswordIsProvenOnAFreshLogin(t *testing.T) {
	job := newJob("192.168.8.1")
	fr := &fakeCredentialRouter{
		hash:         rootHashLocked, // passwd "succeeds" but the account stays locked
		refusePasswd: true,
		passwdOut:    "passwd: password changed\n",
	}

	if _, ok := ensureRootCredential(job, fr.run, fr.proveLogin, "unlock-me-please"); ok {
		t.Fatal("a still-locked account was reported as unlocked with the supplied password")
	}
	if job.Status != "failed" || job.Steps[4].Status != "failed" {
		t.Fatalf("want a failed deploy, got status=%q step4=%q", job.Status, job.Steps[4].Status)
	}
}
