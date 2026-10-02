package main

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// Router host-key trust for the wizard's root SSH session (pre-release audit
// finding C2-I-01).
//
// The wizard collects the router's ROOT password and then pushes and installs a
// package on that router, so "which host is this?" must be answered before any
// credential is offered. SSH verifies the host key during the handshake, which
// completes BEFORE userauth — so a refusal here means the password is never
// transmitted, which is the whole point: a same-LAN attacker that answers TCP/22
// at the router's address gets nothing.
//
// Policy (fail-safe; never a silent accept):
//
//  1. --trust-host-key SHA256:… (or TOLLGATE_TRUST_HOST_KEY, so the operator can
//     use it through the curl|bash launcher) — the operator verified this
//     fingerprint out of band. The key is then remembered in the store, IN PLACE
//     OF the host's previous entry (see rememberHostKey), so the rest of this run
//     and later runs do not need the flag again — including the re-image case,
//     where the same router comes back with a new key.
//  2. The known-hosts store — a host the operator trusted before, verified
//     against the key it presents now. A CHANGED key is refused, always.
//  3. Anything else: refuse, print the fingerprint and the exact command that
//     trusts it, and abort before authentication.
const (
	// knownHostsEnv overrides the store location (the tests use it to keep the
	// operator's own store out of the way).
	knownHostsEnv = "TOLLGATE_KNOWN_HOSTS"
	// trustHostKeyEnv carries an explicitly verified fingerprint. The launcher
	// runs the binary with its own argv, so an environment variable is the only
	// way to pass a trust decision through `bash <(curl …)` without editing it.
	trustHostKeyEnv = "TOLLGATE_TRUST_HOST_KEY"
	// defaultKnownHostsFile is the store, in OpenSSH known_hosts format, kept
	// next to the operator's other SSH state.
	defaultKnownHostsFile = ".tollgate-known-hosts"
)

// knownHostsPath returns the host-key store path, or "" when it cannot be
// determined (no home directory).
func knownHostsPath() string {
	if p := strings.TrimSpace(os.Getenv(knownHostsEnv)); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, defaultKnownHostsFile)
}

// hostKeyTrust returns the fingerprint the operator pinned for this run.
// The flag wins over the environment; both are trimmed, and "" means "nothing
// was pinned" (which is never treated as trust — see routerHostKeyCallback).
func hostKeyTrust() string {
	if v := strings.TrimSpace(*trustHostKey); v != "" {
		return v
	}
	return strings.TrimSpace(os.Getenv(trustHostKeyEnv))
}

// sameFingerprint compares two OpenSSH SHA-256 fingerprints, tolerating a
// leading "SHA256:" and surrounding whitespace. Nothing else is normalised: a
// pin only ever matches the exact key fingerprint the operator verified.
func sameFingerprint(a, b string) bool {
	norm := func(s string) string {
		s = strings.TrimSpace(s)
		s = strings.TrimPrefix(s, "SHA256:")
		return strings.TrimSpace(s)
	}
	na, nb := norm(a), norm(b)
	if na == "" || nb == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(na), []byte(nb)) == 1
}

// hostKeyTypeName maps an SSH host-key algorithm to the short type name the
// router's own console tools and ssh-keyscan use ("ed25519", "rsa", "ecdsa", …),
// so a refusal or a pin can name the key type alongside the fingerprint.
//
// WHY (2026-09-28): OpenWrt dropbear serves MULTIPLE host-key types. An operator
// verified the ed25519 key on the console (dropbearkey -y) while the installer
// had negotiated the router's RSA key; the refusal printed two unlabelled
// fingerprints and the mismatch looked like an impersonation. Naming the type
// makes "ed25519 AdXV8yV vs rsa lXMWKVxu" obvious at a glance.
func hostKeyTypeName(key ssh.PublicKey) string {
	switch key.Type() {
	case ssh.KeyAlgoED25519:
		return "ed25519"
	case ssh.KeyAlgoRSA, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSASHA512:
		return "rsa"
	}
	return normalizeKeyTypeName(key.Type())
}

