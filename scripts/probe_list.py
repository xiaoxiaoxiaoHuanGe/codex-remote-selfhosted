#!/usr/bin/env python3
"""Phase-0 read-only probe: spawn a fresh `codex app-server` over stdio
(sharing ~/.codex with the desktop app), do the initialize handshake, then
list threads. Proves we can READ the user's existing sessions via the protocol.

SAFE: initialize + thread/list + thread/loaded/list only. No turns, no mutation.
"""
import json
import subprocess
import sys
import threading
import time

CODEX = sys.argv[1] if len(sys.argv) > 1 else "/Applications/Codex.app/Contents/Resources/codex"

p = subprocess.Popen(
    [CODEX, "app-server"],
    stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, bufsize=0,
)
out, err = [], []
threading.Thread(target=lambda: [out.append(l) for l in iter(p.stdout.readline, b"")], daemon=True).start()
threading.Thread(target=lambda: [err.append(l) for l in iter(p.stderr.readline, b"")], daemon=True).start()


def send(obj):
    p.stdin.write((json.dumps(obj) + "\n").encode())
    p.stdin.flush()


send({"jsonrpc": "2.0", "id": 1, "method": "initialize",
      "params": {"clientInfo": {"name": "codex-remote-probe", "version": "0.0.1"}}})
time.sleep(0.6)
send({"jsonrpc": "2.0", "method": "initialized", "params": {}})
time.sleep(0.3)
send({"jsonrpc": "2.0", "id": 2, "method": "thread/list", "params": {"limit": 8}})
send({"jsonrpc": "2.0", "id": 3, "method": "thread/loaded/list", "params": {"limit": 8}})
time.sleep(3.0)

try:
    p.stdin.close()
except Exception:
    pass
p.terminate()
try:
    p.wait(timeout=3)
except Exception:
    p.kill()

otext = b"".join(out).decode(errors="replace")
etext = b"".join(err).decode(errors="replace")
print("===== STDERR (first 1200) =====")
print(etext[:1200])
print("\n===== PARSED =====")
for line in otext.splitlines():
    line = line.strip()
    if not line:
        continue
    try:
        m = json.loads(line)
    except Exception:
        continue
    if "id" not in m:
        continue  # skip notifications
    rid = m.get("id")
    if "error" in m:
        print(f"id={rid} ERROR {json.dumps(m['error'])[:300]}")
        continue
    res = m.get("result")
    if rid == 1:
        print(f"id=1 initialize OK; result keys={list(res) if isinstance(res,dict) else res}")
    elif rid in (2, 3):
        threads = None
        if isinstance(res, dict):
            for k in ("threads", "items", "loaded", "list", "data"):
                if isinstance(res.get(k), list):
                    threads = res[k]; key = k; break
        label = "thread/list" if rid == 2 else "thread/loaded/list"
        if threads is None:
            print(f"{label}: result keys={list(res) if isinstance(res,dict) else res}")
        else:
            print(f"{label}: {len(threads)} threads (key='{key}')")
            for t in threads[:6]:
                if isinstance(t, dict):
                    tid = t.get("threadId") or t.get("id") or t.get("thread_id")
                    title = t.get("title") or t.get("name") or t.get("preview") or ""
                    cwd = t.get("cwd") or ""
                    updated = t.get("updatedAt") or t.get("modifiedAt") or t.get("updated_at") or ""
                    print(f"  - {tid}  cwd={cwd}  upd={updated}  title={str(title)[:60]}")
