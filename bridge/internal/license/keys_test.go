package license

import (
	"strings"
	"testing"
)

func TestNewKeyFormat(t *testing.T) {
	k, err := NewKey()
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}
	if !strings.HasPrefix(k, "crk_") || len(k) != 34 {
		t.Fatalf("bad shape: %q (len %d)", k, len(k))
	}
	if !ValidKeyFormat(k) {
		t.Fatalf("fresh key fails its own checksum: %q", k)
	}
	k2, _ := NewKey()
	if k == k2 {
		t.Fatal("two keys identical")
	}
}

func TestValidKeyFormatCatchesTypo(t *testing.T) {
	k, _ := NewKey()
	b := []byte(k)
	if b[10] == 'a' {
		b[10] = 'b'
	} else {
		b[10] = 'a'
	}
	if ValidKeyFormat(string(b)) {
		t.Fatal("single-char typo passed the checksum")
	}
	if ValidKeyFormat("crk_short") || ValidKeyFormat("") {
		t.Fatal("malformed keys accepted")
	}
}

func TestNewCredentialShape(t *testing.T) {
	c, err := NewCredential()
	if err != nil {
		t.Fatalf("NewCredential: %v", err)
	}
	if !strings.HasPrefix(c, "mc_") || len(c) != 3+64 {
		t.Fatalf("bad shape: %q (len %d)", c, len(c))
	}
	c2, _ := NewCredential()
	if c == c2 {
		t.Fatal("two credentials identical")
	}
}
