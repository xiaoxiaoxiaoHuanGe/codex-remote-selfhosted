// Package bridge exposes the Codex app-server to a phone client over a simple
// JSON-over-WebSocket protocol. It holds ONE appserver.Client and fans out its
// notifications to every connected phone client. That client drives a SEPARATE
// `codex app-server` child process — it shares ~/.codex on disk with the desktop
// Codex app, but is NOT the same process — so the bridge only ever sees approval
// requests for turns it initiated itself (which are therefore always owned).
//
// Phone protocol (text frames, JSON objects with a "type" field):
//
//	client -> server:
//	  {"type":"list"}
//	  {"type":"read","threadId":"...","limit":40,"before":70}
//	      ^ limit>0 → at most N turns per frame; before>0 → window ends just before
//	        that turn index (cursor paging into the past; pass the previous frame's
//	        offset). Every frame is byte-bounded server-side (~8MB) regardless.
//	  {"type":"prompt","threadId":"...","text":"...","cwd":"..."}   // threadId optional
//	  {"type":"interrupt","threadId":"...","turnId":"..."}  // turnId optional (tracked)
//	  {"type":"approvalDecision","id":"...","decision":"accept|decline"}
//	  {"type":"registerPush","pushToken":"...","platform":"ios"}  // background push
//	  {"type":"sync"}                                              // re-fetch state on resume
//
//	server -> client:
//	  {"type":"sessions","data":[...]}
//	  {"type":"thread","threadId":"...","thread":{...},"offset":70,"truncated":true,"total":120,"page":true}
//	      ^ offset = index of the window's first turn (pass back as `before` to page
//	        earlier; >0 ⇒ truncated/total present). page only on before>0 responses:
//	        the client grafts the window in FRONT of its transcript instead of replacing it.
//	      ^ large inline base64 images are stripped: imageGeneration gains resultPath, data:image
//	        image_url/imageUrl become absolute paths — clients fetch them lazily via /file
//	  {"type":"promptAccepted","threadId":"..."}
//	  {"type":"event","method":"...","params":{...}}    // forwarded app-server notifications
//	  {"type":"approval","id":"...","method":"...","params":{...}}  // re-sent on reconnect
//	  {"type":"pushRegistered"}
//	  {"type":"error","message":"..."}
//	  {"type":"lanInfo","candidates":["ip:port",...],"pub":bool}
//	      — pushed once per fresh session (alongside the approval resync): the
//	        machine's private-IPv4 direct-connect candidates (empty list = LAN
//	        listener off; authoritative, the phone clears its cache) and whether
//	        the relay tier is active (UX hint only — the hub enforces the tier).
//
// SECURITY MODEL (this bridge may be exposed to the public internet via a relay):
//   - Auth: every /ws and /file request is gated by a pairing token compared in
//     constant time (SHA-256). An empty token rejects everything. Multiple named
//     device tokens are supported (AddDeviceToken) so a lost device can be revoked
//     individually instead of rotating one shared secret. In hub/relay mode the hub
//     can instead require a device PUBLIC-KEY challenge-response (HUB_REQUIRE_DEVICE_AUTH):
//     under E2EE the bridge syncs paired devices' public keys to the hub (DevicePubs,
//     PSK never leaves the bridge), and the hub challenges each phone to prove
//     possession of its key — removing the reusable bearer token from the URL.
//   - Turn clamp: phone-initiated turns are pinned server-side to a
//     workspace-write sandbox (networkAccess:false) regardless of the phone's
//     request; danger-full-access / never are refused (remoteTurnPolicy). The
//     per-command approval gate stays on (approvalPolicy=on-request) by default;
//     the looser on-failure ("full") and the auto_review reviewer ("auto") modes
//     are NOT a client choice — auto_review requires the operator-side opt-in
//     CODEX_ALLOW_AUTO_REVIEW=1, otherwise it is downgraded to a human gate.
//   - cwd clamp: a phone-supplied cwd (the workspace-write root) is validated
//     (validateCwd): absolute, existing dir, inside CODEX_WORKSPACE_ROOTS (default
//     $HOME), and NOT the home root or a sensitive subtree (.ssh/.aws/.codex/…),
//     so a remote turn can't redefine the writable root to creds/config dirs.
//   - Approval clamp: the phone's accept/decline is mapped to the exact
//     protocol-valid decision; persistent variants (acceptForSession /
//     approved_for_session / *_amendment) are NEVER forwarded, so a single
//     approval can't disable the per-command gate for the rest of the session.
//   - Approval-ownership binding: an approval is routed to, and answerable only
//     by, the device whose turn triggered it (keyed on device so it survives
//     reconnect). NOTE: in hub/relay mode every session shares device="phone"
//     (agent.go), so this binding is only meaningful in direct LAN mode.
//   - /file: token-gated, restricted to a small media-root allowlist (NOT bare
//     ~/.codex or all of ~/Documents) AND gated by both a media-extension
//     allowlist and a magic-byte content sniff, so a non-media file renamed to a
//     media extension is still rejected. This narrows — but does not by itself
//     eliminate — its use as a media-exfil channel for a token holder.
//   - LAN listener (agent mode, CODEX_LAN_ADDR, default :8767, "off"
//     disables): serves this same Handler on the LAN. Identical token gate
//     and clamps — no separate code path, no weakened invariant.
//   - DoS guard: concurrent connections are capped.
//   - Audit: connects, auth failures, prompts, approvals + decisions are logged
//     with an `audit:` prefix for post-deployment review.
package bridge

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/yunyuchen/codex-remote/bridge/internal/appserver"
	"github.com/yunyuchen/codex-remote/bridge/internal/devstore"
)

type Server struct {
	cx     *appserver.Client
	tokens map[string]string // pairing token -> device name (set at startup, then read-only)

	mu      sync.Mutex
	clients map[*conn]struct{}
	maxConn int // cap concurrent phone connections (DoS guard)

	apMu      sync.Mutex
	approvals map[string]*pendingApproval
	apTimeout time.Duration // how long to wait for a phone approval decision

	rmu     sync.Mutex
	resumed map[string]bool // threadIds already loaded into our app-server

	tmu       sync.Mutex
	turns     map[string]string // threadId -> active turnId (for turn/interrupt)
	turnOwner map[string]string // threadId -> device that started the turn (approval binding)

	pushMu sync.Mutex
	pushes map[string]PushReg // dedup device registrations by token
	pusher Pusher             // sends background notifications (no-op by default)

	fileRoots []string // allowlisted roots for the /file media endpoint
	// mediaCache is where stripInlineMedia stages base64 images pulled out of
	// thread frames for lazy /file fetch (within fileRoots; "" disables strip).
	mediaCache string

	// modelCatalog is refreshed from this bridge's app-server on a phone request.
	// Until that succeeds it contains only the legacy compatibility fallback.
	modelMu      sync.RWMutex
	modelCatalog map[string]modelCapability

	// allowAutoReview gates the approval="auto" → auto_review path. Default false:
	// a client can NEVER swap the human approval gate for an automated reviewer.
	// The machine operator opts in with CODEX_ALLOW_AUTO_REVIEW=1.
	allowAutoReview bool

	// E2EE control-plane state. All three are nil/unset unless EnableE2EE was
	// called at startup; e2eeOn() keys off devices != nil. When unset the bridge
	// speaks legacy cleartext (the read/write seams pass frames through). When set,
	// the relay only ever sees ciphertext and the first-frame rule is enforced.
	devices   *devstore.Store        // keyId -> paired Device (hot-reloaded by Watch)
	enrollSec *[32]byte              // bridge enrollment secret; seals PairPayloads
	pending   *devstore.PendingStore // one-time pairing ids (single-use consume)

	// devicesChanged coalesces "the device registry changed" signals (buffered 1)
	// so agent mode can re-sync device PUBLIC keys to the hub on a tray pair/revoke.
	// Set by EnableE2EE; nil when E2EE is off (NotifyDevicesChanged is then a no-op).
	devicesChanged chan struct{}

	// LAN direct-connect state. lanPort is the port StartLANListener bound (""
	// = no listener → lanInfo advertises no candidates). pubEnabled tells
	// phones whether this machine's relay tier is active (license cred or
	// agent key present) — UX only, the hub enforces the tier server-side.
	// lanCands is swappable for tests (defaults to LANCandidates).
	lanMu      sync.Mutex
	lanPort    string
	pubEnabled bool
	lanCands   func(port string) []string
}

