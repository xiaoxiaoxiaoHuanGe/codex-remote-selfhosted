// codexbridge is the Phase-1 skeleton of the desktop bridge: it spawns a
// `codex app-server` child (sharing ~/.codex with the desktop app, so it
// inherits the same auth + model) and drives it over JSON-RPC.
//
// Subcommands (CLI test harness; the real bridge will expose a WebSocket):
//
//	codexbridge list                 # list existing sessions  (read-only)
//	codexbridge read <THREAD_ID>     # read one session's content (read-only)
//	codexbridge demo-turn            # start an ephemeral, READ-ONLY, sandboxed
//	                                 # turn and stream the reply (spends a tiny
//	                                 # bit of your ChatGPT quota; safe perms)
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/yunyuchen/codex-remote/bridge/internal/appserver"
	"github.com/yunyuchen/codex-remote/bridge/internal/bridge"
	"github.com/yunyuchen/codex-remote/bridge/internal/provision"
)

func main() {
	codexBin := flag.String("codex", "/Applications/Codex.app/Contents/Resources/codex", "path to the codex binary")
	flag.Parse()
	args := flag.Args()
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: codexbridge [-codex PATH] <list|read THREAD_ID|demo-turn|serve [-addr HOST:PORT]|agent ...|activate ...|reset-token>")
		os.Exit(2)
	}

	if args[0] == "reset-token" {
		res, err := provision.ResetMachineToken()
		if err != nil {
			fatal("reset-token", err)
		}
		fmt.Println("✓ 机器 token 已轮换 — 旧 token(含内置在已分发 App 里的)立即失效。")
		fmt.Printf("  机器:      %s\n", res.MachineID)
		fmt.Printf("  新 token:  %s\n", res.NewToken)
		fmt.Printf("  连接串:    %s\n", res.PhoneURL())
		fmt.Println("  手机重新配对:扫新二维码(托盘 ▸ 显示二维码),或重新填入上面的 token。")
		return
	}

	if args[0] == "serve" {
		fs := flag.NewFlagSet("serve", flag.ExitOnError)
		addr := fs.String("addr", "127.0.0.1:8765", "WebSocket listen address")
		token := fs.String("token", "", "pairing token (else $CODEX_BRIDGE_TOKEN; else random for dev)")
		devRandom := fs.Bool("dev-random-token", false, "allow a random token when none is configured (dev only)")
		_ = fs.Parse(args[1:])
		tok := *token
		if tok == "" {
			tok = os.Getenv("CODEX_BRIDGE_TOKEN") // launchd/systemd inject the secret here
		}
		runServe(*codexBin, *addr, tok, *devRandom)
		return
	}

	if args[0] == "agent" {
		fs := flag.NewFlagSet("agent", flag.ExitOnError)
		hubURL := fs.String("hub", "", "hub agent URL, e.g. wss://relay.example.com/agent")
		id := fs.String("id", "", "machine id the phone selects (e.g. office-win)")
		name := fs.String("name", "", "human label shown in the app (default: id)")
		token := fs.String("token", "", "per-machine phone token (else $CODEX_MACHINE_TOKEN, else -token-file)")
		agentKey := fs.String("agent-key", "", "hub register secret (else $CODEX_AGENT_KEY, else -agent-key-file)")
		tokenFile := fs.String("token-file", "", "read the per-machine token from this file (else $CODEX_MACHINE_TOKEN_FILE) — keeps the secret out of argv / Scheduled-Task XML")
		agentKeyFile := fs.String("agent-key-file", "", "read the hub register secret from this file (else $CODEX_AGENT_KEY_FILE)")
		cred := fs.String("cred", "", "machine credential from `codexbridge activate` (else $CODEX_MACHINE_CRED, else -cred-file)")
		credFile := fs.String("cred-file", "", "read the machine credential from this file (else $CODEX_MACHINE_CRED_FILE)")
		logPath := fs.String("log", "", "append agent output here (the Windows GUI build has no console; defaults to agent.log next to the exe on Windows)")
		_ = fs.Parse(args[1:])
		runAgent(*codexBin, *hubURL, *id, *name, *token, *agentKey, *cred, *tokenFile, *agentKeyFile, *credFile, *logPath)
		return
	}

	if args[0] == "activate" {
		fs := flag.NewFlagSet("activate", flag.ExitOnError)
		hubURL := fs.String("hub", "", "hub base URL, e.g. https://relay.example.com")
		key := fs.String("key", "", "license key (crk_...)")
		id := fs.String("id", "", "machine id the phone selects (e.g. office-mac)")
		name := fs.String("name", "", "human label shown in the app (default: id)")
		out := fs.String("out", "", "write the credential to this file (0600) instead of stdout")
		_ = fs.Parse(args[1:])
		runActivate(*hubURL, *key, *id, *name, *out)
		return
	}

	ctx := context.Background()
	c, err := appserver.New(*codexBin)
	must(err)
	defer c.Close()
	c.OnStderr = func(l string) { fmt.Fprintln(os.Stderr, "[codex] "+l) }
	// CLI safety net: decline any approval the agent requests.
	c.OnServerRequest = func(method string, params json.RawMessage) (any, error) {
		fmt.Fprintf(os.Stderr, "[approval requested: %s -> auto-DECLINE in CLI]\n", method)
		return map[string]any{"decision": "decline"}, nil
	}

	if _, err := c.Initialize(ctx, true); err != nil {
		fatal("initialize", err)
	}

	switch args[0] {
	case "list":
		cmdList(ctx, c)
	case "read":
		if len(args) < 2 {
			fatal("read", fmt.Errorf("need THREAD_ID"))
		}
		cmdRead(ctx, c, args[1])
	case "demo-turn":
		cmdDemoTurn(ctx, c)
	default:
		fatal("main", fmt.Errorf("unknown subcommand %q", args[0]))
	}
}

