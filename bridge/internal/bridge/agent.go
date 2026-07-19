package bridge

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"log"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// maxMediaBytes caps a single /file transfer over the hub. The whole file is
// base64'd into one frame, so this must stay well under the 32MB ws read limit
// (12MB * 1.34 + JSON overhead < 32MB) to bound RAM on both machine and hub.
const maxMediaBytes = 12 << 20

// hubFrame is the envelope on the agent<->hub WebSocket. The hub multiplexes many
// phone sessions over this one connection, tagged by SID; Data carries the raw
// phone/bridge protocol object unchanged.
type hubFrame struct {
	T    string          `json:"t"` // register|registered|error|notice|open|msg|close|file|devices
	SID  string          `json:"sid,omitempty"`
	Data json.RawMessage `json:"data,omitempty"`

	// file proxy
	ReqID  string `json:"reqId,omitempty"`
	Path   string `json:"path,omitempty"`
	Status int    `json:"status,omitempty"`
	Ctype  string `json:"ctype,omitempty"`
	B64    string `json:"b64,omitempty"`

	// register
	MachineID string `json:"machineId,omitempty"`
	Name      string `json:"name,omitempty"`
	Token     string `json:"token,omitempty"`
	AgentKey  string `json:"agentKey,omitempty"`
	Cred      string `json:"cred,omitempty"` // license mode: per-machine credential
	Message   string `json:"message,omitempty"`
}

// DevicePub is the PUBLIC half of one paired device, synced to the hub so the hub
// can run the device challenge-response gate (HUB_REQUIRE_DEVICE_AUTH). It carries
// NO secret: the PSK and the device's private key never leave their owners, so a
// compromised hub still cannot decrypt traffic or impersonate a device.
type DevicePub struct {
	KeyID     string `json:"keyId"`
	Name      string `json:"name"`
	DevicePub string `json:"devicePub"` // base64 std, 32 bytes (X25519)
}

// DevicePubs snapshots the paired devices' public keys for hub sync, stripping the
// PSK. Returns nil when E2EE is off (no registry to sync) — the hub then has no
// device list and, with HUB_REQUIRE_DEVICE_AUTH on, fails closed.
func (s *Server) DevicePubs() []DevicePub {
	if !s.e2eeOn() {
		return nil
	}
	all := s.devices.All()
	out := make([]DevicePub, 0, len(all))
	for _, d := range all {
		out = append(out, DevicePub{KeyID: d.KeyID, Name: d.Name, DevicePub: d.DevicePub})
	}
	return out
}

// AgentConfig configures dial-out (hub) mode.
type AgentConfig struct {
	HubURL    string // e.g. wss://relay.example.com/agent
	MachineID string // stable id the phone selects (e.g. "office-win")
	Name      string // human label shown in the app
	Token     string // per-machine token the phone must present
	AgentKey  string // shared secret to register with the hub (self-host mode)
	// Cred is the per-machine credential from `codexbridge activate` (license
	// mode). When set it replaces AgentKey as the hub register secret.
	Cred string
}

