package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net"
	"os"
	"strings"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// hostkey_test.go pins the router-trust contract for the wizard's root SSH
// session (pre-release audit finding C2-I-01).
//
// The wizard asks the operator for the router's ROOT password and then installs
// a package on that router. If the host key is not verified, any host on the
// same LAN that answers TCP/22 at the router's address is handed that password
// and decides what gets installed. These tests drive the REAL sshConnect path
// against a real in-process SSH server, so the assertions are behavioural and
// not a grep of the source.

// testRouterCredential is the credential the fixture router accepts. It is a test
// fixture and never a real credential; it only has to be a value the fixture
// server can recognise.
const testRouterCredential = "correct-horse-battery-staple"

// rogueRouter is a minimal in-process SSH server standing in for "something on
// the LAN that is not the router": it accepts the password (or an empty
// password, like a fresh OpenWrt/dropbear) and records every credential it is
// offered. The credential log is the point of the fixture — it is how a test
// proves that a REFUSED host key never receives the operator's password.
type rogueRouter struct {
	t  *testing.T
	ln net.Listener

	mu       sync.Mutex
	authSeen []string
}

func (fr *rogueRouter) host() string {
	host, _, err := net.SplitHostPort(fr.ln.Addr().String())
	if err != nil {
		fr.t.Fatalf("split listener addr %q: %v", fr.ln.Addr().String(), err)
	}
	return host
}

func (fr *rogueRouter) port() string {
	_, port, err := net.SplitHostPort(fr.ln.Addr().String())
	if err != nil {
		fr.t.Fatalf("split listener addr %q: %v", fr.ln.Addr().String(), err)
	}
	return port
}

// credentialsSeen returns every password the server was offered, in order.
func (fr *rogueRouter) credentialsSeen() []string {
	fr.mu.Lock()
	defer fr.mu.Unlock()
	out := make([]string, len(fr.authSeen))
	copy(out, fr.authSeen)
	return out
}

// startRogueRouter starts an SSH server on a loopback port with the given host
// key. The listener is closed on test cleanup.
func startRogueRouter(t *testing.T, hostKey ssh.Signer) *rogueRouter {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	fr := &rogueRouter{t: t, ln: ln}
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(_ ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			fr.mu.Lock()
			fr.authSeen = append(fr.authSeen, string(pw))
			fr.mu.Unlock()
			// Same policy as a fresh router: empty password accepted.
			if string(pw) == testRouterCredential || len(pw) == 0 {
				return nil, nil
			}
			return nil, errors.New("password rejected")
		},
	}
	cfg.AddHostKey(hostKey)
	go fr.serve(cfg)
	t.Cleanup(func() { ln.Close() })
	return fr
}

func (fr *rogueRouter) serve(cfg *ssh.ServerConfig) {
	for {
		conn, err := fr.ln.Accept()
		if err != nil {
			return
		}
		go func() {
			sconn, chans, reqs, err := ssh.NewServerConn(conn, cfg)
			if err != nil {
				conn.Close()
				return
			}
			go ssh.DiscardRequests(reqs)
			go func() {
				for ch := range chans {
					ch.Reject(ssh.Prohibited, "no channels")
				}
			}()
			sconn.Wait()
		}()
	}
}

// hostKeySigner mints a fresh ed25519 host key for a fixture server.
func hostKeySigner(t *testing.T) ssh.Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate ed25519 host key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("signer from ed25519 key: %v", err)
	}
	return signer
}

// dialRogueRouter runs the REAL sshConnect against the fixture server on its own
// port (production always dials 22; see sshDialPort).
func dialRogueRouter(t *testing.T, fr *rogueRouter) *ssh.Client {
	t.Helper()
	old := sshDialPort
	sshDialPort = fr.port()
	t.Cleanup(func() { sshDialPort = old })
	return sshConnect(fr.host(), testRouterCredential)
}

