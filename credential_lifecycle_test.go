package main

// Credential-lifecycle tests for the three gate findings on
// OpenTollGate/tollgate-installer#46 (review 5805539772, review comment
// 2026-09-24T00:59:42Z):
//
//	finding 1 — the generated credential is never surfaced (UI lockout path)
//	finding 2 — the one-shot /api/status credential plus guessable job IDs
//	finding 3 — an unreadable /etc/shadow classified as `empty` (a
//	            set-a-password authorisation that OVERWRITES a real hash)
//
// Every assertion here is RED against the head it was written for.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ─── finding 3: `unknown` is not `empty` ─────────────────────────

// TestRootHashProbeCmdClassifiesUnreadableShadowAsUnknown is the finding-3
// regression guard: a probe that cannot READ /etc/shadow (missing file,
// permission failure, no awk) must classify as `unknown`, because `empty`
// authorises "set a password" — and setting one OVERWRITES whatever real hash
// the file held. The last sub-test is the control: the same command text must
// still report a genuine hash, so the guards cannot pass by classifying
// everything as unknown.
func TestRootHashProbeCmdClassifiesUnreadableShadowAsUnknown(t *testing.T) {
	t.Run("missing shadow file", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "shadow-does-not-exist")
		out := shRun(t, strings.ReplaceAll(rootHashProbeCmd, "/etc/shadow", missing))
		if got := parseRootHashState(out); got != rootHashUnknown {
			t.Fatalf("missing /etc/shadow classified as %q (raw output %q), want %q — `empty` would overwrite a real password",
				got, out, rootHashUnknown)
		}
	})

	t.Run("unreadable shadow file (permission failure)", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("running as root: a mode-0000 file is still readable (test -r succeeds)")
		}
		shadow := filepath.Join(t.TempDir(), "shadow")
		if err := os.WriteFile(shadow, []byte("root:$1$abc$defghijklmnop:0:0:99999:7:::\n"), 0o000); err != nil {
			t.Fatal(err)
		}
		out := shRun(t, strings.ReplaceAll(rootHashProbeCmd, "/etc/shadow", shadow))
		if got := parseRootHashState(out); got != rootHashUnknown {
			t.Fatalf("an unreadable /etc/shadow classified as %q (raw output %q), want %q — the router HAS a password the probe could not read",
				got, out, rootHashUnknown)
		}
	})

	t.Run("awk unavailable", func(t *testing.T) {
		// A readable shadow file, but no awk on PATH: the exit status of the
		// command substitution — not its silenced stderr — is what has to
		// decide. Dropping `2>/dev/null` alone does not fix this: the shell's
		// error line lands first and the `case` still prints `empty`, which
		// parseRootHashState reads as the LAST field.
		shadow := filepath.Join(t.TempDir(), "shadow")
		if err := writeFileForTest(shadow, "root:$1$abc$defghijklmnop:0:0:99999:7:::\n"); err != nil {
			t.Fatal(err)
		}
		env := []string{"PATH=" + t.TempDir(), "HOME=" + t.TempDir()}
		out := shRunEnv(t, env, strings.ReplaceAll(rootHashProbeCmd, "/etc/shadow", shadow))
		if got := parseRootHashState(out); got != rootHashUnknown {
			t.Fatalf("PATH-stripped awk classified as %q (raw output %q), want %q", got, out, rootHashUnknown)
		}
	})

	t.Run("control: a readable hash is still reported as set", func(t *testing.T) {
		shadow := filepath.Join(t.TempDir(), "shadow")
		if err := writeFileForTest(shadow, "root:$1$abc$defghijklmnop:0:0:99999:7:::\n"); err != nil {
			t.Fatal(err)
		}
		out := shRun(t, strings.ReplaceAll(rootHashProbeCmd, "/etc/shadow", shadow))
		if got := parseRootHashState(out); got != rootHashSet {
			t.Fatalf("readable hash classified as %q (raw output %q), want %q", got, out, rootHashSet)
		}
	})

	// The shell the router actually runs: BusyBox ash (stock OpenWrt).
	//
	// Note on `awk` there: BusyBox ash resolves awk from its own applet set
	// even when a PATH binary is present or PATH is empty (verified on this
	// host: PATH=… with a shim `awk` that exits 127 still yields a real hash
	// read, and `env -i PATH= busybox ash -c` does too). So on stock OpenWrt
	// awk cannot go missing, and the awk guard below is belt-and-braces for a
	// non-BusyBox /bin/sh (the PATH-stripped case in the sub-test above) or a
	// busybox built without the awk applet. What this shell DOES have to get
	// right is the three-state split.
	t.Run("stock OpenWrt shell (busybox ash)", func(t *testing.T) {
		bb, err := exec.LookPath("busybox")
		if err != nil {
			t.Skip("busybox not available on this host")
		}
		emptyHash := filepath.Join(t.TempDir(), "shadow-empty")
		if err := writeFileForTest(emptyHash, "root::0:0:99999:7:::\n"); err != nil {
			t.Fatal(err)
		}
		realHash := filepath.Join(t.TempDir(), "shadow-set")
		if err := writeFileForTest(realHash, "root:$1$abc$defghijklmnop:0:0:99999:7:::\n"); err != nil {
			t.Fatal(err)
		}
		unreadable := filepath.Join(t.TempDir(), "shadow-unreadable")
		if err := writeFileForTest(unreadable, "root:$1$abc$defghijklmnop:0:0:99999:7:::\n"); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(unreadable, 0o000); err != nil {
			t.Fatal(err)
		}

		cases := []struct {
			name   string
			path   string
			state  rootHashState
			reason string
		}{
			{"real hash", realHash, rootHashSet, "control"},
			{"empty hash field", emptyHash, rootHashEmpty, "the genuinely credential-less state"},
			{"missing shadow", filepath.Join(t.TempDir(), "absent"), rootHashUnknown, "unreadable must never read as empty"},
		}
		if os.Geteuid() != 0 { // as root a mode-0000 file is still readable
			cases = append(cases, struct {
				name   string
				path   string
				state  rootHashState
				reason string
			}{"unreadable shadow", unreadable, rootHashUnknown, "unreadable must never read as empty"})
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				cmd := exec.Command(bb, "ash", "-c", strings.ReplaceAll(rootHashProbeCmd, "/etc/shadow", tc.path))
				cmd.Env = os.Environ()
				out, _ := cmd.CombinedOutput()
				if got := parseRootHashState(string(out)); got != tc.state {
					t.Fatalf("busybox ash: %s → %q (raw %q), want %q (%s)", tc.name, got, out, tc.state, tc.reason)
				}
			})
		}
	})
}