// RunAgent connects to the hub and serves phone sessions it forwards, reusing all
// of Server's logic (downgrade, approval clamp/ownership, media allowlist). It
// reconnects forever until ctx is cancelled.
func (s *Server) RunAgent(ctx context.Context, cfg AgentConfig) {
	backoff := time.Second
	for ctx.Err() == nil {
		start := time.Now()
		err := s.runAgentOnce(ctx, cfg)
		if ctx.Err() != nil {
			return
		}
		// A link that stayed up a while before dropping (e.g. the relay/Cloudflare
		// ~30-min connection recycle) is a healthy endpoint, not a failing one —
		// reconnect immediately instead of carrying over an inflated backoff.
		if time.Since(start) > 60*time.Second {
			backoff = time.Second
		}
		log.Printf("agent: hub link ended (%v); reconnecting in %s", err, backoff)
		t := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func (s *Server) runAgentOnce(ctx context.Context, cfg AgentConfig) error {
	cctx, ccancel := context.WithCancel(ctx)
	defer ccancel()

	dctx, dcancel := context.WithTimeout(cctx, 20*time.Second)
	ws, _, err := websocket.Dial(dctx, cfg.HubURL, nil)
	dcancel()
	if err != nil {
		return err
	}
	ws.SetReadLimit(32 << 20) // file frames (base64) can be large
	defer ws.Close(websocket.StatusNormalClosure, "bye")

	a := &agentLink{ws: ws, srv: s, sessions: map[string]*conn{}}
	defer a.closeAll()

	if err := a.writeFrame(cctx, hubFrame{
		T: "register", MachineID: cfg.MachineID, Name: cfg.Name, Token: cfg.Token,
		AgentKey: cfg.AgentKey, Cred: cfg.Cred,
	}); err != nil {
		return err
	}

	// Sync device PUBLIC keys to the hub right after register so the hub's device
	// challenge-response gate can authenticate phones without a bearer token. Replay
	// the full list on every registry change (tray pair/revoke) and on reconnect.
	// No-op (sends an empty list) when E2EE is off, which is the correct fail-closed
	// input for a hub running HUB_REQUIRE_DEVICE_AUTH.
	a.syncDevices(cctx)
	go func() {
		for {
			select {
			case <-cctx.Done():
				return
			case <-s.devicesChanged:
				a.syncDevices(cctx)
			}
		}
	}()

	// Keepalive: ping the hub every 25s so the link survives Cloudflare/proxy idle
	// timeouts (~60s). On failure, cancel so the read unblocks and we reconnect.
	go func() {
		t := time.NewTicker(25 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-cctx.Done():
				return
			case <-t.C:
				pctx, pcancel := context.WithTimeout(cctx, 10*time.Second)
				err := ws.Ping(pctx)
				pcancel()
				if err != nil {
					ccancel()
					return
				}
			}
		}
	}()

	for {
		var f hubFrame
		if err := wsjson.Read(cctx, ws, &f); err != nil {
			return err
		}
		switch f.T {
		case "registered":
			log.Printf("agent: registered with hub as %q (%s)", cfg.MachineID, cfg.Name)
		case "error":
			log.Printf("agent: hub error: %s", f.Message)
		case "notice":
			// hub-side renewal reminders (expiry grace) — surface in the log/tray
			log.Printf("agent: hub notice: %s", f.Message)
		case "open":
			a.open(cctx, f.SID)
		case "msg":
			a.message(cctx, f.SID, f.Data)
		case "close":
			a.closeSession(f.SID)
		case "file":
			go a.serveFile(cctx, f.ReqID, f.Path)
		}
	}
}

// agentLink owns one agent<->hub WebSocket and the phone sessions on it.
type agentLink struct {
	ws  *websocket.Conn
	srv *Server

	wmu sync.Mutex // serializes writes to the shared hub ws

	mu       sync.Mutex
	sessions map[string]*conn
}

func (a *agentLink) writeFrame(ctx context.Context, f hubFrame) error {
	// Take the lock FIRST, then arm the timeout: the deadline must bound the
	// write itself, not write+lock-wait — otherwise a queue of big frames (e.g.
	// /file transfers) silently eats the budget of everyone waiting behind them
	// and kills writes that would have succeeded.
	a.wmu.Lock()
	defer a.wmu.Unlock()
	wctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return wsjson.Write(wctx, a.ws, f)
}

// syncDevices pushes the current device PUBLIC keys to the hub as a "devices" frame.
// The hub replaces the machine's key set with it. PSKs are never included (DevicePubs
// strips them), so the hub learns device identities but can never decrypt traffic.
func (a *agentLink) syncDevices(ctx context.Context) {
	b, err := json.Marshal(a.srv.DevicePubs())
	if err != nil {
		return
	}
	_ = a.writeFrame(ctx, hubFrame{T: "devices", Data: b})
}

func (a *agentLink) open(ctx context.Context, sid string) {
	c := &conn{
		send:   make(chan any, 256),
		done:   make(chan struct{}),
		id:     sid,
		device: "phone",
		write: func(wctx context.Context, v any) error {
			b, err := json.Marshal(v)
			if err != nil {
				return err
			}
			return a.writeFrame(wctx, hubFrame{T: "msg", SID: sid, Data: b})
		},
		// Mirror the direct transport: under E2EE the codec arms on the first frame
		// and writePump holds outbound until then. The hub stays protocol-blind —
		// it forwards the phone's opaque envelopes/pair_init as this session's Data.
		wantE2EE: a.srv.e2eeOn(),
		armed:    make(chan struct{}),
	}
	// If this session's writePump dies (e.g. a write timeout while another big
	// frame held the shared wmu), reap the session AND tell the hub to drop the
	// phone — its ws closes, it auto-reconnects, and a fresh session replaces the
	// would-be zombie that silently black-holed every reply.
	c.onDead = func() {
		a.srv.audit("agent reaping dead session sid=%s (writePump exit)", sid)
		a.closeSession(sid)
		// Background ctx (not the session's): writeFrame bounds the write with
		// its own post-lock timeout, so this close frame survives the very
		// congestion that killed the pump instead of expiring in the lock queue.
		_ = a.writeFrame(context.Background(), hubFrame{T: "close", SID: sid})
	}
	a.mu.Lock()
	a.sessions[sid] = c
	a.mu.Unlock()
	a.srv.addConn(c)
	go c.writePump(ctx)

	// Resync: re-surface still-pending approvals to the fresh session. Under E2EE
	// this MUST wait until the codec arms (routeInbound calls resyncTo after the
	// first envelope) so nothing is queued as cleartext before a key is bound.
	if !a.srv.e2eeOn() {
		a.srv.resyncTo(c)
		a.srv.sendLanInfo(c)
	}
}

func (a *agentLink) message(ctx context.Context, sid string, data json.RawMessage) {
	a.mu.Lock()
	c := a.sessions[sid]
	a.mu.Unlock()
	if c == nil {
		// Frame for a session we already reaped (e.g. its writePump died and the
		// close notification was lost). Re-send the close so the hub drops the
		// phone and it reconnects, instead of black-holing against a dead sid.
		go func() { _ = a.writeFrame(context.Background(), hubFrame{T: "close", SID: sid}) }()
		return
	}
	// Route through the shared E2EE seam (same as the direct read loop). It decrypts
	// an envelope, handles a plaintext pair_init inline, or — in legacy mode —
	// unmarshals directly. Any decode/decrypt/pairing failure closes the session
	// rather than processing an unverified frame. Called serially by the agent's
	// single read loop, so the per-session seq/bind state needs no extra lock.
	in, err := a.srv.routeInbound(ctx, c, data)
	if err != nil {
		a.srv.audit("agent closing session sid=%s: %v", sid, err)
		a.closeSession(sid)
		return
	}
	if in == nil {
		return // control frame (pairing) handled inline; nothing to dispatch
	}
	go a.srv.handle(ctx, c, *in)
}

func (a *agentLink) closeSession(sid string) {
	a.mu.Lock()
	c := a.sessions[sid]
	delete(a.sessions, sid)
	a.mu.Unlock()
	if c == nil {
		return
	}
	a.srv.removeConn(c)
	c.stop()
}

func (a *agentLink) closeAll() {
	a.mu.Lock()
	ss := a.sessions
	a.sessions = map[string]*conn{}
	a.mu.Unlock()
	for _, c := range ss {
		a.srv.removeConn(c)
		c.stop()
	}
}

// serveFile answers a hub /file proxy request: validate via the SAME media
// allowlist as /file, then return the bytes (base64) over the hub link.
func (a *agentLink) serveFile(ctx context.Context, reqID, path string) {
	real, code := a.srv.resolveMedia(path)
	if code != 200 {
		_ = a.writeFrame(ctx, hubFrame{T: "file", ReqID: reqID, Status: code})
		return
	}
	if fi, err := os.Stat(real); err != nil || fi.Size() > maxMediaBytes {
		st := http.StatusNotFound
		if err == nil {
			st = http.StatusRequestEntityTooLarge
		}
		_ = a.writeFrame(ctx, hubFrame{T: "file", ReqID: reqID, Status: st})
		return
	}
	b, err := os.ReadFile(real)
	if err != nil {
		_ = a.writeFrame(ctx, hubFrame{T: "file", ReqID: reqID, Status: http.StatusNotFound})
		return
	}
	ctype := mime.TypeByExtension(filepath.Ext(real))
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	_ = a.writeFrame(ctx, hubFrame{
		T: "file", ReqID: reqID, Status: 200, Ctype: ctype, B64: base64.StdEncoding.EncodeToString(b),
	})
}
