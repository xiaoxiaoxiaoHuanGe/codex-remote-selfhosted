package devstore

import (
	"encoding/json"
	"errors"
	"os"
	"sync"
	"time"
)

// Pending is one outstanding one-time pairing id with its expiry.
type Pending struct {
	PairingID string `json:"pairingId"`
	ExpiresAt string `json:"expiresAt"` // RFC3339
}

// PendingStore manages pending-pairings.json (array of Pending), mode 0600. The
// tray writes; the bridge consumes single-use.
type PendingStore struct {
	path string
	mu   sync.Mutex
}

// Pairing-consumption errors. A reused id reads back as ErrPairUnknown (the wire
// reason collapses consumed -> unknown, per decision D-e).
var (
	ErrPairUnknown = errors.New("devstore: unknown pairingId")
	ErrPairExpired = errors.New("devstore: pairing expired")
)

// OpenPending returns a PendingStore over path (the file need not exist yet).
func OpenPending(path string) *PendingStore {
	return &PendingStore{path: path}
}

func loadPending(path string) ([]Pending, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var list []Pending
	if len(b) > 0 {
		if err := json.Unmarshal(b, &list); err != nil {
			return nil, err
		}
	}
	return list, nil
}

// AddPending appends a pairingId with ExpiresAt = now+ttl, purges already-expired
// entries, and persists atomically.
func (p *PendingStore) AddPending(pairingID string, ttl time.Duration, now time.Time) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	list, err := loadPending(p.path)
	if err != nil {
		return err
	}
	kept := purgeExpired(list, now)
	kept = append(kept, Pending{
		PairingID: pairingID,
		ExpiresAt: now.Add(ttl).UTC().Format(time.RFC3339),
	})
	return writeJSONAtomic(p.path, kept)
}

// Consume redeems a pairingId at most once: ErrPairUnknown if absent (or already
// consumed), ErrPairExpired if past its expiry, else removes it and persists.
func (p *PendingStore) Consume(pairingID string, now time.Time) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	list, err := loadPending(p.path)
	if err != nil {
		return err
	}
	idx := -1
	for i, pp := range list {
		if pp.PairingID == pairingID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return ErrPairUnknown
	}
	exp, perr := time.Parse(time.RFC3339, list[idx].ExpiresAt)
	expired := perr == nil && now.After(exp)
	// Remove the entry either way (consume or expire) and purge other stale ids.
	list = append(list[:idx], list[idx+1:]...)
	if err := writeJSONAtomic(p.path, purgeExpired(list, now)); err != nil {
		return err
	}
	if expired {
		return ErrPairExpired
	}
	return nil
}

func purgeExpired(list []Pending, now time.Time) []Pending {
	out := make([]Pending, 0, len(list))
	for _, pp := range list {
		if exp, err := time.Parse(time.RFC3339, pp.ExpiresAt); err == nil && now.After(exp) {
			continue
		}
		out = append(out, pp)
	}
	return out
}
