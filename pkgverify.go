package main

// Package integrity verification — pre-release security audit finding C2-I-03.
//
// The install path accepted ANY bytes. There was no checksum and no signature
// check anywhere between "download the package" and "install it as root":
//
//	opkg install --force-downgrade --force-reinstall --force-overwrite --force-depends
//	apk  add    --allow-untrusted --force-overwrite
//
// (the apk branch explicitly disables apk's own signature check), and the only
// crypto/sha256 call in the tree hashed the URL *string* to name a cache file.
// A truncated download, a poisoned staging-cache entry, a swapped mirror, or a
// modified release asset therefore installed as root with no integrity gate and
// no warning — and, because the post-install check only compared versions, the
// deploy still reported success.
//
// What this file adds
// -------------------
// Every tollgate-wrt package we are about to push to the router is verified
// against a digest published OUTSIDE the byte stream we downloaded. Three
// sources, in order of preference:
//
//  1. `<release>/SHA256SUMS` — a manifest asset published with the release.
//     Preferred: one artifact to sign, and it covers every arch/format. The
//     feed publishes this today for NO release yet (verified 2026-09-23: the
//     v0.6.0-alpha2-pre9 release carries exactly 14 assets, all .ipk/.apk), so
//     this source lights up the moment the feed-side change lands. Until then
//     it is attempted and skipped.
//  2. `<asset-URL>.sha256` — a per-asset sidecar, same idea, no parsing.
//  3. The GitHub release API's own per-asset `digest` field
//     (`GET /repos/<owner>/<repo>/releases/tags/<tag>` → `assets[].digest`,
//     `sha256:<hex>`). GitHub computes this server-side and publishes it today
//     for every feed release, so verification is REAL now, not aspirational.
//
// Only (3) is an INDEPENDENT anchor (cold cross-family review 2026-09-23,
// finding 2 — the original claim was too broad):
//
//   - (3) is served by a DIFFERENT origin (api.github.com) over its own
//     connection, so a redirect/mirror/host substituting different package
//     bytes cannot satisfy it.
//   - (1) and (2) are fetched from the SAME origin as the package bytes. A
//     matching digest from there proves only that one origin served consistent
//     bytes: it still catches truncation and at-rest corruption, but an
//     attacker who controls (or MITMs) that host serves the package and its
//     digest together and satisfies the check trivially. A same-origin digest
//     is therefore reported as UNVERIFIED, never as a pass — the step says so
//     explicitly, and TOLLGATE_REQUIRE_PACKAGE_DIGEST=1 rejects it. Making (1)
//     a real anchor needs the feed to SIGN the manifest with a pinned key
//     (audit C3-05); until then it is same-origin by construction.
//
// What (3) does and does not buy, stated honestly: the digest does not arrive
// over the connection that carried the package bytes, so it catches a partial
// or corrupted download, a wrong/substituted file at rest in the staging cache,
// a redirect/mirror serving different bytes, and a swapped /tmp file on the
// router — the whole "wrong or altered package installed as root" class — and it
// turns each of them into a FAILED deploy instead of a green one. It cannot
// detect a compromise of the GitHub release itself (digest and bytes would be
// attacker-fresh together); closing that needs the feed-published manifest (1)
// plus a pinned signing key, which is a separate, already-identified change
// (audit C3-05).
//
// Policy (fail closed, never a silent pass):
//
//   - digest published and DIFFERENT      → fatal: the deploy fails, naming the
//     asset, both digests, and where the expected digest came from.
//   - bytes that cannot be the expected package (wrong magic, absurdly small) →
//     fatal.
//   - no digest published anywhere        → the step renders "warn" with
//     NOT VERIFIED, and TOLLGATE_REQUIRE_PACKAGE_DIGEST=1 makes it fatal for
//     unattended/release runs.
//
// The cache is not exempt: the on-disk staging cache is never trusted for
// package bytes (see persistableDiskAsset), and the in-memory cache is verified
// because verification runs on the bytes immediately before they are pushed,
// whichever path produced them.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

