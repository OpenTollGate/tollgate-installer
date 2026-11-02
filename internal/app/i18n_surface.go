package app

// The HTTP surface of i18n: which language THIS request gets, and how that
// decision and its catalogue reach the page.
//
// # Why the server decides, not the browser
//
// The catalogue could be shipped wholesale and the language picked in JS from
// navigator.language. That would be wrong here for two reasons:
//
//  1. The saved choice lives in a cookie, and a cookie is only meaningful to the
//     side that reads it on the NEXT request. If the browser re-decided, the
//     server-rendered <html lang/dir> and the first paint would disagree with the
//     text that followed, so an RTL reader would see one frame of LTR layout.
//  2. A wrong guess costs a full round trip to correct, and the installer is
//     served by a process on the operator's own LAN — there is no CDN, no edge,
//     and no reason to defer a decision the server can make correctly.
//
// So handleIndex resolves ONCE per request from i18n.Resolve's precedence
// (saved cookie > Accept-Language > country tiebreaker > English), injects the
// merged catalogue, and rewrites <html lang/dir>. The page then renders from
// window.__I18N with no resolution logic of its own — there is exactly one copy
// of the precedence and it is the tested one.

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"strings"

	"github.com/OpenTollGate/tollgate-installer/internal/i18n"
)

// i18nCookieName is the saved explicit choice. It must match the name
// index.html's onLanguageChange() writes; TestCookieNameMatchesThePicker pins
// the two together, because a silent mismatch would make the picker look broken
// (choose German, get English back on reload) with no error anywhere.
const i18nCookieName = "tollgate_i18n"

// i18nPayloadMarker is the comment in index.html that renderIndex replaces.
// Using a marker rather than string-formatting the page keeps the template
// valid HTML on its own: opened straight from disk it is the English reference,
// which is also what a browser shows if injection ever fails.
const i18nPayloadMarker = "<!--I18N-PAYLOAD-->"

// langOption is one picker entry. Label is the language's own name so a reader
// who cannot read the current UI language can still find theirs.
type langOption struct {
	Tag   string `json:"tag"`
	Label string `json:"label"`
}

// langOptions returns the locales with a real catalogue, in menu order. It is
// derived from i18n.ShippedLanguages rather than written out again: a locale
// with no text must never appear in the picker, or choosing it would return
// English and read as a broken translation (see internal/i18n/catalogue.go).
func langOptions() []langOption {
	tags := i18n.ShippedLanguages()
	out := make([]langOption, 0, len(tags))
	for _, tag := range tags {
		out = append(out, langOption{Tag: tag, Label: i18n.LanguageLabel(tag)})
	}
	return out
}

// clientCountry is the country the request appears to come from, or "" when
// there is nothing honest to say.
//
// This installer is normally reached over the operator's LAN, so the client is
// an RFC1918 / link-local / loopback address and geolocation is not merely
// unreliable — there is no data to look up. Returning "" for those is the point:
// the country rung then simply does not fire and the decision rests on the
// header, which is the signal that actually knows the user's language.
//
// A public address is handed to geoResolver if one is installed. No geo database
// ships with the binary (see geoResolver), so today this returns "" in the common
// case as well; the rung is wired and tested, and stays inert rather than making
// a third-party network call from the operator's router.
func clientCountry(remoteAddr string) string {
	host := remoteAddr
	if h, _, err := net.SplitHostPort(remoteAddr); err == nil {
		host = h
	}
	ip := net.ParseIP(strings.TrimSpace(host))
	if ip == nil {
		return ""
	}
	// No opinion for anything that is not a globally routable address. This
	// covers 192.168/10/172.16, 169.254, 127.0.0.0/8 and ::1.
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
		return ""
	}
	return geoResolver(ip)
}