// normalizeKeyTypeName reduces any spelling of a key type ("ssh-ed25519",
// "ssh-rsa", "rsa-sha2-256", "ed25519") to the bare family name used for
// comparison, so a pin written "ssh-ed25519" matches a key whose algorithm is
// "ssh-ed25519", and a pin written "rsa" matches an "rsa-sha2-512" key.
func normalizeKeyTypeName(t string) string {
	t = strings.ToLower(strings.TrimSpace(t))
	t = strings.TrimPrefix(t, "ssh-")
	switch {
	case strings.HasPrefix(t, "rsa"):
		return "rsa"
	case strings.HasPrefix(t, "ecdsa"):
		return "ecdsa"
	case strings.HasPrefix(t, "ed25519"):
		return "ed25519"
	case strings.HasPrefix(t, "dss") || strings.HasPrefix(t, "dsa"):
		return "dsa"
	}
	return t
}

// parsePin splits an operator pin into an optional key type and a fingerprint.
// The type is optional and matches the legacy, fingerprint-only pin:
//
//	"SHA256:abc…"             -> ("",        "SHA256:abc…")
//	"ed25519 SHA256:abc…"     -> ("ed25519", "SHA256:abc…")
//	"ssh-ed25519 SHA256:abc…" -> ("ed25519", "SHA256:abc…")
//
// The final whitespace-separated field is the fingerprint; every earlier field is
// the (possibly hyphenated) type spelling.
func parsePin(pin string) (typ, fingerprint string) {
	fields := strings.Fields(strings.TrimSpace(pin))
	switch len(fields) {
	case 0:
		return "", ""
	case 1:
		return "", fields[0]
	default:
		return normalizeKeyTypeName(strings.Join(fields[:len(fields)-1], "-")), fields[len(fields)-1]
	}
}

// pinnedKeyMatches reports whether the operator's pin accepts key. When the pin
// names a key type the type must match too, so an ed25519 pin can never satisfy a
// router that presented its RSA host key — the 2026-09-28 defect, where the pin
// and the store compared fingerprints with no key-type dimension.
//
// A bare-fingerprint pin keeps the legacy behaviour: negotiation is now pinned
// ed25519-first (ssh.go), so a bare pin still matches the key every operator-facing
// verification path reports.
func pinnedKeyMatches(pin string, key ssh.PublicKey) bool {
	typ, fp := parsePin(pin)
	if typ != "" && typ != hostKeyTypeName(key) {
		return false
	}
	return sameFingerprint(fp, ssh.FingerprintSHA256(key))
}

// knownHostsStoreMu guards the store's read-modify-write. Several jobs can run at
// once (main.go starts a deploy with `go runDeployment(...)`), and two interleaved
// rewrites of the same file would lose an entry.
var knownHostsStoreMu sync.Mutex

