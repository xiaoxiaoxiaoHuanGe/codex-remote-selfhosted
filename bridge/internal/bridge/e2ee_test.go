package bridge

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/yunyuchen/codex-remote/bridge/internal/devstore"
	"github.com/yunyuchen/codex-remote/bridge/internal/e2ee"
)

// newE2EEServer builds a bare Server with the encrypted control-plane enabled over
// temp files (no app-server needed — the seam is transport-agnostic).
func newE2EEServer(t *testing.T) (s *Server, enrollPub *[32]byte, pend *devstore.PendingStore) {
	t.Helper()
	dir := t.TempDir()
	devs, err := devstore.Open(filepath.Join(dir, "devices.json"))
	if err != nil {
		t.Fatalf("devstore.Open: %v", err)
	}
	pend = devstore.OpenPending(filepath.Join(dir, "pending.json"))
	var enrollSec *[32]byte
	enrollPub, enrollSec, err = e2ee.GenEnrollKeypair()
	if err != nil {
		t.Fatalf("GenEnrollKeypair: %v", err)
	}
	s = &Server{}
	s.EnableE2EE(devs, enrollSec, pend)
	return s, enrollPub, pend
}

// captureConn returns a conn whose writes are captured into the returned slice
// pointer (so tests can inspect the outbound frames the seam produced).
func captureConn() (*conn, *[]any) {
	got := &[]any{}
	c := &conn{
		send:  make(chan any, 16),
		done:  make(chan struct{}),
		id:    "test",
		armed: make(chan struct{}),
		write: func(_ context.Context, v any) error { *got = append(*got, v); return nil },
	}
	return c, got
}

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func TestFirstFrameKind(t *testing.T) {
	cases := []struct{ in, want string }{
		{`{"type":"pair_init","pairingId":"x"}`, "pair_init"},
		{`{"k":"key1","s":0,"n":"..","c":".."}`, "envelope"},
		{`{"type":"prompt","text":"hi"}`, "other"},
		{`not json`, "other"},
		{`{}`, "other"},
	}
	for _, tc := range cases {
		if got := firstFrameKind([]byte(tc.in)); got != tc.want {
			t.Errorf("firstFrameKind(%s)=%q want %q", tc.in, got, tc.want)
		}
	}
}

func TestHandlePairInitRoundTrip(t *testing.T) {
	s, enrollPub, pend := newE2EEServer(t)
	if err := pend.AddPending("pid-1", time.Minute, time.Now()); err != nil {
		t.Fatal(err)
	}
	devicePub, deviceSec, _ := e2ee.GenDeviceKeypair()
	in := pairInit{Type: "pair_init", PairingID: "pid-1", DevicePub: b64(devicePub[:]), Name: "iPhone"}

	resp, disconnect := s.handlePairInit(in)
	if disconnect {
		t.Fatal("unexpected disconnect on success")
	}
	pr, ok := resp.(pairResult)
	if !ok {
		t.Fatalf("want pairResult, got %T", resp)
	}
	sealed, err := base64.StdEncoding.DecodeString(pr.Sealed)
	if err != nil {
		t.Fatalf("decode sealed: %v", err)
	}
	payload, err := e2ee.OpenPairing(sealed, enrollPub, deviceSec)
	if err != nil {
		t.Fatalf("OpenPairing: %v", err)
	}
	dev, ok := s.devices.Lookup(payload.KeyID)
	if !ok {
		t.Fatal("device not persisted")
	}
	if dev.Name != "iPhone" {
		t.Fatalf("name=%q", dev.Name)
	}
	if dev.PSK != payload.PSK {
		t.Fatal("persisted PSK != sealed PSK")
	}
	// Single-use: replaying the same pairingId is refused.
	if _, dc := s.handlePairInit(in); !dc {
		t.Fatal("reused pairingId should disconnect")
	}
}

func TestHandlePairInitRejects(t *testing.T) {
	s, _, pend := newE2EEServer(t)
	devicePub, _, _ := e2ee.GenDeviceKeypair()

	// Unknown pairingId.
	_, dc := s.handlePairInit(pairInit{Type: "pair_init", PairingID: "ghost", DevicePub: b64(devicePub[:]), Name: "x"})
	if !dc {
		t.Fatal("unknown pairingId should disconnect")
	}
	// Bad devicePub (not 32 bytes).
	_ = pend.AddPending("pid-2", time.Minute, time.Now())
	_, dc = s.handlePairInit(pairInit{Type: "pair_init", PairingID: "pid-2", DevicePub: "AAers", Name: "x"})
	if !dc {
		t.Fatal("bad devicePub should disconnect")
	}
}

