package main

import (
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

const (
	// tollgatePackage is the opkg/apk package name for the tollgate-wrt
	// package (installed via the OpenWrt feed fallback path).
	tollgatePackage = "tollgate-wrt"
)

var (
	// tollgate-wrt .ipk download URL (OpenWrt <= 24.10 opkg back-compat).
	//
	// NOTE (v0.5.0 de-brand): the tollgate-wrt package now ships from the
	// OpenTollGate org's tollgate-module-basic-go releases. The nftables
	// enforcement rules (PR #283) ship INSIDE this ipk under
	// ./etc/nftables.d/, so no separate overlay download is needed.
	//
	// NOTE (feat/feed-per-arch-urls): the per-arch selectable URLs are derived
	// generically in arch.go via feedAssetURL (feed-primary, GitHub fallback).
	// These two vars are the aarch64_cortex-a53 PRIMARY feed assets, used by
	// the PreStage cache (stageAssetURLs) to pre-download the bench arch in both
	// formats before arch detection runs at install time. They are DERIVED from
	// feedAssetURL so they can never drift from the generic URL builder.
	tollgatePkgURL = feedAssetURL("aarch64_cortex-a53", ".ipk")
	// tollgate-wrt .apk download URL (OpenWrt 25+ with APK support).
	// OpenWrt 25.12+ cannot install legacy .ipk (ar archive) packages.
	tollgatePkgAPKURL = feedAssetURL("aarch64_cortex-a53", ".apk")
)

// ─── Secret carriers ─────────────────────────────────────────────
//
// Secrets that must reach the router (the root password, the WiFi STA
// SSID/passphrase) cross as OCTAL-ESCAPE carriers rather than shell string
// literals: the value is escaped host-side into `\0NNN` sequences and expanded
// on the router by the shell BUILTIN printf:
//
//	pw=$(printf '%b' '\0120\0141\0142')
//
// Why NOT a router-side base64 decode (the constraint that must not regress):
//
//   - Stock OpenWrt's BusyBox does NOT ship the base64 applet. In OpenWrt it is
//     the separate `coreutils-base64` package, absent from a stock image, so a
//     router-side base64 decode fails with `ash: base64: not found` on the
//     wizard's own documented target. The command substitution then returns
//     127, the `&&` chain short-circuits, passwd never runs at all, and the
//     fail-closed guard correctly aborts EVERY deploy at step 4 with an empty
//     credential. Reproduced by the reviewer E2E on a fresh stock OpenWrt
//     24.10.1 x86_64 VM, and locally against a stock 24.10.8 x86_64 rootfs
//     (BusyBox v1.36.1: 0/128 applet symlinks mention base64) —
//     OpenTollGate/tollgate-installer#46, review 5304937880.
//   - printf needs no router-side binary AT ALL. It is a builtin of BusyBox ash
//     (verified against stock OpenWrt 24.10.x /bin/busybox: `printf is a shell
//     builtin`) and of dash/bash, and `%b` with `\0NNN` octal escapes is POSIX.
//     Stock OpenWrt also ships /usr/bin/printf, so either resolution works.
//
// The carrier keeps both properties the base64 carrier was there for:
//
//	(a) no dependency on any router-side binary beyond the shell and what
//	    BusyBox ships by default;
//	(b) the plaintext never appears in the SSH command string (process argv and
//	    shell history on the router) and never participates in shell parsing, so
//	    a password or SSID cannot inject shell.
//
// Regression guards live in secret_carrier_test.go:
// TestRouterCommandsNeverDependOnBase64 fails if a base64 carrier comes back in
// a generated router command (or in deploy.go's code), and
// TestSecretCarrierRoundTripsWithoutAnyRouterBinary executes the SHIPPED command
// under a shell whose PATH is empty, so the secret must land byte-exact with no
// binary available at all.

// shellOctalCarrier returns s as `\0NNN` escapes for the shell builtin
// `printf '%b'`, which expands them back to s byte-for-byte. Every byte a shell
// variable can hold round-trips; NUL cannot (no shell carrier can carry it, the
// base64 one did not either).
//
// Each byte is emitted as exactly four characters — backslash, `0`, three octal
// digits — so an escape is self-delimiting and can never run into the byte that
// follows it.
func shellOctalCarrier(s string) string {
	var b strings.Builder
	b.Grow(len(s) * 4)
	for i := 0; i < len(s); i++ {
		fmt.Fprintf(&b, `\0%03o`, s[i])
	}
	return b.String()
}

// octalCarrierVar returns the router-side command substitution that expands the
// octal carrier for value into the shell variable name. The plaintext is not
// part of the returned text (see the Secret carriers block above).
func octalCarrierVar(name, value string) string {
	return name + `=$(printf '%b' '` + shellOctalCarrier(value) + `')`
}

// passwdCommand returns the router-side command that sets the root password.
// The password crosses as an octal carrier expanded by the shell builtin printf
// and is never in the command string itself; the pipeline shape is the one that
// main and #41–#45 used successfully on stock OpenWrt
// (`printf '%s\n%s\n' pw pw | passwd root`), so no router-side binary is
// involved beyond the shell.
func passwdCommand(password string) string {
	return octalCarrierVar("pw", password) + " && " +
		"printf '%s\\n%s\\n' \"$pw\" \"$pw\" | passwd root 2>&1"
}

// routerRun runs one router-side command and returns its combined output.
// sshRun satisfies it; tests inject a fake router.
type routerRun func(cmd string) string

// ─── Root credential (fail closed) ────────────────────────────────

// rootHashState is what /etc/shadow says about root's password.
type rootHashState string

const (
	// rootHashEmpty: root's shadow hash is empty (or root has no shadow
	// line at all). This is the DANGEROUS state — rpcd's
	// rpc_login_test_password() returns true on an empty hash, so ANY
	// password authenticates, and dropbear accepts an empty password too.
	rootHashEmpty rootHashState = "empty"
	// rootHashLocked: '!' / '*' — the account cannot be logged into with a
	// password at all (crypt() can never match). Not credential-less.
	rootHashLocked rootHashState = "locked"
	// rootHashSet: a real hash — only the real password works.
	rootHashSet rootHashState = "set"
	// rootHashUnknown: the probe produced no recognisable answer (awk or
	// /etc/shadow missing, or the SSH session died) — never assume this is
	// safe.
	rootHashUnknown rootHashState = "unknown"
)

// rootHashProbeCmd prints exactly one of `empty` / `locked` / `set` for root's
// /etc/shadow entry. Field 2 is the hash; a MISSING root line is reported as
// empty because rpcd then has no hash to check either.
const rootHashProbeCmd = `h=$(awk -F: '$1=="root"{print $2}' /etc/shadow 2>/dev/null); ` +
	`case "$h" in "") echo empty ;; '!'*|'*'*) echo locked ;; ?*) echo set ;; esac`

// parseRootHashState maps the probe output to a state. Only the exact probe
// token is accepted (case-sensitive — `rootHashEmpty` from an older/failed
// probe must NOT be read as `empty`); anything else, empty output included, is
// rootHashUnknown and must be treated as unsafe.
func parseRootHashState(out string) rootHashState {
	fields := strings.Fields(out)
	if len(fields) == 0 {
		return rootHashUnknown
	}
	switch state := rootHashState(fields[len(fields)-1]); state {
	case rootHashEmpty, rootHashLocked, rootHashSet:
		return state
	}
	return rootHashUnknown
}

// rootPasswordAlphabet omits characters that are hostile to a shell command or
// to a human retyping the value: no quotes, backslashes, whitespace, or
// look-alike pairs (0/O, 1/l/I).
const rootPasswordAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"

// rootPasswordLength is the generated credential length (log2(57)*20 ≈ 117
// bits of entropy).
const rootPasswordLength = 20

// generateRootPassword returns a fresh root credential from crypto/rand. It
// FAILS (rather than falling back to a weaker source) if the OS CSPRNG is
// unavailable — a predictable root password is worse than a failed deploy.
//
// Rejection sampling above 4*len(alphabet) keeps every character uniformly
// distributed (no modulo bias).
func generateRootPassword() (string, error) {
	const limit = 256 - (256 % len(rootPasswordAlphabet)) // 228
	out := make([]byte, 0, rootPasswordLength)
	buf := make([]byte, rootPasswordLength)
	for len(out) < rootPasswordLength {
		if _, err := cryptorand.Read(buf); err != nil {
			return "", fmt.Errorf("crypto/rand unavailable: %w", err)
		}
		for _, b := range buf {
			if int(b) >= limit {
				continue
			}
			out = append(out, rootPasswordAlphabet[int(b)%len(rootPasswordAlphabet)])
			if len(out) == rootPasswordLength {
				break
			}
		}
	}
	return string(out), nil
}

// ensureRootCredential makes sure the router is never left without a root
// credential and returns the password later deploy steps must reconnect with.
//
// Modes, in order:
//  1. a password was supplied -> set it and verify it took;
//  2. the router already has a real hash -> leave it alone (say so — this is
//     not the silent skip);
//  3. the router's root password is locked ('!'/'*') -> leave it alone (a
//     locked account is not credential-less, and re-enabling password login
//     on a deliberately locked router would be a regression);
//  4. the router has NO credential (the fresh-deploy state) -> generate one,
//     set it, verify it, and show it once;
//  5. the state cannot be read -> fail closed (never assume "fine").
//
// Returns ok=false after calling jobFail when the deploy must stop.
func ensureRootCredential(job *Job, run routerRun, supplied string) (string, bool) {
	state := parseRootHashState(run(rootHashProbeCmd))

	if supplied != "" {
		return supplied, applyRootPassword(job, run, supplied, "supplied")
	}

	switch state {
	case rootHashSet:
		job.addLog("Router root password already set — left unchanged (none supplied)")
		job.setStep(4, "done", "already set (unchanged)")
		return "", true
	case rootHashLocked:
		job.addLog("Router root password is LOCKED ('!'/'*' in /etc/shadow) — left unchanged.")
		job.addLog("Set one from LuCI or run `passwd root` on the router if you need the :8090 admin board.")
		job.setStep(4, "done", "locked (unchanged)")
		return "", true
	case rootHashEmpty:
		pw, err := generateRootPassword()
		if err != nil {
			jobFail(job, 4, "cannot generate a root credential",
				"The router has NO root password and none was supplied, and a credential could not be generated: "+err.Error())
			return "", false
		}
		if !applyRootPassword(job, run, pw, "generated") {
			return "", false
		}
		job.setGeneratedPassword(pw)
		return pw, true
	default:
		jobFail(job, 4, "cannot read the router's root password state",
			"Could not determine whether root has a usable password (/etc/shadow unreadable: no awk, no shadow file, or a dead SSH session). "+
				"Re-run the deploy and supply the router's root password explicitly.")
		return "", false
	}
}

// applyRootPassword sets password on the router and PROVES it took by
// re-reading /etc/shadow. A passwd that "looks" successful but leaves the hash
// empty (the exact failure this path exists for) fails the deploy.
func applyRootPassword(job *Job, run routerRun, password, origin string) bool {
	out := run(passwdCommand(password))
	if state := parseRootHashState(run(rootHashProbeCmd)); state != rootHashSet {
		jobFail(job, 4, "root password not established",
			"The router still has no usable root password after setting one ("+origin+"). "+
				"The router would be left with unauthenticated root administration; aborting. passwd output: "+truncate(out, 200))
		return false
	}
	job.addLog("Root password set (" + origin + ")")
	job.setStep(4, "done", "password set ("+origin+")")
	return true
}

// ─── Mint configuration ──────────────────────────────────────────

// mintCfg mirrors one entry of the router's accepted_mints config.
type mintCfg struct {
	URL                     string `json:"url"`
	MinBalance              int    `json:"min_balance"`
	BalanceTolerancePercent int    `json:"balance_tolerance_percent"`
	PayoutIntervalSeconds   int    `json:"payout_interval_seconds"`
	MinPayoutAmount         int    `json:"min_payout_amount"`
	PricePerStep            int    `json:"price_per_step"`
	PriceUnit               string `json:"price_unit"`
	MinPurchaseSteps        int    `json:"min_purchase_steps"`
}

// prodMintJSONTemplate is the 7 production mints every real deployment gets.
const prodMintJSONTemplate = `{"url":"https://mint.coinos.io","min_balance":64,"balance_tolerance_percent":10,"payout_interval_seconds":60,"min_payout_amount":128,"price_per_step":1,"price_unit":"sats","min_purchase_steps":0},
    {"url":"https://mint.minibits.cash/Bitcoin","min_balance":64,"balance_tolerance_percent":10,"payout_interval_seconds":60,"min_payout_amount":128,"price_per_step":1,"price_unit":"sats","min_purchase_steps":0},
    {"url":"https://mint.lnserver.com","min_balance":64,"balance_tolerance_percent":10,"payout_interval_seconds":60,"min_payout_amount":128,"price_per_step":1,"price_unit":"sats","min_purchase_steps":0},
    {"url":"https://mint.macadamia.cash","min_balance":64,"balance_tolerance_percent":10,"payout_interval_seconds":60,"min_payout_amount":128,"price_per_step":1,"price_unit":"sats","min_purchase_steps":0},
    {"url":"https://mint.westernbtc.com","min_balance":64,"balance_tolerance_percent":10,"payout_interval_seconds":60,"min_payout_amount":128,"price_per_step":1,"price_unit":"sats","min_purchase_steps":0},
    {"url":"https://kashu.me","min_balance":64,"balance_tolerance_percent":10,"payout_interval_seconds":60,"min_payout_amount":128,"price_per_step":1,"price_unit":"sats","min_purchase_steps":0},
    {"url":"https://mint.cubabitcoin.org","min_balance":64,"balance_tolerance_percent":10,"payout_interval_seconds":60,"min_payout_amount":128,"price_per_step":1,"price_unit":"sats","min_purchase_steps":0}`

// testMintJSONTemplate is the 2 testnut zero-fee test mints. They fake
// Lightning payments and exist ONLY for E2E purchase testing — never for
// real customer deployments.
const testMintJSONTemplate = `{"url":"https://nofee.testnut.cashu.space","min_balance":0,"balance_tolerance_percent":0,"payout_interval_seconds":999999,"min_payout_amount":999999,"price_per_step":1,"price_unit":"sats","min_purchase_steps":0},
    {"url":"https://testnut.cashu.space","min_balance":0,"balance_tolerance_percent":0,"payout_interval_seconds":999999,"min_payout_amount":999999,"price_per_step":1,"price_unit":"sats","min_purchase_steps":0}`

// defaultMintsJSON returns the accepted_mints JSON array the wizard writes
// to the router's /etc/tollgate/config.json. ALWAYS the 7 production mints;
// the 2 testnut test mints (fake Lightning, E2E purchase testing only) are
// appended ONLY when includeTestnut is true — test mints are strictly
// opt-in, real customer deployments never get them by default.
func defaultMintsJSON(includeTestnut bool) string {
	if includeTestnut {
		return "[\n    " + prodMintJSONTemplate + ",\n    " + testMintJSONTemplate + "\n  ]"
	}
	return "[\n    " + prodMintJSONTemplate + "\n  ]"
}

// shellQuoteSingle wraps s in single quotes for safe interpolation into a
// router-side shell command, escaping embedded single quotes so a mint URL
// containing a quote/space cannot break out of the quoted token.
func shellQuoteSingle(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// configJqFilter returns the jq filter used by deploy step 7 to merge
// margin, profit_share and mints into /etc/tollgate/config.json.
//
// Semantics:
//   - margin/profit_share factors are overwritten from the deploy payload;
//   - the operator's chosen mint ($mu) is APPENDED if non-empty and not
//     already present (it never replaces the default list);
//   - every default mint ($dm) not already in accepted_mints is APPENDED
//     (idempotent by URL — re-running a deploy adds nothing new);
//   - a config.json without accepted_mints is handled via (// []).
//
// Fixes vs the previous filter, which NEVER wrote margin/profit_share/mints:
//  1. the dedup check inside `($dm | map(...))` referenced .accepted_mints
//     while `.` was bound to each $dm element (null) → "Cannot iterate over
//     null" on every router; the fix snapshots the CURRENT url list into
//     $have BEFORE appending defaults, and every access to .accepted_mints
//     is null-coalesced with `// []`;
//  2. `($mu != "" and $idx | not)` mis-parsed as `(cond) | not`, inverting
//     the condition so an EMPTY mint appended a {"url":""} entry; the fix
//     parenthesizes `(($mu != "") and (index == null))`;
//  3. the operator mint now crosses the shell SINGLE-QUOTED (see
//     shellQuoteSingle) — unquoted, an empty --arg value made jq consume the
//     filter itself as $mu, and a mint with a space/shell char corrupted the
//     whole command.
//
// This filter is exercised end-to-end against local jq by
// TestMintConfigJqMerge (merge, dedup, idempotency, // [] fallback).
func configJqFilter() string {
	return ".margin=$m | " +
		"(.profit_share[] | select(.identity == \"owner\") | .factor) = $of | " +
		"(.profit_share[] | select(.identity == \"developer\") | .factor) = $df | " +
		// Add operator's chosen mint if non-empty and not already present.
		".accepted_mints = (if (($mu != \"\") and (((.accepted_mints // []) | map(.url) | index($mu)) == null)) then " +
		"(.accepted_mints // []) + [{\"url\":$mu,\"min_balance\":64,\"balance_tolerance_percent\":10,\"payout_interval_seconds\":60,\"min_payout_amount\":128,\"price_per_step\":1,\"price_unit\":\"sats\",\"min_purchase_steps\":0}] " +
		"else (.accepted_mints // []) end) | " +
		// Add any default mints that aren't already present (idempotent by
		// URL; map+index instead of unique_by for jq <1.7 on OpenWrt).
		// $have = the CURRENT url list (after the $mu merge above), so a
		// custom mint equal to a default URL is not appended twice.
		"((.accepted_mints // []) | map(.url)) as $have | " +
		".accepted_mints = ((.accepted_mints // []) + ($dm | map(.url as $u | select(($have | index($u)) == null))))"
}

// configJqCmd builds the full router-side command that rewrites
// /etc/tollgate/config.json. Host-computed values ($m/$of/$df/$dm) cross as
// jq --argjson; the operator mint crosses as a single-quoted jq --arg.
func configJqCmd(margin int, ownerFactor, devFactor, mint string, includeTestnut bool) string {
	return "jq --argjson m " + strconv.Itoa(margin) + " " +
		"--argjson of " + ownerFactor + " " +
		"--argjson df " + devFactor + " " +
		"--argjson dm '" + defaultMintsJSON(includeTestnut) + "' " +
		"--arg mu " + shellQuoteSingle(mint) + " " +
		"'" + configJqFilter() + "' " +
		"/etc/tollgate/config.json > /tmp/cfg.tmp 2>&1 && " +
		"mv /tmp/cfg.tmp /etc/tollgate/config.json && echo 'config updated' || echo 'no config'"
}

// brandingCommands returns the router-side commands that brand a deployed
// router: hostname, captive SSID, DNS/domain records and the NoDogSplash
// pre-auth allow list. Lifted verbatim out of runDeployment so a test can run
// the SHIPPED commands against a stub `uci` and assert what a paying-nothing
// captive client can reach (branding_test.go).
//
// NoDogSplash's users_to_router IS the pre-authentication allow list: every
// entry in it is reachable by a client that has paid nothing, on a network
// that is open by design. The customer-journey ports (:80 captive check,
// :2050/:2051 portal, :2121 backend, :8080 LuCI) belong there. The OWNER-facing
// admin board on :8090/:8443 does NOT — it is a root-capable login served over
// plain HTTP (rpcd ACL: file exec, system.password_set, wallet_drain_cashu),
// and the module's 99-tollgate-setup strips it (PR #546). This function used
// to ADD :8090 to the list right after that script removed it, re-arming the
// exposure on every fresh deploy; it now REMOVES the entry (and the :8443
// sibling) from an already-deployed router instead.
func brandingCommands(nodeName, routerIP string) []string {
	// Deduplicate /etc/hosts entries, then write fresh ones
	hostsCmd := "sed -i '/tollgate\\.lan/d; /tollgate\\.local/d' /etc/hosts && " +
		"echo '" + routerIP + " tollgate.lan tollgate.local' >> /etc/hosts"
	return []string{
		// Hostname
		"uci -q set system.@system[0].hostname='" + nodeName + "'",
		// WiFi SSID — only on default_radio* (public captive portal WiFi)
		// Skip private_radio* (admin LAN) and *_uplink (WAN repeater)
		"for i in $(uci -q show wireless 2>/dev/null | grep 'default_radio.*=wifi-iface' | awk -F. '{print $2}' | awk -F= '{print $1}'); do uci -q set wireless.$i.ssid='" + nodeName + "'; done",
		// DNS: deduplicated /etc/hosts entries
		hostsCmd,
		// Ensure dnsmasq serves .lan domain
		"uci -q set dhcp.@dnsmasq[0].domain='lan'",
		"uci -q set dhcp.@dnsmasq[0].local='/lan/'",
		// dnsmasq address records (belt-and-suspenders with /etc/hosts).
		// Purge ALL prior /tollgate.lan/* entries first: an older deploy may
		// have written a corrupt value (e.g. a trailing /24), and a plain
		// del_list of the new value would leave it in place and keep dnsmasq
		// crash-looping.
		"for a in $(uci -q get dhcp.@dnsmasq[0].address); do case \"$a\" in /tollgate.lan*) uci -q del_list dhcp.@dnsmasq[0].address=\"$a\";; esac; done; uci -q add_list dhcp.@dnsmasq[0].address='/tollgate.lan/" + routerIP + "'",
		// DHCP: push router as DNS server to all DHCP clients (option 6)
		// This is what makes .lan domains resolve on connected devices.
		// Same purge-first rationale as the address list above.
		"for a in $(uci -q get dhcp.lan.dhcp_option); do case \"$a\" in 6,*) uci -q del_list dhcp.lan.dhcp_option=\"$a\";; esac; done; uci -q add_list dhcp.lan.dhcp_option='6," + routerIP + "'",
		// dnsmasq: expand /etc/hosts entries with domain suffix
		"uci -q set dhcp.@dnsmasq[0].expandhosts='1'",
		"uci -q set dhcp.@dnsmasq[0].readethers='1'",
		// network: set domain on lan interface
		"uci -q set network.lan.domain='lan'",
		// NoDogSplash config
		"uci -q set nodogsplash.@nodogsplash[0].gatewayname='" + nodeName + "'",
		// Rebrand gateway domain to tollgate.lan so the captive portal serves
		// on tollgate.lan (DNS already resolves it).
		"uci -q set nodogsplash.@nodogsplash[0].gatewaydomainname='tollgate.lan'",
		"uci -q set nodogsplash.@nodogsplash[0].enabled='1'",
		"uci -q set nodogsplash.@nodogsplash[0].clientid='mac'",
		// Customer-journey ports: the captive check, the portal, the backend
		// API and LuCI. These ARE meant to be reachable pre-auth.
		"uci -q del_list nodogsplash.@nodogsplash[0].users_to_router='allow tcp port 2121' 2>/dev/null; uci -q add_list nodogsplash.@nodogsplash[0].users_to_router='allow tcp port 2121'",
		"uci -q del_list nodogsplash.@nodogsplash[0].users_to_router='allow tcp port 2050' 2>/dev/null; uci -q add_list nodogsplash.@nodogsplash[0].users_to_router='allow tcp port 2050'",
		"uci -q del_list nodogsplash.@nodogsplash[0].users_to_router='allow tcp port 2051' 2>/dev/null; uci -q add_list nodogsplash.@nodogsplash[0].users_to_router='allow tcp port 2051'",
		"uci -q del_list nodogsplash.@nodogsplash[0].users_to_router='allow tcp port 80' 2>/dev/null; uci -q add_list nodogsplash.@nodogsplash[0].users_to_router='allow tcp port 80'",
		"uci -q del_list nodogsplash.@nodogsplash[0].users_to_router='allow tcp port 8080' 2>/dev/null; uci -q add_list nodogsplash.@nodogsplash[0].users_to_router='allow tcp port 8080'",
		// Owner-facing admin board: REMOVE, never grant. Plain HTTP, root-capable
		// login, and the installer runs AFTER the package's uci-defaults — so an
		// add_list here silently undid the module fix (PR #546) on every deploy.
		// del_list (not "just stop adding") because a deployed router already
		// carries the entry.
		"uci -q del_list nodogsplash.@nodogsplash[0].users_to_router='allow tcp port 8090' 2>/dev/null; uci -q del_list nodogsplash.@nodogsplash[0].users_to_router='allow tcp port 8443' 2>/dev/null",
		// Commit all
		"uci commit system",
		"uci commit wireless",
		"uci commit dhcp",
		"uci commit network",
		"uci commit nodogsplash",
		// Enable radios (OpenWrt ships with wifi disabled by default)
		"uci -q set wireless.radio0.disabled='0' 2>/dev/null; true",
		"uci -q set wireless.radio1.disabled='0' 2>/dev/null; true",
		"uci commit wireless",
		"/etc/init.d/nodogsplash enable",
		"/etc/init.d/nodogsplash restart 2>/dev/null || true",
		"/etc/init.d/dnsmasq restart 2>/dev/null || true",
		// Apply wireless config (wifi reload applies UCI, wifi starts if not running)
		"wifi reload 2>/dev/null || wifi 2>/dev/null || true",
		"echo 'branded'",
	}
}

// deploySteps returns the ordered deployment step definitions.
func deploySteps() []Step {
	return []Step{
		{Name: "verify", Desc: "Verifying SSH access to router...", Status: "pending"},
		{Name: "stage", Desc: "Pre-downloading packages and firmware...", Status: "pending"},
		{Name: "flash", Desc: "Flashing OpenWrt on stock GL.iNet...", Status: "pending"},
		{Name: "firmware", Desc: "Checking firmware version...", Status: "pending"},
		{Name: "password", Desc: "Setting root password...", Status: "pending"},
		{Name: "upstream", Desc: "Configuring upstream connection...", Status: "pending"},
		{Name: "install", Desc: "Installing tollgate-wrt package + patching backend...", Status: "pending"},
		{Name: "brand", Desc: "Branding captive portal as TollGate...", Status: "pending"},
		{Name: "portal", Desc: "Deploying TollGate captive portal...", Status: "pending"},
		{Name: "lnurl", Desc: "Configuring Lightning address...", Status: "pending"},
		{Name: "services", Desc: "Restarting services...", Status: "pending"},
		{Name: "health", Desc: "Running health check...", Status: "pending"},
	}
}

// runDeployment executes the full deployment sequence.
func runDeployment(job *Job, req deployRequest) {
	client := sshConnect(req.IP, req.Password)
	if client == nil && req.Password != "" {
		// If password auth failed, try key auth
		client = sshConnect(req.IP, "")
	}
	if client == nil {
		job.mu.Lock()
		job.Status = "failed"
		job.Error = "Cannot connect to router via SSH"
		job.mu.Unlock()
		return
	}
	// Closure (not `defer client.Close()`): client can be re-assigned when
	// the STA step re-establishes the session after a wifi reload — the
	// deferred call must close whichever client is live at the end.
	defer func() { client.Close() }()

	// Step 0: Verify SSH
	job.setStep(0, "running", "")
	fwOut := sshRun(client, "cat /etc/openwrt_release 2>/dev/null || cat /etc/openwrt_version 2>/dev/null || echo 'not openwrt'")
	fwOut = strings.TrimSpace(fwOut)
	isStockGL := false
	var glModel string
	if fwOut == "not openwrt" || fwOut == "" {
		// Not OpenWrt — check for stock GL.iNet firmware. If present, we
		// proceed to the flash step (step 2) instead of failing.
		glOut := sshRun(client, "cat /etc/gl-inet-release 2>/dev/null || echo ''")
		glOut = strings.TrimSpace(glOut)
		if glOut != "" {
			isStockGL = true
			glModel, _ = parseGLInetRelease(glOut)
			glModel = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(glModel), " ", "-"))
			if glModel == "" {
				glModel = strings.TrimSpace(sshRun(client, "cat /tmp/sysinfo/board_name 2>/dev/null"))
			}
			job.addLog("Detected stock GL.iNet firmware (model: " + glModel + ")")
			job.setStep(0, "done", "GL.iNet "+glModel)
		} else {
			job.setStep(0, "failed", "Router is not running OpenWrt")
			job.mu.Lock()
			job.Status = "failed"
			job.Error = "Router is not running OpenWrt firmware"
			job.mu.Unlock()
			return
		}
	} else {
		job.addLog("SSH OK. Firmware: " + truncate(fwOut, 100))
		job.setStep(0, "done", truncate(fwOut, 100))
		// On OpenWrt the model is NOT read above (that branch is stock-GL
		// only), yet a forced reflash (req.ForceFlash) still needs it to pick
		// the image. board_name is "glinet,gl-mt6000"; glModelMap keys are the
		// bare lowercase model ("gl-mt6000").
		glModel = glModelFromBoard(sshRun(client, "cat /tmp/sysinfo/board_name 2>/dev/null"))
	}
	time.Sleep(500 * time.Millisecond)

	// Heal dnsmasq/hosts corruption left by an earlier installer run BEFORE any
	// download: older builds wrote the LAN IP with its /24 suffix
	// ("address=/tollgate.lan/192.168.1.1/24"), dnsmasq rejects that and
	// crash-loops, and the router then pings but cannot resolve any name — which
	// breaks the package fetch and the health check. Safe no-op on a stock-GL
	// router that is about to be flashed.
	if !isStockGL {
		if lanIP := repairLanDNS(client); lanIP != "" {
			sshRun(client, "/etc/init.d/dnsmasq restart 2>/dev/null; true")
			job.addLog("DNS entries normalized for " + lanIP)
		}
	}

	// Step 1: Stage — pre-download deploy assets (optional; req.PreStage).
	// If the operator asked for it, pre-download every asset the deploy will
	// need into the Job's stageCache NOW, while the laptop's current
	// internet path is still up. This matters when the laptop's only
	// internet is via the router being flashed or reconfigured (STA mode):
	// the flash/install steps later consume the staged bytes instead of
	// fetching live. Runs immediately after verify (glModel/isStockGL from
	// the SSH probe are required to pick the right image/package URLs) and
	// before flash. Failures are non-fatal — the existing live-fetch →
	// router-wget → feed fallbacks remain on a cache miss.
	job.setStep(1, "running", "")
	if req.PreStage {
		runPreStage(job, client, isStockGL, glModel)
		job.setStep(1, "done", "deploy assets pre-downloaded")
	} else {
		job.setStep(1, "done", "skipped (no pre-stage requested)")
	}
	time.Sleep(500 * time.Millisecond)

	// Step 2: Flash OpenWrt on stock GL.iNet — or a forced reflash on an
	// already-OpenWrt router (skipped when neither applies).
	job.setStep(2, "running", "")
	if isStockGL || req.ForceFlash {
		if glModel == "" {
			jobFail(job, 2, "Cannot determine GL.iNet model", "Force-flash requested but the router's GL.iNet board could not be determined (no /etc/gl-inet-release and no glinet board_name). Refusing to guess an image — flash manually.")
			return
		}

		img, ok := glModelMap[glModel]
		if !ok {
			jobFail(job, 2, "Unknown GL.iNet model: "+glModel, "Unknown GL.iNet model "+glModel+". Please update the model table in images.go or flash manually.")
			return
		}

		if isStockGL {
			job.addLog("Flashing OpenWrt on GL.iNet " + glModel + "...")
		} else {
			job.addLog(fmt.Sprintf("Force-flash requested: reflashing %s to OpenWrt %s (sysupgrade -n — config WIPED)", glModel, img.Version))
		}

		// Obtain the image on the laptop (not the router — limited storage).
		// USE the staging cache first: when the PreStage step pre-downloaded
		// this exact image URL, flashImageBytes serves the staged bytes with
		// zero network (the offline case staging exists for). On a cache miss
		// it downloads live via downloadWithRetry (3 attempts, exponential
		// backoff) — transient network errors and 5xx are retried; a definitive
		// 4xx (bad image pin) fails immediately. Raw http.Get is NOT used (no
		// timeout/redirect/size handling).
		imageURL := img.URL()
		imageData, err := flashImageBytes(job, imageURL)
		if err != nil {
			jobFail(job, 2, "Download failed after 3 attempts: "+err.Error(), "Failed to download OpenWrt image after 3 attempts: "+err.Error()+"\nURL: "+imageURL)
			return
		}

		// Push the image to the router via the SSH stdin pipe.
		// USE sshUploadPipe (ssh.go:74) — same pattern as the package-install
		// step (deploy.go:157). A function called "sshWrite" does NOT exist.
		pushOut := sshUploadPipe(client, imageData, "cat > /tmp/openwrt-sysupgrade.bin && echo PUSH_OK")
		if !strings.Contains(pushOut, "PUSH_OK") {
			// Push failed — check router storage space so the operator knows
			// whether it's a full /tmp (common on small-flash GL.iNet boards)
			// vs a network/SSH problem.
			dfOut := sshRun(client, "df -h /tmp 2>&1")
			jobFail(job, 2, "Image push failed", "Failed to push OpenWrt image to router: "+truncate(pushOut, 80)+"\nRouter /tmp storage:\n"+truncate(dfOut, 200)+"\nIf /tmp is full, free space (remove old images) and retry, or flash manually via GL.iNet recovery mode.")
			return
		}
		job.addLog("Image pushed to router")

		// Run sysupgrade. The router will go down and reboot onto OpenWrt.
		upgradeOut := sshRun(client, "sysupgrade -n /tmp/openwrt-sysupgrade.bin 2>&1")
		job.addLog("sysupgrade: " + truncate(upgradeOut, 200))
		// If sysupgrade returned a recognizable error (image rejected, no
		// space, missing binary), surface it immediately instead of waiting
		// 3 minutes for a router that never reboots.
		if strings.Contains(upgradeOut, "failed") || strings.Contains(upgradeOut, "error") ||
			strings.Contains(upgradeOut, "not found") || strings.Contains(upgradeOut, "invalid") {
			jobFail(job, 2, "sysupgrade failed", parseSysupgradeError(upgradeOut))
			return
		}

		// Wait for the router to reboot. Stock GL.iNet uses 192.168.8.1,
		// OpenWrt defaults to 192.168.1.1. Poll with tcpProbe + reconnectSSH
		// (which retries AND falls back to empty-password auth for the fresh
		// OpenWrt root) until the router comes back on OpenWrt, up to 3min.
		job.addLog("Router rebooting. Waiting for it to come back (up to 3 min)...")
		newIP, newClient, err := waitForRouterAfterFlash(req.IP, req.Password, 3*time.Minute)
		if err != nil {
			// Last-resort fallback: the router may have come back on an
			// unexpected IP (LAN bridge changed the subnet). Scan the /24
			// subnet for any host presenting the OpenWrt banner.
			job.addLog("Router not found on expected IPs. Scanning LAN subnet for OpenWrt...")
			scannedIP := scanSubnetForOpenWrt(req.IP, req.Password, 60*time.Second)
			if scannedIP != "" {
				newClient = reconnectSSH(scannedIP, req.Password, 3, 2*time.Second)
				if newClient != nil {
					newIP = scannedIP
					err = nil
				}
			}
		}
		if err != nil {
			jobFail(job, 2, "Router unreachable after flash", "Router did not come back after flash. Last known IP: "+req.IP+". See manual recovery docs (GL.iNet recovery mode).")
			return
		}
		client.Close()
		client = newClient
		job.addLog("Reconnected to router at " + newIP)
		job.setStep(2, "done", "OpenWrt "+img.Version+" flashed on "+glModel)
	} else {
		job.setStep(2, "done", "skipped (already OpenWrt)")
	}
	time.Sleep(500 * time.Millisecond)

	// Step 3: Check firmware
	job.setStep(3, "running", "")
	versionLine := ""
	for _, line := range strings.Split(fwOut, "\n") {
		if strings.Contains(line, "DISTRIB_DESCRIPTION") {
			parts := strings.SplitN(line, "'", 2)
			if len(parts) > 1 {
				versionLine = strings.Trim(parts[1], "'")
			}
		}
	}
	job.addLog("Firmware: " + versionLine)
	job.setStep(3, "done", versionLine)
	time.Sleep(500 * time.Millisecond)

	// Step 4: root credential.
	//
	// A router left with an EMPTY root password hash is not "open by default",
	// it is UNauthenticated root: rpcd's rpc_login_test_password()
	// (rpcd/session.c) short-circuits to true when the shadow hash is empty,
	// so session.login accepts ANY password — including "" — and dropbear
	// accepts an empty-password login too. The :8090 admin board sits behind
	// exactly that session and its ACL grants file exec / password_set /
	// wallet_drain_cashu, so a fresh deploy that skipped this step handed out
	// root administration over plain HTTP.
	//
	// The operator's password stays OPTIONAL (a fresh OpenWrt has none to
	// type), but the deployed router must never end up credential-less: when
	// the router has no usable root password and none was supplied, GENERATE
	// one, set it, and show it once. If a credential cannot be established,
	// FAIL the deploy — never report success on a password-less router.
	job.setStep(4, "running", "")
	effectivePassword, credentialOK := ensureRootCredential(job, func(cmd string) string {
		return sshRun(client, cmd)
	}, req.Password)
	if !credentialOK {
		return
	}
	if effectivePassword != "" {
		// Later steps (STA reconnect, fsck-style re-auth) must use the live
		// credential, not the possibly-empty value from the request.
		req.Password = effectivePassword
	}
	time.Sleep(500 * time.Millisecond)

	// Step 5: Configure upstream (WiFi STA if requested)
	job.setStep(5, "running", "")
	if req.Mode == "sta" && req.SSID != "" {
		if !configureSTA(job, &client, req.IP, req.Password, req.SSID, req.WifiPass, req.Band) {
			return
		}
	} else {
		job.addLog("Using WAN upstream (default)")
		// A pre-existing local/upstream subnet overlap breaks name resolution
		// (the router answers for the upstream gateway's own IP), which also
		// breaks the package download — fix it before installing.
		client = fixSubnetCollisions(job, client, req.IP, req.Password)
		job.setStep(5, "done", "WAN mode (default)")
	}
	time.Sleep(500 * time.Millisecond)

	// Step 6: Install tollgate package from GitHub releases
	// OpenWrt 25+ uses apk; OpenWrt 24.x uses opkg. Detect at runtime.
	job.setStep(6, "running", "")
	pkgMgr := strings.TrimSpace(sshRun(client, "command -v apk >/dev/null 2>&1 && echo apk || echo opkg"))

	// Auto-detect the router's CPU architecture and select the matching
	// tollgate-wrt asset per-arch. Previously the download URL was hardcoded
	// to aarch64_cortex-a53 — on any other router the wrong-arch binary simply
	// won't execute, which is the exact silent failure this removes.
	routerArch := detectArch(client)
	job.addLog("Detected router CPU arch: " + routerArch)
	if routerArch == "" {
		// FAIL LOUDLY on an undetectable arch. Never silently default to
		// aarch64_cortex-a53 — that is the bug being fixed.
		job.addLog("Could not determine router CPU architecture")
		jobFail(job, 6,
			"Could not determine router CPU architecture",
			"Could not determine router CPU architecture")
		return
	}

	// Select appropriate package URL based on package manager + arch.
	// OpenWrt 25.12+ uses APK and cannot install legacy .ipk packages.
	_, pkgExtension, ok := selectPkgURL(routerArch, pkgMgr)
	if !ok {
		// Undetectable arch — fail, never substitute aarch64.
		jobFail(job, 6, "Unsupported CPU arch "+routerArch,
			"Unsupported CPU arch "+routerArch)
		return
	}
	// Candidate download URLs: the generic feed URL first, then the GitHub
	// release fallback (aarch64 only) if one exists. The first that yields
	// bytes wins.
	//
	// The fallback asset is pinned to a DIFFERENT, OLDER release than the
	// requested feed tag, so it is only offered when the operator has explicitly
	// opted in (--allow-fallback / TOLLGATE_ALLOW_GITHUB_FALLBACK=1). Without
	// the opt-in it is removed from the candidate list AND the refusal is held
	// for the fail-loudly check below: a feed outage must not turn into a silent
	// downgrade reported as success.
	pkgCandidates := pkgCandidateURLs(routerArch, pkgExtension)
	var fallbackRefusal error
	if len(pkgCandidates) > 1 {
		fb, err := githubFallbackSelection(routerArch, pkgExtension, githubFallbackAllowed())
		if err != nil {
			fallbackRefusal = err
			pkgCandidates = pkgCandidates[:1]
			job.addLog("GitHub fallback NOT used: " + err.Error())
		} else {
			job.addLog("GitHub fallback allowed by explicit opt-in (installs " +
				githubFallbackPkgVersion(routerArch, pkgExtension) + ", not " + feedPkgVersion() + "): " + fb)
		}
	}
	if pkgExtension == ".apk" {
		job.addLog("OpenWrt 25+ detected with APK package manager (arch " + routerArch + ")")
	} else {
		job.addLog("OpenWrt <=24.x detected with OPKG package manager (arch " + routerArch + ")")
	}

	// MT3000-class routers have no RTC — after a cold boot the clock is far
	// in the past and router-side TLS to github.com fails cert validation.
	// Sync from the laptop clock before any router-side download attempt.
	sshRun(client, "date -s @"+strconv.FormatInt(time.Now().Unix(), 10)+" >/dev/null 2>&1; true")

	// PRIMARY: prefer the tollgate-wrt bytes the PreStage step cached for this
	// exact URL — the point of staging is that the actual install performs zero
	// network fetches. On a cache miss, download the package on the LAPTOP and
	// push it over SSH stdin. Pushing eliminates the router's DNS/TLS stack
	// from the critical path — a freshly STA-connected router often has no
	// working DNS yet. A live-fetch failure falls through to the router-side
	// wget (and then feed) paths below.
	//
	// pkgCandidates holds the ordered URLs to try (feed first, then the GitHub
	// release fallback for aarch64). The first that yields bytes wins.
	pkgOnRouter := false
	var pkgData []byte
	var pkgFromCache bool
	var pkgErr error
	// pkgLaptopURL is the candidate whose bytes the laptop fetched. It is only
	// promoted to pkgSourceURL once those bytes are CONFIRMED on the router, so
	// a download/push that never landed is never reported as the package's
	// source. Provenance is reported from pkgSourceURL only.
	pkgLaptopURL := ""
	for _, candURL := range pkgCandidates {
		pkgData, pkgFromCache, pkgErr = stagedOrLiveBytes(job, "tollgate-wrt "+pkgExtension, candURL)
		if pkgErr == nil && len(pkgData) > 0 {
			pkgLaptopURL = candURL
			break
		}
		if pkgErr != nil {
			job.addLog("Laptop download failed for " + candURL + ": " + truncate(pkgErr.Error(), 80))
		}
	}
	pkgSourceURL := ""
	if pkgErr == nil && len(pkgData) > 0 {
		push := sshUploadPipe(client, pkgData, "cat > /tmp/tollgate-wrt"+pkgExtension+" && echo PUSH_OK")
		if strings.Contains(push, "PUSH_OK") {
			pkgOnRouter = true
			pkgSourceURL = pkgLaptopURL
			if pkgFromCache {
				job.addLog(fmt.Sprintf("Staged tollgate-wrt %s used from cache (%d KB), pushed to router via SSH", pkgExtension, len(pkgData)/1024))
			} else {
				job.addLog(fmt.Sprintf("Package downloaded on laptop (%d KB), pushed to router via SSH", len(pkgData)/1024))
			}
		} else {
			job.addLog("SSH push failed: " + truncate(push, 80))
		}
	} else if pkgErr != nil {
		job.addLog("Laptop download failed: " + truncate(pkgErr.Error(), 80) + " — falling back to router-side wget")
	}

	// FALLBACK: router-side wget, with a real DNS probe and wget's stderr
	// logged so the job log shows WHY it fails (DNS vs TLS/clock vs routing)
	// instead of a silent empty file.
	if !pkgOnRouter {
		probe := sshRun(client, "nslookup github.com 2>&1 | tail -n2")
		job.addLog("Router DNS probe: " + truncate(probe, 60))
		for _, candURL := range pkgCandidates {
			wgetOut := sshRun(client, "wget -O /tmp/tollgate-wrt"+pkgExtension+" '"+candURL+"' 2>&1; [ -s /tmp/tollgate-wrt"+pkgExtension+" ] && echo WGET_OK || echo WGET_FAIL")
			job.addLog("wget: " + truncate(wgetOut, 120))
			if strings.Contains(wgetOut, "WGET_OK") {
				pkgOnRouter = true
				pkgSourceURL = candURL
				break
			}
		}
	}

	// PROVENANCE: state which source supplied the package — or that none did.
	// The point of installing the FEED build is that an installer run also
	// tests tollgate-module-basic-go + FreedomTechFeed/packages; that is only
	// provable if the source is reported instead of inferred from a URL in the
	// log. Never silently substituted: a GitHub-release install says so.
	if pkgOnRouter {
		job.addLog("tollgate-wrt source: " + pkgSourceLabel(routerArch, pkgExtension, pkgSourceURL) + " — " + pkgSourceURL)
	} else {
		job.addLog("tollgate-wrt source: none — no candidate URL supplied the package (feed and GitHub fallback both failed)")
	}

	// FAIL LOUDLY. The requested release could not be downloaded and the only
	// other candidate is a known OLDER package: stop here and name both versions
	// instead of substituting it, force-downgrading the router, and rendering the
	// step green. (Before this, the v0.5.0 GitHub asset was tried silently and
	// the post-install assertion could not fire for it — C2-I-02.)
	if !pkgOnRouter && fallbackRefusal != nil {
		job.addLog("ERROR: " + fallbackRefusal.Error())
		jobFail(job, 6,
			"requested release "+feedReleaseTag+" unavailable — refusing to install an older package",
			fallbackRefusal.Error())
		return
	}

	installedOK := false
	// installStatus is the status step 6 renders with. "warn" when the package
	// that landed is NOT the requested release (an explicitly opted-in fallback,
	// or an install from the router's own feeds), so the UI cannot show it as an
	// unqualified success. Declared before the download wiring so the deferred
	// refusal path above cannot leave it unset.
	installStatus := "done"
	if pkgOnRouter {
		job.addLog("Installing package via " + pkgMgr + "...")
		sshRun(client, "rm -f /var/lock/opkg.lock 2>/dev/null")
		// Install nodogsplash + jq prerequisites BEFORE the tollgate-wrt .ipk so
		// opkg's dependency resolver doesn't fail on a fresh OpenWrt that
		// doesn't have them pre-installed. Try opkg feed first; if that fails
		// (nodogsplash not in default feeds on fresh 24.10.4), download the
		// .ipk files from the OpenWrt package repo on the laptop and push them
		// to the router via SSH — same pattern as the tollgate-wrt .ipk.
		if pkgMgr != "apk" {
			ndsUpdate := sshRun(client, "opkg update 2>&1")
			job.addLog("opkg update (prereq): " + truncate(ndsUpdate, 60))
			ndsInstall := sshRun(client, "opkg install nodogsplash jq 2>&1 | tail -5")
			if strings.Contains(ndsInstall, "installed") || strings.Contains(ndsInstall, "already") {
				job.addLog("nodogsplash+jq installed via opkg feed: " + truncate(ndsInstall, 80))
			} else {
				job.addLog("opkg feed install failed, downloading .ipk from OpenWrt repo...")
				// Download nodogsplash + jq .ipk from OpenWrt package repo
				// on laptop, push to router, install. nodogsplash is in the
				// routing/ subdirectory, jq is in packages/.
				baseURL := "https://downloads.openwrt.org/releases/24.10.4/packages/" + routerArch + "/"
				routingURL := baseURL + "routing/"
				packagesURL := baseURL + "packages/"
				ndsListHTML := string(httpGetFileOrEmpty(routingURL))
				jqListHTML := string(httpGetFileOrEmpty(packagesURL))
				ndsPkg := extractIPKFilename(ndsListHTML, "nodogsplash")
				jqPkg := extractIPKFilename(jqListHTML, "jq")
				if ndsPkg != "" {
					ndsData, ndsFromCache, ndsErr := stagedOrLiveBytes(job, "nodogsplash .ipk", routingURL+ndsPkg)
					if ndsErr == nil && len(ndsData) > 1000 {
						pushNds := sshUploadPipe(client, ndsData, "cat > /tmp/"+ndsPkg+" && echo NDS_PUSHED")
						if strings.Contains(pushNds, "NDS_PUSHED") {
							if ndsFromCache {
								job.addLog(fmt.Sprintf("nodogsplash .ipk used from staging cache (%d KB), pushed to router", len(ndsData)/1024))
							} else {
								job.addLog(fmt.Sprintf("nodogsplash .ipk downloaded (%d KB), pushed to router", len(ndsData)/1024))
							}
						}
					}
				}
				if jqPkg != "" {
					jqData, jqFromCache, jqErr := stagedOrLiveBytes(job, "jq .ipk", packagesURL+jqPkg)
					if jqErr == nil && len(jqData) > 1000 {
						pushJq := sshUploadPipe(client, jqData, "cat > /tmp/"+jqPkg+" && echo JQ_PUSHED")
						if strings.Contains(pushJq, "JQ_PUSHED") {
							if jqFromCache {
								job.addLog(fmt.Sprintf("jq .ipk used from staging cache (%d KB), pushed to router", len(jqData)/1024))
							} else {
								job.addLog(fmt.Sprintf("jq .ipk downloaded (%d KB), pushed to router", len(jqData)/1024))
							}
						}
					}
				}
				manualInstall := sshRun(client, "opkg install /tmp/nodogsplash_*.ipk /tmp/jq_*.ipk 2>&1 | tail -5")
				job.addLog("nodogsplash+jq manual install: " + truncate(manualInstall, 80))
			}
		}
		// opkg does lexical version compare — 'v0.5.0' > 'main.56...' so it refuses
		// to downgrade unless forced. --force-reinstall ensures the files land even
		// if opkg thinks the package is already present. Detect "Not downgrading"
		// in the output as a hard failure regardless of binary existence.
		installCmd := "opkg install --force-downgrade --force-reinstall --force-overwrite --force-depends /tmp/tollgate-wrt" + pkgExtension + " 2>&1 | tail -5"
		if pkgMgr == "apk" {
			// apk has no downgrade refusal, but --force-overwrite guards against
			// existing-file conflicts on reinstall.
			installCmd = "apk add --allow-untrusted --force-overwrite /tmp/tollgate-wrt" + pkgExtension + " 2>&1 | tail -5"
		}
		installOut := sshRun(client, installCmd)
		job.addLog("Package installed (" + pkgMgr + "): " + truncate(installOut, 100))
		// If opkg refuses to downgrade, the OLD binary stays and the new config
		// will crash against it — treat as a hard failure even if the binary exists.
		if strings.Contains(installOut, "Not downgrading") {
			job.addLog("ERROR: opkg refused to downgrade the package (old version kept)")
			job.setStep(6, "error", "opkg refused to downgrade tollgate-wrt")
			return
		}
		// apk prints failures to stdout; the pipeline above hides the exit code,
		// and a failed UPGRADE leaves the OLD binary in place — so the
		// binary-existence check below would pass while the router still runs
		// the previous package. Fail loudly on apk's error signatures.
		if pkgMgr == "apk" && apkInstallFailed(installOut) {
			job.addLog("ERROR: apk reported an installation failure: " + truncate(installOut, 200))
			jobFail(job, 6, "tollgate-wrt install failed (apk error)",
				"apk could not install/upgrade tollgate-wrt — the previous package (with its OLD captive portal) is still in place:\n"+truncate(installOut, 400))
			return
		}
		// Verify the binary actually exists (secondary check)
		verifyOut := sshRun(client, "ls /usr/bin/tollgate-wrt 2>/dev/null || ls /usr/sbin/tollgate-wrt 2>/dev/null || which tollgate-wrt 2>/dev/null || echo 'NOT FOUND'")
		if !strings.Contains(verifyOut, "NOT FOUND") {
			// Post-install version assertion — UNCONDITIONAL. It used to be
			// gated on the source URL containing the feed tag, so a GitHub
			// fallback install was never compared with anything and a downgrade
			// rendered as a green "done" step. Now the installed version is
			// compared for EVERY source: a mismatch against the version the
			// supplying source names fails the step, and a version that is not
			// the requested release can no longer render as a plain success.
			if pkgVer := readInstalledPkgVersion(client); pkgVer != "" {
				job.addLog("Installed tollgate-wrt package version: " + pkgVer)
				fatal, warn := pkgVersionVerdict(pkgVer, pkgSourceURL, routerArch, pkgExtension)
				if fatal != "" {
					job.addLog("ERROR: " + fatal)
					jobFail(job, 6, "tollgate-wrt version mismatch", fatal)
					return
				}
				if warn != "" {
					job.addLog("WARNING: " + warn)
					installStatus = "warn"
				} else {
					job.addLog("Package version verified against " + feedReleaseTag + ": " + pkgVer)
				}
			} else {
				job.addLog("WARNING: could not read the installed package version to verify the upgrade")
			}
			// NOTE (SW4a): the fw4/nftables enforcement rules (PR #283) ship
			// inside the package under /etc/nftables.d/{20-nds-enforce,30-backend-firewall}.nft —
			// no separate overlay download is performed (the old overlay URL 404'd).
			//
			// Report WHICH build landed: the installed version (and commit when
			// the backend exposes one) plus the source that supplied it, so
			// "which build am I running, and did this run exercise the feed?"
			// is answerable from the deploy log alone.
			build := reportInstalledBuild(job, client)
			detail := installStepDetail(build, pkgMgr, pkgSourceLabel(routerArch, pkgExtension, pkgSourceURL))
			if installStatus == "warn" {
				detail = "NOT THE REQUESTED RELEASE (" + feedReleaseTag + ") — " + detail
			}
			job.setStep(6, installStatus, detail)
			installedOK = true
		}
	}
	if !installedOK {
		// Last resort: feed install (requires the router to already have
		// working internet — usually not the case on a fresh STA uplink).
		job.addLog("Package not on router — trying " + pkgMgr + " feed...")
		var installOut string
		if pkgMgr == "apk" {
			installOut = sshRun(client, "apk update >/dev/null 2>&1; apk add "+tollgatePackage+" 2>&1 | tail -5")
		} else {
			installOut = sshRun(client, "rm -f /var/lock/opkg.lock 2>/dev/null; opkg update >/dev/null 2>&1; opkg install "+tollgatePackage+" 2>&1 | tail -5")
		}
		job.addLog("Feed install: " + truncate(installOut, 100))
		verifyOut := sshRun(client, "which tollgate-wrt 2>/dev/null || echo 'NOT FOUND'")
		if strings.Contains(verifyOut, "NOT FOUND") {
			// STA config + radio changes are live at this point but the
			// deploy is dead — restore the wireless snapshot so the router
			// is left in its pre-deploy state.
			job.addLog("Rolling back wireless config (pre-deploy snapshot)...")
			rollbackWireless(client)
			jobFail(job, 6, "tollgate-wrt install failed", "Package installation failed — wireless config rolled back")
			return
		}
		// This path installed from the ROUTER's own configured package feeds —
		// not the FreedomTechFeed release asset — so the provenance label says
		// so explicitly rather than reusing "feed" for both meanings. The same
		// "is it the requested release?" rule applies: a router-feed package
		// that is not the requested one must not render as a plain success.
		if pkgVer := readInstalledPkgVersion(client); pkgVer != "" {
			job.addLog("Installed tollgate-wrt package version: " + pkgVer)
			if _, warn := pkgVersionVerdict(pkgVer, "", routerArch, pkgExtension); warn != "" {
				job.addLog("WARNING: " + warn)
				installStatus = "warn"
			}
		}
		build := reportInstalledBuild(job, client)
		detail := installStepDetail(build, pkgMgr, pkgSourceRouterFeed)
		if installStatus == "warn" {
			detail = "NOT THE REQUESTED RELEASE (" + feedReleaseTag + ") — " + detail
		}
		job.setStep(6, installStatus, detail)
	}

	// The .ipk now ships gonuts v0.11.1 with all keyset/multimint/existing-wallet
	// fixes built in — no binary replacement needed.
	time.Sleep(500 * time.Millisecond)

	// Step 7: Brand as TollGate — hostname, SSID, DNS, nodogsplash config
	job.setStep(7, "running", "")
	// Generate unique suffix (e.g. tollgate-a7f2) so multiple routers don't clash
	const ssidChars = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	suffix := make([]byte, 4)
	randBytes := make([]byte, 4)
	if _, err := cryptorand.Read(randBytes); err != nil {
		// Fallback: time-seeded
		for i := range randBytes {
			randBytes[i] = byte(time.Now().UnixNano() >> uint(i*8))
		}
	}
	for i := range suffix {
		suffix[i] = ssidChars[int(randBytes[i])%len(ssidChars)]
	}
	// SSID/hostname pattern: "TollGate-" + 4 random chars (ALLCAPS, no lowercase).
	nodeName := "tollgate-" + string(suffix)
	job.addLog("Branding as " + nodeName + "...")

	// Get router LAN IP first (needed for DNS entries). netifd stores
	// network.lan.ipaddr as "192.168.1.1/24" on current OpenWrt, so the value
	// MUST be sanitized to a bare IPv4 — using "192.168.1.1/24" as an address
	// makes dnsmasq reject "address=/tollgate.lan/192.168.1.1/24" (Bad address
	// in --address), crash-loops it, and breaks all name resolution.
	routerIP := sanitizeIPv4(sshRun(client, "uci -q get network.lan.ipaddr 2>/dev/null"))
	if routerIP == "" {
		routerIP = sanitizeIPv4(sshRun(client, "ip -4 -o addr show dev br-lan 2>/dev/null | awk '{print $4}' | head -1"))
	}
	if routerIP == "" {
		routerIP = "192.168.8.1"
	}
	job.addLog("Router LAN IP: " + routerIP)

	// Try to install mdnsd for .local mDNS support (non-fatal if unavailable)
	mdnsCmd := "opkg update >/dev/null 2>&1 && opkg install mdnsd >/dev/null 2>&1 && /etc/init.d/mdnsd enable 2>/dev/null; /etc/init.d/mdnsd start 2>/dev/null; echo ok"

	brandOut := sshRun(client, strings.Join(brandingCommands(nodeName, routerIP), " && "))
	// (brandingCommands holds the command list; it is extracted so the shipped
	// commands can be run against a stub `uci` in branding_test.go.)
	// Install mdnsd for .local (non-fatal, runs separately)
	mdnsOut := sshRun(client, mdnsCmd)
	if strings.Contains(mdnsOut, "ok") {
		job.addLog("mDNS (.local) support: mdnsd installed/enabled")
	} else {
		job.addLog("mDNS (.local) support: not available (opkg may not have mdnsd)")
	}
	if strings.Contains(brandOut, "branded") {
		job.addLog("Branded: hostname=" + nodeName + ", SSID=" + nodeName + ", DNS=tollgate.lan")
		job.setStep(7, "done", "hostname+SSID+DNS+nodogsplash")
	} else {
		job.addLog("Branding attempted: " + truncate(brandOut, 60))
		job.setStep(7, "done", "configured (partial)")
	}
	time.Sleep(500 * time.Millisecond)

	// Step 8: Verify the captive portal shipped by the tollgate-wrt package AND
	// that it can actually load. Checking only that the directory exists let the
	// "dead portal" regression through: the package shipped splash.html
	// referencing /assets/*.js|css that were never installed, so the SPA never
	// booted (browser: "disallowed MIME type (text/html)" / CORRUPTED_CONTENT).
	// Verify every /assets ref in splash.html resolves to a real file.
	job.setStep(8, "running", "")
	portalCheck := sshRun(client, `D=/etc/tollgate/tollgate-captive-portal-site
[ -d "$D" ] || { echo MISSING_DIR; exit 0; }
[ -f "$D/splash.html" ] || { echo MISSING_SPLASH; exit 0; }
refs=$(grep -oE '/assets/[A-Za-z0-9._-]+' "$D/splash.html" 2>/dev/null | sort -u)
[ -n "$refs" ] || { echo NO_REFS; exit 0; }
miss=""
for r in $refs; do [ -f "$D$r" ] || miss="$miss $r"; done
if [ -n "$miss" ]; then echo "MISSING_ASSETS:$miss"; exit 0; fi
n=$(ls "$D/assets" 2>/dev/null | wc -l | tr -d ' ')
if [ -f "$D/logo192.png" ]; then echo "OK:$n"; else echo "OK_NO_ICON:$n"; fi`)
	out := strings.TrimSpace(portalCheck)
	switch {
	case out == "MISSING_DIR":
		job.addLog("WARNING: captive portal directory not found on router")
		job.setStep(8, "done", "portal not found (installed by package)")
	case out == "MISSING_SPLASH":
		job.addLog("WARNING: captive portal has no splash.html")
		job.setStep(8, "done", "portal incomplete (no splash.html)")
	case out == "NO_REFS":
		job.addLog("WARNING: splash.html references no /assets bundles")
		job.setStep(8, "done", "portal present (no asset refs)")
	case strings.HasPrefix(out, "MISSING_ASSETS:"):
		missing := strings.Join(portalMissingAssets(out), " ")
		job.addLog("ERROR: captive portal references missing assets: " + truncate(missing, 200))
		jobFail(job, 8, "captive portal assets missing",
			"splash.html references /assets bundles that are not installed, so the portal cannot boot. Missing: "+
				missing+"\nThis is the \"dead portal\" regression (a feed release built without its portal assets).")
		return
	case strings.HasPrefix(out, "OK_NO_ICON:"):
		n := strings.TrimPrefix(out, "OK_NO_ICON:")
		job.addLog("Captive portal verified: /assets refs resolve (" + n + " files); WARNING: logo192.png missing")
		job.setStep(8, "done", "portal + "+n+" assets verified (no icon)")
	case strings.HasPrefix(out, "OK:"):
		n := strings.TrimPrefix(out, "OK:")
		job.addLog("Captive portal verified: /assets refs resolve (" + n + " files)")
		job.setStep(8, "done", "portal + "+n+" assets verified")
	default:
		job.addLog("WARNING: unexpected portal check output: " + truncate(out, 120))
		job.setStep(8, "done", "portal check inconclusive")
	}
	time.Sleep(500 * time.Millisecond)

	// Step 9: Configure Lightning address + advanced defaults.
	// lightning_address goes into identities.json → public_identities[].lightning_address
	// (per tollgate-module-basic-go's schema — it reads ONLY from identities.json,
	// never from config.json). margin and profit_share factors go into config.json.
	// If files are absent (tollgate not yet installed), we skip gracefully.
	job.setStep(9, "running", "")

	// 8a: Write lightning_address to identities.json (owner identity).
	lnCmd := "jq --arg la '" + req.LNURL + "' " +
		"'(.public_identities[] | select(.name == \"owner\") | .lightning_address) = $la' " +
		"/etc/tollgate/identities.json > /tmp/ident.tmp 2>&1 && " +
		"mv /tmp/ident.tmp /etc/tollgate/identities.json && echo 'identities updated' || echo 'no identities'"
	lnOut := sshRun(client, lnCmd)

	// 8b: Write margin + profit_share to config.json.
	// Also ensure the default mints are present (idempotent):
	//   7 production mints always;
	//   2 testnut zero-fee test mints ONLY when the deploy payload opts in
	//   (req.TestMints — E2E purchase testing, never a real customer default).
	// Does NOT strip minibits (DLEQ keyset rotation bug fixed in gonuts v0.11.1).
	devSplit, margin := req.resolvedAdvanced()
	devSplit = clamp(devSplit, 0, 50)
	margin = clamp(margin, 0, 100)
	ownerFactor := strconv.FormatFloat(1.0-float64(devSplit)/100.0, 'f', 4, 64)
	devFactor := strconv.FormatFloat(float64(devSplit)/100.0, 'f', 4, 64)
	cfgCmd := configJqCmd(margin, ownerFactor, devFactor, req.Mint, req.TestMints)
	cfgOut := sshRun(client, cfgCmd)

	if strings.Contains(lnOut, "identities updated") {
		job.addLog("identities.json: lightning_address=" + req.LNURL + " for owner")
	}
	if strings.Contains(cfgOut, "config updated") {
		job.addLog("config.json: margin=" + strconv.Itoa(margin) + "%, devSplit=" + strconv.Itoa(devSplit) + "% (profit_share updated)")
		mintsLog := "config.json: mints configured (coinos, minibits, lnserver, macadamia, westernbtc, kashu, cubabitcoin)"
		if req.TestMints {
			mintsLog += " + testnut x2 (E2E test mints — opt-in)"
		}
		job.addLog(mintsLog)
	}

	// 8c: Default mints already injected in 8b above (accepted_mints array).

	if strings.Contains(lnOut, "identities updated") || strings.Contains(cfgOut, "config updated") {
		job.setStep(9, "done", "LNURL: "+req.LNURL)
	} else {
		job.addLog("Config update skipped — no tollgate files found")
		job.addLog("identities: " + truncate(lnOut, 60))
		job.addLog("config: " + truncate(cfgOut, 60))
		job.setStep(9, "done", "skipped (no tollgate config)")
	}
	time.Sleep(500 * time.Millisecond)

	// Step 10: Restart services
	job.setStep(10, "running", "")
	job.addLog("Restarting services...")
	// Verify tollgate-wrt init script exists before restart
	initCheck := sshRun(client, "ls /etc/init.d/tollgate-wrt 2>/dev/null && echo 'exists' || echo 'missing'")
	if strings.Contains(initCheck, "missing") {
		job.addLog("ERROR: tollgate-wrt init script not found — package install failed")
		jobFail(job, 10, "tollgate-wrt not installed", "tollgate-wrt init script missing — package install failed")
		return
	}
	svcOut := sshRun(client, strings.Join([]string{
		"/etc/init.d/rpcd restart 2>&1",
		// Use stop||true;start instead of restart — on OpenWrt 25, restart
		// calls "ubus call service delete" which fails if the service was
		// not procd-managed (e.g. after a manual binary swap). stop||true
		// ignores the "Not found" error, then start registers it fresh.
		"/etc/init.d/tollgate-wrt stop 2>/dev/null; /etc/init.d/tollgate-wrt start 2>&1",
		"/etc/init.d/nodogsplash restart 2>&1",
		"/etc/init.d/uhttpd restart 2>&1",
		"sleep 3",
		"echo 'services restarted'",
	}, "; "))
	job.addLog("Services restarted: " + truncate(svcOut, 60))
	job.setStep(10, "done", "tollgate-wrt+nodogsplash+uhttpd")
	time.Sleep(500 * time.Millisecond)

	// Step 10.5: Re-check subnet collisions now that the tollgate-wrt package
	// install has run its uci-defaults and created network.private (derived
	// from network.lan). The derived private subnet can overlap the upstream
	// even when nothing collided at step 5 — and that silently breaks DNS for
	// the deployed router (its own private interface answers for the upstream
	// gateway's IP). Relocate before the health check so the router is usable.
	client = fixSubnetCollisions(job, client, req.IP, req.Password)

	// Step 11: Health check
	job.setStep(11, "running", "")
	job.addLog("Running health check...")
	// tollgate-wrt registers the wallet (probing every configured mint) BEFORE
	// it binds :2121 — 30-90s+ on a fresh router, and longer in STA mode where
	// DNS/time are still settling after the upstream connects. We retry, and we
	// distinguish two failure modes that the old single body check conflated:
	//   - :2121 never listens              => service down / crash-looping
	//   - :2121 listens but GET / has no
	//     advertisement                     => merchant DEGRADED (mint/wallet
	//                                          not ready) — the API is up
	// The old code reported both as "API not responding", which sent operators
	// chasing the wrong problem.
	healthOK := false
	listening := false
	var healthBody string
	const healthAttempts = 40 // ~2 min
	for attempt := 1; attempt <= healthAttempts; attempt++ {
		time.Sleep(3 * time.Second)
		listening, healthBody = tollgateHealthProbe(client)
		if adLooksHealthy(healthBody) {
			healthOK = true
			job.addLog(fmt.Sprintf("Health check passed on attempt %d", attempt))
			break
		}
		if attempt == 1 || attempt%5 == 0 {
			job.addLog(fmt.Sprintf("Health check attempt %d/%d: listening=%v ad=%q",
				attempt, healthAttempts, listening, truncate(healthBody, 40)))
		}
	}
	if healthOK {
		job.addLog("Health check passed — TollGate API responding")
		job.setStep(11, "done", "API healthy on :2121")
	} else {
		diag := tollgateDiagnostics(client, listening, healthBody)
		job.addLog("Health check FAILED. Diagnostics:\n" + diag)
		// Roll back wireless config so the router's radios are usable for
		// re-scanning after a failed deploy (e.g. old binary crashed with
		// new config, leaving radio0 stuck in STA mode).
		job.addLog("Rolling back wireless config to pre-deploy state...")
		rollbackWireless(client)
		job.addLog("Wireless config restored — radios should be available for scanning")
		if listening {
			jobFail(job, 11, "tollgate API up but no advertisement",
				"TollGate API is UP on :2121 but returned no pricing advertisement — the merchant is degraded (mint/wallet not ready), not down.\n"+diag)
		} else {
			jobFail(job, 11, "tollgate service not listening on :2121",
				"The tollgate-wrt service is NOT listening on :2121 (crash-looping or still initializing).\n"+diag)
		}
		return
	}

	job.mu.Lock()
	job.Status = "done"
	job.mu.Unlock()
	job.addLog("TollGate deployment complete!")
}