// pendingApproval keeps an in-flight approval request around so it can be
// re-surfaced to a phone that reconnects after backgrounding (Phase 5 resync).
// ownerDevice is the device whose turn triggered it: only that device may answer
// it, and the prompt is routed only to it (Phase 6 approval-ownership binding).
type pendingApproval struct {
	id          string
	method      string
	params      json.RawMessage
	ownerDevice string
	ch          chan string
}

// PushReg is one device registered to receive background push notifications.
type PushReg struct {
	Token    string `json:"token"`
	Platform string `json:"platform"` // "ios" | "android"
}

// Notification is a provider-agnostic background alert. The concrete Pusher
// (APNs/FCM, added later) maps this onto its wire format.
type Notification struct {
	Kind     string // "approval" | "turnDone"
	Title    string
	Body     string
	ThreadID string
}

// Pusher delivers a Notification to registered devices when the app isn't in the
// foreground. Implementations: LogPusher (default no-op), and later APNs/FCM.
type Pusher interface {
	Push(regs []PushReg, n Notification)
}

// LogPusher is the placeholder Pusher: it only logs what WOULD be sent, so the
// trigger plumbing is exercisable before a real APNs/FCM channel is wired in.
type LogPusher struct{}

func (LogPusher) Push(regs []PushReg, n Notification) {
	log.Printf("push[%s] %q -> %q to %d device(s) (no channel configured)", n.Kind, n.Title, n.Body, len(regs))
}

// mediaExts is the allowlist of file extensions the /file endpoint will serve.
// Anything else (creds, config, source, db, archives) is rejected even inside an
// allowed root — defense against the endpoint becoming a file-exfil channel.
var mediaExts = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true,
	".bmp": true, ".heic": true, ".heif": true,
	".mp4": true, ".mov": true, ".webm": true, ".m4v": true,
}

// conn is one phone session. It's transport-agnostic: in direct-serve mode write
// goes to a WebSocket; in hub-agent mode it's framed back to the hub. Server.handle
// and the broadcast/approval machinery only ever touch push()/id/device, so the
// same logic drives both transports.
type conn struct {
	send      chan any
	done      chan struct{}
	closeOnce sync.Once
	id        string                                 // short session id, for audit logs
	device    string                                 // device name (pairing token / hub session)
	write     func(ctx context.Context, v any) error // transport-specific frame writer
	onDead    func()                                 // transport cleanup when writePump dies (nil = none)
	dropOnce  sync.Once                              // log the first silent push drop per conn

	// E2EE per-connection codec state. wantE2EE is fixed at accept time from
	// s.e2eeOn(); the rest are armed once bindConn resolves the first frame's keyId
	// to a paired device. seqIn/seqOut are touched only by the single read-loop /
	// writePump goroutine respectively, so they need no lock. armed is closed once
	// by bindConn — closing it AFTER the key fields are set publishes them to the
	// writePump goroutine (happens-before), so sealAndWrite never races bindConn.
	wantE2EE  bool          // this transport expects E2EE (vs legacy cleartext)
	e2ee      bool          // codec armed: keyID/kP2B/kB2P are valid
	keyID     string        // bound device keyId (also the envelope's plaintext k)
	kP2B      []byte        // phone->bridge subkey (Open inbound)
	kB2P      []byte        // bridge->phone subkey (Seal outbound)
	seqIn     uint64        // next expected inbound seq (replay/reorder defense)
	seqOut    uint64        // next outbound seq
	armed     chan struct{} // closed once when the codec arms
	armedOnce sync.Once
}

// stop signals writePump to exit. Idempotent. We never close c.send (push() would
// panic on a closed channel if a handle() goroutine is still in flight).
func (c *conn) stop() { c.closeOnce.Do(func() { close(c.done) }) }

type inbound struct {
	Type      string   `json:"type"`
	ThreadID  string   `json:"threadId"`
	TurnID    string   `json:"turnId"`
	Text      string   `json:"text"`
	Images    []string `json:"images"` // prompt: image data: URLs (data:image/...;base64,...)
	Cwd       string   `json:"cwd"`
	ID        string   `json:"id"`
	Decision  string   `json:"decision"`
	PushToken string   `json:"pushToken"` // registerPush: device push token
	Platform  string   `json:"platform"`  // registerPush: "ios" | "android"
	// prompt turn-hints from the phone composer (optional; clamped server-side).
	Effort   string `json:"effort"`   // low | medium | high | xhigh
	Model    string `json:"model"`    // exact model id returned by app-server model/list
	Speed    string `json:"speed"`    // fast | standard
	Approval string `json:"approval"` // default | auto | full | custom
	// read: turn-window bounds. Limit>0 → at most N turns per frame so a heavy
	// session never produces an oversized frame; 0/absent → full history intent
	// (still byte-bounded server-side). Before>0 → the window ENDS just before
	// that turn index (cursor paging into the past; pass the offset from the
	// previous thread frame). 0/absent → the newest window.
	Limit  int `json:"limit"`
	Before int `json:"before"`
}

// NewServer wires the shared app-server client's notification + approval
// callbacks into the broadcast/approval machinery. token is the default device's
// pairing token (named "default"); add more named device tokens with
// AddDeviceToken before serving.
func NewServer(cx *appserver.Client, token string) *Server {
	s := &Server{
		cx:           cx,
		tokens:       make(map[string]string),
		clients:      make(map[*conn]struct{}),
		maxConn:      8,
		approvals:    make(map[string]*pendingApproval),
		apTimeout:    180 * time.Second,
		resumed:      make(map[string]bool),
		turns:        make(map[string]string),
		turnOwner:    make(map[string]string),
		pushes:       make(map[string]PushReg),
		pusher:       LogPusher{},
		fileRoots:    mediaRoots(),
		mediaCache:   mediaCacheDir(),
		modelCatalog: fallbackModelCapabilities(),
		// Operator-side opt-in (env, never a client field): only when explicitly
		// enabled may a remote turn use the auto_review reviewer instead of a human.
		allowAutoReview: os.Getenv("CODEX_ALLOW_AUTO_REVIEW") == "1",
		lanCands:        LANCandidates,
	}
	if token != "" {
		s.tokens[token] = "default"
	}
	cx.OnNotification = s.onNotification
	cx.OnServerRequest = s.onApproval
	return s
}

// AddDeviceToken registers an additional named device pairing token. Call only at
// startup (before Handler is served); the token map is read-only thereafter.
// Revoke a device by removing its token from config and restarting.
func (s *Server) AddDeviceToken(name, token string) {
	if name == "" || token == "" {
		return
	}
	// Detect collisions: the map is keyed by token, so reusing a token value (or
	// a device token equal to the default) would silently collapse entries and
	// defeat per-device revocation. Warn and keep the first registration.
	if existing, dup := s.tokens[token]; dup {
		s.audit("WARNING device %q token collides with %q; ignoring (per-device revocation needs distinct tokens)", name, existing)
		return
	}
	s.tokens[token] = name
}

// audit logs a security-relevant event with a stable, grep-able prefix so an
// operator can review who connected and what was approved once public.
func (s *Server) audit(format string, args ...any) {
	log.Printf("audit: "+format, args...)
}

