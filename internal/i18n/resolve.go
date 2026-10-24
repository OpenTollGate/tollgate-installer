// Package i18n resolves the UI language for a request.
//
// The installer ships 21 locales (see SupportedLanguages). Which one a given
// request gets is decided here, in one place, so the order can be tested rather
// than re-derived per handler.
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

// supportedLanguage is one shipped locale.
type supportedLanguage struct {
	Tag   string // BCP-47 tag as we ship it, e.g. "de", "zh-Hans", "ar-EG"
	Label string // the language's own name, for the picker
}

// supportedLanguages is the shipped catalogue, in menu order.
//
// 21 languages by total speakers. Two entries carry a caveat recorded in the
// design note (docs/i18n.md): Nigerian Pidgin (pcm) and Wu Chinese (wuu) rank
// high by speaker count but have little written convention, so a locale may be
// dropped in favour of the next candidate rather than shipped as machine output
// of doubtful quality. The catalogue below is what we INTEND to ship; a locale
// with no catalogue file is simply not selectable, and English covers it.
var supportedLanguages = []supportedLanguage{
	{"en", "English"},
	{"zh-Hans", "简体中文"},
	{"zh-Hant", "繁體中文"},
	{"hi", "हिन्दी"},
	{"es", "Español"},
	{"fr", "Français"},
	{"ar", "العربية"},
	{"bn", "বাংলা"},
	{"pt", "Português"},
	{"ru", "Русский"},
	{"ur", "اردو"},
	{"id", "Bahasa Indonesia"},
	{"de", "Deutsch"},
	{"ja", "日本語"},
	{"pcm", "Naijá"},
	{"ar-EG", "العربية (مصر)"},
	{"mr", "मराठी"},
	{"vi", "Tiếng Việt"},
	{"te", "తెలుగు"},
	{"tr", "Türkçe"},
	{"ta", "தமிழ்"},
	{"wuu", "吴语"},
}

// DefaultLanguage is used when nothing else matches. English is the fallback
// for a practical reason as well as a conventional one: it is the language the
// existing UI is written in, so a missing catalogue degrades to "what we have
// today" rather than to a blank screen.
const DefaultLanguage = "en"

// SupportedLanguages returns the shipped tags in menu order.
func SupportedLanguages() []string {
	out := make([]string, 0, len(supportedLanguages))
	for _, l := range supportedLanguages {
		out = append(out, l.Tag)
	}
	return out
}

// LanguageLabel returns the language's own name, or "" when unsupported.
func LanguageLabel(tag string) string {
	for _, l := range supportedLanguages {
		if strings.EqualFold(l.Tag, tag) {
			return l.Label
		}
	}
	return ""
}

// IsRTL reports whether the language is written right-to-left. Arabic and Urdu
// need a mirrored layout, not only translated strings.
func IsRTL(tag string) bool {
	base := baseLanguage(tag)
	return base == "ar" || base == "ur"
}

// baseLanguage lowercases a tag and drops any region or script subtag.
// "de-AT" -> "de"; "zh-Hans-CN" -> "zh"; "ar-EG" -> "ar".
func baseLanguage(tag string) string {
	tag = strings.ToLower(strings.TrimSpace(tag))
	if i := strings.IndexAny(tag, "-_"); i >= 0 {
		tag = tag[:i]
	}
	return tag
}

// normalize lowercases a tag's language part but preserves the canonical casing
// of a script subtag, so "zh-hans" and "ZH-Hans" both become "zh-Hans". Region
// subtags are upper-cased ("ar-eg" -> "ar-EG").
func normalize(tag string) string {
	parts := strings.FieldsFunc(strings.TrimSpace(tag), func(r rune) bool { return r == '-' || r == '_' })
	if len(parts) == 0 {
		return ""
	}
	out := []string{strings.ToLower(parts[0])}
	for _, p := range parts[1:] {
		switch {
		case len(p) == 4: // script subtag
			out = append(out, strings.ToUpper(p[:1])+strings.ToLower(p[1:]))
		case len(p) == 2 || len(p) == 3: // region subtag
			out = append(out, strings.ToUpper(p))
		default:
			out = append(out, strings.ToLower(p))
		}
	}
	return strings.Join(out, "-")
}

