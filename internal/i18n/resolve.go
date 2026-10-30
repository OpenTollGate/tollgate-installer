// Language resolution: which locale a given request gets.
//
// The catalogue of locales we actually ship, the match rules (lookup) and the
// RTL/direction helpers live in catalogue.go, because a locale is only
// "supported" when it has text. This file owns the PRECEDENCE only.
//
// # WHY IP GEOLOCATION IS NOT THE PRIMARY SIGNAL
//
// The original request suggested detecting the language from the client's IP.
// That cannot work for this installer:
//
//   - An IP address resolves to a COUNTRY, not a language. Belgium, Switzerland,
//     Canada, India, Nigeria and South Africa all break the mapping outright:
//     one country, several languages, and often no single "default".
//   - The installer runs on the ROUTER, so the client usually appears as an
//     RFC1918 address (192.168.x.x). There is nothing to geolocate.
//   - A VPN, CGNAT or a mobile carrier makes even the public-address case
//     unreliable, and it fails in the direction that annoys users most: a German
//     speaker behind a Spanish exit node is told they speak Spanish.
//
// So the precedence is: an explicit saved choice, then the browser's own
// Accept-Language, and only then the IP country as a tiebreaker.
package i18n

import (
	"sort"
	"strconv"
	"strings"
)

// acceptEntry is one parsed Accept-Language entry.
type acceptEntry struct {
	tag string
	q   float64
}

// parseAcceptLanguage parses an Accept-Language header into tag/q pairs,
// highest q first, ties broken by the order the client listed them (browsers
// emit them in descending preference already).
//
// A malformed q is treated as 1.0: the header is a hint, and dropping a whole
// language because of a stray character helps nobody.
func parseAcceptLanguage(header string) []acceptEntry {
	var out []acceptEntry
	for _, raw := range strings.Split(header, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		tag := raw
		q := 1.0
		if i := strings.Index(raw, ";"); i >= 0 {
			tag = strings.TrimSpace(raw[:i])
			params := raw[i+1:]
			if j := strings.Index(params, "q="); j >= 0 {
				val := strings.TrimSpace(params[j+2:])
				if k := strings.IndexAny(val, ";,"); k >= 0 {
					val = val[:k]
				}
				if parsed, err := strconv.ParseFloat(strings.TrimSpace(val), 64); err == nil {
					q = parsed
				}
			}
		}
		if tag == "" || tag == "*" {
			continue
		}
		out = append(out, acceptEntry{tag: tag, q: q})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].q > out[j].q })
	return out
}

// Request carries the signals Resolve may use. Fields are raw: parsing happens
// here so handlers stay dumb.
type Request struct {
	// Saved is an explicit choice the user made earlier (cookie). It outranks
	// everything: without that, a user who picks German is reset to their
	// browser's language on the next visit, which reads as a broken picker.
	Saved string
	// AcceptLanguage is the raw header.
	AcceptLanguage string
	// CountryCode is an ISO-3166 alpha-2 from IP geolocation, or "" when there
	// is no usable public address (the common case on a LAN). Used ONLY as a
	// tiebreaker.
	CountryCode string
}

// countryDefault maps a country to a language, for the tiebreaker only. The map
// is deliberately sparse and every value MUST be a SHIPPED tag: a country
// mapping to a locale we have no text for silently degrades by falling through
// to the base language, or worse, selects a picker entry that renders English.
// TestCountryMapOnlyNamesShippedLanguages enforces that.
//
// Absent = no opinion, which is the honest answer for Belgium, Switzerland,
// Canada, India and friends: one country, several languages, no safe default.
var countryDefault = map[string]string{
	"DE": "de", "AT": "de",
	"FR": "fr", "ES": "es", "MX": "es", "AR": "es", "CO": "es",
	"BR": "pt", "PT": "pt",
	"IT": "it",
	"JP": "ja",
	"CN": "zh-Hans", "TW": "zh-Hant", "HK": "zh-Hant",
	"RU": "ru",
	"TR": "tr", "VN": "vi", "ID": "id",
	"SA": "ar", "EG": "ar-EG", "AE": "ar",
	"US": "en", "GB": "en", "AU": "en", "IE": "en",
	// Bangladesh (bn), Pakistan (ur) and India (hi) are deliberately ABSENT:
	// we ship no catalogue for those languages yet, and a country entry naming
	// an unshipped locale does not degrade gracefully — lookup() returns "",
	// the rung silently does nothing, and the map reads as if it handled them.
	// They are recorded in requestedLanguages as explicit fallbacks instead.
}

// Resolve returns the language tag to render, in precedence order:
//
//  1. Saved — an explicit user choice.
//  2. Accept-Language — the browser's own preference, q-ordered.
//  3. CountryCode — a tiebreaker, only when the header gave us nothing usable.
//  4. DefaultLanguage.
//
// It never returns "", so callers can use the result directly as a catalogue key.
func Resolve(r Request) string {
	tag, _ := ResolveWithSource(r)
	return tag
}

// ResolveWithSource is the single implementation of the precedence, returning the
// deciding rule's name alongside the tag (for the UI to explain itself, and for
// tests to assert the ORDER rather than only the outcome).
//
// There is deliberately only ONE copy of this logic. An earlier version had
// Resolve and ResolveWithSource each walking the rules, and a mutation that
// reordered the rules in Resolve alone passed the whole suite — because every
// ordering test asserted on ResolveWithSource. Two copies of an ordering
// contract is one copy too many.
func ResolveWithSource(r Request) (tag, source string) {
	if t := lookup(r.Saved); t != "" {
		return t, "saved"
	}
	for _, e := range parseAcceptLanguage(r.AcceptLanguage) {
		if t := lookup(e.tag); t != "" {
			return t, "accept-language"
		}
	}
	if r.CountryCode != "" {
		if want, ok := countryDefault[strings.ToUpper(strings.TrimSpace(r.CountryCode))]; ok {
			if t := lookup(want); t != "" {
				return t, "country"
			}
		}
	}
	return DefaultLanguage, "default"
}
