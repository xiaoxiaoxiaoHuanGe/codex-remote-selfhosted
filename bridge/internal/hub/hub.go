// Package hub is the multi-machine switchboard. Machines (bridges in agent mode)
// dial in to /agent and register a machineId + token; phones connect to
// /ws?machine=<id>&token=<t> and the hub transparently relays the bridge protocol
// between the phone and the selected machine. No per-machine DNS, port, or nginx —
// adding a machine is just running an agent. The phone protocol is unchanged; the
// hub never interprets it (except /file, which it proxies to the machine, and the
// device-auth handshake below, which it consumes and never forwards).
//
// Phone authentication has two modes:
//   - Legacy (default): the unique per-machine bearer token in ?token= both routes
//     to the machine and authorizes the phone (findAgent).
//   - Device-auth (HUB_REQUIRE_DEVICE_AUTH=1): the bearer token is gone. Each machine
//     syncs its paired devices' PUBLIC keys to the hub via a "devices" frame; on /ws
//     the phone sends ?machine=&keyId= and, after the WS upgrade, proves possession
//     of that device's secret in a challenge-response (deviceAuth). The hub seals a
//     fresh nonce to the synced public key; only the real device can open it. No
//     reusable secret ever travels in the URL, and revoking a device on the machine
//     (which drops it from the next "devices" sync) bars it at the hub. The PSK is
//     never synced, so the hub authenticates devices but still cannot decrypt E2EE
//     traffic. /file works without the bearer token via a short-lived, agent-scoped
//     session ticket minted at the handshake (sent in auth_ok, dropped on disconnect).
//
// Agent (machine) authentication likewise has two modes:
//   - Self-host (default): every machine presents the shared HUB_AGENT_KEY.
//   - License (HUB_LICENSE_MODE=1): each machine presents the per-machine
//     credential minted by POST /api/activate against a license key; the
//     internal/license roster (machine limit, swap rate limit, expiry grace)
//     decides admission, re-checked hourly for long-lived links (recheck.go).
//     /api/roster + /api/roster/deactivate let a paired phone list and
//     self-service-unbind its account's machines (api.go).
package hub

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/yunyuchen/codex-remote/bridge/internal/e2ee"
	"github.com/yunyuchen/codex-remote/bridge/internal/license"
)

// frame is the agent<->hub envelope (mirror of bridge.hubFrame).
type frame struct {
	T    string          `json:"t"`
	SID  string          `json:"sid,omitempty"`
	Data json.RawMessage `json:"data,omitempty"`

	ReqID  string `json:"reqId,omitempty"`
	Path   string `json:"path,omitempty"`
	Status int    `json:"status,omitempty"`
	Ctype  string `json:"ctype,omitempty"`
	B64    string `json:"b64,omitempty"`

	MachineID string `json:"machineId,omitempty"`
	Name      string `json:"name,omitempty"`
	Token     string `json:"token,omitempty"`
	AgentKey  string `json:"agentKey,omitempty"`
	Cred      string `json:"cred,omitempty"` // license mode: per-machine credential (replaces AgentKey)
	Message   string `json:"message,omitempty"`
}

type Hub struct {
	agentKey string
	mu       sync.Mutex
	agents   map[string]*agent
	started  time.Time // process-lifetime anchor for the admin /admin/status uptime

	// RequireDeviceAuth replaces the phone's bearer ?token= gate with a device
	// public-key challenge-response on /ws (and a session ticket for /file). Off by
	// default for backward compatibility; flip it (HUB_REQUIRE_DEVICE_AUTH=1) only
	// once every machine runs CODEX_E2EE=1 and every phone client speaks the
	// handshake — a machine with no synced device keys then rejects all phones.
	RequireDeviceAuth bool

	// Lic, when non-nil, switches agent registration from the shared agentKey to
	// per-machine credentials checked against the license roster (HUB_LICENSE_MODE).
	// nil = self-host mode: behavior identical to before this field existed.
	Lic *license.Service

	// fileTix holds short-lived, agent-scoped /file session tickets minted after a
	// successful device-auth handshake. They stand in for the bearer token (which
	// device-auth removed from the URL) so the phone can still fetch media. Each is
	// dropped when its phone disconnects; resolveFileTicket also lazily purges expiry.
	ftmu    sync.Mutex
	fileTix map[string]fileTicket
}

