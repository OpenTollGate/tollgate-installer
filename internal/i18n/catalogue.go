// Package i18n owns the UI language of the installer: which locale a request
// gets (resolve.go) and what text that locale renders (this file).
//
// # THE ONE RULE THIS PACKAGE EXISTS TO ENFORCE
//
// A string a human reads comes from a catalogue, never from the markup or from
// a Go literal. index.html ships KEYS (data-i18n="…") and this package ships the
// TEXT. lint_test.go fails the build when a user-facing string appears outside a
// catalogue, so "we translated the wizard" is a property of the tree rather than
// a claim in a commit message.
//
// # WHY THE CATALOGUE DECIDES WHAT IS 'SUPPORTED'
//
// The operator asked for 21 languages (see docs/i18n.md). Shipping a locale we
// have no text for is worse than not shipping it: the picker would offer
// "मराठी", the user would choose it, and the wizard would answer in English —
// which reads as a broken translation, not as a missing one. So a tag is
// SELECTABLE only when it has a real catalogue, and everything else is recorded
// below as an explicit fallback-to-English with the reason attached. A Marathi
// browser gets English by falling through the resolver, which is the honest
// answer, rather than a "supported" locale that leaks English.
package i18n

import (
	"embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
)

//go:embed locales/*.json
var localeFS embed.FS

// CatalogueDir is where the shipped catalogues live, relative to this package.
const catalogueDir = "locales"

// language is one entry the operator asked for, and what we actually have for it.
type language struct {
	// Tag is the BCP-47 tag as we ship it, e.g. "de", "zh-Hans", "ar-EG".
	Tag string
	// Label is the language's own name, for the picker.
	Label string
	// Catalogue is the tag of the catalogue file this locale renders from, or ""
	// when we ship no text for it. An empty value is not a bug: it is the
	// explicit statement that English is served instead, and FallbackWhy says
	// why. ar-EG reuses the ar catalogue; zh-Hant does NOT reuse zh-Hans,
	// because the script differs, not just the region.
	Catalogue string
	// FallbackWhy is required exactly when Catalogue is empty.
	FallbackWhy string
}

// requestedLanguages is the operator's list (docs/i18n.md), in menu order, with
// the catalogue status of each. Keep the order: it is the picker order.
//
// EVERY entry here either has a catalogue or names why it does not. That
// invariant is enforced by TestEveryRequestedLocaleIsEitherShippedOrExplained.
var requestedLanguages = []language{
	{Tag: "en", Label: "English", Catalogue: "en"},
	{Tag: "zh-Hans", Label: "简体中文", Catalogue: "zh-Hans"},
	{Tag: "zh-Hant", Label: "繁體中文", Catalogue: "zh-Hant"},
	{Tag: "hi", Label: "हिन्दी", FallbackWhy: "No Hindi catalogue in this change, and Hindi UI prose has no single settled register (Devanagari technical vocabulary varies between Sanskritised and Hinglish forms). A translation written without native review risks reading as machine output; English is served until that review exists."},
	{Tag: "es", Label: "Español", Catalogue: "es"},
	{Tag: "fr", Label: "Français", Catalogue: "fr"},
	{Tag: "ar", Label: "العربية", Catalogue: "ar"},
	{Tag: "bn", Label: "বাংলা", FallbackWhy: "No Bengali catalogue in this change. Bengali technical vocabulary for networking and payments is not settled enough to translate without a native reviewer; English is served until then."},
	{Tag: "pt", Label: "Português", Catalogue: "pt"},
	{Tag: "ru", Label: "Русский", Catalogue: "ru"},
	{Tag: "ur", Label: "اردو", FallbackWhy: "No Urdu catalogue in this change. Urdu shares the Arabic script with our ar catalogue but not its vocabulary or idiom, so reusing ar would be wrong; English is served until native review exists."},
	{Tag: "id", Label: "Bahasa Indonesia", Catalogue: "id"},
	{Tag: "de", Label: "Deutsch", Catalogue: "de"},
	{Tag: "ja", Label: "日本語", Catalogue: "ja"},
	{Tag: "pcm", Label: "Naijá", FallbackWhy: "Nigerian Pidgin has no settled written convention for UI prose; a catalogue would be one author's spelling presented as the language. English is served (operator note, 2026-10-21)."},
	{Tag: "ar-EG", Label: "العربية (مصر)", Catalogue: "ar"},
	{Tag: "mr", Label: "मराठी", FallbackWhy: "Marathi shares Devanagari with Hindi but is a separate language with its own vocabulary; shipping a catalogue without native review risks Marathi that reads as mis-spelled Hindi. English is served until that review exists."},
	{Tag: "vi", Label: "Tiếng Việt", Catalogue: "vi"},
	{Tag: "te", Label: "తెలుగు", FallbackWhy: "No confidently-authored Telugu catalogue in this change; a doubtful one is worse than English. Queued for native review."},
	{Tag: "tr", Label: "Türkçe", Catalogue: "tr"},
	{Tag: "ta", Label: "தமிழ்", FallbackWhy: "No confidently-authored Tamil catalogue in this change; a doubtful one is worse than English. Queued for native review."},
	{Tag: "wuu", Label: "吴语", FallbackWhy: "Wu Chinese is overwhelmingly spoken, not written; there is no web convention to follow, so a catalogue would be invented rather than translated. English (or zh-Hans for a Chinese reader) is served (operator note, 2026-10-21)."},
	// The operator's permitted substitution for locales that could not be
	// produced credibly, and useful for this installer's European user base.
	{Tag: "it", Label: "Italiano", Catalogue: "it"},
}

