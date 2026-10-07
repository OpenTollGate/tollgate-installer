# REPORT — captive SSID leading `!` (`!TollGate-<code>`)

- Branch: `pr/installer-ssid-leading-bang`, base `origin/main` = 89ad029.
- Push target: remote `fork` (felixfelix-bot/tollgate-installer). NO PR opened.
- No tag / release (operator will merge this after the current release and does
  not want a separate release for it).

## What changed (contract, pinned exactly)

Captive/guest SSID is now `!TollGate-<code>`; the leading `!` (0x21) makes the
guest SSID sort first in an alphabetically-sorted WiFi list.

1. **Writer — embedded shell fragment** (`deploy.go`, `deviceIdentityScript`):
   `SSID="TollGate-$CODE"` → `SSID="!TollGate-$CODE"`. This value is echoed as
   `TG_SSID=` and parsed by `parseDeviceIdentity` into `deviceIdentity.SSID`,
   which is what the guest-AP writer (`guestSSIDCmd`) and the NoDogSplash
   `gatewayname` line use.
2. **Writer — Go string** (`deploy.go`, `fallbackDeviceIdentity`):
   `SSID: "TollGate-" + code` → `SSID: "!TollGate-" + code`.
3. **Reader** (`deploy.go`, `deviceIdentityScript` → `code_from_name`): strips a
   leading `!` BEFORE the brand-prefix compare (`p=${p#\!}`), suffix logic
   (everything after the first `-`) unchanged. `!TollGate-OQ3Q`,
   `TollGate-OQ3Q` and `tollgate-0GLK` all resolve to the same code, so a router
   already in the field under the bare spelling still adopts its own code.
4. **Escaping guard** (`ssidSafeForShell`, `deploy.go` ~601): verified `!` is
   ACCEPTED (it only refuses `'`, `\n`, `\r`, empty) — no change needed; the
   guard was NOT weakened. A test case for `!TollGate-7F3A` was added next to the
   existing refusal cases, which still assert a single quote and the empty string
   are refused.
5. **Docs**: `README.md` and `docs/device-identity.md` updated to the new
   spelling with the sort-first rationale and the escaping proof. No changelog
   fragment convention exists in this repo (no `.changes/`, `changelog.d/`, or
   towncrier config), so none was invented.

The PRIVATE SSID is untouched.

## Escaping (proved, not assumed)

`SSID="!TollGate-$CODE"` is a double-quoted shell assignment shipped to the
router over SSH. History expansion — which would eat a bare `!` in an
INTERACTIVE shell — does not apply to the non-interactive shell the router runs.
`TestBangPrefixedSSIDSurvivesNonInteractiveShell` runs the SAME shipped resolver
under `bash -c` (non-interactive) and asserts the `!` survives into the emitted
SSID and that the fragment still contains `SSID="!TollGate-$CODE"`. Confirmed by
hand too: `${p#\!}` strips correctly under both `/bin/sh` (dash) and `bash`.

## RED-first

Extended the tests first, against unchanged `deploy.go`. Decisive failure lines:

```
branding_test.go:449: captive SSID = "TollGate-OQ3Q", want "!TollGate-OQ3Q"
--- FAIL: TestDeviceIdentityScriptAdoptionOrder/a_bang-prefixed_captive_SSID_is_still_a_code_source
    branding_test.go:433: code = "4442", want "7F3A" ... TG_CODE_SOURCE=minted
--- FAIL: TestBangPrefixedSSIDSurvivesNonInteractiveShell
    branding_test.go:671: captive SSID = "TollGate-7F3A", want "!TollGate-7F3A"
FAIL	github.com/OpenTollGate/tollgate-installer	4.457s
```

The reader failure (minted `4442` instead of adopting `7F3A`) is the second half
of the contract: without the strip, a field router under the new spelling would
be re-named by the next deploy.

## GREEN (real tails)

Named tests:

```
--- PASS: TestDeviceIdentityScriptAdoptionOrder (1.24s)
    --- PASS: TestDeviceIdentityScriptAdoptionOrder/a_bang-prefixed_captive_SSID_is_still_a_code_source (1.24s)
--- PASS: TestPrivateSSIDCommandRefusesAValueItCannotQuote (0.23s)
--- PASS: TestBangPrefixedSSIDSurvivesNonInteractiveShell (0.88s)
ok  	github.com/OpenTollGate/tollgate-installer	2.392s
```

Whole suite from the repo root:

```
ok  	github.com/OpenTollGate/tollgate-installer	153.190s
?   	github.com/OpenTollGate/tollgate-installer/e2e-probe	[no test files]
?   	github.com/OpenTollGate/tollgate-installer/internal/fakerouter	[no test files]
?   	github.com/OpenTollGate/tollgate-installer/scripts/fixture-router	[no test files]
```

## Layout note (PR #81)

PR #81 (branch `pr/installer-layout`) is still open and MOVES the wizard's
sources from the repo root into `internal/app/` (and the shell tests to
`tests/shell/`). It is NOT merged, so in this base the Go sources are at the ROOT
and that is where this work was done: `deploy.go`, `branding_test.go`,
`root_credential_test.go`. If #81 merges first, the same files will be at
`internal/app/deploy.go` etc. — the edit is mechanical (same three strings, the
same `code_from_name` fragment, the same test file). This PR does NOT try to
handle both layouts.

## Commands run

- `git worktree add ~/worktrees/installer-ssid-bang -b pr/installer-ssid-leading-bang origin/main`
- `go test . -run '…' -count=1` (RED, failures pasted above)
- `go test ./...` (green)
- `git push fork HEAD:refs/heads/pr/installer-ssid-leading-bang`
