# PROGRESS — installer captive SSID leading `!`

- Branch: `pr/installer-ssid-leading-bang` (base `origin/main` = 89ad029)
- Push target: remote `fork` (felixfelix-bot/tollgate-installer). No PR.

## Clusters (one line per verified step)

- [RED] `branding_test.go`: extended `TestDeviceIdentityScriptAdoptionOrder` with the bang-prefix case, changed the SSID assertion to `"!TollGate-"+code`, added the `ssidSafeForShell("!TollGate-7F3A")` case; added `TestBangPrefixedSSIDSurvivesNonInteractiveShell` (+ `shRunBashEnv` helper). Failure captured: `branding_test.go:449: captive SSID = "TollGate-OQ3Q", want "!TollGate-OQ3Q"` and the bang case `code = "4442", want "7F3A"` (reader minted instead of adopting).
