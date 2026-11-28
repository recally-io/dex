"""Run against a disposable emulator that already trusts the Dex QA fixture CA."""

import argparse
import re
import subprocess
import time
import xml.etree.ElementTree as ET
from pathlib import Path
from urllib.parse import urlsplit


def run():
    cli = argparse.ArgumentParser(description=__doc__)
    cli.add_argument("serial")
    cli.add_argument("fixture", type=Path)
    cli.add_argument("primary")
    cli.add_argument("alternate")
    args = cli.parse_args()
    if not re.fullmatch(r"emulator-[0-9]+", args.serial) or not args.fixture.name.startswith("dex-apple-qa."):
        cli.error("Use a disposable emulator and the QA fixture.")
    for origin in (args.primary, args.alternate):
        if not re.fullmatch(r"https://127\.0\.0\.1:[0-9]+", origin):
            cli.error("Use the fixture's HTTPS loopback addresses.")
    if args.primary == args.alternate:
        cli.error("Use different fixture ports.")
    token = (args.fixture / "token").read_text().strip()
    if not re.fullmatch(r"[A-Za-z0-9_-]{32,256}", token):
        cli.error("Invalid fixture token.")

    def adb(*cmd):
        proc = subprocess.run(["adb", "-s", args.serial, *cmd], capture_output=True, timeout=20)
        if proc.returncode:
            raise RuntimeError(f"adb {cmd[0]} failed")  # Input commands can contain the fixture token.
        return proc.stdout

    def get(label=None, kind=None, prefix=False, wait=18):
        deadline = time.monotonic() + wait
        while time.monotonic() < deadline:
            adb("shell", "uiautomator", "dump", "/data/local/tmp/dex-ui.xml")
            root = ET.fromstring(adb("exec-out", "cat", "/data/local/tmp/dex-ui.xml"))
            for node in root.iter("node"):
                a = node.attrib
                if a.get("package") != "io.recally.dex" or (kind and a["class"] != kind):
                    continue
                if label and not any((v.startswith(label) if prefix else v == label) for v in (a["text"], a["content-desc"])):
                    continue
                x1, y1, x2, y2 = map(int, re.findall(r"\d+", a["bounds"]))
                if x2 > x1 and y2 > y1:
                    return str((x1 + x2) // 2), str((y1 + y2) // 2)
        visible = [n.get("content-desc") or n.get("text") for n in root.iter("node") if n.get("package") == "io.recally.dex" and n.get("password") != "true"]
        print("Visible:", ", ".join(x for x in visible if x).replace(token, "[redacted]")[:1200])
        raise AssertionError(f"Did not see {label or kind}")

    def tap(label=None, **kw):
        adb("shell", "input", "tap", *get(label, **kw))

    def key(code):
        adb("shell", "input", "keyevent", code)

    def text(value):
        adb("shell", "input", "text", value)

    def connect(origin):
        tap(kind="android.widget.EditText")
        adb("shell", "input", "keycombination", "113", "29")  # Ctrl+A
        text(origin)
        key("KEYCODE_ENTER")
        get("Sign in to dex")

    def sign_in():
        tap(kind="android.widget.EditText")
        text(token)
        key("KEYCODE_BACK")
        tap("Sign in")

    def switch_workspace_from_sidebar():
        tap("Open navigation", prefix=True)
        tap("Switch workspace", prefix=True)
        get("Switch Workspace")

    def start():
        adb("shell", "am", "start", "-n", "io.recally.dex/dev.oneo.nsgo.NsgoActivity")

    ports = [f"tcp:{urlsplit(origin).port}" for origin in (args.primary, args.alternate)]
    for port in ports:
        adb("reverse", port, port)
    try:
        adb("shell", "pm", "clear", "io.recally.dex")
        start()
        connect(args.primary)
        sign_in()
        tap(kind="android.widget.EditText")
        marker = f"AndroidQA{time.time_ns()}"
        text(marker)
        tap("Send message")
        get("echo: " + marker, wait=30)
        adb("shell", "am", "force-stop", "io.recally.dex")
        start()
        get("echo: " + marker)
        switch_workspace_from_sidebar()
        connect(args.alternate)
        sign_in()
        get("echo: " + marker)
        switch_workspace_from_sidebar()
        tap("Forget server")
        get("Connect to Dex")
        connect(args.alternate)
        print("PASS: chat, restart, switch from the sidebar between same-host ports, forget and sign in again")
    finally:
        adb("shell", "pm", "clear", "io.recally.dex")
        adb("shell", "rm", "-f", "/data/local/tmp/dex-ui.xml")
        for port in ports:
            adb("reverse", "--remove", port)


if __name__ == "__main__":
    run()
