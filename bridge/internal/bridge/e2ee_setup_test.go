package bridge

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yunyuchen/codex-remote/bridge/internal/devstore"
	"github.com/yunyuchen/codex-remote/bridge/internal/e2ee"
)

func TestEnableE2EEFromDirGenerateAndReuse(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "bridge")

	s1 := &Server{}
	pub1, _, err := s1.EnableE2EEFromDir(dir)
	if err != nil {
		t.Fatalf("first EnableE2EEFromDir: %v", err)
	}
	if !s1.e2eeOn() {
		t.Fatal("e2ee not active after EnableE2EEFromDir")
	}
	if _, err := os.Stat(filepath.Join(dir, enrollFileName)); err != nil {
		t.Fatalf("enroll.json not written: %v", err)
	}

	// A second activation over the same dir MUST reuse the enrollment key — else
	// every restart would invalidate already-paired devices' sealed PSKs.
	s2 := &Server{}
	pub2, _, err := s2.EnableE2EEFromDir(dir)
	if err != nil {
		t.Fatalf("second EnableE2EEFromDir: %v", err)
	}
	if *pub1 != *pub2 {
		t.Fatal("enrollment public key changed across restarts (paired devices would break)")
	}
}

func TestEnableE2EEFromDirPersistsDevice(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "bridge")

	s := &Server{}
	enrollPub, _, err := s.EnableE2EEFromDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Pair a device through the real handshake.
	if err := s.pending.AddPending("pid", time.Minute, time.Now()); err != nil {
		t.Fatal(err)
	}
	devicePub, deviceSec, _ := e2ee.GenDeviceKeypair()
	resp, dc := s.handlePairInit(pairInit{Type: "pair_init", PairingID: "pid", DevicePub: b64(devicePub[:]), Name: "iPhone"})
	if dc {
		t.Fatal("pairing unexpectedly failed")
	}
	sealed, _ := base64.StdEncoding.DecodeString(resp.(pairResult).Sealed)
	payload, err := e2ee.OpenPairing(sealed, enrollPub, deviceSec)
	if err != nil {
		t.Fatalf("OpenPairing: %v", err)
	}

	// A fresh process over the same dir must see the persisted device.
	devs2, err := devstore.Open(filepath.Join(dir, devicesFileName))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := devs2.Lookup(payload.KeyID); !ok {
		t.Fatal("paired device not durable across a reopen")
	}
}

func TestLoadOrGenEnrollCorruptRefuses(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, enrollFileName)
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadOrGenEnroll(path); err == nil {
		t.Fatal("expected error on corrupt enroll key (must refuse to overwrite)")
	}
}
