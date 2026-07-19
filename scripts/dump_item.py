import json,subprocess,threading,time,sys
TID=sys.argv[1]; WANT=sys.argv[2]
CODEX="/Applications/Codex.app/Contents/Resources/codex"
p=subprocess.Popen([CODEX,"app-server"],stdin=subprocess.PIPE,stdout=subprocess.PIPE,stderr=subprocess.PIPE,bufsize=0)
out=[]; threading.Thread(target=lambda:[out.append(l) for l in iter(p.stdout.readline,b"")],daemon=True).start()
def s(o): p.stdin.write((json.dumps(o)+"\n").encode()); p.stdin.flush()
s({"jsonrpc":"2.0","id":1,"method":"initialize","params":{"clientInfo":{"name":"d","version":"0"},"capabilities":{"experimentalApi":True}}})
time.sleep(0.5); s({"jsonrpc":"2.0","method":"initialized","params":{}}); time.sleep(0.2)
s({"jsonrpc":"2.0","id":2,"method":"thread/read","params":{"threadId":TID,"includeTurns":True}}); time.sleep(2.5)
p.terminate()
found=[]
def walk(n):
    if isinstance(n,dict):
        if n.get("type")==WANT: found.append(n)
        for v in n.values(): walk(v)
    elif isinstance(n,list):
        for v in n: walk(v)
for ln in b"".join(out).decode(errors="replace").splitlines():
    try: m=json.loads(ln)
    except: continue
    if m.get("id")==2 and "result" in m: walk(m["result"])
for f in found:
    # print keys and a shallow view (truncate long strings/base64)
    def shallow(d,depth=0):
        if depth>3: return "…"
        if isinstance(d,dict): return {k:(shallow(v,depth+1)) for k,v in d.items()}
        if isinstance(d,list): return [shallow(x,depth+1) for x in d[:3]]
        if isinstance(d,str): return d[:80]+("…" if len(d)>80 else "")
        return d
    print(json.dumps(shallow(f),ensure_ascii=False,indent=1)[:1500])
