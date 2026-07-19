package license

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestDeactivateFreesSlotAndRateLimits(t *testing.T) {
	svc, key := issuedSvc(t, 1)
	now := testBase
	svc.Now = func() time.Time { return now }

	var aid int64
	// 3 swaps inside the window are fine; the 4th hits the limit
	for i := 1; i <= 3; i++ {
		mid := fmt.Sprintf("m%d", i)
		if _, err := svc.Activate(key, mid, "", fmt.Sprintf("fp%d", i)); err != nil {
			t.Fatalf("activate %s: %v", mid, err)
		}
		var err error
		if aid, err = svc.AccountIDForMachine(mid); err != nil {
			t.Fatalf("account for %s: %v", mid, err)
		}
		if err := svc.Deactivate(aid, mid); err != nil {
			t.Fatalf("deactivate %s: %v", mid, err)
		}
	}
	if _, err := svc.Activate(key, "m4", "", "fp4"); err != nil {
		t.Fatalf("activate m4: %v", err)
	}
	if err := svc.Deactivate(aid, "m4"); !errors.Is(err, ErrSwapLimit) {
		t.Fatalf("4th swap: %v, want ErrSwapLimit", err)
	}
	// window slides: 31 days later the swap goes through
	now = testBase.Add(31 * 24 * time.Hour)
	if err := svc.Deactivate(aid, "m4"); err != nil {
		t.Fatalf("after window: %v", err)
	}
	// deactivating a machine that isn't active any more
	if err := svc.Deactivate(aid, "m4"); !errors.Is(err, ErrUnknownMachine) {
		t.Fatalf("double deactivate: %v, want ErrUnknownMachine", err)
	}
}

func TestRosterListsActiveOnly(t *testing.T) {
	svc, key := issuedSvc(t, 2)
	if _, err := svc.Activate(key, "m1", "Mac", "fp1"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Activate(key, "m2", "PC", "fp2"); err != nil {
		t.Fatal(err)
	}
	aid, err := svc.AccountIDForMachine("m1")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Deactivate(aid, "m2"); err != nil {
		t.Fatal(err)
	}
	ms, err := svc.Roster(aid)
	if err != nil || len(ms) != 1 || ms[0].MachineID != "m1" || ms[0].Name != "Mac" {
		t.Fatalf("roster: %v %+v", err, ms)
	}
}

func TestAdminDeactivateSkipsSwapLimit(t *testing.T) {
	svc, key := issuedSvc(t, 1)
	aid := int64(0)
	for i := 1; i <= 3; i++ {
		mid := fmt.Sprintf("m%d", i)
		if _, err := svc.Activate(key, mid, "", fmt.Sprintf("fp%d", i)); err != nil {
			t.Fatal(err)
		}
		aid, _ = svc.AccountIDForMachine(mid)
		if err := svc.Deactivate(aid, mid); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := svc.Activate(key, "m4", "", "fp4"); err != nil {
		t.Fatal(err)
	}
	// operator override is not rate-limited and burns no swap
	if err := svc.AdminDeactivate("m4"); err != nil {
		t.Fatalf("AdminDeactivate: %v", err)
	}
	if err := svc.AdminDeactivate("m4"); !errors.Is(err, ErrUnknownMachine) {
		t.Fatalf("double: %v, want ErrUnknownMachine", err)
	}
}

func TestListMachines(t *testing.T) {
	svc := tempSvc(t)
	key, err := svc.IssueKey("beta", 2, 6)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if _, err := svc.Activate(key, "m-studio", "Mac Studio", ""); err != nil {
		t.Fatalf("activate: %v", err)
	}

	ms, err := svc.ListMachines()
	if err != nil || len(ms) != 1 {
		t.Fatalf("list: err=%v len=%d", err, len(ms))
	}
	m := ms[0]
	if m.MachineID != "m-studio" || m.Name != "Mac Studio" || m.KeyPrefix != key[:12] {
		t.Fatalf("row = %+v", m)
	}
	if m.ActivatedAt.IsZero() {
		t.Fatal("ActivatedAt zero")
	}

	if err := svc.AdminDeactivate("m-studio"); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	ms, err = svc.ListMachines()
	if err != nil || len(ms) != 0 {
		t.Fatalf("after deactivate: err=%v len=%d", err, len(ms))
	}
}