func TestOpenEnvelopeBindDecodeReplay(t *testing.T) {
	s, _, _ := newE2EEServer(t)
	psk := bytes.Repeat([]byte{0x07}, 32)
	keyID := "k_unit_01"
	if err := s.devices.Add(devstore.Device{KeyID: keyID, Name: "dev", PSK: b64(psk)}); err != nil {
		t.Fatal(err)
	}
	kP2B, _, err := e2ee.DeriveKeys(psk, keyID)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := captureConn()

	// Frame 0 binds the conn and decodes.
	pt0, _ := json.Marshal(inbound{Type: "prompt", Text: "hi"})
	env0, _ := e2ee.Seal(kP2B, keyID, e2ee.DirP2B, 0, pt0)
	raw0, _ := json.Marshal(env0)
	got0, err := s.openEnvelope(c, raw0)
	if err != nil {
		t.Fatalf("frame0: %v", err)
	}
	if got0.Type != "prompt" || got0.Text != "hi" {
		t.Fatalf("decoded %+v", got0)
	}
	if !c.e2ee || c.keyID != keyID || c.device != "dev" {
		t.Fatalf("conn not armed: e2ee=%v keyID=%q device=%q", c.e2ee, c.keyID, c.device)
	}
	if c.seqIn != 1 {
		t.Fatalf("seqIn=%d want 1", c.seqIn)
	}
	select {
	case <-c.armed:
	default:
		t.Fatal("armed channel not closed after bind")
	}

	// Replay frame 0 (seq 0 again, but seqIn is now 1) → rejected.
	if _, err := s.openEnvelope(c, raw0); err != errReject {
		t.Fatalf("replay want errReject, got %v", err)
	}

	// Frame 1 (next seq) decodes.
	pt1, _ := json.Marshal(inbound{Type: "interrupt"})
	env1, _ := e2ee.Seal(kP2B, keyID, e2ee.DirP2B, 1, pt1)
	raw1, _ := json.Marshal(env1)
	got1, err := s.openEnvelope(c, raw1)
	if err != nil {
		t.Fatalf("frame1: %v", err)
	}
	if got1.Type != "interrupt" || c.seqIn != 2 {
		t.Fatalf("decoded %+v seqIn=%d", got1, c.seqIn)
	}
}

func TestOpenEnvelopeUnknownKey(t *testing.T) {
	s, _, _ := newE2EEServer(t)
	c, _ := captureConn()
	env := e2ee.Envelope{K: "ghost", S: 0, N: b64(make([]byte, 24)), C: b64([]byte("x"))}
	raw, _ := json.Marshal(env)
	if _, err := s.openEnvelope(c, raw); err != errReject {
		t.Fatalf("unknown keyId want errReject, got %v", err)
	}
	if c.e2ee {
		t.Fatal("conn must not arm on unknown keyId")
	}
}

func TestSealAndWriteRoundTrip(t *testing.T) {
	psk := bytes.Repeat([]byte{0x09}, 32)
	keyID := "k_out_01"
	_, kB2P, _ := e2ee.DeriveKeys(psk, keyID)
	c, got := captureConn()
	c.e2ee = true
	c.keyID = keyID
	c.kB2P = kB2P
	ctx := context.Background()

	if err := c.sealAndWrite(ctx, map[string]any{"type": "hello"}); err != nil {
		t.Fatal(err)
	}
	if err := c.sealAndWrite(ctx, map[string]any{"type": "world"}); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 2 {
		t.Fatalf("writes=%d want 2", len(*got))
	}
	env0 := (*got)[0].(e2ee.Envelope)
	if env0.S != 0 || env0.K != keyID {
		t.Fatalf("env0=%+v", env0)
	}
	plain0, err := e2ee.Open(kB2P, keyID, e2ee.DirB2P, 0, env0)
	if err != nil {
		t.Fatalf("open env0: %v", err)
	}
	var m map[string]any
	_ = json.Unmarshal(plain0, &m)
	if m["type"] != "hello" {
		t.Fatalf("decoded %v", m)
	}
	if env1 := (*got)[1].(e2ee.Envelope); env1.S != 1 {
		t.Fatalf("env1 seq=%d want 1", env1.S)
	}
}

func TestSealAndWriteCleartextBeforeArm(t *testing.T) {
	c, got := captureConn() // c.e2ee == false
	if err := c.sealAndWrite(context.Background(), map[string]any{"type": "x"}); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 1 {
		t.Fatalf("writes=%d", len(*got))
	}
	if _, sealed := (*got)[0].(e2ee.Envelope); sealed {
		t.Fatal("must pass through cleartext before the codec arms")
	}
}

func TestRouteInboundLegacyCleartext(t *testing.T) {
	s := &Server{} // E2EE off
	c, _ := captureConn()
	raw, _ := json.Marshal(inbound{Type: "list"})
	in, err := s.routeInbound(context.Background(), c, raw)
	if err != nil {
		t.Fatal(err)
	}
	if in == nil || in.Type != "list" {
		t.Fatalf("in=%+v", in)
	}
}