// SetPusher swaps the background-notification channel (e.g. an APNs/FCM sender).
// Passing nil restores the no-op LogPusher.
func (s *Server) SetPusher(p Pusher) {
	if p == nil {
		p = LogPusher{}
	}
	s.pushMu.Lock()
	s.pusher = p
	s.pushMu.Unlock()
}

// mediaRoots are the directories the /file endpoint is allowed to serve from.
// It MIRRORS workspaceRoots() — the same boundary a phone-driven turn's cwd is
// already clamped to (validateCwd). Rationale: the phone can already make codex
// read/write anything inside the workspace, so letting /file return an *image*
// from that same tree adds no reach — it just removes the asymmetry where a
// session's own screenshot was un-viewable. This collapses the security surface
// to a SINGLE knob: CODEX_WORKSPACE_ROOTS narrows both "what the phone can
// operate on" and "what images it can see" together. Widening /file beyond the
// workspace would expose files the phone otherwise couldn't reach, so we don't.
//
// On top of the workspace we add the Codex media output subdirs under ~/.codex
// (generated images / computer-use captures) — these live OUTSIDE a typical
// workspace but are legitimate session media — plus any explicit CODEX_MEDIA_ROOTS
// (os.PathListSeparator-separated absolute paths). NEVER the bare ~/.codex
// (auth.json/OAuth tokens, config.toml, session DBs).
//
// The load-bearing exfil controls are NOT the root list: resolveMedia also gates
// every request by a media extension allowlist AND a magic-byte content sniff, so
// /file can only ever emit real image/video bytes — never credentials, source,
// or databases — regardless of how wide the roots are.
func mediaRoots() []string {
	// Same boundary the phone can already operate within.
	roots := append([]string{}, workspaceRoots()...)
	// Operator extension: explicit absolute roots, symlink-resolved.
	for _, p := range filepath.SplitList(os.Getenv("CODEX_MEDIA_ROOTS")) {
		if p == "" {
			continue
		}
		if real, err := filepath.EvalSymlinks(p); err == nil {
			roots = append(roots, real)
		}
	}
	// Codex media output dirs under ~/.codex: genuine session media that sits
	// outside the workspace (and survives a narrowed CODEX_WORKSPACE_ROOTS).
	if home, err := os.UserHomeDir(); err == nil {
		for _, d := range []string{
			".codex/generated_images", // Codex image-generation keyframes/output
			".codex/computer-use",     // Codex computer-use captures
		} {
			p := filepath.Join(home, filepath.FromSlash(d))
			if real, err := filepath.EvalSymlinks(p); err == nil {
				roots = append(roots, real)
			}
		}
	}
	// Attachments captured from the phone are staged under the OS temp dir by the
	// remote bridge flow. They are not inside a project workspace, but they are
	// first-class conversation media and still pass the extension + magic sniff.
	if real, err := filepath.EvalSymlinks(filepath.Join(os.TempDir(), "codex-remote-attachments")); err == nil {
		roots = append(roots, real)
	}
	// The inline-media staging cache (stripInlineMedia): thread frames swap big
	// base64 images for paths in here; every fetch still passes /file's
	// extension + magic-byte gates, and only sniffed-as-image bytes are staged.
	if d := mediaCacheDir(); d != "" {
		roots = append(roots, d)
	}
	return roots
}

// NewToken returns a random hex pairing token.
func NewToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// authDevice matches the request's ?token= against every configured device token
// in constant time (SHA-256 digests, no length oracle, no early-exit timing leak)
// and returns the matched device name. No configured tokens => rejects everything;
// the bridge must never run unauthenticated behind a relay.
func (s *Server) authDevice(r *http.Request) (string, bool) {
	got := sha256.Sum256([]byte(r.URL.Query().Get("token")))
	device, ok := "", false
	for tok, name := range s.tokens {
		want := sha256.Sum256([]byte(tok))
		if subtle.ConstantTimeCompare(got[:], want[:]) == 1 {
			device, ok = name, true // keep scanning all tokens (uniform timing)
		}
	}
	return device, ok
}

// tokenOK is the boolean form of authDevice (used where the device name isn't needed).
func (s *Server) tokenOK(r *http.Request) bool {
	_, ok := s.authDevice(r)
	return ok
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", s.handleWS)
	mux.HandleFunc("/file", s.handleFile)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) })
	return mux
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	device, ok := s.authDevice(r)
	if !ok {
		s.audit("ws auth FAILED from %s", clientIP(r))
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	// Cap concurrent connections so a flood can't exhaust the Mac / shared
	// app-server. (Origin check stays disabled: the native phone client sends no
	// Origin, and auth is the token in the query — not an ambient cookie — so
	// cross-site WebSocket hijacking doesn't apply here.)
	s.mu.Lock()
	atCap := len(s.clients) >= s.maxConn
	s.mu.Unlock()
	if atCap {
		s.audit("ws rejected (at cap) device=%s from %s", device, clientIP(r))
		http.Error(w, "too many connections", http.StatusServiceUnavailable)
		return
	}
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	c := &conn{
		send:     make(chan any, 256),
		done:     make(chan struct{}),
		id:       NewToken()[:8],
		device:   device,
		write:    func(ctx context.Context, v any) error { return wsjson.Write(ctx, ws, v) },
		wantE2EE: s.e2eeOn(),
		armed:    make(chan struct{}),
	}
	s.mu.Lock()
	s.clients[c] = struct{}{}
	n := len(s.clients)
	s.mu.Unlock()
	s.audit("ws connected conn=%s device=%s from %s (%d total)", c.id, device, clientIP(r), n)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.writePump(ctx)

	// Resync: a phone reconnecting after backgrounding may have missed an approval
	// prompt (the send channel drops on overflow, and a closed socket loses
	// everything). Re-surface its still-pending approvals (its own, or unowned).
	// Under E2EE this MUST wait until the codec arms (else it would queue cleartext
	// before a key is bound) — routeInbound calls resyncTo after the first envelope.
	if !s.e2eeOn() {
		s.resyncTo(c)
		s.sendLanInfo(c)
	}

	defer func() {
		s.mu.Lock()
		delete(s.clients, c)
		s.mu.Unlock()
		c.stop()
		_ = ws.Close(websocket.StatusNormalClosure, "bye")
		s.audit("ws disconnected conn=%s device=%s", c.id, c.device)
	}()

	for {
		// Read raw bytes and route through the E2EE seam: routeInbound decrypts an
		// envelope, handles a plaintext pair_init inline, or — in legacy cleartext
		// mode — unmarshals directly. ANY decode/decrypt/pairing failure returns an
		// error and we disconnect (the defer tears down); we never fall through to
		// processing an unverified frame.
		_, data, err := ws.Read(ctx)
		if err != nil {
			return
		}
		in, err := s.routeInbound(ctx, c, data)
		if err != nil {
			s.audit("ws closing conn=%s: %v", c.id, err)
			return
		}
		if in == nil {
			continue // control frame (pairing) handled inline; nothing to dispatch
		}
		go s.handle(ctx, c, *in)
	}
}

