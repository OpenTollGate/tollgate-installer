# Deploy transport: the deploy that kills its own SSH session

**Status:** decided and implemented; pinned by nine unit tests
(`deploy_transport_reacquire_test.go`). The **failure** path is observed live; the
**recovery** path is unit-tested only — see "What is not verified".

## Why

Step 10 (`services`) restarts `tollgate-wrt` **on the router the deploy is driving
over**. The package's `uci-defaults` derive `network.private` from `network.lan`
and reload networking, so the restart tears down the very SSH session that issued
it. The restore is not optional and not avoidable: it is how the package claims
its addresses.

Three things then went wrong together, and only the third was reported:

1. **The death was swallowed.** Step 10 read its output through `sshRun`, which
   is nil-safe by design (so a caller cannot park a job on a dead session) — and
   therefore returns an empty string and no error when the session dies. The step
   was marked `done`.
2. **The deploy carried on.** Steps 10.5 (`fixSubnetCollisions`) and 11 (health)
   ran against a corpse.
3. **The router was blamed.** The only visible failure was the health gate's
   `sshTransportAlive` check two steps later:

   ```
   Lost the SSH transport to the router before the health check:
   read tcp 10.117.96.100:52506->10.117.96.1:22: read: connection timed out
   ```

   …which reads as a router fault, and whose operator text sends the operator to
   find the router's new address by hand — for a failure the deploy caused itself.

Observed end-to-end on a GL-MT3000 (OpenWrt 25.12.5), 2026-10-05, deploying
`0.6.0_alpha2_pre9` from the wizard's own HTTP API. Every step up to and including
10 reported `done`; the restart logged `Services restarted:` with an empty body.

## Decision

**A step that knowingly restarts networking on the router must prove its own
session afterwards, and re-acquire it within a bounded window before any later
step runs.**

The alternative — leave it to the health gate — is not a gate but a misattribution:
by the time the health check runs, the deploy can no longer tell "the router is
broken" from "I broke the connection and never looked back".

- **Prove, then recover.** `ensureTransportAfterRestart` checks the session; a live
  session is returned untouched, a dead one is retired and re-acquired.
- **Bounded.** 120 s, retrying every 5 s. Each attempt is a full dial plus a
  liveness check, so a router that never returns costs one bounded pause, not a
  hang. The window is a `var`, not a `const`, so the give-up path is proved in
  milliseconds instead of minutes.
- **Never resume onto a corpse.** A client that *dials* but whose session is
  already dead is rejected (`reacquireTransport` closes it and keeps retrying).
  Accepting it would move the same misattribution one step later.
- **Fail with the real cause.** If re-acquisition fails, the caller fails at
  **step 10** with the original transport error, and the log says how many
  attempts were made over how long. It no longer reports a health-check failure.

## What runs where

`transport_reacquire.go` (new):

- `reacquireLoop(timeout, interval, probe)` — the retry policy, deliberately free
  of SSH so it is testable directly. Always probes at least once; never sleeps
  past the deadline.
- `reacquireTransport(job, ip, password, timeout, interval)` — dials until a live
  session is back; returns nil if the router never came back. Logs the start and
  the outcome (which also feeds the no-progress watchdog).
- `ensureTransportAfterRestart(job, client, ip, password)` — the deploy-facing
  decision, returning the original error when recovery fails.
- Seams `transportAliveFn` / `transportCloseFn` / `transportDialFn` — this package
  has no general SSH seam (`sshTransportAlive` needs a live session), so the three
  SSH calls this decision depends on are indirected. Same pattern as
  `wirelessRollback` in `deploy.go`.

`deploy.go` — step 10 only:

- the restart's output is read through the error-returning `sshRunE` instead of
  nil-safe `sshRun`, so the deploy can see that its own command died;
- a transport error is logged as *expected*, not as evidence of a router fault;
- `ensureTransportAfterRestart` runs before the collision re-check and the health
  gate, and a failure there fails **step 10** through `failTransportLost`.

## What is verified

- Nine unit tests, all green: the retry policy's deadline and success behaviour;
  re-acquire across repeated dials; rejection of a connected-but-dead session;
  a live session being left alone (no dial); successful recovery; and the
  original error surviving a failed recovery.
- A source guard (`TestDeployReacquiresAfterTheServiceRestart`) pins the wiring:
  `deploy.go` must call `ensureTransportAfterRestart` after the restart and must
  read the restart through `sshRunE` — so removing either half fails the build.
- The failure path was observed on the physical router as described above.

## What is not verified

- **The recovery path has not been observed on real hardware.** On the run above
  the router never came back at all, so the deploy took the give-up branch. No
  physical run has yet seen a restart drop the session and the re-acquire succeed
  — that needs a router whose session dies and returns inside 120 s. Until then,
  "recovery puts a real deploy back on the rails" rests on unit tests plus the
  dial/liveness primitives (`sshConnect`, `sshTransportAlive`) already used
  elsewhere.
- **Re-acquisition re-dials the SAME address.** If the restart *re-addresses* the
  deploy interface rather than merely dropping the session, dialling
  `req.IP` cannot succeed and this change reports an honest bounded failure
  instead of a recovery. The run above looks like that case: the deploy lost
  `br-private` entirely and `10.117.96.1` never answered again. Recovery by MAC
  re-discovery on the deploy interface is the obvious follow-up and is **not**
  implemented here.