// rememberHostKey records host+key in the store so later connects in this run
// (and later runs) verify the key without another explicit trust decision.
//
// It REPLACES the host's existing entry instead of appending one. Appending was
// BLOCK 2 of the #41 review: x/crypto's knownhosts keeps only the FIRST key it
// finds for a host+algorithm ("if _, ok := knownKeys[typ]; !ok", knownhosts.go),
// so after the operator pinned a RE-IMAGED router's new key the stale pre-flash
// line still won — the appended line was ignored, every later pin-less connect
// was refused as CHANGED, and the promise this file's header makes ("the key is
// then remembered ... so later runs do not need the flag again") did not hold for
// exactly the re-image case it documents.
//
// Entries for other hosts (and comments) are preserved, and the rewrite is atomic
// so a crash cannot leave a half-written trust store behind.
func rememberHostKey(host string, key ssh.PublicKey) error {
	path := knownHostsPath()
	if path == "" {
		return errors.New("no known-hosts store path available")
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	line := knownhosts.Line([]string{knownhosts.Normalize(host)}, key)
	if strings.TrimSpace(line) == "" {
		return errors.New("could not serialise the host key")
	}

	knownHostsStoreMu.Lock()
	defer knownHostsStoreMu.Unlock()

	if err := replaceHostKeyLine(path, host, line); err != nil {
		return err
	}
	// Prove it with the real parser instead of trusting the removal pass: the
	// store must now resolve host to the key just recorded. A line the pass cannot
	// identify (a hashed "|1|…" entry, whose host is unrecoverable without its
	// per-line salt) still shadows it, and the operator is better served by a
	// warning than by a "remembered" that refuses the router on the next run.
	return verifyStoreResolvesTo(path, host, key)
}

// replaceHostKeyLine writes line as the ONLY store entry for host, keeping every
// other line.
func replaceHostKeyLine(path, host, line string) error {
	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	kept := make([]string, 0, 8)
	for _, l := range strings.Split(string(existing), "\n") {
		if strings.TrimSpace(l) == "" {
			continue // the store is machine-written; a blank line carries nothing
		}
		if knownHostsLineCovers(l, host) {
			continue // replaced by `line` below
		}
		kept = append(kept, l)
	}
	kept = append(kept, line)

	dir := filepath.Dir(path)
	if dir == "" {
		dir = "."
	}
	tmp, err := os.CreateTemp(dir, ".tollgate-known-hosts-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename below succeeded
	if _, err := tmp.WriteString(strings.Join(kept, "\n") + "\n"); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// knownHostsLineCovers reports whether one store line applies to host — i.e.
// whether x/crypto's knownhosts would consider it when verifying host, in which
// case it can shadow the line about to be written (first key wins per algorithm).
//
// The pattern semantics mirror the package's own parser (knownhosts.go:
// hostPattern.match and Normalize): comma-separated patterns, an "!"-prefixed
// pattern excludes the line for that host, "*" and "?" globs are honoured, and a
// pattern applies only to the port it names (22 when it names none).
//
// Hashed ("|1|…") patterns are deliberately not matched here; see
// verifyStoreResolvesTo, which surfaces whatever this pass cannot see.
func knownHostsLineCovers(line, host string) bool {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return false // a comment, or a line without a key: not an entry
	}
	wantHost, wantPort := splitKnownHostsAddr(knownhosts.Normalize(host))

	covered := false
	for _, raw := range strings.Split(fields[0], ",") {
		pattern := strings.TrimSpace(raw)
		negated := strings.HasPrefix(pattern, "!")
		if negated {
			pattern = strings.TrimPrefix(pattern, "!")
		}
		if pattern == "" || strings.HasPrefix(pattern, "|") {
			continue
		}
		patHost, patPort := splitKnownHostsAddr(pattern)
		if patPort != wantPort || !knownHostsGlobMatch(patHost, wantHost) {
			continue
		}
		if negated {
			return false // the line explicitly does not apply to host
		}
		covered = true
	}
	return covered
}

// splitKnownHostsAddr splits a known_hosts host pattern into host and port with
// the defaults the package's parser applies: "[host]:port" and "host:port" split,
// anything else is port 22.
func splitKnownHostsAddr(pattern string) (host, port string) {
	if h, p, err := net.SplitHostPort(pattern); err == nil {
		return h, p
	}
	return pattern, "22"
}

// knownHostsGlobMatch mirrors x/crypto/ssh/knownhosts' wildcardMatch for the
// subset a host pattern can use: "*" matches any run of characters, "?" matches
// exactly one, everything else matches itself.
func knownHostsGlobMatch(pattern, s string) bool {
	pi, si := 0, 0
	star, resume := -1, 0
	for si < len(s) {
		switch {
		case pi < len(pattern) && (pattern[pi] == '?' || pattern[pi] == s[si]):
			pi++
			si++
		case pi < len(pattern) && pattern[pi] == '*':
			star, resume = pi, si
			pi++
		case star >= 0:
			resume++
			pi, si = star+1, resume
		default:
			return false
		}
	}
	for pi < len(pattern) && pattern[pi] == '*' {
		pi++
	}
	return pi == len(pattern)
}

// storeCheckAddr stands in for the far end of a router connection while the store
// is verified. knownhosts prefers the explicit address string it is handed, but it
// dereferences remote.String() first, so a non-nil net.Addr is required.
type storeCheckAddr struct{ addr string }

func (a storeCheckAddr) Network() string { return "tcp" }
func (a storeCheckAddr) String() string  { return a.addr }

// verifyStoreResolvesTo re-reads the store with the real parser and checks that
// host now resolves to key — the invariant the "remembered" promise rests on.
func verifyStoreResolvesTo(path, host string, key ssh.PublicKey) error {
	if _, _, err := net.SplitHostPort(host); err != nil {
		// sshConnect always dials net.JoinHostPort(ip, port) and the callback
		// receives that address (ssh.go), so this is unreachable in production:
		// do not invent a port to make a check pass.
		return nil
	}
	checker, err := knownhosts.New(path)
	if err != nil {
		return fmt.Errorf("cannot re-read %s: %v", path, err)
	}
	if err := checker(host, storeCheckAddr{addr: host}, key); err != nil {
		return fmt.Errorf("%s does not resolve %s to the key that was just recorded (%v) — a remaining entry shadows it, so a later run without --trust-host-key would be refused as CHANGED", path, host, err)
	}
	return nil
}

// hostKeyRefusal is the most recent refusal for one router address: the
// operator-facing message plus the fingerprint the router actually presented.
// The fingerprint is kept structurally (not only embedded in the prose) so the
// browser wizard can offer to trust exactly the key it just showed.
type hostKeyRefusal struct {
	message     string
	fingerprint string
}

var (
	hostKeyRefusalsMu sync.Mutex
	hostKeyRefusals   = map[string]hostKeyRefusal{}
)

// forgetHostKeyRefusal drops the recorded refusal for ip. Called at the start of
// every connect attempt so a refusal can only ever describe the LATEST attempt.
func forgetHostKeyRefusal(ip string) {
	hostKeyRefusalsMu.Lock()
	delete(hostKeyRefusals, ip)
	hostKeyRefusalsMu.Unlock()
}

// recordHostKeyRefusal remembers why the connect attempt to ip was refused — the
// HTTP handlers surface this text, so the operator sees the fingerprint and the
// way to trust it instead of a generic "cannot connect" — and returns it as the
// handshake error that aborts the connection. fingerprint is the key the router
// presented, or "" when none was seen before the attempt failed.
func recordHostKeyRefusal(ip, msg, fingerprint string) error {
	hostKeyRefusalsMu.Lock()
	hostKeyRefusals[ip] = hostKeyRefusal{message: msg, fingerprint: fingerprint}
	hostKeyRefusalsMu.Unlock()
	return errors.New(msg)
}

// lastHostKeyRefusal returns the refusal recorded by the most recent connect
// attempt against ip, or "" when the attempt failed for another reason.
func lastHostKeyRefusal(ip string) string {
	hostKeyRefusalsMu.Lock()
	defer hostKeyRefusalsMu.Unlock()
	return hostKeyRefusals[ip].message
}

// lastHostKeyFingerprint returns the fingerprint the most recent refusal for ip
// named, or "" when there was no refusal (or it carried no fingerprint). It lets
// the wizard name the exact key to trust without parsing the message text.
func lastHostKeyFingerprint(ip string) string {
	hostKeyRefusalsMu.Lock()
	defer hostKeyRefusalsMu.Unlock()
	return hostKeyRefusals[ip].fingerprint
}

// sshConnectFailureMessage is the operator-facing text for a failed connect: the
// host-key refusal when there was one (it carries the fingerprint and the exact
// trust instruction), otherwise the caller's existing generic message.
func sshConnectFailureMessage(ip, fallback string) string {
	if r := lastHostKeyRefusal(ip); r != "" {
		return r
	}
	if cause := describeSSHConnectFailure(ip); cause != "" {
		return fallback + " — " + cause
	}
	return fallback
}

// describeSSHConnectFailure turns the recorded dial/auth error into ONE
// actionable sentence, or "" when nothing was recorded (a successful connect, or
// a failure that was not a connect at all).
//
// The three classes are deliberately separate because the operator's next move
// differs for each: supply/verify the root password, enable SSH or fix the
// address, or fix reachability. Collapsing them is the defect this exists to
// remove.
func describeSSHConnectFailure(ip string) string {
	err, passwordSupplied := lastSSHConnectError(ip)
	if err == nil {
		return ""
	}
	msg := err.Error()
	target := net.JoinHostPort(ip, sshDialPort)
	switch {
	case strings.Contains(msg, "unable to authenticate"):
		if !passwordSupplied {
			return fmt.Sprintf("the router at %s refused the SSH login: no password was supplied and it does not accept an empty root password — enter the router's root password and retry", target)
		}
		return fmt.Sprintf("the router at %s refused the SSH login as root — check the root password", target)
	case strings.Contains(msg, "connection refused"):
		return fmt.Sprintf("nothing is accepting SSH at %s — check the router's address, and that SSH is enabled on it", target)
	case strings.Contains(msg, "i/o timeout"):
		return fmt.Sprintf("no SSH response from %s (timed out) — check the router's address and that this machine is on its LAN", target)
	case strings.Contains(msg, "no route to host"), strings.Contains(msg, "network is unreachable"):
		return fmt.Sprintf("%s is not reachable from this machine — check the router's address and the network connection", target)
	default:
		// An unrecognised failure is still more useful than nothing, and it keeps
		// x/crypto's host-key alarms we do not map visible to the operator.
		return msg
	}
}

// storeDescription names the store in operator-facing text.
func storeDescription(path string) string {
	if path == "" {
		return "(no known-hosts store path available)"
	}
	return path
}

// untrustedHostKeyMessage reports an unseen host key, prints the prominent
// instruction block to stderr (where the operator running the binary sees it
// even when the browser wizard is the UI), and returns the reason the handshake
// is aborted.
func untrustedHostKeyMessage(host, keyType, fingerprint, store string) string {
	fmt.Fprintf(os.Stderr, `
================================================================================
 ROUTER SSH HOST KEY NOT TRUSTED — CONNECTION REFUSED
================================================================================
  router      : %s
  key type    : %s
  fingerprint : %s
This host is not in the trust store, so no credentials were sent.
Verify the fingerprint on the router's own console, for example:
  ssh-keygen -lf /etc/dropbear/dropbear_%s_host_key.pub
If it matches, trust it explicitly:
  --trust-host-key %s
  TOLLGATE_TRUST_HOST_KEY=%s   (for the curl|bash launcher)
The key is then remembered in %s.
================================================================================
`, host, keyType, fingerprint, keyType, fingerprint, fingerprint, storeDescription(store))

	return fmt.Sprintf(
		"router SSH host key is not trusted: %s presents a %s key %s and no credentials were sent. Verify this fingerprint (and its key type) on the router's own console, then re-run with --trust-host-key %s (curl|bash: prefix the command with %s=%s). Trusted keys are remembered in %s.",
		host, keyType, fingerprint, fingerprint, trustHostKeyEnv, fingerprint, storeDescription(store))
}

// changedHostKeyMessage reports that a host already in the store now presents a
// DIFFERENT key. This is never accepted implicitly — it is the MITM signature.
// keyType/fingerprint describe what was presented; known lists the stored keys as
// "type fingerprint" so a type-only difference (the 2026-09-28 defect) is visible.
func changedHostKeyMessage(host, keyType, fingerprint string, known []string, store string) string {
	fmt.Fprintf(os.Stderr, `
================================================================================
 ROUTER SSH HOST KEY CHANGED — CONNECTION REFUSED
================================================================================
  router      : %s
  presented   : %s %s
  trusted     : %s
The key for this host changed, so no credentials were sent. Either the router
was re-flashed / re-keyed, or something on the LAN is impersonating it.
Verify on the router's console; if the router really was re-keyed, remove its
line from %s and connect again to trust the new key.
================================================================================
`, host, keyType, fingerprint, strings.Join(known, ", "), storeDescription(store))

	return fmt.Sprintf(
		"router SSH host key CHANGED for %s: it presents a %s key %s but the trust store has %s and no credentials were sent. Either the router was re-flashed/re-keyed or something on the LAN is impersonating it: verify on the router's console, then remove its line from %s and connect again.",
		host, keyType, fingerprint, strings.Join(known, ", "), storeDescription(store))
}

// routerHostKeyCallback is the HostKeyCallback for every router connection:
// pinned fingerprint, then the store, then refusal. It never silently accepts a
// key, and it never accepts a changed key for a host already in the store.
func routerHostKeyCallback(ip string) ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		fingerprint := ssh.FingerprintSHA256(key)

		// 1. Explicit pin for this run (--trust-host-key / env).
		if want := hostKeyTrust(); want != "" {
			if !pinnedKeyMatches(want, key) {
				// Name the PIN's key type too. Without it the operator is shown two
				// unlabelled fingerprints and has to guess which is which — the
				// shape of the 2026-10-02 loop, where a pin taken for the RSA key
				// could never satisfy a router now presenting ed25519, and the
				// message never said the pin itself was the wrong key type.
				hint := ""
				if pinType, _ := parsePin(want); pinType != "" && pinType != hostKeyTypeName(key) {
					hint = fmt.Sprintf(" The pin is a %s key while the router presented its %s key — one router serves one host key per algorithm, so these are two different keys, and only the %s one can match this handshake.", pinType, hostKeyTypeName(key), hostKeyTypeName(key))
				}
				return recordHostKeyRefusal(ip, fmt.Sprintf(
					"router SSH host key mismatch: %s presents a %s key %s but %s pins %s and no credentials were sent.%s Verify the %s key on the router's console and pass the fingerprint it actually prints.",
					hostname, hostKeyTypeName(key), fingerprint, "--trust-host-key", want, hint, hostKeyTypeName(key)), fingerprint)
			}
			if err := rememberHostKey(hostname, key); err != nil {
				fmt.Fprintf(os.Stderr, "warning: could not remember %s in %s: %v\n", fingerprint, storeDescription(knownHostsPath()), err)
			}
			return nil
		}

		// 2. The store: verify against the key this host presented before, and
		//    refuse a changed key outright.
		store := knownHostsPath()
		if store != "" {
			if _, err := os.Stat(store); err == nil {
				checker, err := knownhosts.New(store)
				if err != nil {
					return recordHostKeyRefusal(ip, fmt.Sprintf(
						"cannot read the SSH trust store %s: %v — refusing to connect to %s and no credentials were sent.",
						store, err, hostname), fingerprint)
				}
				if err := checker(hostname, remote, key); err != nil {
					var keyErr *knownhosts.KeyError
					if errors.As(err, &keyErr) {
						if len(keyErr.Want) > 0 {
							known := make([]string, 0, len(keyErr.Want))
							sameType := false
							for _, k := range keyErr.Want {
								known = append(known, fmt.Sprintf("%s %s", hostKeyTypeName(k.Key), ssh.FingerprintSHA256(k.Key)))
								if hostKeyTypeName(k.Key) == hostKeyTypeName(key) {
									sameType = true
								}
							}
							// A store entry of a DIFFERENT key type is not a changed
							// key. OpenWrt's dropbear serves one host key per
							// algorithm, so the same router legitimately answers with
							// its rsa key when the negotiated algorithm changes — and
							// x/crypto hands us those cross-type keys in Want. Running
							// that through the CHANGED message told an operator who had
							// trusted this very router "either the router was
							// re-flashed/re-keyed or something on the LAN is
							// impersonating it" (measured 2026-10-02). Only a stored
							// key of the SAME type with a different fingerprint is the
							// MITM signature.
							if !sameType {
								return recordHostKeyRefusal(ip, differentKeyTypeMessage(hostname, hostKeyTypeName(key), fingerprint, known, store), fingerprint)
							}
							return recordHostKeyRefusal(ip, changedHostKeyMessage(hostname, hostKeyTypeName(key), fingerprint, known, store), fingerprint)
						}
						// An empty Want does NOT mean "unknown host": x/crypto only
						// offers the keys it holds FOR THE ALGORITHM JUST PRESENTED
						// (knownhosts.go indexes knownKeys by key type), so a host
						// stored under ed25519 that presents its rsa key lands here.
						// Reporting that as "not in the trust store" told an operator
						// who HAD trusted the router that nothing was trusted — the
						// 2026-10-02 defect. Say what the store actually holds.
						if stored := storeKeyEntriesForHost(store, hostname); len(stored) > 0 {
							return recordHostKeyRefusal(ip, differentKeyTypeMessage(hostname, hostKeyTypeName(key), fingerprint, stored, store), fingerprint)
						}
					}
					return recordHostKeyRefusal(ip, untrustedHostKeyMessage(hostname, hostKeyTypeName(key), fingerprint, store), fingerprint)
				}
				return nil // seen before, and the key still matches
			}
		}

		// 3. Unseen host and nothing pinned: fail safe.
		return recordHostKeyRefusal(ip, untrustedHostKeyMessage(hostname, hostKeyTypeName(key), fingerprint, store), fingerprint)
	}
}

