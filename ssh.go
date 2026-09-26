package main

import (
	"bytes"
	"net"
	"os"
	"time"

	"golang.org/x/crypto/ssh"
)

// sshDialPort is the SSH port the wizard dials on the router. It is a variable
// only so the router-trust tests can point the real sshConnect path at an
// in-process SSH server; production always uses 22.
var sshDialPort = "22"

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
	config := &ssh.ClientConfig{
		User:            "root",
		HostKeyCallback: routerHostKeyCallback(ip),
		Timeout:         10 * time.Second,
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
		User:            "root",
		HostKeyCallback: routerHostKeyCallback(ip),
		Timeout:         10 * time.Second,
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
