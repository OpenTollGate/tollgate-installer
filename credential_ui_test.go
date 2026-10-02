package main

// UI half of finding 1 on OpenTollGate/tollgate-installer#46: the credential
// the deploy had to create must be SURFACED, once, in the success view. The
// UI is a single embedded page (index.html), so — like password_reveal_test.go
// — the contract is pinned against the shipped bytes and against the body of
// the shipped functions.

import (
	"strings"
	"testing"
)

// successViewBlock returns the success-view markup so assertions cannot be
// satisfied by markup living in another view.
func successViewBlock(t *testing.T, html string) string {
	t.Helper()
	start := strings.Index(html, `id="success-view"`)
	if start < 0 {
		t.Fatal("index.html: no success view")
	}
	rest := html[start:]
	end := strings.Index(rest, `id="error-view"`)
	if end < 0 {
		t.Fatal("index.html: could not delimit the success view (no error view after it)")
	}
	return rest[:end]
}

// TestSuccessViewSurfacesTheGeneratedCredentialOnce pins where the one-time
// root password is rendered: the success view, with a copy affordance and an
// explicit "shown once — store it now" warning.
func TestSuccessViewSurfacesTheGeneratedCredentialOnce(t *testing.T) {
	view := successViewBlock(t, string(indexHTML))

	for _, want := range []string{
		// Hidden until the one-shot value actually arrives.
		`<div class="credential hidden" id="generated-credential"`,
		`role="alert"`,
		// The value itself, and the copy affordance next to it.
		`id="generated-password"`,
		`<button type="button"`,
		`id="copy-generated-password"`,
		`onclick="copyGeneratedPassword()"`,
		`aria-label="Copy root password to clipboard"`,
		// The warning: an explicit "this is shown once" the operator cannot
		// miss, plus where to put it.
		`shown ONCE`,
		`Store it in your password manager`,
		`before you close`,
	} {
		if !strings.Contains(view, want) {
			t.Errorf("success view is missing %s\n--- success view ---\n%s", want, view)
		}
	}

	// A password sitting in the DOM forever is not "shown once": the copy
	// control must be a real button with a status line, and the credential
	// must never be persisted by the page.
	if html := string(indexHTML); strings.Contains(html, "localStorage") || strings.Contains(html, "sessionStorage") {
		t.Error("index.html persists to browser storage; the one-time credential must not survive in localStorage/sessionStorage")
	}
}

// errorViewBlock returns the failure-view markup (from the error-view container
// up to the page script) so failure-view assertions cannot be satisfied by markup
// living in the success view.
func errorViewBlock(t *testing.T, html string) string {
	t.Helper()
	start := strings.Index(html, `id="error-view"`)
	if start < 0 {
		t.Fatal("index.html: no error view")
	}
	rest := html[start:]
	end := strings.Index(rest, "<script>")
	if end < 0 {
		t.Fatal("index.html: could not delimit the error view (no script tag after it)")
	}
	return rest[:end]
}

// TestFailureViewSurfacesTheGeneratedCredentialOnce is the UI half of the
// failure-path lockout fix (#46 follow-up, finding 2). A deploy can fail AFTER
// step 4 already generated a root password for the router and set it; the
// success view is never reached then, so the credential must be rendered on the
// FAILURE view — with the same one-shot, copy-affordance, "store it now"
// contract the success view's block has.
func TestFailureViewSurfacesTheGeneratedCredentialOnce(t *testing.T) {
	view := errorViewBlock(t, string(indexHTML))

	for _, want := range []string{
		// Hidden until the one-shot value actually arrives.
		`<div class="credential hidden" id="failed-generated-credential" role="alert">`,
		// The value itself, and the copy affordance next to it.
		`id="failed-generated-password"`,
		`<button type="button"`,
		`id="copy-failed-generated-password"`,
		`onclick="copyFailedGeneratedPassword()"`,
		`aria-label="Copy root password to clipboard"`,
		// The warning: an explicit "this is shown once" the operator cannot
		// miss, plus where to put it.
		`shown ONCE`,
		`Store it in your password manager`,
		`before you close`,
	} {
		if !strings.Contains(view, want) {
			t.Errorf("failure view is missing %s\n--- failure view ---\n%s", want, view)
		}
	}

	// The two blocks must be separate DOM nodes with distinct ids: the failure
	// view's value node is not the success view's, so neither view depends on
	// the other's markup (and the ids stay unique in the document).
	html := string(indexHTML)
	if got := strings.Count(html, `id="generated-password"`); got != 1 {
		t.Errorf("index.html declares the success-view value node %d times, want exactly 1", got)
	}
	if got := strings.Count(html, `id="failed-generated-password"`); got != 1 {
		t.Errorf("index.html declares the failure-view value node %d times, want exactly 1", got)
	}
	if got := strings.Count(html, `id="failed-generated-credential"`); got != 1 {
		t.Errorf("index.html declares the failure-view credential block %d times, want exactly 1", got)
	}
}

