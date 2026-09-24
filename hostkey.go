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
//     fingerprint out of band. The key is then remembered in the store, so the
//     rest of this run and later runs do not need the flag again.
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

// rememberHostKey appends host+key to the store so later connects in this run
// (and later runs) verify the key without another explicit trust decision.
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
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	line := knownhosts.Line([]string{knownhosts.Normalize(host)}, key)
	if strings.TrimSpace(line) == "" {
		return errors.New("could not serialise the host key")
	}
	_, err = f.WriteString(line + "\n")
	return err
}

var (
	hostKeyRefusalsMu sync.Mutex
	hostKeyRefusals   = map[string]string{}
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
// handshake error that aborts the connection.
func recordHostKeyRefusal(ip, msg string) error {
	hostKeyRefusalsMu.Lock()
	hostKeyRefusals[ip] = msg
	hostKeyRefusalsMu.Unlock()
	return errors.New(msg)
}

// lastHostKeyRefusal returns the refusal recorded by the most recent connect
// attempt against ip, or "" when the attempt failed for another reason.
func lastHostKeyRefusal(ip string) string {
	hostKeyRefusalsMu.Lock()
	defer hostKeyRefusalsMu.Unlock()
	return hostKeyRefusals[ip]
}

// sshConnectFailureMessage is the operator-facing text for a failed connect: the
// host-key refusal when there was one (it carries the fingerprint and the exact
// trust instruction), otherwise the caller's existing generic message.
func sshConnectFailureMessage(ip, fallback string) string {
	if r := lastHostKeyRefusal(ip); r != "" {
		return r
	}
	return fallback
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
func untrustedHostKeyMessage(host, fingerprint, store string) string {
	fmt.Fprintf(os.Stderr, `
================================================================================
 ROUTER SSH HOST KEY NOT TRUSTED — CONNECTION REFUSED
================================================================================
  router      : %s
  fingerprint : %s
This host is not in the trust store, so no credentials were sent.
Verify the fingerprint on the router's own console, for example:
  ssh-keygen -lf /etc/dropbear/dropbear_ed25519_host_key.pub
If it matches, trust it explicitly:
  --trust-host-key %s
  TOLLGATE_TRUST_HOST_KEY=%s   (for the curl|bash launcher)
The key is then remembered in %s.
================================================================================
`, host, fingerprint, fingerprint, fingerprint, storeDescription(store))

	return fmt.Sprintf(
		"router SSH host key is not trusted: %s presents %s and no credentials were sent. Verify this fingerprint on the router's own console, then re-run with --trust-host-key %s (curl|bash: prefix the command with %s=%s). Trusted keys are remembered in %s.",
		host, fingerprint, fingerprint, trustHostKeyEnv, fingerprint, storeDescription(store))
}

// changedHostKeyMessage reports that a host already in the store now presents a
// DIFFERENT key. This is never accepted implicitly — it is the MITM signature.
func changedHostKeyMessage(host, fingerprint string, known []string, store string) string {
	fmt.Fprintf(os.Stderr, `
================================================================================
 ROUTER SSH HOST KEY CHANGED — CONNECTION REFUSED
================================================================================
  router      : %s
  presented   : %s
  trusted     : %s
The key for this host changed, so no credentials were sent. Either the router
was re-flashed / re-keyed, or something on the LAN is impersonating it.
Verify on the router's console; if the router really was re-keyed, remove its
line from %s and connect again to trust the new key.
================================================================================
`, host, fingerprint, strings.Join(known, ", "), storeDescription(store))

	return fmt.Sprintf(
		"router SSH host key CHANGED for %s: it presents %s but the trust store has %s and no credentials were sent. Either the router was re-flashed/re-keyed or something on the LAN is impersonating it: verify on the router's console, then remove its line from %s and connect again.",
		host, fingerprint, strings.Join(known, ", "), storeDescription(store))
}

// routerHostKeyCallback is the HostKeyCallback for every router connection:
// pinned fingerprint, then the store, then refusal. It never silently accepts a
// key, and it never accepts a changed key for a host already in the store.
func routerHostKeyCallback(ip string) ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		fingerprint := ssh.FingerprintSHA256(key)

		// 1. Explicit pin for this run (--trust-host-key / env).
		if want := hostKeyTrust(); want != "" {
			if !sameFingerprint(want, fingerprint) {
				return recordHostKeyRefusal(ip, fmt.Sprintf(
					"router SSH host key mismatch: %s presents %s but %s pins %s and no credentials were sent. Check the fingerprint on the router's console and pass the one it actually prints.",
					hostname, fingerprint, "--trust-host-key", want))
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
						store, err, hostname))
				}
				if err := checker(hostname, remote, key); err != nil {
					var keyErr *knownhosts.KeyError
					if errors.As(err, &keyErr) && len(keyErr.Want) > 0 {
						known := make([]string, 0, len(keyErr.Want))
						for _, k := range keyErr.Want {
							known = append(known, ssh.FingerprintSHA256(k.Key))
						}
						return recordHostKeyRefusal(ip, changedHostKeyMessage(hostname, fingerprint, known, store))
					}
					return recordHostKeyRefusal(ip, untrustedHostKeyMessage(hostname, fingerprint, store))
				}
				return nil // seen before, and the key still matches
			}
		}

		// 3. Unseen host and nothing pinned: fail safe.
		return recordHostKeyRefusal(ip, untrustedHostKeyMessage(hostname, fingerprint, store))
	}
}
