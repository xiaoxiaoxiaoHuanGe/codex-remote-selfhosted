package e2ee

import (
	"crypto/rand"
	"encoding/json"
	"errors"

	"golang.org/x/crypto/nacl/box"
)

// PairPayload is the secret handed to a freshly paired device, sealed inside a
// NaCl box so ONLY that device (holding the matching Curve25519 secret) can open
// it. It never travels in cleartext and is never persisted in this form.
type PairPayload struct {
	KeyID string `json:"keyId"` // selects this device's PSK on every later connect
	PSK   string `json:"psk"`   // base64 std, 32 bytes — the per-device pre-shared key
}

// ErrPairOpen is the single error OpenPairing returns on any failure (bad length,
// auth failure, malformed JSON). The distinction is never surfaced to the peer.
var ErrPairOpen = errors.New("e2ee: pairing open failed")

// pairNonceLen is the NaCl box nonce size; the sealed blob is framed nonce||box.
const pairNonceLen = 24

// GenEnrollKeypair generates the bridge's per-pairing enrollment keypair. The
// public half is published in the tray QR (non-secret); the secret half seals the
// PairPayload to the device's public key.
func GenEnrollKeypair() (pub, sec *[32]byte, err error) {
	return box.GenerateKey(rand.Reader)
}

// GenDeviceKeypair generates the phone's keypair. The phone keeps sec and sends
// pub in pair_init; the bridge seals the PairPayload to pub.
func GenDeviceKeypair() (pub, sec *[32]byte, err error) {
	return box.GenerateKey(rand.Reader)
}

// SealPairing seals payload (marshaled to JSON) TO devicePub using enrollSec,
// framing the result as nonce||box so OpenPairing can split it back. A fresh
// random nonce is used per seal.
func SealPairing(payload PairPayload, devicePub, enrollSec *[32]byte) ([]byte, error) {
	pt, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	var nonce [pairNonceLen]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	// box.Seal appends the box to the nonce prefix → output is nonce||box.
	return box.Seal(nonce[:], pt, &nonce, devicePub, enrollSec), nil
}

// OpenPairing reverses SealPairing: it splits nonce||box and opens the box with
// enrollPub + deviceSec, returning the decoded PairPayload. This is the PHONE's
// operation — the bridge only seals — but it lives here so both sides share one
// framing and Go round-trip tests can exercise the full path.
func OpenPairing(sealed []byte, enrollPub, deviceSec *[32]byte) (PairPayload, error) {
	if len(sealed) < pairNonceLen {
		return PairPayload{}, ErrPairOpen
	}
	var nonce [pairNonceLen]byte
	copy(nonce[:], sealed[:pairNonceLen])
	pt, ok := box.Open(nil, sealed[pairNonceLen:], &nonce, enrollPub, deviceSec)
	if !ok {
		return PairPayload{}, ErrPairOpen
	}
	var payload PairPayload
	if err := json.Unmarshal(pt, &payload); err != nil {
		return PairPayload{}, ErrPairOpen
	}
	return payload, nil
}