func New(agentKey string) *Hub {
	return &Hub{
		agentKey: agentKey,
		agents:   make(map[string]*agent),
		fileTix:  make(map[string]fileTicket),
		started:  time.Now(),
	}
}

// fileTicket binds a /file session ticket to one machine, with an expiry.
type fileTicket struct {
	machineID string
	expiry    time.Time
}

const fileTicketTTL = 30 * time.Minute

// mintFileTicket creates a random, agent-scoped /file ticket valid for fileTicketTTL.
func (h *Hub) mintFileTicket(machineID string) string {
	t := tok()
	h.ftmu.Lock()
	h.fileTix[t] = fileTicket{machineID: machineID, expiry: time.Now().Add(fileTicketTTL)}
	h.ftmu.Unlock()
	return t
}

// dropFileTicket revokes a ticket (called when its phone disconnects).
func (h *Hub) dropFileTicket(t string) {
	if t == "" {
		return
	}
	h.ftmu.Lock()
	delete(h.fileTix, t)
	h.ftmu.Unlock()
}

// resolveFileTicket returns the machine id for a live ticket, or "" if unknown or
// expired (expired entries are purged on lookup).
func (h *Hub) resolveFileTicket(t string) string {
	if t == "" {
		return ""
	}
	h.ftmu.Lock()
	defer h.ftmu.Unlock()
	e, ok := h.fileTix[t]
	if !ok {
		return ""
	}
	if time.Now().After(e.expiry) {
		delete(h.fileTix, t)
		return ""
	}
	return e.machineID
}

// devicePubEntry is one item of the agent's "devices" sync frame (mirror of
// bridge.DevicePub) — a device's PUBLIC key only, never its PSK.
type devicePubEntry struct {
	KeyID     string `json:"keyId"`
	Name      string `json:"name"`
	DevicePub string `json:"devicePub"` // base64 std, 32 bytes (X25519)
}

// authChallenge is the hub->phone proof-of-possession challenge (phone-protocol
// JSON, consumed by the hub, never forwarded to the machine). The phone opens box
// with its device secret + epk and returns the recovered nonce in an authResponse.
type authChallenge struct {
	Type  string `json:"type"` // "auth_challenge"
	EPK   string `json:"epk"`  // hub ephemeral public key, base64 std
	Box   string `json:"box"`  // sealed boxNonce||box, base64 std
	KeyID string `json:"keyId"`
}

// authResponse is the phone->hub reply carrying the recovered challenge nonce.
type authResponse struct {
	Type  string `json:"type"` // "auth_response"
	KeyID string `json:"keyId"`
	Proof string `json:"proof"` // recovered nonce, base64 std
}

// authOK is the hub->phone confirmation sent after a verified response. It carries a
// short-lived /file session ticket; the phone treats receiving it as the signal that
// the link is authenticated and it may start sending app frames.
type authOK struct {
	Type   string `json:"type"` // "auth_ok"
	Ticket string `json:"ticket"`
}

func (h *Hub) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/agent", h.handleAgent)
	mux.HandleFunc("/ws", h.handlePhone)
	mux.HandleFunc("/file", h.handleFile)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("/machines", h.handleMachines)
	mux.HandleFunc("/api/activate", h.handleActivate)
	mux.HandleFunc("/api/roster", h.handleRoster)
	mux.HandleFunc("/api/roster/deactivate", h.handleRosterDeactivate)
	return mux
}

func tok() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		panic("hub: crypto/rand failed: " + err.Error()) // must not mint weak ids
	}
	return hex.EncodeToString(b)
}

const maxPhonesPerMachine = 8 // per-agent phone connection cap (DoS guard)

// Relay backpressure. The agent read loop hands frames to a per-phone writer
// goroutine instead of writing synchronously: a slow phone used to stall the
// loop inside writeRaw, and while stalled the hub serviced no pings (pongs only
// flow during an active Read), so the agent's keepalive timed out and the whole
// machine link died — kicking EVERY phone session — exactly when one phone
// opened a heavy transcript. The budget bounds per-phone memory; a consumer
// that falls beyond it is dropped alone, the machine link stays up.
const (
	phoneQueueSlots = 1024     // queued frame count per phone
	maxPhoneBacklog = 64 << 20 // queued bytes per phone before it's dropped as a slow consumer
)

