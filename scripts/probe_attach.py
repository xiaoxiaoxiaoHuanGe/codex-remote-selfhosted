#!/usr/bin/env python3
"""Phase-0 read-only probe: attach to the running Codex desktop app-server
via `codex app-server proxy`, do the initialize handshake, then list threads.

SAFE: only calls initialize + thread/loaded/list + thread/list (read-only).
Does NOT start turns, send prompts, or mutate anything.

Usage: python3 probe_attach.py [SOCKET_PATH]
  - If SOCKET_PATH is given, passes --sock to the proxy.
  - If omitted, lets the proxy auto-discover the running app-server.
"""
import json
import subprocess
import sys
import threading
import time

sock_path = sys.argv[1] if len(sys.argv) > 1 else None

cmd = ["codex", "app-server", "proxy"]
if sock_path:
    cmd += ["--sock", sock_path]
print(f"# launching: {' '.join(cmd)}")

proc = subprocess.Popen(
    cmd,
    stdin=subprocess.PIPE,
    stdout=subprocess.PIPE,
    stderr=subprocess.PIPE,
    bufsize=0,
)

out_lines = []
err_lines = []


def reader(stream, sink, tag):
    for raw in iter(stream.readline, b""):
        sink.append(raw)


t_out = threading.Thread(target=reader, args=(proc.stdout, out_lines, "OUT"), daemon=True)
t_err = threading.Thread(target=reader, args=(proc.stderr, err_lines, "ERR"), daemon=True)
t_out.start()
t_err.start()


def send(obj):
    proc.stdin.write((json.dumps(obj) + "\n").encode())
    proc.stdin.flush()
    print(f">>> {obj.get('method')}  (id={obj.get('id')})")


try:
    send({"jsonrpc": "2.0", "id": 1, "method": "initialize",
          "params": {"clientInfo": {"name": "codex-remote-probe", "version": "0.0.1"}}})
    time.sleep(0.6)
    send({"jsonrpc": "2.0", "method": "initialized", "params": {}})
    time.sleep(0.3)
    send({"jsonrpc": "2.0", "id": 2, "method": "thread/loaded/list", "params": {}})
    send({"jsonrpc": "2.0", "id": 3, "method": "thread/list", "params": {}})
    time.sleep(3.0)
except BrokenPipeError:
    print("!!! broken pipe while sending")

try:
    proc.stdin.close()
except Exception:
    pass
time.sleep(0.5)
proc.terminate()
try:
    proc.wait(timeout=3)
except Exception:
    proc.kill()

out = b"".join(out_lines).decode(errors="replace")
err = b"".join(err_lines).decode(errors="replace")

print("\n===== STDERR (first 1500) =====")
print(err[:1500])
print("\n===== STDOUT RAW (first 7000) =====")
print(out[:7000])

print("\n===== PARSED SUMMARY =====")
for line in out.splitlines():
    line = line.strip()
    if not line:
        continue
    try:
        msg = json.loads(line)
    except Exception:
        continue
    if "id" in msg and ("result" in msg or "error" in msg):
        rid = msg["id"]
        if "error" in msg:
            print(f"id={rid} ERROR: {json.dumps(msg['error'])[:300]}")
        else:
            res = msg["result"]
            keys = list(res) if isinstance(res, dict) else type(res).__name__
            extra = ""
            if isinstance(res, dict):
                for k in ("threads", "items", "loaded", "list"):
                    if k in res and isinstance(res[k], list):
                        extra = f", {len(res[k])} entries in '{k}'"
                        break
            print(f"id={rid} OK -> {keys}{extra}")
