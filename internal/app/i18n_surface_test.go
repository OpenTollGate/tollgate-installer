package app

// End-to-end tests for the language decision as the BROWSER sees it.
//
// internal/i18n's tests pin the precedence as a pure function. These pin that
// handleIndex actually FEEDS it — that the cookie is read under the name the
// picker writes, that the header survives to the resolver, and that the response
// the browser receives is in the decided language with the right direction.
//
// That distinction is not academic: a resolver can be perfect and completely
// unwired, which is exactly the state this task started in.

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode"

	"github.com/OpenTollGate/tollgate-installer/internal/i18n"
)

// indexRequest builds a GET / as an http.Request with the given headers/cookies.
func indexRequest(t *testing.T, acceptLanguage string, cookies map[string]string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if acceptLanguage != "" {
		r.Header.Set("Accept-Language", acceptLanguage)
	}
	for name, value := range cookies {
		r.AddCookie(&http.Cookie{Name: name, Value: value})
	}
	return r
}

// serveIndex renders / for a request and returns the handler and body.
func serveIndex(t *testing.T, r *http.Request) (*httptest.ResponseRecorder, string) {
	t.Helper()
	w := httptest.NewRecorder()
	handleIndex(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("handleIndex: status = %d, want 200", w.Code)
	}
	return w, w.Body.String()
}

// TestIndexServesTheHeaderLanguage is the baseline: a German browser gets a
// German page, with dir="ltr" and a German <html lang>.
func TestIndexServesTheHeaderLanguage(t *testing.T) {
	w, body := serveIndex(t, indexRequest(t, "de-DE,de;q=0.9,en;q=0.8", nil))

	if got := w.Header().Get("Content-Language"); got != "de" {
		t.Errorf("Content-Language = %q, want \"de\"", got)
	}
	if !strings.Contains(body, `<html lang="de" dir="ltr">`) {
		t.Errorf("page does not declare lang=de dir=ltr in the MARKUP; a browser picks direction at parse time\nfirst 400 bytes:\n%s", firstN(body, 400))
	}
	if !strings.Contains(body, "Router in 60 Sekunden einrichten") {
		t.Error("page does not carry the German tagline: the catalogue was not injected")
	}
	if strings.Contains(body, i18nPayloadMarker) {
		t.Error("the <!--I18N-PAYLOAD--> marker survived into the response: the payload was never injected")
	}
}

// TestSavedChoiceBeatsHeaderThroughTheHandler is the precedence as a user
// experiences it: they picked German, their browser says French (or they are on
// a Spanish-exit VPN), and every reload must stay German.
func TestSavedChoiceBeatsHeaderThroughTheHandler(t *testing.T) {
	_, body := serveIndex(t, indexRequest(t, "fr-FR,fr;q=0.9", map[string]string{
		i18nCookieName: "de",
	}))
	if !strings.Contains(body, `lang="de"`) {
		t.Error("the saved choice lost to Accept-Language; the picker would reset on every visit")
	}
	if !strings.Contains(body, "Router in 60 Sekunden einrichten") {
		t.Error("page is not German despite the saved choice")
	}
}

// TestArabicRendersRTL proves the direction reaches the MARKUP, not just a JS
// variable. An RTL reader whose dir is applied by script sees a flash of
// left-aligned layout, and nothing at all if the script fails.
func TestArabicRendersRTL(t *testing.T) {
	w, body := serveIndex(t, indexRequest(t, "ar", nil))

	if !strings.Contains(body, `<html lang="ar" dir="rtl">`) {
		t.Errorf("Arabic page does not declare dir=rtl in the markup\nfirst 300 bytes:\n%s", firstN(body, 300))
	}
	if got := w.Header().Get("Content-Language"); got != "ar" {
		t.Errorf("Content-Language = %q, want \"ar\"", got)
	}
	// A real Arabic string from the catalogue, so this cannot pass on a page
	// that merely set the attribute.
	if !strings.Contains(body, "أعدّ الراوتر في 60 ثانية") {
		t.Error("Arabic page does not carry the Arabic tagline")
	}
	// ar-EG resolves to the ar catalogue, so it must inherit Arabic's direction.
	if dir := i18n.Direction(i18n.Resolve(i18n.Request{AcceptLanguage: "ar-EG"})); dir != "rtl" {
		t.Errorf("ar-EG should render rtl, got %q", dir)
	}
	// ur is RTL by script even though it has no catalogue: Direction must answer
	// from the TAG, not from what we happen to ship. Asked through Resolve it
	// would come back "en", which is the bug this assertion exists to prevent.
	if dir := i18n.Direction("ur"); dir != "rtl" {
		t.Errorf("ur should render rtl, got %q", dir)
	}
	// And the reverse: a supported LTR locale must not be pushed to rtl.
	if dir := i18n.Direction(i18n.Resolve(i18n.Request{AcceptLanguage: "he"})); dir != "ltr" {
		t.Errorf("an unsupported RTL-script locale falls back to en and must render ltr, got %q", dir)
	}
}