// handleFile serves an image/video file referenced by an absolute path, gated by
// the pairing token, restricted to allowlisted roots, AND restricted to media
// file extensions. The extension allowlist is the load-bearing control: even
// inside an allowed root it refuses to serve credentials, config, source, or DBs,
// so /file can never become a file-exfiltration channel. ServeFile adds
// Content-Type + Range support so video seeking works.
func (s *Server) handleFile(w http.ResponseWriter, r *http.Request) {
	if !s.tokenOK(r) {
		s.audit("file auth FAILED from %s path=%q", clientIP(r), trunc(r.URL.Query().Get("path"), 256))
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	real, code := s.resolveMedia(r.URL.Query().Get("path"))
	if code != http.StatusOK {
		http.Error(w, http.StatusText(code), code)
		return
	}
	http.ServeFile(w, r, real)
}

// resolveMedia validates a media path request (non-empty, NUL-free, absolute,
// media extension, symlink-resolved to a regular file inside an allowed root) and
// returns the safe real path, or "" plus an HTTP status code on rejection. Shared
// by /file (direct serve) and the hub agent's file handler so both enforce the
// SAME allowlist — the load-bearing control against credential/source exfil.
func (s *Server) resolveMedia(rawPath string) (string, int) {
	if rawPath == "" {
		return "", http.StatusBadRequest
	}
	if strings.ContainsRune(rawPath, 0) { // reject NUL-byte tricks
		return "", http.StatusBadRequest
	}
	abs, err := filepath.Abs(filepath.Clean(rawPath))
	if err != nil || !filepath.IsAbs(abs) {
		return "", http.StatusBadRequest
	}
	if !mediaExts[strings.ToLower(filepath.Ext(abs))] {
		return "", http.StatusForbidden
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", http.StatusNotFound
	}
	if !mediaExts[strings.ToLower(filepath.Ext(real))] {
		return "", http.StatusForbidden
	}
	if fi, err := os.Stat(real); err != nil || !fi.Mode().IsRegular() {
		return "", http.StatusNotFound
	}
	if !s.pathAllowed(real) {
		return "", http.StatusForbidden
	}
	// Content sniff: the extension allowlist is filename-only, so a non-media file
	// (creds, source, a renamed secret) copied to a .png/.mp4 name would otherwise
	// be served verbatim. Require the leading bytes to match a known image/video
	// signature so only genuine media leaves the box.
	if !fileLooksLikeMedia(real) {
		return "", http.StatusForbidden
	}
	return real, http.StatusOK
}

// fileLooksLikeMedia opens path and checks its leading bytes against the media
// magic numbers we serve. Fail-closed: any read error => treated as not-media.
func fileLooksLikeMedia(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var hdr [16]byte
	n, _ := io.ReadFull(f, hdr[:])
	return looksLikeMedia(hdr[:n])
}

// looksLikeMedia reports whether b begins with a signature of one of the formats
// in mediaExts: PNG/JPEG/GIF/WEBP/BMP and the ISO-BMFF family (mp4/mov/m4v/heic/
// heif) plus Matroska/WEBM. Go's http.DetectContentType can't recognize HEIC/MOV,
// so we match the magic bytes directly.
func looksLikeMedia(b []byte) bool {
	switch {
	case len(b) >= 8 && bytes.Equal(b[:8], []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}): // PNG
		return true
	case len(b) >= 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF: // JPEG
		return true
	case len(b) >= 6 && (bytes.Equal(b[:6], []byte("GIF87a")) || bytes.Equal(b[:6], []byte("GIF89a"))): // GIF
		return true
	case len(b) >= 12 && bytes.Equal(b[:4], []byte("RIFF")) && bytes.Equal(b[8:12], []byte("WEBP")): // WEBP
		return true
	case len(b) >= 2 && b[0] == 'B' && b[1] == 'M': // BMP
		return true
	case len(b) >= 8 && bytes.Equal(b[4:8], []byte("ftyp")): // ISO-BMFF: mp4/mov/m4v/heic/heif
		return true
	case len(b) >= 4 && bytes.Equal(b[:4], []byte{0x1A, 0x45, 0xDF, 0xA3}): // Matroska/WEBM (EBML)
		return true
	}
	return false
}

func (s *Server) pathAllowed(p string) bool {
	for _, root := range s.fileRoots {
		if p == root || strings.HasPrefix(p, root+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

func (c *conn) writePump(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.done:
			return
		case msg := <-c.send:
			// Under E2EE, hold every outbound frame until the codec arms so nothing
			// is emitted as cleartext before a key is bound. armed is closed once by
			// bindConn (after the key fields are set), so receiving from it also
			// publishes those fields to this goroutine — sealAndWrite then reads them
			// race-free. Once armed, this select returns immediately every iteration.
			if c.wantE2EE {
				select {
				case <-c.armed:
				case <-ctx.Done():
					return
				case <-c.done:
					return
				}
			}
			wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := c.sealAndWrite(wctx, msg)
			cancel()
			if err != nil {
				// A dead pump must not leave a zombie session: in agent mode the
				// conn has no transport-coupled read loop, so without cleanup the
				// bridge keeps handling reads and push()ing replies into a channel
				// nobody drains — audit logs look healthy while the phone gets
				// nothing. Tear the session down so the phone reconnects. But stay
				// silent during a NORMAL teardown (parent ctx cancelled / session
				// stopped): a context-cancellation "failure" there is expected and
				// would otherwise spam misleading reap audits on every reconnect.
				select {
				case <-ctx.Done():
				case <-c.done:
				default:
					log.Printf("bridge: writePump dead for conn %s: %v", c.id, err)
					if c.onDead != nil {
						go c.onDead()
					}
				}
				return
			}
		}
	}
}

// addConn / removeConn register a phone session (used by the hub-agent transport;
// handleWS manages its own membership inline so it can log the live count).
func (s *Server) addConn(c *conn) {
	s.mu.Lock()
	s.clients[c] = struct{}{}
	s.mu.Unlock()
}

func (s *Server) removeConn(c *conn) {
	s.mu.Lock()
	delete(s.clients, c)
	s.mu.Unlock()
}

func (c *conn) push(v any) {
	select {
	case c.send <- v:
	default:
		// Drop if the client is too slow; resync handled in Phase 5. Log the
		// first drop per conn — a full buffer usually means the writePump died
		// or the link is badly backpressured, and a fully silent drop made
		// "bridge replied but phone got nothing" undiagnosable from logs.
		c.dropOnce.Do(func() {
			log.Printf("bridge: send buffer full for conn %s — dropping frames", c.id)
		})
	}
}

func (s *Server) broadcast(v any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.clients {
		c.push(v)
	}
}

// Thread-frame byte budget. The hub reads agent frames with a 64MB hard cap and
// the phone parses the whole frame before first paint — stay well under both.
// Exceeding the relay's read limit doesn't degrade: it kills the machine link.
const (
	maxThreadBytes     = 8 << 20  // target ceiling per thread frame (turn window shrinks to fit)
	hardMaxThreadBytes = 24 << 20 // even one turn over this → refuse the read with an error
)

// windowThread slices a thread/read result to a turn window so any single
// frame stays bounded: the window ENDS just before turn index `before`
// (before<=0 or out of range → the newest turn) and keeps at most `limit`
// turns (limit<=0 → from the beginning). Returns the sliced JSON, the total
// turn count, and the offset — the index of the first returned turn; offset>0
// means earlier history exists, and a client passes it back as `before` to
// page further into the past. The turns array lives either at the top level or
// nested under "thread"; either way we only re-shape that one field.
// Fail-soft: on any structural surprise or marshal error we return the
// original raw untouched (never ship a broken/empty thread).
func windowThread(raw json.RawMessage, before, limit int) (json.RawMessage, int, int) {
	var top map[string]any
	if err := json.Unmarshal(raw, &top); err != nil {
		return raw, 0, 0
	}
	holder := top
	if t, ok := top["thread"].(map[string]any); ok {
		if _, has := t["turns"]; has {
			holder = t
		}
	}
	turns, ok := holder["turns"].([]any)
	if !ok {
		return raw, 0, 0
	}
	total := len(turns)
	end := total
	if before > 0 && before <= total {
		end = before
	}
	start := 0
	if limit > 0 && end-limit > 0 {
		start = end - limit
	}
	if start == 0 && end == total {
		return raw, total, 0 // full passthrough
	}
	holder["turns"] = turns[start:end]
	out, err := json.Marshal(top)
	if err != nil {
		return raw, total, 0
	}
	return out, total, start
}

// tailThread is the before-less window: the last `limit` turns (the phone's
// initial open). Kept as a named seam for its tests and readability.
func tailThread(raw json.RawMessage, limit int) (json.RawMessage, bool, int) {
	out, total, offset := windowThread(raw, 0, limit)
	return out, offset > 0, total
}

func threadStats(raw json.RawMessage) (int, string) {
	var top any
	if err := json.Unmarshal(raw, &top); err != nil {
		return 0, "unparseable"
	}
	turns := 0
	hist := map[string]int{}
	var walk func(any, int)
	walk = func(v any, depth int) {
		if depth > 8 {
			return
		}
		switch x := v.(type) {
		case map[string]any:
			if t, ok := x["type"].(string); ok {
				hist[t]++
			}
			if arr, ok := x["turns"].([]any); ok && turns == 0 {
				turns = len(arr)
			}
			for _, child := range x {
				walk(child, depth+1)
			}
		case []any:
			for _, child := range x {
				walk(child, depth+1)
			}
		}
	}
	walk(top, 0)
	keys := []string{"userMessage", "agentMessage", "localImage", "fileChange", "reasoning", "commandExecution", "update"}
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		if hist[k] > 0 {
			parts = append(parts, fmt.Sprintf("%s=%d", k, hist[k]))
		}
	}
	if len(parts) == 0 {
		return turns, "none"
	}
	return turns, strings.Join(parts, ",")
}

