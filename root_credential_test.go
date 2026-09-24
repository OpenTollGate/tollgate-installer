package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// ─── test helpers shared with branding_test.go ───────────────────

func writeFileForTest(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}

// shRunEnv runs a snippet of the SHIPPED router-side shell under /bin/sh with a
// caller-supplied environment (PATH shims, stub state files) and returns the
// combined output. The router runs BusyBox ash; /bin/sh is the closest local
// stand-in.
func shRunEnv(t *testing.T, env []string, script string) string {
	t.Helper()
	cmd := exec.Command("sh", "-c", script)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("sh exited non-zero: %v\noutput:\n%s", err, out)
	}
	return string(out)
}

func shRun(t *testing.T, script string) string {
	t.Helper()
	return shRunEnv(t, os.Environ(), script)
}

// ─── /etc/shadow state parsing ───────────────────────────────────

// TestParseRootHashState covers the probe contract. The EMPTY case is the
// security-critical one: rpcd's rpc_login_test_password() returns true when the
// hash is empty, so that state authenticates ANY password — including "".
func TestParseRootHashState(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want rootHashState
	}{
		{"empty hash", "empty\n", rootHashEmpty},
		{"locked with bang", "locked\n", rootHashLocked},
		{"real hash", "set\n", rootHashSet},
		{"no output (unreadable shadow / dead session)", "", rootHashUnknown},
		{"garbage", "sh: awk: not found\n", rootHashUnknown},
		{"an unparsed probe banner is not a state", "rootHashEmpty\n", rootHashUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseRootHashState(tc.out); got != tc.want {
				t.Fatalf("parseRootHashState(%q) = %q, want %q", tc.out, got, tc.want)
			}
		})
	}
}

// TestRootHashProbeCmdClassifiesShadowFields executes the SHIPPED probe against
// a real /bin/sh with fixtures, so the classification is bound to the command
// text that goes to the router (not to a re-implementation in the test).
func TestRootHashProbeCmdClassifiesShadowFields(t *testing.T) {
	// The probe reads /etc/shadow, which we cannot write; run the same command
	// text with a fixture file substituted for the path.
	cases := []struct {
		name   string
		shadow string
		want   rootHashState
	}{
		{"root with empty hash field", "root::0:0:99999:7:::\n", rootHashEmpty},
		{"root missing entirely", "daemon:*:0:0:99999:7:::\n", rootHashEmpty},
		{"root locked with !", "root:!:0:0:99999:7:::\n", rootHashLocked},
		{"root locked with *", "root:*:0:0:99999:7:::\n", rootHashLocked},
		{"root with a real hash", "root:$1$abc$defghijklmnop:0:0:99999:7:::\n", rootHashSet},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			shadow := dir + "/shadow"
			if err := writeFileForTest(shadow, tc.shadow); err != nil {
				t.Fatal(err)
			}
			cmd := strings.ReplaceAll(rootHashProbeCmd, "/etc/shadow", shadow)
			out := shRun(t, cmd)
			if got := parseRootHashState(out); got != tc.want {
				t.Fatalf("probe on %q = %q (raw %q), want %q", tc.shadow, got, out, tc.want)
			}
		})
	}
}

// ─── Generated credential ────────────────────────────────────────

// TestGenerateRootPassword checks the generated credential is long, is drawn
// only from the shell-/human-safe alphabet, and differs across draws.
func TestGenerateRootPassword(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		pw, err := generateRootPassword()
		if err != nil {
			t.Fatalf("generateRootPassword: %v", err)
		}
		if len(pw) != rootPasswordLength {
			t.Fatalf("length = %d, want %d (%q)", len(pw), rootPasswordLength, pw)
		}
		for _, r := range pw {
			if !strings.ContainsRune(rootPasswordAlphabet, r) {
				t.Fatalf("password %q contains %q, which is outside the safe alphabet", pw, r)
			}
			// Nothing that could break out of the router-side shell command
			// or the passwd heredoc.
			if strings.ContainsAny(string(r), `'"\\ `+"\t\n`;$&|<>()") {
				t.Fatalf("password %q contains a shell-hostile character %q", pw, r)
			}
		}
		if seen[pw] {
			t.Fatalf("generateRootPassword repeated %q within 200 draws", pw)
		}
		seen[pw] = true
	}
}

// ─── ensureRootCredential ────────────────────────────────────────

// fakeCredentialRouter answers the probe and accepts passwd, like a real OpenWrt router.
// refusePasswd models a passwd that reports success but leaves the shadow hash
// empty — the exact silent failure the deploy must not walk past.
type fakeCredentialRouter struct {
	hash         rootHashState
	refusePasswd bool
	cmds         []string
	// passwdOut is what the router-side passwd prints.
	passwdOut string
}

