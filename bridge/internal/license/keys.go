package license

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"strings"
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewKey mints a license key: "crk_" + 26 base32 chars (16 random bytes) + 4
// checksum chars. The checksum catches typos client-side before any network
// round-trip; authorization is always the server-side hash lookup.
func NewKey() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	body := strings.ToLower(b32.EncodeToString(raw))
	return "crk_" + body + keyChecksum(body), nil
}

// ValidKeyFormat reports whether k is structurally a crk_ key with a good
// checksum. Typo detection ONLY — never an authorization check.
func ValidKeyFormat(k string) bool {
	if !strings.HasPrefix(k, "crk_") || len(k) != 34 {
		return false
	}
	body, sum := k[4:30], k[30:]
	return keyChecksum(body) == sum
}

func keyChecksum(body string) string {
	sum := sha256.Sum256([]byte("crk:" + body))
	return strings.ToLower(b32.EncodeToString(sum[:]))[:4]
}

// NewCredential mints a per-machine credential ("mc_" + 64 hex chars), issued
// at activation, presented in every agent register, stored hashed.
func NewCredential() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return "mc_" + hex.EncodeToString(raw), nil
}

// hashSecret is the storage form of every key/credential: SHA-256 hex.
func hashSecret(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
