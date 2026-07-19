// Package e2ee implements the control-plane end-to-end encryption codec for the
// phone <-> bridge protocol. The relay (hub/nginx/frp) only ever sees ciphertext.
//
// Wire envelope — one JSON object per encrypted frame, short keys so the relay
// can route but not read:
//
//	{ "k": "<keyId>", "s": <seq>, "n": "<base64 24-byte nonce>", "c": "<base64 ct||tag>" }
//
//   - k: plaintext keyId; selects the per-device PSK on the recipient (non-secret).
//   - s: monotonic per-direction sequence; the recipient rejects a non-increasing
//     s (replay/reorder defense).
//   - n: fresh 24-byte random nonce per frame.
//   - c: XChaCha20-Poly1305 ciphertext with the 16-byte tag appended.
//
// AAD = "<keyId>|<dir>|<seq>" (ASCII), binding each frame to its key, direction
// and sequence. dir is "p2b" (phone->bridge) or "b2p" (bridge->phone). The Dart
// client MUST build byte-identical AAD.
//
// Keys: a 32-byte per-device PSK is expanded by HKDF-SHA256 (salt = keyId bytes)
// into two 32-byte directional subkeys (DeriveKeys) so the two directions never
// share a nonce space.
//
// Primitives: XChaCha20-Poly1305 (golang.org/x/crypto/chacha20poly1305) for
// content; NaCl box (golang.org/x/crypto/nacl/box, see pairing.go) for the
// one-time pairing seal.
package e2ee
