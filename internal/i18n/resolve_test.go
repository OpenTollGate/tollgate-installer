package i18n

import (
	"strings"
	"testing"
)

// contains reports whether haystack holds needle. Local helper so this file
// does not need the slices package (the module targets an older Go for the
// installer's build matrix).
func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// The precedence is the contract. Each test below pins ONE rung, and the
// ordering tests pin that a higher rung beats a lower one even when the lower
// rung would also have produced a valid answer. A test that only checks
// "resolve returns something" would pass with the order inverted.
//
// The bug this prevents: a user picks German, and the next visit resets them to
// the browser's language. That reads as a broken picker, and no single-signal
// test would catch it.

func TestSavedChoiceOutranksEverything(t *testing.T) {
	got, src := ResolveWithSource(Request{
		Saved:          "de",
		AcceptLanguage: "fr-FR,fr;q=0.9",
		CountryCode:    "FR",
	})
	if got != "de" || src != "saved" {
		t.Fatalf("saved choice lost: got %q via %q, want \"de\" via \"saved\"", got, src)
	}
}

func TestAcceptLanguageOutranksCountry(t *testing.T) {
	// The German speaker on a Spanish-exit VPN. Country says ES, header says de.
	// The header must win, or we tell them they speak Spanish.
	got, src := ResolveWithSource(Request{
		AcceptLanguage: "de-DE,de;q=0.9,en;q=0.8",
		CountryCode:    "ES",
	})
	if got != "de" || src != "accept-language" {
		t.Fatalf("header lost to country: got %q via %q, want \"de\" via \"accept-language\"", got, src)
	}
}

func TestCountryIsOnlyATiebreaker(t *testing.T) {
	got, src := ResolveWithSource(Request{CountryCode: "DE"})
	if got != "de" || src != "country" {
		t.Fatalf("country tiebreaker did not fire: got %q via %q", got, src)
	}
}

func TestFallsBackToEnglish(t *testing.T) {
	if got, src := ResolveWithSource(Request{}); got != "en" || src != "default" {
		t.Fatalf("empty request: got %q via %q, want \"en\" via \"default\"", got, src)
	}
}

// A country with no unambiguous language must NOT pick one. Belgium is the
// canonical case, and guessing there is how you tell half the country they
// speak the wrong language.
func TestAmbiguousCountryHasNoOpinion(t *testing.T) {
	for _, cc := range []string{"BE", "CH", "CA", "IN"} {
		if got := Resolve(Request{CountryCode: cc}); got != DefaultLanguage {
			t.Errorf("country %s resolved to %q; want the default (no opinion for this country)", cc, got)
		}
	}
}

// A German speaker with a de-AT or bare-de header must both land on German.
// A browser sends "de-AT" far more often than bare "de".
func TestRegionVariantsHitTheBaseLanguage(t *testing.T) {
	cases := map[string]string{
		"de-AT": "de",
		"de-DE": "de",
		"de":    "de",
		"de-CH": "de",
		"DE-at": "de", // case-insensitive
	}
	for header, want := range cases {
		if got := Resolve(Request{AcceptLanguage: header}); got != want {
			t.Errorf("Accept-Language %q -> %q, want %q", header, got, want)
		}
	}
}

// Chinese is the case where the SCRIPT matters, not the region. A single "zh"
// would be wrong for Traditional readers.
func TestChineseScriptIsHonoured(t *testing.T) {
	cases := map[string]string{
		"zh-CN":      "zh-Hans",
		"zh-Hans":    "zh-Hans",
		"zh-Hans-CN": "zh-Hans",
		"zh-TW":      "zh-Hant",
		"zh-Hant":    "zh-Hant",
		"zh-HK":      "zh-Hant",
	}
	for header, want := range cases {
		if got := Resolve(Request{AcceptLanguage: header}); got != want {
			t.Errorf("Accept-Language %q -> %q, want %q", header, got, want)
		}
	}
}

// q-values and the browser's listed order are both real signals.
//
// The q-value case deliberately lists the LOWER-q language FIRST: a test with
// "fr;q=0.9,de;q=0.8" passes whether or not q is honoured, because French also
// wins on list order. Ordering the input against the answer is the only way to
// tell "we read q" from "we returned the first thing we saw" — a mutation that
// deleted the sort passed the whole suite until this case was added.
func TestQValuesAndOrderAreRespected(t *testing.T) {
	if got := Resolve(Request{AcceptLanguage: "de;q=0.8,fr;q=0.9"}); got != "fr" {
		t.Errorf("highest q should win regardless of listing order: got %q, want \"fr\"", got)
	}
	if got := Resolve(Request{AcceptLanguage: "de,fr"}); got != "de" {
		t.Errorf("equal q should keep the listed order: got %q, want \"de\"", got)
	}
	// A browser realistically emits "de-DE,de;q=0.9,en-US;q=0.8,en;q=0.7".
	if got := Resolve(Request{AcceptLanguage: "de-DE,de;q=0.9,en-US;q=0.8,en;q=0.7"}); got != "de" {
		t.Errorf("realistic German header: got %q, want \"de\"", got)
	}
}