// storeKeyEntriesForHost lists what the trust store holds for host, as
// "type fingerprint", across EVERY key type — not just the one the router just
// presented.
//
// Hashed ("|1|…") store lines cannot be attributed to a host without their
// per-line salt and are therefore not listed; they are still enforced by the
// checker, and verifyStoreResolvesTo is what surfaces them when they shadow a
// write.
func storeKeyEntriesForHost(path, host string) []string {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	out := make([]string, 0, 4)
	for _, line := range strings.Split(string(body), "\n") {
		if !knownHostsLineCovers(line, host) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue // a comment, or a line without a key
		}
		// The store is OpenSSH known_hosts layout: "<pattern> <keytype> <base64>
		// [comment]". Dropping the pattern leaves exactly an authorized-key line.
		pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(strings.Join(fields[1:], " ")))
		if err != nil {
			continue
		}
		out = append(out, fmt.Sprintf("%s %s", hostKeyTypeName(pub), ssh.FingerprintSHA256(pub)))
	}
	return out
}

// differentKeyTypeMessage reports a host that IS in the store, under a key type
// other than the one it just presented. A different key type is not a changed
// key: OpenWrt's dropbear serves one host key per algorithm, so the same router
// legitimately answers with a different key when the negotiated algorithm
// changes. This still refuses and still demands out-of-band verification — it
// just stops pretending the host is unknown, which sent the operator hunting for
// an impostor instead of at the key their own store already held.
func differentKeyTypeMessage(host, keyType, fingerprint string, stored []string, store string) string {
	fmt.Fprintf(os.Stderr, `
================================================================================
 ROUTER SSH HOST KEY TYPE NOT IN THE TRUST STORE — CONNECTION REFUSED
================================================================================
  router      : %s
  presented   : %s %s
  store holds : %s
This host IS in the trust store (%s) — but not under the key type it just
presented. A different key type is not a changed key. Verify the %s key on the
router's own console, for example:
  ssh-keygen -lf /etc/dropbear/dropbear_%s_host_key.pub
If it matches, trust it explicitly:
  --trust-host-key %s
  TOLLGATE_TRUST_HOST_KEY=%s   (for the curl|bash launcher)
================================================================================
`, host, keyType, fingerprint, strings.Join(stored, ", "), storeDescription(store), keyType, keyType, fingerprint, fingerprint)

	return fmt.Sprintf(
		"router SSH host key type not trusted: %s presents a %s key %s, and the trust store holds %s for this host. This is a different key TYPE, not a changed key — verify the %s fingerprint on the router's console, then re-run with --trust-host-key %s (curl|bash: prefix the command with %s=%s).",
		host, keyType, fingerprint, strings.Join(stored, ", "), keyType, fingerprint, trustHostKeyEnv, fingerprint)
}

