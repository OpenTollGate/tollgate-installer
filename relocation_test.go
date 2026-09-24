package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ─── the relocation chain must not abort (BLOCK 1 of the #52 review) ─────────
//
// moveLocalSubnet sends ONE command chain, its segments joined with " && ", so a
// single command that exits non-zero silently aborts the whole relocation: no
// `uci commit network`, no `ifup`, no move — while the caller logs a successful
// "Reconnected to router on <newIP>" (the reconnect fallback still answers on
// the original, unchanged address) and a later deploy step's `uci commit
// network` commits the staged delta out-of-band.
//
// `uci -q delete network.lan.gateway` is exactly such a command: it exits 1 when
// the option does not exist (`-q` silences the message, not the exit code), and
// nothing in this repo ever sets network.lan.gateway, so on the fresh router
// this installer exists for the option is ABSENT.
//
// These tests pin the fix from both ends — the SHAPE of the chain, and its
// BEHAVIOUR against a uci that exits 1 exactly like the real one — and the
// behaviour test carries a positive control, so a green result cannot come from
// a stub that quietly succeeds at everything.

// TestMoveLocalSubnetCommandsCannotAbortTheChain pins the chain shape: the
// gateway of the section being moved is cleared, every delete in the chain is
// unable to abort it, and the commit/ifup that actually perform the move are
// still there.
func TestMoveLocalSubnetCommandsCannotAbortTheChain(t *testing.T) {
	for _, tc := range []struct{ name, netSection, dhcpSection string }{
		{"br-lan (network.lan + dhcp.lan)", "lan", "lan"},
		{"br-private (network.private + dhcp.private)", "private", "private"},
		{"br-private without a DHCP section", "private", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmds := moveLocalSubnetCommands(tc.netSection, tc.dhcpSection, "10.44.9.1")
			joined := strings.Join(cmds, " && ")

			// The stale gateway of the section being MOVED is cleared. Scoping
			// this to lan only (as #52 did) leaves network.private.gateway
			// pointing at the old, unreachable subnet after a br-private move.
			want := "uci -q delete network." + tc.netSection + ".gateway || true"
			if !strings.Contains(joined, want) {
				t.Errorf("the relocation chain does not clear the stale gateway of the moved section in a form that cannot abort it — want %q in:\n%s", want, joined)
			}

			// NO uci delete anywhere in the chain may abort it: an unguarded
			// delete of an absent option kills the commit and the ifup after it.
			for _, c := range cmds {
				if !strings.Contains(c, "uci") || !strings.Contains(c, "delete") {
					continue
				}
				if !strings.Contains(c, "|| true") {
					t.Errorf("chain segment %q exits 1 when the option does not exist and would abort the whole relocation (verified on uci 16ff0bad in the #52 review's BLOCK 1)", c)
				}
			}

			// The commands that perform the move must survive the fix.
			for _, wantCmd := range []string{
				"uci set network." + tc.netSection + ".ipaddr='10.44.9.1'",
				"uci set network." + tc.netSection + ".netmask='255.255.255.0'",
				"uci commit network",
				"/sbin/ifup " + tc.netSection,
			} {
				if !strings.Contains(joined, wantCmd) {
					t.Errorf("the relocation chain no longer contains %q:\n%s", wantCmd, joined)
				}
			}
			if tc.dhcpSection == "" {
				if strings.Contains(joined, "dhcp..") || strings.Contains(joined, "uci -q set dhcp.") {
					t.Errorf("the chain sets a DHCP option for a move with no DHCP section:\n%s", joined)
				}
				return
			}
			for _, wantCmd := range []string{
				"uci -q set dhcp." + tc.dhcpSection + ".start='100'",
				"uci -q set dhcp." + tc.dhcpSection + ".limit='150'",
				"uci -q commit dhcp",
			} {
				if !strings.Contains(joined, wantCmd) {
					t.Errorf("the relocation chain no longer contains %q:\n%s", wantCmd, joined)
				}
			}
		})
	}
}

