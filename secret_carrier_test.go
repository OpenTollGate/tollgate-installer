package main

import (
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ─── Secret carriers: no router-side base64 (OpenTollGate/tollgate-installer#46)
//
// Review 5304937880 reproduced this E2E on a fresh STOCK OpenWrt 24.10.1 x86_64
// image: the router-side `echo <b64> | base64 -d` carrier used to deliver the
// root password (and the WiFi STA credentials) cannot work there, because stock
// OpenWrt's BusyBox does not ship the base64 applet (it is the separate
// `coreutils-base64` package):
//
//	passwd output: ash: base64: not found
//
// The command substitution returns 127, the `&&` chain short-circuits, passwd
// never runs, and the fail-closed guard aborts the deploy at step 4 on the
// wizard's own documented target. The carriers now expand with the SHELL BUILTIN
// printf instead (`pw=$(printf '%b' '\0NNN...')`), which needs no router-side
// binary at all.
//
// These tests hold both required properties:
//
//	(1) TestRouterCommandsNeverDependOnBase64 — no generated router-side command
//	    (and no code in deploy.go) may depend on base64 again.
//	(2) The round-trip tests execute the SHIPPED command text under a shell whose
//	    PATH is empty apart from a recording `passwd` stub, i.e. with NO binary
//	    reachable (no base64, no cat, nothing) — and the secret must still land
//	    byte-exact. TestSecretCarrierRoundTripsWithoutAnyRouterBinary also runs
//	    the OLD base64 command as a CONTROL in the same environment and shows it
//	    failing the way the reviewer's log does, so the test cannot pass by
//	    accident on a host that happens to have base64.

// decodeOctalCarrier is the test-side decoder for the `\0NNN` octal carrier:
// it turns the escape text back into bytes without going through a shell, so a
// test can assert what the carrier MEANS independently of what a shell does
// with it.
func decodeOctalCarrier(esc string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(esc); {
		if i+5 > len(esc) || esc[i] != '\\' || esc[i+1] != '0' {
			return "", fmt.Errorf("carrier %q: expected a \\0NNN escape at offset %d", esc, i)
		}
		var v int
		for _, d := range []byte(esc[i+2 : i+5]) {
			if d < '0' || d > '7' {
				return "", fmt.Errorf("carrier %q: escape at offset %d has a non-octal digit %q", esc, i, string(d))
			}
			v = v*8 + int(d-'0')
		}
		if v > 255 {
			return "", fmt.Errorf("carrier %q: escape at offset %d is out of byte range (%d)", esc, i, v)
		}
		b.WriteByte(byte(v))
		i += 5
	}
	return b.String(), nil
}

// TestShellOctalCarrierRoundTripsEveryByte pins the carrier encoding itself: the
// escapes must decode back to the input for every byte a shell variable can
// hold, and must never contain the plaintext.
func TestShellOctalCarrierRoundTripsEveryByte(t *testing.T) {
	var all []byte
	for i := 1; i < 256; i++ { // NUL cannot exist in a shell variable
		all = append(all, byte(i))
	}
	values := []string{
		"",
		string(all),
		"hunter2$Secret",
		"MyNet`; rm -rf / #",
		"Pass'word$; id #",
		"  leading and trailing spaces  ",
		"tab\there",
		"emoji-🔑-ü",
	}
	for _, v := range values {
		esc := shellOctalCarrier(v)
		if len(v) > 0 && strings.Contains(esc, v) {
			t.Errorf("carrier for %q contains the plaintext: %q", v, esc)
		}
		got, err := decodeOctalCarrier(esc)
		if err != nil {
			t.Fatalf("decodeOctalCarrier(%q): %v", esc, err)
		}
		if got != v {
			t.Errorf("carrier round-trip mismatch: got %q want %q (escapes %q)", got, v, esc)
		}
	}
}

// TestRouterCommandsNeverDependOnBase64 fails if any generated router-side
// command depends on a base64 decode again — the regression that blocked every
// deploy on stock OpenWrt (#46).
func TestRouterCommandsNeverDependOnBase64(t *testing.T) {
	commands := map[string]string{
		"passwdCommand":            passwdCommand("hunter2$Secret"),
		"staSetupScript":           staSetupScript("TollGate-Field", "correct horse", ""),
		"staSetupScriptFor(radio)": staSetupScriptFor("TollGate-Field", "correct horse", "5", "radio1"),
	}
	for name, cmd := range commands {
		if strings.Contains(cmd, "base64") {
			t.Errorf("%s depends on a router-side base64 decode; stock OpenWrt BusyBox ships no base64 applet (see #46 review 5304937880):\n%s", name, cmd)
		}
		// The carrier must be the printf-builtin expansion.
		if !strings.Contains(cmd, "printf '%b' '") {
			t.Errorf("%s does not use the shell-builtin printf carrier:\n%s", name, cmd)
		}
	}

	// The deploy path itself must not reach for base64 again. Comments are
	// stripped first: prose is allowed (and required) to explain WHY base64
	// cannot be used here, code is not.
	src, err := os.ReadFile("deploy.go")
	if err != nil {
		t.Fatalf("reading deploy.go: %v", err)
	}
	code := stripGoComments(src)
	if i := strings.Index(string(code), "base64"); i >= 0 {
		t.Errorf("deploy.go code references base64 again (offset %d): %q", i, snippet(string(code), i))
	}
}

// stripGoComments drops // and /* */ comments from Go source so a scan can look
// at code only (line comments are enough for this file; the pass strips both
// forms to stay honest if one is added).
func stripGoComments(src []byte) []byte {
	var out []byte
	lines := strings.Split(string(src), "\n")
	inBlock := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case inBlock:
			if i := strings.Index(trimmed, "*/"); i >= 0 {
				inBlock = false
				out = append(out, []byte(trimmed[i+2:])...)
			}
			out = append(out, '\n')
			continue
		case strings.HasPrefix(trimmed, "//"):
			out = append(out, '\n')
			continue
		case strings.HasPrefix(trimmed, "/*"):
			if i := strings.Index(trimmed, "*/"); i > 0 {
				out = append(out, []byte(trimmed[i+2:])...)
			} else {
				inBlock = true
			}
			out = append(out, '\n')
			continue
		}
		out = append(out, []byte(line)...)
		out = append(out, '\n')
	}
	return out
}