// knownHostsStore points the trust store at a private temp file (so the
// operator's own ~/.tollgate-known-hosts is never read or written) and clears any
// ambient trust decision.
func knownHostsStore(t *testing.T) string {
	t.Helper()
	path := t.TempDir() + "/known-hosts"
	t.Setenv("TOLLGATE_KNOWN_HOSTS", path)
	t.Setenv("TOLLGATE_TRUST_HOST_KEY", "")
	withTrustedFingerprint(t, "")
	return path
}

// withTrustedFingerprint sets the --trust-host-key pin for the duration of the
// test ("" = nothing pinned).
func withTrustedFingerprint(t *testing.T, fingerprint string) {
	t.Helper()
	old := *trustHostKey
	*trustHostKey = fingerprint
	t.Cleanup(func() { *trustHostKey = old })
}

// TestNoInsecureIgnoreHostKeyInTheInstaller is the permanent regression guard for
// C2-I-01: ssh.InsecureIgnoreHostKey accepted any key, which is what made the
// password harvest possible, so it must never appear in the install path again.
func TestNoInsecureIgnoreHostKeyInTheInstaller(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if strings.Contains(string(src), "InsecureIgnoreHostKey") {
			t.Errorf("%s uses ssh.InsecureIgnoreHostKey — the router's host key MUST be verified (see hostkey.go: pin, trust store, or refuse)", name)
		}
	}
}

// TestUnknownRouterHostKeyIsRefusedWithoutSendingCredentials is the core
// contract of C2-I-01: a router whose host key the operator has not trusted
// must NOT be connected to, and the root password must never be offered to it.
//
// The fixture is the attack from the audit: something other than the router
// answers TCP/22 at the router's address. Before the fix sshConnect accepted it
// and sent the password; that failure IS the vulnerability.
func TestUnknownRouterHostKeyIsRefusedWithoutSendingCredentials(t *testing.T) {
	knownHostsStore(t) // a store that has never seen this host

	hostKey := hostKeySigner(t)
	attacker := startRogueRouter(t, hostKey)

	client := dialRogueRouter(t, attacker)
	seen := attacker.credentialsSeen()
	if client != nil {
		client.Close()
	}
	if client != nil || len(seen) != 0 {
		t.Fatalf("sshConnect connected to a host key the operator never trusted (connected=%v) and offered it %d credential(s) %q — the LAN impostor at %s would have harvested the router root password",
			client != nil, len(seen), seen, attacker.ln.Addr())
	}

	// The refusal has to be actionable: the fingerprint the host presented, and
	// the exact way to trust it.
	refusal := lastHostKeyRefusal(attacker.host())
	if refusal == "" {
		t.Fatal("no host-key refusal was recorded for the operator")
	}
	for _, want := range []string{ssh.FingerprintSHA256(hostKey.PublicKey()), "--trust-host-key", trustHostKeyEnv} {
		if !strings.Contains(refusal, want) {
			t.Errorf("refusal must tell the operator %q so they can trust the router explicitly\n%s", want, refusal)
		}
	}
	// The HTTP handlers show this text instead of a bare "cannot connect".
	if msg := sshConnectFailureMessage(attacker.host(), "cannot connect to router via SSH"); !strings.Contains(msg, "--trust-host-key") {
		t.Errorf("operator-facing connect failure lost the trust instruction: %q", msg)
	}
}

