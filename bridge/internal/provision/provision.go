// Package provision rotates this machine's per-machine phone token in place.
//
// The hub keeps tokens in memory only (last-write-wins): when the agent restarts
// and re-registers with a NEW token, the hub forgets the old one, so any phone or
// baked APK still holding the old token is instantly rejected. Rotation is
// therefore a purely local operation — no hub/relay change is needed:
//
//	1. generate a fresh token
//	2. write it into the agent's runtime source (launchd plist env on macOS,
//	   scheduled-task argument on Windows) AND menubar.env (the tray's QR source)
//	3. restart the agent so it re-registers with the new token
//
// The OS-specific steps live in reset_darwin.go / reset_windows.go.
package provision

import (
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"strings"
)

// ResetResult describes a completed machine-token rotation.
type ResetResult struct {
	MachineID string
	NewToken  string
	Host      string // hub host, for building the phone URL
}

// PhoneURL is the wss:// string the phone scans/enters to re-pair after a reset.
func (r ResetResult) PhoneURL() string {
	return "wss://" + r.Host + "/ws?token=" + r.NewToken
}

// newToken returns a fresh 32-byte hex machine token.
func newToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// readEnvFile parses a KEY=VALUE config (menubar.env) into a map.
func readEnvFile(path string) (map[string]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	kv := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		kv[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"`)
	}
	return kv, nil
}

// setEnvKey rewrites KEY=value in a KEY=VALUE file, preserving other lines and
// appending the key if absent. Written atomically with 0600 perms.
func setEnvKey(path, key, val string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(string(b), "\n")
	found := false
	for i, line := range lines {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		k, _, ok := strings.Cut(t, "=")
		if ok && strings.TrimSpace(k) == key {
			lines[i] = key + "=" + val
			found = true
			break
		}
	}
	if !found {
		lines = append(lines, key+"="+val)
	}
	tmp := path + ".new"
	if err := os.WriteFile(tmp, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// hostFromHub extracts the host (e.g. relay.example.com) from a hub URL.
func hostFromHub(hub string) string {
	if u, err := url.Parse(hub); err == nil && u.Host != "" {
		return u.Host
	}
	return "relay.example.com"
}
