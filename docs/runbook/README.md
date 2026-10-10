# TollGate install run book

There is **one canonical run book**, and it always targets the **latest
pre-release** of `FreedomTechFeed/packages`:

- **[Install Run Book (canonical, portable)](../install-runbook.md)** — the
  complete host-independent OpenWrt procedure (wizard + manual, `.apk`/`.ipk`/
  offline bundle). It resolves the release tag at the top of the session and
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