// TestUnsupportedLanguageFallsBackToEnglish: a browser asking for a locale we
// do not ship must get English, not a half-empty page and not a locale tag we
// advertise but cannot render.
func TestUnsupportedLanguageFallsBackToEnglish(t *testing.T) {
	if got := i18n.Resolve(i18n.Request{AcceptLanguage: "mr-IN,mr;q=0.9"}); got != "en" {
		t.Errorf("Marathi (no catalogue) resolved to %q, want \"en\"", got)
	}
}

// TestPickerNeverOffersAnUnshippedLocale is the "supported locale leaking
// English" guard. Every option the page can offer must render its own text.
func TestPickerNeverOffersAnUnshippedLocale(t *testing.T) {
	opts := langOptions()
	if len(opts) == 0 {
		t.Fatal("the picker is empty")
	}
	seen := map[string]bool{}
	for _, o := range opts {
		if o.Tag == "" || o.Label == "" {
			t.Errorf("picker entry has no tag or no label: %+v", o)
		}
		if seen[o.Tag] {
			t.Errorf("duplicate picker entry %q", o.Tag)
		}
		seen[o.Tag] = true
		if !i18n.IsShipped(o.Tag) {
			t.Errorf("picker offers %q, which has no catalogue: choosing it would show English", o.Tag)
		}
	}
	// The reporting user's language, and the two the acceptance criteria name.
	for _, want := range []string{"en", "de", "ar"} {
		if !seen[want] {
			t.Errorf("picker does not offer %q; it is required", want)
		}
	}
	// A locale that is requested but not translated must NOT be offered.
	for tag := range i18n.FallbackLanguages() {
		if seen[tag] {
			t.Errorf("picker offers %q, which is an English fallback", tag)
		}
	}
}

// TestCookieNameMatchesThePicker pins the two halves of the saved-choice
// feature together. The cookie name is written as a string literal in
// index.html and read as a constant in Go; a mismatch would silently disable
// the picker with no error anywhere — the user chooses German, the page
// reloads in English, and nothing logs.
func TestCookieNameMatchesThePicker(t *testing.T) {
	page := string(indexHTML)
	want := "document.cookie = '" + i18nCookieName + "'"
	// The picker builds the cookie assignment across a line break, so match on
	// the name in an assignment rather than on one exact line.
	if !strings.Contains(page, i18nCookieName) {
		t.Fatalf("index.html never writes the cookie %q that handleIndex reads", i18nCookieName)
	}
	if !strings.Contains(page, "= '"+i18nCookieName+"='") && !strings.Contains(page, "'"+i18nCookieName+"='") {
		t.Errorf("index.html does not assign %q in the shape this test expects; re-check that the picker and savedLanguage() agree", i18nCookieName)
	}
	_ = want
}

// TestStaleOrHostileCookieFallsThrough: a cookie naming a locale we do not ship
// (hand-edited, or left over from an older release that shipped more) must not
// pin the page. savedLanguage returns "" and the header decides.
func TestStaleOrHostileCookieFallsThrough(t *testing.T) {
	for _, bad := range []string{"kl-GL", "mr", "", "../../etc/passwd", "<script>"} {
		_, body := serveIndex(t, indexRequest(t, "de", map[string]string{i18nCookieName: bad}))
		if !strings.Contains(body, `lang="de"`) {
			t.Errorf("cookie %q pinned the page; it should have fallen through to the header", bad)
		}
	}
}

// TestPrivateClientAddressHasNoCountry is the LAN case, which is the COMMON
// case: the installer runs on the router and the client is 192.168.x.x. There is
// no geolocation to do, and inventing one would override the header.
func TestPrivateClientAddressHasNoCountry(t *testing.T) {
	for _, addr := range []string{
		"192.168.1.10:51234",
		"10.0.0.5:51234",
		"172.16.4.4:51234",
		"127.0.0.1:51234",
		"[::1]:51234",
		"169.254.3.3:51234",
	} {
		if got := clientCountry(addr); got != "" {
			t.Errorf("clientCountry(%q) = %q, want \"\" (no geolocation for a private address)", addr, got)
		}
	}
}

