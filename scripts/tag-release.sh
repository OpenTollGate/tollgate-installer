#!/usr/bin/env bash
# Cut, publish, and verify a cross-platform tollgate-installer release.
# Safe to run as: bash <(curl -fsSL <raw-url>/scripts/tag-release.sh)
set -euo pipefail

REPO=""
SOURCE_REPO="${SOURCE_REPO:-}"
TAG=""
NOTES=""
CHECK_ONLY=0
PRERELEASE=0
REF=""
CACHE_ROOT="${HOME}/.cache/tollgate-release"
EXPECTED=(
  tollgate-installer-linux-amd64
  tollgate-installer-linux-arm64
  tollgate-installer-darwin-amd64
  tollgate-installer-darwin-arm64
  tollgate-installer-windows-amd64.exe
  SHA256SUMS
  REPRODUCE.txt
)

usage() {
  cat <<'USAGE'
Usage: tag-release.sh [--tag TAG] [--repo OWNER/NAME] [--ref REF]
                      --notes TEXT] [--check-only] [--prerelease]

Cuts a release from a clean cached clone, using release-binaries.sh for the
build and upload, then verifies the remote release and tag commit.
USAGE
}
die() { printf 'ERROR: %s\n' "$*" >&2; exit 1; }

while (($#)); do
  case "$1" in
    --tag) [[ $# -ge 2 ]] || die "--tag needs a value"; TAG=$2; shift 2 ;;
    --repo) [[ $# -ge 2 ]] || die "--repo needs a value"; REPO=$2; shift 2 ;;
    --ref) [[ $# -ge 2 ]] || die "--ref needs a value"; REF=$2; shift 2 ;;
    --notes) [[ $# -ge 2 ]] || die "--notes needs a value"; NOTES=$2; shift 2 ;;
    --check-only) CHECK_ONLY=1; shift ;;
    --prerelease) PRERELEASE=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) die "unknown option: $1" ;;
  esac
done

command -v gh >/dev/null 2>&1 || die "gh is required"
command -v go >/dev/null 2>&1 || die "go is required"
command -v git >/dev/null 2>&1 || die "git is required"
gh auth status >/dev/null 2>&1 || die "gh auth status is not valid"
ACCOUNT=$(gh api user --jq .login) || die "could not determine authenticated GitHub account"
[[ -n "$REPO" ]] || REPO=$(gh repo view --json nameWithOwner --jq .nameWithOwner)
[[ "$REPO" =~ ^[^/]+/[^/]+$ ]] || die "repo must be OWNER/NAME, got '$REPO'"
[[ -n "$SOURCE_REPO" ]] || SOURCE_REPO="$REPO"
PERM=$(gh api "repos/${REPO}" --jq '.permissions.push') || die "cannot inspect write permission for ${REPO} as ${ACCOUNT}"
if [[ "$PERM" != true ]]; then
  die "GitHub account '${ACCOUNT}' cannot push to '${REPO}' (permissions.push=${PERM})"
fi
printf 'Preflight: account=%s repo=%s push=%s\n' "$ACCOUNT" "$REPO" "$PERM"

DEFAULT_REF=$(gh repo view "$REPO" --json defaultBranchRef --jq .defaultBranchRef.name)
[[ -n "$REF" ]] || REF=$DEFAULT_REF

if [[ -z "$TAG" ]]; then
  highest=0
  while IFS= read -r old; do
    if [[ "$old" =~ ^(.+-rc)([0-9]+)$ ]] && ((10#${BASH_REMATCH[2]} > highest)); then
      highest=$((10#${BASH_REMATCH[2]}))
      prefix=${BASH_REMATCH[1]}
    fi
  done < <(gh release list --repo "$REPO" --limit 200 --json tagName,isDraft --jq '.[] | select(.isDraft == false) | .tagName')
  [[ -n "${prefix:-}" ]] || die "could not derive a released -rcN tag from ${REPO}; pass --tag"
  TAG="${prefix}$((highest + 1))"
  printf 'Resolved tag: %s (no --tag supplied; highest released matching rc tag was %s%d)\n' "$TAG" "$prefix" "$highest"
else
  printf 'Resolved tag: %s (--tag supplied explicitly)\n' "$TAG"
fi

FEED_PRENUMBER_RE='(^|[^A-Za-z0-9])pre[0-9]+([^0-9]|$)'
if [[ "$TAG" =~ $FEED_PRENUMBER_RE ]]; then
  if [[ "${ALLOW_FEED_PRENUMBER_TAG:-0}" == 1 ]]; then
    printf 'WARNING: tag %s embeds a feed pre-number; proceeding because ALLOW_FEED_PRENUMBER_TAG=1\n' "$TAG" >&2
  else
    die "refusing tag '${TAG}': it embeds a feed pre-number (pre followed by digits); use an installer rc tag or set ALLOW_FEED_PRENUMBER_TAG=1 deliberately"
  fi
fi

SRC="${CACHE_ROOT}/${SOURCE_REPO//\//_}/${REF//\//_}"
mkdir -p "$CACHE_ROOT"
rm -rf "$SRC"
git clone --quiet --depth 1 --branch "$REF" "https://github.com/${SOURCE_REPO}.git" "$SRC" || die "could not clone ${SOURCE_REPO} ref ${REF}"
[[ -z "$(git -C "$SRC" status --porcelain)" ]] || die "source tree is dirty: $SRC"
COMMIT=$(git -C "$SRC" rev-parse HEAD)
printf 'Source: ref=%s commit=%s tree=%s (clean)\n' "$REF" "$COMMIT" "$SRC"
[[ -f "$SRC/scripts/release-binaries.sh" ]] || die "scripts/release-binaries.sh is missing"

BUILD_CMD="scripts/release-binaries.sh --tag ${TAG} --publish --repo ${REPO} --repro-check"
printf 'Plan: tag=%s repo=%s commit=%s\n' "$TAG" "$REPO" "$COMMIT"
printf 'Plan: build command: (cd %q && %s)\n' "$SRC" "$BUILD_CMD"
printf 'Plan: expected assets: %s\n' "${EXPECTED[*]}"
if ((CHECK_ONLY)); then
  printf 'Check-only: no build, tag, or upload performed.\n'
  exit 0
fi

# release-binaries.sh owns the build, reproducibility gate, release creation,
# and --clobber upload behavior. Do not duplicate that logic here.
BUILD_ARGS=(--tag "$TAG" --publish --repo "$REPO")
if [[ -n "$NOTES" ]]; then BUILD_ARGS+=(--notes "$NOTES"); fi
BUILD_ARGS+=(--repro-check)
(cd "$SRC" && bash scripts/release-binaries.sh "${BUILD_ARGS[@]}")
if ((PRERELEASE)); then
  gh release edit "$TAG" --repo "$REPO" --prerelease --latest=false
fi

REMOTE_COMMIT=$(gh api "repos/${REPO}/git/ref/tags/${TAG}" --jq '.object.sha') || die "remote tag ${TAG} is missing"
REMOTE_TYPE=$(gh api "repos/${REPO}/git/ref/tags/${TAG}" --jq '.object.type')
if [[ "$REMOTE_TYPE" == tag ]]; then
  REMOTE_COMMIT=$(gh api "repos/${REPO}/git/tags/${REMOTE_COMMIT}" --jq '.object.sha')
fi
[[ "$REMOTE_COMMIT" == "$COMMIT" ]] || die "tag commit mismatch: built ${COMMIT}, remote tag ${REMOTE_COMMIT}"
printf 'Verified tag commit: %s\n' "$REMOTE_COMMIT"

printf 'OS | File | Download URL\n'
printf '%s\n' '---|---|---'
ASSET_LINES=$(gh release view "$TAG" --repo "$REPO" --json tagName,isDraft,isPrerelease,assets --jq '.assets[] | "\(.name)\t\(.size)\t\(.url)"') || die "cannot read remote release ${TAG}"
for expected in "${EXPECTED[@]}"; do
  line=$(printf '%s\n' "$ASSET_LINES" | awk -F '\t' -v n="$expected" '$1 == n {print; exit}')
  [[ -n "$line" ]] || die "remote release is missing asset ${expected}"
  size=$(printf '%s' "$line" | cut -f2)
  [[ "$size" =~ ^[1-9][0-9]*$ ]] || die "remote asset ${expected} has invalid size ${size}"
  printf 'asset: %s (%s bytes)\n' "$expected" "$size"
done
printf '%s\n' "$ASSET_LINES" | while IFS=$'\t' read -r name size url; do
  case "$name" in
    tollgate-installer-darwin-amd64) os='macOS (Intel)' ;;
    tollgate-installer-darwin-arm64) os='macOS (Apple Silicon)' ;;
    tollgate-installer-linux-amd64) os='Linux (x86_64)' ;;
    tollgate-installer-linux-arm64) os='Linux (arm64)' ;;
    tollgate-installer-windows-amd64.exe) os='Windows' ;;
    *) continue ;;
  esac
  printf "| %s | \`%s\` | %s |\n" "$os" "$name" "$url"
done
RELEASE_URL="https://github.com/${REPO}/releases/tag/${TAG}"
printf 'Summary: tag=%s commit=%s repo=%s\n' "$TAG" "$COMMIT" "$REPO"
printf 'Release: %s\n' "$RELEASE_URL"
printf 'Launcher: bash <(curl -fsSL https://raw.githubusercontent.com/%s/%s/scripts/tag-release.sh) --tag %s --repo %s\n' "${REPO%/*}" "${REPO#*/}" "$TAG" "$REPO"
