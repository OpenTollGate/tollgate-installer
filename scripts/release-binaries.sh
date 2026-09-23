#!/usr/bin/env bash
# ============================================================================
#  tollgate-installer — cross-platform release binaries
#
#  Builds the curl|bash launcher's download assets for every supported host, so
#  a macOS user gets a darwin binary from the same release as a Linux user.
#  Runs anywhere with Go installed — no GitHub Actions, in particular no
#  org-level Actions, which are not available to this project.
#
#  USAGE
#  ---------------------------------------------------------------
#  Build only (assets land in dist/):
#      scripts/release-binaries.sh
#
#  Build and assert the build is bit-for-bit repeatable:
#      scripts/release-binaries.sh --repro-check
#
#  Build and create/update a GitHub release with the assets:
#      scripts/release-binaries.sh --tag v0.7.0 --publish
#      scripts/release-binaries.sh --tag v0.7.0 --publish --repo felixfelix-bot/tollgate-installer
#
#  The script:
#    1. builds every target with CGO_ENABLED=0 (static, no libc), -trimpath and
#       -buildvcs=false so the same source + Go toolchain gives the same bytes;
#    2. writes SHA256SUMS (verify with `shasum -a 256 -c SHA256SUMS`, macOS, or
#       `sha256sum -c SHA256SUMS`, Linux) and REPRODUCE.txt (the toolchain and
#       flags used);
#    3. with --publish, reuses the release if it exists and uploads with
#       --clobber, so re-running after a fix updates the assets in place.
#
#  Asset names match what install-and-test.sh requests:
#      tollgate-installer-<os>-<arch>[.exe]
#  ---------------------------------------------------------------
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
OUT="${OUT:-${ROOT}/dist}"
BIN_NAME="tollgate-installer"

# linux and darwin are the platforms install-and-test.sh can run on; windows is
# published for people who download the binary directly (the launcher is bash).
TARGETS="${TARGETS:-linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64}"

TAG=""
PUBLISH=0
REPRO_CHECK=0
REPO=""
RELEASE_NOTES=""

usage() {
    sed -n '2,33p' "$0" | sed 's/^# \{0,1\}//'
    cat <<'USAGE'

Options:
  --tag <tag>       Release tag / version stamped into the binaries
                    (-X main.version). Required with --publish.
  --publish         Create or update the GitHub release for --tag and upload
                    the assets (needs gh, authenticated, with write access).
  --repo <owner/name>
                    Release repo for --publish. Default: the origin remote.
  --out <dir>       Output directory (default: <repo>/dist).
  --notes <text>    Release notes to use when the release is created.
  --repro-check     Build every target twice and compare the SHA-256 sums.
  -h, --help        Show this help.
USAGE
}

while [ $# -gt 0 ]; do
    case "$1" in
        --tag)         TAG="${2:-}"; shift 2 ;;
        --publish)     PUBLISH=1; shift ;;
        --repo)        REPO="${2:-}"; shift 2 ;;
        --out)         OUT="${2:-}"; shift 2 ;;
        --notes)       RELEASE_NOTES="${2:-}"; shift 2 ;;
        --repro-check) REPRO_CHECK=1; shift ;;
        -h|--help)     usage; exit 0 ;;
        *) echo "ERROR: unknown option: $1" >&2; usage >&2; exit 2 ;;
    esac
done

die() { printf 'ERROR: %s\n' "$1" >&2; exit 1; }
note() { printf '  ! %s\n' "$1" >&2; }

# --- preflight --------------------------------------------------------------
command -v go >/dev/null 2>&1 || die "go not found: install Go (https://go.dev/dl/) and re-run"
if command -v shasum >/dev/null 2>&1; then
    SHA_CMD="shasum -a 256"
elif command -v sha256sum >/dev/null 2>&1; then
    SHA_CMD="sha256sum"
else
    die "need 'shasum -a 256' (macOS/perl) or 'sha256sum' (Linux/coreutils) to write SHA256SUMS"
fi
if [ "${PUBLISH}" = 1 ]; then
    [ -n "${TAG}" ] || die "--publish requires --tag <tag>"
    command -v gh >/dev/null 2>&1 || die "--publish needs the gh CLI (https://cli.github.com); or build only and upload by hand"
    if [ -z "${REPO}" ]; then
        REPO="$(git -C "${ROOT}" remote get-url origin 2>/dev/null \
            | sed -e 's#^git@github.com:##' -e 's#^https://github.com/##' -e 's#\.git$##')"
        [ -n "${REPO}" ] || die "could not derive the release repo from the origin remote; pass --repo <owner/name>"
    fi
fi

# Version stamped into the binary: --tag when given, else the newest reachable
# tag plus the commit, so a build from a branch still says what it is.
if [ -n "${TAG}" ]; then
    VERSION="${TAG}"
else
    VERSION="$(git -C "${ROOT}" describe --tags --always 2>/dev/null || echo dev)"
fi
COMMIT="$(git -C "${ROOT}" rev-parse --short=7 HEAD 2>/dev/null || echo unknown)"

GO_VERSION="$(go version)"
LDFLAGS="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}"
GOFLAGS_BUILD="-trimpath -buildvcs=false"

echo "Repo:      ${ROOT}"
echo "Version:   ${VERSION} (commit ${COMMIT})"
echo "Toolchain: ${GO_VERSION}"
echo "Targets:   ${TARGETS}"
echo "Output:    ${OUT}"
echo

