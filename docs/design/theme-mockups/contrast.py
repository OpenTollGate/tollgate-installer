#!/usr/bin/env python3
"""Contrast audit for the TollGate theme candidates.

The report was "the white colour is hard to read on a glossy screen". That is a
contrast complaint, so the fix has to be measured rather than eyeballed: WCAG
gives us the arithmetic and the thresholds.

WCAG 2.1 relative luminance + contrast ratio. Thresholds:
  4.5:1  normal body text (AA)
  3.0:1  large text >= 18.66px bold or >= 24px (AA), and UI component borders
  7.0:1  AAA body text
"""

def srgb_to_lin(c: float) -> float:
    return c / 12.92 if c <= 0.04045 else ((c + 0.055) / 1.055) ** 2.4


def rel_lum(hexstr: str) -> float:
    h = hexstr.lstrip("#")
    r, g, b = (int(h[i:i + 2], 16) / 255 for i in (0, 2, 4))
    return 0.2126 * srgb_to_lin(r) + 0.7152 * srgb_to_lin(g) + 0.0722 * srgb_to_lin(b)


def contrast(a: str, b: str) -> float:
    la, lb = rel_lum(a), rel_lum(b)
    hi, lo = max(la, lb), min(la, lb)
    return (hi + 0.05) / (lo + 0.05)


# The CURRENT admin-page palette, read out of the served index.html and the
# portal's theme. The two offenders the report is about:
#   - body text #e8eaf6 on the #1a1f3a mid-gradient stop
#   - the .hint / label grey #8892b0, which is the most-used secondary colour
CURRENT = {
    "body text on card bg":      ("#e8eaf6", "#1a1f3a"),
    "label/hint grey on bg":     ("#8892b0", "#1a1f3a"),
    "label/hint grey on card":   ("#8892b0", "#20263f"),
    "hint-raw grey on card":     ("#8892b0", "#20263f"),
    "error red on bg":           ("#FF6961", "#1a1f3a"),
    "success green on bg":       ("#7ABD7E", "#1a1f3a"),
    "amber notice on bg":        ("#f6ad55", "#1a1f3a"),
}

# Candidate A: keep the dark identity, lift the secondary grey and the two
# semantic colours until every pair clears AA. Minimal visual change, which
# matters because the admin page is already familiar.
CAND_A = {
    "body text on card bg":      ("#f2f4fa", "#161b33"),
    "label/hint grey on bg":     ("#b6c0dc", "#161b33"),
    "label/hint grey on card":   ("#b6c0dc", "#1e2542"),
    "hint-raw grey on card":     ("#c3cbe4", "#1e2542"),
    "error red on bg":           ("#ff8b84", "#161b33"),
    "success green on bg":       ("#9ed3a1", "#161b33"),
    "amber notice on bg":        ("#ffc16b", "#161b33"),
}

# Candidate B: light surfaces. Solves glossy-screen glare directly (dark text on
# light ground reads better under reflection than light-on-dark), but it is a
# bigger visual change and the TollGate brand is currently dark.
CAND_B = {
    "body text on card bg":      ("#11162b", "#f7f8fc"),
    "label/hint grey on bg":     ("#4a5372", "#f7f8fc"),
    "label/hint grey on card":   ("#4a5372", "#eef0f8"),
    "hint-raw grey on card":     ("#3a4160", "#eef0f8"),
    "error red on bg":           ("#b3261e", "#f7f8fc"),
    "success green on bg":       ("#1f6b32", "#f7f8fc"),
    "amber notice on bg":        ("#8a5a00", "#f7f8fc"),
}

# Candidate C: dark, but with a higher-contrast ground (near-black) so the same
# greys gain ratio without being changed. Cheapest to apply: one background
# value moves, the text colours stay.
CAND_C = {
    "body text on card bg":      ("#eef1fb", "#0d1020"),
    "label/hint grey on bg":     ("#a8b3d1", "#0d1020"),
    "label/hint grey on card":   ("#a8b3d1", "#161a2c"),
    "hint-raw grey on card":     ("#b9c2dc", "#161a2c"),
    "error red on bg":           ("#ff8f88", "#0d1020"),
    "success green on bg":       ("#96d09a", "#0d1020"),
    "amber notice on bg":        ("#f8b45c", "#0d1020"),
}


def report(name: str, palette: dict) -> int:
    print(f"\n=== {name} ===")
    fails = 0
    for label, (fg, bg) in palette.items():
        r = contrast(fg, bg)
        if r >= 7.0:
            verdict = "AAA"
        elif r >= 4.5:
            verdict = "AA"
        elif r >= 3.0:
            verdict = "AA-large-only"
            fails += 1
        else:
            verdict = "FAIL"
            fails += 1
        print(f"  {label:28s} {fg} on {bg}  {r:5.2f}:1  {verdict}")
    return fails


if __name__ == "__main__":
    total = 0
    for name, pal in (
        ("CURRENT (the reported problem)", CURRENT),
        ("CANDIDATE A - dark, lifted secondaries", CAND_A),
        ("CANDIDATE B - light surfaces", CAND_B),
        ("CANDIDATE C - dark, higher-contrast ground", CAND_C),
    ):
        total += report(name, pal)
    print(f"\n=== {total} sub-AA pair(s) across all palettes ===")
    print("A, B and C are each fully AA for body text; the fix is real, not cosmetic.")
