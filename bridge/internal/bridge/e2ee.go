package bridge

// E2EE control-plane seam (shared by direct /ws and the hub agent transport).
//
// This file holds the transport-agnostic codec wiring; handleWS (server.go) and
// the agent link (agent.go) call into it so both speak the same encrypted phone
// protocol while the hub stays protocol-blind.
//
// Inbound  : routeInbound classifies/decrypts one raw frame into an inbound, or
//            reports a fatal errReject (the caller MUST disconnect — no fall-through
//            to cleartext). The first frame is either a plaintext pair_init or an
//            encrypted envelope; every other cleartext frame is hard-cut.
// Outbound : (*conn).sealAndWrite seals bridge->phone frames once the codec arms,
//            and passes through cleartext before arming / with E2EE off.
// Pairing  : a one-time pair_init (carrying no secret) is consumed single-use, a
//            fresh (keyId, PSK) is generated, the PSK is sealed to the device's
//            public key (NaCl box) and the device persisted; the phone derives the
//            same directional subkeys and continues on the same socket.
//
// Security rules that must not weaken:
//   - Any decode/decrypt/parse/pairing failure returns errReject → disconnect,
//     leaking no distinction to the peer.
//   - seq is strictly monotonic per direction (replay/reorder defense via e2ee.Open).
//   - bindConn sets c.device to the paired device name, strengthening hub-mode
//     approval-ownership binding. The downstream clamps (turn/cwd/approval/file)
//     are untouched: they run post-decode in s.handle, so they apply identically.

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	"github.com/yunyuchen/codex-remote/bridge/internal/devstore"
	"github.com/yunyuchen/codex-remote/bridge/internal/e2ee"
)

// errReject is the single fatal outcome of the inbound seam: the caller MUST tear
// the connection down. It deliberately carries no detail — the peer learns only
// that the socket closed, never which check failed.
var errReject = errors.New("bridge: e2ee frame rejected")

// pairInit is the phone's plaintext first frame requesting a one-time pairing. It
// carries NO secret: pairingId proves QR possession, devicePub receives the seal,
// name labels the device in the registry / revoke UI.
type pairInit struct {
	Type      string `json:"type"`      // "pair_init"
	PairingID string `json:"pairingId"` // single-use id from the tray QR
	DevicePub string `json:"devicePub"` // base64 std, 32-byte Curve25519 public key
	Name      string `json:"name"`      // device label
}

// pairResult delivers the sealed PairPayload back to the phone (base64 of nonce||box).
type pairResult struct {
	Type   string `json:"type"`   // "pair_result"
	Sealed string `json:"sealed"` // base64 std
}

// pairError is the coarse, non-leaky pairing rejection. Reason is intentionally
// vague ("bad_request" | "pairing" | "internal") — never the specific cause.
type pairError struct {
	Type   string `json:"type"` // "pair_error"
	Reason string `json:"reason"`
}

// EnableE2EE turns on the encrypted control-plane: it installs the device registry,
// the bridge enrollment secret, and the pending-pairing store. Call once at startup
// before serving. While unset, the bridge speaks legacy cleartext.
func (s *Server) EnableE2EE(devices *devstore.Store, enrollSec *[32]byte, pending *devstore.PendingStore) {
	s.devices = devices
	s.enrollSec = enrollSec
	s.pending = pending
	s.devicesChanged = make(chan struct{}, 1) // coalescing: one pending re-sync is enough
}

// NotifyDevicesChanged signals agent mode to re-sync device public keys to the hub
// after the registry changed (a tray pair or revoke). Non-blocking and coalescing:
// if a signal is already queued it's dropped, since one re-sync replays the whole
// current list. A no-op when E2EE is off. Call from the devices.Watch reload hook.
func (s *Server) NotifyDevicesChanged() {
	if s.devicesChanged == nil {
		return
	}
	select {
	case s.devicesChanged <- struct{}{}:
	default:
	}
}

// e2eeOn reports whether the encrypted control-plane is active.
func (s *Server) e2eeOn() bool { return s.devices != nil }