func TestRouteInboundHardCut(t *testing.T) {
	s, _, _ := newE2EEServer(t)
	c, _ := captureConn()
	raw, _ := json.Marshal(inbound{Type: "prompt", Text: "x"}) // not pair_init, no k
	if _, err := s.routeInbound(context.Background(), c, raw); err != errReject {
		t.Fatalf("cleartext under E2EE want errReject, got %v", err)
	}
}

func TestRouteInboundPairInit(t *testing.T) {
	s, enrollPub, pend := newE2EEServer(t)
	_ = pend.AddPending("pid-9", time.Minute, time.Now())
	devicePub, deviceSec, _ := e2ee.GenDeviceKeypair()
	c, got := captureConn()
	raw, _ := json.Marshal(pairInit{Type: "pair_init", PairingID: "pid-9", DevicePub: b64(devicePub[:]), Name: "px"})

	in, err := s.routeInbound(context.Background(), c, raw)
	if err != nil {
		t.Fatalf("pair_init success should not error: %v", err)
	}
	if in != nil {
		t.Fatal("pair_init yields no inbound to dispatch")
	}
	if len(*got) != 1 {
		t.Fatalf("expected 1 pair_result write, got %d", len(*got))
	}
	pr, ok := (*got)[0].(pairResult)
	if !ok {
		t.Fatalf("want pairResult, got %T", (*got)[0])
	}
	sealed, _ := base64.StdEncoding.DecodeString(pr.Sealed)
	payload, err := e2ee.OpenPairing(sealed, enrollPub, deviceSec)
	if err != nil {
		t.Fatalf("OpenPairing: %v", err)
	}
	if _, ok := s.devices.Lookup(payload.KeyID); !ok {
		t.Fatal("paired device not persisted")
	}
}

// TestWritePumpHoldsUntilArmed drives the real writePump goroutine: under E2EE an
// outbound frame queued BEFORE the codec arms must be held (never leaked as
// cleartext) and then flushed SEALED once bindConn arms the connection. This is the
// one integration path the seam units don't cover; run under -race it also proves
// the armed-close happens-before publishes the key fields to writePump.
func TestWritePumpHoldsUntilArmed(t *testing.T) {
	s, _, _ := newE2EEServer(t)
	psk := bytes.Repeat([]byte{0x11}, 32)
	keyID := "k_pump_01"
	if err := s.devices.Add(devstore.Device{KeyID: keyID, Name: "dev", PSK: b64(psk)}); err != nil {
		t.Fatal(err)
	}
	_, kB2P, _ := e2ee.DeriveKeys(psk, keyID)

	writes := make(chan any, 4)
	c := &conn{
		send:     make(chan any, 8),
		done:     make(chan struct{}),
		id:       "pump",
		wantE2EE: true,
		armed:    make(chan struct{}),
		write:    func(_ context.Context, v any) error { writes <- v; return nil },
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.writePump(ctx)

	// Queue a frame before arming: it must be held (no write yet).
	c.push(map[string]any{"type": "held"})
	select {
	case <-writes:
		t.Fatal("frame written before codec armed (cleartext leak)")
	case <-time.After(100 * time.Millisecond):
	}

	// Arm the codec: the held frame must now flush, sealed under kB2P at seq 0.
	if err := s.bindConn(c, keyID); err != nil {
		t.Fatalf("bindConn: %v", err)
	}
	select {
	case v := <-writes:
		env, ok := v.(e2ee.Envelope)
		if !ok {
			t.Fatalf("held frame not sealed: %T", v)
		}
		plain, err := e2ee.Open(kB2P, keyID, e2ee.DirB2P, 0, env)
		if err != nil {
			t.Fatalf("open sealed frame: %v", err)
		}
		var m map[string]any
		_ = json.Unmarshal(plain, &m)
		if m["type"] != "held" {
			t.Fatalf("decoded %v", m)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("held frame never flushed after arming")
	}
}

func TestRouteInboundPairInitUnknownDisconnects(t *testing.T) {
	s, _, _ := newE2EEServer(t)
	devicePub, _, _ := e2ee.GenDeviceKeypair()
	c, got := captureConn()
	raw, _ := json.Marshal(pairInit{Type: "pair_init", PairingID: "nope", DevicePub: b64(devicePub[:]), Name: "px"})

	_, err := s.routeInbound(context.Background(), c, raw)
	if err != errReject {
		t.Fatalf("unknown pairing should disconnect, got %v", err)
	}
	if len(*got) != 1 {
		t.Fatalf("expected pair_error written, got %d writes", len(*got))
	}
	if _, ok := (*got)[0].(pairError); !ok {
		t.Fatalf("want pairError, got %T", (*got)[0])
	}
}
