# TollGate install run book

There is **one canonical run book**, and it always targets the **latest
release** of `FreedomTechFeed/packages`:

- **[Install Run Book (canonical, portable)](../install-runbook.md)** — the
  complete host-independent OpenWrt procedure (wizard only for v0.6.0; the
  wizard installs `.apk` and `.ipk`). It resolves the release tag at the top of
  the session and never hardcodes a `preNN`.

## Host mechanics

The canonical run book is host-independent; the only host-specific part is how
you download and launch the installer binary. Use the wizard's own launcher,
which already handles the host OS/arch:

```sh
bash <(curl -fsSL https://raw.githubusercontent.com/OpenTollGate/tollgate-installer/main/install-and-test.sh)
```

> The former per-host editions (`pre26-install-runbook-<os>.md`) were removed:
> they pinned a specific stale pre-release and the felixfelix-bot installer
> build, which is exactly the "installed the wrong version" failure the canonical
> run book now prevents. See git history if you need them.

## Recordings

Short narrated walkthroughs sit next to the instructions.
The wizard has two entry points.

- **Installer wizard (interactive):** [video (55 s, real UI + visible cursor)](https://blossom2.orangesync.tech/f202527b2ab8909b89c20ce0014059d0d0b7d994661d4c3a1617bb1b1ca3ae27.mp4) · [captions](https://blossom2.orangesync.tech/133d3cf07954935e893197e4446784e96004d787ae69fe39d4edf4371b0f3f52.vtt)
- **Installer wizard (headless):** [video (20 s)](https://blossom2.orangesync.tech/b7f9c8a4cb9aeb4f477d4924c39ee250a82e918f7aba43ead389414ce3ff2105.mp4)

The former Path B manual-install and Appendix A recordings are archived with the
last maintained manual procedure (`v0.6.0-alpha2-rc33`). Manual install is
temporarily unsupported; see the canonical run book §2.