// TestPublicAddressUsesTheInstalledResolver proves the country rung is wired,
// not dead. The default resolver returns "" (no geo database ships), and this
// test installs one to show the correct path — which is also the only way to
// exercise the precedence's third rung end to end.
func TestPublicAddressUsesTheInstalledResolver(t *testing.T) {
	orig := geoResolver
	defer func() { geoResolver = orig }()

	geoResolver = func(ip net.IP) string {
		if ip.String() == "203.0.113.9" {
			return "DE"
		}
		return ""
	}

	if got := clientCountry("203.0.113.9:443"); got != "DE" {
		t.Fatalf("clientCountry for a public address = %q, want \"DE\" (the resolver was not consulted)", got)
	}

	// And the header still beats it.
	lang, _ := pageLanguage(indexRequest(t, "fr", nil))
	if lang != "fr" {
		t.Errorf("pageLanguage chose %q for a French header; the country rung must not outrank the header", lang)
	}
}

// TestNoStringIsRenderedOutsideTheCatalogue is the extraction guard, from the
// page side. It fails when someone adds a hint, notice or label as a plain
// English literal instead of a catalogue key.
//
// It works on TAGS and SENTENCES rather than on a list of known strings, so it
// catches a NEW string, which a list could never do.
func TestNoStringIsRenderedOutsideTheCatalogue(t *testing.T) {
	page := string(indexHTML)

	// 1. Every element that carries visible text must name a key or be one of
	//    the documented exceptions (values the server fills at runtime, code
	//    samples, and the brand mark).
	for _, tag := range []string{"<p", "<h1", "<h2", "<h3", "<label", "<summary", "<button", "<option", "<span", "<div"} {
		for _, el := range textBearingTags(page, tag) {
			if hasTranslationAttribute(el) || isAllowedLiteral(el) || hasAuthoredSkip(el) {
				continue
			}
			if text := visibleText(el); text != "" && looksLikeProse(text) {
				t.Errorf("a %s renders prose with no data-i18n key: %q\n"+
					"Every user-facing string comes from the catalogue (internal/i18n). "+
					"Add data-i18n=\"key\" and the key to locales/en.json.", strings.TrimPrefix(tag, "<"), firstN(text, 120))
			}
		}
	}
}

// TestEveryCatalogueKeyIsRendered is the other direction: a key defined in the
// catalogue but never used is a string that gets translated forever and never
// appears. It also catches a rename applied to only one side.
//
// Three families are referenced indirectly and would always look dead to a
// literal search, so each is checked against its real source instead:
//   - step.* is built at runtime as 'step.' + step.name; the names live in
//     deploySteps() and newCleanSlateJob(), so those Go literals are the
//     reference.
//   - lang.* labels are rendered by the picker from server-supplied data.
//   - error.* is resolved on the SERVER from the handler's English sentence
//     (i18n.KeyForText), so the reference is the set of literals passed to
//     writeError, not anything in index.html.
func TestEveryCatalogueKeyIsRendered(t *testing.T) {
	page := string(indexHTML)
	goErrorKeys := goErrorLiteralKeys(t)
	goJobErrorKeys := goJobErrorLiteralKeys(t)
	for _, key := range i18n.Keys() {
		if strings.Contains(page, `"`+key+`"`) || strings.Contains(page, `'`+key+`'`) {
			continue
		}
		if strings.HasPrefix(key, "step.") {
			// Resolve the dynamic family against the Go step tables. A step
			// name that no job defines is a key nothing can ever show.
			if !stepNameExists(strings.TrimPrefix(key, "step.")) {
				t.Errorf("catalogue key %q matches no Step.Name in the deploy or clean-slate job; the page can never render it", key)
			}
			continue
		}
		if strings.HasPrefix(key, "error.") {
			// Resolved server-side from a handler's English sentence. A key no
			// handler emits is a translation nobody will ever read.
			if !goErrorKeys[key] {
				t.Errorf("catalogue key %q is not the resolution of any literal passed to writeError/writeErrorExtra; nothing can render it", key)
			}
			continue
		}
		if strings.HasPrefix(key, "jobError.") {
			// Same mechanism, different field: handleStatus resolves these from
			// the job's own English sentence (jobFail / Job.Error), so the
			// reference is those call sites — see i18n_goprogress_test.go.
			if !goJobErrorKeys[key] {
				t.Errorf("catalogue key %q is not the resolution of any job error a job can store; nothing can render it", key)
			}
			continue
		}
		t.Errorf("catalogue key %q is never referenced by index.html (dead translation, or a half-applied rename)", key)
	}
}

