#!/usr/bin/env python3
"""compress-windows — time-compress named segments of a silent recording and
remap its narration timeline so voice-over stays aligned.

Usage:
  compress-windows.py <in.webm> <narration.json> <out.webm> <out.narration.json> <factor> <w0_start:w0_end[,w1_start:w1_end...]>

Windows are milliseconds in the SOURCE timeline. Each is compressed by <factor>
(e.g. 8), which is what "fast-forward the install at 8x" means: the frames are
real, just played faster, and every narration line after a window shifts left by
the time the window saved.
"""
import json
import subprocess
import sys


def parse_windows(spec, dur_ms):
    wins = []
    for part in spec.split(","):
        part = part.strip()
        if not part:
            continue
        a, b = part.split(":")
        a, b = int(a), int(b)
        wins.append((max(0, a), min(b, dur_ms)))
    wins.sort()
    # drop overlaps/empties
    out = []
    for w in wins:
        if w[1] <= w[0]:
            continue
        if out and w[0] < out[-1][1]:
            continue
        out.append(w)
    return out


def remap(t_ms, wins, factor):
    """Map a source-time to compressed-time: each window past t loses
    (covered_ms)*(1 - 1/factor)."""
    saved = 0.0
    for a, b in wins:
        if t_ms <= a:
            break
        covered = min(t_ms, b) - a
        if covered > 0:
            saved += covered * (1.0 - 1.0 / factor)
    return t_ms - saved


def main():
    in_v, in_json, out_v, out_json, factor, spec = sys.argv[1:7]
    factor = float(factor)

    dur_ms = int(float(subprocess.check_output(
        ["ffprobe", "-v", "error", "-show_entries", "format=duration",
         "-of", "csv=p=0", in_v]).strip()) * 1000)
    wins = parse_windows(spec, dur_ms)
    print("windows:", wins, "factor:", factor, "dur_ms:", dur_ms)

    # Build the filtergraph: for every span between windows keep it; for each
    # window speed it up by factor. Segments: [0,w0a] normal [w0a,w0b]/f
    # [w0b,w1a] normal [w1a,w1b]/f ... [wN b, end] normal.
    segs = []  # (start, end, compress?)
    cur = 0
    for a, b in wins:
        if a > cur:
            segs.append((cur, a, False))
        segs.append((a, b, True))
        cur = b
    if cur < dur_ms:
        segs.append((cur, dur_ms, False))

    parts = []
    for i, (a, b, comp) in enumerate(segs):
        if comp:
            parts.append(
                f"[0:v]trim=start={a/1000:.3f}:end={b/1000:.3f},"
                f"setpts=(PTS-STARTPTS)/{factor}[v{i}]")
        else:
            parts.append(
                f"[0:v]trim=start={a/1000:.3f}:end={b/1000:.3f},"
                f"setpts=PTS-STARTPTS[v{i}]")
    concat_in = "".join(f"[v{i}]" for i in range(len(segs)))
    graph = ";".join(parts) + f";{concat_in}concat=n={len(segs)}:v=1:a=0[out]"

    cmd = ["ffmpeg", "-y", "-v", "error", "-i", in_v,
           "-filter_complex", graph, "-map", "[out]",
           "-c:v", "libvpx", "-deadline", "realtime", "-cpu-used", "8",
           "-b:v", "2M", "-an", out_v]
    subprocess.run(cmd, check=True)

    tl = json.load(open(in_json))
    for e in tl["entries"]:
        e["start"] = round(remap(e["start"], wins, factor))
        e["end"] = round(remap(e["end"], wins, factor))
    json.dump(tl, open(out_json, "w"), indent=1)
    new_dur = int(float(subprocess.check_output(
        ["ffprobe", "-v", "error", "-show_entries", "format=duration",
         "-of", "csv=p=0", out_v]).strip()) * 1000)
    print(f"compressed {dur_ms}ms -> {new_dur}ms; wrote {out_v} + {out_json}")


if __name__ == "__main__":
    main()