// DefaultLanguage is used when nothing else matches. English is the fallback for
// a practical reason as well as a conventional one: it is the language the UI
// was originally written in, so a missing key degrades to "what we had" rather
// than to a blank screen.
const DefaultLanguage = "en"

// ShippedLanguages returns the tags the picker may offer: those with a real
// catalogue, in menu order. This is what "supported" means in this package.
func ShippedLanguages() []string {
	out := make([]string, 0, len(requestedLanguages))
	for _, l := range requestedLanguages {
		if l.Catalogue != "" {
			out = append(out, l.Tag)
		}
	}
	return out
}

// RequestedLanguages returns every tag the operator asked for, shipped or not,
// in menu order. Used by the docs and by the tests that pin the fallback list.
func RequestedLanguages() []string {
	out := make([]string, 0, len(requestedLanguages))
	for _, l := range requestedLanguages {
		out = append(out, l.Tag)
	}
	return out
}

// FallbackLanguages returns the requested tags served in English, each with the
// recorded reason. The reason is part of the contract: "we do not ship it" is a
// decision that has to be justifiable, not a gap someone forgot.
func FallbackLanguages() map[string]string {
	out := map[string]string{}
	for _, l := range requestedLanguages {
		if l.Catalogue == "" {
			out[l.Tag] = l.FallbackWhy
		}
	}
	return out
}

// LanguageLabel returns the language's own name, or "" when the tag is not in
// the requested list at all.
func LanguageLabel(tag string) string {
	for _, l := range requestedLanguages {
		if strings.EqualFold(l.Tag, tag) {
			return l.Label
		}
	}
	return ""
}

// cataloguedTag returns the catalogue tag for a shipped tag, or "" when the tag
// has no text.
func cataloguedTag(tag string) string {
	for _, l := range requestedLanguages {
		if strings.EqualFold(l.Tag, tag) {
			return l.Catalogue
		}
	}
	return ""
}

// IsShipped reports whether the tag renders from a real catalogue.
func IsShipped(tag string) bool { return cataloguedTag(tag) != "" }

// IsRTL reports whether the language is written right-to-left. Arabic and Urdu
// need a mirrored layout, not only translated strings, which is why the page
// sets dir on <html> from this rather than from the text itself.
func IsRTL(tag string) bool {
	base := baseLanguage(tag)
	return base == "ar" || base == "ur"
}