func (s *Server) handle(ctx context.Context, c *conn, in inbound) {
	switch in.Type {
	case "models":
		// Keep the model picker tied to the app-server running on this machine;
		// never infer availability from the phone's build-time strings.
		models, err := s.cx.ModelList(ctx)
		if err != nil {
			s.setFallbackModels()
			c.push(map[string]any{
				"type": "models", "source": "fallback",
				"error": "model/list unavailable: " + err.Error(),
				"data":  s.modelWireFallback(),
			})
			return
		}
		catalog, wire := modelCapabilities(models)
		if len(catalog) == 0 {
			s.setFallbackModels()
			c.push(map[string]any{
				"type": "models", "source": "fallback",
				"error": "model/list returned no visible models",
				"data":  s.modelWireFallback(),
			})
			return
		}
		s.modelMu.Lock()
		s.modelCatalog = catalog
		s.modelMu.Unlock()
		c.push(map[string]any{"type": "models", "source": "app-server", "data": wire})
	case "list":
		data, err := s.activeThreadData(ctx)
		if err != nil {
			c.push(map[string]any{"type": "error", "message": err.Error()})
			return
		}
		c.push(map[string]any{"type": "sessions", "data": data})

	case "read":
		raw, err := s.cx.ThreadRead(ctx, in.ThreadID, true)
		if err != nil {
			c.push(map[string]any{"type": "error", "message": err.Error()})
			return
		}
		// Codex always returns the FULL thread; for a heavy session that is a
		// multi-MB frame that must cross the relay and be parsed on the phone
		// before first paint. The client opens with a tail (limit>0) and pages
		// into the past with before=<offset of its earliest loaded turn>; every
		// response carries the window's offset so paging can continue.
		sliced, total, offset := windowThread(raw, in.Before, in.Limit)
		// Swap large inline base64 images for staged paths fetched lazily over
		// /file — an image-heavy session drops from ~3MB to tens of KB on open.
		sliced = stripInlineMedia(sliced, s.mediaCache)
		// Byte ceiling AFTER media-stripping: an oversized frame doesn't degrade
		// across the relay, it KILLS the agent↔hub link (read-limit close) and
		// with it every phone session on this machine — then the phone retries
		// the same read and the machine flaps in a loop. Halve the turn window
		// until the frame fits — the window's newest edge stays fixed (adjacent
		// to what the phone already shows) and it shrinks from the older side;
		// the rest stays reachable through further pages.
		cur := in.Limit
		if cur <= 0 || cur > total {
			cur = total
		}
		for len(sliced) > maxThreadBytes && cur > 1 {
			cur /= 2
			sliced, total, offset = windowThread(raw, in.Before, cur)
			sliced = stripInlineMedia(sliced, s.mediaCache)
		}
		if len(sliced) > hardMaxThreadBytes {
			// Even a single turn exceeds what the link can safely carry.
			c.push(map[string]any{"type": "error",
				"message": "会话内容过大：单条记录超出传输上限，暂无法在手机端展示"})
			s.audit("read thread=%s device=%s REFUSED oversized frame bytes=%d", in.ThreadID, c.device, len(sliced))
			return
		}
		truncated := offset > 0 // earlier turns exist beyond this window
		msg := map[string]any{"type": "thread", "threadId": in.ThreadID,
			"thread": json.RawMessage(sliced), "offset": offset}
		if in.Before > 0 {
			// Page response: the client grafts it in FRONT of what it shows
			// instead of replacing the transcript.
			msg["page"] = true
		}
		if truncated {
			msg["truncated"] = true
			msg["total"] = total
		}
		turns, hist := threadStats(sliced)
		s.audit("read thread=%s device=%s conn=%s limit=%d before=%d offset=%d truncated=%v total=%d returnedTurns=%d bytes=%d types=%s",
			in.ThreadID, c.device, c.id, in.Limit, in.Before, offset, truncated, total, turns, len(sliced), hist)
		c.push(msg)

	case "delete":
		// Archive the thread (codex has no hard delete; archive removes it from the
		// list and is reversible via thread/unarchive). Token-authenticated.
		if in.ThreadID == "" {
			c.push(map[string]any{"type": "error", "message": "delete: missing threadId"})
			return
		}
		if _, err := s.cx.Call(ctx, "thread/archive", map[string]any{"threadId": in.ThreadID}); err != nil {
			c.push(map[string]any{"type": "error", "message": "thread/archive: " + err.Error()})
			return
		}
		s.audit("archive thread=%s device=%s", in.ThreadID, c.device)
		c.push(map[string]any{"type": "deleted", "threadId": in.ThreadID})
		if data, err := s.activeThreadData(ctx); err == nil {
			c.push(map[string]any{"type": "sessions", "data": data})
		}

	case "prompt":
		s.handlePrompt(ctx, c, in)

	case "interrupt":
		// turn/interrupt REQUIRES both threadId and turnId. Prefer the turnId the
		// phone supplies (it sees it in streamed events); fall back to the one we
		// tracked from turn/start. Surface failures so the stop button isn't a no-op.
		turnID := in.TurnID
		if turnID == "" {
			turnID = s.activeTurn(in.ThreadID)
		}
		if turnID == "" {
			c.push(map[string]any{"type": "error", "message": "interrupt: no active turn for thread"})
			return
		}
		if _, err := s.cx.Call(ctx, "turn/interrupt", map[string]any{
			"threadId": in.ThreadID,
			"turnId":   turnID,
		}); err != nil {
			c.push(map[string]any{"type": "error", "message": "turn/interrupt: " + err.Error()})
		}

	case "approvalDecision":
		s.apMu.Lock()
		p := s.approvals[in.ID]
		s.apMu.Unlock()
		if p == nil {
			return // already resolved/expired
		}
		// Approval-ownership binding: only the device whose turn triggered the
		// approval may answer it. A different device (e.g. a rogue co-connection
		// with another token) is rejected and audited.
		if p.ownerDevice != "" && p.ownerDevice != c.device {
			s.audit("approval %s DENIED-FOREIGN by device=%s (owner=%s)", in.ID, c.device, p.ownerDevice)
			c.push(map[string]any{"type": "error", "message": "not your approval to answer"})
			return
		}
		// Unattributed approval (owner==""): the bridge runs its OWN app-server
		// process (separate from the desktop's), so every approval it sees belongs
		// to a bridge-initiated turn that set an owner — owner=="" is an anomaly.
		// Fail closed when it's ambiguous (more than one distinct device connected);
		// allow it in the unambiguous single-device case so single-owner never breaks.
		if p.ownerDevice == "" && s.distinctDevices() > 1 {
			s.audit("approval %s DENIED-AMBIGUOUS by device=%s (no owner, multiple devices)", in.ID, c.device)
			c.push(map[string]any{"type": "error", "message": "approval owner ambiguous; cannot answer"})
			return
		}
		s.audit("approval %s decided=%q by device=%s conn=%s", in.ID, in.Decision, c.device, c.id)
		select {
		case p.ch <- in.Decision:
		default:
		}

	case "registerPush":
		// A phone tells us its device push token so we can alert it in the
		// background. Dedup by token (re-registration just refreshes the entry).
		if in.PushToken != "" {
			s.pushMu.Lock()
			s.pushes[in.PushToken] = PushReg{Token: in.PushToken, Platform: in.Platform}
			n := len(s.pushes)
			s.pushMu.Unlock()
			log.Printf("push device registered (%d total)", n)
			c.push(map[string]any{"type": "pushRegistered"})
		}

	case "sync":
		// Foreground resync: re-send the session list and any pending approvals so
		// a phone that was backgrounded/offline catches up in one round-trip.
		if data, err := s.activeThreadData(ctx); err == nil {
			c.push(map[string]any{"type": "sessions", "data": data})
		}
		for _, p := range s.pendingApprovals() {
			c.push(map[string]any{"type": "approval", "id": p.id, "method": p.method, "params": p.params})
		}

	default:
		c.push(map[string]any{"type": "error", "message": "unknown type: " + in.Type})
	}
}

