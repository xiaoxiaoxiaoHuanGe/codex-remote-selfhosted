package bridge

// E2EE activation glue: load/generate the on-disk state and turn the codec on.
//
// Layered on top of the existing token gate — NOT a replacement. handleWS still
// authenticates ?token= at the WS upgrade (a DO-NOT-WEAKEN clamp); EnableE2EE only
// adds the encrypted control-plane AFTER the upgrade. Activation is opt-in
// (CODEX_E2EE=1 at the call sites) so existing token-only clients keep working
// until the phone app speaks E2EE — without it the read/write seams pass cleartext.

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/yunyuchen/codex-remote/bridge/internal/devstore"
	"github.com/yunyuchen/codex-remote/bridge/internal/e2ee"
)

const (
	enrollFileName  = "enroll.json"
	devicesFileName = "devices.json"
	pendingFileName = "pending-pairings.json"
)

// E2EEDir returns the directory holding the bridge's E2EE state (enrollment key,
// device registry, pending pairings). Default: ~/.codex/bridge. Override with
// CODEX_E2EE_DIR. It sits under ~/.codex but is never served by /file (no media
// extension passes the allowlist), so the registries can't be exfiltrated there.
func E2EEDir() (string, error) {
	if d := os.Getenv("CODEX_E2EE_DIR"); d != "" {
		return d, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".codex", "bridge"), nil
}

// enrollFile is the on-disk form of the bridge enrollment keypair. Only Sec is
// secret; Pub is published in the tray QR.
type enrollFile struct {
	Pub string `json:"pub"` // base64 std, 32 bytes
	Sec string `json:"sec"` // base64 std, 32 bytes (seals PairPayloads)
}

// EnableE2EEFromDir activates the encrypted control-plane backed by files under
// dir: it loads (or first-time generates, 0600) the enrollment keypair and opens
// the device + pending-pairing registries, then calls EnableE2EE. It returns the
// enrollment PUBLIC key (for the tray QR) and the device Store (so the caller can
// Watch it for revocation hot-reload). Idempotent across restarts — an existing
// enroll.json is reused so already-paired devices keep working.
func (s *Server) EnableE2EEFromDir(dir string) (*[32]byte, *devstore.Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, err
	}
	enrollPub, enrollSec, err := loadOrGenEnroll(filepath.Join(dir, enrollFileName))
	if err != nil {
		return nil, nil, err
	}
	devices, err := devstore.Open(filepath.Join(dir, devicesFileName))
	if err != nil {
		return nil, nil, err
	}
	pending := devstore.OpenPending(filepath.Join(dir, pendingFileName))
	s.EnableE2EE(devices, enrollSec, pending)
	return enrollPub, devices, nil
}

// loadOrGenEnroll reads the enrollment keypair, generating a fresh one only when
// the file is ABSENT. A present-but-corrupt file is a hard error (refuse to
// overwrite) so an operator notices rather than silently invalidating in-flight
// pairings. Already-paired devices are unaffected either way — they hold their PSK.
func loadOrGenEnroll(path string) (*[32]byte, *[32]byte, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return genEnroll(path)
	}
	if err != nil {
		return nil, nil, err
	}
	var ef enrollFile
	if err := json.Unmarshal(b, &ef); err != nil {
		return nil, nil, fmt.Errorf("e2ee enroll key %s corrupt (refusing to overwrite): %w", path, err)
	}
	pub, perr := decodePub(ef.Pub)
	sec, serr := decodePub(ef.Sec)
	if perr != nil || serr != nil {
		return nil, nil, fmt.Errorf("e2ee enroll key %s malformed (refusing to overwrite)", path)
	}
	return pub, sec, nil
}

// genEnroll creates a new enrollment keypair and persists it atomically (0600).
func genEnroll(path string) (*[32]byte, *[32]byte, error) {
	pub, sec, err := e2ee.GenEnrollKeypair()
	if err != nil {
		return nil, nil, err
	}
	ef := enrollFile{
		Pub: base64.StdEncoding.EncodeToString(pub[:]),
		Sec: base64.StdEncoding.EncodeToString(sec[:]),
	}
	blob, err := json.MarshalIndent(ef, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, blob, 0o600); err != nil {
		return nil, nil, err
	}
	return pub, sec, os.Rename(tmp, path)
}

// EnrollFingerprint is a short, non-secret label for an enrollment public key, for
// operator-facing startup logs ("which enrollment key is live").
func EnrollFingerprint(pub *[32]byte) string {
	if pub == nil {
		return "none"
	}
	return base64.StdEncoding.EncodeToString(pub[:])[:8]
}
