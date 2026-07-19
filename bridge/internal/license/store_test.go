package license

import (
	"path/filepath"
	"testing"
)

// tempSvc opens a Service on a throwaway db; shared by every test in the package.
func tempSvc(t *testing.T) *Service {
	t.Helper()
	svc, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	return svc
}

func TestOpenMigrateIdempotent(t *testing.T) {
	p := filepath.Join(t.TempDir(), "t.db")
	svc, err := Open(p)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	if err := svc.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	svc2, err := Open(p) // re-open re-runs migrate — must be a no-op, not an error
	if err != nil {
		t.Fatalf("re-open: %v", err)
	}
	_ = svc2.Close()
}
