package main

import (
	"strings"
	"testing"
)

// The installer UI is a single embedded page (index.html, embedded by
// embed.go). These tests pin the accessible show/hide ("reveal") contract for
// BOTH password inputs — #password (router root password) and #wifi-pass
// (upstream WiFi password) — straight against the shipped bytes.
//
// Everything is presentation-only: the field's value must never be read,
// logged, or re-rendered by the toggle. The cheapest way to guarantee that in
// a single-page UI is to pin the source, so the assertions below are text
// assertions scoped to the field they belong to.

// fieldBlock returns the `<div class="field">…` chunk that owns the given
// element id, so assertions cannot accidentally be satisfied by markup that
// lives in a different field.
func fieldBlock(t *testing.T, html, id string) string {
	t.Helper()
	var found []string
	for _, part := range strings.Split(html, `<div class="field">`) {
		if strings.Contains(part, `id="`+id+`"`) {
			found = append(found, part)
		}
	}
	if len(found) == 0 {
		t.Fatalf("index.html: no field block contains id=%q", id)
	}
	for _, part := range found {
		if strings.Contains(part, "<input") {
			return part
		}
	}
	return found[0]
}

// inputTagFor returns the isolated `<input …>` tag carrying the given id.
func inputTagFor(t *testing.T, html, id string) string {
	t.Helper()
	block := fieldBlock(t, html, id)
	i := strings.Index(block, `id="`+id+`"`)
	if i < 0 {
		t.Fatalf("index.html: field block does not contain id=%q", id)
	}
	start := strings.LastIndex(block[:i], "<input")
	end := strings.Index(block[i:], ">")
	if start < 0 || end < 0 {
		t.Fatalf("index.html: could not isolate the <input> tag for id=%q", id)
	}
	return block[start : i+end+1]
}

// funcBody returns the source of `function name(…) { … }` from the page script
// (brace-counted, so the whole body is returned and not just the first line).
func funcBody(t *testing.T, src, name string) string {
	t.Helper()
	marker := "function " + name + "("
	i := strings.Index(src, marker)
	if i < 0 {
		t.Fatalf("index.html: no `%s` function", marker)
	}
	open := strings.Index(src[i:], "{")
	if open < 0 {
		t.Fatalf("index.html: %s has no body", name)
	}
	depth := 0
	for j := i + open; j < len(src); j++ {
		switch src[j] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return src[i : j+1]
			}
		}
	}
	t.Fatalf("index.html: %s body is unterminated", name)
	return ""
}

func passwordFieldIDs() []string { return []string{"password", "wifi-pass"} }

// TestPasswordRevealToggleStartsHidden asserts each password field owns a real
// reveal control: a <button type="button"> (keyboard focusable — never a div),
// wired to the shared flip function for THAT input, exposing the aria contract
// (label + pressed state + controls) and starting in the hidden state.
func TestPasswordRevealToggleStartsHidden(t *testing.T) {
	html := string(indexHTML)
	for _, id := range passwordFieldIDs() {
		t.Run(id, func(t *testing.T) {
			block := fieldBlock(t, html, id)
			for _, want := range []string{
				`<button type="button"`,
				`id="` + id + `-toggle"`,
				`aria-label="Show password"`,
				`aria-pressed="false"`,
				`aria-controls="` + id + `"`,
				"togglePassword('" + id + "', '" + id + "-toggle')",
			} {
				if !strings.Contains(block, want) {
					t.Errorf("reveal toggle for #%s missing %s", id, want)
				}
			}
			// A div/span with onclick is not keyboard reachable.
			if strings.Contains(block, `<div id="`+id+`-toggle"`) || strings.Contains(block, `<span id="`+id+`-toggle"`) {
				t.Errorf("reveal control for #%s must be a <button>, not a div/span", id)
			}
			// Presentation only: no icon markup leaks text into the DOM.
			if strings.Contains(block, ">Show<") || strings.Contains(block, ">Hide<") {
				t.Errorf("reveal toggle for #%s must be icon-only (aria-label carries the name)", id)
			}
		})
	}
}