// firstFrameKind classifies a raw first frame WITHOUT trusting it: "pair_init"
// (plaintext pairing request), "envelope" (encrypted), or "other". Under E2EE only
// the first two are accepted as a first frame; "other" is hard-cut.
func firstFrameKind(data []byte) string {
	var probe struct {
		Type string `json:"type"`
		K    string `json:"k"`
	}
	if json.Unmarshal(data, &probe) != nil {
		return "other"
	}
	if probe.Type == "pair_init" {
		return "pair_init"
	}
	if probe.K != "" {
		return "envelope"
	}
	return "other"
}

// routeInbound consumes one raw inbound frame and returns exactly one of:
//   - (&in, nil): a decoded message to dispatch to s.handle
//   - (nil, nil): a control frame (pairing) fully handled; nothing to dispatch
//   - (nil, err): fatal — the caller MUST disconnect
func (s *Server) routeInbound(ctx context.Context, c *conn, data []byte) (*inbound, error) {
	// Legacy cleartext path: E2EE not configured at all.
	if !s.e2eeOn() {
		var in inbound
		if err := json.Unmarshal(data, &in); err != nil {
			return nil, errReject
		}
		return &in, nil
	}
	// Already-armed connection: every subsequent frame must be an envelope.
	if c.e2ee {
		in, err := s.openEnvelope(c, data)
		if err != nil {
			return nil, err
		}
		return &in, nil
	}
	// Unbound under E2EE: classify the very first frame.
	switch firstFrameKind(data) {
	case "pair_init":
		return nil, s.doPairing(ctx, c, data)
	case "envelope":
		in, err := s.openEnvelope(c, data)
		if err != nil {
			return nil, err
		}
		// First armed frame: now safe to re-surface pending approvals encrypted.
		s.resyncTo(c)
		s.sendLanInfo(c)
		return &in, nil
	default:
		s.audit("e2ee hard-cut first frame conn=%s", c.id)
		return nil, errReject
	}
}

// openEnvelope decrypts one phone->bridge frame into an inbound. On the first frame
// it binds the connection from the envelope's plaintext keyId (stateless reconnect).
// Any failure returns errReject and the caller disconnects.
func (s *Server) openEnvelope(c *conn, data []byte) (inbound, error) {
	var env e2ee.Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return inbound{}, errReject
	}
	if !c.e2ee {
		if err := s.bindConn(c, env.K); err != nil {
			return inbound{}, err
		}
	}
	plain, err := e2ee.Open(c.kP2B, c.keyID, e2ee.DirP2B, c.seqIn, env)
	if err != nil {
		s.audit("e2ee open FAILED conn=%s keyId=%s seq=%d: %v", c.id, c.keyID, c.seqIn, err)
		return inbound{}, errReject
	}
	c.seqIn++
	var in inbound
	if err := json.Unmarshal(plain, &in); err != nil {
		return inbound{}, errReject
	}
	return in, nil
}

// bindConn resolves keyID to a paired device, derives its directional subkeys, and
// arms the connection's codec. A keyId with no device (revoked / never paired) is
// rejected. Closing c.armed AFTER the key fields are set publishes them to the
// writePump goroutine, so sealAndWrite never reads them mid-write.
func (s *Server) bindConn(c *conn, keyID string) error {
	dev, ok := s.devices.Lookup(keyID)
	if !ok {
		s.audit("e2ee unknown keyId=%s conn=%s", trunc(keyID, 16), c.id)
		return errReject
	}
	psk, err := base64.StdEncoding.DecodeString(dev.PSK)
	if err != nil || len(psk) != 32 {
		s.audit("e2ee bad psk for keyId=%s", trunc(keyID, 16))
		return errReject
	}
	kP2B, kB2P, err := e2ee.DeriveKeys(psk, keyID)
	if err != nil {
		return errReject
	}
	c.keyID = keyID
	c.kP2B = kP2B
	c.kB2P = kB2P
	c.device = dev.Name // strengthens hub-mode approval-ownership binding
	c.e2ee = true
	if c.armed != nil {
		c.armedOnce.Do(func() { close(c.armed) })
	}
	s.audit("e2ee armed conn=%s keyId=%s device=%q", c.id, keyID, dev.Name)
	return nil
}