// activeThreadData returns non-archived thread summaries for the phone. Current
// codex excludes archived from thread/list by default; older builds don't, and
// the summaries carry no "archived" flag — so we also fetch the archived set and
// subtract it. Guard against a codex that ignores the filter entirely (its
// archived query would then return everything): never return an empty list when
// the unfiltered one had items.
func (s *Server) activeThreadData(ctx context.Context) ([]appserver.ThreadSummary, error) {
	res, err := s.cx.ThreadList(ctx, 100)
	if err != nil {
		return nil, err
	}
	arch, aerr := s.cx.ThreadListArchivedIDs(ctx, 300)
	if aerr != nil || len(arch) == 0 {
		return res.Data, nil
	}
	active := make([]appserver.ThreadSummary, 0, len(res.Data))
	for _, t := range res.Data {
		if _, isArchived := arch[t.ID]; !isArchived {
			active = append(active, t)
		}
	}
	if len(active) == 0 && len(res.Data) > 0 {
		return res.Data, nil // filter looks ineffective — don't hide everything
	}
	return active, nil
}

// Limits on phone-supplied images so one turn can't exceed the hub/agent ws read
// limit (32MB) or bloat the model request. Each image arrives as a data: URL.
const (
	maxPromptImages = 6
	// 4MB per image data URL (~3MB image) keeps 6 images + text under the 32MB
	// hub/agent ws read ceiling. The phone downscales to ~1600px so real photos
	// land well under this.
	maxImageDataURL = 4 << 20
)

func (s *Server) handlePrompt(ctx context.Context, c *conn, in inbound) {
	// Build the turn input first (text + image items) so an empty prompt bails
	// cleanly before we create or resume a thread.
	input := make([]any, 0, 1+len(in.Images))
	if strings.TrimSpace(in.Text) != "" {
		input = append(input, appserver.Text(in.Text))
	}
	imgN := 0
	for _, u := range in.Images {
		if imgN >= maxPromptImages || len(u) > maxImageDataURL || !strings.HasPrefix(u, "data:image/") {
			continue
		}
		input = append(input, appserver.Image(u))
		imgN++
	}
	if len(input) == 0 {
		c.push(map[string]any{"type": "error", "message": "empty prompt"})
		return
	}

	// Map the phone's composer choice to a SAFE, capped policy (never beyond
	// workspace-write, network always restricted, approval gate always kept).
	pol := remoteTurnPolicy(in.Approval, s.allowAutoReview)

	threadID := in.ThreadID
	if threadID == "" {
		// Clamp the phone-supplied cwd: it becomes the workspace-write root, so an
		// unvalidated value would let a remote turn write into ~/.ssh, ~/.codex, etc.
		cwd, cerr := validateCwd(in.Cwd)
		if cerr != nil {
			s.audit("prompt REJECTED cwd device=%s conn=%s: %v", c.device, c.id, cerr)
			c.push(map[string]any{"type": "error", "message": "rejected working directory: " + cerr.Error()})
			return
		}
		id, err := s.cx.ThreadStart(ctx, appserver.ThreadStartParams{
			Cwd:            cwd,
			Sandbox:        pol.sandboxMode,
			ApprovalPolicy: pol.approvalPolicy,
		})
		if err != nil {
			c.push(map[string]any{"type": "error", "message": "thread/start: " + err.Error()})
			return
		}
		threadID = id
		s.markResumed(threadID)
	} else if !s.isResumed(threadID) {
		// Existing thread (e.g. created by the desktop app) must be loaded into
		// our app-server before a turn can run on it — otherwise turn/start
		// fails with "thread not found".
		if _, err := s.cx.Call(ctx, "thread/resume", map[string]any{
			"threadId":       threadID,
			"approvalPolicy": pol.approvalPolicy,
			"sandbox":        pol.sandboxMode,
		}); err != nil {
			c.push(map[string]any{"type": "error", "message": "thread/resume: " + err.Error()})
			return
		}
		s.markResumed(threadID)
	}
	// Bind this thread's turn to the requesting device so only it can answer the
	// turn's approvals (Phase 6). Survives reconnect because it's keyed on device.
	s.setTurnOwner(threadID, c.device)
	model := s.sanitizeModel(in.Model)
	effort := s.sanitizeEffort(model, in.Effort)
	s.audit("prompt thread=%s device=%s conn=%s images=%d effort=%s model=%s tier=%s approval=%s(clamped=%v)",
		threadID, c.device, c.id, imgN, effort, model,
		serviceTierFor(in.Speed), pol.approvalPolicy, pol.clamped)
	c.push(map[string]any{"type": "promptAccepted", "threadId": threadID})

	// Remote-initiated turns carry the phone's hints (effort/model/speed) but a
	// server-clamped sandbox + approval policy — the phone can never disable the gate.
	res, err := s.cx.TurnStart(ctx, appserver.TurnStartParams{
		ThreadID:          threadID,
		Input:             input,
		ApprovalPolicy:    pol.approvalPolicy,
		ApprovalsReviewer: pol.reviewer,
		SandboxPolicy:     pol.sandboxPolicy,
		Effort:            effort,
		Summary:           "auto",
		Model:             model,
		ServiceTier:       serviceTierFor(in.Speed),
	})
	if err != nil {
		c.push(map[string]any{"type": "error", "message": "turn/start: " + err.Error()})
		return
	}
	if id := turnIDOf(res); id != "" {
		s.setActiveTurn(threadID, id)
	}
}

// turnPolicy is the resolved, server-clamped policy for a remote turn.
type turnPolicy struct {
	sandboxMode    string          // thread/start|resume SandboxMode string
	sandboxPolicy  json.RawMessage // turn/start SandboxPolicy object
	approvalPolicy string          // untrusted|on-failure|on-request|never
	reviewer       string          // ""(user) | auto_review
	clamped        bool            // true when a looser request was capped
}