// regionScriptSpecialCase maps a bare Chinese region to the SCRIPT it implies.
//
// "zh-TW" and "zh-HK" carry no script subtag, so the language+script step cannot
// match them, and the base-language step would return whichever zh variant is
// listed first (zh-Hans) — telling a Traditional reader they read Simplified.
// These regions use Traditional, so the mapping is knowledge that has to be
// encoded rather than derived.
var regionScriptSpecialCase = map[string]string{
	"zh-tw": "zh-Hant",
	"zh-hk": "zh-Hant",
	"zh-mo": "zh-Hant",
	"zh-cn": "zh-Hans",
	"zh-sg": "zh-Hans",
}

// lookup finds the best shipped tag for a requested tag.
//
// Match order: exact tag, then language+script (so "zh-Hans-CN" hits "zh-Hans"),
// then a region that implies a script (zh-TW -> zh-Hant), then the base language
// (so "de-AT" and "de" both hit "de"). Returns "" when nothing matches, so the
// caller can fall through rather than silently getting English from a helper
// that claims success.
func lookup(tag string) string {
	want := normalize(tag)
	if want == "" {
		return ""
	}
	for _, l := range supportedLanguages {
		if strings.EqualFold(normalize(l.Tag), want) {
			return l.Tag
		}
	}
	lower := strings.ToLower(want)
	// language+region that pins a script: "zh-tw" -> "zh-Hant"
	if sc, ok := regionScriptSpecialCase[lower]; ok {
		if t := lookupExact(sc); t != "" {
			return t
		}
	}
	// language+script: "zh-Hans-CN" -> "zh-Hans"
	parts := strings.Split(want, "-")
	if len(parts) > 1 && len(parts[1]) == 4 {
		if t := lookupExact(parts[0] + "-" + parts[1]); t != "" {
			return t
		}
	}
	// base language: "de-AT" -> "de"
	base := baseLanguage(want)
	for _, l := range supportedLanguages {
		if baseLanguage(l.Tag) == base {
			return l.Tag
		}
	}
	return ""
}

// lookupExact matches a tag against the catalogue by exact tag only.
func lookupExact(tag string) string {
	for _, l := range supportedLanguages {
		if strings.EqualFold(normalize(l.Tag), normalize(tag)) {
			return l.Tag
		}
	}
	return ""
}

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
	// Saved is an explicit choice the user made earlier (cookie / localStorage).
	// It outranks everything: without that, a user who picks German is reset to
	// their browser's language on the next visit, which reads as a broken picker.
	Saved string
	// AcceptLanguage is the raw header.
	AcceptLanguage string
	// CountryCode is an ISO-3166 alpha-2 from IP geolocation, or "" when there
	// is no usable public address (the common case on a LAN). Used ONLY as a
	// tiebreaker.
	CountryCode string
}

// countryDefault maps a country to a language, for the tiebreaker only. The map
// is deliberately sparse and every value MUST be a language we ship: a country
// mapping to an unshipped language silently degrades by falling through to
// lookup(), which matches on the base language and can therefore pick something
// surprising (CH -> "de" looked correct only because German happens to be
// shipped). TestCountryMapOnlyNamesShippedLanguages enforces that.
//
// Absent = no opinion, which is the honest answer for Belgium, Switzerland,
// Canada, India and friends: one country, several languages, no safe default.
var countryDefault = map[string]string{
	"DE": "de", "AT": "de",
	"FR": "fr", "ES": "es", "MX": "es", "AR": "es", "CO": "es",
	"BR": "pt", "PT": "pt",
	"JP": "ja",
	"CN": "zh-Hans", "TW": "zh-Hant", "HK": "zh-Hant",
	"RU": "ru",
	"TR": "tr", "VN": "vi", "ID": "id",
	"BD": "bn", "PK": "ur",
	"SA": "ar", "EG": "ar-EG", "AE": "ar",
	"US": "en", "GB": "en", "AU": "en", "IE": "en",
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