// jobFail marks step as failed and the whole job as failed. (Steps that
// previously only did setStep(i,"error") + return left job.Status "running"
// forever — the wizard UI would spin with no error shown.)
func jobFail(job *Job, step int, stepDetail, jobErr string) {
	job.setStep(step, "failed", stepDetail)
	job.mu.Lock()
	job.Status = "failed"
	job.Error = jobErr
	job.mu.Unlock()
}

// tollgateHealthProbe checks the TollGate API from the ROUTER's own shell:
// whether :2121 is listening, and the first bytes of GET /. It never fails —
// a closed port yields listening=false and an empty body.
func tollgateHealthProbe(client *ssh.Client) (listening bool, body string) {
	out := sshRun(client, "netstat -ltn 2>/dev/null | grep -q ':2121' && echo LISTEN || echo NOLISTEN; echo '~~'; wget -qO- --timeout=3 http://127.0.0.1:2121/ 2>/dev/null | head -c 400")
	listening = strings.Contains(out, "LISTEN") && !strings.Contains(out, "NOLISTEN")
	if i := strings.Index(out, "~~"); i >= 0 {
		body = strings.TrimSpace(out[i+2:])
	}
	return listening, body
}

// adLooksHealthy reports whether GET / returned the NIP-61 advertisement
// (which carries the pricing/mint fields) rather than an empty or degraded
// response.
func adLooksHealthy(body string) bool {
	return strings.Contains(body, "kind") || strings.Contains(body, "metric") || strings.Contains(body, "pubkey")
}

