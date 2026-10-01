#!/usr/bin/env python3
"""Capture one real synthetic CLI frame as the README's terminal SVG."""

import argparse
import fcntl
import html
import os
from pathlib import Path
import pty
import re
import select
import signal
import struct
import subprocess
import termios
import time


def capture(binary):
    master, slave = pty.openpty()
    process = None
    try:
        fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", 26, 88, 0, 0))
        env = dict(os.environ, TERM="xterm-256color")
        env.pop("NO_COLOR", None)
        process = subprocess.Popen(
            [str(binary.resolve()), "--demo", "--live"],
            stdin=slave, stdout=slave, stderr=slave, env=env,
        )
        os.close(slave)
        slave = None
        output = bytearray()
        deadline = time.monotonic() + 10
        while time.monotonic() < deadline:
            if select.select([master], [], [], 0.1)[0]:
                output.extend(os.read(master, 65536))
                if b"Ctrl+C quit" in output and output.endswith(b"\x1b[J"):
                    break
        else:
            raise RuntimeError("CLI did not produce a complete demo frame")
        process.send_signal(signal.SIGINT)
        if process.wait(timeout=5) != 0:
            raise RuntimeError("CLI did not exit cleanly")
        return output.decode("utf-8")
    finally:
        if process is not None and process.poll() is None:
            process.kill()
            process.wait()
        os.close(master)
        if slave is not None:
            os.close(slave)


def xterm_color(index):
    if index >= 232:
        shade = 8 + (index - 232) * 10
        return f"#{shade:02x}{shade:02x}{shade:02x}"
    if index >= 16:
        index -= 16
        levels = [0, 95, 135, 175, 215, 255]
        return "#" + "".join(f"{levels[n]:02x}" for n in (
            index // 36, index // 6 % 6, index % 6,
        ))
    raise ValueError(f"unexpected terminal color: {index}")


def render(frame):
    lines = frame.replace("\r", "").split("\n")
    if lines[-1] == "\x1b[J":
        lines.pop()
    width, height = 784, len(lines) * 20 + 32
    svg = [
        f'<svg xmlns="http://www.w3.org/2000/svg" width="{width}" height="{height}" viewBox="0 0 {width} {height}">',
        '<title>claudex-limits live chart with synthetic demo data</title>',
        '<desc>Two quota lines show the percentage remaining over four hours. This preview uses no account data.</desc>',
        f'<rect width="{width}" height="{height}" rx="8" fill="#101418"/>',
        '<g font-family="DejaVu Sans Mono, monospace" font-size="14" xml:space="preserve">',
    ]
    color = "#d8dee9"
    for row, line in enumerate(lines):
        column = 0
        for part in re.split(r"(\x1b\[[0-9;?]*[A-Za-z])", line):
            if part.startswith("\x1b["):
                if part == "\x1b[0m":
                    color = "#d8dee9"
                elif part.startswith("\x1b[38;5;") and part.endswith("m"):
                    color = xterm_color(int(part[7:-1]))
                continue
            if part:
                x, y = 20 + column * 8.4, 30 + row * 20
                svg.append(f'<text x="{x:g}" y="{y}" fill="{color}">{html.escape(part)}</text>')
                column += len(part)
    svg.extend(["</g>", "</svg>"])
    return "\n".join(svg) + "\n"


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=Path, default=Path("./claudex-limits"))
    parser.add_argument("--output", type=Path, default=Path("docs/demo.svg"))
    args = parser.parse_args()
    preview = render(capture(args.binary))
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(preview, encoding="utf-8")
    print(f"Wrote {args.output}")
