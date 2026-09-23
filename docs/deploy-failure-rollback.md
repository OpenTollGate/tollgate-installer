# Deploy failure: who restores the router's wireless config

**Status:** decided, implemented, unit-tested and pinned by a static guard in the
Go suite; exercised end-to-end against the fixture-router harness
(`run-refusal-rollback.sh`, docker, no physical router). **NOT yet run against a
physical a53 router by this change** — see "What is not verified".

## Why

PR #40 added a fail-loud refusal to deploy step 6: when the requested feed
release cannot be downloaded and the only other candidate is the pinned GitHub
fallback (a *different, older* release the operator has not opted into), the
deploy stops and names both versions instead of silently downgrading.

The refusal returned immediately after `jobFail` and so never called the
wireless rollback that the code path it replaced performed. On the hardware this
targets (a53 bench routers, MT3000/MT6000) that is a dead end:

1. step 5 has already committed and reloaded the STA config for WAN-over-WiFi and
   left `/tmp/wireless.pre-tollgate`;
2. the requested feed tag's asset 404s (feed outage / tag not yet published) and
   the operator has not opted into the older fallback, so the refusal fires;
3. the job failed and returned with the radios still in STA mode. A radio hosting
   an STA iface cannot be used by the wizard to re-scan for an upstream SSID, so
   recovering needed a physical visit to the router — where the pre-#40
   fall-through at least left the radios usable for re-scanning.

## Decision

**The refusal restores the pre-deploy wireless snapshot before failing.**

The question is who owns the rollback: the failing step, or the operator. At the
point the refusal fires the operator-visible contract has *not* been delivered —
no package was installed, no branding, no portal, no health — and the only part
of the router this run changed is live radio config it cannot leave behind
usefully. Steps 6 and 11 both run after step 5 committed live radio config, and
both existing terminal failure paths there (the step-6 feed-install failure and
the step-11 health-check failure) already restore the snapshot "so the radios are
usable for re-scanning". A refusal is a terminal failure of step 6 exactly like
those, so:

- the deploy's failure contract is "the deploy failed and the router is left as
  it was found, radios usable for a re-run" — it must not depend on which branch
  failed;
- consistency is what makes the contract checkable: one helper decides, so a new
  failure branch cannot quietly diverge (the refusal did, because it was written
  as a new early return rather than routed through the existing behaviour).

Considered and rejected: leaving STA committed and only logging the deliberate
choice. It keeps the router's uplink (a re-run then needs no STA reconfiguration)
but it removes the wizard's ability to re-scan for the upstream SSID and leaves
the operator with a router whose radio is committed to an uplink the failed
deploy never used — the worse of the two failure modes, on the very scenario the
refusal exists for. The "state it in the log instead of acting" branch *is* used,
but only where it is true: when **no** STA was committed by this run (WAN mode,
or a run that failed before step 5) there is no snapshot, so nothing is restored
and the log claims nothing.

## What runs where

`deploy.go`:

- `wirelessRollback` — the seam the failure paths roll back through. A `var` so a
  unit test can observe the call without a router; it delegates to
  `rollbackWireless`, which is itself snapshot-guarded on the router
  (`[ -f /tmp/wireless.pre-tollgate ] && cp ... && uci commit wireless && wifi
  reload`) so an absent or stale snapshot is already a no-op there.
- `restoreWirelessOnFailure(job, client, staCommitted)` — the single place the
  decision is made. Restores (and logs "Rolling back wireless config to the
  pre-deploy snapshot...") only when this run committed wireless config, and
  returns whether it did, so callers cannot report a restore that did not happen.
- `refuseMissingRequestedRelease(...)` — step 6's fail-loud gate. Logs the
  refusal, calls `restoreWirelessOnFailure`, fails the job naming the requested
  tag, and reports that the caller must stop. Inert (returns `false`, touches
  nothing) when a package landed or no refusal was recorded, so the success path
  is unchanged.
- `staCommitted` — set once, immediately after step 5 succeeds, and passed to all
  three terminal failure sites (the step-6 refusal, the step-6 feed-install
  failure, the step-11 health failure).

A `go/ast` guard (`TestDeployFailureSitesRestoreWireless`) parses the real
`deploy.go` — the same technique as `TestDeployStepIndexGuard` — and fails if the
refusal is re-inlined instead of going through the gate, if `runDeployment` calls
`rollbackWireless` directly, or if the helper stops calling the seam.

## Open gap (tracked separately, not fixed here)

PR #43 adds two more step-6 terminal failures ("package integrity check failed")
*before* this helper existed; they return early after step 5 committed STA and so
have the same defect. They must call `restoreWirelessOnFailure` once #43 lands.

## What is not verified

The fixture router models the *installer's* behaviour and the router-side file
operations (snapshot, STA commit, restore) — not a real radio. It cannot show
that `wifi reload` on real a53 hardware brings the radios back to a scannable
state, only that the installer issues the restore and the fixture's
`/etc/config/wireless` is byte-identical to its pre-deploy content. The physical
check owed: on an MT3000/MT6000, run a deploy in STA mode against a feed tag whose
asset 404s, confirm the wizard fails loudly with the refusals text, then confirm
the router's radios are back in their pre-deploy (scannable) state and a re-run
succeeds once the asset exists.