func ctEq(a, b string) bool {
	ah := sha256.Sum256([]byte(a))
	bh := sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(ah[:], bh[:]) == 1
}

// ---- agent (machine) ----

type agent struct {
	id    string
	name  string
	token string
	cred  string // license mode: the credential this link registered with (for recheck)
	ws    *websocket.Conn
	wmu   sync.Mutex

	mu          sync.Mutex
	closed      bool // set under mu when the agent link tore down (reject late phone inserts)
	phones      map[string]*phone
	fileReqs    map[string]chan frame
	devices     map[string]string // keyId -> devicePub(b64); synced by the agent's "devices" frame
	connectedAt time.Time         // when this link registered (admin status display)
}

// setDevices replaces the agent's synced device public-key set (last-write-wins).
func (a *agent) setDevices(m map[string]string) {
	a.mu.Lock()
	a.devices = m
	a.mu.Unlock()
}

// devicePub returns the synced public key for keyId, or "" if unknown.
func (a *agent) devicePub(keyID string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.devices[keyID]
}

func (a *agent) write(ctx context.Context, f frame) error {
	wctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	a.wmu.Lock()
	defer a.wmu.Unlock()
	return wsjson.Write(wctx, a.ws, f)
}

// authAgent validates a register frame. License mode checks the per-machine
// credential against the roster (the shared agentKey is NOT accepted there);
// self-host mode keeps the constant-time shared-key check. Returns a human
// notice ("" if none) — non-empty during the expiry grace window.
func (h *Hub) authAgent(reg frame) (notice string, ok bool) {
	if h.Lic == nil {
		return "", ctEq(reg.AgentKey, h.agentKey)
	}
	res, err := h.Lic.CheckAgent(reg.MachineID, reg.Cred)
	if err != nil {
		log.Printf("hub: license register denied id=%q: %v", reg.MachineID, err)
		return "", false
	}
	h.Lic.TouchLastSeen(reg.MachineID)
	return res.Message, true
}