// TestPollStatusPinsTheCredentialOnFailure pins the wiring: pollStatus must read
// job.generated_password (the snake-case JSON tag) in the FAILED branch too, and
// must pin it BEFORE the failure view is revealed — the server serves it on the
// first read of the failed job and never again. The log tail must stay out of it.
func TestPollStatusPinsTheCredentialOnFailure(t *testing.T) {
	html := string(indexHTML)
	poll := funcBody(t, html, "pollStatus")

	failStart := strings.Index(poll, "job.status === 'failed'")
	if failStart < 0 {
		t.Fatalf("pollStatus has no failed branch\n%s", poll)
	}
	failBranch := poll[failStart:]
	if !strings.Contains(failBranch, "pinFailedGeneratedCredential(job.generated_password)") {
		t.Errorf("the failed branch does not pin the credential the server surrenders on the failed read — an operator whose router got a new root password from a deploy that then failed is locked out\n--- failed branch ---\n%s", failBranch)
	}
	// The failure view is revealed through the ONE error surface (showError),
	// which every other failure path calls too. Assert the COMPOSITION rather
	// than a literal `error-view` reference: the branch must call the surface,
	// AND the surface must be what actually reveals the view — either half
	// alone leaves the operator staring at a dead screen. (The literal check
	// this replaces passed only while the branch inlined the reveal; it stayed
	// green when showError was called on a page where showError did not exist.)
	reveal := strings.Index(failBranch, "showError(")
	if reveal < 0 {
		t.Fatalf("the failed branch never reveals the failure view (no showError call)\n%s", failBranch)
	}
	if body := funcBody(t, html, "showError"); !strings.Contains(body, "getElementById('error-view')") {
		t.Fatalf("showError does not reveal the failure view, so the failed branch's call leaves the operator on a dead screen:\n%s", body)
	}
	if pin := strings.Index(failBranch, "pinFailedGeneratedCredential("); pin < 0 || pin > reveal {
		t.Errorf("pollStatus pins the credential after revealing the failure view; pin it first so the value is on screen when the view appears\n--- failed branch ---\n%s", failBranch)
	}

	// The log tail renderer must not carry the credential: the log is the wrong
	// channel (it is evicted by later steps, and the server serves the value
	// exactly once).
	logStart := strings.Index(poll, "job.log && job.log.length")
	logEnd := strings.Index(poll, "if (job.status === 'done')")
	if logStart < 0 || logEnd <= logStart {
		t.Fatalf("could not isolate the pollStatus log renderer\n%s", poll)
	}
	if logTail := poll[logStart:logEnd]; strings.Contains(logTail, "generated_password") {
		t.Errorf("the credential is rendered through the log tail; it must be surfaced once from the one-shot field, not re-rendered per poll\n%s", logTail)
	}
}

// TestFailedGeneratedCredentialHelpersAreSafe pins the failure view's two helpers
// the way TestGeneratedCredentialHelpersAreSafe pins the success view's: the value
// is written as text (never markup), nothing persists it, and the copy control
// uses the clipboard API with a visible status line.
func TestFailedGeneratedCredentialHelpersAreSafe(t *testing.T) {
	html := string(indexHTML)

	pin := funcBody(t, html, "pinFailedGeneratedCredential")
	for _, want := range []string{
		"getElementById('failed-generated-password')",
		"textContent",
		"classList.remove('hidden')",
	} {
		if !strings.Contains(pin, want) {
			t.Errorf("pinFailedGeneratedCredential missing %s\n%s", want, pin)
		}
	}
	for _, forbidden := range []string{"innerHTML", "console.", "localStorage", "sessionStorage", "fetch("} {
		if strings.Contains(pin, forbidden) {
			t.Errorf("pinFailedGeneratedCredential must not use %s\n%s", forbidden, pin)
		}
	}

	copyFn := funcBody(t, html, "copyFailedGeneratedPassword")
	for _, want := range []string{
		"navigator.clipboard",
		"writeText",
		"getElementById('failed-generated-password')",
	} {
		if !strings.Contains(copyFn, want) {
			t.Errorf("copyFailedGeneratedPassword missing %s\n%s", want, copyFn)
		}
	}
	for _, forbidden := range []string{"innerHTML", "console.", "localStorage", "sessionStorage"} {
		if strings.Contains(copyFn, forbidden) {
			t.Errorf("copyFailedGeneratedPassword must not use %s\n%s", forbidden, copyFn)
		}
	}
}