// An unsupported language in the header must fall THROUGH to the next entry,
// not stop the search.
func TestUnsupportedHeaderFallsThrough(t *testing.T) {
	if got := Resolve(Request{AcceptLanguage: "kl-GL,de;q=0.5"}); got != "de" {
		t.Errorf("an unsupported language must not stop the scan: got %q, want \"de\"", got)
	}
}

// A wildcard or malformed header is common from scripts and does not mean
// "English" -- it means "no opinion", so the country tiebreaker should still
// get its chance.
func TestWildcardOrGarbageIsNoOpinion(t *testing.T) {
	if got := Resolve(Request{AcceptLanguage: "*", CountryCode: "DE"}); got != "de" {
		t.Errorf("wildcard should leave the tiebreaker room: got %q, want \"de\"", got)
	}
	if got := Resolve(Request{AcceptLanguage: ",;q=,", CountryCode: "DE"}); got != "de" {
		t.Errorf("garbage should leave the tiebreaker room: got %q, want \"de\"", got)
	}
}

// A malformed q must not discard the whole entry; the header is a hint.
func TestMalformedQKeepsTheEntry(t *testing.T) {
	if got := Resolve(Request{AcceptLanguage: "de;q=abc"}); got != "de" {
		t.Errorf("a malformed q dropped the language: got %q, want \"de\"", got)
	}
}

// An unknown saved value must fall through rather than pin something wrong.
func TestUnknownSavedFallsThrough(t *testing.T) {
	got, src := ResolveWithSource(Request{Saved: "kl-GL", AcceptLanguage: "de"})
	if got != "de" || src != "accept-language" {
		t.Fatalf("an unknown saved tag should fall through: got %q via %q", got, src)
	}
}

func TestRTLDetection(t *testing.T) {
	for _, tag := range []string{"ar", "ar-EG", "ur"} {
		if !IsRTL(tag) {
			t.Errorf("%q should be RTL", tag)
		}
	}
	for _, tag := range []string{"en", "de", "zh-Hans", "he"} {
		// NOTE: Hebrew is RTL in reality but is NOT in our catalogue, so IsRTL
		// answering false for it is consistent with what we ship. If he is added
		// later this expectation must change -- hence it is written down.
		if IsRTL(tag) {
			t.Errorf("%q should not be RTL", tag)
		}
	}
}

// The catalogue must be internally consistent: unique tags, and a label for
// every one (the picker renders the labels).
func TestCatalogueIsConsistent(t *testing.T) {
	seen := map[string]bool{}
	for _, l := range requestedLanguages {
		if seen[l.Tag] {
			t.Errorf("duplicate tag %q in the catalogue", l.Tag)
		}
		seen[l.Tag] = true
		if l.Label == "" {
			t.Errorf("tag %q has no label", l.Tag)
		}
		if LanguageLabel(l.Tag) != l.Label {
			t.Errorf("LanguageLabel(%q) did not round-trip", l.Tag)
		}
		// The catalogue status is a two-valued field with a documented
		// invariant: either there is text, or there is a recorded reason.
		// An entry that is neither is a gap someone forgot; an entry that is
		// both is a contradiction.
		if l.Catalogue == "" && l.FallbackWhy == "" {
			t.Errorf("tag %q is neither shipped nor explained: add a catalogue or say why English is served", l.Tag)
		}
		if l.Catalogue != "" && l.FallbackWhy != "" {
			t.Errorf("tag %q claims catalogue %q AND a fallback reason; pick one", l.Tag, l.Catalogue)
		}
		if l.Catalogue != "" {
			if got := catalogueKeys(l.Catalogue); len(got) == 0 {
				t.Errorf("tag %q names catalogue %q, but no such catalogue has any keys", l.Tag, l.Catalogue)
			}
		}
	}
	if !seen[DefaultLanguage] {
		t.Fatalf("the default %q is not in the catalogue", DefaultLanguage)
	}
	// The operator asked for "the 21 most spoken languages". The requested list
	// holds 23 ENTRIES because they are not all distinct languages:
	//   zh-Hans / zh-Hant  one language, two scripts (both needed: they are not
	//                      mutually readable to a casual reader)
	//   ar / ar-EG         Modern Standard Arabic plus an Egyptian variant entry
	//   it                 the operator's permitted substitution for locales that
	//                      could not be produced credibly
	// So the count is asserted with that reasoning attached rather than as a bare
	// number, and the SHIPPED count is checked separately below.
	if n := len(RequestedLanguages()); n != 23 {
		t.Errorf("requested list has %d entries, want 23 (21 languages; zh is two scripts, ar has a regional variant, plus the it substitution)", n)
	}

	// A locale is pickable only when it has text. Advertising one we cannot
	// render is the failure this whole design exists to avoid: the user picks
	// मराठी and the wizard answers in English.
	for _, tag := range ShippedLanguages() {
		if !IsShipped(tag) {
			t.Errorf("ShippedLanguages() offers %q, which IsShipped says has no catalogue", tag)
		}
	}
	for tag, why := range FallbackLanguages() {
		if IsShipped(tag) {
			t.Errorf("%q is listed as a fallback but has a catalogue", tag)
		}
		if strings.TrimSpace(why) == "" {
			t.Errorf("%q falls back to English with no recorded reason", tag)
		}
		if contains(ShippedLanguages(), tag) {
			t.Errorf("%q falls back to English but is still offered in the picker", tag)
		}
	}
}

