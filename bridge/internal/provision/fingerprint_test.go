package provision

import "testing"

func TestFingerprintHintStableAndOpaque(t *testing.T) {
	a, b := FingerprintHint(), FingerprintHint()
	if a != b {
		t.Fatalf("not stable across calls: %q vs %q", a, b)
	}
	// "" is legal (unsupported platform / lookup failure); when present it must
	// be the 32-hex-char truncated hash, never a raw UUID
	if a != "" && len(a) != 32 {
		t.Fatalf("want 32 hex chars or empty, got %q (len %d)", a, len(a))
	}
}
