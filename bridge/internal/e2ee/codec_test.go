package e2ee

import (
	"bytes"
	"encoding/base64"
	"testing"
)

func TestAADFormat(t *testing.T) {
	if got, want := string(aad("k_test_0001", DirP2B, 0)), "k_test_0001|p2b|0"; got != want {
		t.Fatalf("aad = %q, want %q", got, want)
	}
	if got, want := string(aad("kid", DirB2P, 42)), "kid|b2p|42"; got != want {
		t.Fatalf("aad = %q, want %q", got, want)
	}
}

func TestSealOpenRoundTrip(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	pt := []byte(`{"type":"prompt","text":"hi"}`)
	env, err := Seal(key, "kid", DirP2B, 3, pt)
	if err != nil {
		t.Fatal(err)
	}
	if env.K != "kid" || env.S != 3 {
		t.Fatalf("envelope meta: %+v", env)
	}
	got, err := Open(key, "kid", DirP2B, 3, env)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, pt) {
		t.Fatalf("round-trip mismatch: %q", got)
	}
}

func TestOpenRejectsTamper(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	env, _ := Seal(key, "kid", DirP2B, 0, []byte("secret"))
	ct, _ := base64.StdEncoding.DecodeString(env.C)
	ct[0] ^= 0xff
	env.C = base64.StdEncoding.EncodeToString(ct)
	if _, err := Open(key, "kid", DirP2B, 0, env); err != ErrAuth {
		t.Fatalf("tamper: got %v, want ErrAuth", err)
	}
}

func TestOpenRejectsWrongKey(t *testing.T) {
	env, _ := Seal(bytes.Repeat([]byte{1}, 32), "kid", DirP2B, 0, []byte("x"))
	if _, err := Open(bytes.Repeat([]byte{2}, 32), "kid", DirP2B, 0, env); err != ErrAuth {
		t.Fatalf("wrong key: got %v, want ErrAuth", err)
	}
}

func TestOpenRejectsSeqMismatch(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	env, _ := Seal(key, "kid", DirP2B, 5, []byte("x"))
	if _, err := Open(key, "kid", DirP2B, 6, env); err != ErrSeq {
		t.Fatalf("seq: got %v, want ErrSeq", err)
	}
}

func TestOpenRejectsKeyIDMismatch(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	env, _ := Seal(key, "kid", DirP2B, 0, []byte("x"))
	if _, err := Open(key, "other", DirP2B, 0, env); err != ErrKeyMismatch {
		t.Fatalf("keyId: got %v, want ErrKeyMismatch", err)
	}
}

func TestSealRejectsBadNonceLen(t *testing.T) {
	if _, err := sealWithNonce(bytes.Repeat([]byte{7}, 32), "kid", DirP2B, 0, []byte("short"), []byte("x")); err != ErrNonceLen {
		t.Fatalf("nonce len: got %v, want ErrNonceLen", err)
	}
}

// TestPrintVector is the cross-language oracle: with the FIXED inputs it prints
// base64(kP2B), base64(kB2P) and base64(ciphertext). Run with -v and paste the
// printed constants into the Dart tests (T9). Not an assertion — a producer.
func TestPrintVector(t *testing.T) {
	psk := make([]byte, 32)
	for i := range psk {
		psk[i] = byte(i + 1) // 0x01..0x20
	}
	const keyID = "k_test_0001"
	kP2B, kB2P, err := DeriveKeys(psk, keyID)
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, 24)
	for i := range nonce {
		nonce[i] = byte(0xA0 + i) // 0xA0..0xB7
	}
	plain := []byte(`{"type":"prompt","text":"hello e2ee"}`)
	env, err := sealWithNonce(kP2B, keyID, DirP2B, 0, nonce, plain)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("VECTOR kP2B = %s", base64.StdEncoding.EncodeToString(kP2B))
	t.Logf("VECTOR kB2P = %s", base64.StdEncoding.EncodeToString(kB2P))
	t.Logf("VECTOR ciphertext = %s", env.C)
}
