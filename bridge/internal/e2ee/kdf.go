package e2ee

import (
	"crypto/hkdf"
	"crypto/sha256"

	"golang.org/x/crypto/chacha20poly1305"
)

// HKDF info strings: distinct per direction so kP2B != kB2P.
const (
	infoP2B = "codex-e2ee/v1/phone->bridge"
	infoB2P = "codex-e2ee/v1/bridge->phone"
)

// DeriveKeys expands a 32-byte per-device PSK into two 32-byte directional
// subkeys via HKDF-SHA256 (salt = keyId bytes). The phone seals phone->bridge
// frames with kP2B and opens bridge->phone frames with kB2P; the bridge mirrors.
func DeriveKeys(psk []byte, keyID string) (kP2B, kB2P []byte, err error) {
	salt := []byte(keyID)
	kP2B, err = hkdf.Key(sha256.New, psk, salt, infoP2B, chacha20poly1305.KeySize)
	if err != nil {
		return nil, nil, err
	}
	kB2P, err = hkdf.Key(sha256.New, psk, salt, infoB2P, chacha20poly1305.KeySize)
	if err != nil {
		return nil, nil, err
	}
	return kP2B, kB2P, nil
}
