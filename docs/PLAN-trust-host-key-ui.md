# Plan — router SSH host-key trust in the browser wizard

Status: implemented on `pr/trust-host-key-ui` (based on `main`; relies on the
PR #62 label for the installer-version wording, and does not duplicate it).
Scope: installer repo only. The feed package (`v0.6.0-alpha4-pre20`) is not
changed by this plan.

## Problem

On a freshly flashed router the Go wizard refuses to connect:

```
router SSH host key is not trusted: 192.168.1.1:22 presents SHA256:lXMW… and
no credentials were sent. Verify this fingerprint on the router's own console,
then re-run with --trust-host-key SHA256:lXMW… (curl|bash: prefix the command
with TOLLGATE_TRUST_HOST_KEY=SHA256:lXMW…).
```

This is the C2-I-01 safeguard working (`hostkey.go`): a `sysupgrade -n` re-keys
Dropbear, so the key the operator trusted before no longer matches, and the
password is withheld until the new key is trusted.

The defect is **operator experience**, not the gate:

1. The wizard only *renders* `ssh_refusal` (`index.html` `showTrustHint`). There
   is no control to trust the key it just showed, so a browser-first operator is
   dead-ended and must kill the process and relaunch with an env var.
2. The WiFi/pre-stage failure paths mislabel a host-key refusal as a password
   problem ("check router password and try Rescan", `index.html` wifiScan).
3. The fingerprint is only embedded in prose; the UI has no structured field to
   act on.

Separately, `curl: (22) … 500` on the binary download was a transient GitHub
asset failure (the `releases/latest` redirect is healthy), and the WAN-less
post-install surface gate is a probe-timing flake in the acceptance lane. Neither
is a product regression.

## Decisions (operator-confirmed)

1. **Repo/flow**: branch on `felixfelix-bot/tollgate-installer`, PR to
   `OpenTollGate/tollgate-installer` `main`.
2. **Consent**: a confirm dialog that shows the full fingerprint. Not typed
   re-entry.
3. **Versioning**: keep the installer's feed pre-number and rely on the PR #62
   label; do not adopt the alternate tag scheme (PR #63).

Residual risk to record in the PR body: a confirm-dialog-only consent lets a
same-LAN impostor answering the router's address be trusted with one click, which
is what C2-I-01's out-of-band requirement exists to prevent. Compensating
controls folded in here: the fingerprint is shown in a modal (not inline) with
the console-verification instruction, the button is secondary styled, and the
trust decision is written into the store so it is auditable.

## Part A — unblock the live box (runbook, no code)

1. Take the fingerprint from the refusal: `SHA256:lXMWKVxuSfxJ8T3T0AYnj+7M2AHsXrPHW8VOKQ0lozA`.
2. Verify it on the router console:
   `ssh-keygen -lf /etc/dropbear/dropbear_ed25519_host_key.pub` (and the
   `…_rsa_host_key.pub`).
3. Relaunch with the pin (or `--trust-host-key`; use `--bin ./tollgate-installer`
   to avoid another transient download failure):
   ```
   TOLLGATE_TRUST_HOST_KEY=SHA256:lXMWKVxuSfxJ8T3T0AYnj+7M2AHsXrPHW8VOKQ0lozA \
     bash <(curl -fsSL https://raw.githubusercontent.com/OpenTollGate/tollgate-installer/main/install-and-test.sh)
   ```
4. The key is remembered in `~/.tollgate-known-hosts` (it replaces the host's
   old line), so a later pin-less run is the demo path. One priming run per
   flash; booting the same flash again needs no pin.

## Part B — durable fix

### B1 — structured fingerprint
- `hostkey.go`: the refusal registry stores the message only. Extend it to store
  `{message, fingerprint}` for the most recent refusal; add
  `lastHostKeyFingerprint(ip)`. `recordHostKeyRefusal` takes the fingerprint
  (all five call sites already have it in scope).
- `discover.go`: add `SSHFingerprint string \`json:"ssh_fingerprint,omitempty"\``
  to `RouterInfo`, set alongside `SSHRefusal`. `/api/identify` and `/api/scan`
  then carry it unchanged.

### B2 — `POST /api/trust-host-key`
- Body `{ip, fingerprint}`. Reject a malformed/empty fingerprint (400) and a
  missing IP (400).
- Dial with a **function-scoped** host-key callback that accepts only the supplied
  fingerprint; on a match, remember the key via the existing `rememberHostKey`.
  On mismatch, record the refusal and return the operator text. Never touches the
  process-global `--trust-host-key`, so trust is per host.
- No auth methods are configured, so the endpoint never offers a credential — its
  job is only to pin the host key.
- Registered in `main.go`; documented in the README API table.

### B3 — UI
- A secondary "Trust this router's SSH host key" button, shown only when the
  selected router carries a refusal/fingerprint.
- Clicking it opens a confirm dialog naming the full fingerprint and telling the
  operator to verify it on the console first; on confirm it POSTs the endpoint,
  then clears the hint and re-identifies / re-stages.
- `wifiScan()` detects a host-key refusal in the error and routes to the trust
  action instead of appending "check router password and try Rescan".

### B4 — tests
- Go (`trust_api_test.go`): the endpoint accepts the presented key and remembers
  it (a later pin-less `sshConnect` succeeds); refuses a mismatched fingerprint;
  rejects malformed input; and offers **no** credential to the fixture router on
  any path. Preserve the no-`InsecureIgnoreHostKey` guard.
- Shell (`scripts/test-trust-host-key-ui.sh`): a RED/GREEN source-pin that the
  shipped `index.html` has the trust control and the misattribution guard is
  present.

### B5 — release
- Branch is based on `main` and independent of PR #62 (the label fix and this
  change touch different regions of `index.html`). PR to org `main`, then cut the
  installer RC used by `install-and-test.sh`.

## Part C — follow-up (after B)

Green the WAN-less acceptance lane (not the Go installer): bound the post-install
surface probes with retry/backoff, and treat `:2121/balance` `503` as the
documented degraded shape while the mint is unreachable. Re-run
`pre20b-wanless` for a green verdict.

## Verification

```
go build ./...
go test ./...
scripts/test-trust-host-key-ui.sh
scripts/test-installer-version-not-conflated-with-feed.sh
```

RED control: the new tests fail on the pre-change base (35db30d) and pass on the
head. No `ssh.InsecureIgnoreHostKey` anywhere in the install path.

## Follow-up fix, 2026-10-01: the Trust button became an unbreakable loop

Operator report: *"the trust host key button doesn't work"* — with three identical
errors in the UI:

```
{"error":"ssh: handshake failed: router SSH host key mismatch: 192.168.1.1:22
 presents SHA256:iKF/TR/LQ6vMVgrngAyW6ipSfn3GO9iJUfzXYNujQKw but
 SHA256:p1IVyP8jjaMhmY0G1OKskIWzotj5n1A6LTkOq4Ef1RY was supplied and nothing was trusted."}
```

**Cause.** `trustHostKeyForHost` records the fingerprint the router actually
presents, and the refusal prose names it — but `handleTrustHostKey` returned it
only as *prose*, and `index.html`'s `!resp.ok` branch refreshed the hint text
without updating `window._trustFingerprint`. The next click therefore re-posted
the value cached when the prompt was drawn. Once the presented key had changed
once, **every** click failed identically: the operator was told to "correct the
value and retry" with no way to correct it.

**Fix.** The refusal now carries the presented fingerprint structurally, in
`ssh_fingerprint` — the same field the scan refusal already uses (`discover.go`)
— and the UI refreshes its cached value from it, telling the operator which
fingerprint to verify next.

**Constraint 1 is preserved, deliberately.** Refreshing the cached value only
changes what the *next* confirm dialog asks about; the new value is not trusted.
A changed host key is exactly the impersonation case, so it still requires an
out-of-band console check and a fresh explicit confirmation. Auto-trusting the
refreshed value would have turned one click on a stale prompt into trust for a
host the operator never verified.

**Environment note.** It surfaced under the same conditions as the 2026-09-28
incident above: **two devices answering at `192.168.1.1`** (a gateway and the
test router), so the presented key changed between connections. The fix removes
the dead end; it does **not** make a conflicting address safe to trust. Topology
first (one device per address), then the console check, then Trust.
