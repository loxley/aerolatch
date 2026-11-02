#!/usr/bin/env python3
"""Foreground-controlling-terminal regressions; uses mocked CLI test fixtures."""

import argparse
import errno
import fcntl
import os
import pty
import re
import select
import signal
import struct
import termios
import time


class Terminal:
    def __init__(self, binary, mode, cycles=1, returns=True):
        self.output = bytearray()
        self.answered = 0
        self.queries_allowed = True
        self.status = None
        self.master, slave = pty.openpty()
        fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", 32, 110, 0, 0))
        self.original = termios.tcgetattr(slave)
        self.pid = os.fork()
        if self.pid == 0:
            os.close(self.master)
            os.setsid()
            fcntl.ioctl(slave, termios.TIOCSCTTY, 0)
            for fd in (0, 1, 2):
                os.dup2(slave, fd)
            if slave > 2:
                os.close(slave)
            os.tcsetpgrp(0, os.getpgrp())
            os.execve(binary, [binary, "-test.run=^TestNearbyCLITerminalHelper$", "-test.v"], {
                "TERM": "xterm-256color",
                "AEROLATCH_TUI_TEST": mode,
                "AEROLATCH_TUI_CYCLES": str(cycles),
                "AEROLATCH_TUI_RETURN": "1" if returns else "0",
            })
        os.close(slave)

    def pump(self):
        if select.select([self.master], [], [], 0.02)[0]:
            try:
                self.output.extend(os.read(self.master, 65536))
            except OSError as error:
                if error.errno != errno.EIO:
                    raise
        queries = list(re.finditer(rb"\x1b\](10|11);\?\x1b\\", self.output))
        if len(queries) > self.answered:
            assert self.queries_allowed, "Blocking terminal colour query after UI startup"
            # Bubble Tea v1 probes once at package init, before its input reader.
            # Answer that legitimate startup query. Never answer queries from
            # Update/View: those reproduce the keyboard-reader race being fixed.
            for match in queries[self.answered:]:
                code = match.group(1)
                rgb = b"ffff/ffff/ffff" if code == b"10" else b"0000/0000/0000"
                os.write(self.master, b"\x1b]" + code + b";rgb:" + rgb + b"\x07\x1b[1;1R")
            self.answered = len(queries)
        if self.status is None:
            pid, status = os.waitpid(self.pid, os.WNOHANG)
            if pid:
                self.status = status

    def wait(self, token, start=0):
        deadline = time.monotonic() + 4
        while token not in self.output[start:]:
            self.pump()
            assert self.status is None and time.monotonic() < deadline, (
                f"Missing {token!r}: {bytes(self.output)!r}"
            )

    def send(self, keys):
        offset = len(self.output)
        os.write(self.master, keys)
        return offset

    def finish(self, keys=b"q"):
        started = time.monotonic()
        self.send(keys)
        while self.status is None and time.monotonic() - started < 2:
            self.pump()
        assert self.status == 0, f"Quit stalled or failed: {bytes(self.output)!r}"
        assert time.monotonic() - started < 1, "Quit waited on network/cooldown/rendering"
        self.pump()
        assert b"CLI_NEARBY_LIFECYCLE_OK" in self.output, bytes(self.output)
        assert termios.tcgetattr(self.master) == self.original, "TTY settings not restored"
        # One picker session, followed by one browser session, regardless of switches.
        assert self.output.count(b"\x1b[?1049h") == 2, "View switch restarted terminal"
        assert self.output.count(b"\x1b[?1049l") == 2, "Alternate screen not restored"

    def close(self):
        if self.status is None:
            try:
                os.kill(self.pid, signal.SIGKILL)
                os.waitpid(self.pid, 0)
            except ProcessLookupError:
                pass
        os.close(self.master)


def scenario(binary, name, mode, cycles=1, returns=True, quit_key=b"q"):
    terminal = Terminal(binary, mode, cycles, returns)
    try:
        terminal.wait(b"CHOOSE LOCATION")
        terminal.queries_allowed = False
        start = terminal.send(b"j\r")
        terminal.wait(b"NEARBY AIRCRAFT", start)
        identity = b"SAS1021" if mode.startswith("nearby-cli-swedavia") else b"TEST123"
        terminal.wait(identity, start)
        for _ in range(cycles):
            start = terminal.send(b"\r")
            terminal.wait(b"RADAR VIEW", start)
            terminal.wait(identity, start)  # selected identity appears immediately
            if mode == "nearby-cli-slow":
                terminal.wait(b"Fetching aircraft", start)
                terminal.wait(b"NOT CURRENT", start)
            elif mode == "nearby-cli-cooldown":
                terminal.wait(b"rate limit reached", start)
            elif mode == "nearby-cli-swedavia":
                terminal.wait(b"Airport: LANDED", start)
                terminal.wait(b"Arrival actual:", start)
                terminal.wait(b"NOT VISIBLE", start)
                terminal.wait(b"Swedavia https://swedavia.se", start)
            elif mode == "nearby-cli-swedavia-slow":
                terminal.wait(b"AIRPORT_REQUEST_PENDING", start)
                terminal.wait(b"NOT VISIBLE", start)
            elif mode == "nearby-cli-swedavia-off":
                terminal.wait(b"Swedavia: off", start)
                terminal.wait(b"NOT VISIBLE", start)
            else:
                terminal.wait(b"ARN", start)
                terminal.wait(b"LHR", start)
                terminal.wait(b"Phase: descent", start)
                terminal.wait(b"Touchdown: ~", start)
                terminal.wait(b"Arrival: AIRBORNE", start)
                terminal.wait(b"Surface: not observed", start)
            if returns:
                started = time.monotonic()
                start = terminal.send(b"\x1b")
                terminal.wait(b"NEARBY AIRCRAFT", start)
                assert time.monotonic() - started < 1, "Esc waited on network/route lookup"
                if mode != "nearby-cli-cooldown":
                    terminal.wait(identity, start)
        terminal.finish(quit_key)
        print(f"PASS foreground PTY: {name}")
    finally:
        terminal.close()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True, help="compiled internal/cli test binary")
    args = parser.parse_args()
    binary = os.path.abspath(args.binary)
    scenario(binary, "repeated nearby/detail/back, identity and reference route", "nearby-cli", cycles=3)
    scenario(binary, "Esc during stalled telemetry and route requests", "nearby-cli-slow")
    scenario(binary, "q during stalled requests", "nearby-cli-slow", returns=False)
    scenario(binary, "Ctrl+C during stalled requests", "nearby-cli-slow", returns=False, quit_key=b"\x03")
    scenario(binary, "Esc/q during Retry-After cooldown", "nearby-cli-cooldown")
    scenario(binary, "SFT/ARN airport arrival after ADS-B loss; shared board cache", "nearby-cli-swedavia", cycles=2)
    scenario(binary, "Esc during a stalled authenticated airport request", "nearby-cli-swedavia-slow")
    scenario(binary, "exported key with explicit Swedavia opt-out", "nearby-cli-swedavia-off")


if __name__ == "__main__":
    main()