func (h *Hub) handleAgent(w http.ResponseWriter, r *http.Request) {
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	// Small read limit until authenticated (slow-loris / pre-auth memory guard);
	// raised after a valid register since file frames are large.
	ws.SetReadLimit(64 << 10)
	// Managed context: a hijacked WS's r.Context() may not cancel on close, so we
	// drive lifetime ourselves and cancel on handler return (stops keepalive).
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Bound the pre-auth window: a client that never sends register can't pin a
	// connection open indefinitely.
	var reg frame
	regCtx, regCancel := context.WithTimeout(ctx, 10*time.Second)
	err = wsjson.Read(regCtx, ws, &reg)
	regCancel()
	if err != nil {
		_ = ws.Close(websocket.StatusPolicyViolation, "no register")
		return
	}
	notice, ok := h.authAgent(reg)
	if reg.T != "register" || reg.MachineID == "" || reg.Token == "" || !ok {
		_ = wsjson.Write(ctx, ws, frame{T: "error", Message: "register rejected"})
		_ = ws.Close(websocket.StatusPolicyViolation, "register rejected")
		log.Printf("hub: agent register REJECTED id=%q from %s", reg.MachineID, clientIP(r))
		return
	}
	// Authenticated: allow large frames. 64MB matches the bridge's app-server
	// line cap — a thread frame the bridge can produce must be relayable here,
	// because exceeding the read limit doesn't degrade, it KILLS this link and
	// every phone session with it. (New bridges byte-bound thread frames to 8MB;
	// the headroom protects sessions served by not-yet-updated agents.)
	ws.SetReadLimit(64 << 20)

	a := &agent{
		id: reg.MachineID, name: reg.Name, token: reg.Token, cred: reg.Cred, ws: ws,
		phones: make(map[string]*phone), fileReqs: make(map[string]chan frame),
		devices: make(map[string]string), connectedAt: time.Now(),
	}
	h.mu.Lock()
	if old, exists := h.agents[a.id]; exists {
		// Same id is already online. The newcomer already proved possession of the
		// shared agentKey (above) and a unique per-machine token — so it IS this
		// machine reconnecting, not a rogue: take over. We evict the old link and
		// accept the new one. Without takeover, a half-open old conn that the hub
		// hasn't noticed yet wedges the id (every reconnect refused as "duplicate")
		// until keepalive finally times it out. Require a token match so a
		// same-id/different-token attempt is still refused (anti-hijack).
		if !ctEq(reg.Token, old.token) {
			h.mu.Unlock()
			_ = wsjson.Write(ctx, ws, frame{T: "error", Message: "machineId already registered"})
			_ = ws.Close(websocket.StatusPolicyViolation, "duplicate id")
			log.Printf("hub: duplicate register for id=%q rejected (token mismatch)", a.id)
			return
		}
		delete(h.agents, a.id)
		h.mu.Unlock()
		// Close the old ws OUTSIDE the lock: its read loop returns and its defer
		// cleans up its phones/file waiters. That defer's `h.agents[id] == old`
		// guard makes it a no-op against the new registration we install below.
		_ = old.ws.Close(websocket.StatusPolicyViolation, "superseded")
		log.Printf("hub: agent id=%q reconnected — evicted stale link (takeover)", a.id)
		h.mu.Lock()
	}
	// Reject a token already held by another machine — token uniquely identifies a
	// machine for routing, so a collision would route phones non-deterministically.
	for _, ex := range h.agents {
		if ctEq(reg.Token, ex.token) {
			h.mu.Unlock()
			_ = wsjson.Write(ctx, ws, frame{T: "error", Message: "token already in use"})
			_ = ws.Close(websocket.StatusPolicyViolation, "duplicate token")
			log.Printf("hub: register REJECTED id=%q duplicate token (held by id=%q)", a.id, ex.id)
			return
		}
	}
	h.agents[a.id] = a
	h.mu.Unlock()
	_ = a.write(ctx, frame{T: "registered"})
	if notice != "" {
		_ = a.write(ctx, frame{T: "notice", Message: notice})
	}
	log.Printf("hub: agent registered id=%q name=%q from %s", a.id, a.name, clientIP(r))
	go keepalive(ctx, ws) // keep the agent link alive through idle timeouts

	defer func() {
		h.mu.Lock()
		if h.agents[a.id] == a {
			delete(h.agents, a.id)
		}
		h.mu.Unlock()
		a.closeAllPhones()
		// Fail outstanding /file waiters fast (502) instead of letting them hit the
		// 30s timeout when the machine vanished mid-request.
		a.mu.Lock()
		reqs := a.fileReqs
		a.fileReqs = make(map[string]chan frame)
		a.mu.Unlock()
		for _, ch := range reqs {
			select {
			case ch <- frame{T: "file", Status: http.StatusBadGateway}:
			default:
			}
		}
		_ = ws.Close(websocket.StatusNormalClosure, "bye")
		log.Printf("hub: agent gone id=%q", a.id)
	}()

	for {
		var f frame
		if err := wsjson.Read(ctx, ws, &f); err != nil {
			return
		}
		switch f.T {
		case "msg":
			if p := a.phone(f.SID); p != nil {
				// Async handoff — never write synchronously here (see the
				// backpressure comment at phoneQueueSlots).
				if !p.enqueue(f.Data) {
					log.Printf("hub: phone sid=%s dropped — relay backlog over budget (slow consumer)", f.SID)
					a.dropPhone(f.SID, true)
				}
			}
		case "close":
			a.dropPhone(f.SID, true)
		case "devices":
			// Machine re-synced its paired devices' public keys. Replace the set
			// (last-write-wins); a tray revoke shrinks it and bars that phone next
			// connect. Public keys only — never a PSK — so the hub can authenticate
			// devices but never decrypt their traffic.
			var list []devicePubEntry
			if json.Unmarshal(f.Data, &list) == nil {
				m := make(map[string]string, len(list))
				for _, d := range list {
					if d.KeyID != "" && d.DevicePub != "" {
						m[d.KeyID] = d.DevicePub
					}
				}
				a.setDevices(m)
			}
		case "file":
			a.mu.Lock()
			ch := a.fileReqs[f.ReqID]
			a.mu.Unlock()
			if ch != nil {
				select {
				case ch <- f:
				default:
				}
			}
		}
	}
}

func (a *agent) phone(sid string) *phone {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.phones[sid]
}