func cmdList(ctx context.Context, c *appserver.Client) {
	res, err := c.ThreadList(ctx, 12)
	must(err)
	fmt.Printf("%d sessions:\n", len(res.Data))
	for _, t := range res.Data {
		fmt.Printf("  %-38s  %-7s  %-42s  %s\n", t.ID, t.Source, truncate(t.Title(), 42), t.Cwd)
	}
}

func cmdRead(ctx context.Context, c *appserver.Client, id string) {
	raw, err := c.ThreadRead(ctx, id, true)
	must(err)
	var v any
	must(json.Unmarshal(raw, &v))
	hist := map[string]int{}
	walkTypes(v, hist, 0)
	fmt.Printf("thread %s — item types:\n", id)
	keys := make([]string, 0, len(hist))
	for k := range hist {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Printf("  %-24s %d\n", k, hist[k])
	}
}

func cmdDemoTurn(ctx context.Context, c *appserver.Client) {
	done := make(chan struct{})
	closed := false
	c.OnNotification = func(method string, params json.RawMessage) {
		switch {
		case strings.HasSuffix(method, "agentMessage/delta"):
			var d appserver.DeltaNotification
			_ = json.Unmarshal(params, &d)
			fmt.Print(d.Delta)
		case method == "turn/completed":
			if !closed {
				closed = true
				close(done)
			}
		}
	}

	tid, err := c.ThreadStart(ctx, appserver.ThreadStartParams{
		Cwd:            os.TempDir(),
		Sandbox:        "read-only",
		ApprovalPolicy: "untrusted",
		Ephemeral:      true,
	})
	must(err)
	fmt.Println("ephemeral thread:", tid)
	fmt.Print("reply: ")

	input := []any{appserver.Text("Reply with exactly the word: pong. Do not use any tools.")}
	if img := os.Getenv("IMG_DATA_URL"); img != "" {
		input = []any{
			appserver.Text("In one word, what is the dominant color of this image?"),
			appserver.Image(img),
		}
		fmt.Fprintf(os.Stderr, "[demo-turn: including image, %d bytes]\n", len(img))
	}
	_, err = c.TurnStart(ctx, appserver.TurnStartParams{
		ThreadID:       tid,
		Input:          input,
		ApprovalPolicy: "never",
		SandboxPolicy:  appserver.SandboxReadOnly,
	})
	must(err)

	select {
	case <-done:
		fmt.Println("\n[turn/completed]")
	case <-time.After(90 * time.Second):
		fmt.Println("\n[timeout]")
	}
}

