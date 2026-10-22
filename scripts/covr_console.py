#!/usr/bin/env python3
"""Serial access for the D-Link COVR-X1860 bench, under the shared bench mutex.

WHY THIS EXISTS
---------------
The COVR-X1860 is single-owner hardware: one UART, and several context windows
(plus workers, plus a human) can each reach it. The 2026-10-14 incident proved
what happens without a shared record of ownership — the console on
`tollgate-A04E` was handed away mid-run and every later read carried echo but no
shell, and nobody could say who had done it.

So this wrapper is the ONLY sanctioned way for this project to touch the COVR
UART. It follows the bench-mutex contract
(`worktrees/bench-mutex/docs/BENCH-MUTEX.md`): an OS advisory `flock` on a file
under `~/.local/state/tg-bench/`, with the holder's identity written into it, and
**fail-loud, never queue silently**.

Two deliberate choices:

* **Device-scoped lock id.** The bench-mutex default id is `tollgate-A04E`
  (the MT6000). Using that here would lock the WRONG board: it would collide
  with another window's legitimate MT6000 run while doing nothing to protect the
  COVR. This wrapper therefore pins `TG_BENCH_ID=tollgate-COVR-X1860`, and
  records the FTDI serial so a human can see *which* box is locked.
* **Reading is traffic.** Read-only batches take the lock too. A read that
  consumes another owner's output is the same collision as a write.

Usage:
    covr_console.py "cmd one" "cmd two"        # under the lock
    covr_console.py --status                   # who holds it (never locks)
    covr_console.py --no-lock "cmd"            # escape hatch, prints a warning
"""
from __future__ import annotations

import argparse
import fcntl
import os
import socket
import sys
import time

import serial

# The COVR-X1860's console. Pin the FTDI SERIAL, never the ttyUSB index: the
# index drifts between replugs and a wrong index wedges another board's console.
DEVICE = os.environ.get(
    "COVR_SERIAL",
    "/dev/serial/by-id/usb-FTDI_FT232R_USB_UART_BH00D6DL-if00-port0",
)
BAUD = 115200

LOCK_DIR = os.environ.get("TG_BENCH_LOCK_DIR", os.path.expanduser("~/.local/state/tg-bench"))
LOCK_ID = os.environ.get("TG_BENCH_ID", "tollgate-COVR-X1860")
LOCK_PATH = os.environ.get("TG_BENCH_LOCK", os.path.join(LOCK_DIR, f"{LOCK_ID}.lock"))


def holder_record() -> str:
    """The current holder's record, or '' when free. Never takes the lock."""
    try:
        with open(LOCK_PATH, "rb") as fh:
            return fh.read().decode("utf-8", "replace").replace("\x00", "").strip()
    except FileNotFoundError:
        return ""


class BenchLock:
    """The shared mutex: flock(LOCK_EX|LOCK_NB) + a holder record.

    The record is written after the flock is won and cleared BEFORE the flock is
    dropped, so a reader can never see a record with no live holder.
    """

    def __init__(self, task: str):
        self.task = task
        self.fd: int | None = None

    def acquire(self) -> None:
        os.makedirs(LOCK_DIR, exist_ok=True)
        self.fd = os.open(LOCK_PATH, os.O_RDWR | os.O_CREAT, 0o664)
        try:
            fcntl.flock(self.fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except OSError:
            held = holder_record() or "(no record — a holder that ignores the contract)"
            os.close(self.fd)
            self.fd = None
            sys.stderr.write(
                "BENCH LOCKED — refusing to touch the COVR-X1860 UART.\n"
                f"  lock:   {LOCK_PATH}\n"
                f"  holder: {held}\n"
                "Another context window is using this bench. Do NOT force it; wait, or\n"
                "coordinate. A stale record with no live pid is cleared by an OPERATOR\n"
                "only — never steal it silently.\n"
            )
            raise SystemExit(3)
        os.ftruncate(self.fd, 0)
        rec = (
            f"owner={os.environ.get('USER', 'unknown')}@{socket.gethostname()}\n"
            f"task={self.task}\n"
            f"pid={os.getpid()}\n"
            f"device={DEVICE}\n"
            f"started={time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime())}\n"
        )
        os.write(self.fd, rec.encode())
        os.fsync(self.fd)

    def release(self) -> None:
        if self.fd is None:
            return
        try:
            os.ftruncate(self.fd, 0)  # clear the record BEFORE dropping the lock
            fcntl.flock(self.fd, fcntl.LOCK_UN)
        finally:
            os.close(self.fd)
            self.fd = None


def drain(port: serial.Serial, quiet: float = 2.0, cap: int = 200_000) -> str:
    """Read until the line goes quiet for `quiet` seconds."""
    out = b""
    last = time.time()
    while time.time() - last < quiet and len(out) < cap:
        n = port.in_waiting
        if n:
            out += port.read(n)
            last = time.time()
        else:
            time.sleep(0.05)
    return out.decode("utf-8", "replace")


def run_commands(commands: list[str], quiet: float) -> int:
    if not os.path.exists(DEVICE):
        sys.stderr.write(f"device not present: {DEVICE}\n")
        return 2
    port = serial.Serial(DEVICE, BAUD, timeout=0.5, write_timeout=2)
    try:
        port.reset_input_buffer()
        for cmd in commands:
            port.write((cmd + "\n").encode())
            time.sleep(0.15)
            print(f"### {cmd}")
            print(drain(port, quiet))
    finally:
        port.close()
    return 0


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("commands", nargs="*", help="shell commands to run on the router")
    ap.add_argument("--status", action="store_true", help="print the holder and exit")
    ap.add_argument("--no-lock", action="store_true", help="skip the mutex (discouraged)")
    ap.add_argument("--task", default="covr-x1860 console", help="task name recorded in the lock")
    ap.add_argument("--quiet", type=float, default=2.0, help="seconds of silence that ends a read")
    args = ap.parse_args()

    if args.status:
        held = holder_record()
        print(f"lock:   {LOCK_PATH}")
        print(f"holder: {held if held else 'FREE'}")
        print(f"device: {DEVICE}")
        return 0

    if not args.commands:
        ap.error("give at least one command (or --status)")

    if args.no_lock:
        sys.stderr.write("WARNING: --no-lock bypasses the bench mutex.\n")
        return run_commands(args.commands, args.quiet)

    lock = BenchLock(args.task)
    lock.acquire()
    try:
        return run_commands(args.commands, args.quiet)
    finally:
        lock.release()


if __name__ == "__main__":
    raise SystemExit(main())