// trustHostKeyForHost verifies that ip presents exactly the fingerprint the
// operator supplied and, only then, records the key in the trust store so later
// connects need no pin. It is the browser wizard's equivalent of
// --trust-host-key: the operator reads the fingerprint off the router's console,
// the wizard shows it back, and this persists that one decision for that host.
//
// The client config carries NO auth methods, so no credential is ever offered to
// the router — verifying the host key is the entire purpose. A mismatch records
// the refusal (so /api/identify keeps explaining it) and returns an error
// without writing anything to the store; a malformed fingerprint never reaches
// here (see handleTrustHostKey).
//
// Note it deliberately does NOT consult or set the process-global
// --trust-host-key pin: trust granted here is for this host only, so one
// operator confirmation cannot silently trust every other address.
func trustHostKeyForHost(ip, fingerprint string) (ssh.PublicKey, error) {
	var key ssh.PublicKey
	config := &ssh.ClientConfig{
		User:    "root",
		Timeout: 10 * time.Second,
		// WHY (2026-10-02): this dial had no HostKeyAlgorithms, so x/crypto fell
		// back to its own preference and negotiated the router's RSA key — while
		// the fingerprint the operator is shown (and the one this function is
		// then asked to verify) comes from the scan path, which is ed25519-first.
		// The comparison could therefore never match on a dropbear box serving
		// both key types, and the Trust button returned 502 forever:
		//   "router SSH host key mismatch: … presents SHA256:rsKww… but
		//    SHA256:CSPcG8Gu… was supplied and nothing was trusted."
		// Every dial in this package shares ONE preference, as the
		//    sshHostKeyAlgorithms docstring already claims.
		HostKeyAlgorithms: sshHostKeyAlgorithms,
		HostKeyCallback: func(hostname string, remote net.Addr, presented ssh.PublicKey) error {
			got := ssh.FingerprintSHA256(presented)
			if !sameFingerprint(fingerprint, got) {
				return recordHostKeyRefusal(ip, fmt.Sprintf(
					"router SSH host key mismatch: %s presents %s but %s was supplied and nothing was trusted.",
					hostname, got, fingerprint), got)
			}
			key = presented
			return nil
		},
	}
	client, err := ssh.Dial("tcp", net.JoinHostPort(ip, sshDialPort), config)
	if err != nil && key == nil {
		// The callback refused, or the dial never got far enough to present a key.
		return nil, err
	}
	if client != nil {
		client.Close() // handshake done; auth is irrelevant to host-key trust
	}
	if err := rememberHostKey(net.JoinHostPort(ip, sshDialPort), key); err != nil {
		return key, fmt.Errorf("verified %s but could not remember it: %w", ssh.FingerprintSHA256(key), err)
	}
	return key, nil
}