const (
	// sha256SumsAssetName is the release-level checksum manifest asset.
	sha256SumsAssetName = "SHA256SUMS"
	// sha256SidecarSuffix is the per-asset sidecar suffix (<asset>.sha256).
	sha256SidecarSuffix = ".sha256"
	// requirePkgDigestEnv makes a missing published digest fatal. Off by
	// default so an operator can still install during a feed outage; ON for
	// unattended/release runs, where "we could not verify what we installed"
	// must not be reported as success.
	requirePkgDigestEnv = "TOLLGATE_REQUIRE_PACKAGE_DIGEST"
	// minPackageBytes is a floor below which the bytes cannot be a
	// tollgate-wrt package (the shipped assets are ~7.9-9.6 MB; the Go binary
	// plus packaging alone exceeds this). It catches a truncated transfer and
	// a HTML/JSON error page saved as the "package", cheaply.
	minPackageBytes = 100 * 1024
	// ipk/pkg/apk magic numbers, as shipped by the feed release.
	//   .ipk = gzip        (nested tar: debian-binary, control.tar.gz, data.tar.gz)
	//   .apk = ADB format  ("ADBd", apk-tools 3)
	ipkMagic = "\x1f\x8b"
	apkMagic = "ADBd"
)

// githubAPIBase is the API root used to resolve a release's published asset
// digests. A variable so tests can point it at a local server.
var githubAPIBase = "https://api.github.com"

// githubReleaseHosts are the hosts whose
// /<owner>/<repo>/releases/download/<tag>/<asset> URLs are treated as GitHub
// release assets and looked up through the API. A variable so tests can point a
// release fixture at a local server.
var githubReleaseHosts = []string{"github.com", "www.github.com"}

// isGitHubReleaseHost reports whether u's host (ignoring any port) is one of
// githubReleaseHosts.
func isGitHubReleaseHost(u *url.URL) bool {
	h := strings.ToLower(u.Host)
	if i := strings.LastIndex(h, ":"); i >= 0 {
		h = h[:i]
	}
	for _, allowed := range githubReleaseHosts {
		if h == allowed {
			return true
		}
	}
	return false
}

// httpGetBytesForDigest fetches a digest/manifest URL. A variable so tests can
// serve manifests without network access. Returns the body and the HTTP status.
var httpGetBytesForDigest = func(rawURL string, headers map[string]string) ([]byte, int, error) {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, 0, err
	}
	// Manifests and the release API are GitHub endpoints; the Accept header
	// pins the API version so the JSON field names cannot drift under us.
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body := make([]byte, 0, 4096)
	buf := make([]byte, 32*1024)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			body = append(body, buf[:n]...)
			if len(body) > 8<<20 { // manifests are tiny; never buffer a package here
				break
			}
		}
		if err != nil {
			break
		}
	}
	return body, resp.StatusCode, nil
}

// sha256Hex is the lowercase hex sha256 of data — the spelling used by every
// manifest format and by GitHub's API.
func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

var hexDigestRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// normalizeDigest strips an optional "<algo>:" prefix (GitHub's API uses
// "sha256:<hex>") and lowercases the result. It returns "" for anything that is
// not a 64-character hex sha256 — so a malformed or truncated digest can never
// be mistaken for a match.
func normalizeDigest(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if i := strings.LastIndex(s, ":"); i >= 0 {
		algo, rest := s[:i], s[i+1:]
		if algo != "sha256" {
			return ""
		}
		s = rest
	}
	if !hexDigestRe.MatchString(s) {
		return ""
	}
	return s
}

// assetNameFromURL returns the final path element of an asset URL (no query or
// fragment), e.g. "tollgate-wrt_0.6.0_alpha2_pre9_aarch64_cortex-a53.ipk".
func assetNameFromURL(assetURL string) string {
	if assetURL == "" {
		return ""
	}
	u, err := url.Parse(assetURL)
	if err != nil {
		// Fall back to a plain split so a malformed-but-usable URL still
		// yields a name for matching.
		trimmed := strings.TrimRight(assetURL, "/")
		if i := strings.LastIndex(trimmed, "/"); i >= 0 {
			return trimmed[i+1:]
		}
		return ""
	}
	return pathBase(u.Path)
}

