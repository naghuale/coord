package roles

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRegistryHasExclusiveProcessOwnership(t *testing.T) {
	path := filepath.Join(t.TempDir(), "roles.json")
	first, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	if second, err := New(path); err == nil {
		if closer, ok := any(second).(interface{ Close() error }); ok {
			_ = closer.Close()
		}
		t.Fatal("second registry writer accepted")
	}
	closer, ok := any(first).(interface{ Close() error })
	if !ok {
		t.Fatal("registry cannot release ownership")
	}
	if err = closer.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	if closer, ok = any(reopened).(interface{ Close() error }); ok {
		t.Cleanup(func() {
			if err := closer.Close(); err != nil {
				t.Errorf("close test resource: %v", err)
			}
		})
	}
	if err = first.Register("id", "session", []Profile{{Role: Executor, Model: "model"}}); err == nil {
		t.Fatal("closed registry still writes")
	}
}
func TestCorruptRegistryReleasesAcquiredOwnership(t *testing.T) {
	path := filepath.Join(t.TempDir(), "roles.json")
	if err := os.WriteFile(path, []byte("{broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(path); err == nil {
		t.Fatal("corrupt registry accepted")
	}
	if err := os.WriteFile(path, []byte(`{"Version":1,"Agents":{},"Events":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	r, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	if closer, ok := any(r).(interface{ Close() error }); ok {
		t.Cleanup(func() {
			if err := closer.Close(); err != nil {
				t.Errorf("close test resource: %v", err)
			}
		})
	}
}

func TestRegistryPersistenceFailureRequiresReopen(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "registry")
	path := filepath.Join(dir, "roles.json")
	r, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Errorf("close test resource: %v", err)
		}
	})
	if err = r.Register("id", "session", []Profile{{Role: Executor, Model: "original"}}); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(dir, dir+".moved"); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(dir, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	model := "next"
	err = r.Apply("session", "failed", Update{Revision: 1, Updated: []Patch{{Role: Executor, Model: &model}}}, time.Now())
	if !errors.Is(err, ErrPersistence) {
		t.Fatalf("expected storage failure: %v", err)
	}
	if err = os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(dir+".moved", dir); err != nil {
		t.Fatal(err)
	}
	err = r.Apply("session", "later", Update{Revision: 1, Updated: []Patch{{Role: Executor, Model: &model}}}, time.Now())
	if !errors.Is(err, ErrPersistence) {
		t.Fatal("registry continued writing after uncertain commit")
	}
	if _, err = r.Select("session", Requirement{Role: Executor}); !errors.Is(err, ErrPersistence) {
		t.Fatal("registry selected a stale profile after failed commit")
	}
	if err = r.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := reopened.Close(); err != nil {
			t.Errorf("close test resource: %v", err)
		}
	})
	selection, err := reopened.Select("session", Requirement{Role: Executor})
	if err != nil {
		t.Fatal(err)
	}
	if selection.Profile.Model != "original" {
		t.Fatal("reopen lost durable profile")
	}
}