func (f *fakeCredentialRouter) run(cmd string) string {
	f.cmds = append(f.cmds, cmd)
	switch {
	case strings.Contains(cmd, "/etc/shadow"):
		return string(f.hash) + "\n"
	case strings.Contains(cmd, "passwd root"):
		if !f.refusePasswd {
			f.hash = rootHashSet
		}
		return f.passwdOut
	}
	return ""
}

func (f *fakeCredentialRouter) ranPasswd() bool {
	for _, c := range f.cmds {
		if strings.Contains(c, "passwd root") {
			return true
		}
	}
	return false
}

func (f *fakeCredentialRouter) passwdCmd() string {
	for _, c := range f.cmds {
		if strings.Contains(c, "passwd root") {
			return c
		}
	}
	return ""
}

// jobLogText lives in wireless_rollback_test.go (merged from main, #47/#48/#49)
// and is shared by both suites — the merge dropped this branch's identical copy
// rather than declare the helper twice.

// TestEnsureRootCredentialFreshDeployForcesACredential is the pre-release
// blocker: the fresh-deploy state (root with NO password) must never be walked
// past. With no password supplied the deploy GENERATES one, sets it, verifies
// it landed, and hands it to the one-shot channel the UI shows it from.
//
// The credential must NOT be in the deploy log: job.Log is part of every
// /api/status response, so a log line would re-serve it on every poll and
// defeat the one-shot cutoff (see TestGeneratedCredentialNeverEntersTheDeployLog).
func TestEnsureRootCredentialFreshDeployForcesACredential(t *testing.T) {
	job := newJob("192.168.8.1")
	fr := &fakeCredentialRouter{hash: rootHashEmpty, passwdOut: "passwd: password changed\n"}

	pw, ok := ensureRootCredential(job, fr.run, "")
	if !ok {
		t.Fatalf("ensureRootCredential failed on a fresh deploy: %s", job.Error)
	}
	if pw == "" {
		t.Fatal("no credential was established on a password-less router")
	}
	if len(pw) != rootPasswordLength {
		t.Fatalf("generated credential length = %d, want %d", len(pw), rootPasswordLength)
	}
	if !fr.ranPasswd() {
		t.Fatal("passwd root was never run — the router was left with NO root password")
	}
	if fr.hash != rootHashSet {
		t.Fatalf("router hash = %q, want %q", fr.hash, rootHashSet)
	}
	if job.Status == "failed" {
		t.Fatalf("deploy failed: %s", job.Error)
	}
	if got := job.Steps[4].Status; got != "done" {
		t.Fatalf("step 4 status = %q, want done", got)
	}

	// The credential is held for the one-shot serve...
	job.mu.Lock()
	shown := job.generatedPassword
	job.mu.Unlock()
	if shown != pw {
		t.Fatalf("generated_password = %q, want the password that was set (%q)", shown, pw)
	}
	// ...and the log only ANNOUNCES it (shown once), never carries it.
	logText := jobLogText(job)
	if strings.Contains(logText, pw) {
		t.Fatalf("the generated password is written to the deploy log:\n%s", logText)
	}
	if !strings.Contains(logText, "ROOT PASSWORD") || !strings.Contains(logText, "ONCE") {
		t.Fatalf("the deploy log does not tell the operator a one-time credential was created:\n%s", logText)
	}
}

// TestEnsureRootCredentialFailsClosedWhenPasswdDoesNotTake: a passwd that
// reports success but leaves the hash empty must FAIL the deploy — otherwise
// the wizard reports success on a router with unauthenticated root.
func TestEnsureRootCredentialFailsClosedWhenPasswdDoesNotTake(t *testing.T) {
	job := newJob("192.168.8.1")
	fr := &fakeCredentialRouter{hash: rootHashEmpty, refusePasswd: true, passwdOut: "passwd: password changed\n"}

	if _, ok := ensureRootCredential(job, fr.run, ""); ok {
		t.Fatal("ensureRootCredential returned ok=true although the router still has NO root password")
	}
	if job.Status != "failed" {
		t.Fatalf("job status = %q, want failed", job.Status)
	}
	if job.Steps[4].Status != "failed" {
		t.Fatalf("step 4 status = %q, want failed", job.Steps[4].Status)
	}
	if job.generatedPassword != "" {
		t.Fatalf("a credential was recorded as generated (%q) although it never took", job.generatedPassword)
	}
}

// TestEnsureRootCredentialUnreadableStateFailsClosed: if the router's shadow
// state cannot be read, never assume it is fine.
func TestEnsureRootCredentialUnreadableStateFailsClosed(t *testing.T) {
	job := newJob("192.168.8.1")
	fr := &fakeCredentialRouter{hash: rootHashUnknown}

	if _, ok := ensureRootCredential(job, fr.run, ""); ok {
		t.Fatal("ensureRootCredential accepted an unreadable root-hash state")
	}
	if job.Status != "failed" || job.Steps[4].Status != "failed" {
		t.Fatalf("want a failed deploy, got status=%q step4=%q", job.Status, job.Steps[4].Status)
	}
	if fr.ranPasswd() {
		t.Fatal("passwd was run against an unknown credential state")
	}
}