// tollgateDiagnostics gathers router-side state after a failed health check so
// the operator (and the UI) can tell a crash-loop from a degraded merchant.
// Best-effort: every command is capped and individually harmless.
func tollgateDiagnostics(client *ssh.Client, listening bool, body string) string {
	parts := []string{
		fmt.Sprintf("listening=%v ad=%q", listening, truncate(body, 120)),
		"service: " + truncate(sshRun(client, "/etc/init.d/tollgate-wrt status 2>&1 | head -2"), 200),
		"proc: " + truncate(sshRun(client, "pgrep -af tollgate-wrt 2>/dev/null | head -1"), 200),
		"date: " + truncate(sshRun(client, "date -u 2>/dev/null"), 80),
		"mints: " + truncate(sshRun(client, "jq -r '.accepted_mints[]?.url' /etc/tollgate/config.json 2>/dev/null | tr '\\n' ' '"), 200),
		"internet: " + truncate(sshRun(client, "(wget -q -T4 -O /dev/null https://1.1.1.1 2>/dev/null && echo online) || echo 'no internet'"), 40),
		"dns: " + truncate(sshRun(client, "nslookup github.com 2>&1 | tail -2"), 160),
		"log: " + truncate(sshRun(client, "logread 2>/dev/null | grep -iE 'tollgate|merchant|mint|wallet' | tail -12"), 1500),
		"debug: " + truncate(sshRun(client, "tail -15 /tmp/tollgate-debug.log 2>/dev/null"), 1500),
	}
	return strings.Join(parts, "\n")
}

