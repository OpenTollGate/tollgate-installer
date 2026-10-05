package app

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
	"regexp"
	"runtime/debug"
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

// routerAuthProof proves a CANDIDATE root credential against the router on a
// fresh connection: it must offer nothing but that candidate (no empty-password
// retry, no default SSH key) and must report false on any failure instead of
// assuming the credential works. proveRootPassword (ssh.go) satisfies it and
// runDeployment supplies it; tests inject a fake.
//
// It is only a proof that a password CHANGE took when the router had a real
// credential before the write — on an EMPTY root hash every password
// authenticates, so a login proves nothing (see applyRootPassword).
type routerAuthProof func(password string) bool

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

// rootHashProbeCmd prints exactly one of `empty` / `locked` / `set` / `unknown`
// for root's /etc/shadow entry. Field 2 is the hash; a MISSING root line is
// reported as empty because rpcd then has no hash to check either.
//
// The two guards are the difference between "this router has no credential"
// and "this probe cannot tell" (#46 review, finding 3). `empty` AUTHORISES
// setting a password, and setting one OVERWRITES whatever hash is there, so
// anything the probe cannot read must classify as `unknown` and be refused:
//
//   - `[ -r /etc/shadow ] || { echo unknown; exit 0; }` — a missing or
//     unreadable shadow file is not the empty state.
//   - `h=$(awk …) || { echo unknown; exit 0; }` — awk missing from PATH (or
//     killed) is not "root has no hash" either. The guard is on awk's EXIT
//     STATUS, not on its stderr: merely dropping `2>/dev/null` does not work,
//     because the shell's error line lands before the `case` branch still
//     prints `empty`, and parseRootHashState reads the LAST field.
//
// The command is self-contained (`exit 0` on the guarded paths) and must be
// run as a standalone remote command, not concatenated into a longer script.
const rootHashProbeCmd = `[ -r /etc/shadow ] || { echo unknown; exit 0; }; ` +
	`h=$(awk -F: '$1=="root"{print $2}' /etc/shadow) || { echo unknown; exit 0; }; ` +
	`case "$h" in "") echo empty ;; '!'*|'*'*) echo locked ;; ?*) echo set ;; *) echo unknown ;; esac`

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

const (
	// credentialFileEnv overrides the recovery-log location. The tests use it to
	// keep the operator's own log out of the way; an operator who keeps secrets
	// elsewhere can point it at their password manager's drop directory.
	credentialFileEnv = "TOLLGATE_CREDENTIAL_FILE"
	// defaultCredentialFile is the recovery log, kept next to the operator's
	// other SSH state (cf. defaultKnownHostsFile).
	defaultCredentialFile = ".tollgate-root-credentials"
)

// credentialFilePath returns the recovery-log path, or "" when it cannot be
// determined (no override and no home directory).
func credentialFilePath() string {
	if p := strings.TrimSpace(os.Getenv(credentialFileEnv)); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, defaultCredentialFile)
}

// persistRootCredential appends a generated root credential to the operator's
// recovery log and returns the path it wrote to.
//
// It is called BEFORE the credential is applied to the router (see
// ensureRootCredential), because the router must never end up holding a root
// password that exists nowhere. The one-shot /api/status serve covers the case
// where the operator is watching the wizard; this file covers every way that
// serve is still missable — a closed tab, a poll that consumed the value while
// they were away, a crash between the deploy and the final screen. Without it a
// failed deploy leaves a router whose only remaining access (an empty root
// password) has been replaced by a string nobody has: an unrecoverable lockout.
//
// The file is created 0600 and FORCED to 0600 when it already exists, so an
// operator's umask cannot publish a router's root password to other local
// users. Lines are appended, never rewritten: it is a recovery log, so an
// earlier router's credential survives every later deploy.
func persistRootCredential(ip, password string) (string, error) {
	path := credentialFilePath()
	if path == "" {
		return "", fmt.Errorf("no credential file path available (no home directory and no $%s)", credentialFileEnv)
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", err
		}
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	defer f.Close()
	// O_CREATE's mode applies only to a NEW file, and an existing one may have
	// been created wider (or by an older build), so tighten it every time.
	if err := f.Chmod(0o600); err != nil {
		return "", err
	}
	if _, err := fmt.Fprintf(f, "%s	%s	%s\n", time.Now().Format(time.RFC3339), ip, password); err != nil {
		return "", err
	}
	// The credential must outlive a crash the instant after it is applied.
	if err := f.Sync(); err != nil {
		return "", err
	}
	return path, nil
}

// ensureRootCredential makes sure the router is never left without a root
// credential and returns the password later deploy steps must reconnect with.
//
// Modes, in order:
//  1. a password was supplied -> set it and PROVE it took (see
//     applyRootPassword: shadow hash when the router had none, plus a fresh
//     login whenever it already had one, so a passwd that silently leaves the
//     OLD hash in place cannot be reported as success);
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
func ensureRootCredential(job *Job, run routerRun, prove routerAuthProof, supplied string) (string, bool) {
	// A supplied password is applied whatever the state says, so the state probe
	// is only needed by the modes below (it used to run and be discarded here).
	if supplied != "" {
		return supplied, applyRootPassword(job, run, prove, supplied, "supplied")
	}

	state := parseRootHashState(run(rootHashProbeCmd))
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
		// Persist BEFORE applying: whatever happens after this point — a crash,
		// a Ctrl-C, a failed package download two steps later — the router's new
		// credential is already recoverable from disk instead of existing only
		// in this process's memory and one one-shot read.
		if path, perr := persistRootCredential(job.IP, pw); perr != nil {
			where := credentialFilePath()
			if where == "" {
				where = "(no path available)"
			}
			job.addLog("WARNING: could not save the generated root credential to " + where + " (" + perr.Error() + ").")
			job.addLog("It is shown ONCE on this deploy's final screen — store it in your password manager before you close the page, because it is not recoverable from disk.")
		} else {
			job.setCredentialFile(path)
		}
		// DEFER the set. The router keeps the empty credential that got the
		// deploy in until the whole deploy has succeeded (finalizeRootCredential),
		// so a failure at any later step leaves the operator's access untouched.
		job.setPendingRootPassword(pw)
		job.setStep(4, "done", "deferred until the install is verified")
		return "", true
	default:
		// rootHashUnknown: /etc/shadow missing or unreadable, no awk, or a
		// dead session. Never assume this is the credential-less state —
		// setting a password here would overwrite a real one.
		job.addLog("Could NOT read the router's root password state (/etc/shadow missing or unreadable, no awk, or a dead SSH session) — refusing to set a password, which could overwrite a working one.")
		jobFail(job, 4, "cannot read the router's root password state",
			"Could not determine whether root has a usable password (/etc/shadow unreadable: no awk, no shadow file, or a dead SSH session). "+
				"Re-run the deploy and supply the router's root password explicitly.")
		return "", false
	}
}

// finalizeRootCredential applies a credential DEFERRED by ensureRootCredential
// (the router had no root password, so the deploy generated one). It is set
// here, once, after the deploy's last gate — the health check — has passed, and
// only then is it armed for the one-shot view.
//
// Why the deferral: setting a root password OVERWRITES the router's existing
// access, which on a freshly-reset router is "log in with an empty password".
// Doing it at step 4 — before the package download (step 6), the branding, the
// portal, the LNURL config, the service restarts and the health check — means
// any later failure leaves the operator locked out of hardware the wizard just
// changed. Persisting the value (PR #70) makes that recoverable; not re-keying
// until the work has actually succeeded means it does not happen at all.
//
// Arming happens ONLY here, so the failure view's "this deploy failed AFTER the
// router's root password had already been generated and set" can never be shown
// for a credential that was never set.
//
// Returns false after jobFail when the deploy must not be reported successful:
// a deploy that does not establish the credential it promised is not complete.
func finalizeRootCredential(job *Job, run routerRun, prove routerAuthProof) bool {
	pw := job.takePendingRootPassword()
	if pw == "" {
		// Nothing was deferred: the operator supplied the password, or the
		// router already had one, or this deploy never reached step 4.
		return true
	}
	// applyRootPassword owns step 4's status (it marks it done on success and
	// fails the job otherwise) — deliberately no extra setStep here, which would
	// add a second "running" marker for step 4 (TestDeployStepIndexGuard).
	if !applyRootPassword(job, run, prove, pw, "generated") {
		// The value was persisted before it was ever used (step 4), so this
		// deployment is recoverable by hand even though it is not complete.
		job.addLog("The generated root credential could NOT be applied — take it from the credential file named above and set it on the router by hand.")
		return false
	}
	job.setGeneratedPassword(pw)
	return true
}

