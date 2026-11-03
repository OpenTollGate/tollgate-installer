#!/usr/bin/env bash
# Gutter test: the whitelabel re-brand's name must never appear on a shipped
# surface in this repository's tree.
#
# WHY THIS EXISTS (port of the module's gutter)
#
#   OpenTollGate/tollgate-module-basic-go carries the same gutter at
#   tests/packaging/rebrand-literal-gutter_test.sh. TollGate is the non-profit
#   reference implementation; the commercial re-brand is a SEPARATE build (its
#   own org, its own repo, its own brand token passed in as a build INPUT).
#   Upstream must not carry that brand as a literal anywhere, because a bare
#   literal is how the module once grew a SECOND :8090 admin writer — a
#   brand-gated legacy `configUI` section that shipped to every consumer and
#   fought the portal-staged board for the port.
#
#   The invariant is a two-repo contract: the ROUTER-SIDE writer
#   (tollgate-module-basic-go) and the WIZARD (this repo) both name a router,
#   and its adoption path is pinned on both sides (docs/device-identity.md).
#   With the gutter on only one side, a brand literal can drift back into this
#   repo while CI stays green — nobody finds out until a customer-facing bug
#   appears. This port closes that asymmetry.
#
# This is a working-tree gutter, not a history rewrite: the name legitimately
# appears in old commits, and that is accepted (see `git log -S`). What it must
# not do is come back into the tree, so this test fails the moment a literal is
# reintroduced on a code, build, packaging, test or CI surface.
#
# THE BANNED LITERAL IS NEVER SPELLED AS BYTES IN THIS FILE. The scan pattern is
# a bracket expression (`net4[s]ats`) that matches the nine-character name while
# the bytes of this file do not match their own scan — so the gutter can scan
# the whole tree, itself included, without tripping over its own pattern. The
# negative controls below prove the pattern still matches a real occurrence and
# still refuses a near-miss.
#
# KNOWN LIMITS (stated, not fixed — correctness of the port does not depend on
# them, and tightening them is a separate change):
#   1. ONE banned name is hardcoded. This enforces "THIS brand is absent", not
#      the general invariant "ANY brand must be an input". A second whitelabel
#      brand's literal would sail through. The generic form would require a
#      source of truth for "which tokens are brands", which does not exist yet.
#   2. The scan covers TRACKED files (git grep reads the index). An uncommitted
#      or gitignored file in the tree is not scanned. Irrelevant in CI, where
#      the checkout IS the commit; it matters only for a local pre-commit run.
#
# NOT PORTED on purpose: the module's gutter carries a third check — that every
# `uhttpd.<name>` its shipped uci-defaults reference is a KNOWN section, because
# a new section name is how a second admin writer gets introduced. This repo
# ships no uci-defaults at all (AGENTS.md rule 2: those belong to
# tollgate-module-basic-go's packaging), so there is no vocabulary here to
# police. The brand-specific half of that intent — the section NAMED after the
# brand — is scanned below.
#
# usage: tests/shell/test-rebrand-literal-gutter.sh [tree]
# exit 0 = PASS, 2 = FAIL, 3 = environment
set -u

TREE="${1:-$(cd "$(dirname "$0")/../.." && pwd)}"

ok()  { printf 'ok   - %s\n' "$1"; }
bad() { FAIL=$((FAIL + 1)); printf 'FAIL - %s\n' "$1" >&2; }
env_fail() { printf 'environment: %s\n' "$1" >&2; exit 3; }

[ -d "$TREE" ] || env_fail "no such tree: $TREE"
cd "$TREE" || env_fail "cannot enter tree: $TREE"
[ -f internal/app/deploy.go ] || env_fail "$TREE does not look like tollgate-installer (internal/app/deploy.go missing)"

FAIL=0

# ─── the literal under ban ───────────────────────────────────────────────
# `net4[s]ats` matches the nine-character name; the file bytes "net4[s]ats" do
# NOT match a scan for it. Defined ONCE here; every other mention below is
# derived from it, so the name is spelled nowhere as raw bytes.
BANNED='net4[s]ats'

# The banned SECTION name, same trick: `uhttpd.<name>` is the shape the purged
# legacy second admin writer created in the module (a brand-gated configUI
# section that fought the portal-staged board for :8090). This repo ships no
# such section, but the shape is brand-specific, so it is scanned here too.
BANNED_SECTION="uhttpd\\.${BANNED}"

# ─── discussion allowlist: where the name is DISCUSSED, not shipped ───────
# The ban's object is the shipped/config surface. Prose that documents the
# whitelabel MECHANISM — how a build input selects a brand — is not a shipped
# surface, and the module's merged decision records name the re-brand for the
# same reason. Each entry is justified; this is not a blanket prose exemption.
#
# These paths carry ZERO occurrences of the name today (verified by
# `git grep -i -l`). They are listed because they are this repo's documented
# homes for prose about branding and the wizard contract, and because the
# module's gutter exempts the equivalent files. The exemption is a documented
# allowance for prose, not a live blindfold over a code path.
#
# NOT exempt: internal/app/*.go. Source is the shipped surface this gutter
# exists to protect, and a single exempted line there is handled explicitly
# (see EXEMPT_* below) rather than by excluding the file.
DISCUSSION_ALLOWLIST=(
    # Operator-facing and contributor docs. README.md documents the wizard's
    # step table and the device-identity contract; the module's README carries
    # a branding paragraph for the same reason.
    'README.md'
    'docs'
    # Contributor/agent instructions and the release log: the same class of
    # prose record the module exempts (its CONTRIBUTING.md and CHANGELOG.md).
    'AGENTS.md'
    'CHANGELOG.md'
)