func snippet(s string, i int) string {
	lo := i - 40
	if lo < 0 {
		lo = 0
	}
	hi := i + 40
	if hi > len(s) {
		hi = len(s)
	}
	return s[lo:hi]
}

// emptyPathDir returns a directory with nothing in it, used as PATH so no
// external binary is reachable at all — the stock-firmware constraint (stock
// OpenWrt's BusyBox ships no base64 applet, and the command must not need one).
func emptyPathDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	return dir
}

// passwdStubProlog defines a `passwd` shell FUNCTION that records what the real
// passwd would read on stdin ($RECORD) — the shell resolves the function before
// any PATH lookup, so the SHIPPED command text under test stays byte-identical
// while the terminal `passwd root` call is intercepted. The stub needs nothing
// but shell builtins (read/printf), which is the point.
func passwdStubProlog() string {
	return "passwd() { while IFS= read -r line; do printf '%s\\n' \"$line\" >> \"$RECORD\"; done; " +
		"printf 'passwd: password changed\\n'; }\n"
}

// runRestricted executes script under argv[0]+argv[1:] with PATH pointing at an
// EMPTY directory (no binary reachable) and RECORD set for the stub.
func runRestricted(t *testing.T, emptyDir, recordPath string, argv []string) string {
	t.Helper()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = []string{"PATH=" + emptyDir, "RECORD=" + recordPath}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("restricted shell exited non-zero (%v); output:\n%s", err, out)
	}
	return string(out)
}