func runServe(codexBin, addr, token string, devRandom bool) {
	// A public-facing bridge must never silently run on a throwaway secret. If no
	// token is configured, fail closed — unless -dev-random-token is set for local
	// experimentation (never reached by the launchd/systemd path).
	if token == "" {
		if !devRandom {
			fatal("serve", fmt.Errorf("no pairing token: set $CODEX_BRIDGE_TOKEN or pass -token (or -dev-random-token for local dev)"))
		}
		token = bridge.NewToken()
		fmt.Fprintf(os.Stderr, "WARNING: no token configured; using a random dev token (printed once below)\n")
		fmt.Fprintf(os.Stderr, "  dev token: %s\n", token)
	}

	ctx := context.Background()
	c, err := appserver.New(codexBin)
	must(err)
	defer c.Close()
	c.OnStderr = func(l string) { fmt.Fprintln(os.Stderr, "[codex] "+l) }

	srv := bridge.NewServer(c, token)
	// Phase 6: extra named device tokens for per-device revocation, via
	// CODEX_BRIDGE_TOKENS="phone:tokA,ipad:tokB". The -token/CODEX_BRIDGE_TOKEN
	// value remains the "default" device. Revoke a device by dropping its pair.
	for _, pair := range strings.Split(os.Getenv("CODEX_BRIDGE_TOKENS"), ",") {
		if name, tok, ok := strings.Cut(strings.TrimSpace(pair), ":"); ok {
			srv.AddDeviceToken(strings.TrimSpace(name), strings.TrimSpace(tok))
		}
	}
	// Opt-in end-to-end encryption of the phone control-plane (CODEX_E2EE=1). It
	// layers ON TOP of the ?token= gate above — the token still guards the WS
	// upgrade — so leaving it off keeps existing token-only clients working.
	enableE2EE(ctx, srv)
	srv.SetPubEnabled(false) // serve mode has no relay tier; lanInfo says so
	// Phase 5: background push channel. Defaults to a no-op LogPusher. To enable a
	// real channel later, build a bridge.Pusher (APNs/FCM) from env credentials and
	// srv.SetPusher(it) here — see bridge/PUSH.md. Unset creds => stays no-op.
	if _, err := c.Initialize(ctx, true); err != nil {
		fatal("initialize", err)
	}

	// Graceful shutdown: clean up the spawned app-server child on signal.
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
		<-sig
		_ = c.Close()
		os.Exit(0)
	}()

	// Never log the token itself (stdout is persisted to bridge.log under launchd).
	// Print a short fingerprint so the operator can confirm WHICH token is live.
	fmt.Printf("codex-remote bridge ready on ws://%s/ws (token %s)\n", addr, tokenFingerprint(token))
	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	if err := httpSrv.ListenAndServe(); err != nil {
		fatal("serve", err)
	}
}

// enableE2EE activates the opt-in encrypted control-plane when CODEX_E2EE=1. It
// loads/generates the enrollment key + device registry under ~/.codex/bridge (or
// $CODEX_E2EE_DIR), turns the codec on, and hot-reloads the registry so a tray
// revoke takes effect on running connections. A setup error is fatal: if the
// operator asked for E2EE, silently falling back to cleartext is the wrong failure.
func enableE2EE(ctx context.Context, srv *bridge.Server) {
	if os.Getenv("CODEX_E2EE") != "1" {
		return
	}
	dir, err := bridge.E2EEDir()
	must(err)
	enrollPub, devices, err := srv.EnableE2EEFromDir(dir)
	if err != nil {
		fatal("e2ee", err)
	}
	go devices.Watch(ctx, 2*time.Second, func(err error) {
		if err != nil {
			log.Printf("e2ee: device registry reload failed: %v", err)
			return
		}
		log.Printf("e2ee: device registry reloaded")
		srv.NotifyDevicesChanged() // re-sync public keys to the hub (agent mode)
	})
	fmt.Printf("codex-remote E2EE active (enroll %s, state %s)\n", bridge.EnrollFingerprint(enrollPub), dir)
}

