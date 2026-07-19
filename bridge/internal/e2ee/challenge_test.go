package e2ee

import (
	"bytes"
	"encoding/base64"
	"testing"

	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/nacl/box"
)

func TestChallengeRoundTrip(t *testing.T) {
	devicePub, deviceSec, err := GenDeviceKeypair()
	if err != nil {
		t.Fatalf("GenDeviceKeypair: %v", err)
	}
	nonce := bytes.Repeat([]byte{0x7e}, ChallengeNonceLen)

	ephPub, sealed, err := SealChallenge(nonce, devicePub)
	if err != nil {
		t.Fatalf("SealChallenge: %v", err)
	}
	if len(sealed) <= pairNonceLen {
		t.Fatalf("sealed too short: %d", len(sealed))
	}

	got, err := OpenChallenge(sealed, ephPub, deviceSec)
	if err != nil {
		t.Fatalf("OpenChallenge: %v", err)
	}
	if !bytes.Equal(got, nonce) {
		t.Fatalf("nonce mismatch: got %x want %x", got, nonce)
	}
}

func TestChallengeWrongDeviceKeyFails(t *testing.T) {
	devicePub, _, _ := GenDeviceKeypair()
	_, otherSec, _ := GenDeviceKeypair() // a different device's secret
	nonce := bytes.Repeat([]byte{0x01}, ChallengeNonceLen)

	ephPub, sealed, err := SealChallenge(nonce, devicePub)
	if err != nil {
		t.Fatalf("SealChallenge: %v", err)
	}
	if _, err := OpenChallenge(sealed, ephPub, otherSec); err != ErrPairOpen {
		t.Fatalf("expected ErrPairOpen with wrong device key, got %v", err)
	}
}

func TestChallengeWrongEphemeralKeyFails(t *testing.T) {
	devicePub, deviceSec, _ := GenDeviceKeypair()
	otherEph, _, _ := GenDeviceKeypair() // an unrelated public key as sender
	nonce := bytes.Repeat([]byte{0x02}, ChallengeNonceLen)

	_, sealed, _ := SealChallenge(nonce, devicePub)
	if _, err := OpenChallenge(sealed, otherEph, deviceSec); err != ErrPairOpen {
		t.Fatalf("expected ErrPairOpen with wrong ephemeral key, got %v", err)
	}
}

func TestChallengeTruncatedFails(t *testing.T) {
	devicePub, deviceSec, _ := GenDeviceKeypair()
	nonce := bytes.Repeat([]byte{0x03}, ChallengeNonceLen)
	ephPub, sealed, _ := SealChallenge(nonce, devicePub)

	for _, n := range []int{0, 10, pairNonceLen} { // shorter than nonce||box
		if _, err := OpenChallenge(sealed[:n], ephPub, deviceSec); err != ErrPairOpen {
			t.Fatalf("len %d: expected ErrPairOpen, got %v", n, err)
		}
	}
}

func TestChallengeTamperFails(t *testing.T) {
	devicePub, deviceSec, _ := GenDeviceKeypair()
	nonce := bytes.Repeat([]byte{0x04}, ChallengeNonceLen)
	ephPub, sealed, _ := SealChallenge(nonce, devicePub)

	tampered := append([]byte(nil), sealed...)
	tampered[len(tampered)-1] ^= 0xFF // flip a ciphertext bit
	if _, err := OpenChallenge(tampered, ephPub, deviceSec); err != ErrPairOpen {
		t.Fatalf("expected ErrPairOpen on tamper, got %v", err)
	}
}

// TestPrintChallengeVector is the cross-language oracle for the hub challenge seal:
// with FIXED ephemeral key + nonce + device key it prints the hub ephemeral pubkey,
// the device secret, and the sealed blob (boxNonce||box). Run with -v and paste into
// the Dart/JS auth tests so the phone's NaCl-box open is proven byte-compatible.
func TestPrintChallengeVector(t *testing.T) {
	var ephSec, deviceSec [32]byte
	for i := range ephSec {
		ephSec[i] = 0x66
	}
	for i := range deviceSec {
		deviceSec[i] = 0x77
	}
	ephPub, err := curve25519.X25519(ephSec[:], curve25519.Basepoint)
	if err != nil {
		t.Fatal(err)
	}
	devicePub, err := curve25519.X25519(deviceSec[:], curve25519.Basepoint)
	if err != nil {
		t.Fatal(err)
	}
	var devicePubArr, ephSecArr [32]byte
	copy(devicePubArr[:], devicePub)
	ephSecArr = ephSec
	var boxNonce [pairNonceLen]byte
	for i := range boxNonce {
		boxNonce[i] = 0x88
	}
	challengeNonce := bytes.Repeat([]byte{0x99}, ChallengeNonceLen)
	sealed := box.Seal(boxNonce[:], challengeNonce, &boxNonce, &devicePubArr, &ephSecArr)
	t.Logf("CHALVEC ephPub = %s", base64.StdEncoding.EncodeToString(ephPub))
	t.Logf("CHALVEC deviceSec = %s", base64.StdEncoding.EncodeToString(deviceSec[:]))
	t.Logf("CHALVEC sealed = %s", base64.StdEncoding.EncodeToString(sealed))
	t.Logf("CHALVEC nonce = %s", base64.StdEncoding.EncodeToString(challengeNonce))
}
