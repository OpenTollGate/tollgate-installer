package app

import (
	"bytes"
	"errors"
	"net"
	"os"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// sshDialPort is the SSH port the wizard dials on the router. It is a variable
// only so the router-trust tests can point the real sshConnect path at an
// in-process SSH server; production always uses 22.
var sshDialPort = "22"

// sshConnectErrors records WHY the most recent connect attempt to an address
// failed, and whether a password was offered on it.
//
// WHY (2026-10-02): sshConnect returns only a *ssh.Client, so the dial/auth error
// was discarded and sshConnectFailureMessage could say nothing but its generic
// fallback. The operator report is the cost of that —
//
//	XHR POST http://localhost:8099/api/wifi-scan  [HTTP/1.1 502 Bad Gateway]
//	{"error":"cannot connect to router via SSH"}
//
// — which names neither the cause nor the action. A wrong password, a router
// with a root password that was never typed (the Repeater tab scans
// automatically), nothing listening on :22 and an unroutable address were ALL
// that same sentence, so the operator had no way to tell a fixable problem from
// a broken one.
var (
	sshConnectErrorsMu sync.Mutex
	sshConnectErrors   = map[string]sshConnectError{}
)

// sshConnectError is one recorded failure. passwordSupplied is what separates
// "you did not give a password" from "the password you gave was refused" — the
// same dial error, two different operator actions.
type sshConnectError struct {
	err              error
	passwordSupplied bool
}

// forgetSSHConnectError drops the recorded failure for ip. Called at the START of
// every attempt, so a recorded cause can only ever describe the LATEST one — a
// successful connect therefore clears it and a stale message cannot outlive the
// condition it described.
func forgetSSHConnectError(ip string) {
	sshConnectErrorsMu.Lock()
	delete(sshConnectErrors, ip)
	sshConnectErrorsMu.Unlock()
}

func recordSSHConnectError(ip string, err error, passwordSupplied bool) {
	sshConnectErrorsMu.Lock()
	sshConnectErrors[ip] = sshConnectError{err: err, passwordSupplied: passwordSupplied}
	sshConnectErrorsMu.Unlock()
}

func lastSSHConnectError(ip string) (error, bool) {
	sshConnectErrorsMu.Lock()
	defer sshConnectErrorsMu.Unlock()
	e, ok := sshConnectErrors[ip]
	if !ok {
		return nil, false
	}
	return e.err, e.passwordSupplied
}

// sshHostKeyAlgorithms pins the ORDERED host-key preference for every SSH
// dial in this package.
//
// WHY (2026-09-28): these configs previously set no HostKeyAlgorithms, so
// golang.org/x/crypto/ssh negotiated by its own preference and picked RSA
// against an OpenWrt box serving BOTH ed25519 and rsa. An operator verifying
// on the router console (dropbearkey -y) or with ssh-keyscan -t ed25519 saw a
// DIFFERENT fingerprint than the installer, so a correct pin could never
// match. ed25519 is first because that is what every operator-facing check
// reports. rsa/rsa-sha2 remain as fallbacks: older firmware may ship only
// an RSA host key.
var sshHostKeyAlgorithms = []string{
	ssh.KeyAlgoED25519,
	ssh.KeyAlgoRSASHA256,
	ssh.KeyAlgoRSA,
}

// sshConnect establishes an SSH session to the router.
//
// Auth chain, tried in order (a fresh-reset OpenWrt router ships root with
// an EMPTY password, while an already-configured one has the operator's
// password — the wizard must handle both):
//  1. Password(user-supplied)  — configured routers (v0.5.0 back-compat)
//  2. Password("")             — fresh routers, password auth
//  3. KeyboardInteractive      — fresh routers whose dropbear only accepts
//     (answers = password)       keyboard-interactive for the empty password
//  4. Default SSH keys          — key-provisioned routers, if present
//
// The router's host key is verified before any credential is offered (see
// routerHostKeyCallback): an untrusted key aborts the handshake, so the password
// below is never transmitted to a host the operator has not trusted.
func sshConnect(ip, password string) *ssh.Client {
	forgetHostKeyRefusal(ip)
	forgetSSHConnectError(ip)
	config := &ssh.ClientConfig{
		User:              "root",
		HostKeyCallback:   routerHostKeyCallback(ip),
		Timeout:           10 * time.Second,
		HostKeyAlgorithms: sshHostKeyAlgorithms,
	}

	auth := []ssh.AuthMethod{}
	if password != "" {
		auth = append(auth, ssh.Password(password))
	}
	auth = append(auth,
		ssh.Password(""),
		keyboardInteractiveAuth(password),
	)
	if signer := tryDefaultKeys(); signer != nil {
		auth = append(auth, ssh.PublicKeys(signer))
	}
	config.Auth = auth

	client, err := ssh.Dial("tcp", net.JoinHostPort(ip, sshDialPort), config)
	if err != nil {
		// Keep WHY it failed: the caller renders this to the operator (see
		// sshConnectFailureMessage). Discarding it here is what made every
		// non-host-key failure the same unactionable sentence.
		recordSSHConnectError(ip, err, password != "")
		return nil
	}
	return client
}

// keyboardInteractiveAuth answers every keyboard-interactive challenge with
// the given password ("" for a fresh router). The callback MUST return
// exactly one answer per question or x/crypto/ssh fails the auth attempt.
func keyboardInteractiveAuth(password string) ssh.AuthMethod {
	return ssh.KeyboardInteractive(func(user, instruction string, questions []string, echos []bool) ([]string, error) {
		answers := make([]string, len(questions))
		for i := range answers {
			answers[i] = password
		}
		return answers, nil
	})
}

// proveRootPassword opens a FRESH SSH session to ip and authenticates as root
// with exactly the candidate password — nothing else — reporting whether the
// router accepted it.
//
// It exists to prove that a password CHANGE took (deploy step 4, see
// applyRootPassword): re-reading /etc/shadow only shows that SOME hash is
// present, so on a set→set transition a passwd that fails silently leaves the
// OLD hash in place and the re-probe sees the `set` state it is looking for
// (#46 follow-up review).
//
// Every fallback sshConnect() carries is deliberately ABSENT here:
//
//   - no empty-password retry: a fresh OpenWrt router accepts an empty password,
//     so a fallback would report success for a credential the router never
//     adopted — the same false-proof shape this function exists to remove;
//   - no default-key authentication: a key-provisioned router would authenticate
//     without the password, proving nothing about the shadow hash.
//
// The candidate itself crosses as both `password` and keyboard-interactive
// (dropbear can present either method for a password login — see sshConnect);
// both are offers of the SAME candidate, so neither widens the proof.
//
// It returns false on any failure — the caller must fail the deploy (fail
// closed), never assume the password took. NOTE the empty-hash premise: on a
// router whose root hash is EMPTY this returns true for ANY candidate, so it is
// only a proof where the router had a real credential before the write; that is
// the caller's condition (applyRootPassword).
//
// The router's host key is verified before the candidate is offered, exactly as
// for the deploy session (routerHostKeyCallback): an untrusted key aborts the
// handshake, so the candidate is never sent to a host the operator has not
// trusted.
func proveRootPassword(ip, password string) bool {
	if password == "" {
		// An empty candidate is not a proof of anything: an empty-hash router
		// accepts it and a router with a real hash never does, so report refusal
		// without sending it anywhere. (applyRootPassword only calls this for a
		// non-empty candidate, and a generated credential is never empty.)
		return false
	}
	config := &ssh.ClientConfig{
		User:              "root",
		HostKeyCallback:   routerHostKeyCallback(ip),
		Timeout:           10 * time.Second,
		HostKeyAlgorithms: sshHostKeyAlgorithms,
		Auth: []ssh.AuthMethod{
			ssh.Password(password),
			keyboardInteractiveAuth(password),
		},
	}
	client, err := ssh.Dial("tcp", net.JoinHostPort(ip, sshDialPort), config)
	if err != nil {
		return false
	}
	closeSSHClient(client)
	return true
}

// closeSSHClient closes a deploy SSH client, tolerating nil.
//
// The nil case is real and load-bearing: a subnet relocation that loses the
// router (see moveLocalSubnet / fixSubnetCollisions) hands the deploy back a nil
// client, and runDeployment's deferred cleanup then called client.Close() on it.
// That nil dereference panicked the deploy GOROUTINE (main.go runs
// `go runDeployment(...)`), which killed the whole wizard process mid-deploy —
// strictly worse than the failed deploy it replaced (BLOCK 2 of the #52 review).
func closeSSHClient(client *ssh.Client) {
	if client != nil {
		client.Close()
	}
}

// sshTransportAlive probes the SSH TRANSPORT itself (not the router's
// services): it opens a session and runs `true`. A closed or half-dead
// transport — the router was re-addressed, rebooted, or the cable moved to
// another port — errors here, whereas sshRun would silently return "".
//
// WHY (live defect, 2026-10-03): on a real GL-MT3000 the deploy lost the router
// around the network/upstream step. Every subsequent sshRun returned "" and the
// wizard reported "tollgate-wrt service is NOT listening on :2121 (crash-looping
// or still initializing)" for 40 attempts, with a diagnostics block that printed
// every label EMPTY (service:/proc:/date:/mints:/internet:/dns:/log:/debug:).
// The operator chased a service problem that did not exist; the real cause —
// a dead SSH session to a router that had left the LAN — was invisible.
func sshTransportAlive(client *ssh.Client) error {
	if client == nil {
		return errors.New("no SSH client — the router connection was lost (relocation, reboot, or a moved cable)")
	}
	session, err := client.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()
	done := make(chan error, 1)
	go func() { done <- session.Run("true") }()
	select {
	case err := <-done:
		return err
	case <-time.After(8 * time.Second):
		return errors.New("SSH exec did not answer within 8s — the transport is dead")
	}
}

// sshRun executes a command and returns combined output. client MUST be live:
// it is dereferenced here, so callers that may hold a nil client (a relocation
// that lost the router) must check first — see adoptRelocatedClient.
func sshRun(client *ssh.Client, cmd string) string {
	session, err := client.NewSession()
	if err != nil {
		return ""
	}
	defer session.Close()
	output, err := session.CombinedOutput(cmd)
	return string(output)
}

// sshUploadPipe writes binary data to the router via SSH stdin.
func sshUploadPipe(client *ssh.Client, data []byte, extractCmd string) string {
	session, err := client.NewSession()
	if err != nil {
		return ""
	}
	defer session.Close()
	session.Stdin = bytes.NewReader(data)
	output, err := session.CombinedOutput(extractCmd)
	return string(output)
}

// sshWriteFile writes content to a remote path via SSH (cat > path).
func sshWriteFile(client *ssh.Client, remotePath string, content []byte) error {
	session, err := client.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()

	stdin, err := session.StdinPipe()
	if err != nil {
		return err
	}

	if err := session.Start("cat > " + remotePath); err != nil {
		return err
	}

	_, err = stdin.Write(content)
	if err != nil {
		return err
	}
	stdin.Close()

	return session.Wait()
}

// tryDefaultKeys attempts to load the default SSH key.
func tryDefaultKeys() ssh.Signer {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	for _, p := range []string{
		home + "/.ssh/id_ed25519",
		home + "/.ssh/id_rsa",
		home + "/.ssh/id_ecdsa",
	} {
		key, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		signer, err := ssh.ParsePrivateKey(key)
		if err == nil {
			return signer
		}
	}
	return nil
}
