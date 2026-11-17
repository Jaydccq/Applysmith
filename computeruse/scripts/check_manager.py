#!/usr/bin/env python3
"""Drive PersonalCU's real manager window: its headline, its switches, the pause
shortcut and the list of recent actions.

It starts its own copy of the service on a socket of its own
(PERSONAL_CU_RUNTIME_DIR), so a PersonalCU that is already running is left
alone, and finds every control by its accessibility role and title, never by a
pixel. The copy under test acts in no window but its own. Its Pause and
background switches are kept in the defaults domain of the executable, which
this check leaves as it found them.

--shortcut also presses the real Control-Option-Command-Period: that pauses
every PersonalCU that runs, so resume the others afterwards.
"""
import argparse
import json
import os
import pathlib
import shutil
import subprocess
import tempfile
import time

from check_mcp import Client

HEADLINES = ["Ready", "Paused", "Background control off", "Desktop locked", "Not running",
             "Needs Accessibility", "Needs Screen Recording", "Needs Accessibility and Screen Recording"]
SHORTCUT = """import CoreGraphics
for down in [true, false] {
    let event = CGEvent(keyboardEventSource: nil, virtualKey: 47, keyDown: down)!
    event.flags = [.maskControl, .maskAlternate, .maskCommand]
    event.post(tap: .cghidEventTap)
}"""
QUIT = "import AppKit\nexit(NSRunningApplication(processIdentifier: {pid})?.terminate() == true ? 0 : 1)"


def until(condition, seconds=10, what="the condition"):
    end = time.monotonic() + seconds
    while time.monotonic() < end:
        value = condition()
        if value:
            return value
        time.sleep(0.1)
    raise TimeoutError(f"{what} was not observed in {seconds} s")


def connect(executable, command):
    client = Client(executable, command)
    client.request("initialize", {"protocolVersion": "2025-06-18", "capabilities": {}, "clientInfo": {"name": "manager-check", "version": "1"}})
    client.send({"jsonrpc": "2.0", "method": "notifications/initialized"})
    return client