// TestEnsureRootCredentialHonoursSuppliedPassword: an operator-supplied
// password is what gets set, and the plaintext never enters the SSH command.
func TestEnsureRootCredentialHonoursSuppliedPassword(t *testing.T) {
	job := newJob("192.168.8.1")
	fr := &fakeCredentialRouter{hash: rootHashEmpty, passwdOut: "passwd: password changed\n"}

	pw, ok := ensureRootCredential(job, fr.run, "corr3ct-h0rse")
	if !ok {
		t.Fatalf("ensureRootCredential failed: %s", job.Error)
	}
	if pw != "corr3ct-h0rse" {
		t.Fatalf("returned password = %q, want the supplied one", pw)
	}
	if job.generatedPassword != "" {
		t.Fatalf("a supplied password must not be reported as generated (got %q)", job.generatedPassword)
	}
	cmd := fr.passwdCmd()
	if strings.Contains(cmd, "corr3ct-h0rse") {
		t.Fatalf("the plaintext password appears in the router command: %q", cmd)
	}
	// The carrier crosses as printf-expanded octal escapes: no router-side
	// binary (stock OpenWrt BusyBox has no base64 applet — #46 review
	// 5304937880), no plaintext in the command string.
	if strings.Contains(cmd, "base64") {
		t.Fatalf("the passwd command must not need a router-side base64 decode: %q", cmd)
	}
	carrier := octalCarrierVar("pw", "corr3ct-h0rse")
	if !strings.Contains(cmd, carrier) {
		t.Fatalf("no printf carrier in the passwd command: %q", cmd)
	}
	esc := strings.TrimSuffix(strings.TrimPrefix(carrier, "pw=$(printf '%b' '"), "')")
	if got, err := decodeOctalCarrier(esc); err != nil || got != "corr3ct-h0rse" {
		t.Fatalf("carrier %q decodes to %q (err %v), want the supplied password", esc, got, err)
	}
}

// TestEnsureRootCredentialLeavesAnExistingCredentialAlone: a router that
// already has a real hash is not touched when no password is supplied — and the
// deploy SAYS so (the card's "must not silently skip").
func TestEnsureRootCredentialLeavesAnExistingCredentialAlone(t *testing.T) {
	job := newJob("192.168.8.1")
	fr := &fakeCredentialRouter{hash: rootHashSet}

	pw, ok := ensureRootCredential(job, fr.run, "")
	if !ok {
		t.Fatalf("ensureRootCredential failed: %s", job.Error)
	}
	if pw != "" {
		t.Fatalf("returned password = %q, want empty (nothing changed)", pw)
	}
	if fr.ranPasswd() {
		t.Fatal("passwd was run on a router that already had a credential")
	}
	if job.generatedPassword != "" {
		t.Fatal("a credential was recorded as generated although the router already had one")
	}
	if logText := jobLogText(job); !strings.Contains(logText, "already set") {
		t.Fatalf("the skip is silent; log:\n%s", logText)
	}
	if job.Status == "failed" {
		t.Fatalf("deploy failed: %s", job.Error)
	}
}

// TestEnsureRootCredentialLeavesALockedPasswordAlone: '!'/'*' is a LOCKED
// account — password login is refused, so it is NOT credential-less, and
// re-enabling password auth on it would be a regression.
func TestEnsureRootCredentialLeavesALockedPasswordAlone(t *testing.T) {
	job := newJob("192.168.8.1")
	fr := &fakeCredentialRouter{hash: rootHashLocked}

	pw, ok := ensureRootCredential(job, fr.run, "")
	if !ok {
		t.Fatalf("ensureRootCredential failed: %s", job.Error)
	}
	if pw != "" || fr.ranPasswd() {
		t.Fatalf("a locked root account was modified (pw=%q, passwd ran=%v)", pw, fr.ranPasswd())
	}
	if logText := jobLogText(job); !strings.Contains(logText, "LOCKED") {
		t.Fatalf("the locked state is not reported; log:\n%s", logText)
	}
}

// TestEnsureRootCredentialSuppliedPasswordOverridesLocked: an explicit
// password always wins — that is how an operator unlocks the board.
func TestEnsureRootCredentialSuppliedPasswordOverridesLocked(t *testing.T) {
	job := newJob("192.168.8.1")
	fr := &fakeCredentialRouter{hash: rootHashLocked, passwdOut: "passwd: password changed\n"}

	pw, ok := ensureRootCredential(job, fr.run, "unlock-me-please")
	if !ok || pw != "unlock-me-please" {
		t.Fatalf("supplied password was not applied (ok=%v pw=%q err=%s)", ok, pw, job.Error)
	}
	if fr.hash != rootHashSet {
		t.Fatalf("hash = %q, want %q", fr.hash, rootHashSet)
	}
}
