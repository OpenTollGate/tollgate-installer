package app

// Brand ASSETS for the wizard UI.
//
// WHY THIS EXISTS
//
// The UI's logo and favicon are embedded as inline SVG in index.html and there is no
// input seam anywhere in this repo, so a white-label build cannot show its own mark
// however the caller is shaped. This adds the seam: two optional values that replace
// the built-in mark when supplied, and leave the page BYTE-IDENTICAL when not.
//
// Design choices, each deliberate:
//
//   * A missing asset changes NOTHING. The unbranded page is the embedded file verbatim,
//     so an upstream user's UI cannot be perturbed by this feature.
//   * Asset URLs are VALIDATED, not escaped. A bad brand input must not become an XSS in
//     the operator's browser, and the browser that renders this page is the operator's
//     own — the highest-value target in the flow.
//   * The replacement is anchored on the markup that is actually there (the `tg-logo`
//     class and the inline favicon), and refuses to act if that anchor is missing, so a
//     future HTML edit fails loudly here rather than silently dropping the brand.

import (
	"regexp"
	"strings"
)

// brandAssetURLPattern accepts an absolute http(s) URL, an absolute path, or a bare
// relative filename. Deliberately restrictive: no scheme other than http/https (so
// `javascript:` and `data:` are refused), and no characters that could terminate the
// surrounding HTML attribute.
var brandAssetURLPattern = regexp.MustCompile(`^(https?://[A-Za-z0-9._~:/?#@!$&()*+,;=%-]+|/[A-Za-z0-9._~:/?#@!$&()*+,;=%-]*|[A-Za-z0-9._-]+\.(svg|png|webp|jpg|jpeg|gif|ico))$`)

// sanitizeBrandAssetURL returns the URL if it is safe to interpolate into an HTML
// attribute, or "" if it is not. "" always means "keep the built-in asset".
func sanitizeBrandAssetURL(in string) string {
	u := strings.TrimSpace(in)
	if u == "" || !brandAssetURLPattern.MatchString(u) {
		return ""
	}
	return u
}

// anchorLogo is the wordmark's opening tag as it appears in index.html.
const anchorLogo = `<h1 aria-label="TollGate"><svg class="tg-logo"`

// anchorFavicon is the inline favicon link's prefix as it appears in index.html.
const anchorFavicon = `<link rel="icon" href="data:image/svg+xml,`

// brandedHTML returns the page to serve, with the brand's logo and favicon substituted
// when supplied. With no assets it returns the input unchanged, byte for byte.
//
// brand is used for the logo's accessible name; when it is empty the existing
// aria-label is left alone.
func brandedHTML(page, logoURL, faviconURL string) string {
	logo := sanitizeBrandAssetURL(logoURL)
	icon := sanitizeBrandAssetURL(faviconURL)
	if logo == "" && icon == "" {
		return page
	}

	out := page

	if logo != "" {
		// Replace the whole <h1>…</h1> wordmark with an <img> carrying the supplied src.
		if i := strings.Index(out, anchorLogo); i >= 0 {
			if j := strings.Index(out[i:], "</h1>"); j >= 0 {
				end := i + j + len("</h1>")
				name := "TollGate"
				if b := sanitizeBrandToken(brandTokenFromEnv()); b != "" {
					name = brandDisplaySpelling(b)
				} else if b := sanitizeBrandToken(brandTokenHint); b != "" {
					name = brandDisplaySpelling(b)
				}
				repl := `<h1 aria-label="` + name + `"><img class="tg-logo" src="` + logo + `" alt="` + name + `"></h1>`
				out = out[:i] + repl + out[end:]
			}
		}
	}

	if icon != "" {
		// Replace only the href value, keeping the tag's other attributes intact.
		if i := strings.Index(out, anchorFavicon); i >= 0 {
			start := i + len(`<link rel="icon" href="`)
			if j := strings.Index(out[start:], `"`); j >= 0 {
				out = out[:start] + icon + out[start+j:]
			}
		}
	}

	return out
}

// brandTokenHint is set by the caller (the CLI) before serving, so the logo's
// accessible name matches the brand being deployed. Kept as a package variable rather
// than threaded through every handler signature: the HTML is embedded once and served
// from several places, and the value is process-wide by nature.
var brandTokenHint string

// brandTokenFromEnv reads the brand the caller supplied. Mirrors the flag/env pairing
// already used for --trust-host-key, so there is one convention in this repo.
func brandTokenFromEnv() string {
	return brandEnvValue
}

// brandEnvValue is set from the --brand flag or TOLLGATE_BRAND at startup.
var brandEnvValue string

// SetBrandToken records the brand for the current process. Called from flag parsing.
func SetBrandToken(token string) {
	brandEnvValue = sanitizeBrandToken(token)
	brandTokenHint = brandEnvValue
}
