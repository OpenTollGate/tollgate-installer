package i18n

// Guards that make "every user-facing string comes from a catalogue" a property
// of the tree instead of a claim in a commit message.
//
// The failure this prevents is specific and easy to reintroduce: someone adds a
// new hint, notice or error to index.html as a plain English string. Nothing
// breaks. The string simply never appears in any other language, and no test
// notices — which is exactly how a half-translated UI rots. These tests fail the
// build instead, so the cost of adding a string is naming it.

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestEveryCatalogueHasExactlyTheReferenceKeys is the parity contract. A key
// missing from a translation is not fatal at runtime (T falls back to English
// per key), but it IS a silent gap: the page renders a half-English paragraph
// and nobody files a bug. A key present in a translation but absent from the
// reference is dead text that will never be rendered, which usually means a key
// was renamed in one file only.
func TestEveryCatalogueHasExactlyTheReferenceKeys(t *testing.T) {
	ref := Keys()
	if len(ref) == 0 {
		t.Fatal("the reference catalogue is empty")
	}
	all, err := loadAll()
	if err != nil {
		t.Fatalf("catalogues are unreadable: %v", err)
	}
	for tag := range all {
		got := catalogueKeys(tag)
		missing := diff(ref, got)
		extra := diff(got, ref)
		for _, k := range missing {
			t.Errorf("%s.json is missing %q (the page will render English text mid-sentence)", tag, k)
		}
		for _, k := range extra {
			t.Errorf("%s.json has %q, which the reference catalogue does not define (renamed key?)", tag, k)
		}
	}
}

// TestEveryCatalogueKeepsItsPlaceholders is the check that catches the one
// translation bug a bilingual reader would not: a translator drops {ip} or
// renames it to {IP}. The Go side is fine, the page renders, and the sentence
// silently omits the address the operator needs — or renders the literal
// "{ip}". Placeholders are part of the key's contract, not of its prose.
func TestEveryCatalogueKeepsItsPlaceholders(t *testing.T) {
	ref, err := loadAll()
	if err != nil {
		t.Fatalf("catalogues are unreadable: %v", err)
	}
	for tag, cat := range ref {
		for key, english := range ref[DefaultLanguage] {
			text, ok := cat[key]
			if !ok {
				continue // parity is TestEveryCatalogueHasExactlyTheReferenceKeys' job
			}
			want := placeholders(english)
			got := placeholders(text)
			if !equalSets(want, got) {
				t.Errorf("%s: %q placeholders are %v, reference has %v\n  ref: %s\n  got: %s",
					tag, key, got, want, english, text)
			}
		}
	}
}

// TestEveryCatalogueIsNonEmptyAndNotJustEnglishCopy catches the other silent
// failure: a locale file copied from en.json as a placeholder. Shipping that is
// worse than not shipping the locale, because the picker promises a language
// and delivers English — see the package comment.
//
// A handful of strings are legitimately identical across languages (product
// names, "WAN (Ethernet)", "Lightning"), so this asserts a RATIO rather than
// every key.
func TestEveryCatalogueIsNonEmptyAndNotJustEnglishCopy(t *testing.T) {
	ref, err := loadAll()
	if err != nil {
		t.Fatalf("catalogues are unreadable: %v", err)
	}
	english := ref[DefaultLanguage]
	for tag, cat := range ref {
		if tag == DefaultLanguage {
			continue
		}
		if len(cat) == 0 {
			t.Errorf("%s.json is empty", tag)
			continue
		}
		var same, total int
		for k, v := range cat {
			total++
			if english[k] == v {
				same++
			}
		}
		// 15% covers the genuinely-invariant strings and nothing else. A file
		// that is a copy of en.json scores 100%.
		if total > 0 && same*100/total > 15 {
			t.Errorf("%s.json has %d/%d strings identical to English (%d%%): is it a copy of en.json?",
				tag, same, total, same*100/total)
		}
	}
}

// TestEveryShippedTagHasACatalogueFile pins the join between the two halves of
// this package: a locale is "shipped" only if its Catalogue tag resolves to a
// loaded file. Without this, a typo in a Catalogue field would make the picker
// offer a locale whose text silently comes from English.
func TestEveryShippedTagHasACatalogueFile(t *testing.T) {
	all, err := loadAll()
	if err != nil {
		t.Fatalf("catalogues are unreadable: %v", err)
	}
	for _, l := range requestedLanguages {
		if l.Catalogue == "" {
			continue
		}
		if _, ok := all[l.Catalogue]; !ok {
			t.Errorf("%q claims catalogue %q, which did not load (files: %v)", l.Tag, l.Catalogue, sortedKeys(all))
		}
	}
}

// TestNoOrphanCatalogueFile is the reverse: a locale file nothing points at is
// dead weight that will drift, and it usually means someone added a catalogue
// and forgot to offer it.
func TestNoOrphanCatalogueFile(t *testing.T) {
	claimed := map[string]bool{}
	for _, l := range requestedLanguages {
		if l.Catalogue != "" {
			claimed[l.Catalogue] = true
		}
	}
	all, err := loadAll()
	if err != nil {
		t.Fatalf("catalogues are unreadable: %v", err)
	}
	for tag := range all {
		if !claimed[tag] {
			t.Errorf("locales/%s.json exists but no requested language names it: add the picker entry or delete the file", tag)
		}
	}
}