// TestPasswordFieldsKeepTheirWiring guards the regression that matters most:
// the router input must still POST nothing on keystroke but must still drive
// onPasswordChange() (identify/pre-stage debounce), autocomplete stays off, and
// both fields reset the reveal state when emptied.
func TestPasswordFieldsKeepTheirWiring(t *testing.T) {
	html := string(indexHTML)

	router := inputTagFor(t, html, "password")
	for _, want := range []string{
		`type="password"`,
		`autocomplete="off"`,
		`oninput="onPasswordChange();`,
		"onPasswordFieldInput('password', 'password-toggle')",
	} {
		if !strings.Contains(router, want) {
			t.Errorf("router password input missing %s\ngot: %s", want, router)
		}
	}

	wifi := inputTagFor(t, html, "wifi-pass")
	for _, want := range []string{
		`type="password"`,
		"onPasswordFieldInput('wifi-pass', 'wifi-pass-toggle')",
	} {
		if !strings.Contains(wifi, want) {
			t.Errorf("upstream WiFi password input missing %s\ngot: %s", want, wifi)
		}
	}

	// An emptied field must go back to hidden (onPasswordFieldInput → reset).
	handler := funcBody(t, html, "onPasswordFieldInput")
	for _, want := range []string{"value === ''", "resetPasswordReveal("} {
		if !strings.Contains(handler, want) {
			t.Errorf("onPasswordFieldInput must reset on empty: missing %s\n%s", want, handler)
		}
	}

	reset := funcBody(t, html, "resetPasswordReveal")
	for _, want := range []string{
		"input.type = 'password'",
		"aria-pressed",
		`'false'`,
		"Show password",
	} {
		if !strings.Contains(reset, want) {
			t.Errorf("resetPasswordReveal missing %s\n%s", want, reset)
		}
	}

	// Restarting the step must not leave a password visible.
	if !strings.Contains(funcBody(t, html, "scanAgain"), "resetPasswordReveal(") {
		t.Error("scanAgain() must reset the password reveal state when the flow restarts")
	}
}

// TestPasswordRevealToggleFlipsType pins the flip itself: both directions, one
// input at a time, aria state updated to describe the NEXT click.
func TestPasswordRevealToggleFlipsType(t *testing.T) {
	html := string(indexHTML)
	flip := funcBody(t, html, "togglePassword")

	for _, want := range []string{
		"input.type",
		"'text'",
		"'password'",
		"'aria-pressed'",
		"'aria-label'",
		"Hide password", // label after revealing says what the next click does
		"Show password",
		"setSelectionRange", // value/selection survive the type flip
	} {
		if !strings.Contains(flip, want) {
			t.Errorf("togglePassword missing %s\n%s", want, flip)
		}
	}
	// The flip targets the input it was handed — not every password field.
	if !strings.Contains(flip, "getElementById(inputId)") {
		t.Errorf("togglePassword must operate on the input it is given\n%s", flip)
	}
}

// TestPasswordValueNeverLeavesTheInput is the security contract: the reveal
// toggle is presentation-only. No console logging anywhere in the page, and the
// toggle/reset helpers must not touch innerHTML/textContent/dataset/fetch in any
// form (nothing that could carry the secret out of the input).
func TestPasswordValueNeverLeavesTheInput(t *testing.T) {
	html := string(indexHTML)

	if strings.Contains(html, "console.log(") {
		t.Error("index.html must not console.log anything (a password value could reach it)")
	}

	for _, fn := range []string{"togglePassword", "resetPasswordReveal", "onPasswordFieldInput"} {
		body := funcBody(t, html, fn)
		for _, forbidden := range []string{
			"console.",
			"fetch(",
			"innerHTML",
			"textContent",
			"dataset",
			"localStorage",
			"sessionStorage",
			"JSON.stringify",
		} {
			if strings.Contains(body, forbidden) {
				t.Errorf("%s must not use %s (the toggle only flips input.type)\n%s", fn, forbidden, body)
			}
		}
	}
}