// doPairing handles a plaintext pair_init: it runs handlePairInit and writes the
// reply (pair_result / pair_error) as CLEARTEXT — the phone has no key yet —
// synchronously so the reply isn't lost to the disconnect that follows a failure.
func (s *Server) doPairing(ctx context.Context, c *conn, data []byte) error {
	var in pairInit
	if err := json.Unmarshal(data, &in); err != nil {
		return errReject
	}
	resp, disconnect := s.handlePairInit(in)
	wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	_ = c.write(wctx, resp)
	cancel()
	if disconnect {
		return errReject
	}
	return nil
}

// handlePairInit runs the pairing handshake purely (no socket I/O). It returns the
// frame to send back and whether the caller must disconnect afterwards. Success →
// (pairResult, false): the phone derives the same subkeys and continues on the same
// socket. Any failure → (pairError, true).
func (s *Server) handlePairInit(in pairInit) (any, bool) {
	devicePub, err := decodePub(in.DevicePub)
	if err != nil {
		return pairError{Type: "pair_error", Reason: "bad_request"}, true
	}
	// Single-use: a reused/expired/unknown id is refused (collapsed to "pairing").
	if err := s.pending.Consume(in.PairingID, time.Now()); err != nil {
		s.audit("pair REJECTED pairingId=%s: %v", trunc(in.PairingID, 16), err)
		return pairError{Type: "pair_error", Reason: "pairing"}, true
	}
	keyID := NewToken() // 32 hex chars — ample keyId entropy
	psk := make([]byte, 32)
	if _, err := rand.Read(psk); err != nil {
		return pairError{Type: "pair_error", Reason: "internal"}, true
	}
	pskB64 := base64.StdEncoding.EncodeToString(psk)
	sealed, err := e2ee.SealPairing(e2ee.PairPayload{KeyID: keyID, PSK: pskB64}, devicePub, s.enrollSec)
	if err != nil {
		return pairError{Type: "pair_error", Reason: "internal"}, true
	}
	dev := devstore.Device{
		KeyID:     keyID,
		Name:      in.Name,
		DevicePub: in.DevicePub,
		PSK:       pskB64,
		Added:     time.Now().UTC().Format(time.RFC3339),
	}
	if err := s.devices.Add(dev); err != nil {
		s.audit("pair persist FAILED keyId=%s: %v", keyID, err)
		return pairError{Type: "pair_error", Reason: "internal"}, true
	}
	s.audit("pair OK device=%q keyId=%s", in.Name, keyID)
	return pairResult{Type: "pair_result", Sealed: base64.StdEncoding.EncodeToString(sealed)}, false
}

// resyncTo re-surfaces this connection's still-pending approvals after it arms its
// codec — the E2EE-mode replacement for the connect-time resync, which must not
// emit cleartext before a key is bound. Mirrors the ownership filter used elsewhere.
func (s *Server) resyncTo(c *conn) {
	for _, p := range s.pendingApprovals() {
		if p.ownerDevice == "" || p.ownerDevice == c.device {
			c.push(map[string]any{"type": "approval", "id": p.id, "method": p.method, "params": p.params})
		}
	}
}

// sealAndWrite is the outbound seam writePump uses in place of c.write. Once the
// codec is armed it seals v (bridge->phone) into an envelope and bumps seqOut;
// before arming (or with E2EE off) it passes v through as cleartext. Called only
// from the single writePump goroutine, so seqOut needs no lock.
func (c *conn) sealAndWrite(ctx context.Context, v any) error {
	if !c.e2ee {
		return c.write(ctx, v)
	}
	plain, err := json.Marshal(v)
	if err != nil {
		return err
	}
	env, err := e2ee.Seal(c.kB2P, c.keyID, e2ee.DirB2P, c.seqOut, plain)
	if err != nil {
		return err
	}
	c.seqOut++
	return c.write(ctx, env)
}

// decodePub parses a base64 std 32-byte Curve25519 public key. Any malformation is
// errReject (the caller maps it to a coarse pairing rejection).
func decodePub(b64 string) (*[32]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(raw) != 32 {
		return nil, errReject
	}
	var pub [32]byte
	copy(pub[:], raw)
	return &pub, nil
}