// repairLanDNS makes dnsmasq + /etc/hosts serve the router's LAN IP as
// tollgate.lan/tollgate.local, purging any prior entries first. It is
// idempotent and — crucially — heals corruption written by an older installer
// build that used network.lan.ipaddr verbatim ("192.168.1.1/24"), which made
// dnsmasq reject its own config ("Bad address in --address") and crash-loop,
// breaking all name resolution while ping still worked. Returns the bare LAN
// IP used, or "" when it could not be determined.
func repairLanDNS(client *ssh.Client) string {
	ip := sanitizeIPv4(sshRun(client, "uci -q get network.lan.ipaddr 2>/dev/null"))
	if ip == "" {
		ip = sanitizeIPv4(sshRun(client, "ip -4 -o addr show dev br-lan 2>/dev/null | awk '{print $4}' | head -1"))
	}
	if ip == "" {
		return ""
	}
	sshRun(client, strings.Join([]string{
		"for a in $(uci -q get dhcp.@dnsmasq[0].address); do case \"$a\" in /tollgate.lan*) uci -q del_list dhcp.@dnsmasq[0].address=\"$a\";; esac; done",
		"uci -q add_list dhcp.@dnsmasq[0].address='/tollgate.lan/" + ip + "'",
		"for a in $(uci -q get dhcp.lan.dhcp_option); do case \"$a\" in 6,*) uci -q del_list dhcp.lan.dhcp_option=\"$a\";; esac; done",
		"uci -q add_list dhcp.lan.dhcp_option='6," + ip + "'",
		"sed -i '/tollgate\\.lan/d; /tollgate\\.local/d' /etc/hosts",
		"echo '" + ip + " tollgate.lan tollgate.local' >> /etc/hosts",
		"uci commit dhcp",
	}, " && "))
	return ip
}

