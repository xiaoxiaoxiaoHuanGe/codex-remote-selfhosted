package devstore

import (
	"path/filepath"
	"testing"
	"time"
)

func tmpPending(t *testing.T) *PendingStore {
	t.Helper()
	return OpenPending(filepath.Join(t.TempDir(), "pending-pairings.json"))
}

func TestPendingConsumeValidThenReuse(t *testing.T) {
	p := tmpPending(t)
	now := time.Date(2026, 6, 9, 0, 0, 0, 0, time.UTC)
	if err := p.AddPending("pid1", 5*time.Minute, now); err != nil {
		t.Fatal(err)
	}
	if err := p.Consume("pid1", now.Add(time.Minute)); err != nil {
		t.Fatalf("consume valid: %v", err)
	}
	if err := p.Consume("pid1", now.Add(time.Minute)); err != ErrPairUnknown {
		t.Fatalf("reuse (single-use): got %v, want ErrPairUnknown", err)
	}
}

func TestPendingConsumeExpired(t *testing.T) {
	p := tmpPending(t)
	now := time.Date(2026, 6, 9, 0, 0, 0, 0, time.UTC)
	_ = p.AddPending("pid1", time.Minute, now)
	if err := p.Consume("pid1", now.Add(2*time.Minute)); err != ErrPairExpired {
		t.Fatalf("expired: got %v, want ErrPairExpired", err)
	}
	if err := p.Consume("pid1", now.Add(2*time.Minute)); err != ErrPairUnknown {
		t.Fatalf("post-expiry reuse: got %v, want ErrPairUnknown", err)
	}
}

func TestPendingConsumeUnknown(t *testing.T) {
	p := tmpPending(t)
	if err := p.Consume("nope", time.Now()); err != ErrPairUnknown {
		t.Fatalf("unknown: got %v, want ErrPairUnknown", err)
	}
}

func TestPendingAddPurgesExpired(t *testing.T) {
	p := tmpPending(t)
	now := time.Date(2026, 6, 9, 0, 0, 0, 0, time.UTC)
	_ = p.AddPending("old", time.Minute, now)
	// Adding a new one 2 minutes later must purge the expired "old".
	_ = p.AddPending("new", 5*time.Minute, now.Add(2*time.Minute))
	if err := p.Consume("old", now.Add(2*time.Minute)); err != ErrPairUnknown {
		t.Fatalf("expired 'old' should have been purged: got %v", err)
	}
	if err := p.Consume("new", now.Add(2*time.Minute)); err != nil {
		t.Fatalf("'new' should still be consumable: %v", err)
	}
}
