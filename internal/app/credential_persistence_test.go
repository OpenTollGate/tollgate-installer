package app

// Persistence half of the credential-lockout fix for
// OpenTollGate/tollgate-installer#46.
//
// The one-shot serve (handleStatus / main.go) fixes the case where the operator
// is sitting in front of the wizard. It does NOT fix the durability case, and
// that is the one that bit a real board: the credential exists only in memory
// and only until the first terminal /api/status read. Miss that read — the tab
// was closed, the poll consumed it while the operator was away, a crash or a
// Ctrl-C landed between the deploy and the success view — and the router is
// left holding a root password NOBODY has, on a device whose stock state was
// "log in with an empty password". The operator is then locked out of their own
// router by the tool that was supposed to configure it (observed on
// 192.168.23.1: root password unknown to everyone, no recovery path).
//
// The fix pinned here: the credential is written to an owner-only file BEFORE
// it is applied to the router, so no ordering of failure, crash, or a missed
// poll can produce a re-keyed router with a lost credential.
//
// Every assertion in this file is RED against the head it was written for.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// credentialFileEnvName is intentionally the literal env name, not the
// constant: this file has to COMPILE against the unfixed tree so the failure
// it reports is the behavioural one (no file on disk), not a build error.
const credentialFileEnvName = "TOLLGATE_CREDENTIAL_FILE"

// TestGeneratedRootCredentialIsPersistedBeforePasswdRuns is the core contract.
// The ordering half is the load-bearing one: persisting after the write to the
// router would still leave a window where the router is re-keyed and the
// credential is nowhere.
func TestGeneratedRootCredentialIsPersistedBeforePasswdRuns(t *testing.T) {
	cred := filepath.Join(t.TempDir(), "tollgate-root-credentials")
	t.Setenv(credentialFileEnvName, cred)

	fr := &fakeCredentialRouter{hash: rootHashEmpty, passwdOut: "passwd: password changed\n"}

	// Snapshot the file at the exact moment the router is re-keyed: the proof
	// that persistence happened FIRST is what was already on disk when
	// `passwd root` ran.
	var atPasswd string
	run := func(cmd string) string {
		if strings.Contains(cmd, "passwd root") {
			if b, err := os.ReadFile(cred); err != nil {
				atPasswd = "MISSING: " + err.Error()
			} else {
				atPasswd = string(b)
			}
		}
		return fr.run(cmd)
	}

	job := newJob("192.168.23.1")
	if _, ok := ensureRootCredential(job, run, fr.proveLogin, ""); !ok {
		t.Fatalf("ensureRootCredential failed: %s", job.Error)
	}
	job.mu.Lock()
	pw := job.pendingRootPassword
	job.mu.Unlock()
	if pw == "" {
		t.Fatal("no credential was generated for a router with an empty root hash")
	}
	// The credential is APPLIED here, not at step 4: the set is deferred until
	// the install is verified (see credential_after_verify_test.go). The
	// invariant is unchanged — it must be on disk BEFORE it is applied — and
	// `run` snapshots the file at the moment `passwd root` executes.
	if !finalizeRootCredential(job, run, fr.proveLogin) {
		t.Fatalf("finalizeRootCredential failed: %s", job.Error)
	}

	// 1. The credential reached the disk at all.
	onDisk, err := os.ReadFile(cred)
	if err != nil {
		t.Fatalf("the generated root credential was NOT persisted (%v). A missed one-shot /api/status read then leaves the router with a password nobody has — exactly the lockout this file exists to prevent", err)
	}
	if !strings.Contains(string(onDisk), pw) {
		t.Fatalf("the credential file does not contain the credential the deploy set; file:\n%s", onDisk)
	}

	// 2. It was there BEFORE the router was re-keyed (ordering).
	if strings.HasPrefix(atPasswd, "MISSING") {
		t.Fatalf("when `passwd root` ran, the credential file did not exist yet (%s): a failure between the write and the persist still locks the operator out", atPasswd)
	}
	if !strings.Contains(atPasswd, pw) {
		t.Fatalf("when `passwd root` ran the file did not yet hold the credential — persistence must precede the re-key; snapshot:\n%s", atPasswd)
	}

	// 3. Owner-only, and only that.
	fi, err := os.Stat(cred)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("credential file mode = %04o, want 0600 (this file holds a router's root password)", perm)
	}

	logText := jobLogText(job)

	// 4. The operator is TOLD where it went — the log is served by every status
	//    poll, so it survives a missed one-shot read.
	if !strings.Contains(logText, cred) {
		t.Fatalf("the deploy log does not name the credential file, so the operator cannot find it after a missed one-shot read; log:\n%s", logText)
	}

	// 5. ...and the log still must not carry the secret itself. Every poll
	//    serves the full log, so a logged password defeats the one-shot cutoff.
	if strings.Contains(logText, pw) {
		t.Fatalf("the credential itself is in the deploy log, which every /api/status poll returns in full:\n%s", logText)
	}

	// 6. The path is on the status payload from the start of the run (it is a
	//    path, not a secret), so the UI can name it before the job is terminal.
	const jobID = "persisted-credential-job"
	registerStatusJob(t, jobID, job)
	if got := readStatus(t, jobID)["credential_file"]; got != cred {
		t.Fatalf("status credential_file = %v, want %q — the UI cannot tell the operator where the password was saved", got, cred)
	}
}

