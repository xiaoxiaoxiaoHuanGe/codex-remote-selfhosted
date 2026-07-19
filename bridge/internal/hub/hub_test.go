package hub

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/yunyuchen/codex-remote/bridge/internal/e2ee"
)

const (
	testAgentKey = "agentkey"
	testMachine  = "m1"
	testKeyID    = "k_test_0001"
)

// fakeAgent registers a machine over /agent, syncs one device's public key, and
// surfaces frames the hub sends it (notably "open" on a successful phone auth).
type fakeAgent struct {
	ws     *websocket.Conn
	frames chan frame
}

func wsBase(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

// newHubWithAgent starts a hub (device-auth optionally on) with one registered
// machine that has synced devicePub under testKeyID. Returns the live server.
func newHubWithAgent(t *testing.T, requireDeviceAuth bool, devicePub *[32]byte) (*Hub, *httptest.Server, *fakeAgent) {
	t.Helper()
	h := New(testAgentKey)
	h.RequireDeviceAuth = requireDeviceAuth
	srv := httptest.NewServer(h.Handler())
	t.Cleanup(srv.Close)

	ctx := context.Background()
	ws, _, err := websocket.Dial(ctx, wsBase(t, srv)+"/agent", nil)
	if err != nil {
		t.Fatalf("agent dial: %v", err)
	}
	t.Cleanup(func() { _ = ws.Close(websocket.StatusNormalClosure, "") })

	if err := wsjson.Write(ctx, ws, frame{T: "register", MachineID: testMachine, Token: "tok1", AgentKey: testAgentKey}); err != nil {
		t.Fatalf("register write: %v", err)
	}
	var reg frame
	if err := wsjson.Read(ctx, ws, &reg); err != nil || reg.T != "registered" {
		t.Fatalf("register ack: %v (t=%q)", err, reg.T)
	}

	dl, _ := json.Marshal([]devicePubEntry{{
		KeyID:     testKeyID,
		DevicePub: base64.StdEncoding.EncodeToString(devicePub[:]),
	}})
	if err := wsjson.Write(ctx, ws, frame{T: "devices", Data: dl}); err != nil {
		t.Fatalf("devices sync write: %v", err)
	}
	// The hub applies "devices" in its agent read loop (no ack); poll its state so
	// the phone never races ahead of the sync.
	waitFor(t, func() bool {
		a := h.agentByID(testMachine)
		return a != nil && a.devicePub(testKeyID) != ""
	})

	fa := &fakeAgent{ws: ws, frames: make(chan frame, 8)}
	go func() {
		for {
			var f frame
			if err := wsjson.Read(ctx, ws, &f); err != nil {
				return
			}
			fa.frames <- f
		}
	}()
	return h, srv, fa
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met within deadline")
}

// TestDeviceAuthHappyPath: a phone holding the device secret opens the box and is
// admitted — the agent receives an "open" frame for the new session.
func TestDeviceAuthHappyPath(t *testing.T) {
	devicePub, deviceSec, _ := e2ee.GenDeviceKeypair()
	_, srv, fa := newHubWithAgent(t, true, devicePub)

	ctx := context.Background()
	// keyId-only URL: no machine=, no token — mirrors the real phone connect string.
	ws, _, err := websocket.Dial(ctx, wsBase(t, srv)+"/ws?keyId="+testKeyID, nil)
	if err != nil {
		t.Fatalf("phone dial: %v", err)
	}
	defer ws.Close(websocket.StatusNormalClosure, "")

	nonce := solveChallenge(t, ctx, ws, deviceSec)
	if err := wsjson.Write(ctx, ws, authResponse{Type: "auth_response", KeyID: testKeyID, Proof: base64.StdEncoding.EncodeToString(nonce)}); err != nil {
		t.Fatalf("auth_response write: %v", err)
	}

	// The hub confirms with auth_ok carrying a /file session ticket before relaying.
	var ok authOK
	rctx, rcancel := context.WithTimeout(ctx, 3*time.Second)
	if err := wsjson.Read(rctx, ws, &ok); err != nil {
		rcancel()
		t.Fatalf("read auth_ok: %v", err)
	}
	rcancel()
	if ok.Type != "auth_ok" || ok.Ticket == "" {
		t.Fatalf("want auth_ok with a ticket, got %+v", ok)
	}

	select {
	case f := <-fa.frames:
		if f.T != "open" {
			t.Fatalf("want open frame after auth, got %q", f.T)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no open frame after a valid proof")
	}
}

// TestFileTicketGatesFileEndpoint: device-auth /file is refused without a valid
// ticket and accepted with the one auth_ok handed out — reaching the agent.
func TestFileTicketGatesFileEndpoint(t *testing.T) {
	devicePub, deviceSec, _ := e2ee.GenDeviceKeypair()
	_, srv, fa := newHubWithAgent(t, true, devicePub)
	httpBase := srv.URL // http://... (the /file endpoint is plain HTTP)

	// A bogus ticket is rejected.
	if resp, err := http.Get(httpBase + "/file?path=/x.png&ticket=bogus"); err == nil {
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("bogus ticket: want 401, got %d", resp.StatusCode)
		}
	} else {
		t.Fatalf("file GET (bogus): %v", err)
	}

	// Authenticate to obtain a real ticket.
	ctx := context.Background()
	ws, _, err := websocket.Dial(ctx, wsBase(t, srv)+"/ws?keyId="+testKeyID, nil)
	if err != nil {
		t.Fatalf("phone dial: %v", err)
	}
	defer ws.Close(websocket.StatusNormalClosure, "")
	nonce := solveChallenge(t, ctx, ws, deviceSec)
	_ = wsjson.Write(ctx, ws, authResponse{Type: "auth_response", KeyID: testKeyID, Proof: base64.StdEncoding.EncodeToString(nonce)})
	var ok authOK
	rctx, rcancel := context.WithTimeout(ctx, 3*time.Second)
	if err := wsjson.Read(rctx, ws, &ok); err != nil {
		rcancel()
		t.Fatalf("read auth_ok: %v", err)
	}
	rcancel()

	// Have the fake agent answer the relayed file request with a stub image.
	go func() {
		for f := range fa.frames {
			if f.T == "file" {
				_ = wsjson.Write(context.Background(), fa.ws, frame{
					T: "file", ReqID: f.ReqID, Status: http.StatusOK,
					Ctype: "image/png", B64: base64.StdEncoding.EncodeToString([]byte("PNGDATA")),
				})
			}
		}
	}()

	resp, err := http.Get(httpBase + "/file?path=/x.png&ticket=" + ok.Ticket)
	if err != nil {
		t.Fatalf("file GET (valid): %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("valid ticket: want 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "PNGDATA" {
		t.Fatalf("unexpected media body: %q", body)
	}
}

// TestDeviceAuthWrongProofRejected: a phone that can't open the box (wrong secret)
// sends a bogus proof — the hub closes it and never opens a session.
func TestDeviceAuthWrongProofRejected(t *testing.T) {
	devicePub, _, _ := e2ee.GenDeviceKeypair()
	_, srv, fa := newHubWithAgent(t, true, devicePub)

	ctx := context.Background()
	ws, _, err := websocket.Dial(ctx, wsBase(t, srv)+"/ws?keyId="+testKeyID, nil)
	if err != nil {
		t.Fatalf("phone dial: %v", err)
	}
	defer ws.Close(websocket.StatusNormalClosure, "")

	readChallenge(t, ctx, ws) // consume the challenge, then answer wrong
	bogus := make([]byte, e2ee.ChallengeNonceLen)
	_, _ = rand.Read(bogus)
	_ = wsjson.Write(ctx, ws, authResponse{Type: "auth_response", KeyID: testKeyID, Proof: base64.StdEncoding.EncodeToString(bogus)})

	assertNoOpen(t, fa)
	assertPhoneClosed(t, ctx, ws)
}

// TestDeviceAuthUnknownKeyIdRejected: a keyId no machine synced matches no agent, so
// keyId-only routing finds nothing and the dial is refused at the HTTP layer.
func TestDeviceAuthUnknownKeyIdRejected(t *testing.T) {
	devicePub, _, _ := e2ee.GenDeviceKeypair()
	_, srv, _ := newHubWithAgent(t, true, devicePub)

	ctx := context.Background()
	if ws, _, err := websocket.Dial(ctx, wsBase(t, srv)+"/ws?keyId=does_not_exist", nil); err == nil {
		ws.Close(websocket.StatusNormalClosure, "")
		t.Fatal("expected dial to fail for an unknown keyId")
	}
}

// TestDeviceAuthUnknownMachineRejected: an unknown machine is refused at the HTTP
// layer before any WS upgrade (the dial itself fails).
func TestDeviceAuthUnknownMachineRejected(t *testing.T) {
	devicePub, _, _ := e2ee.GenDeviceKeypair()
	_, srv, _ := newHubWithAgent(t, true, devicePub)

	ctx := context.Background()
	if ws, _, err := websocket.Dial(ctx, wsBase(t, srv)+"/ws?machine=ghost&keyId="+testKeyID, nil); err == nil {
		ws.Close(websocket.StatusNormalClosure, "")
		t.Fatal("expected dial to fail for unknown machine")
	}
}

// TestLegacyTokenPathStillWorks: with device-auth OFF, a phone presenting the
// machine token connects with no handshake — backward compatibility.
func TestLegacyTokenPathStillWorks(t *testing.T) {
	devicePub, _, _ := e2ee.GenDeviceKeypair()
	_, srv, fa := newHubWithAgent(t, false, devicePub)

	ctx := context.Background()
	ws, _, err := websocket.Dial(ctx, wsBase(t, srv)+"/ws?machine="+testMachine+"&token=tok1", nil)
	if err != nil {
		t.Fatalf("phone dial: %v", err)
	}
	defer ws.Close(websocket.StatusNormalClosure, "")

	select {
	case f := <-fa.frames:
		if f.T != "open" {
			t.Fatalf("want open frame on legacy token path, got %q", f.T)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no open frame on legacy token path")
	}
}

// ---- helpers ----

func readChallenge(t *testing.T, ctx context.Context, ws *websocket.Conn) authChallenge {
	t.Helper()
	var chal authChallenge
	rctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := wsjson.Read(rctx, ws, &chal); err != nil {
		t.Fatalf("read auth_challenge: %v", err)
	}
	if chal.Type != "auth_challenge" || chal.EPK == "" || chal.Box == "" {
		t.Fatalf("malformed challenge: %+v", chal)
	}
	return chal
}

func solveChallenge(t *testing.T, ctx context.Context, ws *websocket.Conn, deviceSec *[32]byte) []byte {
	t.Helper()
	chal := readChallenge(t, ctx, ws)
	epkRaw, err := base64.StdEncoding.DecodeString(chal.EPK)
	if err != nil || len(epkRaw) != 32 {
		t.Fatalf("bad epk: %v", err)
	}
	sealed, err := base64.StdEncoding.DecodeString(chal.Box)
	if err != nil {
		t.Fatalf("bad box: %v", err)
	}
	var epk [32]byte
	copy(epk[:], epkRaw)
	nonce, err := e2ee.OpenChallenge(sealed, &epk, deviceSec)
	if err != nil {
		t.Fatalf("OpenChallenge: %v", err)
	}
	return nonce
}

func assertNoOpen(t *testing.T, fa *fakeAgent) {
	t.Helper()
	select {
	case f := <-fa.frames:
		t.Fatalf("unexpected agent frame after rejected auth: %q", f.T)
	case <-time.After(500 * time.Millisecond):
	}
}

func assertPhoneClosed(t *testing.T, ctx context.Context, ws *websocket.Conn) {
	t.Helper()
	rctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if _, _, err := ws.Read(rctx); err == nil {
		t.Fatal("expected phone ws to be closed after rejected auth")
	}
}

// registerAgent dials /agent and sends one register frame, returning the ack
// frame's type (or the read error). Used to exercise the duplicate-id path.
func registerAgent(t *testing.T, srv *httptest.Server, id, token string) (string, *websocket.Conn) {
	t.Helper()
	ctx := context.Background()
	ws, _, err := websocket.Dial(ctx, wsBase(t, srv)+"/agent", nil)
	if err != nil {
		t.Fatalf("agent dial: %v", err)
	}
	t.Cleanup(func() { _ = ws.Close(websocket.StatusNormalClosure, "") })
	if err := wsjson.Write(ctx, ws, frame{T: "register", MachineID: id, Token: token, AgentKey: testAgentKey}); err != nil {
		t.Fatalf("register write: %v", err)
	}
	var reg frame
	rctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := wsjson.Read(rctx, ws, &reg); err != nil {
		return "", ws
	}
	return reg.T, ws
}

// TestAgentTakeoverReconnect: the same machine reconnecting (same id+token+
// agentKey) takes over instead of being refused. This is the fix for the
// half-open deadlock where a stale old conn wedged the id and every reconnect
// got "duplicate id" forever.
func TestAgentTakeoverReconnect(t *testing.T) {
	var pub [32]byte
	h, srv, _ := newHubWithAgent(t, false, &pub)

	ack, _ := registerAgent(t, srv, testMachine, "tok1") // same id, same token
	if ack != "registered" {
		t.Fatalf("reconnect with same id+token should take over (registered), got %q", ack)
	}
	// The machine is still online — now served by the new connection.
	waitFor(t, func() bool { return h.agentByID(testMachine) != nil })
}

// TestAgentDuplicateIdTokenMismatchRejected: a same-id connection with a
// DIFFERENT token is still refused, so takeover can't be used to hijack a live
// machine's id without also holding its token.
func TestAgentDuplicateIdTokenMismatchRejected(t *testing.T) {
	var pub [32]byte
	h, srv, _ := newHubWithAgent(t, false, &pub)

	ack, _ := registerAgent(t, srv, testMachine, "tok-other") // same id, wrong token
	if ack == "registered" {
		t.Fatalf("same id with a different token must NOT take over")
	}
	// The original agent is untouched and still online.
	if h.agentByID(testMachine) == nil {
		t.Fatal("original agent should remain registered after a rejected takeover")
	}
}