// TestPollStatusPinsTheCredentialOnDone pins the wiring: pollStatus must read
// job.generated_password (the snake-case JSON tag), and must do it in the
// `done` branch — the only branch the server ever serves the value on — before
// the success view is revealed. The log tail must never be the channel.
func TestPollStatusPinsTheCredentialOnDone(t *testing.T) {
	html := string(indexHTML)
	poll := funcBody(t, html, "pollStatus")

	if !strings.Contains(poll, "job.generated_password") {
		t.Fatalf("pollStatus never reads job.generated_password — the credential the server sends is dropped on the floor\n%s", poll)
	}
	if !strings.Contains(poll, "pinGeneratedCredential(") {
		t.Fatalf("pollStatus does not pin the credential into the UI\n%s", poll)
	}

	doneStart := strings.Index(poll, "job.status === 'done'")
	if doneStart < 0 {
		t.Fatalf("pollStatus has no done branch\n%s", poll)
	}
	doneBranch := poll[doneStart:]
	if failed := strings.Index(doneBranch, "job.status === 'failed'"); failed > 0 {
		doneBranch = doneBranch[:failed]
	}
	if !strings.Contains(doneBranch, "pinGeneratedCredential(job.generated_password)") {
		t.Errorf("the credential must be pinned in the done branch (the only branch the server serves it on), before the success view is shown\n--- done branch ---\n%s", doneBranch)
	}
	if strings.Index(poll, "pinGeneratedCredential(") > strings.Index(poll, "success-view") {
		t.Error("pollStatus pins the credential after revealing the success view; pin it first so the value is on screen when the view appears")
	}

	// The log tail renderer must not carry the credential: the log is the
	// wrong channel (it is evicted by later steps, and the server serves the
	// value exactly once).
	logStart := strings.Index(poll, "job.log && job.log.length")
	logEnd := strings.Index(poll, "if (job.status === 'done')")
	if logStart < 0 || logEnd <= logStart {
		t.Fatalf("could not isolate the pollStatus log renderer\n%s", poll)
	}
	if logTail := poll[logStart:logEnd]; strings.Contains(logTail, "generated_password") {
		t.Errorf("the credential is rendered through the log tail; it must be surfaced once from the one-shot field, not re-rendered per poll\n%s", logTail)
	}
}

// TestGeneratedCredentialHelpersAreSafe pins the two new helpers: the value is
// written as text (never markup), nothing persists it, and the copy control
// uses the clipboard API with a visible status line for both outcomes.
func TestGeneratedCredentialHelpersAreSafe(t *testing.T) {
	html := string(indexHTML)

	pin := funcBody(t, html, "pinGeneratedCredential")
	for _, want := range []string{
		"getElementById('generated-password')",
		"textContent",
		"classList.remove('hidden')",
	} {
		if !strings.Contains(pin, want) {
			t.Errorf("pinGeneratedCredential missing %s\n%s", want, pin)
		}
	}
	for _, forbidden := range []string{"innerHTML", "console.", "localStorage", "sessionStorage", "fetch("} {
		if strings.Contains(pin, forbidden) {
			t.Errorf("pinGeneratedCredential must not use %s\n%s", forbidden, pin)
		}
	}

	copyFn := funcBody(t, html, "copyGeneratedPassword")
	for _, want := range []string{
		"navigator.clipboard",
		"writeText",
		"getElementById('generated-password')",
	} {
		if !strings.Contains(copyFn, want) {
			t.Errorf("copyGeneratedPassword missing %s\n%s", want, copyFn)
		}
	}
}