// TestCredentialFileKeepsEarlierCredentialsAndIsTightenedToOwnerOnly pins the
// two properties that make the file usable as a recovery log rather than a
// one-shot scratch space: it APPENDS (an older router's credential must not be
// clobbered by the next deploy) and it is forced to 0600 even when it already
// existed with looser permissions (an operator's umask must not be able to
// publish a root password to other local users).
func TestCredentialFileKeepsEarlierCredentialsAndIsTightenedToOwnerOnly(t *testing.T) {
	const earlier = "2026-01-01T00:00:00Z\t192.168.9.9\tOLDPASSWORDDOESNOTMATTER"
	cred := filepath.Join(t.TempDir(), "tollgate-root-credentials")
	if err := os.WriteFile(cred, []byte(earlier+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(credentialFileEnvName, cred)

	fr := &fakeCredentialRouter{hash: rootHashEmpty, passwdOut: "passwd: password changed\n"}
	job := newJob("192.168.23.1")
	if _, ok := ensureRootCredential(job, fr.run, fr.proveLogin, ""); !ok {
		t.Fatalf("ensureRootCredential failed: %s", job.Error)
	}

	b, err := os.ReadFile(cred)
	if err != nil {
		t.Fatal(err)
	}
	job.mu.Lock()
	pending := job.pendingRootPassword
	job.mu.Unlock()
	if !strings.Contains(string(b), earlier) {
		t.Fatalf("the earlier credential was removed — this file is a recovery log and must only ever append; file:\n%s", b)
	}
	if pending == "" || !strings.Contains(string(b), pending) {
		t.Fatalf("the generated credential (pending=%q) was not appended; file:\n%s", pending, b)
	}
	if fi, err := os.Stat(cred); err != nil {
		t.Fatal(err)
	} else if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("pre-existing file left at %04o, want 0600 — the deploy must tighten it", perm)
	}
}

// TestCredentialPersistFailureWarnsLoudlyAndDoesNotAbort pins the deliberate
// failure mode. Refusing to configure a router because a local file could not
// be written (read-only home, full disk) would be the wrong trade: the one-shot
// terminal-state serve can still surrender the credential. What must NEVER
// happen is a silent save — an operator who believes the credential is on disk
// will not copy it from the screen.
func TestCredentialPersistFailureWarnsLoudlyAndDoesNotAbort(t *testing.T) {
	// A FILE where a directory is needed: MkdirAll cannot succeed.
	base := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(base, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	cred := filepath.Join(base, "tollgate-root-credentials")
	t.Setenv(credentialFileEnvName, cred)

	fr := &fakeCredentialRouter{hash: rootHashEmpty, passwdOut: "passwd: password changed\n"}
	job := newJob("192.168.23.1")
	pw, ok := ensureRootCredential(job, fr.run, fr.proveLogin, "")
	if !ok {
		t.Fatalf("an unwritable credential FILE aborted the deploy (%s); the router can still be configured and the one-shot serve still fires", job.Error)
	}
	if job.Status == "failed" {
		t.Fatalf("an unwritable credential file FAILED the deploy (%s); the one-shot serve still fires, and refusing to configure a router over a local file write error is the wrong trade", job.Error)
	}

	logText := jobLogText(job)
	if !strings.Contains(logText, "could not save") {
		t.Fatalf("a failed persistence was not reported to the operator; log:\n%s", logText)
	}
	if !strings.Contains(logText, cred) {
		t.Fatalf("the warning does not name the path that failed; log:\n%s", logText)
	}
	if pw != "" && strings.Contains(logText, pw) {
		t.Fatalf("the warning leaked the credential into the log:\n%s", logText)
	}

	const jobID = "persist-failure-job"
	registerStatusJob(t, jobID, job)
	if got := readStatus(t, jobID)["credential_file"]; got != nil && got != "" {
		t.Fatalf("status advertised credential_file = %v although nothing was written", got)
	}
}
