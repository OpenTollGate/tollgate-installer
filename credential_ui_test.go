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
