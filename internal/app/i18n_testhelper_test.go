package app

import (
	"strings"
	"testing"

	"github.com/OpenTollGate/tollgate-installer/internal/i18n"
)

// requireCatalogueString asserts that a key exists in the SHIPPED catalogue and
// that its English text still carries `want`.
//
// Why it exists: these guard tests used to pin exact English copy by grepping
// index.html ("Show password", "not trusted yet", ...). Once the copy moved into
// the catalogue, grepping the page only proved that a KEY NAME was written —
// which passes even when the key was renamed in en.json and the page now
// renders the raw key. The operator-facing invariant is "the wizard still TELLS
// the operator this", so the assertion has to travel to where the text lives.
//
// The `want` argument is a substring, not the whole string: translations and
// wording edits are allowed, deleting the meaning is not.
func requireCatalogueString(t *testing.T, key, want string) {
	t.Helper()
	got := i18n.T("en", key)
	if got == key {
		t.Errorf("i18n: key %q is not in the shipped catalogue (the page would render the raw key %q)", key, key)
		return
	}
	if !strings.Contains(got, want) {
		t.Errorf("i18n: %q = %q, want it to contain %q — the operator is no longer told this", key, got, want)
	}
}
