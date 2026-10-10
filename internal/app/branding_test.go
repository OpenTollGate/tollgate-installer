package app

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
  show)
    # uci show <pkg>: section lines bare, option lines single-quoted — close
    # enough to real uci for the parser under test (the guest-AP writer reads it).
    [ "${1:-}" = "wireless" ] || exit 0
    while IFS= read -r line; do
      case "${line%%=*}" in
        wireless.*.*) printf "%s='%s'\n" "${line%%=*}" "${line#*=}" ;;
        wireless.*)   printf '%s\n' "$line" ;;
      esac
    done < "$STATE"
    ;;
  commit|delete|add|export|revert) : ;;
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

// testDeviceIdentity is the identity the branding assertions are built on: the
// code "TEST", as if the router had resolved and stored it.
func testDeviceIdentity() deviceIdentity {
	return deviceIdentity{
		Code:        "TEST",
		Nym:         deviceIdentityDefaultNym,
		Source:      "store",
		Hostname:    "tollgate-TEST",
		SSID:        "!TollGate-TEST",
		PrivateSSID: deviceIdentityDefaultNym + "-TEST",
	}
}

// uciStubEnv builds a PATH shim containing the stub `uci` plus the flat state
// file it reads and the log it writes, and returns the environment for
// shRunEnv. seedLines are raw "key=value" state lines (one list entry per
// line), the same shape the stub stores.
func uciStubEnv(t *testing.T, seedLines []string) (env []string, statePath, logPath string) {
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
	statePath = filepath.Join(dir, "uci.state")
	logPath = filepath.Join(dir, "uci.log")
	if err := writeFileForTest(logPath, ""); err != nil {
		t.Fatal(err)
	}
	seed := ""
	if len(seedLines) > 0 {
		seed = strings.Join(seedLines, "\n") + "\n"
	}
	if err := writeFileForTest(statePath, seed); err != nil {
		t.Fatal(err)
	}
	env = append(os.Environ(),
		"PATH="+bin+":"+os.Getenv("PATH"),
		"UCI_STATE="+statePath,
		"UCI_LOG="+logPath,
	)
	return env, statePath, logPath
}

// readUciState parses the stub's flat state file into key -> values.
func readUciState(t *testing.T, statePath string) map[string][]string {
	t.Helper()
	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	state := map[string][]string{}
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
	return state
}

