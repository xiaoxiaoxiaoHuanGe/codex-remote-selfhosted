package license

import (
	"errors"
	"strings"
	"testing"
	"time"
)

var testBase = time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC)

// issuedSvc returns a Service pinned to testBase with one key of the given limit.
func issuedSvc(t *testing.T, limit int) (*Service, string) {
	t.Helper()
	svc := tempSvc(t)
	svc.Now = func() time.Time { return testBase }
	key, err := svc.IssueKey("beta", limit, 6)
	if err != nil {
		t.Fatalf("IssueKey: %v", err)
	}
	return svc, key
}

func TestActivateHappyPath(t *testing.T) {
	svc, key := issuedSvc(t, 2)
	res, err := svc.Activate(key, "m1", "Office Mac", "fp1")
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if !strings.HasPrefix(res.Credential, "mc_") || res.Used != 1 || res.Limit != 2 || res.Reused {
		t.Fatalf("bad result: %+v", res)
	}
}

func TestActivateRosterFull(t *testing.T) {
	svc, key := issuedSvc(t, 1)
	if _, err := svc.Activate(key, "m1", "", "fp1"); err != nil {
		t.Fatalf("first: %v", err)
	}
	if _, err := svc.Activate(key, "m2", "", "fp2"); !errors.Is(err, ErrRosterFull) {
		t.Fatalf("second: %v, want ErrRosterFull", err)
	}
}

func TestActivateReusesSlotOnFingerprintMatch(t *testing.T) {
	svc, key := issuedSvc(t, 1)
	first, err := svc.Activate(key, "m1", "Mac", "fp1")
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	// reinstall: same fingerprint, new machine id — reuse the slot, reissue cred
	again, err := svc.Activate(key, "m1-fresh", "Mac", "fp1")
	if err != nil {
		t.Fatalf("reuse: %v", err)
	}
	if !again.Reused || again.Used != 1 {
		t.Fatalf("want reuse of the single slot: %+v", again)
	}
	if again.Credential == first.Credential {
		t.Fatal("credential not reissued")
	}
	// old id is gone, old credential dead; new pair works
	if _, err := svc.CheckAgent("m1", first.Credential); !errors.Is(err, ErrUnknownMachine) {
		t.Fatalf("old id: %v, want ErrUnknownMachine", err)
	}
	if _, err := svc.CheckAgent("m1-fresh", first.Credential); !errors.Is(err, ErrBadCredential) {
		t.Fatalf("old cred: %v, want ErrBadCredential", err)
	}
	if _, err := svc.CheckAgent("m1-fresh", again.Credential); err != nil {
		t.Fatalf("new cred: %v", err)
	}
}

func TestActivateMachineIDTakenAcrossAccounts(t *testing.T) {
	svc, key1 := issuedSvc(t, 2)
	key2, err := svc.IssueKey("beta", 2, 6)
	if err != nil {
		t.Fatalf("second key: %v", err)
	}
	if _, err := svc.Activate(key1, "dup", "", "fpA"); err != nil {
		t.Fatalf("first: %v", err)
	}
	// the hub routes by machine id globally, so the second account can't take it
	if _, err := svc.Activate(key2, "dup", "", "fpB"); !errors.Is(err, ErrMachineTaken) {
		t.Fatalf("clash: %v, want ErrMachineTaken", err)
	}
}

func TestActivateRejectsBadExpiredRevoked(t *testing.T) {
	svc, key := issuedSvc(t, 1)
	if _, err := svc.Activate("not-a-key", "m1", "", ""); !errors.Is(err, ErrBadKey) {
		t.Fatalf("garbage: %v, want ErrBadKey", err)
	}
	if _, err := svc.Activate(key, "bad id with spaces", "", ""); !errors.Is(err, ErrBadMachineID) {
		t.Fatalf("bad id: %v, want ErrBadMachineID", err)
	}
	svc.Now = func() time.Time { return testBase.AddDate(0, 7, 0) } // past expiry
	if _, err := svc.Activate(key, "m1", "", ""); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired: %v, want ErrExpired", err)
	}
	svc.Now = func() time.Time { return testBase }
	if err := svc.RevokeKey(key); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := svc.Activate(key, "m1", "", ""); !errors.Is(err, ErrRevoked) {
		t.Fatalf("revoked: %v, want ErrRevoked", err)
	}
}