// stubUCI writes an `uci` executable modelling the two semantics BLOCK 1 rests
// on, both verified against the real CLI in the #52 review: `delete` of an
// option that does not exist exits 1 (`-q` silences the message, not the
// status), everything else exits 0. Every invocation is appended to a log so the
// test can see HOW FAR the chain got, and the option set lives in a state file
// so a delete of an option that DOES exist is observable too.
func stubUCI(t *testing.T) (dir, logPath, statePath string) {
	t.Helper()
	dir = t.TempDir()
	logPath = filepath.Join(dir, "uci.log")
	statePath = filepath.Join(dir, "uci.state")
	const script = `#!/bin/sh
# stub uci — models only what the relocation chain depends on.
[ "$1" = "-q" ] && shift
cmd="$1"; shift
printf '%s %s\n' "$cmd" "$*" >> "$UCI_STUB_LOG"
case "$cmd" in
delete)
	if [ -f "$UCI_STUB_STATE" ] && grep -qx "$1" "$UCI_STUB_STATE"; then
		grep -vx "$1" "$UCI_STUB_STATE" > "$UCI_STUB_STATE.tmp"
		mv "$UCI_STUB_STATE.tmp" "$UCI_STUB_STATE"
		exit 0
	fi
	exit 1
	;;
*)
	exit 0
	;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "uci"), []byte(script), 0o755); err != nil {
		t.Fatalf("writing the stub uci: %v", err)
	}
	return dir, logPath, statePath
}

// runUCIChain runs a command chain the way it reaches the router (segments
// joined with " && ") with the stub uci first on PATH, and returns the chain's
// exit status plus the stub's invocation log.
func runUCIChain(t *testing.T, dir, logPath, statePath string, cmds []string) (int, string) {
	t.Helper()
	cmd := exec.Command("sh", "-c", strings.Join(cmds, " && "))
	cmd.Env = append(os.Environ(),
		"PATH="+dir+":"+os.Getenv("PATH"),
		"UCI_STUB_LOG="+logPath,
		"UCI_STUB_STATE="+statePath,
	)
	err := cmd.Run()
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("running the chain: %v", err)
		}
		code = ee.ExitCode()
	}
	log, _ := os.ReadFile(logPath)
	return code, string(log)
}

// resetStubLog clears the stub's invocation log so the next run's log starts
// empty. A missing file is not an error: the log only exists once the stub has
// actually run, and every call site here resets BEFORE a run.
func resetStubLog(t *testing.T, logPath string) {
	t.Helper()
	if err := os.Remove(logPath); err != nil && !os.IsNotExist(err) {
		t.Fatalf("resetting the stub log: %v", err)
	}
}

// TestRelocationChainSurvivesAnAbsentGatewayOption is the reproduction of the
// #52 review's BLOCK 1, runnable without a router: the chain must reach
// `uci commit network` even though the lan gateway option does not exist (the
// stock-router case), and the positive control proves this stub reproduces the
// exit-1 behaviour it is protecting against.
func TestRelocationChainSurvivesAnAbsentGatewayOption(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no POSIX shell on PATH — the shape test above still pins the chain")
	}
	cmds := moveLocalSubnetCommands("lan", "lan", "10.44.9.1")

	// ── positive control: the unguarded delete exits 1 (this is BLOCK 1) ──
	dir, logPath, statePath := stubUCI(t)
	code, log := runUCIChain(t, dir, logPath, statePath, []string{"uci -q delete network.lan.gateway"})
	if code != 1 {
		t.Fatalf("control: `uci -q delete network.lan.gateway` with no such option exited %d, want 1 — the stub no longer models uci's exit code, so a green chain below would prove nothing (log: %q)", code, log)
	}
	resetStubLog(t, logPath)

	// ── the splice point: the chain up to and including the delete ──
	// This is where #52's chain died on a stock router. `|| true` keeps it at 0.
	code, log = runUCIChain(t, dir, logPath, statePath, cmds[:3])
	if code != 0 {
		t.Errorf("the chain up to and including the gateway delete exited %d, want 0 — a non-zero status here aborts the commit and the ifup that follow it (BLOCK 1):\n%s", code, log)
	}
	if !strings.Contains(log, "delete network.lan.gateway") {
		t.Errorf("the chain no longer deletes the stale gateway at all:\n%s", log)
	}

	// ── positive control, part 2: the chain as #52 built it must NOT get
	// through, or this test would pass on a tree that still has the defect ──
	preFix := make([]string, len(cmds))
	copy(preFix, cmds)
	preFix[2] = "uci -q delete network.lan.gateway" // #52's segment, verbatim
	resetStubLog(t, logPath)
	_, preFixLog := runUCIChain(t, dir, logPath, statePath, preFix)
	if strings.Contains(preFixLog, "commit network") {
		t.Fatalf("control: the UNGUARDED chain (as #52 shipped it) reached `uci commit network` — the stub does not reproduce BLOCK 1, so the green result below would prove nothing:\n%s", preFixLog)
	}
	resetStubLog(t, logPath)

	// ── the full chain: it must not die AT the delete ──
	// (The tail needs a router: /sbin/ifup does not exist here, so the chain's
	// final status is not the subject — reaching the commit is.)
	resetStubLog(t, logPath)
	_, log = runUCIChain(t, dir, logPath, statePath, cmds)
	delAt := strings.Index(log, "delete network.lan.gateway")
	commitAt := strings.Index(log, "commit network")
	dhcpAt := strings.Index(log, "commit dhcp")
	if delAt < 0 || commitAt < delAt || dhcpAt < delAt {
		t.Errorf("the relocation chain did not get past the gateway delete (delete at %d, commit network at %d, commit dhcp at %d) — the move would silently no-op:\n%s", delAt, commitAt, dhcpAt, log)
	}
	// The stub logs argv, and the chain reaches it through `sh -c`, so the
	// shell has already consumed the quoting: `uci set network.lan.ipaddr='X'`
	// arrives as the single argv "network.lan.ipaddr=X".
	if !strings.Contains(log, "set network.lan.ipaddr=10.44.9.1") {
		t.Errorf("the chain did not stage the new address before it died:\n%s", log)
	}

	// ── and when the option DOES exist, the guard is not a no-op ──
	// A router that really has a stale lan gateway must still have it removed.
	if err := os.WriteFile(statePath, []byte("network.lan.ipaddr\nnetwork.lan.gateway\n"), 0o644); err != nil {
		t.Fatalf("seeding the stub state: %v", err)
	}
	resetStubLog(t, logPath)
	_, log = runUCIChain(t, dir, logPath, statePath, cmds)
	if !strings.Contains(log, "commit network") {
		t.Errorf("with an existing lan gateway the chain still did not reach the commit:\n%s", log)
	}
	state, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("reading the stub state: %v", err)
	}
	if strings.Contains(string(state), "network.lan.gateway") {
		t.Errorf("the stale lan gateway survived the move (state: %q) — the old subnet's gateway would be left as a default route to nowhere", string(state))
	}
}
