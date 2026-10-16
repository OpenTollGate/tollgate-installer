package app

import (
	"strings"
	"testing"
)

// The wizard UI's logo and favicon are embedded as inline SVG in index.html, with no
// input seam at all:
//
//	index.html:7    <link rel="icon" href="data:image/svg+xml,...">   (the favicon)
//	index.html:220  <h1 aria-label="TollGate"><svg class="tg-logo" ...>  (the wordmark)
//
// and the pinned installer has NO TOLLGATE_LOGO / brandAsset / logoURL anywhere. So a
// white-label build cannot show its own logo however the caller is shaped. This is the
// same wall the captive portal hit, and it has the same answer: contribute a slot.
//
// Both halves are asserted, as everywhere else in this work:
//
//	(A) DE-BRAND IS UNCHANGED. With no brand asset supplied, the served HTML must be
//	    BYTE-IDENTICAL to the embedded file. A branded build must not perturb what
//	    upstream users see.
//	(B) A SUPPLIED ASSET IS USED, and it is validated before it is interpolated into
//	    HTML.

// (A) No asset => the page is untouched, byte for byte.
func TestBrandedHTMLWithoutAssetsIsUnchanged(t *testing.T) {
	got := brandedHTML(string(indexHTML), "", "")
	if got != string(indexHTML) {
		t.Fatalf("HTML changed with no brand assets supplied:\n got %d bytes\nwant %d bytes",
			len(got), len(indexHTML))
	}
}

// (A) The wordmark must still be present and self-describing with no asset.
func TestBrandedHTMLKeepsTheTollGateWordmarkWithoutAssets(t *testing.T) {
	got := brandedHTML(string(indexHTML), "", "")
	if !strings.Contains(got, `aria-label="TollGate"`) {
		t.Error("the default wordmark's accessible name disappeared")
	}
	if !strings.Contains(got, "tg-logo") {
		t.Error("the default wordmark's class disappeared")
	}
}

// (B) A supplied logo URL replaces the inline SVG wordmark.
func TestBrandedHTMLUsesASuppliedLogo(t *testing.T) {
	// The accessible name follows the brand, so the brand must be set for this test to
	// mean anything. Set it and restore, since it is process-wide.
	prev := brandEnvValue
	SetBrandToken("net4sats")
	t.Cleanup(func() { SetBrandToken(prev) })

	got := brandedHTML(string(indexHTML), "https://cdn.example.net/n4s.svg", "")
	if !strings.Contains(got, "https://cdn.example.net/n4s.svg") {
		t.Error("the supplied logo URL is not in the served HTML")
	}
	if strings.Contains(got, `viewBox="0 0 138 28"`) {
		t.Error("the built-in wordmark SVG paths are still present alongside the supplied logo")
	}
	// The accessible name must follow the brand, not keep claiming TollGate.
	if !strings.Contains(got, `aria-label="Net4sats"`) {
		t.Error("no accessible name for the branded logo; a screen reader would announce nothing")
	}
	if strings.Contains(got, `aria-label="TollGate"`) {
		t.Error("the TollGate accessible name survived a branded build")
	}
}

// (B) A supplied favicon URL replaces the inline data-URL favicon.
func TestBrandedHTMLUsesASuppliedFavicon(t *testing.T) {
	got := brandedHTML(string(indexHTML), "", "https://cdn.example.net/icon.png")
	if !strings.Contains(got, "https://cdn.example.net/icon.png") {
		t.Error("the supplied favicon is not in the served HTML")
	}
	if strings.Contains(got, `rel="icon" href="data:image/svg+xml`) {
		t.Error("the built-in inline favicon survived a branded build")
	}
}

// (B) The asset values come from outside, so they are validated before interpolation.
// A logo URL that could break out of the attribute is refused outright rather than
// escaped: a bad brand input must not become an XSS in the operator's browser.
func TestBrandAssetURLValidation(t *testing.T) {
	bad := []string{
		`"><script>alert(1)</script>`,
		"javascript:alert(1)",
		"data:text/html,<script>alert(1)</script>",
		`x" onload="alert(1)`,
		"https://ok.example/a\"b.svg",
		"HTTPS://UPPER.example/a.svg", // uppercase scheme is fine actually
	}
	for _, in := range bad[:5] {
		if got := sanitizeBrandAssetURL(in); got != "" {
			t.Errorf("sanitizeBrandAssetURL(%q) = %q, want it refused", in, got)
		}
	}
	good := []string{
		"https://cdn.example.net/n4s.svg",
		"http://localhost:8080/logo.png",
		"/static/logo.svg",
		"logo.svg",
	}
	for _, in := range good {
		if got := sanitizeBrandAssetURL(in); got != in {
			t.Errorf("sanitizeBrandAssetURL(%q) = %q, want it kept", in, got)
		}
	}
}
