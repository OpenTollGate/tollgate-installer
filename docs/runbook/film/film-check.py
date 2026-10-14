#!/usr/bin/env python3
"""film-check — validate the Round-2 films: artifacts exist, URLs resolve (HTTP),
captions present, durations sane, and narration lines never overlap."""
import json, os, subprocess, sys, urllib.request

D = "/tmp/opencode/filmcap"
URLS = {
    "i1-manual-full2": "fa844a207ee826cdffce1a76b0b599a5cbe6fe28705e3c669c2a2be854f466b5",
    "i2-wizard-full2": "cd97f49c315eb3f0c050d98767399c3f41b42541b2c083c0c09951bc2786cebb",
    "tail-payment-admin2": "5c2856066f3e2f6003bb3fcbca72725a5f751fe5851af7808dd5f59690b31f8a",
    "i1-flash-manual": "4d4c687bde6537b33fcfc1e5993927d49d340b0b88c0e975e86cf343000815de",
    "i2-wizard": "02267c51235d9ab8eaf899ecf778d987c8e31693319717ba21645fdef022acce",
    "rc-fixes": "9b44dbc1d9ddb0bd98a4a32460105148ed4c6e096efd0a861b3562dc16d07bfc",
}
VTT = {
    "i1-manual-full2": "1afec0cfee18915d4b5bcd68d3ffb5cc3edf8b911974750e19093cb217668cd6",
    "i2-wizard-full2": "1ee12203c639fef974066a04d72c68902155df50a1069c2d41de889f580e5bc4",
    "tail-payment-admin2": "0fef5e40ef971b1d1d07282e9c8d9a54b3d344a6ca5d57c13e5c0da4a3c060a1",
}
BASE = "https://blossom2.orangesync.tech"
fails = 0

def head(url):
    try:
        req = urllib.request.Request(url, method="HEAD")
        with urllib.request.urlopen(req, timeout=30) as r:
            return r.status, r.headers.get("Content-Length")
    except Exception as e:
        return 0, str(e)[:60]

def dur(p):
    try:
        return float(subprocess.check_output(["ffprobe", "-v", "error", "-show_entries",
                    "format=duration", "-of", "csv=p=0", p]).decode().strip())
    except Exception:
        return None

print("== artifact URLs ==")
for name, sha in URLS.items():
    st, cl = head(f"{BASE}/{sha}.mp4")
    ok = st == 200
    local = os.path.exists(f"{D}/{name}.mp4")
    print(f"  {'OK ' if ok else 'FAIL'} {name}.mp4  http={st} bytes={cl}  local={local}")
    fails += 0 if ok else 1
for name, sha in VTT.items():
    st, cl = head(f"{BASE}/{sha}.vtt")
    ok = st == 200
    print(f"  {'OK ' if ok else 'FAIL'} {name}.vtt  http={st}")
    fails += 0 if ok else 1

print("== captions cover the film (last cue end <= duration) ==")
def ts2ms(t):
    h, m, rest = t.split(":"); s, ms = rest.split(".")
    return ((int(h) * 60 + int(m)) * 60 + int(s)) * 1000 + int(ms)
for name in ("i1-manual-full2", "i2-wizard-full2", "tail-payment-admin2"):
    vtt = f"{D}/{name}.vtt"; mp4 = f"{D}/{name}.mp4"
    if not (os.path.exists(vtt) and os.path.exists(mp4)):
        print(f"  SKIP {name} (missing artifact)"); continue
    last = 0
    for line in open(vtt):
        if "-->" in line:
            last = max(last, ts2ms(line.split("-->")[1].strip()))
    d = dur(mp4) or 0
    ok = last <= d * 1000 + 500 and last > d * 1000 * 0.6
    print(f"  {'OK ' if ok else 'FAIL'} {name}: last cue {last/1000:.1f}s, film {d:.1f}s")
    fails += 0 if ok else 1

print("== narration lines never overlap ==")
for name in ("i1-flash-manual", "i2-wizard-gui", "i2-headless", "tail-payment", "tail-admin2", "rc-fixes"):
    p = f"{D}/{name}.narration.json"
    if not os.path.exists(p):
        print(f"  SKIP {name}"); continue
    ent = json.load(open(p))["entries"]
    bad = 0
    for a, b in zip(ent, ent[1:]):
        if a.get("end", 0) > b["start"]:
            bad += 1
    print(f"  {'OK ' if bad == 0 else 'FAIL'} {name}: {len(ent)} lines, {bad} overlaps")
    fails += 0 if bad == 0 else 1

print(f"\n{'PASS' if fails == 0 else 'FAIL'}: {fails} problem(s)")
sys.exit(0 if fails == 0 else 1)