// TestEnsureRootCredentialSetEmptyUnknownAreDistinct runs the three probe
// states through the deploy path and pins what each one is allowed to do. The
// security-critical pair is empty/unknown: they are indistinguishable to a
// `case ""` fall-through, but only `empty` may authorise a write.
func TestEnsureRootCredentialSetEmptyUnknownAreDistinct(t *testing.T) {
	cases := []struct {
		state    rootHashState
		name     string
		wantOK   bool
		wantSet  bool // passwd root must run
		wantHash rootHashState
	}{
		{name: "set — left untouched", state: rootHashSet, wantOK: true, wantSet: false, wantHash: rootHashSet},
		{name: "empty — a credential is generated and set", state: rootHashEmpty, wantOK: true, wantSet: true, wantHash: rootHashSet},
		{name: "unknown — fail closed, nothing written", state: rootHashUnknown, wantOK: false, wantSet: false, wantHash: rootHashUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			job := newJob("192.168.8.1")
			fr := &fakeCredentialRouter{hash: tc.state, passwdOut: "passwd: password changed\n"}

			_, ok := ensureRootCredential(job, fr.run, "")
			if ok != tc.wantOK {
				t.Fatalf("ensureRootCredential ok = %v, want %v (job error: %s)", ok, tc.wantOK, job.Error)
			}
			if got := fr.ranPasswd(); got != tc.wantSet {
				t.Fatalf("passwd root ran = %v, want %v", got, tc.wantSet)
			}
			if fr.hash != tc.wantHash {
				t.Fatalf("router hash after the deploy = %q, want %q", fr.hash, tc.wantHash)
			}
			if tc.state == rootHashUnknown {
				if job.Status != "failed" || job.Steps[4].Status != "failed" {
					t.Fatalf("unknown state: status=%q step4=%q, want both failed", job.Status, job.Steps[4].Status)
				}
				logText := jobLogText(job)
				if !strings.Contains(logText, "unreadable") && !strings.Contains(logText, "cannot read") {
					t.Fatalf("the unknown state is not reported to the operator; log:\n%s", logText)
				}
			}
		})
	}
}