// upstreamOnline reports whether the router can actually USE the internet
// after the STA associates — a wwan interface can be "up" (layer-2 associated)
// with no default route or no working DNS. It first repairs the dnsmasq
// entries (see repairLanDNS) so corruption from an earlier installer run does
// not mask a healthy upstream, then restarts dnsmasq once. Returns a
// multi-line diagnostic block for logging/failure detail. The payment backend
// registers its wallet (probing every mint) BEFORE it binds :2121, so no
// internet means the API never comes up — this check turns a 2-minute
// health-check timeout into an immediate, actionable message.
func upstreamOnline(client *ssh.Client) (bool, string) {
	if lanIP := repairLanDNS(client); lanIP != "" {
		sshRun(client, "logger -t tollgate-installer 'dns entries repaired for "+lanIP+"' 2>/dev/null; true")
	}
	sshRun(client, "/etc/init.d/dnsmasq restart 2>/dev/null; true")
	var pingOK, dnsOK, routeOK bool
	for i := 0; i < 8; i++ {
		pout := sshRun(client, "ping -c1 -W3 1.1.1.1 2>&1 | tail -2")
		pingOK = strings.Contains(pout, "1 received") || strings.Contains(pout, "1 packets received")
		routeOK = strings.TrimSpace(sshRun(client, "ip route show default 2>/dev/null | head -1")) != ""
		dout := sshRun(client, "nslookup github.com 2>&1 | tail -2")
		dnsOK = strings.Contains(dout, "Address") &&
			!strings.Contains(dout, "can't") && !strings.Contains(dout, "timed out") && !strings.Contains(dout, "refused")
		if pingOK && dnsOK {
			break
		}
		time.Sleep(2 * time.Second)
	}
	diag := strings.Join([]string{
		fmt.Sprintf("ping(1.1.1.1)=%v dns(github.com)=%v default-route=%v", pingOK, dnsOK, routeOK),
		"route: " + truncate(sshRun(client, "ip route show 2>/dev/null | head -5 | tr '\\n' ' '"), 300),
		"uplink: " + truncate(sshRun(client, "for i in wwan wan; do s=$(ubus call network.interface.$i status 2>/dev/null | grep -E '\"up\"|address' | head -3 | tr '\\n' ' '); [ -n \"$s\" ] && echo \"$i: $s\"; done"), 300),
		"resolv: " + truncate(sshRun(client, "grep -v '^#' /etc/resolv.conf 2>/dev/null | head -4 | tr '\\n' ' '"), 200),
		"resolv.auto: " + truncate(sshRun(client, "grep -v '^#' /tmp/resolv.conf.d/resolv.conf.auto 2>/dev/null | head -4 | tr '\\n' ' '"), 200),
		"dnsmasq: " + truncate(sshRun(client, "pgrep -f '[d]nsmasq' >/dev/null && echo running || echo 'not running'"), 40),
		"dnsmasq-log: " + truncate(sshRun(client, "logread 2>/dev/null | grep -i dnsmasq | tail -3 | tr '\\n' ' '"), 300),
		"dnsmasq-address: " + truncate(sshRun(client, "grep -h '^address=' /var/etc/dnsmasq.conf.* 2>/dev/null | head -3 | tr '\\n' ' '"), 200),
	}, "\n")
	return pingOK && dnsOK, diag
}

