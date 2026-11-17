#!/usr/bin/env python3
"""Exercise the real MCP process without third-party Python dependencies."""
import argparse
import base64
import json
import pathlib
import queue
import select
import subprocess
import threading
import time


def concurrent_frame_check(executable, command):
    """Force backpressure while other requests queue behind a large response."""
    process = subprocess.Popen([str(executable.resolve()), command], stdin=subprocess.PIPE,
                               stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
    def send(message):
        process.stdin.write(json.dumps(message).encode() + b"\n")
        process.stdin.flush()
    try:
        send({"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {
            "protocolVersion": "2025-06-18", "capabilities": {},
            "clientInfo": {"name": "personal-cu-framing", "version": "1"}}})
        assert select.select([process.stdout], [], [], 10)[0], "Framing client did not initialize"
        assert json.loads(process.stdout.readline())["id"] == 1
        send({"jsonrpc": "2.0", "method": "notifications/initialized"})
        send({"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": {
            "name": "js", "arguments": {"code": "'x'.repeat(6000000)", "timeout_ms": 5000}}})
        assert select.select([process.stdout], [], [], 10)[0], "Large response never reached stdout"
        time.sleep(0.05)
        for request_id in range(3, 13):
            send({"jsonrpc": "2.0", "id": request_id, "method": "ping", "params": {}})
            time.sleep(0.02)
        responses = queue.Queue()
        def read():
            for line in process.stdout:
                try:
                    message = json.loads(line)
                    if message.get("id") == 2:
                        assert not message["result"].get("isError"), message["result"]
                        data = json.loads(next(c["text"] for c in message["result"]["content"] if c["type"] == "text"))
                        assert len(data["value"]) == 6000000
                    responses.put(message.get("id"))
                except Exception as error:
                    responses.put(error)
        threading.Thread(target=read, daemon=True).start()
        received = []
        deadline = time.monotonic() + 15
        while len(received) < 11:
            item = responses.get(timeout=max(0.01, deadline - time.monotonic()))
            assert isinstance(item, int), item
            received.append(item)
        assert set(received) == set(range(2, 13)), received
    finally:
        try:
            process.stdin.close()
        except BrokenPipeError:
            pass
        try:
            process.wait(timeout=3)
        except subprocess.TimeoutExpired:
            process.terminate()
            process.wait(timeout=3)


class Client:
    def __init__(self, executable, command="standalone-mcp"):
        self.process = subprocess.Popen([str(executable), command], stdin=subprocess.PIPE,
                                        stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, bufsize=1)
        self.responses = queue.Queue()
        self.errors = []
        self.counter = 0
        def read():
            for line in self.process.stdout:
                try:
                    self.responses.put(json.loads(line))
                except ValueError:
                    self.responses.put(RuntimeError("Non-JSON text on MCP stdout"))
            self.responses.put(RuntimeError("MCP process closed its output before replying"))
        def errors():
            for line in self.process.stderr:
                self.errors.append(line)
        threading.Thread(target=read, daemon=True).start()
        threading.Thread(target=errors, daemon=True).start()

    def request(self, method, params=None):
        self.counter += 1
        self.send({"jsonrpc": "2.0", "id": self.counter, "method": method, "params": params or {}})
        deadline = time.monotonic() + 30
        while time.monotonic() < deadline:
            item = self.responses.get(timeout=max(0.01, deadline - time.monotonic()))
            if isinstance(item, Exception):
                raise item
            if item.get("id") == self.counter:
                return item
        raise TimeoutError(method)

    def send(self, value):
        self.process.stdin.write(json.dumps(value, ensure_ascii=False) + "\n")
        self.process.stdin.flush()

    def tool(self, name, arguments=None, error=False):
        reply = self.request("tools/call", {"name": name, "arguments": arguments or {}})
        assert "error" not in reply, reply
        result = reply["result"]
        assert bool(result.get("isError")) == error, (name, result)
        return result

    @staticmethod
    def data(result):
        return json.loads(next(c["text"] for c in result["content"] if c["type"] == "text"))

    def state(self, pid, mode="ax"):
        return self.tool("get_app_state", {"pid": pid, "mode": mode, "disable_diff": True})

    def close(self):
        try:
            self.process.stdin.close()
        except BrokenPipeError:
            pass
        try:
            self.process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            self.process.terminate()
            self.process.wait(timeout=3)


def strict_schema(schema):
    if schema.get("type") == "object":
        assert schema.get("additionalProperties") is False, schema
        for child in schema.get("properties", {}).values():
            strict_schema(child)
    if "items" in schema:
        strict_schema(schema["items"])


