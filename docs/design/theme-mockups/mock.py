#!/usr/bin/env python3
"""Render TollGate theme mockups as SVG -> PNG via rsvg-convert.

No Chrome on this box, so the mockups are SVG rather than screenshots. That is
fine for a colour decision: the layout is identical across variants, so the only
variable under comparison is the palette.

Contrast for every pair is printed alongside, because the report ("white is hard
to read on a glossy screen") is a legibility complaint and the fix has to be
measured, not eyeballed.
"""
import subprocess
import pathlib

OUT = pathlib.Path("/tmp/theme-audit")
W, H = 760, 580

# name, page bg, card bg, body text, secondary, accent, ok-green, border
THEMES = [
    ("1-current", "#1a1f3a", "#20263f", "#e8eaf6", "#8892b0", "#FF6961", "#7ABD7E", "#2f3757"),
    ("2-dark-lifted", "#161b33", "#1e2542", "#f2f4fa", "#b6c0dc", "#ff8b84", "#9ed3a1", "#39426b"),
    ("3-light", "#f7f8fc", "#eef0f8", "#11162b", "#4a5372", "#b3261e", "#1f6b32", "#d3d8e8"),
    ("4-dark-grounded", "#0d1020", "#161a2c", "#eef1fb", "#a8b3d1", "#ff8f88", "#96d09a", "#2b3350"),
]


def svg(page, card, text, sec, accent, ok, border, title, btn_label="#11162b"):
    cx, cw = (W - 430) / 2, 430
    cy, ch = 70, 440
    rows = [("Remaining balance", "2.41 GB"), ("Session ends", "in 3h 12m"), ("Paid", "21 sats")]
    parts = [
        f'<svg xmlns="http://www.w3.org/2000/svg" width="{W}" height="{H}" viewBox="0 0 {W} {H}">',
        f'<rect width="{W}" height="{H}" fill="{page}"/>',
        f'<rect x="{cx}" y="{cy}" width="{cw}" height="{ch}" rx="16" fill="{card}" stroke="{border}"/>',
        f'<text x="{cx+28}" y="{cy+42}" fill="{text}" font-family="Helvetica,Arial,sans-serif" '
        f'font-size="23" font-weight="bold">{title}</text>',
        f'<text x="{cx+28}" y="{cy+64}" fill="{sec}" font-family="Helvetica,Arial,sans-serif" '
        f'font-size="12.5">Balance &amp; session</text>',
    ]
    y = cy + 96
    for k, v in rows:
        parts.append(
            f'<text x="{cx+28}" y="{y}" fill="{sec}" font-family="Helvetica,Arial,sans-serif" '
            f'font-size="14.5">{k}</text>'
            f'<text x="{cx+cw-28}" y="{y}" fill="{text}" text-anchor="end" '
            f'font-family="Helvetica,Arial,sans-serif" font-size="14.5" font-weight="bold">{v}</text>'
            f'<line x1="{cx+28}" y1="{y+11}" x2="{cx+cw-28}" y2="{y+11}" stroke="{border}"/>'
        )
        y += 38
    # meter
    parts.append(f'<rect x="{cx+28}" y="{y+8}" width="{cw-56}" height="7" rx="4" fill="{border}"/>')
    parts.append(f'<rect x="{cx+28}" y="{y+8}" width="{(cw-56)*0.62:.0f}" height="7" rx="4" fill="{accent}"/>')
    y += 42
    # the paragraph the report is about: the most-used secondary colour
    parts.append(
        f'<text x="{cx+28}" y="{y}" fill="{sec}" font-family="Helvetica,Arial,sans-serif" '
        f'font-size="11.5">Your session is metered by volume. Reconnect any time by</text>'
        f'<text x="{cx+28}" y="{y+17}" fill="{sec}" font-family="Helvetica,Arial,sans-serif" '
        f'font-size="11.5">opening tollgate.lan in your browser.</text>'
    )
    y += 46
    parts.append(
        f'<text x="{cx+28}" y="{y}" fill="{accent}" font-family="Helvetica,Arial,sans-serif" '
        f'font-size="12.5" font-weight="bold">Not enough credit to start a new session.</text>'
        f'<text x="{cx+28}" y="{y+20}" fill="{ok}" font-family="Helvetica,Arial,sans-serif" '
        f'font-size="12.5" font-weight="bold">Session active.</text>'
    )
    y += 44
    parts.append(
        f'<rect x="{cx+28}" y="{y}" width="{cw-56}" height="44" rx="10" fill="{accent}"/>'
        f'<text x="{cx+cw/2}" y="{y+28}" fill="{btn_label}" text-anchor="middle" '
        f'font-family="Helvetica,Arial,sans-serif" font-size="14.5" font-weight="bold">Top up</text>'
    )
    parts.append("</svg>")
    return "\n".join(parts)


def lum(h):
    h = h.lstrip("#")
    f = lambda c: c / 12.92 if c <= 0.04045 else ((c + 0.055) / 1.055) ** 2.4
    r, g, b = (int(h[i:i + 2], 16) / 255 for i in (0, 2, 4))
    return 0.2126 * f(r) + 0.7152 * f(g) + 0.0722 * f(b)


def ratio(a, b):
    la, lb = lum(a), lum(b)
    hi, lo = max(la, lb), min(la, lb)
    return (hi + 0.05) / (lo + 0.05)


if __name__ == "__main__":
    for name, page, card, text, sec, accent, ok, border in THEMES:
        btn = "#ffffff" if name == "3-light" else "#11162b"
        s = svg(page, card, text, sec, accent, ok, border, "TollGate", btn)
        sp = OUT / f"{name}.svg"
        sp.write_text(s)
        png = OUT / f"{name}.png"
        r = subprocess.run(["rsvg-convert", "-w", str(W * 2), "-o", str(png), str(sp)],
                           capture_output=True)
        status = f"{png.stat().st_size} bytes" if png.exists() else f"FAILED {r.stderr[:80]}"
        print(f"--- {name}")
        print(f"    png: {status}")
        print(f"    body text on card : {ratio(text, card):5.2f}:1")
        print(f"    secondary on card : {ratio(sec, card):5.2f}:1")
        print(f"    secondary on page : {ratio(sec, page):5.2f}:1")
        print(f"    accent on card    : {ratio(accent, card):5.2f}:1")
        print(f"    ok-green on card  : {ratio(ok, card):5.2f}:1")
