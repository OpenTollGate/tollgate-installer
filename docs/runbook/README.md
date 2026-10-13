# TollGate install run book

There is **one canonical run book**, and it always targets the **latest
release** of `FreedomTechFeed/packages`:

- **[Install Run Book (canonical, portable)](../install-runbook.md)** — the
  complete host-independent OpenWrt procedure (wizard + manual, `.apk`/`.ipk`).
  It resolves the release tag at the top of the session and
  never hardcodes a `preNN`.

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

Short narrated walkthroughs sit next to the instructions. The wizard path has
one now; the manual and offline paths follow.

- **Path A — installer wizard:** [video (55 s, narrated, real UI with a visible cursor)](https://blossom2.orangesync.tech/f202527b2ab8909b89c20ce0014059d0d0b7d994661d4c3a1617bb1b1ca3ae27.mp4) · [captions](https://blossom2.orangesync.tech/133d3cf07954935e893197e4446784e96004d787ae69fe39d4edf4371b0f3f52.vtt)
