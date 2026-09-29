#!/usr/bin/env python3
"""Drive a VM's serial console from scripts.

QEMU's serial socket takes a single client, so one long-lived broker owns it:
everything the VM prints is appended to a log file, and anything written to a
FIFO is typed into the VM. `expect` and `send` then work on those two files,
which makes them safe to call repeatedly over ssh. `expect` also works on any
plain log file, such as the one QEMU writes for `-serial file:` in CI.

  serial.py broker SOCKET LOG FIFO
  serial.py expect LOG REGEX [TIMEOUT]    wait for REGEX after the last match;
                                          prints group 1 (or the whole match),
                                          or every group, space-separated, when
                                          REGEX has more than one
  serial.py mark LOG                      skip everything logged so far
  serial.py send FIFO TEXT                type TEXT followed by Enter
"""
import os, re, select, socket, sys, time


def broker(sock_path, log_path, fifo_path):
    if not os.path.exists(fifo_path):
        os.mkfifo(fifo_path)
    # O_RDWR keeps the FIFO open even when no writer is attached.
    fifo = os.open(fifo_path, os.O_RDWR | os.O_NONBLOCK)
    while True:
        try:
            s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
            s.connect(sock_path)
            break
        except OSError:
            time.sleep(0.2)
    with open(log_path, "ab", buffering=0) as log:
        while True:
            ready, _, _ = select.select([s, fifo], [], [])
            if s in ready:
                data = s.recv(65536)
                if not data:
                    return  # VM went away
                log.write(data)
            if fifo in ready:
                try:
                    data = os.read(fifo, 65536)
                except BlockingIOError:
                    continue
                if data:
                    s.sendall(data)


def render(m):
    """What `expect` prints for a match: the one group, all groups, or the match."""
    if m.re.groups == 0:
        return m.group(0).decode(errors="replace")
    if m.re.groups == 1:
        return (m.group(1) or b"").decode(errors="replace")
    return " ".join((g or b"").decode(errors="replace") for g in m.groups())


def expect(log_path, pattern, timeout=300):
    pos_path = log_path + ".pos"
    try:
        with open(pos_path) as f:
            pos = int(f.read())
    except (OSError, ValueError):
        pos = 0
    rx = re.compile(pattern.encode(), re.M)
    deadline = time.time() + float(timeout)
    while True:
        try:
            with open(log_path, "rb") as f:
                f.seek(pos)
                buf = f.read()
        except OSError:
            buf = b""
        m = rx.search(buf)
        if m:
            with open(pos_path, "w") as f:
                f.write(str(pos + m.end()))
            print(render(m))
            return 0
        if time.time() >= deadline:
            break
        time.sleep(0.5)
    sys.stderr.write(f"timeout after {timeout}s waiting for /{pattern}/\n")
    return 1


def mark(log_path):
    try:
        size = os.path.getsize(log_path)
    except OSError:
        size = 0
    with open(log_path + ".pos", "w") as f:
        f.write(str(size))
    return 0


def send(fifo_path, text):
    with open(fifo_path, "wb", buffering=0) as f:
        f.write(text.encode() + b"\r")
    return 0


if __name__ == "__main__":
    cmd, *args = sys.argv[1:] or [""]
    if cmd == "broker":
        broker(*args)
    elif cmd == "expect":
        sys.exit(expect(*args))
    elif cmd == "mark":
        sys.exit(mark(*args))
    elif cmd == "send":
        sys.exit(send(*args))
    else:
        sys.exit(__doc__)