// pathBase is path.Base without importing path for one call.
func pathBase(p string) string {
	p = strings.TrimRight(p, "/")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

// releaseAssetDir returns the URL prefix an asset URL sits under
// (".../releases/download/<tag>/"), which is where a release-level manifest
// (SHA256SUMS) lives. "" when the URL is not a release-asset URL.
func releaseAssetDir(assetURL string) string {
	const marker = "/releases/download/"
	if i := strings.Index(assetURL, marker); i >= 0 {
		rest := assetURL[i+len(marker):]
		if j := strings.Index(rest, "/"); j > 0 {
			return assetURL[:i+len(marker)] + rest[:j] + "/"
		}
	}
	return ""
}

// releaseAssetRef identifies a GitHub release asset URL so its published digest
// can be looked up through the API.
//
//	https://github.com/<owner>/<repo>/releases/download/<tag>/<name>
//	  → owner, repo, tag, name
func releaseAssetRef(assetURL string) (owner, repo, tag, name string, ok bool) {
	u, err := url.Parse(assetURL)
	if err != nil || u.Host == "" {
		return "", "", "", "", false
	}
	if !isGitHubReleaseHost(u) {
		return "", "", "", "", false
	}
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(segs) < 6 || segs[2] != "releases" || segs[3] != "download" {
		return "", "", "", "", false
	}
	name = segs[len(segs)-1]
	if segs[0] == "" || segs[1] == "" || segs[4] == "" || name == "" {
		return "", "", "", "", false
	}
	return segs[0], segs[1], segs[4], name, true
}

// parseChecksumManifestFor finds the digest recorded for assetName in a
// GNU-coreutils-style manifest ("<hex>  <name>" / "<hex> *<name>"), tolerating
// CRLF and blank lines. It matches the FILE NAME exactly (both the full path
// form "./name" and the bare name), so an entry for a different asset — or for
// a different release's identical-looking name — cannot satisfy the lookup.
func parseChecksumManifestFor(manifest []byte, assetName string) string {
	if assetName == "" {
		return ""
	}
	for _, line := range strings.Split(string(manifest), "\n") {
		line = strings.TrimRight(line, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) < 2 {
			continue
		}
		digest := normalizeDigest(fields[0])
		if digest == "" {
			continue
		}
		listed := strings.TrimPrefix(fields[len(fields)-1], "*")
		listed = strings.TrimPrefix(listed, "./")
		if pathBase(listed) == assetName {
			return digest
		}
	}
	return ""
}

// firstDigestToken returns the first 64-hex token in a small sidecar file
// ("<hex>  <name>", "<hex>" alone, or "<hex>\n").
func firstDigestToken(body []byte) string {
	for _, f := range strings.Fields(strings.TrimRight(string(body), "\r\n")) {
		if d := normalizeDigest(strings.TrimPrefix(f, "*")); d != "" {
			return d
		}
	}
	return ""
}

// githubReleaseAssetDigest looks assetName up in the release's asset list and
// returns its published sha256 digest. GitHub computes `digest` server-side,
// so it is published independently of the bytes we downloaded.
func githubReleaseAssetDigest(assetURL, assetName string) (string, bool) {
	owner, repo, tag, name, ok := releaseAssetRef(assetURL)
	if !ok {
		return "", false
	}
	if assetName == "" {
		assetName = name
	}
	headers := map[string]string{
		"Accept":               "application/vnd.github+json",
		"X-GitHub-Api-Version": "2022-11-28",
	}
	// An optional token only raises the anonymous rate limit; it is never
	// required for a public release.
	for _, env := range []string{"TOLLGATE_GITHUB_TOKEN", "GITHUB_TOKEN", "GH_TOKEN"} {
		if tok := strings.TrimSpace(os.Getenv(env)); tok != "" {
			headers["Authorization"] = "Bearer " + tok
			break
		}
	}
	apiURL := githubAPIBase + "/repos/" + owner + "/" + repo + "/releases/tags/" + url.PathEscape(tag)
	body, status, err := httpGetBytesForDigest(apiURL, headers)
	if err != nil || status != http.StatusOK {
		return "", false
	}
	var rel struct {
		Assets []struct {
			Name   string `json:"name"`
			Digest string `json:"digest"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(body, &rel); err != nil {
		return "", false
	}
	for _, a := range rel.Assets {
		if a.Name == assetName {
			if d := normalizeDigest(a.Digest); d != "" {
				return d, true
			}
			return "", false
		}
	}
	return "", false
}

// publishedDigestForAsset resolves the digest a release publishes for the asset
// at assetURL, trying the release manifest, then a per-asset sidecar, then the
// GitHub API. Returns ("", "", false) when the release publishes no digest for
// it — which is reported as NOT VERIFIED, never as a pass.
//
// independent reports whether the digest came from an origin DIFFERENT from the
// one serving the package bytes. Only the GitHub API is independent; the
// manifest and the sidecar are fetched from the asset's own host, so a hostile
// (or MITMed) host serves both and defeats them (cold review finding 2). The
// caller treats a non-independent match as UNVERIFIED.
func publishedDigestForAsset(assetURL string) (digest, source string, independent bool) {
	name := assetNameFromURL(assetURL)
	if name == "" {
		return "", "", false
	}
	if dir := releaseAssetDir(assetURL); dir != "" {
		if body, status, err := httpGetBytesForDigest(dir+sha256SumsAssetName, nil); err == nil && status == http.StatusOK {
			if d := parseChecksumManifestFor(body, name); d != "" {
				return d, sha256SumsAssetName + " manifest on " + dir, false
			}
		}
	}
	if body, status, err := httpGetBytesForDigest(assetURL+sha256SidecarSuffix, nil); err == nil && status == http.StatusOK {
		if d := firstDigestToken(body); d != "" {
			return d, "per-asset " + sha256SidecarSuffix + " sidecar", false
		}
	}
	if d, ok := githubReleaseAssetDigest(assetURL, name); ok {
		return d, "GitHub release API asset digest", true
	}
	return "", "", false
}

// packageLooksStructural reports whether data can be the package format ext
// names at all. It is a cheap first gate (magic bytes + size floor) that runs
// before any network lookup, so an HTML error page saved as "the package" or a
// truncated transfer is rejected even when no digest is published.
func packageLooksStructural(ext string, data []byte) error {
	if len(data) < minPackageBytes {
		return fmt.Errorf("package is only %d bytes — cannot be a tollgate-wrt %s package (expected at least %d bytes)",
			len(data), ext, minPackageBytes)
	}
	switch ext {
	case ".ipk":
		if !strings.HasPrefix(string(data[:2]), ipkMagic) {
			return fmt.Errorf("package does not start with the gzip magic %q a .ipk requires (first bytes: % x)",
				ipkMagic, data[:min(4, len(data))])
		}
	case ".apk":
		if !strings.HasPrefix(string(data[:len(apkMagic)]), apkMagic) {
			return fmt.Errorf("package does not start with the apk-tools 3 ADB magic %q (first bytes: % x)",
				apkMagic, data[:min(4, len(data))])
		}
	}
	return nil
}

// pkgIntegrity is the verdict on a package's bytes.
type pkgIntegrity struct {
	// Status is one of: verified | mismatch | malformed | unverified.
	Status string
	// Detail is the operator-facing one-liner (also used as the step detail).
	Detail string
	Want   string
	Got    string
	// Source names where a published digest came from ("" when none did).
	Source string
}

// fatal reports whether this verdict must stop the deploy.
func (v pkgIntegrity) fatal() bool {
	switch v.Status {
	case "mismatch", "malformed":
		return true
	}
	return false
}

// verified reports whether the bytes were checked against a published digest
// AND matched.
func (v pkgIntegrity) verified() bool { return v.Status == "verified" }

// suffix is the short tag appended to the install step detail.
func (v pkgIntegrity) suffix() string {
	switch v.Status {
	case "verified":
		return " [sha256 verified]"
	case "unverified":
		return " [sha256 NOT VERIFIED]"
	}
	return ""
}

// installStepStatus maps a package-integrity verdict to the step-6 status.
//
// GREEN ("done") is reserved for a verdict that actually verified the bytes
// against an INDEPENDENT published digest. Every other state renders "warn":
// unverified, mismatch/malformed, and above all the ZERO VALUE — an install
// path that never consulted the gate at all renders no suffix, so a green
// "done" there would be indistinguishable from a verified install (cold
// cross-family review 2026-09-23, finding 1: the router-feed path used to do
// exactly that).
func installStepStatus(v pkgIntegrity) string {
	if v.verified() {
		return "done"
	}
	return "warn"
}

// routerFeedInstallVerdict is the verdict for the last-resort install from the
// ROUTER'S OWN configured package feeds: the bytes never passed through this
// process, so nothing about them was verified here. It exists so that path also
// renders "warn" with an explicit NOT VERIFIED suffix rather than a green
// "done" with none.
func routerFeedInstallVerdict() pkgIntegrity {
	return pkgIntegrity{
		Status: "unverified",
		Detail: "installed from the router's own package feeds — these bytes never passed through this installer, so no published digest was checked",
	}
}

// requirePackageDigest reports whether a missing published digest must stop the
// deploy (TOLLGATE_REQUIRE_PACKAGE_DIGEST=1).
func requirePackageDigest(getenv func(string) string) bool {
	switch strings.ToLower(strings.TrimSpace(getenv(requirePkgDigestEnv))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// verifyPackageBytes checks data — the bytes about to be pushed to the router —
// against the digest published for assetURL, and returns the verdict. It never
// panics and never returns a verdict that could be mistaken for a pass when the
// bytes were not actually checked.
func verifyPackageBytes(assetURL, ext string, data []byte) pkgIntegrity {
	got := sha256Hex(data)
	if err := packageLooksStructural(ext, data); err != nil {
		return pkgIntegrity{
			Status: "malformed",
			Detail: fmt.Sprintf("REFUSING to install %s from %s: %v", assetNameFromURL(assetURL), assetURL, err),
			Got:    got,
		}
	}
	want, source, independent := publishedDigestForAsset(assetURL)
	if want == "" {
		return pkgIntegrity{
			Status: "unverified",
			Detail: fmt.Sprintf("no published sha256 for %s (checked %s, the per-asset .sha256 sidecar, and the GitHub release API): installed bytes are UNVERIFIED (%s)",
				assetNameFromURL(assetURL), sha256SumsAssetName, got),
			Got: got,
		}
	}
	if want != got {
		return pkgIntegrity{
			Status: "mismatch",
			Detail: fmt.Sprintf("sha256 MISMATCH for %s from %s: expected %s (from the %s) but the bytes hash to %s — refusing to install altered or corrupted package bytes",
				assetNameFromURL(assetURL), assetURL, want, source, got),
			Want:   want,
			Got:    got,
			Source: source,
		}
	}
	if !independent {
		// Same-origin digest: a hostile (or intercepted) host that served the
		// package also served this expected value, so a match proves only
		// self-consistency, not authenticity. Never rendered as a pass.
		return pkgIntegrity{
			Status: "unverified",
			Detail: fmt.Sprintf("the %s for %s matches the package bytes, but that digest is served from the SAME HOST as the package itself — a host that served altered bytes could serve a matching digest, so this is not an independent anchor: installed bytes are UNVERIFIED (%s)",
				source, assetNameFromURL(assetURL), got),
			Want:   want,
			Got:    got,
			Source: source,
		}
	}
	return pkgIntegrity{
		Status: "verified",
		Detail: fmt.Sprintf("%s sha256 %s matches the digest published by the %s", assetNameFromURL(assetURL), want, source),
		Want:   want,
		Got:    got,
		Source: source,
	}
}

// checkPackageBytes is the deploy-path wrapper: it verifies the bytes, logs the
// verdict either way, and returns a fatal error when the deploy must stop. The
// caller turns that into jobFail so the step cannot render green.
func checkPackageBytes(job *Job, assetURL, ext string, data []byte) (pkgIntegrity, error) {
	v := verifyPackageBytes(assetURL, ext, data)
	switch {
	case v.fatal():
		job.addLog("ERROR: " + v.Detail)
		return v, fmt.Errorf("%s", v.Detail)
	case v.verified():
		job.addLog("Package integrity: " + v.Detail)
	case requirePackageDigest(os.Getenv):
		job.addLog("ERROR: " + v.Detail)
		return v, fmt.Errorf("%s (and %s=1 requires a published digest)", v.Detail, requirePkgDigestEnv)
	default:
		job.addLog("WARNING: " + v.Detail)
	}
	return v, nil
}

// routerFileSHA256 computes the sha256 of a file ON the router, for the
// router-side wget path where the bytes never pass through this process.
// Returns "" when the router has no sha256 tool — the caller then reports the
// package as unverified rather than assuming it is fine.
func routerFileSHA256(client *ssh.Client, path string) string {
	if client == nil {
		return ""
	}
	for _, cmd := range []string{
		"sha256sum " + path + " 2>/dev/null | awk '{print $1}'",
		"busybox sha256sum " + path + " 2>/dev/null | awk '{print $1}'",
		"openssl dgst -sha256 " + path + " 2>/dev/null | awk '{print $NF}'",
	} {
		if d := normalizeDigest(strings.TrimSpace(sshRun(client, cmd))); d != "" {
			return d
		}
	}
	return ""
}

// checkRouterFileDigest verifies a package the ROUTER downloaded itself, by
// hashing it on the router and comparing with the published digest. A missing
// sha256 applet (or a release with no published digest) yields the unverified
// verdict, never a pass.
func checkRouterFileDigest(job *Job, client *ssh.Client, assetURL, ext, remotePath string) (pkgIntegrity, error) {
	name := assetNameFromURL(assetURL)
	got := routerFileSHA256(client, remotePath)
	if got == "" {
		v := pkgIntegrity{
			Status: "unverified",
			Detail: fmt.Sprintf("cannot hash %s on the router (no sha256sum/busybox/openssl) — installed bytes are UNVERIFIED", name),
		}
		if requirePackageDigest(os.Getenv) {
			job.addLog("ERROR: " + v.Detail)
			return v, fmt.Errorf("%s (and %s=1 requires a verified digest)", v.Detail, requirePkgDigestEnv)
		}
		job.addLog("WARNING: " + v.Detail)
		return v, nil
	}
	want, source, independent := publishedDigestForAsset(assetURL)
	switch {
	case want == "":
		v := pkgIntegrity{
			Status: "unverified",
			Detail: fmt.Sprintf("no published sha256 for %s (checked %s, the per-asset .sha256 sidecar, and the GitHub release API): installed bytes are UNVERIFIED (%s)",
				name, sha256SumsAssetName, got),
			Got: got,
		}
		if requirePackageDigest(os.Getenv) {
			job.addLog("ERROR: " + v.Detail)
			return v, fmt.Errorf("%s (and %s=1 requires a published digest)", v.Detail, requirePkgDigestEnv)
		}
		job.addLog("WARNING: " + v.Detail)
		return v, nil
	case want != got:
		v := pkgIntegrity{
			Status: "mismatch",
			Detail: fmt.Sprintf("sha256 MISMATCH for %s downloaded on the router: expected %s (from the %s) but /tmp/%s hashes to %s — refusing to install altered or corrupted package bytes",
				name, want, source, pathBase(remotePath), got),
			Want:   want,
			Got:    got,
			Source: source,
		}
		job.addLog("ERROR: " + v.Detail)
		return v, fmt.Errorf("%s", v.Detail)
	}
	if !independent {
		v := pkgIntegrity{
			Status: "unverified",
			Detail: fmt.Sprintf("the %s for %s matches the bytes wget left on the router, but that digest is served from the SAME HOST as the package itself — not an independent anchor: installed bytes are UNVERIFIED (%s)",
				source, name, got),
			Want:   want,
			Got:    got,
			Source: source,
		}
		if requirePackageDigest(os.Getenv) {
			job.addLog("ERROR: " + v.Detail)
			return v, fmt.Errorf("%s (and %s=1 requires a verified digest)", v.Detail, requirePkgDigestEnv)
		}
		job.addLog("WARNING: " + v.Detail)
		return v, nil
	}
	v := pkgIntegrity{
		Status: "verified",
		Detail: fmt.Sprintf("%s sha256 %s matches the digest published by the %s (hashed on the router)", name, want, source),
		Want:   want,
		Got:    got,
		Source: source,
	}
	job.addLog("Package integrity: " + v.Detail)
	return v, nil
}
