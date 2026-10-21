package app

import (
	"fmt"
	"net"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// Self-inflicted transport loss, and how the deploy now survives it.
//
// Step 10 (services) restarts networking ON THE ROUTER WE ARE DRIVING.
// tollgate-wrt's uci-defaults derive network.private from network.lan and reload
// the network, so the restart tears down the very SSH session that issued it.
//
// Observed on a GL-MT3000, 2026-10-05: the restart dropped the deploy interface's
// address, sshRun (nil-safe) swallowed the death, step 10 was reported DONE, and
// the failure only surfaced two steps later at the health gate as "lost the SSH
// transport" — which points the operator at a router that was never at fault and
// leaves them hunting for its new address by hand.
//
// Contract added here: after a step that knowingly restarts router networking,
// the deploy PROVES the session is alive; if it is not, it RE-ACQUIRES the
// transport within a bounded window before any later step runs.
// Vars, not consts: tests shrink the window so a give-up path is proved in
// milliseconds instead of minutes.
var (
	deployReacquireTimeout  = 120 * time.Second
	deployReacquireInterval = 5 * time.Second
)

// Seams. This package has no general SSH seam (sshTransportAlive needs a live
// session), so the decision below is made testable by indirecting the three SSH
// calls it depends on — the same pattern as wirelessRollback in deploy.go.
var (
	transportAliveFn = sshTransportAlive
	transportCloseFn = func(c *ssh.Client) { closeSSHClient(c) }
	transportDialFn  = func(ip, password string) *ssh.Client { return sshConnect(ip, password) }
)

// reacquireLoop is the retry policy, deliberately free of SSH so it can be
// tested directly. It always probes at least once, and never sleeps past the
// deadline (a probe already in flight may overrun it by its own duration).
func reacquireLoop(timeout, interval time.Duration, probe func() bool) (attempts int, ok bool) {
	deadline := time.Now().Add(timeout)
	for {
		attempts++
		if probe() {
			return attempts, true
		}
		if !time.Now().Before(deadline) {
			return attempts, false
		}
		time.Sleep(interval)
	}
}

// reacquireTransport dials the router until a LIVE session is back or the window
// closes. A client that dials but whose session is already dead is rejected, so
// the deploy never resumes onto a corpse. Returns nil if the router never came
// back — the caller then fails, with the reason already in the job log.
func reacquireTransport(job *Job, ip, password string, timeout, interval time.Duration) *ssh.Client {
	job.addLog(fmt.Sprintf(
		"Re-acquiring the SSH transport to %s (retrying for up to %s — the service restart reloads router networking)",
		ip, timeout))

	var client *ssh.Client
	attempts, ok := reacquireLoop(timeout, interval, func() bool {
		c := transportDialFn(ip, password)
		if c == nil {
			return false
		}
		if err := transportAliveFn(c); err != nil {
			transportCloseFn(c)
			return false
		}
		client = c
		return true
	})

	if !ok {
		job.addLog(fmt.Sprintf("Could not re-establish the SSH transport to %s after %d attempt(s) over %s",
			ip, attempts, timeout))
		return nil
	}
	job.addLog(fmt.Sprintf("Re-established the SSH transport to %s on attempt %d", ip, attempts))
	return client
}

// ensureTransportAfterRestart returns a client proven alive after a step that
// restarted networking on the router. A live session is returned untouched; a
// dead one is retired and re-acquired. The second return is the ORIGINAL
// transport error, so the caller can fail with the real cause.
func ensureTransportAfterRestart(job *Job, client *ssh.Client, ip, password string) (*ssh.Client, error) {
	if err := transportAliveFn(client); err == nil {
		return client, nil
	} else {
		job.addLog("SSH transport died during the service restart (expected — that step reloads router networking): " + err.Error())
		transportCloseFn(client)
		if nc := reacquireTransport(job, ip, password, deployReacquireTimeout, deployReacquireInterval); nc != nil {
			return nc, nil
		}
		return nil, err
	}
}

// routerCandidatesFn returns the addresses the deploy tries when it must find
// the router after a self-inflicted restart moved it. A var so a test can supply
// candidates without touching the network.
var routerCandidatesFn = func() []string {
	out := []string{}
	for _, r := range discoverRouters() {
		if r.IP != "" {
			out = append(out, r.IP)
		}
	}
	return out
}

// installRestartCandidates builds the addresses to try after the postinst moves
// the wired port. The private address captured before the restart is the exact
// destination; lan/24 ±1 covers a first install whose network.private did not
// exist yet; the old address is the last resort.
func installRestartCandidates(privateCIDR, lanCIDR, oldIP string) []string {
	out := make([]string, 0, 4)
	seen := map[string]bool{}
	add := func(raw string) {
		ip := strings.TrimSpace(raw)
		if i := strings.IndexByte(ip, '/'); i >= 0 {
			ip = ip[:i]
		}
		if ip == "" || net.ParseIP(ip) == nil || seen[ip] {
			return
		}
		seen[ip] = true
		out = append(out, ip)
	}
	add(privateCIDR)
	if lan := strings.TrimSpace(lanCIDR); lan != "" {
		if i := strings.IndexByte(lan, '/'); i >= 0 {
			lan = lan[:i]
		}
		if v4 := net.ParseIP(lan).To4(); v4 != nil {
			base := int64(v4[0])<<24 | int64(v4[1])<<16 | int64(v4[2])<<8 | int64(v4[3])
			for _, delta := range []int64{1, -1} {
				n := base + delta*256 // the /24 next to the LAN
				if n < 0 || n > 0xffffffff {
					continue
				}
				add(net.IPv4(byte(n>>24), byte(n>>16), byte(n>>8), byte(n)).String())
			}
		}
	}
	add(oldIP)
	return out
}

// reacquireAfterInstallRestart finds the router after the package INSTALL
// restarted its networking. The postinst runs /etc/init.d/network restart and
// 99-tollgate-setup derives network.private (lan/24 ±1), moving the wired port
// onto br-private — so the pre-deploy address is gone and the step-6 session
// dies with it. This is the install-time twin of ensureTransportAfterRestart
// (which only covers the step-10 service restart).
//
// Candidates: a fresh LAN scan leads with the OS default-route gateway, which is
// where the router lands once the wired client re-leases on br-private; the
// pre-deploy address is a last resort. Every attempt logs, so a legitimate
// ~2-minute reconnect keeps feeding the stall watchdog instead of being read as
// a hang. Returns the live client and the address it answered on, or (nil, "")
// when the router never came back within the bounded window.
func reacquireAfterInstallRestart(job *Job, oldIP, password string, extra ...string) (*ssh.Client, string) {
	job.addLog("The install moved the router: re-acquiring the SSH transport at its new address...")
	// The captured management address comes FIRST: a fresh LAN scan cannot see a
	// br-private address the host has not leased onto, but the installer can read
	// network.private before the postinst moves the port.
	cands := make([]string, 0, len(extra)+4)
	cands = append(cands, extra...)
	cands = append(cands, routerCandidatesFn()...)
	if oldIP != "" {
		cands = append(cands, oldIP)
	}
	seen := map[string]bool{}
	deadline := time.Now().Add(deployReacquireTimeout)
	for _, ip := range cands {
		if ip == "" || seen[ip] {
			continue
		}
		seen[ip] = true
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		perCandidate := 20 * time.Second
		if remaining < perCandidate {
			perCandidate = remaining
		}
		if c := reacquireTransport(job, ip, password, perCandidate, deployReacquireInterval); c != nil {
			return c, ip
		}
	}
	job.addLog("Could not re-acquire the SSH transport to the router after the install restart")
	return nil, ""
}
