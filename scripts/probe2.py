import json,subprocess,sys,threading,time
sock=sys.argv[1] if len(sys.argv)>1 else None
cmd=["codex","app-server","proxy"]+(["--sock",sock] if sock else [])
print("# launch:",*cmd)
p=subprocess.Popen(cmd,stdin=subprocess.PIPE,stdout=subprocess.PIPE,stderr=subprocess.PIPE,bufsize=0)
out=[];err=[]
threading.Thread(target=lambda:[out.append(l) for l in iter(p.stdout.readline,b"")],daemon=True).start()
threading.Thread(target=lambda:[err.append(l) for l in iter(p.stderr.readline,b"")],daemon=True).start()
try:
    p.stdin.write((json.dumps({"jsonrpc":"2.0","id":1,"method":"initialize","params":{"clientInfo":{"name":"probe","version":"0.0.1"}}})+"\n").encode())
    p.stdin.flush()
    print(">>> sent initialize, waiting 4s ...")
    time.sleep(4)
except BrokenPipeError:
    print("!!! broken pipe on initialize")
try: p.stdin.close()
except: pass
time.sleep(0.3); p.terminate()
try: p.wait(timeout=2)
except: p.kill()
print("STDERR:",b"".join(err).decode(errors='replace')[:800])
print("STDOUT:",b"".join(out).decode(errors='replace')[:3000])
