#!/usr/bin/env python3
"""sercap.py PORT CMDS.json [--login] — run commands over a serial console and
emit a clean JSON transcript: [{"cmd":..., "out":...}, ...]."""
import serial, time, re, sys, json

port = sys.argv[1]
spec = json.load(open(sys.argv[2]))
baud = int(spec.get("baud", 115200))
login_user = spec.get("login_user", "root")
s = serial.Serial(port, baud, timeout=0.2)
time.sleep(0.3)


def drain(timeout, idle=1.2):
    end = time.monotonic() + timeout
    last = time.monotonic()
    buf = ""
    while time.monotonic() < end:
        d = s.read(4096)
        if d:
            buf += d.decode("utf-8", "replace")
            last = time.monotonic()
        elif idle > 0 and time.monotonic() - last > idle:
            break
        else:
            time.sleep(0.05)
    return buf


s.write(b"\r\n")
time.sleep(0.6)
head = drain(4)
if re.search(r"login:", head):
    s.write((login_user + "\r\n").encode())
    time.sleep(1.5)
    drain(3)

out = []
for c in spec["cmds"]:
    cmd = c["cmd"] if isinstance(c, dict) else c
    wait = c.get("wait", 12) if isinstance(c, dict) else 12
    idle = c.get("idle", spec.get("idle", 1.2)) if isinstance(c, dict) else spec.get("idle", 1.2)
    s.reset_input_buffer()
    s.write((cmd + "\r\n").encode())
    s.flush()
    time.sleep(0.25)
    o = drain(wait, idle)
    out.append({"cmd": cmd, "out": o})
print(json.dumps({"port": port, "head": head, "results": out}, indent=1))
s.close()
