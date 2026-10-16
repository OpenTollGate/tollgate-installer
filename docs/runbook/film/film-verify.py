#!/usr/bin/env python3
"""film-verify — CONTENT validation for the runbook films.

film-check.py only proves URLs/captions/overlap. That is why a film could show
"Failed to fetch invoice status #LN004", a black stretch, or a placeholder
email and still "pass". This validator looks at the actual pixels:

  * blackdetect — fails on any black segment >= BLACK_MIN (a blank frame reads
    as a broken film).
  * OCR (tesseract) — fails when a FORBIDDEN string appears (an error state)
    or when a REQUIRED string is missing (the happy-path evidence).

Run it BEFORE publishing; it is the gate that would have caught the broken
tail film. Requires ffmpeg + tesseract on PATH.

Usage:
  film-verify.py <film.mp4> [<film.mp4> ...]
Exit 0 = pass, 1 = fail.
"""
import json
import os
import subprocess
import sys

BLACK_MIN = float(os.environ.get("BLACK_MIN", "0.5"))
# Sample this many evenly-spaced frames for OCR.
SAMPLES = int(os.environ.get("OCR_SAMPLES", "12"))

# Strings that must NEVER appear (an error/placeholder state on camera).
# NOTE: `you@wallet.app` is deliberately NOT here — it is the wizard's input
# PLACEHOLDER, shown before the operator types; the REQUIRED check below proves
# the real `tollgate@coinos.io` was actually entered.
FORBIDDEN = [
    "LN004",              # invoice status failure (Lightning path)
    "CU107",              # no captive-portal session
    "Setup failed",
    "Connection refused",
    "no captive-portal",
    "unrecognized option",
]
# Strings that MUST appear, keyed by a substring of the film filename.
REQUIRED = {
    "i2-wizard-live": ["tollgate@coinos.io"],
    "portal-pay":     ["Payment successful"],
    "configui":       ["Wallet"],
}


def dur(path):
    return float(subprocess.check_output(
        ["ffprobe", "-v", "error", "-show_entries", "format=duration",
         "-of", "csv=p=0", path]).decode().strip())


def black_segments(path):
    out = subprocess.run(
        ["ffmpeg", "-i", path, "-vf",
         f"blackdetect=d={BLACK_MIN}:pic_th=0.98", "-an", "-f", "null", "-"],
        capture_output=True, text=True).stderr
    segs = []
    for line in out.splitlines():
        if "blackdetect" in line and "black_start" in line:
            import re
            m = re.search(r"black_start:([\d.]+).*black_end:([\d.]+)", line)
            if m:
                segs.append((float(m.group(1)), float(m.group(2))))
    return segs


def ocr_frame(path, t, crop=None):
    cmd = ["ffmpeg", "-v", "error", "-ss", str(t), "-i", path, "-frames:v", "1"]
    if crop:
        # crop=w:h:x:y then upscale — small UI text OCRs far better magnified.
        w, h, x, y = crop
        cmd += ["-vf", f"crop={w}:{h}:{x}:{y},scale={w*2}:{h*2}"]
    cmd += ["-f", "image2pipe", "-vcodec", "png", "-"]
    png = subprocess.run(cmd, capture_output=True).stdout
    return subprocess.run(["tesseract", "stdin", "stdout"],
                          input=png, capture_output=True).stdout.decode(errors="replace")


def scan_text(path, d, frames, crop=None):
    return " ".join(ocr_frame(path, t, crop) for t in frames)


def main():
    films = sys.argv[1:]
    if not films:
        print("usage: film-verify.py <film.mp4> ...", file=sys.stderr)
        return 2
    fails = 0
    for f in films:
        name = os.path.splitext(os.path.basename(f))[0]
        d = dur(f)
        print(f"== {name} ({d:.1f}s) ==")
        # black
        segs = black_segments(f)
        if segs:
            print(f"  FAIL black segments: " + ", ".join(f"{a:.1f}-{b:.1f}s" for a, b in segs))
            fails += 1
        else:
            print("  OK   no black segment")
        # OCR across the film (full frame) for forbidden strings
        text = []
        for i in range(SAMPLES):
            t = max(0.5, d * (i + 0.5) / SAMPLES)
            text.append(ocr_frame(f, t))
        joined = " ".join(text)
        for bad in FORBIDDEN:
            if bad.lower() in joined.lower():
                print(f"  FAIL forbidden string on screen: {bad!r}")
                fails += 1
        # Required strings: scan DENSELY on the lower content area, where the
        # wizard form / portal controls live (small text needs the crop).
        need = None
        for key, v in REQUIRED.items():
            if key in name:
                need = v
        if need:
            dense = [d * (i + 0.5) / 40 for i in range(40)]
            lower = scan_text(f, d, dense, crop=(1000, 560, 0, 150))
            full = scan_text(f, d, dense)
            hay = (lower + " " + full).lower()
            for s in need:
                if s.lower() in hay:
                    print(f"  OK   required string present: {s!r}")
                else:
                    print(f"  FAIL required string MISSING: {s!r}")
                    fails += 1
    print(f"\n{'PASS' if fails == 0 else 'FAIL'}: {fails} problem(s)")
    return 0 if fails == 0 else 1


if __name__ == "__main__":
    raise SystemExit(main())