// Direction returns "rtl" or "ltr" for the <html dir> attribute.
func Direction(tag string) string {
	if IsRTL(tag) {
		return "rtl"
	}
	return "ltr"
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

// lookup finds the best SHIPPED tag for a requested tag.
//
// Match order: exact tag, then a region that implies a script (zh-TW ->
// zh-Hant), then language+script (so "zh-Hans-CN" hits "zh-Hans"), then the base
// language (so "de-AT" and "de" both hit "de"). Returns "" when nothing matches,
// so the caller can fall through rather than silently getting English from a
// helper that claims success.
//
// Only shipped tags match: a request for a locale we have no text for must fall
// through to the next signal (or English), not select a picker entry that would
// render English.
func lookup(tag string) string {
	want := normalize(tag)
	if want == "" {
		return ""
	}
	if t := lookupExact(want); t != "" {
		return t
	}
	// language+region that pins a script: "zh-tw" -> "zh-Hant"
	if sc, ok := regionScriptSpecialCase[strings.ToLower(want)]; ok {
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
	for _, l := range requestedLanguages {
		if l.Catalogue == "" {
			continue
		}
		if baseLanguage(l.Tag) == base {
			return l.Tag
		}
	}
	return ""
}

// lookupExact matches a tag against a SHIPPED catalogue by exact tag only.
func lookupExact(tag string) string {
	for _, l := range requestedLanguages {
		if l.Catalogue == "" {
			continue
		}
		if strings.EqualFold(normalize(l.Tag), normalize(tag)) {
			return l.Tag
		}
	}
	return ""
}

// ---- the catalogues themselves ----

// catalogue is one locale's key -> text map.
type catalogue map[string]string

var (
	loadOnce   sync.Once
	catalogues map[string]catalogue // keyed by CATALOGUE tag, e.g. "ar" for ar-EG
	loadErr    error
)

// loadAll reads every embedded catalogue once. A failure here is a build defect
// (the JSON is embedded), so init panics rather than letting the wizard serve an
// empty page; tests call loadAll directly to see the error instead.
//
// Keys beginning with "_" are METADATA, not user-facing text: en.json carries a
// "_comment" explaining the file's contract. They are stripped on load so no
// runtime lookup, parity check or renderer can ever see them — otherwise every
// translation would be asked to supply a translation of the comment, and a
// missing one would make Merge() emit it into the page payload.
func loadAll() (map[string]catalogue, error) {
	loadOnce.Do(func() {
		entries, err := localeFS.ReadDir(catalogueDir)
		if err != nil {
			loadErr = fmt.Errorf("read %s: %w", catalogueDir, err)
			return
		}
		out := map[string]catalogue{}
		for _, e := range entries {
			name := e.Name()
			if !strings.HasSuffix(name, ".json") {
				continue
			}
			tag := strings.TrimSuffix(name, ".json")
			raw, err := localeFS.ReadFile(catalogueDir + "/" + name)
			if err != nil {
				loadErr = fmt.Errorf("read %s: %w", name, err)
				return
			}
			var c catalogue
			if err := json.Unmarshal(raw, &c); err != nil {
				loadErr = fmt.Errorf("parse %s: %w", name, err)
				return
			}
			for k := range c {
				if strings.HasPrefix(k, "_") {
					delete(c, k)
				}
			}
			out[tag] = c
		}
		if loadErr != nil {
			return
		}
		if _, ok := out[DefaultLanguage]; !ok {
			loadErr = fmt.Errorf("no %s catalogue", DefaultLanguage)
			return
		}
		catalogues = out
	})
	return catalogues, loadErr
}

func init() {
	if _, err := loadAll(); err != nil {
		panic("i18n: embedded catalogues are unusable: " + err.Error())
	}
}

// Keys returns every key the reference catalogue defines, sorted. This is the
// contract every other catalogue is measured against.
func Keys() []string {
	c, err := loadAll()
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(c[DefaultLanguage]))
	for k := range c[DefaultLanguage] {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// CatalogueKeys returns the sorted keys of one catalogue tag (not a locale tag:
// pass "ar", not "ar-EG").
func catalogueKeys(tag string) []string {
	c, err := loadAll()
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(c[tag]))
	for k := range c[tag] {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Has reports whether the reference catalogue defines key.
func Has(key string) bool {
	c, err := loadAll()
	if err != nil {
		return false
	}
	_, ok := c[DefaultLanguage][key]
	return ok
}

// T renders key in the request's language.
//
// Resolution is per KEY, not per locale: a string missing from a shipped
// catalogue falls back to English rather than to the key name, so a partial
// translation degrades to a readable sentence. A key that is in NO catalogue
// returns the key itself — deliberately visible, because a silently empty label
// is the one failure mode nobody notices.
func T(lang, key string) string {
	c, err := loadAll()
	if err != nil {
		return key
	}
	if tag := cataloguedTag(lang); tag != "" {
		if v, ok := c[tag][key]; ok && v != "" {
			return v
		}
	}
	if v, ok := c[DefaultLanguage][key]; ok && v != "" {
		return v
	}
	return key
}

// Merge returns one locale's catalogue overlaid on English: the payload the page
// boots from. A key the locale is missing keeps its English text, so the client
// needs no fallback logic of its own (and cannot disagree with T).
func Merge(lang string) map[string]string {
	c, err := loadAll()
	if err != nil {
		return map[string]string{}
	}
	out := make(map[string]string, len(c[DefaultLanguage]))
	for k, v := range c[DefaultLanguage] {
		out[k] = v
	}
	if tag := cataloguedTag(lang); tag != "" && tag != DefaultLanguage {
		for k, v := range c[tag] {
			if v != "" {
				out[k] = v
			}
		}
	}
	return out
}
