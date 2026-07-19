package e2ee

import (
	"bytes"
	"testing"
)

func TestDeriveKeysDeterministicAndDistinct(t *testing.T) {
	psk := make([]byte, 32)
	for i := range psk {
		psk[i] = byte(i + 1)
	}
	a1, b1, err := DeriveKeys(psk, "k_test_0001")
	if err != nil {
		t.Fatal(err)
	}
	a2, b2, _ := DeriveKeys(psk, "k_test_0001")
	if !bytes.Equal(a1, a2) || !bytes.Equal(b1, b2) {
		t.Fatal("DeriveKeys not deterministic")
	}
	if bytes.Equal(a1, b1) {
		t.Fatal("kP2B == kB2P (the two directions must differ)")
	}
	if len(a1) != 32 || len(b1) != 32 {
		t.Fatalf("subkey length: kP2B=%d kB2P=%d, want 32", len(a1), len(b1))
	}
	a3, _, _ := DeriveKeys(psk, "k_test_0002")
	if bytes.Equal(a1, a3) {
		t.Fatal("salt (keyId) does not affect derivation")
	}
}
