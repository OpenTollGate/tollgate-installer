# Clean-slate flash report

## What changed
- `internal/app/clean_slate.go:14-250`: added version-selectable clean-slate image resolution for OpenWrt 25.12.5 and 24.10.8, mapped-board refusal, server-side typed confirmation, published `sha256sums` lookup, hash verification before upload, `sshUploadPipe` + `sysupgrade -n`, reboot/reconnect job flow, and no package installation.
- `internal/app/clean_slate_test.go:1-61`: added TDD coverage for release URL derivation, unmapped-board refusal, confirmation refusal, checksum mismatch before push, and package-install absence.
- `internal/app/main.go:1804-1805`: registered `/api/clean-slate-info` and `/api/clean-slate-flash`.
- `internal/app/index.html:318-331,1518-1561`: added opt-in advanced UI with both release lanes, exact URL/hash display, typed board confirmation, and status polling.
- `PROGRESS.md:1-6`: step log.

## RED evidence
`go test ./internal/app -run TestCleanSlate -count=1` before implementation failed with:
- `undefined: cleanSlateImage`
- `undefined: validateCleanSlateConfirmation`
- `undefined: verifyCleanSlateImage`
- `undefined: cleanSlateFlashCommand`

## GREEN evidence
- `go test ./...`: passed (`internal/app` passed; all packages passed).
- Shell pins: every `tests/shell/test-*.sh /home/c03rad0r/worktrees/installer-clean-slate` passed.
- `gofmt` completed.

## Git and PR
- Commit SHA: `c52914886eaa08403d319a9c04e480a14568476c`
- Pushed branch SHA observed with `git ls-remote fork refs/heads/pr/clean-slate-flash`: `c52914886eaa08403d319a9c04e480a14568476c`
- PR: https://github.com/OpenTollGate/tollgate-installer/pull/90

## Remaining steps
1. None observed for the requested implementation and verification. Real hardware flashing was intentionally not performed; validation used unit tests and repository fixtures/conventions only.