// staSetupScript returns the shell script that configures the
// tollgate_uplink STA iface on the radio matching band ("2.4"/"5"/"6"), or
// radio0 when band is empty/unknown.
//
// CRITICAL (band): the MT3000 has a 2.4 GHz radio0 and a 5 GHz radio1. The
// original script always targeted radio0, so a 5 GHz-only upstream never
// associated and the failure looked like a wrong password. The target radio is
// now chosen by matching the requested band against each wifi-device's
// `band` (OpenWrt 21+) or `hwmode` (legacy). A "NO_BAND_RADIO" marker means no
// radio on this router serves the requested band.
//
// CRITICAL (dual-STA guard): a radio can host only ONE STA interface — a
// second one kills the router's wireless entirely. Any existing STA iface
// on the target radio (including a previous tollgate_uplink on re-run) is
// DISABLED — not deleted — before the new uplink is added.
//
// The script snapshots /etc/config/wireless to /tmp for rollback (see
// rollbackWireless) and performs a single commit pair; the caller applies the
// whole change set with ONE `wifi reload`.
func staSetupScript(ssid, wifiKey, band string) string {
	return staSetupScriptFor(ssid, wifiKey, band, "")
}

// staSetupScriptFor builds the STA setup script. When radio is non-empty the
// target wifi-device is FORCED to it (used by the multi-radio retry); otherwise
// the radio is chosen by band, falling back to radio0.
func staSetupScriptFor(ssid, wifiKey, band, radio string) string {
	// ssid/wifiKey cross as octal carriers expanded by the shell builtin
	// printf (see the Secret carriers block above) and are decoded into shell
	// variables before any uci call — no router-side binary is needed, and the
	// plaintext never appears in the script text itself (keeps the STA
	// passphrase out of the SSH command string and makes shell injection
	// through the SSID/key impossible).
	//
	// The trailing "\n" matters: without it the last carrier line and the first
	// selector line below form ONE shell word
	// (`sta_key=$(...)want_band="2.4"`), which silently swallows the selector
	// into the key and never assigns want_band/target. Caught by
	// TestStaSetupScriptCarriersRoundTripWithoutAnyRouterBinary.
	carriers := "\n" + octalCarrierVar("sta_ssid", ssid) + "\n" + octalCarrierVar("sta_key", wifiKey) + "\n"
	selector := ""
	if r := strings.TrimSpace(radio); r != "" {
		selector = "target='" + r + "'\n" +
			"uci -q get wireless.$target >/dev/null 2>&1 || { echo 'NO_RADIO'; exit 0; }"
	} else {
		selector = `want_band="` + normalizeBand(band) + `"
target=""
for r in $(uci -q show wireless 2>/dev/null | sed -n "s/^wireless\.\([^.]*\)=wifi-device$/\1/p"); do
	rb=$(uci -q get wireless.$r.band 2>/dev/null)
	[ -z "$rb" ] && rb=$(uci -q get wireless.$r.hwmode 2>/dev/null)
	case "$rb" in 2g|bg|11g) rb=2.4;; 5g|a|11a) rb=5;; 6g|11ax6g) rb=6;; esac
	if [ -n "$want_band" ] && [ "$rb" = "$want_band" ]; then target="$r"; break; fi
done
if [ -z "$target" ]; then
	target=radio0
	uci -q get wireless.radio0 >/dev/null 2>&1 || target=$(uci -q show wireless 2>/dev/null | sed -n "s/^wireless\.\([^.]*\)=wifi-device$/\1/p" | head -n1)
fi
if [ -z "$target" ]; then echo 'NO_RADIO'; exit 0; fi
if [ -n "$want_band" ]; then
	rb=$(uci -q get wireless.$target.band 2>/dev/null)
	[ -z "$rb" ] && rb=$(uci -q get wireless.$target.hwmode 2>/dev/null)
	case "$rb" in 2g|bg|11g) rb=2.4;; 5g|a|11a) rb=5;; 6g|11ax6g) rb=6;; esac
	if [ -n "$rb" ] && [ "$rb" != "$want_band" ]; then echo "NO_BAND_RADIO target=$target band=$rb want=$want_band"; exit 0; fi
fi`
	}
	return carriers + selector + `
cp /etc/config/wireless /tmp/wireless.pre-tollgate &&
uci -q set wireless.$target.disabled='0' &&
for s in $(uci -q show wireless 2>/dev/null | sed -n "s/^wireless\.\([^.]*\)\.device='$target'$/\1/p"); do
	if [ "$(uci -q get wireless.$s.mode 2>/dev/null)" = 'sta' ]; then uci -q set wireless.$s.disabled='1'; fi
done &&
uci set wireless.tollgate_uplink=wifi-iface &&
uci set wireless.tollgate_uplink.network='wwan' &&
uci set wireless.tollgate_uplink.device="$target" &&
uci set wireless.tollgate_uplink.mode='sta' &&
uci set wireless.tollgate_uplink.ssid="$sta_ssid" &&
uci set wireless.tollgate_uplink.encryption='psk2' &&
uci set wireless.tollgate_uplink.key="$sta_key" &&
uci set wireless.tollgate_uplink.disabled='0' &&
uci set network.wwan=interface &&
uci set network.wwan.proto='dhcp' &&
uci commit wireless &&
uci commit network &&
echo "STA_CFG_OK target=$target"`
}

// rollbackWireless restores the /etc/config/wireless snapshot taken before
// STA changes and reloads wifi, returning the router to its pre-deploy
// wireless state. Safe to call when no snapshot exists (no-op).
func rollbackWireless(client *ssh.Client) {
	sshRun(client, "[ -f /tmp/wireless.pre-tollgate ] && cp /tmp/wireless.pre-tollgate /etc/config/wireless && uci commit wireless && (wifi reload 2>/dev/null || wifi 2>/dev/null); true")
}

// reconnectSSH retries sshConnect (radios may be restarting after a wifi
// reload, so the first attempts can time out). Falls back to empty-password
// auth like the initial connect.
func reconnectSSH(ip, password string, attempts int, delay time.Duration) *ssh.Client {
	for i := 0; i < attempts; i++ {
		time.Sleep(delay)
		c := sshConnect(ip, password)
		if c == nil && password != "" {
			c = sshConnect(ip, "")
		}
		if c != nil {
			return c
		}
	}
	return nil
}

// glModelFromBoard normalizes an OpenWrt board_name into a glModelMap key.
// OpenWrt reports GL.iNet boards as "vendor,model" (e.g. "glinet,gl-mt6000"),
// while glModelMap is keyed by the bare lowercase model ("gl-mt6000"). Returns
// "" for an empty or non-GL.iNet board so callers never guess an image.
func glModelFromBoard(board string) string {
	board = strings.ToLower(strings.TrimSpace(board))
	if board == "" {
		return ""
	}
	// Take the model segment after the vendor comma (glinet,gl-mt6000 → gl-mt6000).
	if i := strings.LastIndex(board, ","); i >= 0 {
		board = strings.TrimSpace(board[i+1:])
	}
	if !strings.HasPrefix(board, "gl-") {
		return ""
	}
	return board
}

// waitForRouterAfterFlash polls for the router to come back after sysupgrade.
// After `sysupgrade -n` the router reboots onto a fresh OpenWrt install whose
// root password is EMPTY, so each candidate IP is probed with tcpProbe and then
// connected via reconnectSSH (which retries AND falls back to empty-password
// auth). The connection is only accepted once it verifies the OpenWrt banner,
// so a stock GL.iNet still mid-reboot is not mistaken for the new install.
// Returns the new IP and a live SSH client, or an error on timeout.
func waitForRouterAfterFlash(originalIP, password string, timeout time.Duration) (string, *ssh.Client, error) {
	deadline := time.Now().Add(timeout)
	// Candidate IPs: the original (stock GL may keep it), the OpenWrt default,
	// and the stock GL default. Dedupe against the original.
	candidates := []string{originalIP}
	for _, ip := range []string{"192.168.1.1", "192.168.8.1"} {
		if ip != originalIP {
			candidates = append(candidates, ip)
		}
	}

	for time.Now().Before(deadline) {
		for _, ip := range candidates {
			if !tcpProbe(ip, 22, 500*time.Millisecond) {
				continue
			}
			// Port 22 is open — try to connect. reconnectSSH retries and
			// falls back to empty-password auth for the fresh OpenWrt root.
			client := reconnectSSH(ip, password, 2, 1*time.Second)
			if client == nil {
				continue
			}
			// Verify it's actually OpenWrt (not stock GL still booting).
			out := sshRun(client, "cat /etc/openwrt_release 2>/dev/null | head -1")
			if strings.Contains(out, "OpenWrt") {
				return ip, client, nil
			}
			client.Close()
		}
		// Only pause between polls if we still have time left.
		if time.Now().Before(deadline) {
			time.Sleep(5 * time.Second)
		}
	}
	return "", nil, fmt.Errorf("router did not come back within %v", timeout)
}

// scanSubnetForOpenWrt scans the /24 subnet containing baseIP for a host with
// port 22 open that presents the OpenWrt banner. It is the last-resort fallback
// when the router comes back on an unexpected IP (e.g. the LAN bridge changed
// the subnet). Returns the first matching IP, or "" if none found.
func scanSubnetForOpenWrt(baseIP, password string, timeout time.Duration) string {
	// Derive the /24 prefix from baseIP (e.g. 192.168.1.5 -> 192.168.1).
	parts := strings.Split(baseIP, ".")
	if len(parts) != 4 {
		return ""
	}
	prefix := parts[0] + "." + parts[1] + "." + parts[2] + "."
	deadline := time.Now().Add(timeout)
	for i := 1; i <= 254 && time.Now().Before(deadline); i++ {
		ip := prefix + strconv.Itoa(i)
		if !tcpProbe(ip, 22, 300*time.Millisecond) {
			continue
		}
		client := reconnectSSH(ip, password, 1, 500*time.Millisecond)
		if client == nil {
			continue
		}
		out := sshRun(client, "cat /etc/openwrt_release 2>/dev/null | head -1")
		client.Close()
		if strings.Contains(out, "OpenWrt") {
			return ip
		}
	}
	return ""
}

// ifaceUp parses `ubus call network.interface.<name> status` output and
// reports whether the interface is up. This is the only reliable STA
// verification: grepping iwinfo never matches (kernel interface names are
// not UCI section names), and `network.wireless status | grep up` matches
// ANY radio being up — not the STA association.
func ifaceUp(statusJSON string) bool {
	var st map[string]any
	if err := json.Unmarshal([]byte(statusJSON), &st); err != nil {
		return false
	}
	up, _ := st["up"].(bool)
	return up
}

// wifiRadios returns the UCI wifi-device names (radio0, radio1, ...).
func wifiRadios(client *ssh.Client) []string {
	out := sshRun(client, "uci -q show wireless 2>/dev/null | sed -n 's/^wireless\\.\\([^.]*\\)=wifi-device$/\\1/p'")
	var rs []string
	for _, l := range strings.Split(out, "\n") {
		if s := strings.TrimSpace(l); s != "" {
			rs = append(rs, s)
		}
	}
	return rs
}

// radioBand returns a radio's configured band ("2.4"/"5"/"6") from UCI's
// `band` (OpenWrt 21+) or legacy `hwmode`, or "" when unknown.
func radioBand(client *ssh.Client, radio string) string {
	rb := strings.TrimSpace(sshRun(client, "uci -q get wireless."+radio+".band 2>/dev/null"))
	if rb == "" {
		rb = strings.TrimSpace(sshRun(client, "uci -q get wireless."+radio+".hwmode 2>/dev/null"))
	}
	switch rb {
	case "2g", "bg", "11g":
		return "2.4"
	case "5g", "a", "11a":
		return "5"
	case "6g", "11ax6g":
		return "6"
	}
	return ""
}

// orderRadiosForBand orders radios so the one matching band (when known) is
// tried first; the rest follow. With an unknown band the UCI order is kept.
func orderRadiosForBand(client *ssh.Client, radios []string, band string) []string {
	if band == "" {
		return radios
	}
	var first, rest []string
	for _, r := range radios {
		if radioBand(client, r) == band {
			first = append(first, r)
		} else {
			rest = append(rest, r)
		}
	}
	return append(first, rest...)
}

// attemptSTA applies the STA config on ONE radio, reloads wifi, reconnects and
// waits for wwan to associate. On any failure it rolls the wireless config back
// (best-effort) and returns ok=false. On success it returns a live client the
// caller owns. It never touches the caller's deploy session.
func attemptSTA(ip, password, ssid, wifiPass, radio string) (*ssh.Client, bool) {
	client := sshConnect(ip, password)
	if client == nil && password != "" {
		client = sshConnect(ip, "")
	}
	if client == nil {
		return nil, false
	}
	out := sshRun(client, staSetupScriptFor(ssid, wifiPass, "", radio))
	if !strings.Contains(out, "STA_CFG_OK") {
		rollbackWireless(client)
		client.Close()
		return nil, false
	}
	sshRun(client, "wifi reload 2>/dev/null || wifi 2>/dev/null || true")
	client.Close()

	c := reconnectSSH(ip, password, 3, 5*time.Second)
	if c == nil {
		if r := reconnectSSH(ip, password, 2, 8*time.Second); r != nil {
			rollbackWireless(r)
			r.Close()
		}
		return nil, false
	}
	up := false
	for i := 0; i < 15 && !up; i++ { // ~22s budget: association + DHCP
		up = ifaceUp(sshRun(c, "ubus call network.interface.wwan status 2>/dev/null"))
		if !up {
			time.Sleep(1500 * time.Millisecond)
		}
	}
	if !up {
		rollbackWireless(c)
		c.Close()
		return nil, false
	}
	return c, true
}

