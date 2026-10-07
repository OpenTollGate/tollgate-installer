# AGENTS.md — TollGate Architecture for LLM Sessions


## Layout — where the wizard lives

The module root holds only the build surface: `go.mod`, `Makefile`, `install-and-test.sh`
(the operator's one-liner) and `main.go` — a 4-line shim so `go build -o tollgate-installer .`
still produces the binary at the root.

The wizard itself is `internal/app`:

- `internal/app/*.go` — servers, handlers, SSH/trust, deploy, feed resolution;
- `internal/app/*_test.go` — the Go tests, **beside the code they test** (Go requires this:
  a `_test.go` file cannot live in a different directory from its package, which is why the
  tests are not in a `tests/` directory);
- `internal/app/index.html` + `embed.go` — the embedded UI.

`tests/shell/` — shell pins run against a tree (`bash tests/shell/test-*.sh <tree>`);
`tests/ui/` — the UI harness; `scripts/` — operator tools, not tests; `e2e/` — Playwright.

Version stamping follows the package: `-X github.com/OpenTollGate/tollgate-installer/internal/app.version`
(see `scripts/release-binaries.sh`).

## Architectural Principle

**TollGate repos are thin UI wrappers.** All business logic resides in
`tollgate-module-basic-go` (the Go backend installed as a system package
on OpenWrt routers). The TollGate repos only call backend APIs and render
results — they do NOT implement or duplicate business logic.

## Repo Map

### tollgate-installer (this repo)
**Role:** Cross-platform onboarding wizard (Go binary, served locally).
- Discovers routers on LAN (ARP scan + TCP probe)
- Walks operator through router deployment
- Calls the Go backend's REST API on the router for identity, status, etc.
- Does NOT implement: DeriveIPv4, DeriveMAC, BIP39 mnemonic, kind:0 publishing
- Does NOT ship: uci-defaults scripts (those belong to tollgate-module-basic-go packaging)

### tollgate-module-basic-go (OpenTollGate/tollgate-module-basic-go)
**Role:** All business logic. The core backend.
- Identity: `src/identity/` — DeriveIPv4, DeriveMAC, PrivateKeyToMnemonic, MnemonicToPrivateKey
- API: GET /identity, POST /identity/reveal-seed
- uci-defaults: `packaging/files/etc/uci-defaults/95-router-identity`
- Pricing: NIP-61 kind:10021 advertisement events (with supports_ln tags per PR #181)
- Payments: Cashu ecash + Lightning invoices
- Kind:0 profile publishing (opt-in)
- Captive portal: ships `tollgate-captive-portal-site` via its uci-defaults

## Identity Derivation (belongs ONLY in tollgate-module-basic-go)

```
merchant private key (identities.json)
  │
  ├─ SHA256(npub + "tollgate-lan-ipv4") → 10.X.Y.1/24 (DeriveIPv4)
  ├─ SHA256(npub + "tollgate-lan-mac") → 02:xx:xx:xx:xx:xx (DeriveMAC)
  └─ BIP39 mnemonic (24 words) → seed phrase (PrivateKeyToMnemonic)
```

All three derivation functions live in `tollgate-module-basic-go/src/identity/`.
Wizard and admin UI call `GET /identity` and `POST /identity/reveal-seed` to
get the results. Zero computation in the UI layer.

## Key Rules for Agents

1. **Do NOT add derivation logic to this repo.** If you need DeriveIPv4 or
   DeriveMAC or BIP39 conversion, add it to tollgate-module-basic-go and call
   the API from here.

2. **Do NOT ship uci-defaults scripts from this repo.** Those belong in
   tollgate-module-basic-go's packaging directory.

3. **Backward compatibility is paramount.** The wizard must work with older
   versions of tollgate-module-basic-go (v0.5.0). New API calls should
   gracefully degrade if the endpoint doesn't exist yet.

4. **This repo is a thin wrapper.** If you find yourself writing more logic
   than API-call-and-render, stop and reconsider whether it belongs in the
   backend instead.

5. **This repo ships NO admin panel.** OpenTollGate has no standalone admin
   dashboard yet — the admin panel and its rpcd plugin are out of scope. The
   captive portal ships inside the tollgate-wrt package.
