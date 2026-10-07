package app

import (
	"regexp"
	"strings"
	"testing"
)

// ui_error_surface_test.go covers a defect the Playwright UI run found that no
// string-based test in this repo could have: the page CALLS showError() on its
// failure paths -- including when the deploy request itself fails -- but the
// function was never defined anywhere.
//
// A call to an undefined name is not a syntax error, so `node --check` passes and
// every existing assertion passes. At runtime it throws a ReferenceError, which
// leaves the operator looking at a button that did nothing: the exact "nothing
// happens" experience reported repeatedly.
//
// Observed: the UI run's Act 1 cancelled the host-key confirmation and asserted
// the error view became visible -- it did not. So the check below is not
// theoretical, and it fails on the page as it was.
func TestShowErrorIsDefinedAndSurfaces(t *testing.T) {
	src := string(indexHTML)

	if !strings.Contains(src, "function showError(") {
		t.Fatalf("index.html calls showError() on its failure paths but never defines it: every one of them throws a ReferenceError and shows the operator NOTHING")
	}

	// The definition must actually reveal and populate the error view, or a
	// failure is still invisible even though the call no longer throws.
	body := jsFuncBody(t, src, "function showError(")
	for _, want := range []struct{ frag, why string }{
		{"error-view", "must reveal the error view"},
		{"error-detail", "must put the reason into the error view"},
		{"hidden", "must remove the hidden class, otherwise the view stays invisible"},
	} {
		if !strings.Contains(body, want.frag) {
			t.Errorf("showError(): %s", want.why)
		}
	}

	// At least one call site must exist, so this test cannot pass on a page that
	// silently stopped reporting failures at all.
	calls := regexp.MustCompile(`\bshowError\(`).FindAllString(src, -1)
	defs := regexp.MustCompile(`function showError\(`).FindAllString(src, -1)
	if len(calls) <= len(defs) {
		t.Fatalf("showError() is defined but never called (%d definition, %d call sites): a failure path would report nothing", len(defs), len(calls))
	}
	t.Logf("showError(): %d definition(s), %d call site(s)", len(defs), len(calls))
}