// randomPrivateLANIP returns a random address inside 10.0.0.0/8 (RFC1918)
// ending in .1 — the scheme the wizard already used whenever a subnet had to
// move. Keeps the third octet >= 2 to avoid odd edge cases.
func randomPrivateLANIP() string {
	b := make([]byte, 2)
	cryptorand.Read(b)
	return fmt.Sprintf("10.%d.%d.1", int(b[0])%200+10, int(b[1])%200+2)
}

// sanitizeIPv4 extracts a bare IPv4 address from a value that may carry a CIDR
// suffix and/or surrounding quotes: "192.168.1.1/24" -> "192.168.1.1".
//
// OpenWrt stores network.lan.ipaddr BOTH as a bare address (legacy) and as an
// address/prefix pair (netifd), so any caller that needs a plain address MUST
// go through this. Using the raw value as an IP silently corrupts dnsmasq —
// "address=/tollgate.lan/192.168.1.1/24" is rejected with "Bad address in
// --address", dnsmasq crash-loops, and the router can ping 1.1.1.1 but cannot
// resolve any name. The same value also poisons /etc/hosts and DHCP option 6.
// Returns "" when no valid IPv4 address is present.
func sanitizeIPv4(s string) string {
	s = strings.TrimSpace(strings.Trim(strings.TrimSpace(s), "'\""))
	if s == "" {
		return ""
	}
	if i := strings.IndexAny(s, "/ \t"); i >= 0 {
		s = s[:i]
	}
	ip := net.ParseIP(s)
	if ip == nil {
		return ""
	}
	if v4 := ip.To4(); v4 != nil {
		return v4.String()
	}
	return ""
}

// validCIDR reports whether s is a parseable "addr/prefix" CIDR. It exists to
// reject jq's "null/null" (emitted when a ubus status has no ipv4-address),
// which a naive non-empty check would accept and thereby disable collision
// detection.
func validCIDR(s string) bool {
	_, _, err := net.ParseCIDR(strings.TrimSpace(s))
	return err == nil
}

// upstreamCIDR returns the upstream interface address/prefix and the default
// gateway. It checks the WiFi STA (wwan) first, then the wired WAN (wan) — a
// collision breaks DNS for both uplink types — using the live ubus status
// (which carries the real netmask, often not /24) and falling back to the
// gateway as a /24.
func upstreamCIDR(client *ssh.Client) (cidr, gateway string) {
	gateway = strings.TrimSpace(sshRun(client, "ip route show default 2>/dev/null | awk '{print $3}' | head -1"))
	for _, iface := range []string{"wwan", "wan"} {
		// Note: index .address/.mask (not a string interpolation of the whole
		// object) so a status with no ipv4-address yields EMPTY stdout rather
		// than the literal "null/null" that would otherwise pass a naive
		// non-empty check and silently disable collision detection.
		out := strings.TrimSpace(sshRun(client,
			"ubus call network.interface."+iface+" status 2>/dev/null | jq -r '.\"ipv4-address\"[0].address + \"/\" + (.\"ipv4-address\"[0].mask|tostring)' 2>/dev/null"))
		if validCIDR(out) {
			return out, gateway
		}
	}
	if gateway != "" && validCIDR(gateway+"/24") {
		return gateway + "/24", gateway
	}
	return "", ""
}

// localCIDR returns the IPv4 CIDR configured on a local interface (e.g.
// br-lan), or "" when the interface has no address.
func localCIDR(client *ssh.Client, ifname string) string {
	return strings.TrimSpace(sshRun(client, "ip -4 -o addr show dev "+ifname+" 2>/dev/null | awk '{print $4}' | head -1"))
}

// subnetsOverlap reports whether two CIDRs share address space. Malformed input
// yields false (never a false collision).
func subnetsOverlap(a, b string) bool {
	_, na, errA := net.ParseCIDR(a)
	_, nb, errB := net.ParseCIDR(b)
	if errA != nil || errB != nil {
		return false
	}
	return na.Contains(nb.IP) || nb.Contains(na.IP)
}

// moveLocalSubnet relocates a local interface and its DHCP pool to a fresh
// random 10.x.y.0/24, commits, restarts the network and reconnects. Returns
// the live client (best-effort: the original client if reconnecting failed).
func moveLocalSubnet(job *Job, client *ssh.Client, ip, password, ifname, netSection, dhcpSection, why string) *ssh.Client {
	newIP := randomPrivateLANIP()
	job.addLog(fmt.Sprintf("%s — moving %s to %s/24", why, ifname, newIP))
	cmds := []string{
		"uci set network." + netSection + ".ipaddr='" + newIP + "'",
		"uci set network." + netSection + ".netmask='255.255.255.0'",
	}
	if dhcpSection != "" {
		cmds = append(cmds,
			"uci -q set dhcp."+dhcpSection+".start='100'",
			"uci -q set dhcp."+dhcpSection+".limit='150'")
	}
	cmds = append(cmds,
		"uci commit network",
		"uci -q commit dhcp",
		// ifup applies ONLY this interface. A full "/etc/init.d/network
		// restart" has been observed to leave network.lan without an address
		// (br-lan up but no IPv4) — which drops the operator's LAN access — and
		// is otherwise unnecessarily disruptive. Fall back to a restart only
		// if ifup is unavailable.
		"/sbin/ifup "+netSection+" 2>/dev/null || /etc/init.d/network restart 2>/dev/null",
		"sleep 2")
	sshRun(client, strings.Join(cmds, " && "))
	client.Close()

	nc := reconnectSSH(newIP, password, 5, 3*time.Second)
	if nc == nil {
		job.addLog("Could not reconnect on new " + ifname + " IP " + newIP + ", trying original IP " + ip + "...")
		nc = reconnectSSH(ip, password, 3, 5*time.Second)
	}
	if nc == nil {
		job.addLog("WARNING: could not reconnect after moving " + ifname + " — subsequent steps may fail")
		return client
	}
	job.addLog("Reconnected to router on " + newIP)
	return nc
}

// fixSubnetCollisions relocates any local network (br-lan, br-private) that
// overlaps the upstream subnet, then reconnects. A collision makes the router
// route the upstream's own subnet (including its DNS server) to itself, so DNS
// breaks while ping still works.
//
// It MUST also run AFTER the tollgate-wrt package install: the package's
// uci-defaults derives network.private from network.lan (lan/24 with the third
// octet +/-1), which can land inside the upstream subnet even though nothing
// collided at step 5. Returns the live client (reconnected if a subnet moved).
func fixSubnetCollisions(job *Job, client *ssh.Client, ip, password string) *ssh.Client {
	upCIDR, gw := upstreamCIDR(client)
	if upCIDR == "" {
		job.addLog("WARNING: could not determine the upstream subnet — skipping collision detection")
		return client
	}
	locals := []struct{ ifname, netSection, dhcpSection string }{
		{"br-lan", "lan", "lan"},
		{"br-private", "private", "private"},
	}
	for _, ln := range locals {
		lCIDR := localCIDR(client, ln.ifname)
		if lCIDR == "" {
			continue
		}
		if subnetsOverlap(lCIDR, upCIDR) {
			client = moveLocalSubnet(job, client, ip, password, ln.ifname, ln.netSection, ln.dhcpSection,
				fmt.Sprintf("Subnet collision: %s=%s overlaps upstream %s (gw %s)", ln.ifname, lCIDR, upCIDR, gw))
		} else {
			job.addLog(fmt.Sprintf("No subnet collision (%s=%s vs upstream=%s)", ln.ifname, lCIDR, upCIDR))
		}
	}
	return client
}

// configureSTA wires up the tollgate_uplink WiFi STA (deploy step 5).
// Returns false after marking the job failed; any failure AFTER the
// wireless snapshot restores the snapshot and reloads wifi (rollback).
//
// The SSH client is re-established after the single `wifi reload` — the old
// session can go stale while radios restart — and written back through
// pclient so subsequent steps use the live session.
func configureSTA(job *Job, pclient **ssh.Client, ip, password, ssid, wifiPass, band string) bool {
	client := *pclient
	b := normalizeBand(band)
	if b != "" {
		job.addLog("Configuring WiFi STA uplink: " + ssid + " (" + b + " GHz)")
	} else {
		job.addLog("Configuring WiFi STA uplink: " + ssid)
	}

	radios := wifiRadios(client)
	if len(radios) == 0 {
		jobFail(job, 5, "no wireless radio found", "No wifi-device found in UCI — cannot configure STA uplink")
		return false
	}
	// The band-matched radio is tried first (when the band is known); the rest
	// follow, so an unknown/misparsed band still reaches the right radio. This
	// is what fixes a 5 GHz SSID failing on the 2.4 GHz radio.
	ordered := orderRadiosForBand(client, radios, b)
	job.addLog("Trying STA on radios in order: " + strings.Join(ordered, ", "))

	var live *ssh.Client
	for _, r := range ordered {
		newc, ok := attemptSTA(ip, password, ssid, wifiPass, r)
		if ok {
			live = newc
			job.addLog("WiFi STA connected on " + r)
			break
		}
		job.addLog("STA on " + r + " did not associate — trying next radio")
	}
	if live == nil {
		hint := ""
		if c := reconnectSSH(ip, password, 2, 3*time.Second); c != nil {
			hint = staFailureHint(c, ssid, band)
			c.Close()
		}
		detail := "WiFi STA connection failed for \"" + ssid + "\" — check SSID and password (wireless config rolled back)"
		if hint != "" {
			detail += "\n" + hint
		}
		jobFail(job, 5, "WiFi connection failed — check SSID and password", detail)
		return false
	}

	// The retry used its own SSH session; adopt the live one so subsequent
	// deploy steps (and the subnet-conflict fix below) use a working client.
	if client != nil {
		client.Close()
	}
	*pclient = live
	client = live

	// --- Upstream subnet collision detection ---
	// If any of our local networks (br-lan, br-private) overlaps the upstream
	// subnet, the router routes to itself and loses the internet, and DHCP can
	// hand out addresses that collide with the upstream gateway. Upstream masks
	// are not always /24 (e.g. 10.47.0.0/16), so compare the REAL CIDRs rather
	// than just the first three octets. Every colliding local network is
	// relocated to a fresh random 10.x.y.0/24, and its DHCP pool is moved with
	// it (otherwise clients get leases from the old, colliding range).
	client = fixSubnetCollisions(job, client, ip, password)
	*pclient = client

	// Verify the router can actually USE the upstream before continuing: a
	// wwan iface can be "up" with no route/DNS, and the payment backend cannot
	// bind :2121 until its wallet registers against the mints over the
	// internet. Fail early with an actionable message rather than a 2-minute
	// health-check timeout.
	if online, odiag := upstreamOnline(client); !online {
		job.addLog("Router associated to \"" + ssid + "\" but the internet looks unavailable:\n" + odiag)
		job.addLog("Retrying after a network + dnsmasq reload...")
		sshRun(client, "/etc/init.d/network reload 2>/dev/null; /etc/init.d/dnsmasq restart 2>/dev/null; sleep 3")
		if online2, odiag2 := upstreamOnline(client); !online2 {
			job.addLog("Router still offline after reload:\n" + odiag2)
			jobFail(job, 5, "upstream has no internet",
				"Associated to \""+ssid+"\" but the router cannot use the internet. Check whether the failing check below is routing or name resolution (DNS), and whether the upstream network actually provides internet or is a captive portal.\n"+odiag2)
			return false
		}
		job.addLog("Internet available after reload")
	} else {
		job.addLog("Upstream internet verified (route + DNS)")
	}

	job.setStep(5, "done", "STA mode: "+ssid)
	return true
}

// testSTAConfig applies the STA settings for ssid/wifiPass, waits for the
// wwan interface to come up, then ALWAYS restores the pre-change wireless
// config. Used by /api/wifi-test so a wrong SSID/password surfaces on the form
// before a deploy spends time flashing/installing. Returns (ok, message).
func testSTAConfig(ip, password, ssid, wifiPass, band string) (bool, string) {
	b := normalizeBand(band)
	client := sshConnect(ip, password)
	if client == nil && password != "" {
		client = sshConnect(ip, "")
	}
	if client == nil {
		return false, "cannot connect to router via SSH"
	}
	fw := sshRun(client, "cat /etc/openwrt_release 2>/dev/null")
	if !strings.Contains(fw, "OpenWrt") {
		client.Close()
		return false, "the router is not running OpenWrt yet — the WiFi check runs after flashing"
	}
	radios := wifiRadios(client)
	ordered := orderRadiosForBand(client, radios, b)
	client.Close()
	if len(ordered) == 0 {
		return false, "no wireless radio found on the router"
	}

	// Try each radio until one associates (band-matched first). The test must
	// leave the router's prior wireless config in place, so every attempt —
	// successful or not — restores the snapshot (attemptSTA rolls back on
	// failure; we roll back the successful one here).
	for _, r := range ordered {
		c, ok := attemptSTA(ip, password, ssid, wifiPass, r)
		if ok {
			rollbackWireless(c)
			c.Close()
			return true, "connected to \"" + ssid + "\" on " + r
		}
	}

	hint := ""
	if c := reconnectSSH(ip, password, 2, 3*time.Second); c != nil {
		hint = staFailureHint(c, ssid, band)
		c.Close()
	}
	msg := "WiFi connection failed for \"" + ssid + "\""
	if b != "" {
		msg += " (" + b + " GHz)"
	}
	msg += " — check the SSID and password"
	if hint != "" {
		msg += "\n" + hint
	}
	return false, msg
}

// staFailureHint inspects the wifi logs after a failed association and returns
// a short, actionable hint — distinguishing a wrong password (WPA 4-way
// handshake failure) from a band/visibility problem. Best-effort: "" when the
// logs do not clearly indicate one.
func staFailureHint(client *ssh.Client, ssid, band string) string {
	if client == nil {
		return ""
	}
	low := strings.ToLower(sshRun(client, "logread 2>/dev/null | grep -iE 'wpa|handshake|assoc|ssid|sae' | tail -8"))
	switch {
	case strings.Contains(low, "4-way handshake failed"),
		strings.Contains(low, "pre-shared key"),
		strings.Contains(low, "invalid psk"),
		strings.Contains(low, "psk mismatch"):
		return "The password was rejected (WPA handshake failed) — the WiFi password looks wrong."
	case strings.Contains(low, "ap not found"),
		strings.Contains(low, "no suitable network"),
		strings.Contains(low, "ssid not found"),
		strings.Contains(low, "join failed"):
		if b := normalizeBand(band); b != "" {
			return "The network was not found on the " + b + " GHz radio — check the band and that the router can reach the access point."
		}
		return "The network was not found — check the SSID and that the router can reach the access point."
	}
	return ""
}

// httpGetFile downloads a release asset on the laptop, following redirects
// (GitHub release URLs redirect to a CDN), with a 60s timeout and a 64 MB
// size guard. This is the PRIMARY package path — pushing the bytes over SSH
// avoids depending on the router's DNS/TLS stack entirely.
func httpGetFile(url string) ([]byte, error) {
	netClient := &http.Client{Timeout: 60 * time.Second}
	resp, err := netClient.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %s", resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 64<<20))
}

