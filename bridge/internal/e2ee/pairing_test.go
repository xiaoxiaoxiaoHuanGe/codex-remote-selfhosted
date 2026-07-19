package e2ee

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"testing"

	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/nacl/box"
)

func TestPairingRoundTrip(t *testing.T) {
	enrollPub, enrollSec, err := GenEnrollKeypair()
	if err != nil {
		t.Fatalf("GenEnrollKeypair: %v", err)
	}
	devicePub, deviceSec, err := GenDeviceKeypair()
	if err != nil {
		t.Fatalf("GenDeviceKeypair: %v", err)
	}

	want := PairPayload{
		KeyID: "k_test_0001",
		PSK:   base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x42}, 32)),
	}
	sealed, err := SealPairing(want, devicePub, enrollSec)
	if err != nil {
		t.Fatalf("SealPairing: %v", err)
	}
	if len(sealed) <= pairNonceLen {
		t.Fatalf("sealed too short: %d", len(sealed))
	}

	got, err := OpenPairing(sealed, enrollPub, deviceSec)
	if err != nil {
		t.Fatalf("OpenPairing: %v", err)
	}
	if got != want {
		t.Fatalf("payload mismatch: got %+v want %+v", got, want)
	}
}

func TestPairingWrongDeviceKeyFails(t *testing.T) {
	enrollPub, enrollSec, _ := GenEnrollKeypair()
	devicePub, _, _ := GenDeviceKeypair()
	_, otherSec, _ := GenDeviceKeypair() // a different device's secret

	sealed, err := SealPairing(PairPayload{KeyID: "k", PSK: "x"}, devicePub, enrollSec)
	if err != nil {
		t.Fatalf("SealPairing: %v", err)
	}
	if _, err := OpenPairing(sealed, enrollPub, otherSec); err != ErrPairOpen {
		t.Fatalf("expected ErrPairOpen with wrong device key, got %v", err)
	}
}

func TestPairingWrongEnrollKeyFails(t *testing.T) {
	_, enrollSec, _ := GenEnrollKeypair()
	otherEnrollPub, _, _ := GenEnrollKeypair() // wrong enrollment public key
	devicePub, deviceSec, _ := GenDeviceKeypair()

	sealed, _ := SealPairing(PairPayload{KeyID: "k", PSK: "x"}, devicePub, enrollSec)
	if _, err := OpenPairing(sealed, otherEnrollPub, deviceSec); err != ErrPairOpen {
		t.Fatalf("expected ErrPairOpen with wrong enroll key, got %v", err)
	}
}

func TestPairingTruncatedFails(t *testing.T) {
	enrollPub, enrollSec, _ := GenEnrollKeypair()
	devicePub, deviceSec, _ := GenDeviceKeypair()
	sealed, _ := SealPairing(PairPayload{KeyID: "k", PSK: "x"}, devicePub, enrollSec)

	for _, n := range []int{0, 10, pairNonceLen} { // shorter than nonce||box
		if _, err := OpenPairing(sealed[:n], enrollPub, deviceSec); err != ErrPairOpen {
			t.Fatalf("len %d: expected ErrPairOpen, got %v", n, err)
		}
	}
}

// TestPrintPairingVector is the cross-language oracle for the pairing seal: with
// FIXED keys + nonce it prints the bridge enrollment pubkey, the device secret, and
// the sealed blob (nonce||box). Run with -v and paste into the Dart pairing test so
// the phone's NaCl-box open (pinenacl) is proven byte-compatible with Go's seal.
func TestPrintPairingVector(t *testing.T) {
	var enrollSec, deviceSec [32]byte
	for i := range enrollSec {
		enrollSec[i] = 0x11
	}
	for i := range deviceSec {
		deviceSec[i] = 0x22
	}
	enrollPub, err := curve25519.X25519(enrollSec[:], curve25519.Basepoint)
	if err != nil {
		t.Fatal(err)
	}
	devicePub, err := curve25519.X25519(deviceSec[:], curve25519.Basepoint)
	if err != nil {
		t.Fatal(err)
	}
	var devicePubArr, enrollSecArr [32]byte
	copy(devicePubArr[:], devicePub)
	enrollSecArr = enrollSec
	var nonce [pairNonceLen]byte
	for i := range nonce {
		nonce[i] = 0x33
	}
	payload := PairPayload{KeyID: "k_pair_0001", PSK: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x55}, 32))}
	pt, _ := json.Marshal(payload)
	sealed := box.Seal(nonce[:], pt, &nonce, &devicePubArr, &enrollSecArr)
	t.Logf("PAIRVEC enrollPub = %s", base64.StdEncoding.EncodeToString(enrollPub))
	t.Logf("PAIRVEC deviceSec = %s", base64.StdEncoding.EncodeToString(deviceSec[:]))
	t.Logf("PAIRVEC sealed = %s", base64.StdEncoding.EncodeToString(sealed))
	t.Logf("PAIRVEC keyId = %s", payload.KeyID)
	t.Logf("PAIRVEC psk = %s", payload.PSK)
}

func TestPairingTamperFails(t *testing.T) {
	enrollPub, enrollSec, _ := GenEnrollKeypair()
	devicePub, deviceSec, _ := GenDeviceKeypair()
	sealed, _ := SealPairing(PairPayload{KeyID: "k", PSK: "x"}, devicePub, enrollSec)

	tampered := append([]byte(nil), sealed...)
	tampered[len(tampered)-1] ^= 0xFF // flip a ciphertext bit
	if _, err := OpenPairing(tampered, enrollPub, deviceSec); err != ErrPairOpen {
		t.Fatalf("expected ErrPairOpen on tamper, got %v", err)
	}
}