func (a *agent) dropPhone(sid string, fromAgent bool) {
	a.mu.Lock()
	p := a.phones[sid]
	delete(a.phones, sid)
	a.mu.Unlock()
	if p != nil {
		_ = p.ws.Close(websocket.StatusNormalClosure, "closed")
	}
}

func (a *agent) closeAllPhones() {
	a.mu.Lock()
	a.closed = true // reject any phone insert racing after teardown
	ps := a.phones
	a.phones = make(map[string]*phone)
	a.mu.Unlock()
	for _, p := range ps {
		_ = p.ws.Close(websocket.StatusGoingAway, "machine offline")
	}
}

// ---- phone ----

type phone struct {
	sid    string
	ws     *websocket.Conn
	wmu    sync.Mutex
	out    chan json.RawMessage // agent→phone relay queue, drained by the writer goroutine
	queued int64                // bytes sitting in out (atomic) — slow-consumer budget
}

// enqueue hands data to the phone's writer goroutine without ever blocking the
// caller (the agent read loop). false = the phone is beyond its backlog budget
// and must be dropped.
func (p *phone) enqueue(data json.RawMessage) bool {
	if atomic.AddInt64(&p.queued, int64(len(data))) > maxPhoneBacklog {
		atomic.AddInt64(&p.queued, -int64(len(data)))
		return false
	}
	select {
	case p.out <- data:
		return true
	default: // queue slots exhausted (a flood of tiny frames): same verdict
		atomic.AddInt64(&p.queued, -int64(len(data)))
		return false
	}
}

func (p *phone) writeRaw(ctx context.Context, data json.RawMessage) error {
	wctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	p.wmu.Lock()
	defer p.wmu.Unlock()
	return p.ws.Write(wctx, websocket.MessageText, data)
}

