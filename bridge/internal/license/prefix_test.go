package license

import (
	"errors"
	"testing"
	"time"
)

func TestRevokeKeyByPrefix(t *testing.T) {
	svc := tempSvc(t)
	key, err := svc.IssueKey("beta", 2, 6)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	if err := svc.RevokeKeyByPrefix("crk_nosuchpfx"); !errors.Is(err, ErrBadKey) {
		t.Fatalf("unknown prefix: got %v, want ErrBadKey", err)
	}
	if err := svc.RevokeKeyByPrefix(key[:12]); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	keys, err := svc.ListKeys()
	if err != nil || len(keys) != 1 {
		t.Fatalf("list: err=%v len=%d", err, len(keys))
	}
	if !keys[0].Revoked {
		t.Fatal("key not marked revoked")
	}
}

func TestExtendKeyByPrefix(t *testing.T) {
	svc := tempSvc(t)
	base := time.Date(2026, 6, 11, 0, 0, 0, 0, time.UTC)
	svc.Now = func() time.Time { return base }
	key, err := svc.IssueKey("beta", 2, 1) // 到期 2026-07-11
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if err := svc.ExtendKeyByPrefix(key[:12], 2); err != nil {
		t.Fatalf("extend: %v", err)
	}
	keys, err := svc.ListKeys()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	want := base.AddDate(0, 3, 0) // 发卡 1 个月 + 续期 2 个月
	if !keys[0].ExpiresAt.Equal(want) {
		t.Fatalf("expiry = %v, want %v", keys[0].ExpiresAt, want)
	}
}

func TestByPrefixAmbiguous(t *testing.T) {
	svc := tempSvc(t)
	k1, err := svc.IssueKey("beta", 1, 1)
	if err != nil {
		t.Fatalf("issue k1: %v", err)
	}
	k2, err := svc.IssueKey("beta", 1, 1)
	if err != nil {
		t.Fatalf("issue k2: %v", err)
	}
	// 真实 key 几乎不可能撞 12 位前缀，手工把第二行改成第一行的前缀制造歧义。
	if _, err := svc.db.Exec(`UPDATE license_keys SET prefix=? WHERE key_hash=?`,
		k1[:12], hashSecret(k2)); err != nil {
		t.Fatalf("force collision: %v", err)
	}
	if err := svc.RevokeKeyByPrefix(k1[:12]); !errors.Is(err, ErrPrefixAmbiguous) {
		t.Fatalf("revoke: got %v, want ErrPrefixAmbiguous", err)
	}
	if err := svc.ExtendKeyByPrefix(k1[:12], 1); !errors.Is(err, ErrPrefixAmbiguous) {
		t.Fatalf("extend: got %v, want ErrPrefixAmbiguous", err)
	}
}

func TestDeleteKeyByPrefix(t *testing.T) {
	svc := tempSvc(t)
	key, err := svc.IssueKey("beta", 2, 6)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	res, err := svc.Activate(key, "m-del", "Del Mac", "")
	if err != nil {
		t.Fatalf("activate: %v", err)
	}
	if _, err := svc.CheckAgent("m-del", res.Credential); err != nil {
		t.Fatalf("pre-delete check: %v", err)
	}
	if _, err := svc.DeleteKeyByPrefix("crk_nosuchpfx"); !errors.Is(err, ErrBadKey) {
		t.Fatalf("unknown prefix: got %v, want ErrBadKey", err)
	}
	gone, err := svc.DeleteKeyByPrefix(key[:12])
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if len(gone) != 1 || gone[0] != "m-del" {
		t.Fatalf("returned machines = %v, want [m-del]", gone)
	}
	if keys, _ := svc.ListKeys(); len(keys) != 0 {
		t.Fatalf("keys after delete = %d, want 0", len(keys))
	}
	if ms, _ := svc.ListMachines(); len(ms) != 0 {
		t.Fatalf("machines after delete = %d, want 0", len(ms))
	}
	if _, err := svc.CheckAgent("m-del", res.Credential); !errors.Is(err, ErrUnknownMachine) {
		t.Fatalf("post-delete check: got %v, want ErrUnknownMachine", err)
	}
}

func TestBlockUnblockMachine(t *testing.T) {
	svc := tempSvc(t)
	key, err := svc.IssueKey("beta", 2, 6)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	res, err := svc.Activate(key, "m-blk", "Blk Mac", "")
	if err != nil {
		t.Fatalf("activate: %v", err)
	}
	if _, err := svc.CheckAgent("m-blk", res.Credential); err != nil {
		t.Fatalf("pre-block check: %v", err)
	}
	if err := svc.BlockMachine("nope"); !errors.Is(err, ErrUnknownMachine) {
		t.Fatalf("block unknown: got %v, want ErrUnknownMachine", err)
	}
	if err := svc.BlockMachine("m-blk"); err != nil {
		t.Fatalf("block: %v", err)
	}
	if _, err := svc.CheckAgent("m-blk", res.Credential); !errors.Is(err, ErrMachineBlocked) {
		t.Fatalf("blocked check: got %v, want ErrMachineBlocked", err)
	}
	if ms, _ := svc.ListMachines(); len(ms) != 1 || !ms[0].Blocked {
		t.Fatalf("ListMachines blocked = %+v", ms)
	}
	if err := svc.UnblockMachine("m-blk"); err != nil {
		t.Fatalf("unblock: %v", err)
	}
	if _, err := svc.CheckAgent("m-blk", res.Credential); err != nil {
		t.Fatalf("post-unblock check: %v", err)
	}
}