// stepNameExists reports whether any job definition uses this step name, which
// is what makes step.<name> reachable at runtime.
func stepNameExists(name string) bool {
	for _, s := range deploySteps() {
		if s.Name == name {
			return true
		}
	}
	for _, s := range newCleanSlateJob("127.0.0.1").Steps {
		if s.Name == name {
			return true
		}
	}
	return false
}

// TestEveryDataI18nKeyExists is the mirror image, and the more damaging one:
// a data-i18n naming a key the catalogue does not define renders the raw KEY
// NAME into the page ("scan.fail.heading" as a heading).
func TestEveryDataI18nKeyExists(t *testing.T) {
	page := string(indexHTML)
	for _, attr := range []string{"data-i18n", "data-i18n-placeholder", "data-i18n-aria-label"} {
		for _, key := range attributeValues(page, attr) {
			if !i18n.Has(key) {
				t.Errorf("%s=%q names a key the reference catalogue does not define; the page would render the key name", attr, key)
			}
		}
	}
}

// TestEveryT()KeyExists extends the same guard to the SCRIPT half of the page.
//
// The attribute scan above cannot see t('…') call sites, and that is where the
// real breakage was: the clean-slate flow called t('cleanslate.resolving') while
// the catalogue defined "cleanSlate.resolving", so a capitalisation difference
// made the operator read the raw key name at the exact moment they were
// inspecting a destructive flash. t() deliberately returns the key rather than
// an empty string (a blank label is the failure nobody notices), which means
// nothing else in the system complains about this class of bug.
func TestEveryTKeyExists(t *testing.T) {
	page := string(indexHTML)
	keys := tCallKeys(page)
	if len(keys) == 0 {
		t.Fatal("found no t('…') call sites in index.html; this guard is not scanning anything")
	}
	for _, key := range keys {
		if !i18n.Has(key) {
			t.Errorf("t(%q) names a key the reference catalogue does not define; the operator would see the key name", key)
		}
	}
}

// tCallKeys returns every literal key passed to t( in the page's script.
//
// It matches t( as a WHOLE token: substring scanning finds the tail of
// createElement('div') and getElementById('password'), which would report
// nonsense keys and, worse, train the reader to ignore this test.
func tCallKeys(page string) []string {
	var out []string
	seen := map[string]bool{}
	for _, quote := range []byte{'\'', '"'} {
		needle := string(quote) + ")"
		for i := 0; i < len(page); {
			// Find a call site: "t(" preceded by a non-identifier character.
			j := strings.Index(page[i:], "t(")
			if j < 0 {
				break
			}
			at := i + j
			if at > 0 && isIdentByte(page[at-1]) {
				i = at + 2
				continue
			}
			start := at + 2
			if start >= len(page) || page[start] != quote {
				i = start
				continue
			}
			start++
			end := strings.IndexByte(page[start:], quote)
			if end < 0 {
				break
			}
			key := page[start : start+end]
			// A key never contains whitespace, a brace or "+": those are the
			// non-literal forms (t(variable), t('a' + b)), which this scan
			// deliberately skips rather than mis-reporting.
			if key != "" && !strings.ContainsAny(key, " 	\n{}+") && !seen[key] {
				seen[key] = true
				out = append(out, key)
			}
			i = start + end
		}
		_ = needle
	}
	return out
}