def javascript_checks(client):
    def js(code, *, timeout_ms=3000, error=False):
        return client.tool("js", {"code": code, "timeout_ms": timeout_ms}, error=error)

    first = client.data(js("const acceptanceAnswer = await Promise.resolve(41); acceptanceAnswer"))
    assert first["value"] == 41 and first["value_type"] == "number", first
    second = client.data(js("console.log('acceptance log'); process.stdout.write('captured stdout'); acceptanceAnswer + 1"))
    assert second["value"] == 42, second
    assert second["logs"] == ["acceptance log", "captured stdout"], second
    native = client.data(js("await cu.doctor()"))
    assert isinstance(native["value"]["accessibility"], bool) and native["native_calls"] == 1, native
    parallel = client.data(js("await Promise.all([cu.doctor(), cu.doctor()])"))
    assert len(parallel["value"]) == 2 and parallel["native_calls"] == 2, parallel
    for code, expected in [
        ("throw new Error('expected synchronous js error')", "expected synchronous js error"),
        ("await Promise.reject(new Error('expected asynchronous js error'))", "expected asynchronous js error"),
        ("const = invalid", None),
        ("await cu.doctor({unknown_argument: true})", None),
    ]:
        error = js(code, error=True)
        if expected:
            assert expected in "\n".join(c.get("text", "") for c in error["content"]), error
    assert client.data(js("acceptanceAnswer"))["value"] == 41
    start = time.monotonic()
    timed_out = js("while (true) {}", timeout_ms=100, error=True)
    assert "timed out" in "\n".join(c.get("text", "") for c in timed_out["content"]), timed_out
    assert time.monotonic() - start < 5, "100 ms JS deadline did not promptly release the MCP request"
    assert client.data(js("typeof acceptanceAnswer"))["value"] == "undefined"
    assert client.data(js("const resetMarker = 99; resetMarker"))["value"] == 99
    assert client.data(client.tool("js_reset"))["status"] == "reset"
    assert client.data(js("typeof resetMarker"))["value"] == "undefined"
    assert client.data(client.tool("js_reset"))["status"] == "reset"
    return ["javascript_top_level_await", "javascript_persistent_lexical_state",
            "javascript_console_and_stdout_capture", "javascript_native_doctor_rpc",
            "javascript_promise_all_native_rpc", "javascript_sync_async_syntax_native_errors",
            "javascript_timeout_and_clean_restart", "javascript_explicit_reset"]


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("executable", type=pathlib.Path)
    parser.add_argument("--fixture-pid", type=int)
    parser.add_argument("--output", type=pathlib.Path)
    parser.add_argument("--service", action="store_true", help="Connect through the running native service using the mcp proxy")
    args = parser.parse_args()
    command = "mcp" if args.service else "standalone-mcp"
    client = Client(args.executable.resolve(), command=command)
    checks = []
    try:
        init = client.request("initialize", {"protocolVersion": "2025-06-18", "capabilities": {},
                                           "clientInfo": {"name": "personal-cu-acceptance", "version": "1"}})
        assert init["result"]["serverInfo"]["name"] == "personal-cu", init
        client.send({"jsonrpc": "2.0", "method": "notifications/initialized"})
        assert client.request("ping")["result"] == {}
        tools = client.request("tools/list")["result"]["tools"]
        expected_names = {"list_apps", "list_windows", "get_window", "launch_app", "get_app_state",
                          "get_window_state", "activate_window", "click", "type_text", "paste", "press_key",
                          "scroll", "set_value", "perform_secondary_action", "select_text", "drag", "drag_paths",
                          "debug_tap", "js", "js_reset", "doctor", "activate_app"}
        assert len(tools) == 22 and {t["name"] for t in tools} == expected_names, tools
        for tool in tools:
            strict_schema(tool["inputSchema"])
        checks += ["initialize", "ping", "twenty_two_strict_tool_schemas"]
        doctor = client.data(client.tool("doctor"))
        apps = client.data(client.tool("list_apps", {"running_only": True}))["apps"]
        assert isinstance(apps, list)
        checks += ["doctor", "list_apps"]
        for name, params in [
            ("unknown", {}), ("doctor", {"oops": 1}), ("get_app_state", {"pid": True}),
            ("get_app_state", {"pid": 1, "bundle_id": "x"}),
            ("get_app_state", {"pid": 2147483648}),
            ("click", {"pid": 1, "snapshot_id": "missing", "index": 1, "x": 1}),
            ("press_key", {"pid": 1, "snapshot_id": "missing", "key": "a", "modifiers": ["command", "command"]}),
            ("scroll", {"pid": 1, "snapshot_id": "missing", "delta_y": 1001}),
        ]:
            client.tool(name, params, error=True)
        checks.append("eight_invalid_requests_rejected")
        checks += javascript_checks(client)
        concurrent_frame_check(args.executable, command)
        checks.append("concurrent_six_megabyte_response_framing")
        if doctor["accessibility"]:
            client.tool("click", {"pid": 1, "snapshot_id": "missing", "index": 0}, error=True)
            checks.append("unknown_snapshot_rejected")
        if args.fixture_pid:
            pid = args.fixture_pid
            state = client.data(client.state(pid))
            field = next(n for n in state["elements"] if n.get("identifier") == "acceptance-input")
            token = {"pid": pid, "snapshot_id": state["snapshot_id"]}
            changed = client.data(client.tool("set_value", {**token, "index": field["index"], "value": "\u4e13\u5c5e Computer Use \u2713"}))
            assert changed["status"] == "verified"
            state = client.data(client.state(pid))
            assert any(n.get("value") == "\u4e13\u5c5e Computer Use \u2713" for n in state["elements"])
            checks.append("native_unicode_ax_write_readback")
            button = next(n for n in state["elements"] if n.get("identifier") == "acceptance-button")
            client.tool("click", {"pid": pid, "snapshot_id": state["snapshot_id"], "index": button["index"]})
            time.sleep(0.2)
            state = client.data(client.state(pid))
            assert any("Click verified" in str(n) for n in state["elements"])
            checks.append("native_ax_click_visible_state")
            screenshot = client.state(pid, "full")
            data = client.data(screenshot)
            image = next(c for c in screenshot["content"] if c["type"] == "image")
            png = base64.b64decode(image["data"], validate=True)
            assert png.startswith(b"\x89PNG\r\n\x1a\n") and data["screenshot"]["pixel_width"] > 0
            if args.output:
                args.output.mkdir(parents=True, exist_ok=True)
                (args.output / "acceptance.png").write_bytes(png)
            checks.append("native_window_png_capture")
        report = {"checks": checks, "transport": command, "permissions": doctor, "app_count": len(apps)}
        if args.output:
            args.output.mkdir(parents=True, exist_ok=True)
            (args.output / "acceptance.json").write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
        print(json.dumps(report, ensure_ascii=False, indent=2))
    finally:
        client.close()


if __name__ == "__main__":
    main()
