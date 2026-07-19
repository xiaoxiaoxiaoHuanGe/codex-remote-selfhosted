package e2ee

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strconv"

	"golang.org/x/crypto/chacha20poly1305"
)

// Envelope is the wire form of one encrypted frame. The JSON field names k,s,n,c
// MUST byte-match the Dart side.
type Envelope struct {
	K string `json:"k"` // keyId (plaintext, selects the PSK)
	S uint64 `json:"s"` // seq, monotonic per direction
	N string `json:"n"` // nonce, base64 std, 24 bytes
	C string `json:"c"` // ciphertext||tag, base64 std
}

// Dir is the frame direction; it is part of the AAD and selects the subkey.
type Dir uint8

const (
	DirP2B Dir = iota // phone -> bridge
	DirB2P            // bridge -> phone
)

// String renders the direction tag used inside the AAD ("p2b" / "b2p").
func (d Dir) String() string {
	if d == DirB2P {
		return "b2p"
	}
	return "p2b"
}

// Sentinel errors. Callers map ALL of them to the same action: audit + disconnect,
// leaking no detail to the peer.
var (
	ErrSeq         = errors.New("e2ee: sequence mismatch")
	ErrAuth        = errors.New("e2ee: auth failed")
	ErrNonceLen    = errors.New("e2ee: bad nonce length")
	ErrKeyMismatch = errors.New("e2ee: keyId mismatch")
)

// aad builds the additional-authenticated-data bytes: ASCII "keyId|dir|seq".
// Seal and Open both use it so they stay in lockstep; Dart must produce the same
// bytes.
func aad(keyID string, dir Dir, seq uint64) []byte {
	b := make([]byte, 0, len(keyID)+1+3+1+20)
	b = append(b, keyID...)
	b = append(b, '|')
	b = append(b, dir.String()...)
	b = append(b, '|')
	b = strconv.AppendUint(b, seq, 10)
	return b
}

// Seal encrypts plaintext with XChaCha20-Poly1305 under key (a 32-byte directional
// subkey) using a fresh 24-byte random nonce. AAD = aad(keyID, dir, seq).
func Seal(key []byte, keyID string, dir Dir, seq uint64, plaintext []byte) (Envelope, error) {
	nonce := make([]byte, chacha20poly1305.NonceSizeX)
	if _, err := rand.Read(nonce); err != nil {
		return Envelope{}, err
	}
	return sealWithNonce(key, keyID, dir, seq, nonce, plaintext)
}

// sealWithNonce is the deterministic-nonce variant used only by tests to pin a
// cross-language vector. Production code uses Seal (random nonce).
func sealWithNonce(key []byte, keyID string, dir Dir, seq uint64, nonce, plaintext []byte) (Envelope, error) {
	if len(nonce) != chacha20poly1305.NonceSizeX {
		return Envelope{}, ErrNonceLen
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return Envelope{}, err
	}
	ct := aead.Seal(nil, nonce, plaintext, aad(keyID, dir, seq))
	return Envelope{
		K: keyID,
		S: seq,
		N: base64.StdEncoding.EncodeToString(nonce),
		C: base64.StdEncoding.EncodeToString(ct),
	}, nil
}

// Open decrypts env. It verifies env.K == keyID and env.S == expectSeq (the caller
// enforces monotonicity by passing the next expected seq), then opens the AEAD
// with AAD = aad(keyID, dir, env.S). Every failure returns one of the sentinel
// errors; the caller must not surface the distinction to the peer.
func Open(key []byte, keyID string, dir Dir, expectSeq uint64, env Envelope) ([]byte, error) {
	if env.K != keyID {
		return nil, ErrKeyMismatch
	}
	if env.S != expectSeq {
		return nil, ErrSeq
	}
	nonce, err := base64.StdEncoding.DecodeString(env.N)
	if err != nil || len(nonce) != chacha20poly1305.NonceSizeX {
		return nil, ErrNonceLen
	}
	ct, err := base64.StdEncoding.DecodeString(env.C)
	if err != nil {
		return nil, ErrAuth
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	plain, err := aead.Open(nil, nonce, ct, aad(keyID, dir, env.S))
	if err != nil {
		return nil, ErrAuth
	}
	return plain, nil
}