// TestCountryMapOnlyNamesShippedLanguages guards a silent-degradation bug found
// while writing this file: the country map originally named "it", "ko", "th" and
// "uk", none of which we ship. lookup() falls through to the BASE language, so
// "IT" produced an empty match and the country rung quietly did nothing — while
// "CH" -> "de" appeared to work only because German happens to be shipped. The
// fix was to drop the unshipped values AND add this test.
func TestCountryMapOnlyNamesShippedLanguages(t *testing.T) {
	for cc, want := range countryDefault {
		if got := lookupExact(want); got != want {
			t.Errorf("country %s maps to %q, which is not an exact catalogue tag (lookup gave %q); "+
				"an unshipped value degrades by falling through to the base language", cc, want, got)
		}
	}
}

// The Chinese region mapping is knowledge, not derivation: "zh-TW" carries no
// script subtag, so without regionScriptSpecialCase it would match the
// first-listed zh variant and serve Simplified to a Traditional reader.
func TestChineseRegionsWithoutScriptResolveToTheRightScript(t *testing.T) {
	for region, want := range map[string]string{
		"zh-TW": "zh-Hant",
		"zh-HK": "zh-Hant",
		"zh-MO": "zh-Hant",
		"zh-CN": "zh-Hans",
		"zh-SG": "zh-Hans",
	} {
		if got := Resolve(Request{AcceptLanguage: region}); got != want {
			t.Errorf("%s -> %q, want %q", region, got, want)
		}
	}
}

// The precedence must hold on BOTH entry points. An earlier version of this file
// had Resolve and ResolveWithSource each walking the rules independently, and a
// mutation that reordered the rules in Resolve ALONE passed the whole suite,
// because every ordering test above asserts on ResolveWithSource. These tests
// exercise Resolve directly so the contract is pinned on the function callers
// actually use.
func TestPrecedenceHoldsOnResolveItself(t *testing.T) {
	cases := []struct {
		name string
		req  Request
		want string
	}{
		{"saved beats header and country", Request{Saved: "de", AcceptLanguage: "fr-FR", CountryCode: "FR"}, "de"},
		{"header beats country (the VPN case)", Request{AcceptLanguage: "de-DE,de;q=0.9", CountryCode: "ES"}, "de"},
		{"country when the header is silent", Request{CountryCode: "DE"}, "de"},
		{"default when nothing is known", Request{}, "en"},
	}
	for _, c := range cases {
		if got := Resolve(c.req); got != c.want {
			t.Errorf("%s: Resolve -> %q, want %q", c.name, got, c.want)
		}
	}
}

// Resolve and ResolveWithSource must never disagree. If they do, one of them has
// drifted, and callers of the "simple" one render a different language from what
// the UI reports.
func TestResolveAndResolveWithSourceAgree(t *testing.T) {
	requests := []Request{
		{},
		{Saved: "de", AcceptLanguage: "fr", CountryCode: "FR"},
		{AcceptLanguage: "zh-TW", CountryCode: "CN"},
		{AcceptLanguage: "kl-GL", CountryCode: "JP"},
		{AcceptLanguage: "*,garbage;q=,", CountryCode: "BE"},
	}
	for _, r := range requests {
		tag, _ := ResolveWithSource(r)
		if got := Resolve(r); got != tag {
			t.Errorf("Resolve(%+v) = %q but ResolveWithSource = %q; they have drifted", r, got, tag)
		}
	}
}

// Every shipped language must be resolvable from its own tag, so a saved choice
// or an exact header match always works.
func TestEveryShippedLanguageResolvesFromItsOwnTag(t *testing.T) {
	for _, tag := range ShippedLanguages() {
		if got := Resolve(Request{Saved: tag}); got != tag {
			t.Errorf("saved %q resolved to %q", tag, got)
		}
	}
}