// remoteTurnPolicy maps the phone's approval-mode choice to a SAFE policy. The
// remote sandbox is NEVER loosened beyond workspace-write and network stays
// restricted, regardless of the choice. "full" reduces repeated prompts by using
// on-failure inside that sandbox, but it still does not grant danger-full-access;
// "custom" is clamped. auto_review still needs the operator-side opt-in.
func remoteTurnPolicy(approval string, allowAutoReview bool) turnPolicy {
	p := turnPolicy{
		sandboxMode:    "workspace-write",
		sandboxPolicy:  appserver.SandboxWorkspaceWrite,
		approvalPolicy: "on-request",
	}
	switch approval {
	case "auto":
		// auto_review swaps the human approver for an automated one — i.e.
		// unattended execution inside the sandbox. A client field must not disable
		// the human gate, so this needs the operator-side opt-in; otherwise it
		// falls back to the default human-gated on-request policy.
		if allowAutoReview {
			p.reviewer = "auto_review"
		} else {
			p.clamped = true
		}
	case "full":
		// Requested "full access" from the phone. Keep the sandbox ceiling at
		// workspace-write + no network, but use on-failure so sandbox-permitted
		// commands don't ask for repetitive manual approval.
		p.approvalPolicy = "on-failure"
		p.clamped = true
	case "custom":
		// Requested local config (danger-full-access/never); refused remotely.
		p.clamped = true
	default: // "default" or empty/unknown
	}
	return p
}

// sensitiveDirs are home-relative subtrees a remote turn must never use as its
// cwd: making one the workspace-write root would let an (potentially unattended)
// turn write inside it — ssh keys, cloud creds, Codex's own auth/config — with no
// approval, since writes within the workspace root don't trigger the gate.
var sensitiveDirs = []string{
	".ssh", ".aws", ".gnupg", ".codex", ".config", ".kube", ".docker",
	".gemini", ".claude", "Library/Keychains",
}

// workspaceRoots is the allowlist of roots a remote-supplied cwd must fall under.
// Defaults to the user's home directory; override with CODEX_WORKSPACE_ROOTS
// (os.PathListSeparator-separated absolute paths), symlink-resolved.
func workspaceRoots() []string {
	if env := os.Getenv("CODEX_WORKSPACE_ROOTS"); env != "" {
		var roots []string
		for _, p := range filepath.SplitList(env) {
			if p == "" {
				continue
			}
			if real, err := filepath.EvalSymlinks(p); err == nil {
				roots = append(roots, real)
			}
		}
		if len(roots) > 0 {
			return roots
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		if real, err := filepath.EvalSymlinks(home); err == nil {
			return []string{real}
		}
		return []string{home}
	}
	return nil
}

// validateCwd clamps a remote-supplied working directory to a safe, existing
// directory: absolute, inside an allowed workspace root, and outside the home
// root itself and any sensitive subtree. Empty cwd is allowed (codex uses its own
// default — not attacker-chosen). Returns the resolved cwd to use, or an error to
// REJECT the turn — never silently fall back to a default the caller didn't intend.
func validateCwd(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", nil
	}
	if strings.ContainsRune(raw, 0) {
		return "", fmt.Errorf("path contains NUL")
	}
	abs, err := filepath.Abs(filepath.Clean(raw))
	if err != nil || !filepath.IsAbs(abs) {
		return "", fmt.Errorf("must be an absolute path")
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("does not exist")
	}
	if fi, err := os.Stat(real); err != nil || !fi.IsDir() {
		return "", fmt.Errorf("not a directory")
	}
	// Reject the home root itself and sensitive home subtrees. These DENY checks are
	// case-insensitive / inode-aware so a case-insensitive FS (macOS/Windows) can't
	// dodge the denylist (e.g. cwd=~/.SSH resolving to the same dir as ~/.ssh).
	if home, herr := os.UserHomeDir(); herr == nil {
		if hr, e := filepath.EvalSymlinks(home); e == nil {
			home = hr
		}
		if sameDir(real, home) {
			return "", fmt.Errorf("must be a project subdirectory, not the home root")
		}
		for _, d := range sensitiveDirs {
			if dirIsOrUnder(real, filepath.Join(home, filepath.FromSlash(d))) {
				return "", fmt.Errorf("is a protected directory")
			}
		}
	}
	// Must fall within an allowed workspace root. This ALLOW check stays
	// case-sensitive (the legitimate client uses the on-disk casing): over-rejecting
	// an odd-cased path is fail-closed, over-accepting one would not be.
	for _, root := range workspaceRoots() {
		if real == root || strings.HasPrefix(real, root+string(os.PathSeparator)) {
			return real, nil
		}
	}
	return "", fmt.Errorf("outside the allowed workspace root(s)")
}

// sameDir reports whether two paths denote the same directory, robust to symlinks
// and case-insensitive filesystems (os.SameFile compares device+inode; EqualFold
// is the fallback when a path can't be stat'd).
func sameDir(a, b string) bool {
	if a == b {
		return true
	}
	if fa, e1 := os.Stat(a); e1 == nil {
		if fb, e2 := os.Stat(b); e2 == nil {
			return os.SameFile(fa, fb)
		}
	}
	return strings.EqualFold(a, b)
}

// dirIsOrUnder reports whether real is base or sits inside base. Used for the cwd
// DENYLIST, so it matches case-insensitively (macOS/Windows case folding must not
// bypass a protected root) and is inode-aware for the exact-dir case.
func dirIsOrUnder(real, base string) bool {
	if sameDir(real, base) {
		return true
	}
	return strings.HasPrefix(strings.ToLower(real), strings.ToLower(base)+string(os.PathSeparator))
}

func (s *Server) setFallbackModels() {
	s.modelMu.Lock()
	s.modelCatalog = fallbackModelCapabilities()
	s.modelMu.Unlock()
}

func (s *Server) modelWireFallback() []map[string]any {
	s.modelMu.RLock()
	defer s.modelMu.RUnlock()
	ids := make([]string, 0, len(s.modelCatalog))
	for id := range s.modelCatalog {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		m := s.modelCatalog[id]
		out = append(out, map[string]any{
			"id": m.ID, "label": m.Label, "efforts": sortedKeys(m.Efforts),
			"defaultEffort": m.DefaultEffort, "default": m.IsDefault,
		})
	}
	return out
}

// sanitizeModel only accepts IDs returned by this machine's app-server. The
// fallback catalog keeps old bridge/app combinations working when model/list is
// unavailable, while a successful refresh immediately admits new desktop IDs.
func (s *Server) sanitizeModel(m string) string {
	m = strings.TrimSpace(m)
	s.modelMu.RLock()
	defer s.modelMu.RUnlock()
	if _, ok := s.modelCatalog[m]; ok {
		return m
	}
	return ""
}

// sanitizeEffort validates against the selected model's advertised capabilities
// so newer values such as max/ultra are forwarded only where app-server allows
// them. Unknown/invalid values are omitted rather than guessed.
func (s *Server) sanitizeEffort(model, effort string) string {
	effort = strings.TrimSpace(effort)
	s.modelMu.RLock()
	m, ok := s.modelCatalog[model]
	s.modelMu.RUnlock()
	if ok {
		if m.Efforts[effort] {
			return effort
		}
		return ""
	}
	// If the phone omitted a model, retain the historical safe enum for the
	// desktop-configured default model.
	switch effort {
	case "none", "minimal", "low", "medium", "high", "xhigh":
		return effort
	}
	return ""
}

// serviceTierFor maps the composer's speed choice to a Codex ServiceTier.
func serviceTierFor(speed string) string {
	switch speed {
	case "fast":
		return "fast"
	case "standard":
		return "flex"
	}
	return ""
}

// turnIDOf extracts turn.id from a turn/start response or turn notification body.
func turnIDOf(raw json.RawMessage) string {
	var v struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return ""
	}
	return v.Turn.ID
}

func (s *Server) isResumed(id string) bool {
	s.rmu.Lock()
	defer s.rmu.Unlock()
	return s.resumed[id]
}

func (s *Server) markResumed(id string) {
	s.rmu.Lock()
	defer s.rmu.Unlock()
	s.resumed[id] = true
}

func (s *Server) setActiveTurn(threadID, turnID string) {
	s.tmu.Lock()
	defer s.tmu.Unlock()
	s.turns[threadID] = turnID
}

