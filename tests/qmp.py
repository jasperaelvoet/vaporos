#!/usr/bin/env python3
"""Send one QMP command to a running QEMU and print its result as JSON.

  qmp.py SOCKET COMMAND [ARGUMENTS_JSON]

  qmp.py vm.qmp screendump '{"filename": "/tmp/screen.ppm"}'
  qmp.py vm.qmp quit

QMP is QEMU's machine protocol: JSON lines, a greeting, a capabilities
handshake, then commands whose replies may be preceded by asynchronous
events. The CI VM test uses it for screendumps and to stop the VM; a real
client library would be one more thing to install on the runner.
"""
import json, socket, sys


def read(f):
    line = f.readline()
    if not line:
        raise ConnectionError("QEMU closed the QMP connection")
    return json.loads(line)


def call(f, command, arguments=None):
    msg = {"execute": command}
    if arguments is not None:
        msg["arguments"] = arguments
    f.write(json.dumps(msg).encode() + b"\n")
    f.flush()
    while True:
        reply = read(f)
        if "return" in reply:
            return reply["return"]
        if "error" in reply:
            err = reply["error"]
            raise RuntimeError(f"{command}: {err.get('desc', err)}")
        # Anything else is an event (e.g. SHUTDOWN, RESET); skip it.


def main(argv):
    if len(argv) not in (2, 3):
        sys.exit(__doc__)
    sock_path, command = argv[0], argv[1]
    arguments = json.loads(argv[2]) if len(argv) == 3 else None

    s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    s.settimeout(60)
    s.connect(sock_path)
    with s, s.makefile("rwb") as f:
        read(f)  # the greeting
        call(f, "qmp_capabilities")
        try:
            result = call(f, command, arguments)
        except ConnectionError:
            # `quit` may close the socket before its reply arrives.
            if command != "quit":
                raise
            result = {}
    print(json.dumps(result))
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main(sys.argv[1:]))
    except (OSError, ValueError, RuntimeError) as e:
        sys.exit(f"qmp: {e}")
