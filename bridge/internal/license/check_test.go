package license

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestCheckAgentLifecycle(t *testing.T) {
	svc, key := issuedSvc(t, 1) // expires testBase + 6 months
	now := testBase
	svc.Now = func() time.Time { return now }
	res, err := svc.Activate(key, "m1", "Mac", "fp1")
	if err != nil {
		t.Fatalf("activate: %v", err)
	}
	cred := res.Credential

	// active
	cr, err := svc.CheckAgent("m1", cred)
	if err != nil || cr.State != StateActive || cr.Message != "" {
		t.Fatalf("active: %v %+v", err, cr)
	}
	// wrong credential / unknown machine
	if _, err := svc.CheckAgent("m1", "mc_wrong"); !errors.Is(err, ErrBadCredential) {
		t.Fatalf("bad cred: %v", err)
	}
	if _, err := svc.CheckAgent("ghost", cred); !errors.Is(err, ErrUnknownMachine) {
		t.Fatalf("ghost: %v", err)
	}
	// grace: expired but inside GracePeriod — allowed, with a renewal notice
	now = testBase.AddDate(0, 6, 0).Add(time.Hour)
	cr, err = svc.CheckAgent("m1", cred)
	if err != nil || cr.State != StateGrace || !strings.Contains(cr.Message, "expired") {
		t.Fatalf("grace: %v %+v", err, cr)
	}
	// past grace — refused
	now = testBase.AddDate(0, 6, 0).Add(GracePeriod + time.Hour)
	if _, err := svc.CheckAgent("m1", cred); !errors.Is(err, ErrExpired) {
		t.Fatalf("past grace: %v", err)
	}
	// renewal restores access
	now = testBase
	if err := svc.ExtendKey(key, 12); err != nil {
		t.Fatalf("extend: %v", err)
	}
	now = testBase.AddDate(0, 7, 0)
	if cr, err = svc.CheckAgent("m1", cred); err != nil || cr.State != StateActive {
		t.Fatalf("after renew: %v %+v", err, cr)
	}
	// revocation kills it regardless of expiry
	if err := svc.RevokeKey(key); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := svc.CheckAgent("m1", cred); !errors.Is(err, ErrRevoked) {
		t.Fatalf("revoked: %v", err)
	}
}

func TestTouchLastSeen(t *testing.T) {
	svc, key := issuedSvc(t, 1)
	if _, err := svc.Activate(key, "m1", "", "fp1"); err != nil {
		t.Fatal(err)
	}
	svc.TouchLastSeen("m1")
	aid, _ := svc.AccountIDForMachine("m1")
	ms, err := svc.Roster(aid)
	if err != nil || len(ms) != 1 || ms[0].LastSeen.IsZero() {
		t.Fatalf("last_seen not set: %v %+v", err, ms)
	}
}
