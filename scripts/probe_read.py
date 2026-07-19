#!/usr/bin/env python3
"""Phase-0 read-only probe: read one thread's content + turns.
Proves "read my current session" and reveals the item/message shapes
needed for the Flutter renderer.

SAFE: initialize + thread/read + thread/turns/list. No turns started, no mutation.

Usage: python3 probe_read.py <THREAD_ID> [CODEX_BIN]
"""
import json
import subprocess
import sys
import threading
import time

TID = sys.argv[1]
CODEX = sys.argv[2] if len(sys.argv) > 2 else "/Applications/Codex.app/Contents/Resources/codex"

p = subprocess.Popen([CODEX, "app-server"], stdin=subprocess.PIPE,
                     stdout=subprocess.PIPE, stderr=subprocess.PIPE, bufsize=0)
out, err = [], []
threading.Thread(target=lambda: [out.append(l) for l in iter(p.stdout.readline, b"")], daemon=True).start()
threading.Thread(target=lambda: [err.append(l) for l in iter(p.stderr.readline, b"")], daemon=True).start()


def send(o):
    p.stdin.write((json.dumps(o) + "\n").encode()); p.stdin.flush()


send({"jsonrpc": "2.0", "id": 1, "method": "initialize",
      "params": {"clientInfo": {"name": "probe", "version": "0.0.1"}}})
time.sleep(0.6)
send({"jsonrpc": "2.0", "method": "initialized", "params": {}})
time.sleep(0.3)
send({"jsonrpc": "2.0", "id": 2, "method": "thread/read",
      "params": {"threadId": TID, "includeTurns": True}})
send({"jsonrpc": "2.0", "id": 3, "method": "thread/turns/list",
      "params": {"threadId": TID, "limit": 30}})
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


def walk_types(obj, acc, depth=0):
    """collect 'type' field values to learn item kinds."""
    if depth > 6:
        return
    if isinstance(obj, dict):
        if "type" in obj and isinstance(obj["type"], str):
            acc[obj["type"]] = acc.get(obj["type"], 0) + 1
        for v in obj.values():
            walk_types(v, acc, depth + 1)
    elif isinstance(obj, list):
        for v in obj:
            walk_types(v, acc, depth + 1)


otext = b"".join(out).decode(errors="replace")
print("STDERR:", b"".join(err).decode(errors="replace")[:600])
for line in otext.splitlines():
    line = line.strip()
    if not line:
        continue
    try:
        m = json.loads(line)
    except Exception:
        continue
    if "id" not in m:
        continue
    rid = m.get("id")
    if "error" in m:
        print(f"id={rid} ERROR {json.dumps(m['error'])[:300]}"); continue
    res = m.get("result")
    if rid == 1:
        continue
    print(f"\n===== id={rid} ({'thread/read' if rid==2 else 'thread/turns/list'}) =====")
    if isinstance(res, dict):
        print("top-level keys:", list(res))
        types = {}
        walk_types(res, types)
        print("item 'type' histogram:", json.dumps(types, ensure_ascii=False))
        # try to find turns/items lists
        for k, v in res.items():
            if isinstance(v, list):
                print(f"  list '{k}': {len(v)} entries; first entry keys:",
                      list(v[0]) if v and isinstance(v[0], dict) else (v[0] if v else None))
    else:
        print("result:", str(res)[:300])
