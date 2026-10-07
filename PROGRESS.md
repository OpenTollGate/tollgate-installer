# PROGRESS — installer captive SSID leading `!`

- Branch: `pr/installer-ssid-leading-bang` (base `origin/main` = 89ad029)
- Push target: remote `fork` (felixfelix-bot/tollgate-installer). No PR.

## Clusters (one line per verified step)

- [RED] `branding_test.go`: extended `TestDeviceIdentityScriptAdoptionOrder` with the bang-prefix case, changed the SSID assertion to `"!TollGate-"+code`, added the `ssidSafeForShell("!TollGate-7F3A")` case; added `TestBangPrefixedSSIDSurvivesNonInteractiveShell` (+ `shRunBashEnv` helper in `root_credential_test.go`). Failure captured: `branding_test.go:449: captive SSID = "TollGate-OQ3Q", want "!TollGate-OQ3Q"` and the bang case `code = "4442", want "7F3A"` (reader minted instead of adopting). Committed + pushed: da4f5c6.
- [GREEN] `deploy.go`: `SSID="!TollGate-$CODE"` in the embedded shell fragment; `SSID: "!TollGate-" + code` in `fallbackDeviceIdentity`; `code_from_name` now strips a leading `!` before the prefix compare (`p=${p#\!}`, suffix logic untouched) so `!TollGate-<code>` and the bare `TollGate-<code>` both resolve to the same code; comments updated. Targeted tests PASS; `go test ./...` green.
- [DOCS] `README.md` + `docs/device-identity.md` updated to `!TollGate-<code>` with the sort-first rationale and the escaping proof. No changelog fragment convention exists in this repo (no `.changes`/`changelog.d`/towncrier) — none added.