func (s *Server) clearActiveTurn(threadID string) {
	s.tmu.Lock()
	defer s.tmu.Unlock()
	delete(s.turns, threadID)
	// Also drop the turn owner so the map can't grow unboundedly and a later,
	// non-bridge turn on the same thread can't inherit a stale owner. Pending
	// approvals already captured ownerDevice on their struct, and resync/answer
	// read THAT (not turnOwner), so clearing here doesn't affect in-flight approvals.
	delete(s.turnOwner, threadID)
}

func (s *Server) activeTurn(threadID string) string {
	s.tmu.Lock()
	defer s.tmu.Unlock()
	return s.turns[threadID]
}

func (s *Server) setTurnOwner(threadID, device string) {
	s.tmu.Lock()
	defer s.tmu.Unlock()
	s.turnOwner[threadID] = device
}

func (s *Server) ownerOf(threadID string) string {
	s.tmu.Lock()
	defer s.tmu.Unlock()
	return s.turnOwner[threadID]
}

// pushToDevice sends to every connection of the named device; device=="" means
// broadcast to all connections.
func (s *Server) pushToDevice(device string, v any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.clients {
		if device == "" || c.device == device {
			c.push(v)
		}
	}
}

// distinctDevices counts the distinct device identities currently connected.
func (s *Server) distinctDevices() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	set := make(map[string]struct{}, len(s.clients))
	for c := range s.clients {
		set[c.device] = struct{}{}
	}
	return len(set)
}

// trunc bounds attacker-controlled strings before they reach the audit log.
func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// clientIP returns the real caller address, preferring the headers nginx sets
// (behind the frp tunnel r.RemoteAddr is just 127.0.0.1). Logging only — never
// used for an authorization decision (X-Forwarded-For is client-spoofable).
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i >= 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	if xr := r.Header.Get("X-Real-IP"); xr != "" {
		return xr
	}
	return r.RemoteAddr
}

// threadOf extracts the thread/conversation id from approval params (v2 uses
// threadId, the legacy exec/patch approvals use conversationId).
func threadOf(params json.RawMessage) string {
	var v struct {
		ThreadID       string `json:"threadId"`
		ConversationID string `json:"conversationId"`
	}
	_ = json.Unmarshal(params, &v)
	if v.ThreadID != "" {
		return v.ThreadID
	}
	return v.ConversationID
}

// pendingApprovals snapshots the currently-open approval requests so they can be
// re-surfaced to a (re)connecting client.
func (s *Server) pendingApprovals() []*pendingApproval {
	s.apMu.Lock()
	defer s.apMu.Unlock()
	out := make([]*pendingApproval, 0, len(s.approvals))
	for _, p := range s.approvals {
		out = append(out, p)
	}
	return out
}

// pushNotify fires a background notification through the configured Pusher, but
// only when no phone is currently connected (a foreground app already sees the
// live WS event, so a push would be redundant/noisy).
func (s *Server) pushNotify(n Notification) {
	s.mu.Lock()
	connected := len(s.clients)
	s.mu.Unlock()
	if connected > 0 {
		return
	}
	s.pushMu.Lock()
	regs := make([]PushReg, 0, len(s.pushes))
	for _, r := range s.pushes {
		regs = append(regs, r)
	}
	p := s.pusher
	s.pushMu.Unlock()
	if p == nil || len(regs) == 0 {
		return
	}
	go p.Push(regs, n)
}

// onNotification forwards every app-server notification to all clients, and keeps
// the threadId->turnId map current so the phone's interrupt can target the turn.
func (s *Server) onNotification(method string, params json.RawMessage) {
	switch {
	case strings.HasSuffix(method, "turn/started"), method == "turnStarted":
		var v struct {
			ThreadID string `json:"threadId"`
		}
		if json.Unmarshal(params, &v) == nil && v.ThreadID != "" {
			if id := turnIDOf(params); id != "" {
				s.setActiveTurn(v.ThreadID, id)
			}
		}
	case strings.HasSuffix(method, "turn/completed"), strings.HasSuffix(method, "turn/failed"), method == "turnCompleted":
		var v struct {
			ThreadID string `json:"threadId"`
		}
		if json.Unmarshal(params, &v) == nil && v.ThreadID != "" {
			s.clearActiveTurn(v.ThreadID)
			s.pushNotify(Notification{Kind: "turnDone", Title: "Codex", Body: "任务完成", ThreadID: v.ThreadID})
		}
	}
	// Codex echoes input images back inside item notifications. The phone already
	// has any image it sent and never renders these events, so strip the base64
	// payloads before forwarding — otherwise every image turn ships the picture
	// back over mobile (hundreds of KB) for nothing.
	s.broadcast(map[string]any{"type": "event", "method": method, "params": stripImageData(params)})
}

// dataImageRe matches an inline image data URL (the bulky part of an image
// turn-input item echoed in notifications).
var dataImageRe = regexp.MustCompile(`data:image/[A-Za-z0-9.+-]+;base64,[A-Za-z0-9+/=]+`)

// stripImageData replaces inline base64 image payloads with a tiny placeholder,
// keeping the JSON valid. Only touches large params that actually contain one.
func stripImageData(params json.RawMessage) json.RawMessage {
	if len(params) < 4096 || !bytes.Contains(params, []byte("data:image/")) {
		return params
	}
	return dataImageRe.ReplaceAll(params, []byte("data:image/jpeg;base64,_stripped_"))
}

// onApproval surfaces an approval request to the owning device and waits for a
// decision. The prompt is routed only to the device whose turn triggered it
// (Phase 6); if the owner is unknown (e.g. a turn not initiated via the bridge)
// it falls back to a broadcast that any device may answer.
func (s *Server) onApproval(method string, params json.RawMessage) (any, error) {
	id := NewToken()
	owner := s.ownerOf(threadOf(params))
	p := &pendingApproval{id: id, method: method, params: params, ownerDevice: owner, ch: make(chan string, 1)}
	s.apMu.Lock()
	s.approvals[id] = p
	s.apMu.Unlock()
	defer func() {
		s.apMu.Lock()
		delete(s.approvals, id)
		s.apMu.Unlock()
	}()

	s.audit("approval %s surfaced method=%s owner=%s", id, method, owner)
	msg := map[string]any{"type": "approval", "id": id, "method": method, "params": json.RawMessage(params)}
	if owner != "" {
		s.pushToDevice(owner, msg)
	} else {
		s.broadcast(msg)
	}
	// Alert a backgrounded phone that there's something to approve.
	s.pushNotify(Notification{Kind: "approval", Title: "Codex 需要批准", Body: "有命令/改动等待你确认", ThreadID: ""})

	select {
	case dec := <-p.ch:
		return map[string]any{"decision": clampDecision(method, dec)}, nil
	case <-time.After(s.apTimeout):
		// Fail closed, but return a well-formed decline (not an RPC error) so the
		// app-server gets a clean response on the approval callback.
		return map[string]any{"decision": clampDecision(method, "decline")}, nil
	}
}

// clampDecision maps the phone's coarse accept/decline into the EXACT,
// protocol-valid decision string for the given approval method, and NEVER
// forwards the persistent/amendment variants (acceptForSession,
// approved_for_session, *_amendment) — those disable future prompting and would
// defeat the per-turn read-only/untrusted downgrade. Anything ambiguous declines.
//
//	execCommandApproval / applyPatchApproval -> ReviewDecision{approved|denied}
//	everything else (commandExecution/fileChange/...) -> {accept|decline}
func clampDecision(method, dec string) string {
	approve := dec == "accept" || dec == "approve" || dec == "approved" || dec == "allow" || dec == "yes"
	if strings.Contains(method, "execCommand") || strings.Contains(method, "applyPatch") ||
		strings.Contains(method, "ExecCommand") || strings.Contains(method, "ApplyPatch") {
		if approve {
			return "approved"
		}
		return "denied"
	}
	if approve {
		return "accept"
	}
	return "decline"
}