// geoResolver maps a public IP to an ISO-3166 alpha-2 country code. It is a
// package-level var so a deployment can install a geolocation database without
// this package depending on one, and so tests can exercise the rung.
//
// It returns "" by default, deliberately. The alternatives were worse: bundling
// a country database adds megabytes to a binary that is curl|bash'd onto a
// router's neighbour, and calling a public geo API would leak the operator's
// address from their own LAN to a third party for a tiebreaker that only fires
// when the browser sent no usable Accept-Language. A country code is the weakest
// signal in the precedence; it is allowed to be absent.
var geoResolver = func(net.IP) string { return "" }

// pageLanguage resolves the language for a request and returns it with its text
// direction.
func pageLanguage(r *http.Request) (lang, dir string) {
	req := i18n.Request{
		Saved:          savedLanguage(r),
		AcceptLanguage: r.Header.Get("Accept-Language"),
		CountryCode:    clientCountry(r.RemoteAddr),
	}
	lang = i18n.Resolve(req)
	return lang, i18n.Direction(lang)
}

// savedLanguage reads the picker's cookie. It returns "" for anything that is
// not a shipped locale, so a hand-edited or stale cookie falls through to the
// header instead of pinning the page to a language we cannot render.
func savedLanguage(r *http.Request) string {
	c, err := r.Cookie(i18nCookieName)
	if err != nil || c.Value == "" {
		return ""
	}
	tag := strings.TrimSpace(c.Value)
	if !i18n.IsShipped(tag) {
		return ""
	}
	return tag
}

// jsSafeJSON marshals v for embedding inside a <script> block.
//
// json.Marshal already escapes < > & (so a catalogue value can never close the
// script element), but not the two Unicode line separators, which terminate a
// string literal in pre-ES2019 engines. Escaping all four by hand is cheaper
// than reasoning about which browser the operator has.
func jsSafeJSON(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	replacer := strings.NewReplacer(
		"\u2028", `\u2028`,
		"\u2029", `\u2029`,
	)
	return []byte(replacer.Replace(string(raw))), nil
}

// i18nPayloadScript builds the inline script that carries the decision and the
// merged catalogue. Merging here (rather than sending only the locale's own
// file) means a key a translation has not caught up with keeps its English text
// and the page needs no fallback logic of its own.
func i18nPayloadScript(lang, dir string) []byte {
	var buf bytes.Buffer
	buf.WriteString("<script>\n")
	buf.WriteString("window.__LANG=")
	writeJSON(&buf, lang)
	buf.WriteString(";window.__DIR=")
	writeJSON(&buf, dir)
	buf.WriteString(";window.__LANGS=")
	writeJSON(&buf, langOptions())
	buf.WriteString(";window.__I18N=")
	writeJSON(&buf, i18n.Merge(lang))
	buf.WriteString(";\n</script>")
	return buf.Bytes()
}

// writeJSON writes v, or a safe placeholder if it cannot be encoded. A catalogue
// that fails to encode is a build defect (the values are embedded), and an empty
// object degrades the page to its shipped English text rather than to a blank.
func writeJSON(buf *bytes.Buffer, v any) {
	raw, err := jsSafeJSON(v)
	if err != nil {
		buf.WriteString("{}")
		return
	}
	buf.Write(raw)
}

// renderIndex returns the wizard HTML rendered for one language.
func renderIndex(lang, dir string) []byte {
	payload := i18nPayloadScript(lang, dir)

	page := indexHTML
	if i := bytes.Index(page, []byte(i18nPayloadMarker)); i >= 0 {
		var out bytes.Buffer
		out.Grow(len(page) + len(payload))
		out.Write(page[:i])
		out.Write(payload)
		out.Write(page[i+len(i18nPayloadMarker):])
		page = out.Bytes()
	}
	// The dir attribute has to be right in the MARKUP, not only after
	// applyI18n runs: the browser picks its direction and text-alignment rules
	// at parse time, so an RTL reader would otherwise see a frame of LTR
	// layout — and, if the script is blocked, all of it.
	page = bytes.Replace(page,
		[]byte(`<html lang="en" dir="ltr">`),
		[]byte(`<html lang="`+lang+`" dir="`+dir+`">`), 1)
	return page
}
