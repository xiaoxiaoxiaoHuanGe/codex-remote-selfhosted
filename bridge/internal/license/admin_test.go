package license

import (
	"errors"
	"testing"
	"time"
)

func TestIssueListRevokeExtend(t *testing.T) {
	svc := tempSvc(t)
	base := time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC)
	svc.Now = func() time.Time { return base }

	key, err := svc.IssueKey("beta", 3, 6)
	if err != nil {
		t.Fatalf("IssueKey: %v", err)
	}
	if !ValidKeyFormat(key) {
		t.Fatalf("issued key malformed: %q", key)
	}

	infos, err := svc.ListKeys()
	if err != nil || len(infos) != 1 {
		t.Fatalf("ListKeys: %v (n=%d)", err, len(infos))
	}
	ki := infos[0]
	if ki.Plan != "beta" || ki.Limit != 3 || ki.Used != 0 || ki.Revoked {
		t.Fatalf("bad info: %+v", ki)
	}
	if ki.Prefix != key[:12] {
		t.Fatalf("prefix %q, want %q", ki.Prefix, key[:12])
	}
	if want := base.AddDate(0, 6, 0); !ki.ExpiresAt.Equal(want) {
		t.Fatalf("expires %v, want %v", ki.ExpiresAt, want)
	}

	if err := svc.ExtendKey(key, 6); err != nil {
		t.Fatalf("ExtendKey: %v", err)
	}
	infos, _ = svc.ListKeys()
	if want := base.AddDate(0, 12, 0); !infos[0].ExpiresAt.Equal(want) {
		t.Fatalf("after extend: %v, want %v", infos[0].ExpiresAt, want)
	}

	if err := svc.RevokeKey(key); err != nil {
		t.Fatalf("RevokeKey: %v", err)
	}
	infos, _ = svc.ListKeys()
	if !infos[0].Revoked {
		t.Fatal("not revoked")
	}

	ghost, _ := NewKey() // valid format, never issued
	if err := svc.RevokeKey(ghost); !errors.Is(err, ErrBadKey) {
		t.Fatalf("revoke unknown: %v, want ErrBadKey", err)
	}
	if err := svc.ExtendKey("garbage", 1); !errors.Is(err, ErrBadKey) {
		t.Fatalf("extend garbage: %v, want ErrBadKey", err)
	}
}
