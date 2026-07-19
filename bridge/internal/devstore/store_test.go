package devstore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func tmpStore(t *testing.T) (*Store, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "devices.json")
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	return s, p
}

func TestOpenMissingIsEmpty(t *testing.T) {
	s, _ := tmpStore(t)
	if len(s.All()) != 0 {
		t.Fatal("fresh store should be empty")
	}
	if _, ok := s.Lookup("nope"); ok {
		t.Fatal("lookup in empty store should miss")
	}
}

func TestAddLookupRevoke(t *testing.T) {
	s, p := tmpStore(t)
	d := Device{KeyID: "k1", Name: "ipad", DevicePub: "cHViMQ==", PSK: "cHNr", Added: "2026-06-09T00:00:00Z"}
	if err := s.Add(d); err != nil {
		t.Fatal(err)
	}
	got, ok := s.Lookup("k1")
	if !ok || got.Name != "ipad" {
		t.Fatalf("lookup: %+v ok=%v", got, ok)
	}
	// Add must persist to disk.
	s2, _ := Open(p)
	if _, ok := s2.Lookup("k1"); !ok {
		t.Fatal("Add did not persist")
	}
	ok, err := s.Revoke("k1")
	if !ok || err != nil {
		t.Fatalf("revoke: ok=%v err=%v", ok, err)
	}
	if _, ok := s.Lookup("k1"); ok {
		t.Fatal("revoked key still present")
	}
	if ok, _ := s.Revoke("k1"); ok {
		t.Fatal("revoke of an absent key should return false")
	}
}

func TestReloadPicksUpExternalEdit(t *testing.T) {
	s, p := tmpStore(t)
	_ = s.Add(Device{KeyID: "k1", Name: "a"})
	b, _ := json.Marshal([]Device{{KeyID: "k1", Name: "a"}, {KeyID: "k2", Name: "b"}})
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Reload(); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Lookup("k2"); !ok {
		t.Fatal("reload missed externally-added k2")
	}
}

func TestReloadParseErrorKeepsOldMap(t *testing.T) {
	s, p := tmpStore(t)
	_ = s.Add(Device{KeyID: "k1", Name: "a"})
	if err := os.WriteFile(p, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Reload(); err == nil {
		t.Fatal("expected a parse error")
	}
	if _, ok := s.Lookup("k1"); !ok {
		t.Fatal("a parse error must keep the old in-memory map")
	}
}

func TestFilePerms0600(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permissions only")
	}
	s, p := tmpStore(t)
	_ = s.Add(Device{KeyID: "k1"})
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("perms = %o, want 600", fi.Mode().Perm())
	}
}