// TestExplicitlyTrustedFingerprintIsAcceptedAndRemembered is the other half of
// the contract: an operator who verified the fingerprint out of band can proceed,
// and the decision persists so the rest of the run (identify, wifi-scan,
// prestage, deploy) does not need the flag again.
func TestExplicitlyTrustedFingerprintIsAcceptedAndRemembered(t *testing.T) {
	store := knownHostsStore(t)

	hostKey := hostKeySigner(t)
	router := startRogueRouter(t, hostKey)
	withTrustedFingerprint(t, ssh.FingerprintSHA256(hostKey.PublicKey()))

	client := dialRogueRouter(t, router)
	if client == nil {
		t.Fatalf("an explicitly trusted fingerprint was refused: %s", lastHostKeyRefusal(router.host()))
	}
	client.Close()
	if len(router.credentialsSeen()) == 0 {
		t.Error("the trusted router was never asked for a password")
	}

	// Later connects need no pin at all: the trust decision was remembered.
	withTrustedFingerprint(t, "")
	again := dialRogueRouter(t, router)
	if again == nil {
		t.Fatalf("remembered host key was not honoured: %s", lastHostKeyRefusal(router.host()))
	}
	again.Close()

	stored, err := os.ReadFile(store)
	if err != nil {
		t.Fatalf("trust store was not written: %v", err)
	}
	if !strings.Contains(string(stored), router.host()) {
		t.Errorf("trust store does not name the trusted router %s:\n%s", router.host(), stored)
	}
}

// TestChangedRouterHostKeyIsRefused is the MITM case: a host the operator trusted
// before now presents a DIFFERENT key. It must never be accepted silently, even
// though the address is in the store.
func TestChangedRouterHostKeyIsRefused(t *testing.T) {
	store := knownHostsStore(t)

	storedKey := hostKeySigner(t)
	impostorKey := hostKeySigner(t)
	router := startRogueRouter(t, impostorKey)

	// The operator trusted this address before, when it presented storedKey.
	line := knownhosts.Line([]string{knownhosts.Normalize(router.ln.Addr().String())}, storedKey.PublicKey())
	if err := os.WriteFile(store, []byte(line+"\n"), 0o600); err != nil {
		t.Fatalf("seed trust store: %v", err)
	}

	client := dialRogueRouter(t, router)
	seen := router.credentialsSeen()
	if client != nil {
		client.Close()
	}
	if client != nil || len(seen) != 0 {
		t.Fatalf("a changed host key was accepted (connected=%v, credentials offered=%q) — this is the MITM signature and must never be silent", client != nil, seen)
	}
	refusal := lastHostKeyRefusal(router.host())
	for _, want := range []string{"CHANGED", ssh.FingerprintSHA256(impostorKey.PublicKey()), ssh.FingerprintSHA256(storedKey.PublicKey())} {
		if !strings.Contains(refusal, want) {
			t.Errorf("changed-key refusal must contain %q so the operator can compare fingerprints\n%s", want, refusal)
		}
	}
}

// TestMismatchedTrustPinIsRefused: a --trust-host-key that does not match what
// the host presents is a refusal, not a hint. The pin only ever matches the exact
// fingerprint the operator verified.
func TestMismatchedTrustPinIsRefused(t *testing.T) {
	knownHostsStore(t)

	router := startRogueRouter(t, hostKeySigner(t))
	withTrustedFingerprint(t, ssh.FingerprintSHA256(hostKeySigner(t).PublicKey())) // some other router's key

	client := dialRogueRouter(t, router)
	if client != nil {
		client.Close()
		t.Fatal("a --trust-host-key that does not match the presented key must be refused, never treated as consent")
	}
	if seen := router.credentialsSeen(); len(seen) != 0 {
		t.Fatalf("a mismatched pin still leaked %q", seen)
	}
	if refusal := lastHostKeyRefusal(router.host()); !strings.Contains(refusal, "mismatch") {
		t.Errorf("refusal should say the pin mismatched\n%s", refusal)
	}
}

// storeLinesCovering returns every non-comment known_hosts line whose host field
// names the given address, using the same normalisation the store is written
// with. Used to assert REPLACE semantics: exactly one line may cover a host.
func storeLinesCovering(t *testing.T, store, addr string) []string {
	t.Helper()
	body, err := os.ReadFile(store)
	if err != nil {
		t.Fatalf("read trust store: %v", err)
	}
	want := knownhosts.Normalize(addr)
	var found []string
	for _, line := range strings.Split(string(body), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) < 2 {
			continue
		}
		for _, pattern := range strings.Split(fields[0], ",") {
			if strings.TrimPrefix(strings.TrimSpace(pattern), "!") == want {
				found = append(found, trimmed)
			}
		}
	}
	return found
}