build_into() {
    # build_into <dir> <goos> <goarch> <asset-name>
    local dir="$1" goos="$2" goarch="$3" name="$4"
    mkdir -p "${dir}"
    # shellcheck disable=SC2086  # word splitting of GOFLAGS_BUILD is intended
    if ! GOOS="${goos}" GOARCH="${goarch}" CGO_ENABLED=0 \
        go build ${GOFLAGS_BUILD} -ldflags "${LDFLAGS}" \
        -o "${dir}/${name}" "${ROOT}"; then
        return 1
    fi
    return 0
}

asset_name() {
    # asset_name <goos> <goarch>
    local ext=""
    [ "$1" = "windows" ] && ext=".exe"
    printf '%s-%s-%s%s' "${BIN_NAME}" "$1" "$2" "${ext}"
}

rm -rf "${OUT}"
mkdir -p "${OUT}"

ASSETS=""
BUILT=0
for target in ${TARGETS}; do
    goos="${target%/*}"
    goarch="${target#*/}"
    name="$(asset_name "${goos}" "${goarch}")"
    if build_into "${OUT}" "${goos}" "${goarch}" "${name}"; then
        printf 'built  %-14s %10s bytes\n' "${target}" "$(wc -c < "${OUT}/${name}" | tr -d ' ')"
        ASSETS="${ASSETS} ${name}"
        BUILT=$((BUILT + 1))
    else
        note "FAILED to build ${target} — dropping it from the release"
    fi
done

[ "${BUILT}" -gt 0 ] || die "no target built"

# --- reproducibility self-check ---------------------------------------------
if [ "${REPRO_CHECK}" = 1 ]; then
    echo
    echo "=== reproducibility check (second build of every target) ==="
    SECOND="${OUT}/.repro"
    fail=0
    for target in ${TARGETS}; do
        goos="${target%/*}"
        goarch="${target#*/}"
        name="$(asset_name "${goos}" "${goarch}")"
        [ -f "${OUT}/${name}" ] || continue
        build_into "${SECOND}" "${goos}" "${goarch}" "${name}" || { note "second build of ${target} failed"; fail=1; continue; }
        a="$(cd "${OUT}" && ${SHA_CMD} "${name}" | cut -d' ' -f1)"
        b="$(cd "${SECOND}" && ${SHA_CMD} "${name}" | cut -d' ' -f1)"
        if [ "${a}" = "${b}" ]; then
            printf 'repeatable  %-14s %s\n' "${target}" "${a}"
        else
            printf 'DIFFERS     %-14s %s != %s\n' "${target}" "${a}" "${b}"
            fail=1
        fi
    done
    rm -rf "${SECOND}"
    if [ "${fail}" -ne 0 ]; then
        die "builds are not reproducible on this toolchain — do not publish until you know why"
    fi
fi

# --- checksums + provenance -------------------------------------------------
# sha256sum's own format ("<hash>  <name>"), so `sha256sum -c` / `shasum -a 256 -c`
# verify the file as-is on Linux and macOS.
(
    cd "${OUT}"
    for name in ${ASSETS}; do
        ${SHA_CMD} "${name}"
    done
) > "${OUT}/SHA256SUMS"

{
    echo "tollgate-installer release build"
    echo "version:   ${VERSION}"
    echo "commit:    ${COMMIT}"
    echo "toolchain: ${GO_VERSION}"
    echo "env:       CGO_ENABLED=0"
    echo "go build:  ${GOFLAGS_BUILD} -ldflags \"${LDFLAGS}\""
    echo "targets:   ${TARGETS}"
    echo
    echo "Rebuild with the same Go toolchain and these flags to get the same"
    echo "bytes; scripts/release-binaries.sh --repro-check asserts it locally."
} > "${OUT}/REPRODUCE.txt"

echo
echo "=== assets in ${OUT} ==="
cat "${OUT}/SHA256SUMS"

if [ "${REPRO_CHECK}" = 1 ]; then
    echo
    echo "Reproducibility check passed for every target."
fi

# --- publish -----------------------------------------------------------------
if [ "${PUBLISH}" = 0 ]; then
    echo
    echo "Not publishing (pass --publish --tag <tag> to create/update a release)."
    exit 0
fi

echo
echo "=== publishing ${TAG} to ${REPO} ==="
if gh release view "${TAG}" --repo "${REPO}" >/dev/null 2>&1; then
    echo "Release ${TAG} already exists — uploading with --clobber (re-runnable)."
else
    if [ -n "${RELEASE_NOTES}" ]; then
        gh release create "${TAG}" --repo "${REPO}" --title "${TAG}" --notes "${RELEASE_NOTES}"
    else
        gh release create "${TAG}" --repo "${REPO}" --title "${TAG}" \
            --notes "Cross-platform installer binaries built by scripts/release-binaries.sh (see REPRODUCE.txt)."
    fi
fi

UPLOAD_FILES=()
for name in ${ASSETS}; do
    UPLOAD_FILES+=("${OUT}/${name}")
done
gh release upload "${TAG}" --repo "${REPO}" --clobber \
    "${UPLOAD_FILES[@]}" "${OUT}/SHA256SUMS" "${OUT}/REPRODUCE.txt"

echo
echo "Published: https://github.com/${REPO}/releases/tag/${TAG}"
echo "Launcher URL check:"
for target in ${TARGETS}; do
    printf '  https://github.com/%s/releases/latest/download/%s\n' "${REPO}" "$(asset_name "${target%/*}" "${target#*/}")"
done
