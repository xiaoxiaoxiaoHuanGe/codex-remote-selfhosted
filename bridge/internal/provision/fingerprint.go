package provision

import (
	"crypto/sha256"
	"encoding/hex"
)

// FingerprintHint returns a stable, non-reversible hardware hint (truncated
// SHA-256 of the platform UUID) or "" when unavailable. Activation uses it to
// recognize a reinstalled machine and reuse its roster slot without burning a
// swap; it is NEVER a hard gate (VMs / board swaps would misfire) — see the
// license package doc.
func FingerprintHint() string {
	u := platformUUID()
	if u == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("fp:" + u))
	return hex.EncodeToString(sum[:16])
}
