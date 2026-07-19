package devstore

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

// Device is one paired device, persisted in devices.json (an array), mode 0600.
type Device struct {
	KeyID     string `json:"keyId"`
	Name      string `json:"name"`
	DevicePub string `json:"devicePub"` // base64 std, 32 bytes
	Account   string `json:"account"`   // reserved, "" for now
	PSK       string `json:"psk"`       // base64 std, 32 bytes
	Added     string `json:"added"`     // RFC3339
}

// Store is the in-memory keyId->Device map mirroring devices.json on disk.
type Store struct {
	path string
	mu   sync.RWMutex
	byID map[string]Device
}

// Open loads devices.json into memory (empty if the file is missing).
func Open(path string) (*Store, error) {
	s := &Store{path: path, byID: map[string]Device{}}
	if err := s.Reload(); err != nil {
		return nil, err
	}
	return s, nil
}

func load(path string) (map[string]Device, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]Device{}, nil
	}
	if err != nil {
		return nil, err
	}
	var list []Device
	if len(b) > 0 {
		if err := json.Unmarshal(b, &list); err != nil {
			return nil, err
		}
	}
	m := make(map[string]Device, len(list))
	for _, d := range list {
		if d.KeyID != "" {
			m[d.KeyID] = d
		}
	}
	return m, nil
}

// Reload re-reads devices.json and swaps the in-memory map. On read/parse error
// it KEEPS the old map and returns the error (the caller audit-warns; no crash).
func (s *Store) Reload() error {
	m, err := load(s.path)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.byID = m
	s.mu.Unlock()
	return nil
}

// Lookup returns the Device for a keyId from the in-memory map.
func (s *Store) Lookup(keyID string) (Device, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	d, ok := s.byID[keyID]
	return d, ok
}

// All returns a snapshot copy of all devices (for the tray manage/revoke list).
func (s *Store) All() []Device {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Device, 0, len(s.byID))
	for _, d := range s.byID {
		out = append(out, d)
	}
	return out
}

// Add inserts/replaces a device and persists the whole array atomically (0600).
func (s *Store) Add(d Device) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byID[d.KeyID] = d
	return s.persistLocked()
}

// Revoke removes a keyId and persists. Returns false if it was absent.
func (s *Store) Revoke(keyID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byID[keyID]; !ok {
		return false, nil
	}
	delete(s.byID, keyID)
	return true, s.persistLocked()
}

func (s *Store) persistLocked() error {
	list := make([]Device, 0, len(s.byID))
	for _, d := range s.byID {
		list = append(list, d)
	}
	return writeJSONAtomic(s.path, list)
}

// writeJSONAtomic marshals v and writes it to path via temp file + rename, 0600.
func writeJSONAtomic(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
