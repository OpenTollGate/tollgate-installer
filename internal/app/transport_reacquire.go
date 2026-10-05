package app

import (
	"fmt"
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