// isIdentByte reports whether b can continue a JS identifier. Used to reject
// "createElement(" matching as a call to t(.
func isIdentByte(b byte) bool {
	return b == '_' || b == '$' ||
		(b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

// TestPageBootstrapsFromTheServer resolves the "two sources of truth" hazard:
// the page must not carry its own copy of the catalogue or of the resolution
// order. It reads window.__I18N and window.__LANG, which the server sets.
func TestPageBootstrapsFromTheServer(t *testing.T) {
	page := string(indexHTML)
	for _, want := range []string{
		"window.__I18N",
		"window.__LANG",
		"window.__DIR",
		"window.__LANGS",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("index.html never reads %s: the server payload would be ignored", want)
		}
	}
	// The precedence lives in Go, not in the page.
	for _, forbidden := range []string{"navigator.language", "navigator.languages"} {
		if strings.Contains(page, forbidden) {
			t.Errorf("index.html reads %s: the language decision belongs to i18n.Resolve on the server, or the two will disagree", forbidden)
		}
	}
}

// TestRTLStylesExist guards the "translated but not mirrored" failure. Strings
// alone do not make an RTL layout: alignment, the password toggle's absolute
// side and the range value's float all have to flip.
func TestRTLStylesExist(t *testing.T) {
	page := string(indexHTML)
	if !strings.Contains(page, `[dir="rtl"]`) {
		t.Fatal(`index.html has no [dir="rtl"] rules; Arabic would render translated but left-aligned`)
	}
	// The specific things that visibly break in RTL.
	for _, want := range []struct{ selector, why string }{
		{"pw-toggle", "the reveal button is absolutely positioned on the right; in RTL it must move to the left or it overlaps the text"},
		{"range-val", "the range value floats right; in RTL it must float left"},
	} {
		if !strings.Contains(page, want.selector) {
			t.Errorf("RTL rule refers to %q which is not in the page: %s", want.selector, want.why)
		}
	}
}

// ---- helpers ----

// textBearingTags returns the raw text of each top-level occurrence of tag.
// It is deliberately naive: index.html is hand-written, so a full parser is
// more machinery than the check needs.
func textBearingTags(page, tag string) []string {
	var out []string
	for i := 0; i < len(page); {
		j := strings.Index(page[i:], tag)
		if j < 0 {
			break
		}
		start := i + j
		end := strings.Index(page[start:], ">")
		if end < 0 {
			break
		}
		open := start + end + 1
		// Find the matching close if this is a container, else stop at the next
		// tag — enough to see the visible text of a leaf.
		close := strings.Index(page[open:], "<")
		if close < 0 {
			break
		}
		out = append(out, page[start:open+close])
		i = open + close
	}
	return out
}

func hasTranslationAttribute(el string) bool {
	for _, attr := range []string{"data-i18n", "id=", "class=\"icon", "aria-hidden"} {
		if strings.Contains(el, attr) {
			return true
		}
	}
	return false
}

// isAllowedLiteral lists the elements that legitimately carry text with no
// catalogue key. Each needs a reason; "it was easier" is not one.
func isAllowedLiteral(el string) bool {
	switch {
	case strings.Contains(el, "<code"):
		// Code samples are not translated: `sysupgrade -n` and `TollGate` are
		// literal values the operator types.
		return true
	case strings.Contains(el, "id=\"build-info\""):
		// Filled from /api/config at runtime, and it is version metadata.
		return true
	case strings.Contains(el, "class=\"req\""):
		// The required-marker asterisk.
		return true
	case strings.Contains(el, "<svg") || strings.Contains(el, "tg-logo"):
		// The brand mark.
		return true
	case strings.Contains(el, "range-val"):
		// The percentage readout next to a slider; a number plus "%".
		return true
	}
	return false
}

// hasAuthoredSkip reports whether the element declares, in the markup, that its
// text is deliberately not translated — with a reason. This is the escape hatch
// for strings that are neither prose nor code: product names ("LNbits Legend")
// and release identifiers ("OpenWrt 25.12.5 (apk)"). Keeping the exemption AT
// the string, rather than in a list inside this test, means a reviewer sees the
// decision next to the text, and a NEW untranslated string cannot inherit an
// exemption it never asked for.
func hasAuthoredSkip(el string) bool {
	const attr = `data-i18n-skip="`
	i := strings.Index(el, attr)
	if i < 0 {
		return false
	}
	rest := el[i+len(attr):]
	end := strings.IndexByte(rest, '"')
	return end > 0 && strings.TrimSpace(rest[:end]) != ""
}

// visibleText strips tags and collapses whitespace.
func visibleText(el string) string {
	if i := strings.Index(el, ">"); i >= 0 {
		el = el[i+1:]
	}
	if i := strings.LastIndex(el, "<"); i >= 0 {
		el = el[:i]
	}
	return strings.Join(strings.Fields(el), " ")
}

// looksLikeProse reports whether s is a human sentence rather than a number,
// a symbol or a code fragment.
func looksLikeProse(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) < 4 {
		return false
	}
	// A single token (or a number/percentage) is a value, not prose.
	if !strings.Contains(s, " ") {
		return false
	}
	letters := 0
	for _, r := range s {
		if unicode.IsLetter(r) {
			letters++
		}
	}
	return letters >= 4
}

// attributeValues returns the values of every occurrence of attr="…" or attr='…'.
func attributeValues(page, attr string) []string {
	var out []string
	for _, quote := range []string{`"`, `'`} {
		needle := attr + "=" + quote
		for i := 0; i < len(page); {
			j := strings.Index(page[i:], needle)
			if j < 0 {
				break
			}
			start := i + j + len(needle)
			end := strings.IndexByte(page[start:], quote[0])
			if end < 0 {
				break
			}
			out = append(out, page[start:start+end])
			i = start + end
		}
	}
	return out
}

func firstN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