func uciStateValue(t *testing.T, state map[string][]string, key string) string {
	t.Helper()
	values := state[key]
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

// runBrandingUciCommands executes every shipped commands entry that starts with
// `uci ` against the stub, and returns the stub's recorded argv log.
func runBrandingUciCommands(t *testing.T, seed []string) (state map[string][]string, log string) {
	t.Helper()
	var seedLines []string
	for _, v := range seed {
		seedLines = append(seedLines, nodogsplashAllowKey+"="+v)
	}
	env, statePath, logPath := uciStubEnv(t, seedLines)

	for _, cmd := range brandingCommands(testDeviceIdentity(), "192.168.1.1") {
		if !strings.HasPrefix(cmd, "uci ") {
			continue
		}
		shRunEnv(t, env, cmd)
	}

	state = readUciState(t, statePath)
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
	cmds := brandingCommands(testDeviceIdentity(), "192.168.1.1")
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

// ─── the device code: one code, stored, reused (branding half) ───────────
//
// The contract is owned by the module's uci-defaults script
// (OpenTollGate/tollgate-module-basic-go, packaging/files/etc/uci-defaults/
// 99-tollgate-setup → setup_device_identity) and the decision record lives in
// docs/architecture/one-device-code.md there. The installer is the writer that
// runs LAST on a deployed router, so it must read the SAME store with the SAME
// adoption order — a second, private mint here is what gave the bench MT3000
// three different names for one router. These tests pin the router-side
// resolver (deviceIdentityScript) and the writer (brandingCommands) against the
// stub uci; the module's tests/uci-defaults-device-code_test.sh pins the same
// case table on the other side, so a change to the order or the alphabet fails
// one of the two.

// identityStorePath is the store the sandboxed resolver is pointed at. The one
// absolute path in deviceIdentityScript is rewritten (below), the same way the
// module's offline suites rewrite SETUP_FLAG/LOGFILE, so a test never touches
// the host's /etc/config.
const identityStoreAnchor = "CODE_STORE=/etc/config/tollgate"

// runIdentityScript runs the SHIPPED resolver against the stub uci, with its
// store path redirected into the sandbox, and returns what it printed plus the
// uci state afterwards.
func runIdentityScript(t *testing.T, env []string, statePath string) (deviceIdentity, string) {
	t.Helper()
	store := filepath.Join(filepath.Dir(statePath), "tollgate.conf")
	script := strings.Replace(deviceIdentityScript, identityStoreAnchor, "CODE_STORE="+store, 1)
	if strings.Contains(script, "/etc/config/tollgate") {
		t.Fatalf("could not redirect the device-code store into the sandbox — deviceIdentityScript no longer declares %q", identityStoreAnchor)
	}
	out := shRunEnv(t, env, script)
	return parseDeviceIdentity(out), out
}

// appendUciState adds a state line to the stub's file (simulating a second
// writer having moved the router's names since the last run).
func appendUciState(t *testing.T, statePath, line string) {
	t.Helper()
	f, err := os.OpenFile(statePath, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(line + "\n"); err != nil {
		t.Fatal(err)
	}
}

// TestDeviceIdentityScriptAdoptionOrder pins the adoption order and the derived
// names against the shipped resolver. The order is the whole point: every
// router already deployed has an EMPTY store, so steps 2 and 3 are what let it
// converge on the code it is already known by instead of collecting a third one.
func TestDeviceIdentityScriptAdoptionOrder(t *testing.T) {
	cases := []struct {
		name        string
		seed        []string
		wantCode    string
		wantSource  string
		wantNym     string
		wantPrivate string
	}{
		{
			name: "the store is authoritative over every other name",
			seed: []string{
				"tollgate.device=device", "tollgate.device.code=OQ3Q", "tollgate.device.nym=c08r4d0r",
				"system.@system[0].hostname=tollgate-ZZZZ",
				"wireless.tollgate_2g_open.ssid=TollGate-1111",
			},
			wantCode: "OQ3Q", wantSource: "store", wantNym: "c08r4d0r", wantPrivate: "c08r4d0r-OQ3Q",
		},
		{
			name: "a lowercase store value is normalised",
			seed: []string{
				"tollgate.device=device", "tollgate.device.code=oq3q",
				"system.@system[0].hostname=tollgate-ZZZZ",
			},
			wantCode: "OQ3Q", wantSource: "store", wantNym: "c08r4d0r", wantPrivate: "c08r4d0r-OQ3Q",
		},
		{
			name: "a junk store value is re-derived, never trusted",
			seed: []string{
				"tollgate.device=device", "tollgate.device.code=nope!",
				"system.@system[0].hostname=tollgate-7Q7Q",
			},
			wantCode: "7Q7Q", wantSource: "hostname", wantNym: "c08r4d0r", wantPrivate: "c08r4d0r-7Q7Q",
		},
		{
			// The bench box: the installer wrote the hostname, a later deploy
			// re-minted the SSID, and no store existed on either side.
			name: "an already-deployed router adopts its hostname, not the stale SSID",
			seed: []string{
				"system.@system[0].hostname=tollgate-OQ3Q",
				"wireless.tollgate_2g_open.ssid=tollgate-0GLK",
			},
			wantCode: "OQ3Q", wantSource: "hostname", wantNym: "c08r4d0r", wantPrivate: "c08r4d0r-OQ3Q",
		},
		{
			name: "a custom hostname is not a code source, the captive SSID is",
			seed: []string{
				"system.@system[0].hostname=OperatorBox",
				"wireless.tollgate_2g_open.ssid=TollGate-9K2M",
			},
			wantCode: "9K2M", wantSource: "captive-ssid", wantNym: "c08r4d0r", wantPrivate: "c08r4d0r-9K2M",
		},
		{
			// The guest-facing captive SSID now carries a leading '!' so it
			// sorts first in an alphabetically-sorted WiFi scan list (0x21 sorts
			// before digits and letters). The reader must strip that optional
			// decoration BEFORE the brand-prefix compare, or a router already in
			// the field under the new spelling would be re-named by the next
			// deploy. The bare spelling above must keep working too.
			name: "a bang-prefixed captive SSID is still a code source",
			seed: []string{
				"system.@system[0].hostname=OperatorBox",
				"wireless.tollgate_2g_open.ssid=!TollGate-7F3A",
			},
			wantCode: "7F3A", wantSource: "captive-ssid", wantNym: "c08r4d0r", wantPrivate: "c08r4d0r-7F3A",
		},
		{
			name: "a stock default_radio0 SSID is a code source too",
			seed: []string{
				"system.@system[0].hostname=myrouter",
				"wireless.default_radio0.ssid=tollgate-7Q7Q",
			},
			wantCode: "7Q7Q", wantSource: "captive-ssid", wantNym: "c08r4d0r", wantPrivate: "c08r4d0r-7Q7Q",
		},
		{
			// The module mints <nym>-<suffix> with the operator's own nym; the
			// installer must keep that prefix rather than assume the default.
			name: "the nym is adopted from a machine-shaped private SSID",
			seed: []string{
				"system.@system[0].hostname=tollgate-OQ3Q",
				"wireless.private_radio0.ssid=amperstrand-11AA",
			},
			wantCode: "OQ3Q", wantSource: "hostname", wantNym: "amperstrand", wantPrivate: "amperstrand-OQ3Q",
		},
		{
			// `tollgate network private rename <name>` is the escape hatch: a
			// renamed SSID is not machine-shaped, so it is not re-derived.
			name: "an operator's renamed private SSID is preserved",
			seed: []string{
				"system.@system[0].hostname=tollgate-OQ3Q",
				"wireless.private_radio0.ssid=MyNewNetwork",
			},
			wantCode: "OQ3Q", wantSource: "hostname", wantNym: "c08r4d0r", wantPrivate: "MyNewNetwork",
		},
		{
			// The private SSID is interpolated into a single-quoted shell line
			// by brandingCommands, so a value carrying a quote must not be
			// adopted as the nym AND must not be preserved as-is: the resolver
			// falls back to the default nym and the code-derived SSID.
			name: "a value that cannot be quoted is neither adopted nor preserved",
			seed: []string{
				"system.@system[0].hostname=tollgate-OQ3Q",
				"wireless.private_radio0.ssid=x';reboot #-1234",
			},
			wantCode: "OQ3Q", wantSource: "hostname", wantNym: "c08r4d0r", wantPrivate: "c08r4d0r-OQ3Q",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, statePath, logPath := uciStubEnv(t, tc.seed)
			id, out := runIdentityScript(t, env, statePath)
			if id.Code != tc.wantCode {
				t.Fatalf("code = %q, want %q (resolver output:\n%s)", id.Code, tc.wantCode, out)
			}
			if id.Source != tc.wantSource {
				t.Errorf("code source = %q, want %q", id.Source, tc.wantSource)
			}
			if id.Nym != tc.wantNym {
				t.Errorf("nym = %q, want %q", id.Nym, tc.wantNym)
			}
			if id.PrivateSSID != tc.wantPrivate {
				t.Errorf("private SSID = %q, want %q", id.PrivateSSID, tc.wantPrivate)
			}
			// The three identifiers share the ONE code.
			if want := "tollgate-" + tc.wantCode; id.Hostname != want {
				t.Errorf("hostname = %q, want %q", id.Hostname, want)
			}
			if want := "!TollGate-" + tc.wantCode; id.SSID != want {
				t.Errorf("captive SSID = %q, want %q", id.SSID, want)
			}
			// And the value is STORED, so the next writer reads it back.
			state := readUciState(t, statePath)
			if got := uciStateValue(t, state, "tollgate.device.code"); got != tc.wantCode {
				t.Errorf("stored code = %q, want %q", got, tc.wantCode)
			}
			if got := uciStateValue(t, state, "tollgate.device.nym"); got != tc.wantNym {
				t.Errorf("stored nym = %q, want %q", got, tc.wantNym)
			}
			logRaw, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(logRaw), "commit tollgate") {
				t.Errorf("the store was never committed (uci log:\n%s)", logRaw)
			}
		})
	}
}

// TestDeviceIdentityScriptNeverReMints is the core promise: once a code is
// stored, nothing re-mints it — not a later deploy, not a hostname that another
// writer moved, not the module re-running its setup.
func TestDeviceIdentityScriptNeverReMints(t *testing.T) {
	env, statePath, _ := uciStubEnv(t, []string{"system.@system[0].hostname=OpenWrt"})
	first, out := runIdentityScript(t, env, statePath)
	if first.Code == "" {
		t.Fatalf("first run minted no code (output:\n%s)", out)
	}
	if first.Source != "minted" {
		t.Errorf("first run source = %q, want %q", first.Source, "minted")
	}
	for i, r := range first.Code {
		if !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') {
			t.Fatalf("minted code %q has a character outside [A-Z0-9] at %d", first.Code, i)
		}
	}
	if len(first.Code) != 4 {
		t.Fatalf("minted code %q is not four characters", first.Code)
	}

	// A second writer moves the hostname (the installer's own branding does
	// exactly this) and a third name appears on the captive AP. The store wins.
	appendUciState(t, statePath, "system.@system[0].hostname=tollgate-ZZZZ")
	appendUciState(t, statePath, "wireless.tollgate_2g_open.ssid=TollGate-1111")
	second, out := runIdentityScript(t, env, statePath)
	if second.Code != first.Code {
		t.Fatalf("the code was re-minted: %q -> %q (output:\n%s)", first.Code, second.Code, out)
	}
	if second.Source != "store" {
		t.Errorf("second run source = %q, want %q", second.Source, "store")
	}
}