// TestKeysAreAllLiveLivesInTheAppPackage is a pointer, not a test: whether a key
// is RENDERED can only be answered by reading index.html, which is embedded in
// internal/app, not here. The reverse direction — no literal string in the page
// that skips the catalogue — is enforced by
// internal/app/i18n_surface_test.go, which owns the page.
func TestKeysAreAllLiveLivesInTheAppPackage(t *testing.T) {
	// Kept as a named location so a reader looking for the "dead key" guard
	// finds the pointer instead of concluding there is none.
	t.Skip("enforced by internal/app: TestEveryCatalogueKeyIsRendered")
}

// TestCatalogueValuesArePlainText pins the catalogue's contract with the page.
//
// Every value is written with textContent (never innerHTML), because several
// strings sit next to router-supplied and user-supplied values (see
// internal/app/dom_sink_test.go). A translator who pastes <b> or &nbsp; into a
// value would therefore produce visible literal markup, and an HTML-looking
// value near user input is exactly the shape a future innerHTML "fix" would
// dangerously accommodate.
//
// ONE exemption: scan.fail.try3 is a shell command the operator copies
// verbatim, and bash requires the literal < for process substitution and for
// the <ROUTER-IP> placeholder. Text aside, the whole point of that string is
// that its characters are exactly what must be typed — so it is checked for
// absence of MARKUP tags rather than of angle brackets, and the exemption is
// named here rather than being a silent skip.
func TestCatalogueValuesArePlainText(t *testing.T) {
	const shellCommandKey = "scan.fail.try3"

	ref, err := loadAll()
	if err != nil {
		t.Fatalf("catalogues are unreadable: %v", err)
	}
	// markupTags catches "<b>", "</div>", "<br/>" but not "<(curl" or
	// "<ROUTER-IP>": the letter after '<' has to be a known tag opener followed
	// by a tag-ish character.
	markupTags := regexp.MustCompile(`</?[a-zA-Z][a-zA-Z0-9]*(\s|/?>)`)

	for tag, cat := range ref {
		for key, v := range cat {
			if v == "" {
				t.Errorf("%s: %q is empty; omit the key instead so it falls back to English", tag, key)
			}
			if markupTags.MatchString(v) {
				t.Errorf("%s: %q contains what looks like an HTML tag (%q); values are written with textContent, so it would render literally", tag, key, markupTags.FindString(v))
			}
			if key != shellCommandKey && strings.ContainsAny(v, "<>") && !markupTags.MatchString(v) {
				// Angle brackets outside the shell command are almost always a
				// mistyped entity or an attempt to bold a word.
				t.Errorf("%s: %q contains angle brackets (%q) outside the one key allowed to; use plain text", tag, key, v)
			}
			if strings.Contains(v, "&nbsp;") || strings.Contains(v, "&amp;") || strings.Contains(v, "&lt;") {
				t.Errorf("%s: %q contains an HTML entity; it would render literally", tag, key)
			}
		}
	}

	// The exemption itself must stay a shell command, or it becomes a hole.
	for tag, cat := range ref {
		if v, ok := cat[shellCommandKey]; ok && !strings.Contains(v, "curl") {
			t.Errorf("%s: %q no longer looks like the curl|bash one-liner, so its angle-bracket exemption is stale: %q", tag, shellCommandKey, v)
		}
	}
}

// TestCatalogueJSONIsStableShape stops a malformed value from being silently
// accepted: every catalogue must decode as a flat string->string object. A
// nested object would unmarshal into a string map only by luck.
func TestCatalogueJSONIsStableShape(t *testing.T) {
	all, err := loadAll()
	if err != nil {
		t.Fatalf("catalogues are unreadable: %v", err)
	}
	for tag := range all {
		raw, err := localeFS.ReadFile(catalogueDir + "/" + tag + ".json")
		if err != nil {
			t.Fatalf("read %s: %v", tag, err)
		}
		var probe map[string]any
		if err := json.Unmarshal(raw, &probe); err != nil {
			t.Fatalf("%s.json is not valid JSON: %v", tag, err)
		}
		for k, v := range probe {
			if _, ok := v.(string); !ok {
				t.Errorf("%s.json: %q is not a string (got %T); the page expects flat key -> text", tag, k, v)
			}
		}
	}
}

// ---- helpers ----

// placeholders returns the sorted {name} tokens in s.
func placeholders(s string) []string {
	var out []string
	for i := 0; i < len(s); i++ {
		if s[i] != '{' {
			continue
		}
		if j := strings.IndexByte(s[i:], '}'); j > 0 {
			out = append(out, s[i:i+j+1])
			i += j
		}
	}
	sort.Strings(out)
	return out
}

func diff(a, b []string) []string {
	inB := map[string]bool{}
	for _, s := range b {
		inB[s] = true
	}
	var out []string
	for _, s := range a {
		if !inB[s] {
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

func equalSets(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sortedKeys(m map[string]catalogue) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