// findAgent resolves the target machine. With an explicit machine id, it verifies
// the token matches that machine. Without one, it finds the (unique) machine whose
// token matches — so a phone can connect with just ?token=<machineToken>, no
// machine param, and the app needs no change. Returns nil if no match (caller 401s,
// which also avoids leaking whether a machine id exists).
func (h *Hub) findAgent(machine, token string) *agent {
	if token == "" {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if machine != "" {
		if a := h.agents[machine]; a != nil && ctEq(token, a.token) {
			return a
		}
		return nil
	}
	// Token-only: scan ALL agents (uniform timing, mirrors bridge authDevice) and
	// refuse to guess if more than one matches (tokens are unique per machine; a
	// collision is rejected at register, this is defense in depth).
	var match *agent
	n := 0
	for _, a := range h.agents {
		if ctEq(token, a.token) {
			match = a
			n++
		}
	}
	if n != 1 {
		return nil
	}
	return match
}

// agentByID returns the agent registered under an exact machine id, or nil. Used in
// device-auth mode, where the bearer token no longer selects the machine.
func (h *Hub) agentByID(id string) *agent {
	if id == "" {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.agents[id]
}

// agentByKeyID finds the unique machine that has synced a device with this keyId.
// keyIds are generated per pairing (globally unique), so a phone can connect with
// just ?keyId= and no machine param — mirroring legacy token-only routing, and
// letting the URL carry no reusable secret at all. Refuses to guess if zero or more
// than one match. Lock order h.mu -> a.mu matches every other call site.
func (h *Hub) agentByKeyID(keyID string) *agent {
	if keyID == "" {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	var match *agent
	n := 0
	for _, a := range h.agents {
		if a.devicePub(keyID) != "" {
			match = a
			n++
		}
	}
	if n != 1 {
		return nil
	}
	return match
}

// deviceAuth runs the proof-of-possession handshake on an already-accepted phone
// socket: seal a fresh random nonce to the device's synced public key, then require
// the phone to return the recovered nonce. Single-use and replay-proof by
// construction — the nonce is freshly generated per connection, exists only in this
// stack frame, and is read back under a short deadline (no shared nonce table to
// attack or replay against). Every failure path returns false uniformly, never
// revealing whether the keyId was unknown, the box unopenable, or the proof wrong.
func (h *Hub) deviceAuth(ctx context.Context, ws *websocket.Conn, a *agent, keyID string) bool {
	pubB64 := a.devicePub(keyID)
	if keyID == "" || pubB64 == "" {
		return false
	}
	pub, err := decodeKey(pubB64)
	if err != nil {
		return false
	}
	nonce := make([]byte, e2ee.ChallengeNonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return false
	}
	ephPub, sealed, err := e2ee.SealChallenge(nonce, pub)
	if err != nil {
		return false
	}
	wctx, wcancel := context.WithTimeout(ctx, 10*time.Second)
	werr := wsjson.Write(wctx, ws, authChallenge{
		Type:  "auth_challenge",
		EPK:   base64.StdEncoding.EncodeToString(ephPub[:]),
		Box:   base64.StdEncoding.EncodeToString(sealed),
		KeyID: keyID,
	})
	wcancel()
	if werr != nil {
		return false
	}
	rctx, rcancel := context.WithTimeout(ctx, 10*time.Second)
	var resp authResponse
	rerr := wsjson.Read(rctx, ws, &resp)
	rcancel()
	if rerr != nil || resp.Type != "auth_response" || resp.KeyID != keyID {
		return false
	}
	proof, err := base64.StdEncoding.DecodeString(resp.Proof)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(proof, nonce) == 1
}

// decodeKey parses a base64 std 32-byte X25519 key into a fixed array.
func decodeKey(b64 string) (*[32]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(raw) != 32 {
		return nil, errBadKey
	}
	var k [32]byte
	copy(k[:], raw)
	return &k, nil
}

var errBadKey = errors.New("hub: bad device key")

func (h *Hub) handlePhone(w http.ResponseWriter, r *http.Request) {
	machine := r.URL.Query().Get("machine")
	// Device-auth mode: the bearer ?token= no longer selects/authorizes a machine;
	// the phone proves possession of a registered device key AFTER the WS upgrade.
	// Legacy mode: the unique per-machine token both routes and authorizes.
	keyID := r.URL.Query().Get("keyId")
	var a *agent
	if h.RequireDeviceAuth {
		// The phone URL carries no machine= and no token — just ?keyId=. Route by the
		// globally-unique keyId (or by machine= if a client does send it).
		if machine != "" {
			a = h.agentByID(machine)
		} else {
			a = h.agentByKeyID(keyID)
		}
	} else {
		a = h.findAgent(machine, r.URL.Query().Get("token"))
	}
	if a == nil {
		log.Printf("hub: phone auth FAILED machine=%q from %s", machine, clientIP(r))
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if h.RequireDeviceAuth {
		ws.SetReadLimit(64 << 10) // small pre-auth guard until the device proves itself
		if !h.deviceAuth(ctx, ws, a, keyID) {
			log.Printf("hub: phone device-auth FAILED machine=%q keyId=%q from %s", machine, keyID, clientIP(r))
			_ = ws.Close(websocket.StatusPolicyViolation, "unauthorized")
			return
		}
		// Mint a short-lived, agent-scoped /file ticket so media fetch works without
		// the bearer token device-auth removed. Sent in auth_ok; dropped on disconnect.
		ticket := h.mintFileTicket(a.id)
		defer h.dropFileTicket(ticket)
		octx, ocancel := context.WithTimeout(ctx, 10*time.Second)
		oerr := wsjson.Write(octx, ws, authOK{Type: "auth_ok", Ticket: ticket})
		ocancel()
		if oerr != nil {
			_ = ws.Close(websocket.StatusPolicyViolation, "auth_ok failed")
			return
		}
	}

	// Phones send prompts carrying image data: URLs; the default 32KB read limit
	// would silently drop any real photo. Match the agent link's 32MB ceiling.
	ws.SetReadLimit(32 << 20)
	sid := tok()
	p := &phone{sid: sid, ws: ws, out: make(chan json.RawMessage, phoneQueueSlots)}
	// Insert + liveness/cap check atomically under a.mu (same lock closeAllPhones
	// takes), so a phone can't slip into a torn-down agent and leak, and a single
	// token can't fan out unbounded sessions on the machine.
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		_ = ws.Close(websocket.StatusGoingAway, "machine offline")
		return
	}
	if len(a.phones) >= maxPhonesPerMachine {
		a.mu.Unlock()
		_ = ws.Close(websocket.StatusTryAgainLater, "too many connections")
		return
	}
	a.phones[sid] = p
	a.mu.Unlock()
	// Writer goroutine: drains the relay queue onto this phone's link so the
	// AGENT read loop never blocks on a slow phone (see phoneQueueSlots). A
	// write error cancels ctx, which unblocks the read loop below; its defer
	// does the cleanup.
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case data := <-p.out:
				werr := p.writeRaw(ctx, data)
				atomic.AddInt64(&p.queued, -int64(len(data)))
				if werr != nil {
					cancel()
					return
				}
			}
		}
	}()
	_ = a.write(ctx, frame{T: "open", SID: sid})
	log.Printf("hub: phone connected sid=%s machine=%q from %s", sid, machine, clientIP(r))
	go keepalive(ctx, ws) // keep idle phone link alive (backgrounded app)

	defer func() {
		a.dropPhone(sid, false)
		_ = a.write(context.Background(), frame{T: "close", SID: sid})
		_ = ws.Close(websocket.StatusNormalClosure, "bye")
	}()

	for {
		_, data, err := ws.Read(ctx)
		if err != nil {
			return
		}
		if err := a.write(ctx, frame{T: "msg", SID: sid, Data: data}); err != nil {
			return
		}
	}
}

