# TollGate installer v0.6.0

**Released**: 2026-10-21
**Installs**: `FreedomTechFeed/packages` `v0.6.0` (`tollgate-wrt` 0.6.0,
module `cf7cad3`; the module release commit `9364f2e` adds version metadata
only)
**Platforms**: Linux amd64/arm64, macOS amd64/arm64, Windows amd64

This is the installer cut for the **v0.6.0 stable** release. Its compiled fallback
pin is `v0.6.0`, and the launcher defaults to the **stable** channel, so a run
without `--tag` / `--installer-tag` resolves this pairing:

```
Feed release: v0.6.0 (channel stable)
Installer build: v0.6.0 (channel stable)
```

To follow the next release candidate, pass `--channel alpha` (or set
`TOLLGATE_FEED_CHANNEL=alpha`).

## Install run book

The v0.6.0 run book is **wizard-only**. The wizard installs `.apk` on OpenWrt
25.12+ and `.ipk` on 24.10; on a router already running OpenWrt it skips
flashing and selects the package by package manager.

**Manual install is temporarily unsupported.** The last maintained procedure is
in the `v0.6.0-alpha2-rc33` tag. Restoration is tracked at
[OpenTollGate/tollgate-installer#155](https://github.com/OpenTollGate/tollgate-installer/issues/155).

## Known issues

- Manual install deferred: the fresh-install default-credential write, the
  first-pass `entry_ui` fallback, the wizard-only `tollgate.lan` alias, and the
  wizard-only leading-`!` guest SSID.
- The module-side loopback rate-limit exemption is post-release (#999); the
  installer classifies the resulting `429`/starting state as `pending`.
