import json,subprocess,threading,time,sys
exp = (sys.argv[1]=="exp") if len(sys.argv)>1 else False
CODEX="/Applications/Codex.app/Contents/Resources/codex"
p=subprocess.Popen([CODEX,"app-server"],stdin=subprocess.PIPE,stdout=subprocess.PIPE,stderr=subprocess.PIPE,bufsize=0)
out=[]; threading.Thread(target=lambda:[out.append(l) for l in iter(p.stdout.readline,b"")],daemon=True).start()
def s(o): p.stdin.write((json.dumps(o)+"\n").encode()); p.stdin.flush()
caps={"experimentalApi":True} if exp else {}
s({"jsonrpc":"2.0","id":1,"method":"initialize","params":{"clientInfo":{"name":"d","version":"0"},"capabilities":caps}})
time.sleep(0.5); s({"jsonrpc":"2.0","method":"initialized","params":{}}); time.sleep(0.2)
s({"jsonrpc":"2.0","id":2,"method":"thread/list","params":{"limit":3}}); time.sleep(2)
p.terminate()
for ln in b"".join(out).decode(errors="replace").splitlines():
    try: m=json.loads(ln)
    except: continue
    if m.get("id")==2 and "result" in m:
        data=m["result"].get("data",[])
        print(f"exp={exp}  entries={len(data)}")
        if data: print("first entry:",json.dumps(data[0],ensure_ascii=False)[:600])