def expected_headline(doctor):
    """ServiceSummary's order, from the doctor's facts."""
    missing = [name for name, key in [("Accessibility", "accessibility"), ("Screen Recording", "screen_recording")] if not doctor[key]]
    service = doctor["service"]
    if not service["running"]:
        return "Not running"
    if missing:
        return "Needs " + " and ".join(missing)
    if doctor["desktop_session"].get("locked"):
        return "Desktop locked"
    if service["paused"]:
        return "Paused"
    return "Ready" if service["background_enabled"] else "Background control off"


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("executable", type=pathlib.Path)
    parser.add_argument("--shortcut", action="store_true", help="press the real pause shortcut too")
    options = parser.parse_args()
    executable = options.executable.resolve()
    runtime = tempfile.mkdtemp(prefix="pcu-")
    os.chmod(runtime, 0o700)
    os.environ["PERSONAL_CU_RUNTIME_DIR"] = runtime
    checks, clients, services = [], [], []

    def start():
        process = subprocess.Popen([str(executable), "service"], env={**os.environ, "PERSONAL_CU_SHOW_MANAGER": "1"},
                                   stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        services.append(process)
        until(lambda: os.path.exists(os.path.join(runtime, "runtime.sock")), what="the service's socket")
        client = connect(executable, "mcp")
        clients.append(client)
        return process, client

    def call(client, name, arguments=None):
        return client.data(client.tool(name, arguments or {}))

    try:
        # The driver is a process of its own: the service refuses its own clicks while it is paused.
        driver = connect(executable, "standalone-mcp")
        clients.append(driver)
        assert call(driver, "doctor")["accessibility"], "Grant Accessibility to the program that runs this check"
        process, service = start()

        def look():
            # By layer, not by title: a window's title is listed only for a program with Screen Recording.
            # The window may be off screen, on another Space or behind a full-screen app; it is read all the same.
            window = until(lambda: [w for w in call(driver, "list_windows", {"pid": process.pid})["windows"] if w["layer"] == 0 and w["rect"]["width"] == 520],
                           what="the manager window")[0]
            state = call(driver, "get_window_state", {"window_id": window["window_id"], "include_screenshot": False, "disable_diff": True})
            return window, state

        def texts(state):
            return [e.get("value") or e.get("title") or "" for e in state["elements"] if e["role"] == "AXStaticText"]

        def press(title):
            _, state = look()
            box = [e for e in state["elements"] if e["role"] == "AXCheckBox" and e.get("title") == title]
            assert len(box) == 1, (title, box)
            call(driver, "click", {"pid": process.pid, "snapshot_id": state["snapshot_id"], "index": box[0]["index"]})

        def doctor():
            return call(service, "doctor")

        def headline_agrees():
            shown = [t for t in texts(look()[1]) if t in HEADLINES]
            return shown == [expected_headline(doctor())] and shown[0]

        first = doctor()
        assert first["service"]["running"] and first["service"]["clients"] >= 1, first
        was_paused, was_background = first["service"]["paused"], first["service"]["background_enabled"]
        if was_paused:
            press("Pause input")
        if not was_background:
            press("Allow background control")
        until(lambda: not doctor()["service"]["paused"] and doctor()["service"]["background_enabled"], what="input on and background control on")
        before = until(headline_agrees, what="a headline that agrees with the doctor")
        checks.append(f"headline_agrees_with_doctor:{before}")

        frame = look()[0]["rect"]
        # The service's own click fills the list of recent actions: it switches background
        # control off, after which the service refuses its own input, so the driver switches it back.
        for _ in range(2):
            mine = call(service, "get_window_state", {"window_id": look()[0]["window_id"], "include_screenshot": False, "disable_diff": True})
            box = next(e for e in mine["elements"] if e["role"] == "AXCheckBox" and e.get("title") == "Allow background control")
            call(service, "click", {"pid": process.pid, "snapshot_id": mine["snapshot_id"], "index": box["index"]})
            until(lambda: not doctor()["service"]["background_enabled"], what="background control off")
            press("Allow background control")
            until(lambda: doctor()["service"]["background_enabled"], what="background control on")
        until(lambda: any("click" in t and "·" in t for t in texts(look()[1])), what="the clicks in Recent actions")
        # Within the one point per side by which a window's bounds differ with the keyboard focus.
        after = look()[0]["rect"]
        assert all(abs(after[k] - frame[k]) <= 2 for k in ("width", "height")), ("the window changed size as the list filled", frame, after)
        checks.append("recent_actions_listed_without_resizing_the_window")

        press("Pause input")
        until(lambda: doctor()["service"]["paused"], what="the pause")
        mine = call(service, "get_window_state", {"window_id": look()[0]["window_id"], "include_screenshot": False, "disable_diff": True})
        refused = service.tool("click", {"pid": process.pid, "snapshot_id": mine["snapshot_id"], "index": 0}, error=True)
        assert "paused" in refused["content"][0]["text"], refused
        until(headline_agrees, what="the paused headline")
        checks.append("pause_refuses_actions_and_reads_remain")

        subprocess.run(["swift", "-e", QUIT.format(pid=process.pid)], check=True, capture_output=True)
        process.wait(timeout=30)
        assert not os.path.exists(os.path.join(runtime, "runtime.sock")), "a quit service removes its socket"
        service.close()
        process, service = start()
        assert doctor()["service"]["paused"], "the pause is kept across a quit"
        press("Pause input")
        until(lambda: not doctor()["service"]["paused"], what="the resume")
        checks.append("pause_kept_across_quit_and_reopen")

        press("Allow background control")
        until(lambda: not doctor()["service"]["background_enabled"], what="background control off")
        mine = call(service, "get_window_state", {"window_id": look()[0]["window_id"], "include_screenshot": False, "disable_diff": True})
        refused = service.tool("click", {"pid": process.pid, "snapshot_id": mine["snapshot_id"], "index": 0}, error=True)
        assert "Background control is disabled" in refused["content"][0]["text"], refused
        until(headline_agrees, what="the background headline")
        press("Allow background control")
        until(lambda: doctor()["service"]["background_enabled"], what="background control on")
        checks.append("background_switch_refuses_background_input")

        if options.shortcut:
            subprocess.run(["swift", "-e", SHORTCUT], check=True, capture_output=True)
            until(lambda: doctor()["service"]["paused"], what="the pause from the shortcut")
            subprocess.run(["swift", "-e", SHORTCUT], check=True, capture_output=True)
            time.sleep(0.5)
            assert doctor()["service"]["paused"], "the shortcut pauses and never resumes"
            press("Pause input")
            until(lambda: not doctor()["service"]["paused"], what="the resume")
            checks.append("shortcut_pauses_and_never_resumes")

        if was_paused:
            press("Pause input")
        if not was_background:
            press("Allow background control")
        until(lambda: doctor()["service"]["paused"] == was_paused and doctor()["service"]["background_enabled"] == was_background, what="the switches as found")
        print(json.dumps({"checks": checks}, ensure_ascii=False))
    finally:
        for client in clients:
            try:
                client.close()
            except Exception:
                pass
        for process in services:
            if process.poll() is None:
                process.terminate()
                process.wait(timeout=10)
        shutil.rmtree(runtime, ignore_errors=True)


if __name__ == "__main__":
    main()
