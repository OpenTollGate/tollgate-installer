# The router's device identity — one code for hostname, captive SSID and private SSID

**Status:** implemented, unit-tested offline; NOT yet run against a physical
router by this change (see "What is not verified").

The decision record is
[`docs/architecture/one-device-code.md`](https://github.com/OpenTollGate/tollgate-module-basic-go/blob/main/docs/architecture/one-device-code.md)
in `OpenTollGate/tollgate-module-basic-go` — that repo owns the contract. This
document covers the installer's half of it.

## Why

The wizard used to mint a **fresh** four-character suffix on *every* deploy
(`crypto/rand`, `nodeName := "tollgate-" + suffix`) and write it to the hostname
and the captive SSID only. The module's own uci-defaults script minted a
different suffix on every full setup, and minted the private SSID from a third
value. Nothing was ever stored, so nothing could ever be reused.

Measured on the bench MT3000 (2026-09-26): `hostname=tollgate-OQ3Q`, the open
SSID was `tollgate-OQ3Q` in the morning and `tollgate-0GLK` after a later
deploy, and the private SSID carried a suffix from a third mint path. Three
names for one router, and no way to tell at a glance which box you were on.

## What the installer does now

One code, four characters of `[A-Z0-9]`, stored in `/etc/config/tollgate`
(`config device 'device'` → `option code`, `option nym`) and **reused**:

| Identifier | Value |
|---|---|
| hostname | `tollgate-<code>` |
| captive SSID | `TollGate-<code>` |
| private SSID | `<nym>-<code>` |

The installer no longer mints when it can adopt. Step 7 runs
`deviceIdentityScript` **on the router** (`deploy.go`), which:

1. reads `tollgate.device.code` from the store — authoritative, never re-minted;
2. else adopts the code from a machine-shaped hostname (`tollgate-OQ3Q`);
3. else from a machine-shaped captive SSID (`TollGate-OQ3Q`, `tollgate-0GLK`);
4. else mints (BusyBox `hexdump`, no `od` on the target) and **stores** it.

It also resolves the nym for the private SSID (stored value, else the prefix of
a machine-shaped private SSID, else `c08r4d0r`), writes both back to the store,
and prints `TG_*` markers that `parseDeviceIdentity` reads. `brandingCommands`
then only *writes* the three names — it contains no mint, which
`TestBrandingNeverMintsACode` pins.

A redeploy of an already-deployed router therefore keeps the name it already
answers to. `TestDeviceIdentityScriptNeverReMints` pins exactly that: a second
writer moves the hostname and the SSID, and the store still wins.

## What branding writes, and what it deliberately does not

* **Hostname**: `tollgate-<code>` (unchanged behaviour in shape — the installer
  has always written this spelling).
* **Captive SSID**: `TollGate-<code>`, on the guest APs under **both** spellings
  (`tollgate_2g_open` / `tollgate_5g_open` from the module, `default_radio0/1`
  from a stock OpenWrt). The old writer matched only `default_radio*`, so on a
  module-configured router it silently matched nothing.
* **Private SSID**: `<nym>-<code>` on `private_radio0` / `private_radio1`, and
  only when those sections exist. The old writer skipped `private_radio*` on
  purpose, which is why the private SSID kept a code no other name used.
* **Never**: the uplink STA's SSID, and the private **PSK**. Re-keying the
  private network would drop every paired admin device; the module owns that
  key. `TestBrandingWritesAllThreeIdentifiers` asserts both.
* The private SSID is *not* re-derived when the operator renamed it
  (`tollgate network private rename <name>`): only a machine-shaped suffix
  (four characters, or digits) is treated as machine-managed.

The prefix case is deliberate on both sides: the captive SSID keeps `TollGate-`
(reseller-mode upstream discovery in the module matches `TollGate-*`
case-sensitively), the hostname is lowercase (RFC-1123), and the unified thing
is the **code** — which is what a human reads and compares.

## Where the code lives, and when it changes

`/etc/config/tollgate`, which survives an in-place reinstall, an apk/opkg
upgrade, and a sysupgrade that keeps settings (OpenWrt keeps `/etc/config/*`).
A sysupgrade **with a wipe**, or a factory reset, has no store by definition and
mints a new code — the only case in which the code changes.

## Tests

* `branding_test.go` — `TestDeviceIdentityScriptAdoptionOrder` (the adoption
  order and the derived names, per case), `TestDeviceIdentityScriptNeverReMints`,
  `TestBrandingWritesAllThreeIdentifiers`,
  `TestPrivateSSIDCommandIsANoOpWithoutTheSection`,
  `TestBrandingNeverMintsACode`, plus the existing pre-auth allow-list pins.
  All run the **shipped** router-side shell against a stub `uci`.
* The same case table is pinned on the module side
  (`tests/uci-defaults-device-code_test.sh`), so a change to the adoption order
  or the mint alphabet fails one of the two suites.

## What is not verified

No physical router was flashed or deployed by this change: every assertion here
is offline (stub `uci`, `/bin/sh`). The router-visible behaviour — that a real
`uci` accepts the store write, that `wifi reload` picks the new SSIDs up, and
that a redeploy of an existing router keeps its name — still needs one deploy
on the bench against a build carrying both halves (the module half ships via
the feed).
