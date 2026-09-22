package main

import "testing"

// TestParseGLInetReleaseKeyValue covers the key=value line format of
// /etc/gl-inet-release (model=..., version=...).
func TestParseGLInetReleaseKeyValue(t *testing.T) {
	content := "model=GL-MT3000\nversion=4.5.16\n"
	model, version := parseGLInetRelease(content)
	if model != "GL-MT3000" {
		t.Errorf("model = %q, want %q", model, "GL-MT3000")
	}
	if version != "4.5.16" {
		t.Errorf("version = %q, want %q", version, "4.5.16")
	}
}

// TestParseGLInetReleaseBareProduct covers the single bare product string
// format (no key=value structure).
func TestParseGLInetReleaseBareProduct(t *testing.T) {
	content := "GL-MT3000\n"
	model, version := parseGLInetRelease(content)
	if model != "GL-MT3000" {
		t.Errorf("model = %q, want %q", model, "GL-MT3000")
	}
	if version != "" {
		t.Errorf("version = %q, want empty", version)
	}
}

// TestParseGLInetReleaseQuotedValues covers values wrapped in quotes, which
// the parser must strip.
func TestParseGLInetReleaseQuotedValues(t *testing.T) {
	content := "model=\"GL-MT6000\"\nversion='4.5.16'\n"
	model, version := parseGLInetRelease(content)
	if model != "GL-MT6000" {
		t.Errorf("model = %q, want %q", model, "GL-MT6000")
	}
	if version != "4.5.16" {
		t.Errorf("version = %q, want %q", version, "4.5.16")
	}
}

// TestParseGLInetReleaseEmpty covers an empty / empty-ish input.
func TestParseGLInetReleaseEmpty(t *testing.T) {
	model, version := parseGLInetRelease("")
	if model != "" || version != "" {
		t.Errorf("empty input: model=%q version=%q, want both empty", model, version)
	}
}

// TestParseGLInetReleaseModelPrecedence covers the case where both a model
// and a product line appear — the last matching line wins (defensive).
func TestParseGLInetReleaseModelPrecedence(t *testing.T) {
	content := "product=GL-MT3000\nmodel=GL-MT6000\n"
	model, _ := parseGLInetRelease(content)
	if model != "GL-MT6000" {
		t.Errorf("model = %q, want %q (last model/product line wins)", model, "GL-MT6000")
	}
}