// TestReImagedRouterHostKeyIsRememberedByReplace is BLOCK 2 of the #41 review.
//
// After the operator pins a RE-IMAGED router's new key, the store must validate
// that key on the NEXT connect with no pin at all — that is the promise hostkey.go
// makes ("the key is then remembered ... so the rest of this run and later runs do
// not need the flag again") and the exact case the PR documents.
//
// It did not hold: rememberHostKey APPENDED, and x/crypto's knownhosts keeps only
// the FIRST key it finds for a host+algorithm, so the pre-flash line shadowed the
// appended one. Every later pin-less connect was then refused as CHANGED while the
// stale pre-flash key still validated.
func TestReImagedRouterHostKeyIsRememberedByReplace(t *testing.T) {
	store := knownHostsStore(t)

	// The router the operator trusted on the earlier run (pre-flash key A).
	oldKey := hostKeySigner(t)
	// The same router AFTER a re-image: same address, new host key B.
	newKey := hostKeySigner(t)

	// A re-imaged router: the store already names this address with key A, and the
	// store also holds an unrelated host that must survive the rewrite.
	unrelated := hostKeySigner(t)
	unrelatedLine := knownhosts.Line([]string{"10.47.41.1"}, unrelated.PublicKey())
	router := startRogueRouter(t, newKey)
	dialAddr := net.JoinHostPort(router.host(), router.port())
	seed := knownhosts.Line([]string{knownhosts.Normalize(dialAddr)}, oldKey.PublicKey()) +
		"\n" + unrelatedLine + "\n"
	if err := os.WriteFile(store, []byte(seed), 0o600); err != nil {
		t.Fatalf("seed trust store: %v", err)
	}

	// The operator verifies key B out of band (it really is their re-imaged
	// router) and pins it for this run.
	withTrustedFingerprint(t, ssh.FingerprintSHA256(newKey.PublicKey()))
	client := dialRogueRouter(t, router)
	if client == nil {
		t.Fatalf("the explicitly pinned new key was refused: %s", lastHostKeyRefusal(router.host()))
	}
	client.Close()

	// BLOCK 2: with the pin gone, the rest of this run (identify, wifi-scan,
	// prestage, deploy) and every later run must connect WITHOUT the flag.
	withTrustedFingerprint(t, "")
	again := dialRogueRouter(t, router)
	if again == nil {
		t.Fatalf("the pinned-then-remembered re-imaged router was refused with no pin — the new key was not remembered in place of the old one: %s\nstore:\n%s",
			lastHostKeyRefusal(router.host()), readFileString(t, store))
	}
	again.Close()

	// Mechanism, so a future change cannot regress it quietly: the store holds
	// exactly ONE line for this address (the new key), and the unrelated host is
	// still present.
	if covering := storeLinesCovering(t, store, dialAddr); len(covering) != 1 {
		t.Errorf("store has %d lines covering %s, want exactly 1 (a stale entry shadows the new key: x/crypto knownhosts keeps only the FIRST key per host+algorithm)\n%s",
			len(covering), dialAddr, strings.Join(covering, "\n"))
	} else if !strings.Contains(covering[0], base64.StdEncoding.EncodeToString(newKey.PublicKey().Marshal())) {
		t.Errorf("the surviving line for %s does not carry the newly trusted key:\n%s", dialAddr, covering[0])
	}
	if stored := readFileString(t, store); !strings.Contains(stored, unrelatedLine) {
		t.Errorf("replacing the host's entry dropped an unrelated host's line:\n%s", stored)
	}
}

func readFileString(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(body)
}