// ─── finding 2: unguessable job IDs ──────────────────────────────

// ─── finding 2: exactly-once credential on /api/status ───────────

// TestStatusServesGeneratedPasswordExactlyOnce pins the endpoint contract:
// the credential is served on the FIRST read of a COMPLETED job, every later
// read omits it, and the server drops it after that read.
//
// The "only when done" half is load-bearing: the UI polls every second while
// the deploy runs, so serving (and consuming) the value mid-run would burn it
// before the operator ever reaches the success view that shows it.
func TestStatusServesGeneratedPasswordExactlyOnce(t *testing.T) {
	const (
		jobID = "one-shot-credential-job"
		pw    = "Sup3rSecretRootPw1234"
	)
	job := newJob("192.168.8.1")
	job.setGeneratedPassword(pw)

	jobsMutex.Lock()
	jobs[jobID] = job
	jobsMutex.Unlock()
	t.Cleanup(func() {
		jobsMutex.Lock()
		delete(jobs, jobID)
		jobsMutex.Unlock()
	})

	read := func() map[string]any {
		t.Helper()
		rr := httptest.NewRecorder()
		handleStatus(rr, httptest.NewRequest(http.MethodGet, "/api/status/"+jobID, nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("handleStatus status = %d, want 200", rr.Code)
		}
		var body map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode status body: %v", err)
		}
		return body
	}

	// Running: the value must not be served, and must not be consumed either.
	if got := read()["generated_password"]; got != nil && got != "" {
		t.Fatalf("generated_password served while the job was still running (%v) — the UI's 1 s polling would consume it before the success view", got)
	}

	job.mu.Lock()
	job.Status = "done"
	job.mu.Unlock()

	first := read()
	if first["generated_password"] != pw {
		t.Fatalf("first read of a completed job = %v, want the generated credential %q", first["generated_password"], pw)
	}
	for poll := 2; poll <= 5; poll++ {
		if got := read()["generated_password"]; got != nil && got != "" {
			t.Fatalf("read %d returned the credential again (%v) — the endpoint must serve it at most once", poll, got)
		}
	}
	job.mu.Lock()
	held := job.generatedPassword
	job.mu.Unlock()
	if held != "" {
		t.Fatalf("the server still holds the credential (%q) after the one-shot read — it must be dropped", held)
	}
}

// TestGeneratedCredentialNeverEntersTheDeployLog: job.Log is served IN FULL by
// every /api/status response, so a "ROOT PASSWORD: …" log line would re-serve
// the credential on every poll and silently defeat the one-shot cutoff above.
// The log may announce that a credential was created; it must not carry it.
func TestGeneratedCredentialNeverEntersTheDeployLog(t *testing.T) {
	const pw = "Sup3rSecretRootPw1234"
	job := newJob("192.168.8.1")
	job.setGeneratedPassword(pw)

	logText := jobLogText(job)
	if strings.Contains(logText, pw) {
		t.Fatalf("the credential is written to the deploy log, which is part of every /api/status payload:\n%s", logText)
	}
	if !strings.Contains(logText, "ROOT PASSWORD") {
		t.Fatalf("the operator is not told a root credential was created; log:\n%s", logText)
	}
	if !strings.Contains(logText, "ONCE") {
		t.Fatalf("the log does not say the credential is shown once; log:\n%s", logText)
	}

	// And it must not reach the response body through any other field either.
	rr := httptest.NewRecorder()
	jobsMutex.Lock()
	jobs["log-leak-check"] = job
	jobsMutex.Unlock()
	defer func() {
		jobsMutex.Lock()
		delete(jobs, "log-leak-check")
		jobsMutex.Unlock()
	}()
	handleStatus(rr, httptest.NewRequest(http.MethodGet, "/api/status/log-leak-check", nil))
	if body := rr.Body.String(); strings.Contains(body, pw) {
		t.Fatalf("the credential appears in a running job's /api/status body: %s", body)
	}
}