// TestBrandingWritesAllThreeIdentifiers is the behavioural pin on the writer:
// hostname, captive SSID and private SSID all carry the resolved code, the
// uplink STA and the private PSK are left alone, and the whole chain completes
// (a writer that returns non-zero would truncate the " && " chain the deploy
// runs, silently dropping every command after it).
func TestBrandingWritesAllThreeIdentifiers(t *testing.T) {
	seed := []string{
		"system.@system[0]=system", "system.@system[0].hostname=OpenWrt",
		"wireless.default_radio0=wifi-iface", "wireless.default_radio0.mode=ap", "wireless.default_radio0.ssid=TollGate-OLD",
		"wireless.default_radio1=wifi-iface", "wireless.default_radio1.mode=ap", "wireless.default_radio1.ssid=TollGate-OLD",
		"wireless.tollgate_2g_open=wifi-iface", "wireless.tollgate_2g_open.mode=ap", "wireless.tollgate_2g_open.ssid=tollgate-OLD",
		"wireless.tollgate_5g_open=wifi-iface", "wireless.tollgate_5g_open.mode=ap", "wireless.tollgate_5g_open.ssid=tollgate-OLD",
		"wireless.tollgate_uplink=wifi-iface", "wireless.tollgate_uplink.mode=sta", "wireless.tollgate_uplink.ssid=UpstreamAP",
		"wireless.private_radio0=wifi-iface", "wireless.private_radio0.mode=ap",
		"wireless.private_radio0.ssid=mgmt-old", "wireless.private_radio0.key=Operator-Chosen-Key-01",
		"wireless.private_radio1=wifi-iface", "wireless.private_radio1.mode=ap", "wireless.private_radio1.ssid=mgmt-old",
		"nodogsplash.@nodogsplash[0]=nodogsplash",
	}
	env, statePath, _ := uciStubEnv(t, seed)
	id := testDeviceIdentity()

	var writers []string
	for _, cmd := range brandingCommands(id, "192.168.1.1") {
		for _, prefix := range []string{
			"uci -q set system.@system[0].hostname",
			"for i in $(uci -q show wireless",
			"if uci -q get wireless.private_radio0",
			"if uci -q get wireless.private_radio1",
			"uci -q set nodogsplash.@nodogsplash[0].gatewayname",
		} {
			if strings.HasPrefix(cmd, prefix) {
				writers = append(writers, cmd)
			}
		}
	}
	if len(writers) != 5 {
		t.Fatalf("expected 5 identifier writers in the shipped command list, found %d", len(writers))
	}
	out := shRunEnv(t, env, strings.Join(writers, " && ")+" && echo CHAIN_OK")
	if !strings.Contains(out, "CHAIN_OK") {
		t.Fatalf("the identifier writers did not complete the && chain:\n%s", out)
	}

	state := readUciState(t, statePath)
	if got := uciStateValue(t, state, "system.@system[0].hostname"); got != id.Hostname {
		t.Errorf("hostname = %q, want %q", got, id.Hostname)
	}
	for _, sec := range []string{"default_radio0", "default_radio1", "tollgate_2g_open", "tollgate_5g_open"} {
		if got := uciStateValue(t, state, "wireless."+sec+".ssid"); got != id.SSID {
			t.Errorf("captive SSID on %s = %q, want %q (%s)", sec, got, id.SSID, sec)
		}
	}
	for _, sec := range []string{"private_radio0", "private_radio1"} {
		if got := uciStateValue(t, state, "wireless."+sec+".ssid"); got != id.PrivateSSID {
			t.Errorf("private SSID on %s = %q, want %q (%s)", sec, got, id.PrivateSSID, sec)
		}
	}
	if got := uciStateValue(t, state, "wireless.tollgate_uplink.ssid"); got != "UpstreamAP" {
		t.Errorf("the uplink STA's SSID was rewritten to %q — branding must never touch a station interface", got)
	}
	if got := uciStateValue(t, state, "wireless.private_radio0.key"); got != "Operator-Chosen-Key-01" {
		t.Errorf("the private PSK was rewritten to %q — re-keying drops every paired admin device", got)
	}
	if got := uciStateValue(t, state, "nodogsplash.@nodogsplash[0].gatewayname"); got != id.SSID {
		t.Errorf("nodogsplash gatewayname = %q, want %q", got, id.SSID)
	}
}

