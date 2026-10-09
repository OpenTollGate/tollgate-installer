# TollGate pre26 install run book - host editions

Choose the edition matching the workstation that runs the installer. Router-side sections are shared; editions change only host mechanics.

| OS | File |
|---|---|
| macOS Intel | [pre26-install-runbook-darwin-amd64.md](pre26-install-runbook-darwin-amd64.md) |
| macOS Apple Silicon | [pre26-install-runbook-darwin-arm64.md](pre26-install-runbook-darwin-arm64.md) |
| Linux x86_64 | [pre26-install-runbook-linux-amd64.md](pre26-install-runbook-linux-amd64.md) |
| Linux arm64 | [pre26-install-runbook-linux-arm64.md](pre26-install-runbook-linux-arm64.md) |
| Windows x86_64 | [pre26-install-runbook-windows-amd64.md](pre26-install-runbook-windows-amd64.md) |

## How to choose

Choose the host OS, then CPU architecture: Intel/AMD 64-bit is `amd64`/`x86_64`; Apple Silicon and Linux ARM are `arm64`/`aarch64`. Do not run amd64 on arm64. Windows uses PowerShell; headless Bash use requires WSL or Git Bash.

These editions target existing release `v0.6.0-alpha2-rc17` in `felixfelix-bot/tollgate-installer`.

## Canonical portable runbook

For the complete host-independent OpenWrt procedure, see the [canonical portable runbook](../pre26-install-runbook.md).
