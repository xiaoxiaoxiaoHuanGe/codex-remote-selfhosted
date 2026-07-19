package e2ee

import (
	"crypto/rand"

	"golang.org/x/crypto/nacl/box"
)

// ChallengeNonceLen is the size of the random challenge a verifier (the hub) seals
// to a device's public key. The device proves possession of its secret key by
// opening the box and returning the nonce — a proof-of-possession handshake that
// needs no signing key (the device keypair is X25519, used here exactly as in
// pairing). 32 bytes gives a collision-free, unguessable single-use challenge.
const ChallengeNonceLen = 32

// SealChallenge seals nonce TO devicePub under a FRESH ephemeral keypair, returning
// the ephemeral PUBLIC key (the device needs it to open) and the framed blob
// boxNonce||box (same layout as SealPairing, so the phone reuses one open routine).
// The verifier keeps no secret: a new ephemeral key per call means the sealed blob
// reveals nothing and can't be replayed against a different device. Only the holder
// of devicePub's secret can recover nonce.
func SealChallenge(nonce []byte, devicePub *[32]byte) (ephPub *[32]byte, sealed []byte, err error) {
	ephPub, ephSec, err := box.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	var boxNonce [pairNonceLen]byte
	if _, err := rand.Read(boxNonce[:]); err != nil {
		return nil, nil, err
	}
	// box.Seal appends the box to the nonce prefix → output is boxNonce||box.
	return ephPub, box.Seal(boxNonce[:], nonce, &boxNonce, devicePub, ephSec), nil
}

// OpenChallenge reverses SealChallenge: it splits boxNonce||box and opens the box
// with ephPub + deviceSec, returning the recovered challenge nonce. This is the
// PHONE's operation — the hub only seals — but it lives here so both sides share one
// framing and Go round-trip tests can exercise the full path. Any failure (short
// blob, auth failure, tamper) returns ErrPairOpen, never a distinguishing reason.
func OpenChallenge(sealed []byte, ephPub, deviceSec *[32]byte) ([]byte, error) {
	if len(sealed) < pairNonceLen {
		return nil, ErrPairOpen
	}
	var boxNonce [pairNonceLen]byte
	copy(boxNonce[:], sealed[:pairNonceLen])
	pt, ok := box.Open(nil, sealed[pairNonceLen:], &boxNonce, ephPub, deviceSec)
	if !ok {
		return nil, ErrPairOpen
	}
	return pt, nil
}
