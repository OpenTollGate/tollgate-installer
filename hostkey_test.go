package main

import (
	"crypto/ed25519"
	"crypto/rand"
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
