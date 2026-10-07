package filelock

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestLockExclusivityOrFailClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.lock")
	release, err := Lock(path)
	if err != nil {
		if release != nil {
			t.Fatal("refused lock returned a release function")
		}
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("Lock error = %v, want ErrUnavailable on unsupported platform", err)
		}
		return
	}
	if release == nil {
		t.Fatal("successful lock returned no release function")
	}
	secondRelease, err := Lock(path)
	if err == nil {
		if secondRelease != nil {
			if closeErr := secondRelease(); closeErr != nil {
				t.Errorf("release unexpected second lock: %v", closeErr)
			}
		}
		t.Fatal("second writer acquired the same lock")
	}
	if secondRelease != nil || !errors.Is(err, ErrLocked) {
		t.Fatalf("second lock release is nil=%t, error=%v; want nil release and ErrLocked", secondRelease == nil, err)
	}
	if err = release(); err != nil {
		t.Fatal(err)
	}
	if next, err := Lock(path); err != nil {
		t.Fatalf("lock after release: %v", err)
	} else if err = next(); err != nil {
		t.Fatalf("release reacquired lock: %v", err)
	}
}