// TestKnownHostsLineCoverage pins the removal pass's matching rules against the
// parser they must agree with: a line the pass leaves behind keeps shadowing the
// entry that is written (knownhosts offers only the FIRST key per algorithm).
func TestKnownHostsLineCoverage(t *testing.T) {
	const key = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIBxGUESSEDxGUESSEDxGUESSEDxGUESSEDxGUESSED"
	cases := []struct {
		name string
		line string
		host string
		want bool
	}{
		{"exact, bracketed port", "[192.168.1.1]:2222 " + key, "192.168.1.1:2222", true},
		{"exact, default port", "192.168.1.1 " + key, "192.168.1.1:22", true},
		{"exact, default port via join", "192.168.1.1 " + key, "192.168.1.1", true},
		{"different port", "[192.168.1.1]:2222 " + key, "192.168.1.1:22", false},
		{"different host", "10.0.0.9 " + key, "192.168.1.1:22", false},
		{"comma-separated list", "10.0.0.9,192.168.1.1 " + key, "192.168.1.1:22", true},
		{"glob", "192.168.1.* " + key, "192.168.1.7:22", true},
		{"glob without wildcard match", "192.168.1.?? " + key, "192.168.1.7:22", false},
		{"negated for this host", "!192.168.1.1,192.168.* " + key, "192.168.1.1:22", false},
		{"hashed line is not matched", "|1|YWJj|ZGVm " + key, "192.168.1.1:22", false},
		{"comment", "# 192.168.1.1 " + key, "192.168.1.1:22", false},
		{"no key on the line", "192.168.1.1", "192.168.1.1:22", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := knownHostsLineCovers(tc.line, tc.host); got != tc.want {
				t.Errorf("knownHostsLineCovers(%q, %q) = %v, want %v", tc.line, tc.host, got, tc.want)
			}
		})
	}
}

// TestHashedStoreLineIsReportedNotSilentlyIgnored documents the one boundary of
// the removal pass: a hashed "|1|…" entry names its host only under a per-line
// salt, so the pass cannot identify it and must not pretend it removed it. The
// connect still succeeds (the operator pinned the key), but the "remembered"
// claim has to be reported as failed rather than silently swallowed — otherwise
// the next pin-less run refuses the router as CHANGED with no explanation.
func TestHashedStoreLineIsReportedNotSilentlyIgnored(t *testing.T) {
	store := knownHostsStore(t)

	routerKey := hostKeySigner(t)
	router := startRogueRouter(t, routerKey)
	addr := net.JoinHostPort(router.host(), router.port())

	// Seed the store with a hashed entry for this address carrying an OLD key,
	// exactly as `ssh-keygen -H` would leave it.
	oldKey := hostKeySigner(t)
	line := knownhosts.Line([]string{addr}, oldKey.PublicKey())
	if _, rest, ok := strings.Cut(line, " "); ok {
		line = knownhosts.HashHostname(knownhosts.Normalize(addr)) + " " + rest
	}
	if err := os.WriteFile(store, []byte(line+"\n"), 0o600); err != nil {
		t.Fatalf("seed trust store: %v", err)
	}

	withTrustedFingerprint(t, ssh.FingerprintSHA256(routerKey.PublicKey()))
	client := dialRogueRouter(t, router)
	if client == nil {
		t.Fatalf("the explicitly pinned key was refused: %s", lastHostKeyRefusal(router.host()))
	}
	client.Close()

	// The warning path: remembering the key must REPORT that the store does not
	// resolve this host to the key just recorded, instead of silently claiming
	// success.
	if err := rememberHostKey(addr, routerKey.PublicKey()); err == nil {
		t.Errorf("rememberHostKey reported success for %s while a hashed entry still shadows the recorded key", addr)
	}
	if err := verifyStoreResolvesTo(store, addr, routerKey.PublicKey()); err == nil {
		t.Errorf("the store reportedly resolves %s to the freshly recorded key while a hashed entry still shadows it", addr)
	}

	// And the consequence is visible in behaviour, not just in the return value:
	// with no pin the router is still refused (first key wins per algorithm).
	withTrustedFingerprint(t, "")
	again := dialRogueRouter(t, router)
	if again != nil {
		again.Close()
		t.Fatal("a hashed entry that shadows the recorded key was nevertheless accepted")
	}
}