// TestPrivateSSIDCommandIsANoOpWithoutTheSection covers the one shape trap in
// the writer: these commands are joined with " && ", so a bare
// `uci -q get … && uci -q set …` would return non-zero on a router whose
// private AP lives on another section (or does not exist) and truncate
// everything after it — including the commits.
func TestPrivateSSIDCommandIsANoOpWithoutTheSection(t *testing.T) {
	env, statePath, _ := uciStubEnv(t, []string{"wireless.private_radio0=wifi-iface", "wireless.private_radio0.ssid=old"})
	out := shRunEnv(t, env, privateSSIDCommand("private_radio9", "c08r4d0r-TEST")+" && echo CHAIN_OK")
	if !strings.Contains(out, "CHAIN_OK") {
		t.Fatalf("a missing private section truncated the chain:\n%s", out)
	}
	state := readUciState(t, statePath)
	if got := uciStateValue(t, state, "wireless.private_radio9.ssid"); got != "" {
		t.Errorf("a section that does not exist was created (ssid=%q) — the module owns the private network's layout", got)
	}
	if got := uciStateValue(t, state, "wireless.private_radio0.ssid"); got != "old" {
		t.Errorf("private_radio0.ssid = %q, want it untouched by another section's command", got)
	}
}

// TestPrivateSSIDCommandRefusesAValueItCannotQuote closes the hole the resolver
// cannot close on its own: the branding lines are joined with " && " and run as
// root on the router, so an SSID carrying a single quote would end the quote and
// let the rest of the value be read as shell syntax. A value like that is refused
// rather than escaped — and the refusal must not be a blanket no-op, so the safe
// case is asserted in the same test.
func TestPrivateSSIDCommandRefusesAValueItCannotQuote(t *testing.T) {
	unsafe := privateSSIDCommand("private_radio0", "x';reboot #")
	if strings.Contains(unsafe, "reboot") {
		t.Errorf("the value that cannot be quoted reached the command line: %q", unsafe)
	}
	if !strings.Contains(unsafe, "not written") {
		t.Errorf("expected a refusal for an unquotable value, got %q", unsafe)
	}
	if !ssidSafeForShell("c08r4d0r-OQ3Q") || ssidSafeForShell("x'y") || ssidSafeForShell("") {
		t.Errorf("ssidSafeForShell disagrees with the refusal above")
	}
	// The captive SSID now carries a leading '!' so it sorts first in an
	// alphabetically-sorted WiFi list. '!' is history-expansion syntax in an
	// INTERACTIVE shell, but this guard exists to keep a value quotable inside
	// the single-quoted uci lines this file builds, and '!' is not a quoting
	// hazard — so it must be ACCEPTED. The guard must still refuse a quote and
	// the empty string (asserted right above and again here).
	if !ssidSafeForShell("!TollGate-7F3A") {
		t.Errorf("ssidSafeForShell refused %q — the leading '!' the captive SSID now carries is not a quoting hazard", "!TollGate-7F3A")
	}
	if ssidSafeForShell("x'y") || ssidSafeForShell("") {
		t.Errorf("ssidSafeForShell stopped refusing a quote / the empty string")
	}
	safe := privateSSIDCommand("private_radio0", "c08r4d0r-OQ3Q")
	if !strings.Contains(safe, "uci -q set wireless.private_radio0.ssid='c08r4d0r-OQ3Q'") {
		t.Errorf("a quotable value was not written: %q", safe)
	}
	// The refusal must still leave the " && " chain intact.
	env, _, _ := uciStubEnv(t, []string{"wireless.private_radio0=wifi-iface"})
	out := shRunEnv(t, env, privateSSIDCommand("private_radio0", "x'y")+" && echo CHAIN_OK")
	if !strings.Contains(out, "CHAIN_OK") {
		t.Fatalf("the refusal truncated the command chain:\n%s", out)
	}
}

