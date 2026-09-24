# Deploy failure: who restores the router's wireless config

**Status:** decided, implemented, unit-tested and pinned per-site by a static guard
in the Go suite; exercised end-to-end against the fixture-router harness
(`run-terminal-rollback.sh`, docker, no physical router) for the step-5, step-6 and
step-6-install failure sites. **NOT yet run against a physical a53 router by this
change** — see "What is not verified".

## Why

PR #40 added a fail-loud refusal to deploy step 6: when the requested feed release
cannot be downloaded and the only other candidate is the pinned GitHub fallback (a
*different, older* release the operator has not opted into), the deploy stops and
names both versions instead of silently downgrading.

The refusal returned immediately after `jobFail` and so never called the wireless
rollback that the code path it replaced performed. On the hardware this targets
(a53 bench routers, MT3000/MT6000) that is a dead end:

1. step 5 has already committed and reloaded the STA config for WAN-over-WiFi and
   left `/tmp/wireless.pre-tollgate`;
2. the requested feed tag's asset 404s (feed outage / tag not yet published) and
   the operator has not opted into the older fallback, so the refusal fires;
3. the job failed and returned with the radios still in STA mode. A radio hosting
   an STA iface cannot be used by the wizard to re-scan for an upstream SSID, so
   recovering needed a physical visit to the router — where the pre-#40
   fall-through at least left the radios usable for re-scanning.

PR #47 fixed that one site. A gate-1 review of it then found that the sentence
"no terminal failure after step 5 can forget the wireless restore" was, at the
time, a claim about **three** sites out of twelve: seven more terminal failures
in `runDeployment` still returned with the STA config committed, two step-5
failure paths inside `configureSTA` could not use the gate at all, and the
restore itself only covered `/etc/config/wireless` while the same commit pair also
wrote `network.wwan`. This document (and the code) is the correction.

## Decision

**Every terminal failure of a deploy that committed live radio config restores it
before failing, and the restore covers everything that commit wrote.**

The question is who owns the rollback: the failing step, or the operator. At the
point any of these failures fire the operator-visible contract has *not* been
delivered — no package installed, no branding, no portal, no health — and the only
part of the router the run changed for its own purposes is radio + network config
it cannot leave behind usefully. Steps 6–11 all run after step 5 committed live
radio config, so:

- the deploy's failure contract is "the deploy failed and the router's wireless
  config is left as it was found, radios usable for a re-run" — it must not depend
  on which branch failed;
- consistency is what makes the contract checkable: one exit decides, so a new
  failure branch cannot quietly diverge (the refusal did, because it was written
  as a new early return rather than routed through the existing behaviour).

Considered and rejected: leaving STA committed and only logging the deliberate
choice. It keeps the router's uplink (a re-run then needs no STA reconfiguration)
but it removes the wizard's ability to re-scan for the upstream SSID and leaves
the operator with a router whose radio is committed to an uplink the failed
deploy never used — the worse of the two failure modes, on the very scenario the
refusal exists for. The "state it in the log instead of acting" branch *is* used,
but only where it is true: when **no** STA was committed by this run (WAN mode, or
a run that failed before step 5) there is no snapshot, so nothing is restored and
the log claims nothing.

### What "left as it was found" means exactly

The step-5 commit rewrites two config files, and the restore is defined against
both:

| changed by the run | restored on failure? |
|---|---|
| `/etc/config/wireless` (STA iface, disabled duplicate STA iface, radio enabled) | yes — byte-restored from `/tmp/wireless.pre-tollgate` and `wifi reload`ed |
| `network.wwan` (interface section + `proto dhcp`) | yes — **deleted** when this run created the section, or its pre-deploy `proto` put back when the router already had one (`uci` has no per-section export, so the snapshot is existence + the one option this script writes) |
| `network.lan` / `network.private` (**relocated** by `fixSubnetCollisions` when they collide with the upstream subnet) | **no, deliberately.** The relocation is what makes the router usable at all (a local subnet that overlaps the upstream makes the router route the upstream's own DNS server to itself). Reverting it on failure would also re-break name resolution for the operator who is still connected through it, and could leave the operator's client holding a lease from the retired range. It is a *working* change, not a leftover of a failed one; it is reported in the deploy log at the moment it happens |
| the installed package / `uci-defaults` the package ran (steps 6+ failures) | no — the failure is *about* that state, and the operator needs it to diagnose; the wireless side is what blocks recovery |
| nothing (WAN mode, or a failure before step 5) | nothing to restore — no snapshot exists and `/tmp/wireless.pre-tollgate` may hold a **stale** snapshot from an earlier run, so the restore is not attempted and the log claims nothing (see "Open gaps", item 4) |

## What runs where

`deploy.go`:

- `rollbackWireless` / `rollbackWirelessCmd` — the router-side restore, as one
  shell command (a const, so the unit suite can pin its content without a live
  client). It restores the wireless snapshot and reloads wifi, then puts
  `network.wwan` back (delete / proto restore). It is snapshot-guarded on the
  router (`[ -f /tmp/wireless.pre-tollgate ]`, `[ -f
  /tmp/network.wwan.pre-tollgate ]`), so an absent or stale snapshot is a no-op
  there as well.
- `wirelessRollback` — the seam the failure paths roll back through. A `var` so a
  unit test can observe the call without a router; it delegates to
  `rollbackWireless`.
- `restoreWirelessOnFailure(job, client, staCommitted)` — the single place the
  decision is made. Restores (and logs "Rolling back wireless config to the
  pre-deploy snapshot...") only when this run committed wireless config, and
  returns whether it did, so callers cannot report a restore that did not happen.
- `restoreWirelessFromSTACommit(job, client)` — the same gate, for the step-5
  failure paths *inside* `configureSTA`: there a commit has already happened (a
  successful association) while `runDeployment`'s `staCommitted` is still `false`
  (it is only set once `configureSTA` returns true), so no caller-level gate can
  fire yet and the failing path states the commit itself.
- `jobFailAfterRestore(job, client, staCommitted, step, stepDetail, jobErr)` — the
  terminal-failure exit **every** post-step-5 failure uses: restore (through the
  gate) first, then `jobFail`, and the operator-facing error gains
  "— pre-deploy wireless config restored" only when it really ran.
- `attemptSTA` — returns `leftCommitted` for the one case it cannot clean up: the
  STA config was committed (`STA_CFG_OK`) and the router then stopped answering
  SSH entirely, so there was no session left to roll it back through. The callers
  (`configureSTA`, `testSTAConfig`) get one more chance to restore when the router
  answers again and otherwise say so instead of claiming a rollback.
- `staCommitted` — set once, immediately after step 5 succeeds, and passed to
  every post-step-5 failure exit.
- The STA setup script records `ABSENT`/`EXISTED` + the pre-deploy `proto` for
  `network.wwan` next to the wireless snapshot, *before* it writes the section.

A `go/ast` guard (`TestDeployFailureSitesRestoreWireless`) parses the real
`deploy.go` — the same technique as `TestDeployStepIndexGuard` — and pins
**per-site** coverage, not a call count:

- no bare `jobFail` with a literal step ≥ 6 may remain in `runDeployment`;
- the `jobFailAfterRestore` sites must be *exactly* the registry
  `postStep5FailureSites` in `wireless_rollback_test.go` (ten sites: step 6 × 6,
  step 8 × 1, step 10 × 1, step 11 × 2) — so removing a restore and adding an
  unregistered failure site both fail the build;
- the refusal must still be reached through `refuseMissingRequestedRelease`,
  `runDeployment` must not call `rollbackWireless` directly, the shared exit must
  restore **before** `jobFail`, `restoreWirelessFromSTACommit` must delegate to the
  gate, and `rollbackWirelessCmd` must still cover the `network.wwan` half.

The earlier check (`strings.Count(runBody, "restoreWirelessOnFailure(") >= 2`)
stayed green while the seven `runDeployment` sites this document is named after
returned without restoring — a count is not coverage. It is now gone.

### The sites, and what each one is

| step | failure | exit |
|---|---|---|
| 5 | association succeeded but the upstream has no internet (after a reload retry) | `restoreWirelessFromSTACommit` + `jobFail` |
| 5 | every radio failed to associate | `attemptSTA` rolls back per attempt; a radio whose commit could not be rolled back (router unreachable) is reported in the failure text |
| 6 | requested release unavailable, fallback not opted into (the #40 refusal) | `refuseMissingRequestedRelease` (restores, then fails) |
| 6 | undetectable CPU arch | `jobFailAfterRestore` |
| 6 | arch with no package URL (defensive: `selectPkgURL` is generic today) | `jobFailAfterRestore` |
| 6 | `opkg: Not downgrading` — **also a status bug fix**: this used `setStep(6,"error")` + `return`, leaving `job.Status` "running" for ever (the wizard spun with no error) | `jobFailAfterRestore` |
| 6 | `apk` reports an install/upgrade failure | `jobFailAfterRestore` |
| 6 | installed version is not the requested release | `jobFailAfterRestore` |
| 6 | feed last resort: no package (on router or in the feed) | `jobFailAfterRestore` |
| 8 | captive portal assets missing (dead-portal regression) | `jobFailAfterRestore` |
| 10 | `tollgate-wrt` init script missing | `jobFailAfterRestore` |
| 11 | health check: API up, no pricing advertisement | `jobFailAfterRestore` |
| 11 | health check: service not listening on :2121 | `jobFailAfterRestore` |

## Open gaps (each one deliberate, and none of them is "forgot to restore")

1. **PR #43's two step-6 "package integrity check failed" sites** (tracked as
   `t_3fe64c6e`) return early after step 5 committed STA and so have the same
   defect. They do not exist on this branch — #43 is still open — and must call
   `jobFailAfterRestore` once it lands. #43 predates this exit, exactly as the
   refusal predated `restoreWirelessOnFailure`.
2. **A committed STA config the router stopped answering SSH for** cannot be
   restored by the installer: there is no session to run the restore in.
   `attemptSTA` reports that case (`leftCommitted`), `configureSTA` retries the
   restore once the router answers again, and when it cannot the failure text says
   the radio is still committed and cannot scan — the operator re-runs the deploy.
   Anything else would be a claim the installer cannot back with an action.
3. **Subnet relocations and install artifacts are not reverted** — see the table
   above for why (each is either a *working* change or the very state being
   diagnosed).
4. **Snapshot pollution across runs (accepted, from #47).** The STA script
   snapshots whatever `/etc/config/wireless` + `network.wwan` look like when it
   runs, so a re-run after a deploy that failed to restore re-snapshots the
   already-STA-committed state: a later restore returns to *that* point, not to
   factory. The snapshot files live in `/tmp` (cleared on reboot), so a router
   power-cycle resets the baseline. Related and also accepted: a **WAN-mode** run
   no longer cleans up a stale `/tmp/wireless.pre-tollgate` (and now
   `/tmp/network.wwan.pre-tollgate`) + committed STA left by an earlier failed STA
   run — WAN mode takes no snapshot, restores nothing, and claims nothing.
5. **`network.wwan` is restored at existence + `proto` level, not option by
   option.** `uci` offers no per-section export/import, and hand-parsing a section
   back out of `uci show` for a whole-section revert risks corrupting the
   operator's network config — the exact failure mode this codebase already warns
   about. The script writes only the section and its `proto`, so that is what the
   snapshot records.

## What is not verified

The fixture router models the *installer's* behaviour and the router-side file
operations (snapshot, STA commit, restore, `network.wwan` create/delete) — not a
real radio, and not real `uci` (the fixture's `uci` is a file-backed shim). It
cannot show that `wifi reload` on real a53 hardware brings the radios back to a
scannable state, nor that `uci -q delete network.wwan` + `uci commit network`
behaves as modelled on OpenWrt 24.10/25.12. The physical check owed: on an
MT3000/MT6000, run a deploy in STA mode against a feed tag whose asset 404s,
confirm the wizard fails loudly with the refusals text, then confirm the router's
radios are back in their pre-deploy (scannable) state, that `uci show network`
has no leftover `wwan` section, and that a re-run succeeds once the asset exists.