// TestSecretCarrierRoundTripsWithoutAnyRouterBinary runs the SHIPPED
// passwdCommand under a shell with an EMPTY PATH (only the recording passwd stub
// — no base64, no cat, nothing) and requires the password to reach `passwd`
// byte-exact, twice, as `passwd root` expects. It then runs the OLD base64
// command in the identical environment as a CONTROL, which must fail the way the
// reviewer's log does. If the control ever passed, the environment would have a
// base64 and the test would be proving nothing.
func TestSecretCarrierRoundTripsWithoutAnyRouterBinary(t *testing.T) {
	const pw = "hunter2$Secret" // shell metacharacters included on purpose

	emptyDir := emptyPathDir(t)
	record := filepath.Join(t.TempDir(), "passwd-received.txt")

	// (a) No plaintext in the command string — the property kept from the
	//     base64 carrier.
	cmdText := passwdCommand(pw)
	if strings.Contains(cmdText, pw) {
		t.Fatalf("the plaintext password appears in the router command: %q", cmdText)
	}
	if !strings.Contains(cmdText, "| passwd root") {
		t.Fatalf("passwd command must pipe into `passwd root`: %q", cmdText)
	}

	// (b) The shipped command sets the credential with NO binary reachable.
	out := runRestricted(t, emptyDir, record, []string{"/bin/sh", "-c", passwdStubProlog() + cmdText})
	got, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("passwd stub recorded nothing (the credential never reached passwd): %v\nshell output:\n%s", err, out)
	}
	want := pw + "\n" + pw + "\n"
	if string(got) != want {
		t.Fatalf("passwd received %q, want %q (shell output:\n%s)", got, want, out)
	}

	// (c) CONTROL: the old base64 carrier in the same environment must fail —
	//     the failure the reviewer saw (`ash: base64: not found`), with passwd
	//     never reached. If this passed, the environment would have a base64 and
	//     the test above would prove nothing.
	old := "pw=$(echo " + base64.StdEncoding.EncodeToString([]byte(pw)) + " | base64 -d) && " +
		"printf '%s\\n%s\\n' \"$pw\" \"$pw\" | passwd root 2>&1"
	if err := os.Remove(record); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	oldOut := runRestricted(t, emptyDir, record, []string{"/bin/sh", "-c", passwdStubProlog() + old})
	if !strings.Contains(oldOut, "base64: not found") {
		t.Errorf("CONTROL did not reproduce the reviewer's failure mode; output was:\n%s", oldOut)
	}
	if b, err := os.ReadFile(record); err == nil && len(b) > 0 {
		t.Errorf("CONTROL reached passwd (%q) — the test environment has a base64 and proves nothing", b)
	}

	// (d) Same shipped command under BusyBox ash, when one is installed: the
	//     shell a real router runs.
	bb, err := exec.LookPath("busybox")
	if err != nil {
		t.Log("busybox not installed locally: skipped the BusyBox ash sub-check")
		return
	}
	if err := os.Remove(record); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	bbOut := runRestricted(t, emptyDir, record, []string{bb, "sh", "-c", passwdStubProlog() + cmdText})
	bbGot, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("BusyBox ash: passwd stub recorded nothing: %v\noutput:\n%s", err, bbOut)
	}
	if string(bbGot) != want {
		t.Fatalf("BusyBox ash: passwd received %q, want %q (output:\n%s)", bbGot, want, bbOut)
	}
	// BusyBox's shell can find its own applets regardless of PATH (standalone
	// shell mode), so the OLD carrier only fails under a BusyBox that has no
	// base64 applet — i.e. the stock OpenWrt build. Run the control only then;
	// the stock-build control is exercised in the PR evidence instead.
	if bbHasApplet(t, bb, "base64") {
		t.Logf("%s ships a base64 applet (not a stock-OpenWrt build): skipped the BusyBox control", bb)
		return
	}
	if err := os.Remove(record); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	bbOld := runRestricted(t, emptyDir, record, []string{bb, "sh", "-c", passwdStubProlog() + old})
	if !strings.Contains(bbOld, "base64: not found") {
		t.Errorf("BusyBox ash CONTROL did not reproduce `base64: not found`; output:\n%s", bbOld)
	}
}

// bbHasApplet reports whether the BusyBox at path publishes applet.
func bbHasApplet(t *testing.T, bbPath, applet string) bool {
	t.Helper()
	out, err := exec.Command(bbPath, "--list").Output()
	if err != nil {
		t.Logf("%s --list failed (%v): cannot tell whether it has %q", bbPath, err, applet)
		return true // be conservative: assume present, skip the control
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) == applet {
			return true
		}
	}
	return false
}

// TestStaSetupScriptCarriersRoundTripWithoutAnyRouterBinary does the same for
// the WiFi STA credentials: the carrier lines are taken out of the SHIPPED
// script (so a change to the script is what gets tested) and expanded under a
// shell with an empty PATH, then compared byte-exact.
func TestStaSetupScriptCarriersRoundTripWithoutAnyRouterBinary(t *testing.T) {
	const ssid = "MyNet`; rm -rf / #"
	const key = "Pass'word$; id #"
	script := staSetupScript(ssid, key, "")

	var carriers []string
	for _, line := range strings.Split(script, "\n") {
		if strings.HasPrefix(line, "sta_ssid=") || strings.HasPrefix(line, "sta_key=") {
			carriers = append(carriers, line)
		}
	}
	if len(carriers) != 2 {
		t.Fatalf("expected exactly two carrier lines (ssid+key) in the STA script, found %d:\n%s", len(carriers), script)
	}
	if strings.Contains(script, ssid) || strings.Contains(script, key) {
		t.Fatalf("the STA script contains a raw credential (injection-prone):\n%s", script)
	}

	emptyDir := emptyPathDir(t)
	probe := strings.Join(carriers, "\n") + "\nprintf '%s\\n%s\\n' \"$sta_ssid\" \"$sta_key\"\n"
	out := runRestricted(t, emptyDir, filepath.Join(t.TempDir(), "unused.txt"), []string{"/bin/sh", "-c", probe})
	if out != ssid+"\n"+key+"\n" {
		t.Fatalf("STA carriers did not round-trip byte-exact under an empty-PATH shell:\ngot  %q\nwant %q", out, ssid+"\n"+key+"\n")
	}
}
