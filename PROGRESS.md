# Progress

- 2026-10-09: created installer worktree `pr/pre26-install-runbook` from `origin/main`.
- 2026-10-09: copied review-club pre26 run book into `docs/pre26-install-runbook.md`.
- 2026-10-09: corrected README's stale claim that wizard never flashes firmware; added run-book links in Quick start and Prerequisites.
- 2026-10-13: C2-I-02 test lane (t_ad81c3fa): baseline re-measured at main 2703568 — 8/8 PASS (04aedb5 already added the fingerprint-trust step). Added the fixture-log step-4 gate (>=2 /etc/shadow probes, >=1 passwd exec counted from the fixture's own log) + TOLLGATE_TEST_FORCE_FINGERPRINT negative-control hook + fixture-log dump in evidence; documented the fixture-path trust-flag requirement in install-and-test.sh usage. RED control: wrong-fp run FAILs at the step-4 gate (exit 2). GREEN: 10/10.