// runAgent runs the bridge in dial-out (hub) mode: it connects to the hub, registers
// this machine, and serves phone sessions the hub forwards. No inbound port / DNS.
func runAgent(codexBin, hubURL, id, name, token, agentKey, cred, tokenFile, agentKeyFile, credFile, logPath string) {
	// On Windows the agent ships as a GUI-subsystem binary (-H windowsgui) so no
	// console window pops up at logon — but that detaches stdout/stderr, so route
	// all output to a log file instead (default: agent.log next to the exe).
	if logPath == "" && runtime.GOOS == "windows" {
		if exe, err := os.Executable(); err == nil {
			logPath = filepath.Join(filepath.Dir(exe), "agent.log")
		}
	}
	if logPath != "" {
		if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
			os.Stdout = f
			os.Stderr = f
			log.SetOutput(f)
		}
	}

	if hubURL == "" {
		fatal("agent", fmt.Errorf("need -hub (e.g. wss://relay.example.com/agent)"))
	}
	if id == "" {
		fatal("agent", fmt.Errorf("need -id (machine id the phone selects)"))
	}
	// Resolution order for each secret: explicit flag → env value → file (flag or
	// env). The *-file path lets a Scheduled Task / launchd unit reference an
	// ACL/0600-locked file instead of putting the secret on argv (Tasks XML and the
	// process command line are readable by other local users on Windows).
	if token == "" {
		token = os.Getenv("CODEX_MACHINE_TOKEN")
	}
	if token == "" {
		if tokenFile == "" {
			tokenFile = os.Getenv("CODEX_MACHINE_TOKEN_FILE")
		}
		token = readSecretFile(tokenFile)
	}
	if token == "" {
		fatal("agent", fmt.Errorf("no machine token: set $CODEX_MACHINE_TOKEN, -token, or -token-file"))
	}
	if agentKey == "" {
		agentKey = os.Getenv("CODEX_AGENT_KEY")
	}
	if agentKey == "" {
		if agentKeyFile == "" {
			agentKeyFile = os.Getenv("CODEX_AGENT_KEY_FILE")
		}
		agentKey = readSecretFile(agentKeyFile)
	}
	if cred == "" {
		cred = os.Getenv("CODEX_MACHINE_CRED")
	}
	if cred == "" {
		if credFile == "" {
			credFile = os.Getenv("CODEX_MACHINE_CRED_FILE")
		}
		cred = readSecretFile(credFile)
	}
	if agentKey == "" && cred == "" && credFile == "" {
		fatal("agent", fmt.Errorf("no hub register secret: license mode needs $CODEX_MACHINE_CRED / -cred / -cred-file (run `codexbridge activate` first); self-host needs $CODEX_AGENT_KEY / -agent-key / -agent-key-file"))
	}
	if name == "" {
		name = id
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c, err := appserver.New(codexBin)
	must(err)
	defer c.Close()
	c.OnStderr = func(l string) { fmt.Fprintln(os.Stderr, "[codex] "+l) }

	srv := bridge.NewServer(c, token)
	// Opt-in E2EE (CODEX_E2EE=1), layered on top of the machine token gate. The hub
	// stays protocol-blind: it forwards the phone's opaque envelopes by SID.
	enableE2EE(ctx, srv)
	// LAN direct listener (default ON): the same Server — token gate and every
	// clamp included. CODEX_LAN_ADDR overrides (":8767" form), "off" disables.
	// Starts BEFORE the license-credential wait below so an unactivated install
	// is immediately usable on the local network (the free tier).
	lanAddr := os.Getenv("CODEX_LAN_ADDR")
	if lanAddr == "" {
		lanAddr = ":8767"
	}
	if port := bridge.StartLANListener(srv, lanAddr); port != "" {
		srv.SetLANPort(port)
	}
	srv.SetPubEnabled(agentKey != "" || cred != "")
	// Bound the handshake: if codex app-server doesn't respond (e.g. spawned in a
	// context where it dies), fail fast so launchd restarts us instead of hanging
	// forever with no banner and no registration.
	ictx, icancel := context.WithTimeout(ctx, 30*time.Second)
	_, err = c.Initialize(ictx, true)
	icancel()
	if err != nil {
		fatal("initialize", err)
	}

	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
		<-sig
		cancel()
		_ = c.Close()
		os.Exit(0)
	}()

	if agentKey == "" && cred == "" {
		// License mode, not yet activated: the LAN listener above already
		// serves phones on the local network (don't crash-loop under launchd/
		// Scheduled Task); wait for the tray activation (「激活订阅码…」, or a
		// manual `codexbridge activate -out`) to write the credential, then
		// register with the hub. Install and activation stay decoupled.
		log.Printf("agent: not activated yet — LAN direct available; waiting for credential file %s (tray ▸ 激活订阅码)", credFile)
		for cred == "" {
			time.Sleep(5 * time.Second)
			cred = readSecretFile(credFile)
		}
		log.Printf("agent: credential found — registering")
		srv.SetPubEnabled(true)
	}

	fmt.Printf("codex-remote agent: machine %q (%s) -> %s (token %s)\n", id, name, hubURL, tokenFingerprint(token))
	srv.RunAgent(ctx, bridge.AgentConfig{HubURL: hubURL, MachineID: id, Name: name, Token: token, AgentKey: agentKey, Cred: cred})
}

// readSecretFile reads a secret (token / agent key) from a file and trims
// surrounding whitespace. Empty path or any read error returns "" so the caller's
// "still empty" check produces the right fatal message.
func readSecretFile(path string) string {
	if path == "" {
		return ""
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// tokenFingerprint returns a short, non-reversible id for a token so logs can
// identify it without disclosing it.
func tokenFingerprint(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return "sha256:" + hex.EncodeToString(sum[:4])
}

// walkTypes counts every "type" string field in a decoded JSON tree.
func walkTypes(v any, acc map[string]int, depth int) {
	if depth > 8 {
		return
	}
	switch x := v.(type) {
	case map[string]any:
		if t, ok := x["type"].(string); ok {
			acc[t]++
		}
		for _, val := range x {
			walkTypes(val, acc, depth+1)
		}
	case []any:
		for _, val := range x {
			walkTypes(val, acc, depth+1)
		}
	}
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func fatal(where string, err error) {
	fmt.Fprintf(os.Stderr, "%s: %v\n", where, err)
	os.Exit(1)
}