// TestBrandingNeverMintsACode is the text-level companion: the writer must not
// contain a mint, whatever the state. A second mint in this repo is exactly how
// one router ended up with three names.
func TestBrandingNeverMintsACode(t *testing.T) {
	for _, cmd := range brandingCommands(testDeviceIdentity(), "192.168.1.1") {
		for _, banned := range []string{"hexdump", "/dev/urandom", "cryptorand", "$RANDOM"} {
			if strings.Contains(cmd, banned) {
				t.Errorf("shipped branding command %q mints a code (%s) — the code is resolved once, on the router", cmd, banned)
			}
		}
	}
	// The resolver, by contrast, must be able to mint (step 4) and must read the
	// store before it does.
	if !strings.Contains(deviceIdentityScript, "tollgate.device.code") {
		t.Error("deviceIdentityScript no longer reads the stored code — nothing would be reused")
	}
	if !strings.Contains(deviceIdentityScript, "hexdump") {
		t.Error("deviceIdentityScript has no mint path — a router with no stored code and no machine-shaped name could not be named")
	}
}

// TestBangPrefixedSSIDSurvivesNonInteractiveShell is the escaping proof the
// leading '!' needs. The captive SSID is written inside a double-quoted shell
// assignment (`SSID="!TollGate-$CODE"`) that ships to the router over SSH. In
// an INTERACTIVE bash the '!' would be history-expansion syntax and be eaten;
// the router runs the script through a NON-interactive channel, where history
// expansion is off. This runs the SAME shipped resolver under `bash -c` (a
// non-interactive shell, the closest strict stand-in for the SSH command
// channel) and asserts the '!' survives into the SSID the resolver emits.
func TestBangPrefixedSSIDSurvivesNonInteractiveShell(t *testing.T) {
	env, statePath, _ := uciStubEnv(t, []string{
		"system.@system[0].hostname=tollgate-7F3A",
	})
	store := filepath.Join(filepath.Dir(statePath), "tollgate.conf")
	script := strings.Replace(deviceIdentityScript, identityStoreAnchor, "CODE_STORE="+store, 1)
	out := shRunBashEnv(t, env, script)
	id := parseDeviceIdentity(out)
	if id.SSID != "!TollGate-7F3A" {
		t.Fatalf("captive SSID = %q, want %q — the leading '!' was lost (history expansion or quoting)\nresolver output:\n%s", id.SSID, "!TollGate-7F3A", out)
	}
	if !strings.Contains(deviceIdentityScript, `SSID="!TollGate-$CODE"`) {
		t.Errorf("the shipped resolver no longer emits the bang-prefixed captive SSID in its double-quoted assignment")
	}
}
