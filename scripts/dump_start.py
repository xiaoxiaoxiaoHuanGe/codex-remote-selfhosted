import json,subprocess,threading,time
CODEX="/Applications/Codex.app/Contents/Resources/codex"
p=subprocess.Popen([CODEX,"app-server"],stdin=subprocess.PIPE,stdout=subprocess.PIPE,stderr=subprocess.PIPE,bufsize=0)
out=[]; threading.Thread(target=lambda:[out.append(l) for l in iter(p.stdout.readline,b"")],daemon=True).start()
def s(o): p.stdin.write((json.dumps(o)+"\n").encode()); p.stdin.flush()
s({"jsonrpc":"2.0","id":1,"method":"initialize","params":{"clientInfo":{"name":"d","version":"0"},"capabilities":{"experimentalApi":True}}})
time.sleep(0.5); s({"jsonrpc":"2.0","method":"initialized","params":{}}); time.sleep(0.2)
import tempfile
s({"jsonrpc":"2.0","id":2,"method":"thread/start","params":{"cwd":tempfile.gettempdir(),"sandbox":"read-only","approvalPolicy":"untrusted","ephemeral":True}})
time.sleep(2); p.terminate()
for ln in b"".join(out).decode(errors="replace").splitlines():
    try: m=json.loads(ln)
    except: continue
    if m.get("id")==2:
        print("RESULT:",json.dumps(m.get("result",m.get("error")),ensure_ascii=False)[:700])