# ─── the one documented, path-scoped, line-scoped exemption ───────────────
# internal/app/deploy.go carries ONE occurrence, at the `code_from_name` case
# rule of the router-side device-code resolver (deviceIdentityScript):
#
#     tollgate|<the re-brand>) code_norm "$s" ;;
#
# This is an EXPLICIT EXEMPTION WITH A RATIONALE, not a silent one. Why it
# stays a literal, and why the alternative was rejected:
#
#   * `code_from_name` recovers a device code from a name a router ALREADY
#     carries (`<brand>-<code>`). Its whole purpose is ADOPTION: a router
#     already named by a brand must resolve to the SAME code rather than
#     collect a new one. Recognising only the default brand would make this
#     repo blind to a name the re-brand's own writer ships.
#
#   * The module's equivalent resolves the brand GENERICALLY
#     (`packaging/files/etc/uci-defaults/99-tollgate-setup` → `brand_token()`,
#     which reads the router's brand file). This repo has NO brand input at all
#     — it hardcodes `tollgate-<code>` / `!TollGate-<code>`
#     (deviceIdentityScript, fallbackDeviceIdentity) and never reads a brand
#     file. Resolving generically here is therefore not a deletion but a NEW
#     BEHAVIOUR on the device-code adoption path: it would add a router-side
#     read of an environment/global path, a decision about the absent-file
#     case, and a back-compat question for older module versions (AGENTS.md
#     rule 3: the wizard must work with v0.5.0). That is a change to the
#     adoption path, not a port, and it must not be guessed at inside a
#     lint-gutter PR.
#
#   * The re-brand's OWN wizard does not use this code path: it is a separate
#     org and repo (that org's own Go wizard, named after the brand above) with
#     its own vendored deploy.go, which brands from its own token and contains
#     no `code_from_name`. So removing the rule cannot fix a call site there.
#
#   * The rule is narrowly reachable today: the resolver is STORE-FIRST, so
#     this case runs only for a router named by a brand with no store yet.
#
# To retire the exemption: give this repo a brand input it reads generically
# (the module's `brand_token()` shape), then delete the rule AND this
# exemption together. The scan below FAILS if the exemption goes stale (zero
# matches) or grows (more than one), so it cannot rot unnoticed.
EXEMPT_PATH='internal/app/deploy.go'
EXEMPT_LINE_ERE="tollgate|${BANNED}"
EXEMPT_COUNT_EXPECTED=1

# ─── scanning ────────────────────────────────────────────────────────────
in_work_tree() { git rev-parse --is-inside-work-tree >/dev/null 2>&1; }

