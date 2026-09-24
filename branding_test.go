package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The admin board (:8090 HTTP / :8443 HTTPS) is owner-facing. It must NEVER be
// in nodogsplash's users_to_router list: that list is the PRE-AUTHENTICATION
// allow list, so an entry there is reachable by a client that has paid nothing,
// from the open captive SSID. The board is a root-capable login over plain HTTP
// (rpcd ACL: file exec, system.password_set, wallet_drain_cashu).
//
// The module's 99-tollgate-setup strips the entry (PR #546) — but the installer
// runs AFTER the package's uci-defaults and used to add it straight back, so a
// deploy re-armed the exposure. These tests bind the shipped command list.

// uciStub is a minimal `uci` implementation backed by a flat key=value state
// file. `-q` is accepted; `add_list` appends unless the value is present;
// `del_list` removes EVERY line whose value matches (entries contain spaces, so
// the match is whole-line and never word-split). Every invocation is appended
// to a log with one argv element per tab so the test can assert which VERB was
// used for which value.
const uciStub = `#!/bin/sh
STATE="${UCI_STATE}"
LOG="${UCI_LOG}"
printf '%s\n' "$*" >> "$LOG"
case "$1" in -q) shift ;; esac
cmd="$1"
[ "$#" -gt 0 ] && shift

key="${1%%=*}"
val="${1#*=}"

case "$cmd" in
  get)
    [ -f "$STATE" ] || exit 1
    found=0
    while IFS= read -r line; do
      case "$line" in "$key="*) printf '%s ' "${line#*=}"; found=1 ;; esac
    done < "$STATE"
    [ "$found" = 1 ] && echo && exit 0
    exit 1
    ;;
  set)
    grep -v -F -- "$key=" "$STATE" 2>/dev/null > "$STATE.tmp" || : > "$STATE.tmp"
    printf '%s=%s\n' "$key" "$val" >> "$STATE.tmp"
    mv "$STATE.tmp" "$STATE"
    ;;
  add_list)
    if ! grep -F -x -- "$key=$val" "$STATE" >/dev/null 2>&1; then
      printf '%s=%s\n' "$key" "$val" >> "$STATE"
    fi
    ;;
  del_list)
    grep -v -F -x -- "$key=$val" "$STATE" > "$STATE.tmp" 2>/dev/null || : > "$STATE.tmp"
    mv "$STATE.tmp" "$STATE"
    ;;
  commit|delete|add|show|export|revert) : ;;
  *) : ;;
esac
exit 0
`

// nodogsplashAllowKey is the UCI key under test.
const nodogsplashAllowKey = "nodogsplash.@nodogsplash[0].users_to_router"

// deployedRouterSeed is the users_to_router list a REAL already-deployed router
// carries: the module's pre-#546 allow list plus the admin board entries.
var deployedRouterSeed = []string{
	"allow tcp port 22",
	"allow tcp port 2121",
	"allow tcp port 8080",
	"allow tcp port 2050",
	"allow tcp port 8090",
	"allow tcp port 8443",
	"allow tcp port 2051",
	"allow tcp port 443",
}

// runBrandingUciCommands executes every shipped commands entry that starts with
// `uci ` against the stub, and returns the stub's recorded argv log.
func runBrandingUciCommands(t *testing.T, seed []string) (state map[string][]string, log string) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	stub := filepath.Join(bin, "uci")
	if err := writeFileForTest(stub, uciStub); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(stub, 0o755); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(dir, "uci.state")
	logPath := filepath.Join(dir, "uci.log")
	if err := writeFileForTest(logPath, ""); err != nil {
		t.Fatal(err)
	}
	var seedLines strings.Builder
	for _, v := range seed {
		seedLines.WriteString(nodogsplashAllowKey + "=" + v + "\n")
	}
	if err := writeFileForTest(statePath, seedLines.String()); err != nil {
		t.Fatal(err)
	}

	env := append(os.Environ(),
		"PATH="+bin+":"+os.Getenv("PATH"),
		"UCI_STATE="+statePath,
		"UCI_LOG="+logPath,
	)

	for _, cmd := range brandingCommands("tollgate-TEST", "192.168.1.1") {
		if !strings.HasPrefix(cmd, "uci ") {
			continue
		}
		shRunEnv(t, env, cmd)
	}

	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	state = map[string][]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		if line == "" {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		state[k] = append(state[k], v)
	}
	logRaw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	return state, string(logRaw)
}

// TestBrandingCommandsStripAdminBoardFromPreauthAllowList is the regression
// guard for the installer half of the pre-release blocker: after the branding
// step runs on an already-deployed router, the owner-facing admin board is NOT
// in the pre-auth allow list, and every customer-journey port still is.
func TestBrandingCommandsStripAdminBoardFromPreauthAllowList(t *testing.T) {
	state, uciLog := runBrandingUciCommands(t, deployedRouterSeed)

	got := state[nodogsplashAllowKey]
	for _, banned := range []string{"allow tcp port 8090", "allow tcp port 8443"} {
		for _, v := range got {
			if v == banned {
				t.Fatalf("%q is still in users_to_router after branding — a pre-auth guest can reach the admin board.\nfull list: %v", banned, got)
			}
		}
		if strings.Contains(uciLog, "add_list "+nodogsplashAllowKey+"="+banned) {
			t.Fatalf("branding still ADDS %q to the pre-auth allow list:\n%s", banned, uciLog)
		}
	}

	for _, want := range []string{
		"allow tcp port 2121", // backend API
		"allow tcp port 2050", // portal
		"allow tcp port 2051", // portal (stub)
		"allow tcp port 80",   // captive check / DNAT target
		"allow tcp port 8080", // LuCI
	} {
		if !containsString(got, want) {
			t.Errorf("customer-journey port %q is missing from users_to_router after branding — the rewrite removed too much.\nfull list: %v", want, got)
		}
	}
}

// TestBrandingCommandsNeverGrantAdminBoard is the text-level companion: no
// entry of the shipped command list may add a :8090/:8443 allowance, whatever
// the current UCI state is. (A future refactor that re-introduces the add would
// otherwise only fail when the state made the behavioural test notice.)
func TestBrandingCommandsNeverGrantAdminBoard(t *testing.T) {
	cmds := brandingCommands("tollgate-TEST", "192.168.1.1")
	if len(cmds) == 0 {
		t.Fatal("brandingCommands returned no commands")
	}
	var seenDel bool
	for _, cmd := range cmds {
		for _, port := range []string{"8090", "8443"} {
			grant := "add_list nodogsplash.@nodogsplash[0].users_to_router='allow tcp port " + port + "'"
			if strings.Contains(cmd, grant) {
				t.Errorf("shipped branding command grants :%s pre-auth: %q", port, cmd)
			}
			revoke := "del_list nodogsplash.@nodogsplash[0].users_to_router='allow tcp port " + port + "'"
			if strings.Contains(cmd, revoke) {
				seenDel = true
			}
		}
	}
	if !seenDel {
		t.Error("brandingCommands no longer REVOKES the :8090/:8443 pre-auth allowance — a router that already carries it (every deployed router) would keep it")
	}
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