// applyRootPassword sets password on the router and PROVES that password — not
// merely that SOME hash — is the one the router now accepts.
//
// The proof has two parts, because neither alone is sound:
//
//  1. the shadow re-read must report a real hash (rootHashSet). This is the
//     empty-hash regression the path exists for: a passwd that silently does
//     nothing on a credential-less router is caught here.
//  2. when the router had a usable credential BEFORE the write (state set,
//     locked, or unreadable), the candidate must ALSO be accepted on a FRESH
//     login. "A hash exists" is not proof that THIS password is in it: on a
//     set→set transition (the router already had a password, the operator
//     supplied a new one) a passwd that fails silently leaves the OLD hash in
//     place, and part 1 then sees precisely the `set` state it looks for — the
//     wizard reported a password nothing on the router accepts, and every later
//     step (STA reconnect, re-auth) carried the same dead credential. This is
//     the #46 follow-up review finding.
//
// Part 2 is deliberately SKIPPED when the router had an EMPTY hash: rpcd's
// rpc_login_test_password() and dropbear both accept ANY password ("" included)
// while the hash is empty, so a successful login in that state proves nothing
// and must not be treated as proof of anything. There part 1 IS the proof.
//
// A missing proof channel (nil prove) is not a licence to skip part 2 either:
// the deploy fails closed, because the alternative is reporting success on a
// credential the router may reject.
func applyRootPassword(job *Job, run routerRun, prove routerAuthProof, password, origin string) bool {
	preState := parseRootHashState(run(rootHashProbeCmd))
	out := run(passwdCommand(password))
	if state := parseRootHashState(run(rootHashProbeCmd)); state != rootHashSet {
		jobFail(job, 4, "root password not established",
			"The router still has no usable root password after setting one ("+origin+"). "+
				"The router would be left with unauthenticated root administration; aborting. passwd output: "+truncate(out, 200))
		return false
	}
	if preState != rootHashEmpty {
		if prove == nil {
			jobFail(job, 4, "root password could not be proven ("+origin+")",
				"The router already had a root credential, so \"a hash is present\" is not proof that the "+origin+" password is the one it accepts — and this deploy has no way to prove it on a fresh SSH login. Aborting: reporting success here could hand the operator a credential the router rejects.")
			return false
		}
		if !prove(password) {
			jobFail(job, 4, "root password did not take ("+origin+")",
				"The router still has a root password, but a FRESH SSH login with the "+origin+" password was REFUSED: the credential the wizard set is not the one the router accepts (a passwd that fails silently leaves the previous hash in place). Aborting rather than reporting a working router. passwd output: "+truncate(out, 200))
			return false
		}
		job.addLog("Root password (" + origin + ") accepted on a fresh SSH login")
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

// deviceIdentity is the router's ONE device code and the names derived from it.
// Produced by deviceIdentityScript (router side) + parseDeviceIdentity, and
// consumed by brandingCommands (writer). Nothing else in this repo may mint or
// derive a code.
type deviceIdentity struct {
	Code        string // four characters of [A-Z0-9]
	Nym         string // the operator's nym — the private SSID's prefix
	Source      string // where the code came from: store|hostname|captive-ssid|minted
	Hostname    string // tollgate-<code>
	SSID        string // TollGate-<code> (brand prefix, as the discovery code expects)
	PrivateSSID string // <nym>-<code>, or the operator's own rename
}

// deviceIdentityDefaultNym is the private SSID's prefix when nothing on the
// router says otherwise (the same default the module compiles in).
const deviceIdentityDefaultNym = "c08r4d0r"

// ssidSafeForShell reports whether a value can be carried inside the
// single-quoted `uci -q set …` lines this file builds. Those lines are joined
// with " && " and run as root on the router, so a value containing a single
// quote would end the quote and let the remainder be read as shell syntax. The
// private SSID is built from the operator's own nym and from an SSID read back
// off the router, so by the time it reaches here it is NOT a fixed alphabet.
func ssidSafeForShell(ssid string) bool {
	return ssid != "" && !strings.ContainsAny(ssid, "'\n\r")
}

// privateSSIDCommand writes the private SSID on a private_radio* section, and
// only when that section exists (the module owns the private network's layout).
// An `if` without an else is used deliberately: a bare `uci -q get ... && uci
// -q set ...` returns non-zero when the section is missing, and these commands
// are joined with " && " on the router, so it would abort everything after it —
// including the commits.
//
// A value that cannot be quoted safely is REFUSED rather than escaped: the
// resolver decides the value (deviceIdentityScript's ssid_safe), so arriving here
// with one that cannot be quoted means it came from somewhere unexpected, and a
// no-op that says so is better than a line whose quoting cannot hold.
func privateSSIDCommand(section, ssid string) string {
	if !ssidSafeForShell(ssid) {
		return "echo 'private SSID not written: the value cannot be quoted safely'"
	}
	return "if uci -q get wireless." + section + " >/dev/null 2>&1; then " +
		"uci -q set wireless." + section + ".ssid='" + ssid + "'; fi"
}

// deviceIdentityScript is the router-side half of the device-code contract. ONE
// code, minted once, stored in UCI and reused; the module's uci-defaults script
// (OpenTollGate/tollgate-module-basic-go,
// packaging/files/etc/uci-defaults/99-tollgate-setup → setup_device_identity)
// resolves it the same way, and the decision record lives in
// docs/architecture/one-device-code.md there.
//
// Why the installer asks the ROUTER instead of minting here: this repo was a
// second mint. It generated a fresh code on every deploy and wrote only the
// hostname and the captive SSID, so a later deploy re-named a router that the
// module had already named (bench MT3000: hostname=tollgate-OQ3Q, open SSID
// re-minted to tollgate-0GLK, private SSID carrying a third suffix). Resolving
// on the router means the store is read and written in one place, by both
// writers, and the value cannot drift between them.
//
// Adoption order — identical to the module's, and pinned on both sides:
//
//  1. tollgate.device.code in /etc/config/tollgate   (authoritative)
//  2. a machine-shaped hostname                      (tollgate-OQ3Q)
//  3. a machine-shaped captive SSID                  (TollGate-OQ3Q, tollgate-0GLK)
//  4. mint                                           (only when nothing above hit)
//
// BusyBox ash only: no bashisms, no `od` (the target has no guarantee of it),
// no GNU sed. Every branch is a POSIX `case` so the same alphabet is enforced
// on both sides.
var deviceIdentityScript = `CODE_STORE=/etc/config/tollgate
[ -f "$CODE_STORE" ] || touch "$CODE_STORE" 2>/dev/null
uci -q get tollgate.device >/dev/null 2>&1 || uci -q set tollgate.device='device'

code_norm() {
    v=$(trim_ws "$1" | tr 'a-z' 'A-Z')
    case "$v" in
        [A-Z0-9][A-Z0-9][A-Z0-9][A-Z0-9]) printf '%s' "$v" ;;
    esac
}
code_from_name() {
    p=$(trim_ws "${1%%-*}" | tr 'A-Z' 'a-z')
    s=${1#*-}
    case "$p" in
        tollgate|net4sats) code_norm "$s" ;;
    esac
}
trim_ws() {
    printf '%s' "$1" | tr -d ' \011\015\012'
}
minted_suffix() {
    case "$1" in
        '') return 1 ;;
        [A-Za-z0-9][A-Za-z0-9][A-Za-z0-9][A-Za-z0-9]) return 0 ;;
        *[!0-9]*) return 1 ;;
    esac
    return 0
}
# Can this value be carried inside one of the single-quoted "uci -q set" lines
# brandingCommands builds? Those are joined with " && " and run as root on the
# router, so a single quote in the value would end the quote and let the rest be
# read as shell syntax. The private SSID is built from an SSID read off the router,
# so it is checked here (and again, in Go, by ssidSafeForShell) rather than
# assumed to be machine-shaped text.
ssid_safe() {
    case "$1" in
        ''|*"'"*) return 1 ;;
        *) return 0 ;;
    esac
}

CODE=$(code_norm "$(uci -q get tollgate.device.code 2>/dev/null)")
SRC=store
if [ -z "$CODE" ]; then
    CODE=$(code_from_name "$(uci -q get system.@system[0].hostname 2>/dev/null)")
    SRC=hostname
fi
if [ -z "$CODE" ]; then
    CODE=$(code_from_name "$(uci -q get wireless.tollgate_2g_open.ssid 2>/dev/null)")
    SRC=captive-ssid
fi
if [ -z "$CODE" ]; then
    CODE=$(code_from_name "$(uci -q get wireless.default_radio0.ssid 2>/dev/null)")
    SRC=captive-ssid
fi
if [ -z "$CODE" ]; then
    CODE=$(code_norm "$(hexdump -n 3 -e '4/1 "%02X"' /dev/urandom 2>/dev/null | cut -c1-4)")
    SRC=minted
fi
if [ -z "$CODE" ]; then
    CODE=$(printf '%04X' "$(( $(date +%s) % 65536 ))")
    SRC=minted
fi

NYM=$(trim_ws "$(uci -q get tollgate.device.nym 2>/dev/null)")
case "$NYM" in
    ''|*[!A-Za-z0-9_-]*) NYM="" ;;
esac
if [ -z "$NYM" ]; then
    PRI=$(trim_ws "$(uci -q get wireless.private_radio0.ssid 2>/dev/null)")
    P=${PRI%%-*}; S=${PRI#*-}
    if [ "$S" != "$PRI" ] && [ -n "$P" ] && minted_suffix "$S" && ssid_safe "$P"; then
        NYM="$P"
    else
        NYM=` + strconv.Quote(deviceIdentityDefaultNym) + `
    fi
fi

uci -q set tollgate.device.code="$CODE"
uci -q set tollgate.device.nym="$NYM"
uci commit tollgate

HOSTNAME="tollgate-$CODE"
SSID="TollGate-$CODE"
PRIVATE_SSID="$NYM-$CODE"
PRI=$(trim_ws "$(uci -q get wireless.private_radio0.ssid 2>/dev/null)")
if [ -n "$PRI" ] && ssid_safe "$PRI"; then
    P=${PRI%%-*}; S=${PRI#*-}
    if [ "$P" != "$NYM" ] || [ "$S" = "$PRI" ] || ! minted_suffix "$S"; then
        PRIVATE_SSID="$PRI"
    fi
fi

echo "TG_CODE=$CODE"
echo "TG_CODE_SOURCE=$SRC"
echo "TG_NYM=$NYM"
echo "TG_HOSTNAME=$HOSTNAME"
echo "TG_SSID=$SSID"
echo "TG_PRIVATE_SSID=$PRIVATE_SSID"
`

// parseDeviceIdentity reads the TG_* markers deviceIdentityScript echoes. It
// returns the zero identity when no code was resolved (the router-side resolver
// produced nothing at all) so the caller can fall back LOUDLY rather than
// brand a router with an empty name.
func parseDeviceIdentity(out string) deviceIdentity {
	var id deviceIdentity
	for _, line := range strings.Split(out, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch key {
		case "TG_CODE":
			id.Code = value
		case "TG_CODE_SOURCE":
			id.Source = value
		case "TG_NYM":
			id.Nym = value
		case "TG_HOSTNAME":
			id.Hostname = value
		case "TG_SSID":
			id.SSID = value
		case "TG_PRIVATE_SSID":
			id.PrivateSSID = value
		}
	}
	if id.Code == "" {
		return deviceIdentity{}
	}
	return id
}

// fallbackDeviceIdentity mints a code LOCALLY, for the case where the router-side
// resolver returned nothing usable at all (no uci, no /dev/urandom — a router
// that is already broken in a way the deploy should still report). It is the
// same shape and the same derived names as the router-side resolver, and the
// caller logs that the code could not be stored, so a router branded this way
// will be re-named by the next install that can reach its UCI.
func fallbackDeviceIdentity() deviceIdentity {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	suffix := make([]byte, 4)
	randBytes := make([]byte, 4)
	if _, err := cryptorand.Read(randBytes); err != nil {
		// Fallback: time-seeded
		for i := range randBytes {
			randBytes[i] = byte(time.Now().UnixNano() >> uint(i*8))
		}
	}
	for i := range suffix {
		suffix[i] = alphabet[int(randBytes[i])%len(alphabet)]
	}
	code := string(suffix)
	return deviceIdentity{
		Code:        code,
		Nym:         deviceIdentityDefaultNym,
		Source:      "minted-locally",
		Hostname:    "tollgate-" + code,
		SSID:        "TollGate-" + code,
		PrivateSSID: deviceIdentityDefaultNym + "-" + code,
	}
}

// brandingCommands returns the router-side commands that brand a deployed
// router: hostname, captive SSID, private SSID, DNS/domain records and the
// NoDogSplash pre-auth allow list. Lifted verbatim out of runDeployment so a test
// can run the SHIPPED commands against a stub `uci` and assert what a paying-
// nothing captive client can reach (branding_test.go).
//
// The three names come from ONE device identity (devdeviceIdentityScript below):
// the hostname, the captive SSID and the private SSID all carry the same code,
// so a router is recognisable at a glance and two writers cannot disagree about
// what it is called. This function WRITES the resolved values; it never derives,
// compares or mints a code of its own (a second mint here is what gave the bench
// MT3000 three different names — see docs/architecture/one-device-code.md in
// OpenTollGate/tollgate-module-basic-go).
//
// The private APs ARE written here now (they used to be skipped on purpose,
// which left the private SSID with a code no other name used). The KEY is still
// never touched: the module owns it, and re-writing it would drop every paired
// admin device off the management network.
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
func brandingCommands(id deviceIdentity, routerIP string) []string {
	// Deduplicate /etc/hosts entries, then write fresh ones
	hostsCmd := "sed -i '/tollgate\\.lan/d; /tollgate\\.local/d' /etc/hosts && " +
		"echo '" + routerIP + " tollgate.lan tollgate.local' >> /etc/hosts"
	// The captive (guest) APs, by BOTH spellings: the module names them
	// tollgate_2g_open / tollgate_5g_open, a stock OpenWrt router calls them
	// default_radio0/1, and a deploy can meet either. The uplink STA is skipped
	// (it is not an AP at all) and so is every private_* section — the private
	// APs share the private SSID with the private network, written below.
	guestSSIDCmd := "for i in $(uci -q show wireless 2>/dev/null | sed -n 's/^\\(wireless\\.[A-Za-z0-9_]*\\)=wifi-iface$/\\1/p'); do " +
		"case \"$i\" in *default_radio[0-9]|*tollgate_2g_open|*tollgate_5g_open) " +
		"if [ \"$(uci -q get \"$i.mode\" 2>/dev/null)\" != \"sta\" ]; then uci -q set \"$i.ssid=" + id.SSID + "\"; fi ;; esac; done; true"
	return []string{
		// Hostname
		"uci -q set system.@system[0].hostname='" + id.Hostname + "'",
		// Captive SSID — the guest APs only
		guestSSIDCmd,
		// Private SSID — the operator's admin LAN, same code, nym prefix. The
		// section is only written when it EXISTS: the module owns the private
		// network's layout, and creating a wifi-iface here that the module did
		// not sanction would be a second writer for the same interface.
		privateSSIDCommand("private_radio0", id.PrivateSSID),
		privateSSIDCommand("private_radio1", id.PrivateSSID),
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
		"uci -q set nodogsplash.@nodogsplash[0].gatewayname='" + id.SSID + "'",
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

// runDeploymentGuarded is the deploy entry point the wizard's goroutine uses:
// runDeployment wrapped in a top-level recover (guardDeploymentPanic).
//
// The installer is not supposed to panic, but it runs in a GOROUTINE (main.go)
// and one unrecovered panic there kills the whole wizard process — taking the
// UI and every other job with it, which is strictly worse than a failed deploy
// the operator can see and retry. The #52 review asked for exactly this belt
// alongside the nil-client braces.
func runDeploymentGuarded(job *Job, req deployRequest) {
	guardDeploymentPanic(job, func() { runDeployment(job, req) })
}

// guardDeploymentPanic runs fn, converting any panic into a FAILED JOB — never a
// silent swallow: the job is failed through jobFail (Status "failed" + Error),
// and the panic value with its stack is written to the job log so the operator
// can report it. Split out from runDeploymentGuarded so the containment is
// testable without a router (a panic inside fn must not escape, and must leave
// job.Status == "failed").
func guardDeploymentPanic(job *Job, fn func()) {
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		job.addLog(fmt.Sprintf("PANIC in the deploy goroutine (contained — the wizard stays up): %v\n%s", r, debug.Stack()))
		// Report the step the deploy died on (guarded read; clamped, because a
		// panic can land after a step was renumbered or before any setStep).
		job.mu.Lock()
		step := job.Step
		job.mu.Unlock()
		if step < 0 || step >= len(job.Steps) {
			step = 0
		}
		jobFail(job, step, "internal error — see log",
			fmt.Sprintf("The installer hit an unexpected internal error and stopped instead of grinding on: %v. "+
				"The router may be part-way through the deploy — re-run the wizard from the router's LAN (the log above has the details to report).", r))
	}()
	fn()
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
		// The host-key refusal (when there was one) carries the fingerprint and
		// the exact trust instruction; a generic message would hide both.
		job.Error = sshConnectFailureMessage(req.IP, "Cannot connect to router via SSH")
		job.mu.Unlock()
		return
	}
	// Closure (not `defer client.Close()`): client can be re-assigned when
	// the STA step re-establishes the session after a wifi reload — the
	// deferred call must close whichever client is live at the end. It is
	// nil-SAFE (closeSSHClient): a subnet relocation that severs the connection
	// leaves client nil, and the bare client.Close() this replaced dereferenced
	// that nil and panicked the whole installer (BLOCK 2 of the #52 review).
	defer func() { closeSSHClient(client) }()

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
		// The next statement is another REMOTE COMMAND, and it is exactly where
		// rc17's infinite spinner was measured (2026-10-05): the router answered
		// the firmware read above and then went silent, and the unbounded sshRun
		// parked the deploy goroutine here FOREVER — step 0 done, step 1 still
		// pending, progressTotal 0, /api/status still "running". Use the bounded
		// form and fail the job with the reason instead of spinning.
		board, boardErr := sshRunE(client, "cat /tmp/sysinfo/board_name 2>/dev/null")
		if boardErr != nil {
			failTransportLost(job, 0, req.IP, boardErr)
			return
		}
		glModel = glModelFromBoard(board)
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
			detail := "Router did not come back after flash. Last known IP: " + req.IP + ". See manual recovery docs (GL.iNet recovery mode)."
			// A re-imaged router presents a NEW SSH host key and is therefore
			// refused rather than accepted: say that, and how to trust it,
			// instead of implying the router is dead.
			if r := lastHostKeyRefusal(req.IP); r != "" {
				detail = "Router came back on a DIFFERENT SSH host key (expected after re-imaging). Last known IP: " + req.IP + ". " + r
			}
			jobFail(job, 2, "Router unreachable after flash", detail)
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

	// 3b: free-space pre-flight, BEFORE step 4 touches the credential.
	//
	// WHY (live defect, 2026-10-02/03): on a 16MB-flash GL-AR300M16 the wizard
	// rotated/generated the router's root credential and only then discovered the
	// package does not fit ("Only have 8016kb available on filesystem /overlay,
	// pkg tollgate-wrt needs 23850"). The operator can lose the credential and
	// get nothing installed. Refuse FIRST, while the router is still untouched.
	if !preflightOverlaySpace(job, client) {
		return
	}

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
	}, func(pw string) bool {
		// The proof channel: a FRESH SSH login with only the candidate
		// credential (see proveRootPassword). Deliberately not the live client —
		// the deploy session proves nothing about what the router will accept
		// after the password write.
		return proveRootPassword(req.IP, pw)
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
	// staCommitted records that THIS run committed live radio config (the STA
	// iface + network.wwan) and therefore left a pre-deploy wireless snapshot
	// on the router. Every terminal failure after this point owns restoring it:
	// see restoreWirelessOnFailure.
	staCommitted := false
	if req.Mode == "sta" && req.SSID != "" {
		if !configureSTA(job, &client, req.IP, req.Password, req.SSID, req.WifiPass, req.Band) {
			return
		}
		staCommitted = true
	} else {
		job.addLog("Using WAN upstream (default)")
		// A pre-existing local/upstream subnet overlap breaks name resolution
		// (the router answers for the upstream gateway's own IP), which also
		// breaks the package download — fix it before installing.
		//
		// A nil result means the move severed our only path to the router:
		// adoptRelocatedClient fails STEP 5 (jobFail — a setStep+return used to
		// leave job.Status "running" forever) and we stop WITHOUT dereferencing
		// the dead client.
		nc, ok := adoptRelocatedClient(job, fixSubnetCollisions(job, client, req.IP, req.Password), 5)
		if !ok {
			return
		}
		client = nc
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
		jobFailAfterRestore(job, client, staCommitted, 6,
			"Could not determine router CPU architecture",
			"Could not determine router CPU architecture")
		return
	}

	// Select appropriate package URL based on package manager + arch.
	// OpenWrt 25.12+ uses APK and cannot install legacy .ipk packages.
	_, pkgExtension, ok := selectPkgURL(routerArch, pkgMgr)
	if !ok {
		// Undetectable arch — fail, never substitute aarch64. (selectPkgURL is
		// generic today and only refuses an empty tuple, so this is defensive;
		// it still restores, because the step-5 STA commit is already live.)
		jobFailAfterRestore(job, client, staCommitted, 6,
			"Unsupported CPU arch "+routerArch,
			"Unsupported CPU arch "+routerArch)
		return
	}
	// Candidate download URLs: the tag-consistent feed URL, plus the GitHub
	// release fallback (aarch64 only) ONLY when the operator has explicitly
	// opted in (--allow-fallback / TOLLGATE_ALLOW_GITHUB_FALLBACK=1). The
	// fallback asset is pinned to a DIFFERENT, OLDER release than the requested
	// feed tag, so without the opt-in it is not part of the candidate list at
	// all AND the refusal is held for the fail-loudly check below: a feed
	// outage must not turn into a silent downgrade reported as success.
	pkgCandidates, fallbackRefusal := pkgCandidateURLsWithFallback(
		routerArch, pkgExtension, githubFallbackAllowed())
	// The selection-time report goes through fallbackSelectionLogLine, which
	// says what was DECIDED, not that the requested release is unavailable:
	// nothing has been downloaded yet at this point (C2-I-02 follow-up).
	if line := fallbackSelectionLogLine(routerArch, pkgExtension, pkgCandidates, fallbackRefusal); line != "" {
		job.addLog(line)
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
	// integrity is the verdict on the package bytes (see pkgverify.go). It is
	// carried to the install step detail so a package that could not be checked
	// — or that came from a different release — can never render as an
	// unqualified green "done".
	var integrity pkgIntegrity
	if pkgErr == nil && len(pkgData) > 0 {
		// INTEGRITY GATE (audit C2-I-03). The bytes are about to be installed
		// as root by a package manager whose own verification is disabled
		// (--allow-untrusted / --force-*), so this is the last point at which
		// "are these the bytes the release published?" can be answered. A
		// mismatch or a structurally impossible package stops the deploy here;
		// unverifiable bytes are logged, marked in the UI, and fatal when
		// TOLLGATE_REQUIRE_PACKAGE_DIGEST=1.
		v, err := checkPackageBytes(job, pkgLaptopURL, pkgExtension, pkgData)
		integrity = v
		if err != nil {
			// Terminal failure of a deploy that already committed the STA
			// config in step 5, so it leaves through the shared exit: the
			// pre-deploy wireless snapshot is restored BEFORE the job fails
			// (radios usable for a re-scan instead of committed to an uplink
			// the dead deploy never used), and the error states the restore
			// only when one actually ran. The detail names which half of the
			// download the bytes came from — the router-side gate below has
			// its own site and its own detail (card t_3fe64c6e).
			jobFailAfterRestore(job, client, staCommitted, 6,
				"laptop-side package integrity check failed", err.Error())
			return
		}
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
				// Same integrity gate as the laptop path, applied to the bytes
				// the ROUTER fetched: hash the file on the router and compare
				// with the published digest (pkgverify.go).
				v, err := checkRouterFileDigest(job, client, candURL, pkgExtension, "/tmp/tollgate-wrt"+pkgExtension)
				if err != nil {
					// Same terminal-failure exit as the laptop-side gate above:
					// by now step 5 has committed and reloaded the STA config,
					// so the snapshot is restored before the job fails (card
					// t_3fe64c6e).
					jobFailAfterRestore(job, client, staCommitted, 6,
						"router-side package integrity check failed", err.Error())
					return
				}
				integrity = v
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
	//
	// This is a step-6 terminal failure like any other, so it restores the
	// pre-deploy wireless snapshot first — see refuseMissingRequestedRelease.
	if refuseMissingRequestedRelease(job, client, staCommitted, pkgOnRouter, feedReleaseTag, fallbackRefusal) {
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
			// A jobFail, not setStep(6,"error") + return: the latter left
			// job.Status "running" forever, so the wizard spun with no error
			// shown (see jobFail). It is also a terminal failure after step 5,
			// so it restores the wireless snapshot like every other one.
			jobFailAfterRestore(job, client, staCommitted, 6,
				"opkg refused to downgrade tollgate-wrt",
				"opkg refused to downgrade the package: the router still runs its PREVIOUS tollgate-wrt binary with the new config. Install a package whose version opkg accepts, or remove the installed tollgate-wrt first (opkg remove tollgate-wrt) and re-run.")
			return
		}
		// apk prints failures to stdout; the pipeline above hides the exit code,
		// and a failed UPGRADE leaves the OLD binary in place — so the
		// binary-existence check below would pass while the router still runs
		// the previous package. Fail loudly on apk's error signatures.
		if pkgMgr == "apk" && apkInstallFailed(installOut) {
			job.addLog("ERROR: apk reported an installation failure: " + truncate(installOut, 200))
			jobFailAfterRestore(job, client, staCommitted, 6,
				"tollgate-wrt install failed (apk error)",
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
					jobFailAfterRestore(job, client, staCommitted, 6, "tollgate-wrt version mismatch", fatal)
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
			// Two independent verdicts meet here: C2-I-02's version verdict
			// ("did the package that landed match the release the supplying
			// source names?" — installStatus) and C2-I-03's integrity verdict
			// (were the bytes checked against a published digest? — integrity).
			// The step is GREEN only when both passed; anything else renders
			// "warn" with the reason in the detail, so neither a downgrade nor
			// unchecked bytes can look like an unqualified success.
			detail := installStepDetail(build, pkgMgr, pkgSourceLabel(routerArch, pkgExtension, pkgSourceURL))
			if installStatus == "warn" {
				detail = "NOT THE REQUESTED RELEASE (" + feedReleaseTag + ") — " + detail
			}
			job.setStep(6, installStepStatusFor(installStatus, integrity), detail+integrity.suffix())
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
			// is left in its pre-deploy state. The refusal path above does
			// the same: no terminal failure in this step may skip it.
			jobFailAfterRestore(job, client, staCommitted, 6,
				"tollgate-wrt install failed",
				"Package installation failed")
			return
		}
		// This path installed from the ROUTER's own configured package feeds —
		// not the FreedomTechFeed release asset — so the provenance label says
		// so explicitly rather than reusing "feed" for both meanings. The same
		// "is it the requested release?" rule applies: a router-feed package
		// that is not the requested one must not render as a plain success —
		// AND these bytes never passed through this installer, so nothing about
		// them was checked: the step also renders "warn" with an explicit
		// NOT VERIFIED suffix rather than a green "done" with no suffix at all
		// (cold cross-family review 2026-09-23, finding 1; C2-I-03).
		if pkgVer := readInstalledPkgVersion(client); pkgVer != "" {
			job.addLog("Installed tollgate-wrt package version: " + pkgVer)
			if _, warn := pkgVersionVerdict(pkgVer, "", routerArch, pkgExtension); warn != "" {
				job.addLog("WARNING: " + warn)
				installStatus = "warn"
			}
		}
		build := reportInstalledBuild(job, client)
		feedVerdict := routerFeedInstallVerdict()
		job.addLog("WARNING: " + feedVerdict.Detail)
		detail := installStepDetail(build, pkgMgr, pkgSourceRouterFeed)
		if installStatus == "warn" {
			detail = "NOT THE REQUESTED RELEASE (" + feedReleaseTag + ") — " + detail
		}
		job.setStep(6, installStepStatusFor(installStatus, feedVerdict), detail+feedVerdict.suffix())
	}

	// The .ipk now ships gonuts v0.11.1 with all keyset/multimint/existing-wallet
	// fixes built in — no binary replacement needed.
	time.Sleep(500 * time.Millisecond)

	// Step 7: Brand as TollGate — hostname, SSIDs, DNS, nodogsplash config.
	//
	// ONE device code names all three identifiers (hostname, captive SSID,
	// private SSID). It is resolved ON THE ROUTER — which reads the store
	// (/etc/config/tollgate) the module's uci-defaults wrote, and only mints
	// when nothing on the router carries a code — so a redeploy of an existing
	// router keeps the name it already answers to instead of re-minting one.
	// This used to mint a fresh code here on EVERY deploy (crypto/rand), which
	// is why the bench box showed hostname=tollgate-OQ3Q with an open SSID of
	// tollgate-0GLK: the module minted, then the installer minted again.
	job.setStep(7, "running", "")

	idOut := sshRun(client, deviceIdentityScript)
	id := parseDeviceIdentity(idOut)
	if id.Code == "" {
		// The router-side resolver returned nothing usable (no uci, no
		// /dev/urandom). Brand anyway — the deploy is far past the point of no
		// return — and say so, because a locally minted code could not be
		// stored and the next install will therefore re-name the router.
		id = fallbackDeviceIdentity()
		job.addLog("WARNING: could not resolve a device code on the router (" + truncate(strings.TrimSpace(idOut), 80) + ") — minted " + id.Code + " locally; it is NOT stored on the router")
	} else {
		job.addLog("Device code " + id.Code + " (" + id.Source + "): hostname=" + id.Hostname + ", captive SSID=" + id.SSID + ", private SSID=" + id.PrivateSSID)
	}

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

	brandOut := sshRun(client, strings.Join(brandingCommands(id, routerIP), " && "))
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
		job.addLog("Branded: code=" + id.Code + ", hostname=" + id.Hostname + ", SSID=" + id.SSID + ", private SSID=" + id.PrivateSSID + ", DNS=tollgate.lan")
		job.setStep(7, "done", "device code "+id.Code+" → hostname+SSIDs+DNS+nodogsplash")
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
		jobFailAfterRestore(job, client, staCommitted, 8, "captive portal assets missing",
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
		jobFailAfterRestore(job, client, staCommitted, 10,
			"tollgate-wrt not installed", "tollgate-wrt init script missing — package install failed")
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
	//
	// Same contract as step 5: a nil client (the move lost the router) fails
	// STEP 10 through jobFail and stops here, with no nil dereference.
	nc, ok := adoptRelocatedClient(job, fixSubnetCollisions(job, client, req.IP, req.Password), 10)
	if !ok {
		return
	}
	client = nc

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
	// A DEAD TRANSPORT IS NOT A SERVICE PROBLEM. Before blaming :2121, prove the
	// session we are asking is still alive: on a real router this deploy lost the
	// connection when the router was re-addressed mid-run, then spent 2 minutes
	// reporting "service not listening" against a closed socket. See
	// sshTransportAlive.
	if err := sshTransportAlive(client); err != nil {
		job.addLog("Lost the SSH transport to the router before the health check: " + err.Error())
		failTransportLost(job, 11, req.IP, err)
		return
	}
	for attempt := 1; attempt <= healthAttempts; attempt++ {
		time.Sleep(3 * time.Second)
		if err := sshTransportAlive(client); err != nil {
			job.addLog(fmt.Sprintf("Health check attempt %d/%d: SSH TRANSPORT LOST — %v", attempt, healthAttempts, err))
			failTransportLost(job, 11, req.IP, err)
			return
		}
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
		// new config, leaving radio0 stuck in STA mode). Both branches below
		// are terminal failures of this step, so both go through the shared
		// exit that restores the snapshot.
		if listening {
			jobFailAfterRestore(job, client, staCommitted, 11, "tollgate API up but no advertisement",
				"TollGate API is UP on :2121 but returned no pricing advertisement — the merchant is degraded (mint/wallet not ready), not down.\n"+diag)
		} else {
			jobFailAfterRestore(job, client, staCommitted, 11, "tollgate service not listening on :2121",
				"The tollgate-wrt service is NOT listening on :2121 (crash-looping or still initializing).\n"+diag)
		}
		return
	}

	// The health check was the last gate, so the install has succeeded: this is
	// the point at which re-keying the router can no longer strand the operator.
	// Apply the credential deferred at step 4, and only now arm it for the
	// one-shot view.
	if !finalizeRootCredential(job, func(cmd string) string {
		return sshRun(client, cmd)
	}, func(pw string) bool {
		return proveRootPassword(req.IP, pw)
	}) {
		return
	}

	job.mu.Lock()
	job.Status = "done"
	job.mu.Unlock()
	job.addLog("TollGate deployment complete!")
}

// wirelessRollback is the wireless-restore side effect of the deploy's terminal
// failure paths, indirected so the fail-loud refusal path can be driven in a
// test without a router. This package has no SSH seam (rollbackWireless takes a
// live *ssh.Client), so the CALL is what a unit test can pin; the end-to-end
// evidence for the same path is the fixture-router harness in
// ~/tollgate-artifacts/pre-release-security/harness/run-refusal-rollback.sh,
// which asserts the rollback command reached the router AND that the fixture's
// /etc/config/wireless is back to its pre-deploy bytes.
var wirelessRollback = func(client *ssh.Client) { rollbackWireless(client) }

// restoreWirelessOnFailure restores the pre-deploy /etc/config/wireless snapshot
// when THIS run committed wireless config (deploy step 5, STA mode) and reports
// whether it did. It is the single place that decision is made, so no terminal
// failure after step 5 can forget it:
//
//   - steps 6 and 11 run AFTER step 5 committed and reloaded the STA config for
//     WAN-over-WiFi, so leaving the radios in STA mode after a failure is a dead
//     end: the wizard cannot re-scan for an upstream SSID while the radio hosts
//     an STA iface, which means a physical visit to the router. The pre-#40 code
//     path restored the snapshot for exactly this reason ("so the radios are
//     usable for re-scanning").
//   - with no STA configured (WAN mode, or a run that failed before step 5) no
//     snapshot exists, so there is nothing to restore and nothing to claim: the
//     rollback is skipped and the log stays truthful.
//
// rollbackWireless is itself snapshot-guarded on the router ([ -f
// /tmp/wireless.pre-tollgate ] && ...), so an absent or stale snapshot is a
// no-op there as well.
func restoreWirelessOnFailure(job *Job, client *ssh.Client, staCommitted bool) bool {
	if !staCommitted {
		return false
	}
	job.addLog("Rolling back wireless config to the pre-deploy snapshot...")
	wirelessRollback(client)
	return true
}

// restoreWirelessFromSTACommit is the same restore for the deploy step-5 failure
// paths, which run INSIDE configureSTA: there a commit may already have happened
// while runDeployment's staCommitted is still false (it is only set once
// configureSTA returns true), so the staCommitted gate above cannot fire yet and
// the caller states the commit itself.
//
// Only call it where the STA commit really ran (attemptSTA reported STA_CFG_OK,
// or the failing path runs after a successful association) — an unconditional
// restore would also write a STALE /tmp/wireless.pre-tollgate left by an earlier
// run over the current config.
func restoreWirelessFromSTACommit(job *Job, client *ssh.Client) bool {
	return restoreWirelessOnFailure(job, client, true)
}

// jobFailAfterRestore is the terminal-failure exit for every deploy path that
// runs AFTER step 5. It restores the pre-deploy wireless snapshot (through the
// same staCommitted gate as the rest, so the decision is still made in exactly
// one place), THEN fails the job, and appends the restore to the operator-facing
// error only when it actually ran — the log can neither claim a restore that did
// not happen nor hide one that did.
//
// Every post-step-5 terminal failure goes through it (see
// TestDeployFailureSitesRestoreWireless, which pins the per-site coverage): a
// deploy that fails at step 6, 8, 10 or 11 leaves the router with its radios
// usable for a re-run instead of committed to an uplink the failed deploy never
// used.
func jobFailAfterRestore(job *Job, client *ssh.Client, staCommitted bool, step int, stepDetail, jobErr string) {
	if restoreWirelessOnFailure(job, client, staCommitted) {
		jobErr += " — pre-deploy wireless config restored"
	}
	jobFail(job, step, stepDetail, jobErr)
}

// refuseMissingRequestedRelease is deploy step 6's fail-loud gate: the requested
// release could not be fetched (pkgOnRouter == false) and a refusal was recorded
// against the only other download candidate — a KNOWN OLDER package the operator
// has not opted into (C2-I-02). It logs the refusal, restores the pre-deploy
// wireless snapshot (see restoreWirelessOnFailure), fails the job naming the
// requested tag, and reports that the caller MUST stop.
//
// It returns false — and touches nothing — when a package did land, or when no
// refusal was recorded, so the caller's happy path is unchanged.
//
// The refusal is a terminal failure of step 6 exactly like the feed last-resort
// failure below it, so it must leave the router in the same state: choosing to
// fail loudly must not also silently strand the radios in STA mode. On the very
// feed-outage scenario this refusal exists for, leaving STA committed meant the
// wizard could no longer re-scan for an upstream SSID and the router needed a
// physical visit — where the path this refusal replaced at least left the radios
// usable.
func refuseMissingRequestedRelease(job *Job, client *ssh.Client, staCommitted, pkgOnRouter bool, requestedTag string, refusal error) bool {
	if pkgOnRouter || refusal == nil {
		return false
	}
	job.addLog("ERROR: " + refusal.Error())
	restoreWirelessOnFailure(job, client, staCommitted)
	jobFail(job, 6,
		"requested release "+requestedTag+" unavailable — refusing to install an older package",
		refusal.Error())
	return true
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

// jobStallTimeout is how long a RUNNING job may make no observable progress
// (no log line, no step change, no progress-counter move) before the watchdog
// fails it.
//
// It is the backstop for the whole class of defect that produced rc17's
// infinite "Deploying TollGate..." spinner: a goroutine parked in an unbounded
// blocking call — an SSH command on a half-dead transport, a TCP read, a lock.
// sshRun is individually bounded now, but only a watchdog covers the call site
// nobody thought about, and the operator must never be left staring at a
// progress bar that will never move.
//
// 3 minutes is comfortably longer than the slowest legitimate gap: a 10 MiB
// package download on a slow uplink emits its "Downloading ..." line first, and
// the post-flash reboot wait logs while it polls.
var jobStallTimeout = 3 * time.Minute

// failIfStalled fails job when it is still running and has made no observable
// progress for limit, and reports whether it did so. The whole decision happens
// under ONE lock: the watchdog reads the job and then acts, so a deploy that
// finished (or already failed) in between must not be overwritten by a stale
// stall verdict — that would report a good deploy as a failure.
func failIfStalled(job *Job, limit time.Duration) bool {
	if job == nil {
		return false
	}
	job.mu.Lock()
	defer job.mu.Unlock()
	if job.Status != "running" {
		return false
	}
	if time.Since(job.lastActivity) < limit {
		return false
	}
	step := job.Step
	where := ""
	if step >= 0 && step < len(job.Steps) {
		where = job.Steps[step].Desc
		job.Steps[step].Status = "failed"
		job.Steps[step].Detail = "no progress for " + limit.String()
	}
	// Same consistency rule as failTransportLost: a job that never reached a
	// step must not render as pending/running once the job is terminal.
	for i := range job.Steps {
		if i == step {
			continue
		}
		if job.Steps[i].Status == "pending" || job.Steps[i].Status == "running" {
			job.Steps[i].Status = "skipped"
			job.Steps[i].Detail = "not reached — the installer stalled"
		}
	}
	job.Status = "failed"
	job.Error = fmt.Sprintf("the installer stopped making progress: no answer for %s at step %d (%s).\n\n"+
		"The router stopped answering mid-deploy, so the remaining steps did not run and nothing could be\n"+
		"verified. Power-cycle the router and re-run. If it stalls at the same step again, capture the\n"+
		"wizard's terminal output (and a `pkill -QUIT -f tollgate-installer` goroutine dump) and report it.",
		limit, step, where)
	return true
}

// startJobWatchdog fails job if it makes no observable progress for limit. It
// returns immediately; the watcher exits on its own once the job is terminal.
func startJobWatchdog(job *Job, limit time.Duration) {
	if job == nil || limit <= 0 {
		return
	}
	tick := limit / 10
	if tick < 250*time.Millisecond {
		tick = 250 * time.Millisecond
	}
	go func() {
		for {
			time.Sleep(tick)
			if failIfStalled(job, limit) {
				return
			}
			job.mu.Lock()
			done := job.Status != "running"
			job.mu.Unlock()
			if done {
				return
			}
		}
	}()
}

// requiredInstalledKiB is the installed footprint of the tollgate-wrt payload.
// MEASURED, not guessed: opkg itself reported "pkg tollgate-wrt needs 23850"
// when it refused the install on a 16MB-flash GL-AR300M16 (8,016 KiB free).
// Deliberately a little generous — refusing a router that might just have fitted
// costs one re-run, while installing on one that cannot fit costs the whole
// deploy (and, before PR #71, the router's root credential).
const requiredInstalledKiB = 25600

// overlaySpaceVerdict decides, from the free KiB on /overlay, whether the payload
// can be installed. free < 0 means "could not be read" — never refuse a router on
// a measurement we do not have.
func overlaySpaceVerdict(freeKiB int) (bool, string) {
	if freeKiB < 0 {
		return true, "free-space pre-flight skipped: /overlay size could not be read"
	}
	if freeKiB < requiredInstalledKiB {
		return false, fmt.Sprintf("only %d KiB free on /overlay — tollgate-wrt needs about %d KiB installed", freeKiB, requiredInstalledKiB)
	}
	return true, fmt.Sprintf("%d KiB free on /overlay (needs ~%d KiB)", freeKiB, requiredInstalledKiB)
}

// parseAvailKiB reads the Available column out of `df -k` output, or -1 when it
// cannot (no df, or an unexpected format).
func parseAvailKiB(dfOut string) int {
	for _, line := range strings.Split(dfOut, "\n") {
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		if strings.EqualFold(f[0], "Filesystem") {
			continue // header row
		}
		n, err := strconv.Atoi(f[3])
		if err != nil {
			continue // not a data row (an error line, a wrapped line)
		}
		return n
	}
	return -1
}

// preflightOverlaySpace refuses a deploy that cannot fit BEFORE step 4 touches
// the router's credential. See the call site comment in runDeployment.
func preflightOverlaySpace(job *Job, client *ssh.Client) bool {
	free := parseAvailKiB(sshRun(client, "df -k /overlay / 2>/dev/null"))
	ok, msg := overlaySpaceVerdict(free)
	if ok {
		job.addLog("Free-space pre-flight: " + msg)
		return true
	}
	job.addLog("Free-space pre-flight FAILED: " + msg)
	job.setStep(3, "failed", msg)
	jobFail(job, 3, "not enough flash for tollgate-wrt",
		"This router has "+msg+".\n\n"+
			"Nothing was changed on the router: its root password was NOT touched and no package was\n"+
			"installed.\n\n"+
			"Use a router with more flash (a NAND-class device such as a GL-MT3000 has ~200 MB free), or\n"+
			"install a compressed build if one is published for this architecture.")
	return false
}

// failTransportLost fails a deploy whose SSH transport died, naming the cause and
// leaving a step list CONSISTENT with the terminal step. A live run ended with the
// job failed at step 11 while steps 6..11 still read pending/running, so the UI
// showed a deploy that had "reached" steps it never ran.
func failTransportLost(job *Job, step int, ip string, err error) {
	if job == nil {
		return
	}
	job.mu.Lock()
	for i := range job.Steps {
		if i == step {
			continue
		}
		if job.Steps[i].Status == "pending" || job.Steps[i].Status == "running" {
			job.Steps[i].Status = "skipped"
			job.Steps[i].Detail = "not reached — the SSH connection to the router was lost"
		}
	}
	job.mu.Unlock()
	job.addLog("Deploy stopped: lost the SSH transport to " + ip + " (" + err.Error() + "). " +
		"The router is no longer answering on " + ip + " — it was re-addressed, rebooted, or the cable moved to another port.")
	jobFail(job, step, "lost the router — SSH transport died",
		"Lost the SSH connection to "+ip+" ("+err.Error()+").\n\n"+
			"The router stopped answering mid-deploy, so the remaining steps did not run and nothing could be\n"+
			"verified. Nothing further was changed on it — in particular its root credential was NOT re-keyed.\n\n"+
			"Likely causes: the deploy re-addressed the router's LAN, the router rebooted, or the cable is now in\n"+
			"a different port. Try the router's OTHER ethernet port, or find it on its new LAN address, then retry.")
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
	// If the transport is dead, every command below would return "" and the
	// block would read as nine empty labels — which is how a lost router was
	// reported as a crash-looping service with NO evidence at all. Say what
	// actually happened instead.
	if err := sshTransportAlive(client); err != nil {
		return transportLostDiagnostics(err)
	}
	parts := []string{
		fmt.Sprintf("listening=%v ad=%q", listening, truncate(body, 120)),
		diagField("service", sshRun(client, "/etc/init.d/tollgate-wrt status 2>&1 | head -2"), 200),
		diagField("proc", sshRun(client, "pgrep -af tollgate-wrt 2>/dev/null | head -1"), 200),
		diagField("date", sshRun(client, "date -u 2>/dev/null"), 80),
		diagField("mints", sshRun(client, "jq -r '.accepted_mints[]?.url' /etc/tollgate/config.json 2>/dev/null | tr '\\n' ' '"), 200),
		diagField("internet", sshRun(client, "(wget -q -T4 -O /dev/null https://1.1.1.1 2>/dev/null && echo online) || echo 'no internet'"), 40),
		diagField("dns", sshRun(client, "nslookup github.com 2>&1 | tail -2"), 160),
		diagField("log", sshRun(client, "logread 2>/dev/null | grep -iE 'tollgate|merchant|mint|wallet' | tail -12"), 1500),
		diagField("debug", sshRun(client, "tail -15 /tmp/tollgate-debug.log 2>/dev/null"), 1500),
	}
	return strings.Join(parts, "\n")
}

// diagField renders one diagnostics label. An empty command result is printed
// as an explicit "unavailable" — never as a bare label, which reads as "the
// router said nothing" when it actually means "we could not ask".
func diagField(label, out string, limit int) string {
	out = strings.TrimSpace(out)
	if out == "" {
		out = "unavailable (the command returned nothing)"
	}
	return label + ": " + truncate(out, limit)
}

// transportLostDiagnostics is the diagnostics block for a DEAD SSH session. It
// is pure (no router) so the "never an empty label" contract is unit-tested.
func transportLostDiagnostics(err error) string {
	return "transport: SSH session LOST — " + err.Error() + "\n" +
		"The router stopped answering on the address this deploy dialled, so the\n" +
		"service state above could not be observed AT ALL. This is a lost router,\n" +
		"not necessarily a broken service: the deploy re-addressed its LAN, the\n" +
		"router rebooted, or the cable/port it is plugged into changed.\n" +
		"Next: find the router (try its OTHER ethernet port, or its new LAN\n" +
		"address), then retry. The root credential was NOT changed by this deploy."
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

// defaultRoutePresent reports whether a route table has a default route.
//
// It greps the table itself because `ip route show default` is NOT filtered by
// OpenWrt's busybox ip: it returns the whole table, so `[ -n "$(ip route show
// default)" ]` — and the installer's old routeOK check — is true on any router
// that has any route at all. The read-only diagnostic script hit the same trap
// and reported a "gateway" literally named br-lan.
func defaultRoutePresent(ipRouteOutput string) bool {
	for _, line := range strings.Split(ipRouteOutput, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "default") {
			return true
		}
	}
	return false
}

var ipv4Re = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)

// dnsAnswerOK reports whether an nslookup output is a real answer. A REFUSED /
// NXDOMAIN / timed-out answer, an empty answer, or dnsmasq answering for itself
// (127.0.0.1) is NOT resolution — the old check accepted any line containing
// "Address", which is the one line every refusal also carries.
func dnsAnswerOK(out string) bool {
	s := strings.ToLower(out)
	if strings.TrimSpace(s) == "" {
		return false
	}
	for _, bad := range []string{"can't find", "cant find", "refused", "nxdomain", "timed out", "no servers could be reached"} {
		if strings.Contains(s, bad) {
			return false
		}
	}
	for _, ip := range ipv4Re.FindAllString(s, -1) {
		if !strings.HasPrefix(ip, "127.") && ip != "0.0.0.0" {
			return true
		}
	}
	return false
}

// dnsResultLabel returns a compact description of a resolver probe outcome.
// When the probe did not resolve, it extracts the reason word the operator
// needs (REFUSED, NXDOMAIN, timed out, ...) so the diagnostic answers
// "how each failed".
func dnsResultLabel(out string) string {
	if dnsAnswerOK(out) {
		return "ok"
	}
	s := strings.ToLower(strings.TrimSpace(strings.ReplaceAll(out, "\n", " ")))
	for _, reason := range []string{"refused", "nxdomain", "no servers could be reached", "timed out", "can't find"} {
		if strings.Contains(s, reason) {
			return reason
		}
	}
	if s == "" {
		return "empty"
	}
	return "fail"
}

// upstreamUsable reports whether the router has a usable internet path: a
// reachable first hop (pingOK) AND at least one working resolver path. The
// resolver path can be the upstream's own resolvers (dnsOK) or a public fallback
// (publicOK). This prevents a deploy-blocking false negative when the AP hands
// out a DNS resolver it does not actually serve, but 1.1.1.1/9.9.9.9 answer.
func upstreamUsable(pingOK, dnsOK, publicOK bool) bool {
	return pingOK && (dnsOK || publicOK)
}

// upstreamVerdict names which side owns an upstream failure, so the operator
// does not have to work it out from a wall of probe output. Pure, so the
// classification is unit-tested against every real-world case seen in the field.
func upstreamVerdict(pingOK, dnsOK, defaultRoute, publicDNSOK bool) string {
	switch {
	case !defaultRoute:
		return "verdict: no default route — the router is not on an upstream. " +
			"Fix the uplink (re-enter the wifi credentials, or attach a WAN cable), then retry."
	case !pingOK:
		return "verdict: a default route exists but the first hop does not answer — " +
			"association/AP problem, not DNS. The router side (or the AP) is dropping traffic."
	case dnsOK:
		return "verdict: routing and name resolution both work — the upstream is usable."
	case publicDNSOK:
		return "verdict: the upstream handed out resolvers it does not serve (a public resolver answers) — " +
			"fixable on the router: point dnsmasq at 1.1.1.1, or let the installer apply that fallback."
	default:
		return "verdict: the upstream network blocks DNS — nothing resolves, not even 1.1.1.1/9.9.9.9. " +
			"Use a network whose DNS works; nothing to fix on the router."
	}
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
//
// When the upstream's own resolvers do not answer but a public resolver does
// (a guest network that hands out resolvers it does not serve), the fix is one
// uci command — so the installer applies it and re-tests, instead of failing an
// install that can succeed. Nothing is applied when public DNS is blocked too.
func upstreamOnline(client *ssh.Client) (bool, string) {
	if lanIP := repairLanDNS(client); lanIP != "" {
		sshRun(client, "logger -t tollgate-installer 'dns entries repaired for "+lanIP+"' 2>/dev/null; true")
	}
	sshRun(client, "/etc/init.d/dnsmasq restart 2>/dev/null; true")
	var pingOK, dnsOK, routeOK, publicOK bool
	fallback := ""
	resolverResults := []string{"upstream-resolver: probing via nslookup github.com"}
	for i := 0; i < 8; i++ {
		pout := sshRun(client, "ping -c1 -W3 1.1.1.1 2>&1 | tail -2")
		pingOK = strings.Contains(pout, "1 received") || strings.Contains(pout, "1 packets received")
		routeOK = defaultRoutePresent(sshRun(client, "ip route show 2>/dev/null"))
		dout := sshRun(client, "nslookup github.com 2>&1 | tail -3")
		dnsOK = dnsAnswerOK(dout)
		if pingOK && dnsOK {
			break
		}
		time.Sleep(2 * time.Second)
	}
	resolverResults[0] = "upstream-resolver: " + dnsResultLabel(sshRun(client, "nslookup github.com 2>&1 | tail -3"))
	// Resolvers handed out by the upstream are dead; is DNS blocked outright?
	if pingOK && !dnsOK {
		for _, pub := range []string{"1.1.1.1", "9.9.9.9"} {
			pout := sshRun(client, "nslookup github.com "+pub+" 2>&1 | tail -3")
			ok := dnsAnswerOK(pout)
			resolverResults = append(resolverResults, fmt.Sprintf("public %s: %s", pub, dnsResultLabel(pout)))
			if !ok {
				continue
			}
			publicOK = true
			sshRun(client, "uci -q del_list dhcp.@dnsmasq[0].server='"+pub+"' 2>/dev/null; "+
				"uci -q add_list dhcp.@dnsmasq[0].server='"+pub+"' && uci commit dhcp && "+
				"/etc/init.d/dnsmasq restart 2>/dev/null; sleep 3")
			if dnsAnswerOK(sshRun(client, "nslookup github.com 2>&1 | tail -3")) {
				dnsOK = true
				fallback = "resolver-fallback: dnsmasq pointed at " + pub + " (the upstream's own resolvers do not answer)"
			}
			break
		}
	}
	parts := []string{
		upstreamVerdict(pingOK, dnsOK, routeOK, publicOK),
		fmt.Sprintf("ping(1.1.1.1)=%v dns(github.com)=%v default-route=%v public-dns=%v", pingOK, dnsOK, routeOK, publicOK),
		"resolvers:\n  " + strings.Join(resolverResults, "\n  "),
	}
	if fallback != "" {
		parts = append(parts, fallback)
	}
	for _, p := range []string{
		"route: " + truncate(sshRun(client, "ip route show 2>/dev/null | head -5 | tr '\\n' ' '"), 300),
		"uplink: " + truncate(sshRun(client, "for i in wwan wan; do s=$(ubus call network.interface.$i status 2>/dev/null | grep -E '\"up\"|address' | head -3 | tr '\\n' ' '); [ -n \"$s\" ] && echo \"$i: $s\"; done"), 300),
		"resolv: " + truncate(sshRun(client, "grep -v '^#' /etc/resolv.conf 2>/dev/null | head -4 | tr '\\n' ' '"), 200),
		"resolv.auto: " + truncate(sshRun(client, "grep -v '^#' /tmp/resolv.conf.d/resolv.conf.auto 2>/dev/null | head -4 | tr '\\n' ' '"), 200),
		"dnsmasq: " + truncate(sshRun(client, "pgrep -f '[d]nsmasq' >/dev/null && echo running || echo 'not running'"), 40),
		"dnsmasq-log: " + truncate(sshRun(client, "logread 2>/dev/null | grep -i dnsmasq | tail -3 | tr '\\n' ' '"), 300),
		"dnsmasq-address: " + truncate(sshRun(client, "grep -h '^address=' /var/etc/dnsmasq.conf.* 2>/dev/null | head -3 | tr '\\n' ' '"), 200),
	} {
		parts = append(parts, p)
	}
	return upstreamUsable(pingOK, dnsOK, publicOK), strings.Join(parts, "\n")
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
// The script snapshots /etc/config/wireless AND network.wwan's pre-deploy state
// to /tmp for rollback (see rollbackWireless) and performs a single commit pair;
// the caller applies the whole change set with ONE `wifi reload`.
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
	// CRITICAL (line termination): the carriers block MUST end with a newline.
	// The selectors below are separate shell statements; without it the block's
	// last line and the selector's first line are ONE word, so
	// `sta_key=$(...)target='radio0'` assigns sta_key the literal
	// "...target=radio0" and leaves `target` UNSET — the forced-radio selector
	// then probes `uci -q get wireless.` (an empty section, a hard error on a
	// real uci: it exits non-zero even with -q), hits its own NO_RADIO guard and
	// exits before writing anything, so every STA attempt fails and step 5 dies
	// with a misleading "check SSID and password".
	//
	// Both carrier shapes (this branch's octal/printf and the base64 carrier it
	// replaced) end with "\n" for exactly that reason: the base64 fix is pinned
	// by TestStaSetupScriptForcedRadioRunsUnderAStrictUci, this branch's by
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
# Snapshot network.wwan's PRE-DEPLOY state. The commit pair below writes that
# section, and rollbackWireless must be able to put /etc/config/network back too
# — so record whether it already existed (EXISTED) or this run creates it
# (ABSENT), and for a pre-existing section the one option written below (proto).
if uci -q show network.wwan >/dev/null 2>&1; then
	echo EXISTED > /tmp/network.wwan.pre-tollgate
	uci -q get network.wwan.proto > /tmp/network.wwan.proto.pre-tollgate 2>/dev/null || : > /tmp/network.wwan.proto.pre-tollgate
else
	echo ABSENT > /tmp/network.wwan.pre-tollgate
fi &&
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

// rollbackWirelessCmd is the router-side restore every failure path runs, as ONE
// shell command. It is a const so a unit test can pin its content — the function
// below needs a live *ssh.Client, so the command text is the only thing testable
// in-process (the fixture-router harness runs the real thing end-to-end).
//
// It undoes BOTH commits the STA setup made:
//
//  1. /etc/config/wireless is restored from the snapshot taken before the STA
//     changes and reloaded. Snapshot-guarded (`[ -f ... ]`), so an absent or
//     stale snapshot is a no-op on the router as well.
//  2. network.wwan — written by the same commit pair (the STA script's
//     `uci set network.wwan=interface` + `.proto='dhcp'`) — is put back: deleted
//     when this run created it, or its pre-existing proto restored when the
//     router already had that section. Without this the network config keeps an
//     interface pointing at an iface the rollback just removed, and "left as it
//     was found" was false (deploy-failure-rollback.md).
//
// It is deliberately NOT a /etc/config/network revert: fixSubnetCollisions
// relocates br-lan/br-private on purpose when they collide with the upstream,
// and that relocation is left in place (see docs/deploy-failure-rollback.md).
const rollbackWirelessCmd = `[ -f /tmp/wireless.pre-tollgate ] && cp /tmp/wireless.pre-tollgate /etc/config/wireless && uci commit wireless && (wifi reload 2>/dev/null || wifi 2>/dev/null); ` +
	`if [ -f /tmp/network.wwan.pre-tollgate ]; then ` +
	`if [ "$(cat /tmp/network.wwan.pre-tollgate)" = "ABSENT" ]; then ` +
	`uci -q show network.wwan >/dev/null 2>&1 && { uci -q delete network.wwan; uci commit network; }; ` +
	`else ` +
	`p=$(cat /tmp/network.wwan.proto.pre-tollgate 2>/dev/null); ` +
	`if [ -n "$p" ]; then uci set network.wwan.proto="$p"; else uci -q delete network.wwan.proto; fi; ` +
	`uci commit network; ` +
	`fi; ` +
	`fi; true`

// rollbackWireless restores the /etc/config/wireless snapshot taken before STA
// changes and removes/restores the network.wwan section the same commit wrote,
// reloading wifi, returning the router to its pre-deploy wireless state. Safe to
// call when no snapshot exists (no-op).
func rollbackWireless(client *ssh.Client) {
	sshRun(client, rollbackWirelessCmd)
}

// reconnectSSH retries sshConnect (radios may be restarting after a wifi
// reload, so the first attempts can time out). Falls back to empty-password
// auth like the initial connect.
//
// It reports nothing itself: a host-key refusal is recorded against ip by the
// connect helper (hostkey.go) and each caller hands it to the operator with
// sshConnectFailureMessage, so a refused key is never reported as "the router is
// not answering" (RISK item of the #41 review).
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
//
// leftCommitted reports the one case it cannot clean up after: the STA config was
// committed (STA_CFG_OK) and the router then stopped answering SSH altogether, so
// there was no session left to roll it back through. The caller must say that
// instead of claiming a rollback that never ran, and it gets one more chance to
// restore once the router answers again.
func attemptSTA(ip, password, ssid, wifiPass, radio string) (client *ssh.Client, ok bool, leftCommitted bool) {
	client = sshConnect(ip, password)
	if client == nil && password != "" {
		client = sshConnect(ip, "")
	}
	// As in reconnectSSH: nothing is reported here. A host-key refusal stays in
	// the registry for the caller's sshConnectFailureMessage, which is what keeps
	// the refusal text uniform across every connect site.
	if client == nil {
		return nil, false, false
	}
	out := sshRun(client, staSetupScriptFor(ssid, wifiPass, "", radio))
	if !strings.Contains(out, "STA_CFG_OK") {
		rollbackWireless(client)
		client.Close()
		return nil, false, false
	}
	sshRun(client, "wifi reload 2>/dev/null || wifi 2>/dev/null || true")
	client.Close()

	c := reconnectSSH(ip, password, 3, 5*time.Second)
	if c == nil {
		if r := reconnectSSH(ip, password, 2, 8*time.Second); r != nil {
			rollbackWireless(r)
			r.Close()
			return nil, false, false
		}
		// Committed, and there is no session to undo it in. Report it — the
		// wireless config is very likely still committed on the router.
		return nil, false, true
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
		return nil, false, false
	}
	return c, true, false
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

// sshConnectionSourceIP extracts the client IP from `echo $SSH_CONNECTION`
// output — the connection's source address as the ROUTER sees it.
//
// Both layouts the wizard can meet put the client IP in field 0:
// OpenSSH emits "clientip serverip clientport serverport", and OpenWrt 24.10
// ships dropbear, whose svr-chansession.c make_connection_string emits
// "remoteip remoteport localip localport". The layout differs after field 0, so
// only the first field is read. (The review's INFO item: this comment used to
// document the OpenSSH contract as if it were dropbear's.)
func sshConnectionSourceIP(out string) string {
	f := strings.Fields(strings.TrimSpace(out))
	if len(f) == 0 || net.ParseIP(f[0]) == nil {
		return ""
	}
	return f[0]
}

func ipInCIDRS(ip, cidrS string) bool {
	pip := net.ParseIP(strings.TrimSpace(ip))
	_, n, err := net.ParseCIDR(strings.TrimSpace(cidrS))
	return err == nil && pip != nil && n.Contains(pip)
}

// ipInDHCPLeases matches field 3 of a /tmp/dhcp.leases line
// ("<expiry> <mac> <ip> <hostname> <clientid>").
func ipInDHCPLeases(ip, leases string) bool {
	for _, line := range strings.Split(leases, "\n") {
		f := strings.Fields(line)
		if len(f) >= 3 && f[2] == strings.TrimSpace(ip) {
			return true
		}
	}
	return false
}

// relocationIsSafe reports whether the deploy SSH connection will survive
// moving lanCIDR to a new subnet: the connection's source IP must sit inside
// the moving subnet AND be a DHCP client of this router (such a client
// renews onto the new subnet and follows the router). A static management
// host, a WAN-side laptop, or a VM-host bridge address does not follow —
// moving anyway severs the only management path mid-deploy (issue #51).
func relocationIsSafe(srcIP, lanCIDR, leases string) bool {
	return ipInCIDRS(srcIP, lanCIDR) && ipInDHCPLeases(srcIP, leases)
}

// moveLocalSubnetCommands builds the uci chain that moves netSection (and its
// DHCP pool) to newIP, commits it and applies it by ifup-ing ONLY that
// interface. A full "/etc/init.d/network restart" has been observed to leave
// network.lan without an address (br-lan up but no IPv4) — which drops the
// operator's LAN access — and is otherwise unnecessarily disruptive, so it is
// NEVER part of a relocation: the fallback for a router without ifup is a
// NON-DISRUPTIVE `ubus call network reload` (what /etc/init.d/network reload
// runs), which re-reads /etc/config/network and applies the moved section
// without tearing down the interfaces the move did not touch.
//
// It matters most for a NON-management bridge: on 2026-10-05 the restart
// fallback fired while br-private was being moved and took br-lan's IPv4 down
// with it, so the deploy lost the router on the management address too — see
// moveLocalSubnet.
//
// It is a separate pure function because the SHAPE of this chain is the whole
// point: every command is spliced into ONE `&&` chain, so a single command that
// exits non-zero silently aborts the move.
//
// The gateway delete is exactly that command. `uci -q delete` exits 1 when the
// option does not exist — `-q` silences the message, not the exit code — and
// nothing in this repo ever sets network.lan.gateway, so on the fresh router
// this installer exists for, the option is ABSENT. Unguarded, the chain died at
// command 3 of 8: no `uci commit network`, no `ifup`, no move — while the caller
// logged a successful relocation, the reconnect "fallback" succeeded on the
// original (unchanged) address, and a LATER step's `uci commit network` (see
// runDeployment step 7) committed the staged delta out-of-band, moving the
// router at some unattended future reboot. Hence `|| true`: the file's own idiom
// for a best-effort uci operation (see the service restarts in runDeployment).
//
// It is section-scoped, not lan-only: a stale gateway from the old subnet is
// just as unreachable from the new one on br-private (network.private.gateway),
// which the lan-only version left behind.
func moveLocalSubnetCommands(netSection, dhcpSection, newIP string) []string {
	cmds := []string{
		"uci set network." + netSection + ".ipaddr='" + newIP + "'",
		"uci set network." + netSection + ".netmask='255.255.255.0'",
		// A stale gateway from the old subnet is unreachable from the new one
		// and leaves the router with a default route to nowhere. The `|| true`
		// is load-bearing — see the function comment.
		"uci -q delete network." + netSection + ".gateway || true",
	}
	if dhcpSection != "" {
		cmds = append(cmds,
			"uci -q set dhcp."+dhcpSection+".start='100'",
			"uci -q set dhcp."+dhcpSection+".limit='150'")
	}
	return append(cmds,
		"uci commit network",
		"uci -q commit dhcp",
		// Apply by ifup-ing ONLY the moved section; if this router has no ifup,
		// reload the config non-disruptively. NOT "/etc/init.d/network
		// restart": that command is documented above to leave network.lan with
		// no IPv4 at all, and it fires most readily when ifup exits non-zero —
		// which is precisely the case for a bridge that is not the management
		// path and never needed the management path touched.
		"/sbin/ifup "+netSection+" 2>/dev/null || ubus call network reload 2>/dev/null",
		"sleep 2")
}

// moveReconnect dials the router again after a relocation. It is a variable so
// the reconnect decision below — WHICH address is dialled, and which address the
// operator is told answered — can be driven end to end without a router (the
// same test-seam pattern as sshDialPort). Production always uses reconnectSSH.
var moveReconnect = reconnectSSH

// relocationManagementAlive reports whether the deploy's SSH session — which
// lives on the MANAGEMENT path (br-lan, the address the wizard dialled) — still
// answers. It is used after a move of a bridge that is NOT the management path
// (br-private): that move must leave the session alone, so the session is the
// thing to verify, on the UNCHANGED management address.
//
// Production uses sshTransportAlive: a real exec on the EXISTING session,
// bounded at 8s, which errors on a closed/half-dead transport instead of
// silently returning "" (the 2026-10-03 GL-MT3000 defect). It is a variable so
// the "the non-management move took the management path down anyway" branch is
// drivable without a router — the same test-seam pattern as moveReconnect.
var relocationManagementAlive = sshTransportAlive

// managementBridgeIfname is the local bridge that CARRIES the deploy session.
// fixSubnetCollisions marks exactly this one as management; moveLocalSubnet
// names it in operator-facing messages, because losing it is the real problem
// (a client on it cannot reach a relocated non-management subnet at all).
const managementBridgeIfname = "br-lan"

// reconnectWithProgress dials ip up to `attempts` times, logging every attempt
// BEFORE it is made, and reports the give-up explicitly.
//
// WHY (2026-10-05 operator report): this retry used to be silent, and one
// attempt can cost sshConnect's 10s dial timeout plus the delay — so a
// relocation that could not reconnect looked EXACTLY like a hung step for up to
// ~65s ("Configuring upstream connection" with no further output). A bounded
// window that announces each attempt is indistinguishable from nothing else.
func reconnectWithProgress(job *Job, ip, password string, attempts int, delay time.Duration) *ssh.Client {
	for i := 1; i <= attempts; i++ {
		job.addLog(fmt.Sprintf("Reconnecting to %s — attempt %d/%d", ip, i, attempts))
		if c := moveReconnect(ip, password, 1, delay); c != nil {
			return c
		}
	}
	job.addLog(fmt.Sprintf("No answer from %s after %d attempts", ip, attempts))
	return nil
}

// moveLocalSubnet relocates a local interface and its DHCP pool to a fresh
// random 10.x.y.0/24, commits it, applies it, and re-establishes the deploy
// session.
//
// `management` says whether the interface being moved CARRIES THE DEPLOY
// SESSION (br-lan). The two cases are NOT symmetric, and treating them as if
// they were is the 2026-10-05 defect:
//
//   - management move (br-lan): the session lives on the address that is
//     changing, so the client is closed and the router is dialled again — first
//     on the new address, then on the pre-move one (which only answers if the
//     move silently did not take effect);
//   - non-management move (br-private): the session is NOT on that bridge and
//     must not be touched or moved. The address br-private was just given is
//     unroutable from this machine BY CONSTRUCTION — it exists only inside the
//     router, and this machine's only path to the router is br-lan — so it must
//     NEVER be dialled: that dial left via the default gateway, blackholed, and
//     burned up to ~65s of silent retry (the operator's reported hang). The
//     EXISTING session is verified instead, on the unchanged management
//     address; only if it really stopped answering is that address dialled
//     again.
//
// It returns the live client AND THE ADDRESS THAT ACTUALLY ANSWERED:
//   - newIP, when a management move took effect and the router answers on its
//     new address;
//   - the pre-move `ip`, when the reconnect fell back to it — which is exactly
//     the "the move silently did not happen" signal (a staging `uci set` that
//     never reached the commit), and whose log line used to claim newIP;
//   - the UNCHANGED management `ip` for a non-management move that kept the
//     session, which is reported as its own result and never as a session move;
//   - (nil, "") when NOTHING answered, meaning there is no session left and the
//     caller must stop (see adoptRelocatedClient).
//
// The fallback dials `ip`, the address the router had BEFORE THIS move, which
// only answers if this move did not take effect. A deploy that relocates twice
// therefore must fall back to where the router is NOW, not to the original
// address the first move already killed: fixSubnetCollisions threads that
// through (its curIP).
func moveLocalSubnet(job *Job, client *ssh.Client, ip, password, ifname, netSection, dhcpSection, why string, management bool) (*ssh.Client, string) {
	newIP := randomPrivateLANIP()
	job.addLog(fmt.Sprintf("%s — moving %s to %s/24", why, ifname, newIP))

	if !management {
		// PRE-FLIGHT: record whether the management path answers BEFORE the
		// move, so a failure afterwards says which of the two it is instead of
		// blaming the bridge that moved.
		mgmtBefore := relocationManagementAlive(client)
		if mgmtBefore != nil {
			job.addLog("WARNING: the deploy session on the management address " + ip + " (" + managementBridgeIfname + ") is not answering before the " + ifname + " move (" + mgmtBefore.Error() + ") — a failure below is not necessarily this move's doing")
		}

		sshRun(client, strings.Join(moveLocalSubnetCommands(netSection, dhcpSection, newIP), " && "))

		if err := relocationManagementAlive(client); err == nil {
			// The session is exactly where it was. Report the bridge move as its
			// own result: it is NOT a session move, and the address to verify is
			// the unchanged management one.
			job.addLog(fmt.Sprintf("Moved %s to %s/24 — the deploy session stays on the management path %s (%s, unchanged, still answering); no reconnect needed",
				ifname, newIP, ip, managementBridgeIfname))
			return client, ip
		}

		// The management path went down with (or during) the move. Never point
		// the operator at the new private subnet: only the management address
		// can be reached from this machine.
		blame := " — it was answering before the move, so this move took the management bridge down with it"
		if mgmtBefore != nil {
			blame = " (it was already not answering before the move — see the WARNING above)"
		}
		job.addLog("ERROR: moving " + ifname + " to " + newIP + "/24 left the MANAGEMENT address " + ip + " (" + managementBridgeIfname + ") unreachable from this machine" + blame +
			". " + newIP + "/24 exists only inside the router, so it is NOT a recovery address from here.")
		job.setRelocationFailDetail(fmt.Sprintf(
			"A colliding local subnet was moved, but the router could not be reached from this machine afterwards, so the deploy stopped instead of continuing against a dead connection. "+
				"The bridge that moved was %s (now %s/24) — the deploy session lives on %s, not on %s, and %s did not answer afterwards%s. "+
				"A full network restart during the move can leave %s without an IPv4 address; power-cycle the router to restore %s, then re-run the wizard.",
			ifname, newIP, managementBridgeIfname, ifname, managementBridgeIfname, blame, managementBridgeIfname, managementBridgeIfname))
		closeSSHClient(client)
		nc := reconnectWithProgress(job, ip, password, 3, 5*time.Second)
		if nc == nil {
			return nil, ""
		}
		job.addLog("Reconnected to router on " + ip)
		return nc, ip
	}

	// The moved interface carries the deploy session: the old client dies the
	// moment the address changes, so close it and dial again.
	sshRun(client, strings.Join(moveLocalSubnetCommands(netSection, dhcpSection, newIP), " && "))
	closeSSHClient(client)

	nc := reconnectWithProgress(job, newIP, password, 5, 3*time.Second)
	answered := newIP
	if nc == nil {
		job.addLog("Could not reconnect on new " + ifname + " IP " + newIP + ", trying original IP " + ip + "...")
		nc = reconnectWithProgress(job, ip, password, 3, 5*time.Second)
		answered = ip
	}
	if nc == nil {
		// Returning the closed client here used to leave the deploy grinding
		// against a dead connection for minutes (issue #51). Fail fast with
		// an actionable message instead.
		job.addLog("ERROR: " + ifname + " moved to " + newIP + " but the router is unreachable from this machine — " +
			"connect a client to the router's LAN (it will get an address on " + newIP + "/24) and re-run the wizard from there")
		job.setRelocationFailDetail("A colliding local subnet was moved (" + ifname + " is now " + newIP + "/24), but the router could not be reached from this machine afterwards, so the deploy stopped instead of continuing against a dead connection. " +
			"The bridge that moved (" + ifname + ") carries the deploy session: connect a client to the router's LAN (it now hands out addresses on the new subnet) and re-run the wizard from there.")
		return nil, ""
	}
	// Report the address that ACTUALLY answered. Printing newIP unconditionally
	// (as this did) is false in the fallback case — the router never moved — and
	// after this PR the fallback-success case is precisely that signal (the
	// review's INFO item).
	job.addLog("Reconnected to router on " + answered)
	return nc, answered
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
	if client == nil {
		// Defensive, and reachable in a re-run: every query below goes through
		// sshRun, which dereferences the client. Report it as "no session" so
		// the caller fails the job (adoptRelocatedClient) instead of panicking.
		job.addLog("ERROR: subnet collision check requested with no live SSH session — nothing to check")
		return nil
	}
	// A previous attempt's failure detail must not be attributed to this run.
	job.setRelocationFailDetail("")
	upCIDR, gw := upstreamCIDR(client)
	if upCIDR == "" {
		job.addLog("WARNING: could not determine the upstream subnet — skipping collision detection")
		return client
	}
	// management is true for the bridge that CARRIES THE DEPLOY SSH SESSION
	// (br-lan: the address the wizard dialled, and the only path back to this
	// machine). A move of a bridge that is NOT the management path must never
	// relocate the session: see moveLocalSubnet.
	locals := []struct {
		ifname, netSection, dhcpSection string
		management                      bool
	}{
		{"br-lan", "lan", "lan", true},
		{"br-private", "private", "private", false},
	}
	// curIP is where the router is NOW. Each successful move moves it, so the
	// NEXT move's reconnect fallback dials an address that can actually answer
	// instead of the pre-deploy one the earlier move killed (BLOCK 2, item 3 of
	// the #52 review: br-lan moved to 10.x.y.1, then br-private's reconnect
	// failed and its fallback dialled the dead original address).
	curIP := ip
	for _, ln := range locals {
		lCIDR := localCIDR(client, ln.ifname)
		if lCIDR == "" {
			continue
		}
		if subnetsOverlap(lCIDR, upCIDR) {
			if ln.netSection == "lan" {
				src := sshConnectionSourceIP(sshRun(client, "echo $SSH_CONNECTION"))
				leases := sshRun(client, "cat /tmp/dhcp.leases 2>/dev/null")
				if !relocationIsSafe(src, lCIDR, leases) {
					job.addLog(fmt.Sprintf(
						"WARNING: %s=%s overlaps upstream %s but relocation is SKIPPED: the deploy connection (%q) is not a DHCP client of this router on that subnet and would be severed by the move. "+
							"DNS may be affected by the overlap; to relocate, re-run the wizard from a client that gets its address from the router.",
						ln.ifname, lCIDR, upCIDR, src))
					continue
				}
			}
			nc, answered := moveLocalSubnet(job, client, curIP, password, ln.ifname, ln.netSection, ln.dhcpSection,
				fmt.Sprintf("Subnet collision: %s=%s overlaps upstream %s (gw %s)", ln.ifname, lCIDR, upCIDR, gw), ln.management)
			if nc == nil {
				return nil
			}
			client = nc
			if answered != "" {
				curIP = answered
			}
		} else {
			job.addLog(fmt.Sprintf("No subnet collision (%s=%s vs upstream=%s)", ln.ifname, lCIDR, upCIDR))
		}
	}
	return client
}

// adoptRelocatedClient is the ONLY correct way for a deploy step to consume
// fixSubnetCollisions' result. A nil client means the relocation moved a local
// subnet and then lost the router from this machine: no session is left, so
// nothing after this point can run, and the job must be failed HERE.
//
// Both halves are load-bearing, and each closes one half of BLOCK 2 of the #52
// review:
//   - failing through jobFail (never `setStep(n,"failed") + return`) is what sets
//     job.Status="failed". The setStep-only form left Status "running" forever
//     and the wizard spun with no error shown — the anti-pattern jobFail's own
//     comment documents.
//   - never dereferencing the nil is what keeps the installer ALIVE: the old
//     shape wrote the nil into `client` / `*pclient`, so runDeployment's deferred
//     client.Close() dereferenced it, and (because runDeployment runs in a
//     goroutine) one panic killed the whole wizard process.
//
// Returns the live client and true to continue, or (nil, false) to stop.
func adoptRelocatedClient(job *Job, client *ssh.Client, step int) (*ssh.Client, bool) {
	if client != nil {
		return client, true
	}
	// moveLocalSubnet records the scenario-specific reason and recovery when it
	// loses the router. Falling back to the generic text only happens when
	// there was no session to begin with (a re-run of a step whose client is
	// gone), where nothing is known about which bridge moved.
	detail := job.relocationFailureDetail()
	if detail == "" {
		detail = "A colliding local subnet may have been moved, but there is no live SSH session to the router, so the deploy stopped instead of continuing against a dead connection. " +
			"Check the log for which bridge moved; if " + managementBridgeIfname + " lost its address, power-cycle the router to restore it, then re-run the wizard from there."
	}
	jobFail(job, step, "subnet relocation severed the connection — see log", detail)
	return nil, false
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
	// leftCommitted names the radio whose STA config was committed and could NOT
	// be rolled back (the router stopped answering SSH after the wifi reload). The
	// deploy must not claim "wireless config rolled back" in that case, and the
	// router is left with a committed radio until the operator re-runs.
	leftCommitted := ""
	for _, r := range ordered {
		newc, ok, stuck := attemptSTA(ip, password, ssid, wifiPass, r)
		if ok {
			live = newc
			job.addLog("WiFi STA connected on " + r)
			break
		}
		if stuck {
			leftCommitted = r
			job.addLog("STA config on " + r + " is committed but the router stopped answering SSH — could not roll it back")
		}
		job.addLog("STA on " + r + " did not associate — trying next radio")
	}
	if live == nil {
		hint := ""
		if c := reconnectSSH(ip, password, 2, 3*time.Second); c != nil {
			// The router answers again: the commit attemptSTA could not undo
			// CAN be rolled back after all, so do it before failing.
			if leftCommitted != "" {
				restoreWirelessFromSTACommit(job, c)
				job.addLog("Rolled back the committed STA config left on " + leftCommitted)
				leftCommitted = ""
			}
			hint = staFailureHint(c, ssid, band)
			c.Close()
		}
		// A connect that never happened (untrusted or changed host key) is not a
		// WiFi problem: the refusal carries the fingerprint and the exact trust
		// instruction, so it replaces the SSID/password hint when there was one.
		detail := sshConnectFailureMessage(ip, "WiFi STA connection failed for \""+ssid+"\" — check SSID and password")
		if leftCommitted == "" {
			detail += " (wireless config rolled back)"
		} else {
			detail += " (the STA config committed on " + leftCommitted + " could NOT be rolled back: the router stopped answering SSH after the wifi reload, so its radio is still committed to this uplink and cannot scan. Re-run the deploy once the router answers again)"
		}
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
	//
	// A nil client means the relocation lost the router: fail STEP 5 through
	// jobFail, hand the nil back through pclient (runDeployment's deferred
	// cleanup is nil-safe — closeSSHClient), and return FALSE so the caller
	// stops. Without this check the nil flowed into upstreamOnline ->
	// repairLanDNS -> sshRun(nil) and panicked the whole installer (BLOCK 2).
	client, ok := adoptRelocatedClient(job, fixSubnetCollisions(job, client, ip, password), 5)
	*pclient = client
	if !ok {
		return false
	}

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
			// The STA config was committed by the successful association above,
			// and runDeployment's staCommitted is only set once configureSTA
			// returns true — so the shared gate cannot fire here. Restore now:
			// a deploy that cannot use its uplink must not leave the radios
			// committed to it.
			restoreWirelessFromSTACommit(job, client)
			jobFail(job, 5, "upstream has no internet",
				"Associated to \""+ssid+"\" but the router cannot use the internet. Check whether the failing check below is routing or name resolution (DNS), and whether the upstream network actually provides internet or is a captive portal.\n"+odiag2+
					"\nThe STA configuration was rolled back, so the radios are usable for re-scanning.")
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
		return false, sshConnectFailureMessage(ip, "cannot connect to router via SSH")
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
	leftCommitted := ""
	for _, r := range ordered {
		c, ok, stuck := attemptSTA(ip, password, ssid, wifiPass, r)
		if ok {
			rollbackWireless(c)
			c.Close()
			return true, "connected to \"" + ssid + "\" on " + r
		}
		if stuck {
			leftCommitted = r
		}
	}

	hint := ""
	if c := reconnectSSH(ip, password, 2, 3*time.Second); c != nil {
		// Same one-more-chance restore as configureSTA: the router stopped
		// answering after the reload of a committed attempt, and it is back.
		if leftCommitted != "" {
			rollbackWireless(c)
			leftCommitted = ""
		}
		hint = staFailureHint(c, ssid, band)
		c.Close()
	}
	// Same uniformity as configureSTA (see above): a refused host key is not a
	// WiFi problem, and the refusal text is the only actionable one.
	msg := sshConnectFailureMessage(ip, "WiFi connection failed for \""+ssid+"\"")
	if b != "" {
		msg += " (" + b + " GHz)"
	}
	msg += " — check the SSID and password"
	if leftCommitted != "" {
		msg += " (the STA config committed on " + leftCommitted + " could NOT be rolled back: the router stopped answering SSH after the wifi reload)"
	}
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

// fallbackSuppressedError reports that the older GitHub fallback was not
// prefetched and will not be used, and why. It is the wording for every
// PRE-DOWNLOAD report (the step-6 candidate selection and the pre-stage
// report), so it may not reuse the list-level refusal: that error asserts the
// requested release "is not downloadable", which is only established once a
// download has actually failed (deploy step 6's fail-loud gate,
// refuseMissingRequestedRelease). What a pre-download reader needs to know is
// narrower and always true: no package from another release was taken, and the
// explicit opt-in is what would change that.
type fallbackSuppressedError struct {
	requestedTag string
	arch         string
	fbVersion    string
}

func (e *fallbackSuppressedError) Error() string {
	return fmt.Sprintf("the older GitHub fallback for %s (package %s, a different and OLDER release than %s) was not prefetched or used — it is installed only with the explicit opt-in (--allow-fallback or %s=1)",
		e.arch, e.fbVersion, e.requestedTag, githubFallbackEnv)
}

// fallbackSuppressedReason builds the pre-download suppression report for arch
// and its package format. It derives the report from the REQUEST (the effective
// feed tag, the arch, the fallback's package version) rather than from a failed
// download, so the same wording is true in the pre-stage report and in step 6's
// candidate-selection line — and neither can drift back into the list-level
// "is not downloadable" claim (C2-I-02 follow-up).
func fallbackSuppressedReason(arch, ext string) *fallbackSuppressedError {
	return &fallbackSuppressedError{
		requestedTag: feedReleaseTag,
		arch:         arch,
		fbVersion:    githubFallbackPkgVersion(arch, ext),
	}
}

// fallbackSelectionLogLine is step 6's candidate-selection report: which
// fallback decision was taken for this arch, in wording that is true BEFORE any
// download has been attempted. The list-level refusal returned by
// pkgCandidateURLsWithFallback must NOT be published here — it asserts the
// requested release "is not downloadable", which is only knowable once every
// candidate has failed, and on a healthy non-opted-in aarch64 deploy the feed
// asset downloads and installs cleanly right after this line. That refusal is
// published at fail time by refuseMissingRequestedRelease instead.
//
// Returns "" when there is nothing operator-worthy to say: the arch has no
// pinned fallback, so no candidate was withheld and none was added.
func fallbackSelectionLogLine(arch, ext string, candidates []string, refusal error) string {
	switch {
	case refusal != nil:
		// A pinned fallback exists and the operator did not opt in: name the
		// withheld release and the opt-in, and keep the asset URL the old
		// list-level line carried so nothing debuggable is lost.
		return "GitHub fallback NOT used: " + fallbackSuppressedReason(arch, ext).Error() +
			" (fallback asset: " + githubFallbackURL(arch, ext) + ")"
	case len(candidates) > 1:
		return "GitHub fallback allowed by explicit opt-in (installs " +
			githubFallbackPkgVersion(arch, ext) + ", not " + feedPkgVersion() + "): " + candidates[1]
	}
	return ""
}

// stageAssetURLsForArch is the arch-aware variant of stageAssetURLs used by
// the selection-time pre-stage job and the deploy PreStage step. It derives
// the package URLs for the DETECTED OpenWrt arch (tag-consistent feed primary
// plus the GitHub fallback, the latter only when the operator opted into it),
// so a non-aarch64 router is not served the wrong package.
// An empty arch (unknown, e.g. a stock router that will be flashed) falls back
// to the pinned aarch64 assets, matching the historical behaviour.
//
// The second return value is non-nil when the older GitHub fallback was
// deliberately NOT staged, and says so (with the requested release, the arch
// and the fallback's package version). Pre-staging is best-effort and must not
// fail the deploy, but it must not swallow that reason either — an install that
// fails later on a cache miss should be able to point the operator at
// --allow-fallback from the same log.
func stageAssetURLsForArch(arch string, isStockGL bool, glModel, pkgMgr string) ([]string, error) {
	urls := []string{}
	var refusal error
	pkg := func(ext string) []string {
		if arch == "" {
			if ext == ".apk" {
				return []string{tollgatePkgAPKURL}
			}
			return []string{tollgatePkgURL}
		}
		// Tag-consistent candidates, plus the older GitHub fallback ONLY when
		// this run opted into it: prefetching a different release's package
		// that the deploy will refuse to install is wasted bandwidth and a
		// provenance trap (C2-I-02).
		candidates, err := pkgCandidateURLsWithFallback(arch, ext, githubFallbackAllowed())
		if err != nil && refusal == nil {
			refusal = fallbackSuppressedReason(arch, ext)
		}
		return candidates
	}
	if isStockGL {
		if img, ok := glModelMap[glModel]; ok {
			urls = append(urls, img.URL())
		}
		// The post-flash package manager is only known after the reboot, so
		// stage both formats.
		urls = append(urls, pkg(".ipk")...)
		urls = append(urls, pkg(".apk")...)
		return urls, refusal
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
	return urls, refusal
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
	urls, refusal := stageAssetURLsForArch(arch, isStockGL, glModel, pkgMgr)
	if refusal != nil {
		// The fallback was deliberately not staged; say why here so an install
		// that fails later on a cache miss points at --allow-fallback in the
		// same log (review finding 3).
		job.addLog("PreStage: GitHub fallback NOT staged — " + refusal.Error())
	}
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