# scan_tree <extended-regex>: every hit in the TRACKED tree, minus the
# DISCUSSION_ALLOWLIST paths. git grep when this is a git work tree (the
# tracked tree, which is what CI checks out); a recursive grep otherwise, so
# the gutter is not silently inert outside a repo.
scan_tree() {
    if in_work_tree; then
        local excludes=() path
        for path in "${DISCUSSION_ALLOWLIST[@]}"; do
            excludes+=(":(exclude)$path")
        done
        git grep -I -i -n -E -- "$1" -- . "${excludes[@]}" 2>/dev/null || true
    else
        while IFS= read -r hit; do
            local rel="${hit#./}"
            local skip=0 allow
            for allow in "${DISCUSSION_ALLOWLIST[@]}"; do
                case "$rel" in "$allow"|"$allow"/*) skip=1 ;; esac
            done
            [ "$skip" = 1 ] || printf '%s\n' "$hit"
        done < <(grep -rInI -i -E -- "$1" . 2>/dev/null | grep -v '^\./\.git/' || true)
    fi
}

# is_exempt <hit-line>: a hit is exempt only when it is the ONE documented
# source line, in the ONE documented path. Exemption is path-scoped, so the
# same text in any other file is still a violation.
is_exempt() {
    local hit="$1" path="${1%%:*}" rest="${1#*:}" text
    [ "$path" = "$EXEMPT_PATH" ] || return 1
    text="${rest#*:}"                       # drop the line number
    printf '%s' "$text" | grep -q -E -- "$EXEMPT_LINE_ERE"
}

# split_hits <hits>: prints "<exempt-count> <violations>" — violations on
# stdout one per line, the exempt count first. Kept in one place so the main
# scan and the path-scoping control use exactly the same classification.
split_hits() {
    local hits="$1" exempt=0 hit out='' have_any=0
    while IFS= read -r hit; do
        [ -n "$hit" ] || continue
        have_any=1
        if is_exempt "$hit"; then
            exempt=$((exempt + 1))
        else
            out="${out}${hit}"$'\n'
        fi
    done <<EOF
$hits
EOF
    printf '%s\n' "$exempt"
    [ "$have_any" = 1 ] || return 0
    printf '%s' "$out"
}

echo "== the re-brand's name appears nowhere on a shipped surface in the tracked tree"
hits="$(scan_tree "$BANNED")"
split="$(split_hits "$hits")"
exempt_hits="$(printf '%s\n' "$split" | head -1)"
violations="$(printf '%s\n' "$split" | tail -n +2)"
violation_count="$(printf '%s\n' "$violations" | grep -c . || true)"

if [ "$violation_count" = 0 ]; then
    ok "0 unexempted hits for the banned literal in $(in_work_tree && printf 'the tracked tree' || printf 'the tree')"
else
    bad "the banned literal is back ($violation_count unexempted hit(s)):"
    printf '%s\n' "$violations" | sed 's/^/       /' >&2
fi

# The exemption must be exactly the documented one: not stale, not grown.
echo
echo "== the documented exemption in $EXEMPT_PATH is exactly one line"
if [ "$exempt_hits" = "$EXEMPT_COUNT_EXPECTED" ]; then
    ok "the exemption covers $exempt_hits line(s) — the one documented literal"
elif [ "$exempt_hits" -lt "$EXEMPT_COUNT_EXPECTED" ]; then
    bad "the exemption is STALE: 0 hits in $EXEMPT_PATH. The literal was removed, so delete EXEMPT_PATH/EXEMPT_LINE_ERE from this gutter rather than leaving a blindfold over a line that no longer exists."
else
    bad "the exemption has GROWN to $exempt_hits lines — a second literal is hiding behind a named exemption. Each occurrence needs its own documented decision."
fi

# The section shape the module's legacy second :8090 writer created must not
# exist here either. Tree-wide, prose included.
echo
echo "== no uhttpd section named after the re-brand exists in the tree"
sec_hits="$(scan_tree "$BANNED_SECTION")"
if [ -z "$sec_hits" ]; then
    ok "0 hits for a uhttpd section named after the banned literal"
else
    bad "a uhttpd section named after the banned literal is back ($(printf '%s\n' "$sec_hits" | grep -c . ) hit(s)):"
    printf '%s\n' "$sec_hits" | sed 's/^/       /' >&2
fi

# ─── negative controls, both directions ──────────────────────────────────
# A gutter that cannot see a planted occurrence is worthless; a gutter that
# matches a DIFFERENT brand is worse than worthless. Both are pinned.
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
name="$(printf '%s' "$BANNED" | tr -d '[]')"     # assembled, never spelled
near="${name%?}"                                  # the name minus its last char

echo
echo "== control: the scan pattern sees a planted occurrence"
planted="$TMP/planted.txt"
printf 'uhttpd.%s=uhttpd\n' "$name" > "$planted"
if grep -q -E -- "$BANNED" "$planted"; then
    ok "the pattern matches a planted occurrence (the gutter is not inert)"
else
    bad "the pattern does NOT match a planted occurrence — the gutter cannot fail, so it proves nothing"
fi

echo
echo "== control: the pattern does not match a near-miss"
near_miss="$TMP/near-miss.txt"
printf 'uhttpd.%s=uhttpd\n' "$near" > "$near_miss"
if grep -q -E -- "$BANNED" "$near_miss"; then
    bad "the pattern matches a near-miss (a different brand name) — it is too loose to be a gutter"
else
    ok "the pattern does not match a near-miss brand name (it is anchored to the banned literal)"
fi

echo
echo "== control: the documented exemption is path-scoped, not a blindfold"
# The SAME text as the exempted source line, in a file that is NOT the
# exempted path, must still classify as a violation.
lookalike="$TMP/lookalike.txt"
printf 'case "$p" in\n    %s) code_norm "$s" ;;\nesac\n' "$EXEMPT_LINE_ERE" > "$lookalike"
lookalike_hit="$lookalike:2:    $EXEMPT_LINE_ERE) code_norm \"\$s\" ;;"
if is_exempt "$lookalike_hit"; then
    bad "the exemption matched the same text in another path — it is a global blindfold, not a scoped exemption"
else
    ok "the exemption only matches $EXEMPT_PATH (the same line elsewhere is still a violation)"
fi

echo
echo "== control: the control files are outside the tracked tree"
if printf '%s\n' "$hits" | grep -q -F "$TMP/"; then
    bad "scan_tree reported a control file as a tracked hit — the tree scan is not scoped to the tree"
else
    ok "scan_tree ignores the control files (they are outside the tracked tree)"
fi

echo
if [ "$FAIL" != 0 ]; then
    printf 'FAIL: %d check(s) failed\n' "$FAIL" >&2
    exit 2
fi
echo "PASS: the re-brand's name is absent from this repo's shipped surfaces"
exit 0
