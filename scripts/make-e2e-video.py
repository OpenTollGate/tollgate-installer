#!/usr/bin/env python3
"""Render a REAL end-to-end test run into an mp4 (terminal-style screen recording).

Not a mock-up: it runs the tests itself, captures the actual stdout, and renders
that output frame by frame. If the tests fail, the video shows the failure.

Usage:
    python3 make-e2e-video.py <out.mp4>                    # run the tests, render them
    python3 make-e2e-video.py <out.mp4> --from-log <file>  # render an existing run log

The `--from-log` form exists because the renderer is its own process environment:
a run that passes in the shell can behave differently under it (timeouts under
load, a different PATH). Rendering the log of a run whose exit code you actually
saw removes that ambiguity, and the log is kept beside the video as provenance.
"""
import os
import re
import shutil
import subprocess
import sys
import tempfile
import textwrap

REPO = os.path.expanduser("~/worktrees/installer-rc6")
GO = "/usr/local/go/bin"
TESTS = "TestWifiScanEndToEnd|TestPasswordFailureIsNamed|TestTrustButtonEndToEnd"
COLS = 96
LINES = 26


def verdict_of(output):
    """PASS only if the output claims it AND shows no failure line."""
    failed = any(l.startswith("--- FAIL") or l.strip() == "FAIL"
                 for l in output.splitlines())
    ok = any(l.startswith("ok ") or l.startswith("PASS") or "--- PASS" in l
             for l in output.splitlines())
    return (not failed) and ok

FONT_CANDIDATES = [
    "/usr/share/texmf/fonts/opentype/public/lm/lmmono10-regular.otf",
    "/usr/share/fonts/truetype/dejavu/DejaVuSansMono.ttf",
    "/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf",
]


def pick_font():
    for f in FONT_CANDIDATES:
        if os.path.exists(f):
            return f
    return None


def run_tests():
    env = dict(os.environ)
    env["PATH"] = GO + ":" + env.get("PATH", "")
    cmd = ["go", "test", "-count=1", "-v", "-run", TESTS, "."]
    p = subprocess.run(cmd, cwd=REPO, env=env, capture_output=True, text=True, timeout=1800)
    out = p.stdout + (("\n[stderr]\n" + p.stderr) if p.stderr.strip() else "")
    return out, p.returncode


def wrap(line):
    """Wrap to terminal width, preserving leading indentation."""
    if len(line) <= COLS:
        return [line]
    indent = re.match(r"\s*", line).group(0)
    return textwrap.wrap(line, COLS, subsequent_indent=indent + "    ") or [""]


def frames_from(output):
    """One screen state per new line of output (scrolling window)."""
    lines = []
    for raw in output.splitlines():
        for piece in wrap(raw.expandtabs(4)):
            lines.append(piece)
    screens = []
    for i in range(1, len(lines) + 1):
        screens.append(lines[max(0, i - LINES):i])
    return screens


def render(screens, font, outdir):
    paths = []
    for i, screen in enumerate(screens):
        body = "\n".join(screen)
        png = os.path.join(outdir, "f%04d.png" % i)
        # Inline the text: ImageMagick's policy.xml refuses the `caption:@file`
        # indirection, and passing it as one argv element sidesteps the shell too.
        cmd = ["convert", "-background", "#0b0f14", "-fill", "#d5dde5",
               "-size", "1240x660", "-gravity", "northwest",
               "-pointsize", "17", "caption:%s" % body, png]
        if font:
            cmd[1:1] = ["-font", font]
        subprocess.run(cmd, check=True)
        paths.append(png)
    return paths


def render_card(text, font, outdir, name, color="#7ee787"):
    png = os.path.join(outdir, name + ".png")
    cmd = ["convert", "-background", "#0b0f14", "-fill", color,
           "-size", "1240x660", "-gravity", "center",
           "-pointsize", "26", "caption:%s" % text, png]
    if font:
        cmd[1:1] = ["-font", font]
    subprocess.run(cmd, check=True)
    return png


def main():
    argv = sys.argv[1:]
    from_log = None
    if "--from-log" in argv:
        from_log = argv[argv.index("--from-log") + 1]
        argv = [a for a in argv if a not in ("--from-log", from_log)]
    out = argv[0]
    font = pick_font()
    print("font:", font)
    if from_log:
        print("rendering the captured run log:", from_log)
        with open(from_log) as fh:
            output = fh.read()
        rc = 0 if verdict_of(output) else 1
    else:
        print("running the real end-to-end tests ...")
        output, rc = run_tests()
        with open(out + ".log", "w") as fh:
            fh.write(output)
        print("run log:", out + ".log")
        rc = 0 if (rc == 0 and verdict_of(output)) else 1
    verdict = "ALL TESTS PASSED" if rc == 0 else "TESTS FAILED"
    headline = ('INSTALLER E2E — real `go test -v` output\n'
                'wifi-scan + trust, against a dropbear-like router fixture\n'
                'that REQUIRES a root password\n\n'
                '$ go test -count=1 -v .\n')
    tail = ("\n%s (exit %d)\n" % (verdict, rc))
    screens = [[l] for l in headline.splitlines()] + frames_from(output) \
        + [[l] for l in tail.splitlines()]
    # a short hold on the final screen
    for _ in range(6):
        screens.append(screens[-1])

    work = tempfile.mkdtemp(prefix="e2e-video-")
    pngs = render(screens, font, work)
    title = render_card(headline, font, work, "title")
    end = render_card("REAL END-TO-END RESULT\n\n" + verdict + "\n\nexit code %d" % rc,
                      font, work, "end", "#7ee787" if rc == 0 else "#ff7b72")

    listfile = os.path.join(work, "list.txt")
    with open(listfile, "w") as fh:
        for _ in range(3):
            fh.write("file '%s'\nduration 1.2\n" % title)
        for p in pngs:
            fh.write("file '%s'\nduration 0.85\n" % p)
        for _ in range(6):
            fh.write("file '%s'\nduration 0.6\n" % end)
        fh.write("file '%s'\n" % end)

    subprocess.run([
        "ffmpeg", "-y", "-loglevel", "error",
        "-f", "concat", "-safe", "0", "-i", listfile,
        "-vf", "scale=1280:720:force_original_aspect_ratio=decrease,"
               "pad=1280:720:(ow-iw)/2:(oh-ih)/2:color=#0b0f14,format=yuv420p",
        "-r", "30", "-c:v", "libx264", "-preset", "medium", out,
    ], check=True)
    shutil.rmtree(work, ignore_errors=True)
    size = os.path.getsize(out)
    print("wrote %s (%.1f MB) verdict=%s" % (out, size / 1e6, verdict))
    return 0 if rc == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