// downloadWithRetry downloads url with up to `attempts` tries, backing off
// exponentially (baseDelay * 2^attempt) between failures. Transient network
// errors and HTTP 5xx responses are retried; a definitive 4xx (e.g. 404 for a
// bad image pin) is NOT retried — retrying a 404 wastes time and masks a
// broken URL. Returns the first non-retryable error or the last error.
func downloadWithRetry(url string, attempts int, baseDelay time.Duration) ([]byte, error) {
	var lastErr error
	for i := 0; i < attempts; i++ {
		data, err := httpGetFile(url)
		if err == nil {
			return data, nil
		}
		lastErr = err
		// Do not retry definitive client errors (404, 403, 410, etc.) — the
		// URL is broken and retrying will not fix it.
		if isDefinitiveHTTPError(err) {
			return nil, err
		}
		if i < attempts-1 {
			time.Sleep(baseDelay * time.Duration(1<<i))
		}
	}
	return nil, lastErr
}

// isDefinitiveHTTPError reports whether err is a non-retryable HTTP client
// error (4xx). Network errors and 5xx are transient and retryable.
func isDefinitiveHTTPError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	// httpGetFile returns errors of the form "HTTP 404 Not Found".
	if !strings.HasPrefix(msg, "HTTP ") {
		return false
	}
	// Extract the status code.
	rest := strings.TrimPrefix(msg, "HTTP ")
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return false
	}
	code, convErr := strconv.Atoi(fields[0])
	if convErr != nil {
		return false
	}
	return code >= 400 && code < 500
}

// parseSysupgradeError inspects sysupgrade output for common failure modes and
// returns a human-readable, actionable message. Unknown output falls back to a
// generic message with the raw output truncated.
func parseSysupgradeError(out string) string {
	low := strings.ToLower(out)
	switch {
	case strings.Contains(low, "image check failed"),
		strings.Contains(low, "invalid image"),
		strings.Contains(low, "wrong image"),
		strings.Contains(low, "unsupported image"),
		strings.Contains(low, "not a valid sysupgrade"):
		return "sysupgrade rejected the image (incompatible or corrupt). Re-download and retry, or flash manually via GL.iNet recovery mode."
	case strings.Contains(low, "no space left"),
		strings.Contains(low, "not enough space"),
		strings.Contains(low, "insufficient space"),
		strings.Contains(low, "cannot allocate"):
		return "Router storage is full. Free space on /tmp (e.g. remove old images) and retry, or flash manually."
	case strings.Contains(low, "command not found"),
		strings.Contains(low, "sysupgrade: not found"):
		return "sysupgrade is not available on this firmware — it is not a standard OpenWrt install. Flash manually via GL.iNet recovery mode."
	case strings.Contains(low, "connection refused"),
		strings.Contains(low, "connection reset"),
		strings.Contains(low, "broken pipe"):
		return "SSH connection dropped during sysupgrade (expected — the router reboots). Waiting for it to come back."
	default:
		return "sysupgrade output: " + truncate(out, 200)
	}
}

// httpGetFileOrEmpty is like httpGetFile but returns an empty slice on error
// instead of an error — used for best-effort directory listing fetches where
// a failure just means we can't parse the page (non-fatal).
func httpGetFileOrEmpty(url string) []byte {
	data, err := httpGetFile(url)
	if err != nil {
		return nil
	}
	return data
}

// stageAssetURLs returns the ordered list of asset URLs a PreStage run must
// download for the given router state:
//
//   - stock GL.iNet router that will be flashed (isStockGL): the OpenWrt
//     sysupgrade image for the detected model (when known) PLUS the
//     tollgate-wrt package in BOTH formats — the post-flash package manager
//     is only known after the reboot, and staging both guarantees a cache
//     hit whichever one the install step probes.
//   - router already on OpenWrt: only the package format matching its live
//     package manager (apk vs opkg). An empty/unknown pkgMgr stages both so
//     the deploy still works offline.
//
// Unknown GL models contribute no image URL (the flash step reports its own
// actionable "unknown model" error); the package formats are still staged so
// an operator can fix the model table and re-deploy offline.
func stageAssetURLs(isStockGL bool, glModel, pkgMgr string) []string {
	urls := []string{}
	if isStockGL {
		if img, ok := glModelMap[glModel]; ok {
			urls = append(urls, img.URL())
		}
		urls = append(urls, tollgatePkgURL, tollgatePkgAPKURL)
		return urls
	}
	switch pkgMgr {
	case "apk":
		urls = append(urls, tollgatePkgAPKURL)
	case "opkg":
		urls = append(urls, tollgatePkgURL)
	default: // unknown — stage both so a later probe hits the cache
		urls = append(urls, tollgatePkgURL, tollgatePkgAPKURL)
	}
	return urls
}

// stageAssetURLsForArch is the arch-aware variant of stageAssetURLs used by
// the selection-time pre-stage job and the deploy PreStage step. It derives
// the package URLs for the DETECTED OpenWrt arch (feed-primary plus the
// GitHub fallback), so a non-aarch64 router is not served the wrong package.
// An empty arch (unknown, e.g. a stock router that will be flashed) falls back
// to the pinned aarch64 assets, matching the historical behaviour.
func stageAssetURLsForArch(arch string, isStockGL bool, glModel, pkgMgr string) []string {
	urls := []string{}
	pkg := func(ext string) []string {
		if arch == "" {
			if ext == ".apk" {
				return []string{tollgatePkgAPKURL}
			}
			return []string{tollgatePkgURL}
		}
		return pkgCandidateURLs(arch, ext)
	}
	if isStockGL {
		if img, ok := glModelMap[glModel]; ok {
			urls = append(urls, img.URL())
		}
		// The post-flash package manager is only known after the reboot, so
		// stage both formats.
		urls = append(urls, pkg(".ipk")...)
		urls = append(urls, pkg(".apk")...)
		return urls
	}
	switch pkgMgr {
	case "apk":
		urls = append(urls, pkg(".apk")...)
	case "opkg":
		urls = append(urls, pkg(".ipk")...)
	default: // unknown — stage both so a later probe hits the cache
		urls = append(urls, pkg(".ipk")...)
		urls = append(urls, pkg(".apk")...)
	}
	return urls
}

// runPreStage is the PreStage wiring point called from runDeployment right
// after verify (so glModel/isStockGL are known) and before flash. It probes
// the router's package manager (when the router is already OpenWrt — a stock
// GL.iNet router will be flashed to the pinned OpenWrt release whose package
// manager stageAssetURLs covers by staging both formats), picks the asset
// URLs for the router state, and stages them into the Job's stageCache.
// Failures are logged but non-fatal: the flash/install steps keep their
// live-fetch → router-wget → feed fallbacks on a cache miss.
func runPreStage(job *Job, client *ssh.Client, isStockGL bool, glModel string) {
	pkgMgr := ""
	arch := ""
	if !isStockGL && client != nil {
		pkgMgr = strings.TrimSpace(sshRun(client, "command -v apk >/dev/null 2>&1 && echo apk || echo opkg"))
		arch = detectArch(client)
	}
	urls := stageAssetURLsForArch(arch, isStockGL, glModel, pkgMgr)
	if len(urls) == 0 {
		job.addLog("PreStage: nothing to stage for this router state")
		return
	}
	job.addLog(fmt.Sprintf("PreStage: downloading %d asset(s) to staging cache...", len(urls)))
	failed := stageAssets(job, urls)
	if len(failed) > 0 {
		for _, u := range failed {
			job.addLog("PreStage: could not stage " + truncate(u, 100) + " — deploy will fall back to live download")
		}
	}
	staged := len(urls) - len(failed)
	job.addLog(fmt.Sprintf("PreStage: %d/%d asset(s) staged", staged, len(urls)))
}

// stageAssets downloads every URL in urls into the Job's stageCache (keyed
// by the exact URL) unless that URL is already staged. Already-cached URLs
// are skipped — staging is idempotent, so re-running it (e.g. a retried
// deploy sharing the Job) performs zero network fetches. Uses
// downloadWithRetry (3 attempts, 4xx short-circuit) for every asset, the
// same reliability pattern as the flash-image download. Returns the URLs
// that failed to stage; callers log them and continue, because the
// flash/install steps fall back to live fetch → router-side wget → feed on
// a cache miss.
func stageAssets(job *Job, urls []string) []string {
	var failed []string
	total := 0
	for _, u := range urls {
		if u != "" {
			total++
		}
	}
	done := 0
	advance := func() { done++; job.setProgress(done, total, "downloading") }
	job.setProgress(0, total, "downloading")
	for _, u := range urls {
		if u == "" {
			continue
		}
		if _, ok := job.stagedAsset(u); ok {
			advance()
			continue
		}
		// Disk re-deploy cache (Task 5): if a previous deploy to another
		// router already staged this URL, load the persisted bytes instead of
		// downloading again. Only version-pinned assets are eligible (see
		// persistableDiskAsset) — a package binary is never loaded from disk,
		// so a stale cached package can never shadow a newer release.
		if persistableDiskAsset(u) {
			if data, ok := loadStageDisk(u); ok {
				job.addLog("PreStage: using disk cache for " + truncate(u, 100) + " (no download)")
				job.stageAsset(u, data)
				advance()
				continue
			}
		}
		data, err := downloadWithRetry(u, 3, 2*time.Second)
		if err != nil || len(data) == 0 {
			failed = append(failed, u)
			advance()
			continue
		}
		job.stageAsset(u, data)
		// Write through to the disk cache so a later deploy to another router
		// re-uses these bytes. Non-fatal: a read-only HOME or full disk must
		// not fail the deploy — the live-fetch fallback remains for next time.
		if persistableDiskAsset(u) {
			if err := saveStageDisk(u, data); err != nil {
				job.addLog("PreStage: could not persist " + truncate(u, 100) + " to disk cache: " + err.Error())
			}
		}
		advance()
	}
	return failed
}

// stagedOrLiveBytes returns the bytes for a small deploy asset — the
// tollgate-wrt package (.ipk/.apk), nodogsplash .ipk, or jq .ipk — keyed by
// the EXACT source URL. When the PreStage step cached that URL the bytes are
// served with zero network (the offline-install case staging exists for). On a
// cache miss the asset is fetched live with httpGetFile — packages are small,
// single downloads, so they intentionally do NOT use the heavier
// downloadWithRetry path (that is reserved for the flash image, see
// flashImageBytes in images.go); a live failure here is what triggers the
// install step's router-side wget → feed fallback chain. The second return
// reports whether the bytes came from the cache so callers can log the actual
// acquisition path. A "Downloading ..." log is emitted before any live fetch
// so the operator sees progress during the (up to 60s) download.
func stagedOrLiveBytes(job *Job, label, url string) ([]byte, bool, error) {
	if data, ok := job.stagedAsset(url); ok {
		job.addLog("Using staged " + label + " from cache (no download)")
		return data, true, nil
	}
	job.addLog("Downloading " + label + " (laptop-side)...")
	data, err := httpGetFile(url)
	return data, false, err
}

// ---- On-disk re-deploy cache (~/.tollgate-stage) ----
//
// The Job stageCache is in-memory and per-Job, so a second deploy to a
// different router (a fresh Job) would re-download every asset. Task 5 adds a
// small on-disk cache so re-deploys re-use previously staged binaries.
//
// RISK 3 (consultant): ONLY the version-pinned flash image is persisted.
// Package binaries (tollgate-wrt .ipk/.apk, nodogsplash, jq) can change
// between releases — a stale cached copy would shadow the newer package and
// could install an outdated backend. The flash image URL is pinned to a fixed
// OpenWrt release (openWrtVersion), so its bytes are stable by construction.

// stageDiskDirOverride redirects the on-disk staging cache directory.
// Non-empty in tests to keep the real home directory untouched.
var stageDiskDirOverride = ""

// stageDiskDir returns the on-disk staging cache directory: ~/.tollgate-stage
// (or stageDiskDirOverride, set by tests to keep the real home directory
// untouched). Empty when no home dir exists — callers then skip disk
// persistence (in-memory cache + live fallback only).
func stageDiskDir() string {
	if stageDiskDirOverride != "" {
		return stageDiskDirOverride
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".tollgate-stage")
}

// stageDiskPath returns the on-disk cache file path for an asset URL:
// <cacheDir>/<hex sha256 of url>. Addressing by URL keeps one file per asset
// across deploys and makes corruption detectable by size (see loadStageDisk);
// a changed URL (new release) naturally misses and re-downloads.
func stageDiskPath(url string) string {
	sum := sha256.Sum256([]byte(url))
	return filepath.Join(stageDiskDir(), hex.EncodeToString(sum[:]))
}

// persistableDiskAsset reports whether an asset URL may be written to / read
// from the on-disk re-deploy cache. Default policy: ONLY version-pinned flash
// images from glModelMap qualify — package binaries are never persisted
// (consultant RISK 3, staging plan 2026-09-08). Var so tests can pin the
// policy to a local httptest URL without network access.
var persistableDiskAsset = func(url string) bool {
	for _, img := range glModelMap {
		if img.URL() == url {
			return true
		}
	}
	return false
}

// loadStageDisk returns the persisted bytes for url from the on-disk staging
// cache. ok=false on any miss or when the file is empty/corrupt (size 0) —
// the caller then falls back to a live download.
func loadStageDisk(url string) ([]byte, bool) {
	if stageDiskDir() == "" {
		return nil, false
	}
	path := stageDiskPath(url)
	fi, err := os.Stat(path)
	if err != nil || fi.Size() == 0 {
		return nil, false
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return nil, false
	}
	return data, true
}

// saveStageDisk persists data for url to the on-disk staging cache, creating
// the cache directory if needed. The write is atomic (temp file + rename) so
// a crash mid-write can never leave a truncated file that a later deploy
// would trust as a complete image.
func saveStageDisk(url string, data []byte) error {
	if stageDiskDir() == "" || len(data) == 0 {
		return nil
	}
	path := stageDiskPath(url)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".stage-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// extractIPKFilename scans an OpenWrt package directory listing (HTML) and
// returns the first .ipk filename that starts with the given package name.
// e.g. extractIPKFilename(html, "nodogsplash") → "nodogsplash_5.0.2-1_aarch64_cortex-a53.ipk"
func extractIPKFilename(html string, pkgName string) string {
	// The listing has entries like: <a href="nodogsplash_5.0.2-1_aarch64_cortex-a53.ipk">
	prefix := pkgName + "_"
	for _, line := range strings.Split(html, "\n") {
		idx := strings.Index(line, prefix)
		if idx < 0 {
			continue
		}
		rest := line[idx:]
		end := strings.Index(rest, ".ipk")
		if end < 0 {
			continue
		}
		// Verify the character after .ipk is a quote or end of attribute
		afterIPK := rest[end+4:]
		if len(afterIPK) == 0 || afterIPK[0] == '"' || afterIPK[0] == '\'' || afterIPK[0] == '<' {
			return rest[:end+4]
		}
	}
	return ""
}

func truncate(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	if len(s) > maxLen {
		return s[:maxLen] + "..."
	}
	return s
}

// clamp returns n constrained to the inclusive range [lo, hi]. Used to keep
// the advanced defaults (devSplit, margin) within safe bounds regardless of
// what the client sends.
func clamp(n, lo, hi int) int {
	if n < lo {
		return lo
	}
	if n > hi {
		return hi
	}
	return n
}