// ---- /file proxy ----

func (h *Hub) handleFile(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	var a *agent
	if h.RequireDeviceAuth {
		// device-auth: a short-lived session ticket (minted at ws auth) stands in for
		// the bearer token, which was removed from the URL.
		if id := h.resolveFileTicket(r.URL.Query().Get("ticket")); id != "" {
			a = h.agentByID(id)
		}
	} else {
		a = h.findAgent(r.URL.Query().Get("machine"), r.URL.Query().Get("token"))
	}
	if a == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	reqID := tok()
	ch := make(chan frame, 1)
	a.mu.Lock()
	a.fileReqs[reqID] = ch
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		delete(a.fileReqs, reqID)
		a.mu.Unlock()
	}()

	if err := a.write(r.Context(), frame{T: "file", ReqID: reqID, Path: path}); err != nil {
		http.Error(w, "machine unreachable", http.StatusBadGateway)
		return
	}
	select {
	case f := <-ch:
		if f.Status != http.StatusOK {
			http.Error(w, http.StatusText(f.Status), f.Status)
			return
		}
		b, err := base64.StdEncoding.DecodeString(f.B64)
		if err != nil {
			http.Error(w, "bad media", http.StatusBadGateway)
			return
		}
		if f.Ctype != "" {
			w.Header().Set("Content-Type", f.Ctype)
		}
		w.Header().Set("Cache-Control", "private, max-age=300")
		_, _ = w.Write(b)
	case <-time.After(30 * time.Second):
		http.Error(w, "media timeout", http.StatusGatewayTimeout)
	case <-r.Context().Done():
	}
}

// handleMachines lists currently-online machine ids+names (no tokens). Handy for
// debugging / a future app picker; does not expose secrets.
func (h *Hub) handleMachines(w http.ResponseWriter, r *http.Request) {
	// Gate behind the agent key so the public endpoint can't enumerate online
	// machine ids/names. 404 (not 401) to avoid confirming the route exists.
	if !ctEq(r.URL.Query().Get("key"), h.agentKey) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	h.mu.Lock()
	type m struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	out := make([]m, 0, len(h.agents))
	for _, a := range h.agents {
		out = append(out, m{ID: a.id, Name: a.name})
	}
	h.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// keepalive pings the WebSocket every 25s so idle agent/phone links aren't reaped
// by Cloudflare/nginx/proxy idle timeouts (observed ~60s). Returns when ctx is
// done or the peer is unreachable. coder/websocket auto-replies to pings, so this
// alone keeps both directions warm.
func keepalive(ctx context.Context, ws *websocket.Conn) {
	t := time.NewTicker(25 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := ws.Ping(pctx)
			cancel()
			if err != nil {
				// Ping timed out / failed: the link is dead or half-open (peer
				// vanished without a FIN). Close it so the agent's blocked Read
				// returns and its defer clears the machineId registration —
				// otherwise a stale entry lingers and every reconnect is refused
				// as "duplicate id" (the half-open deadlock).
				_ = ws.Close(websocket.StatusGoingAway, "ping timeout")
				return
			}
		}
	}
}

func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return xff
	}
	return r.RemoteAddr
}
