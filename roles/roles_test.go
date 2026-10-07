package roles

import (
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestAtomicUpdatePreservesCapabilitiesAndReplays(t *testing.T) {
	path := filepath.Join(t.TempDir(), "roles.json")
	r, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	initial := []Profile{{Executor, "old", []string{"go"}}, {Reviewer, "review", []string{"code_review"}}}
	if err = r.Register("nemotron", "session-1", initial); err != nil {
		t.Fatal(err)
	}
	model := "new"
	u := Update{Revision: 1, Added: []Profile{{Auditor, "audit", []string{"security_audit"}}}, Removed: []Role{Reviewer}, Updated: []Patch{{Role: Executor, Model: &model}}}
	if err = r.Apply("session-1", "m1", u, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err = r.Apply("session-1", "m1", u, time.Now()); err != nil {
		t.Fatal(err)
	}
	a, _ := r.Agent("session-1")
	if a.Revision != 2 || a.Roles[Executor].Model != "new" || len(a.Roles[Executor].Capabilities) != 1 || len(a.Roles) != 2 || len(r.Events()) != 1 {
		t.Fatalf("bad update: %+v", a)
	}
	bad := Update{Revision: 2, Added: []Profile{{Reviewer, "review", nil}}, Removed: []Role{Coordinator}}
	if err = r.Apply("session-1", "bad", bad, time.Now()); err == nil {
		t.Fatal("invalid batch accepted")
	}
	a, _ = r.Agent("session-1")
	if a.Revision != 2 || len(a.Roles) != 2 {
		t.Fatal("partial update committed")
	}
	if err = r.Close(); err != nil {
		t.Fatal(err)
	}
	restored, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := restored.Close(); err != nil {
			t.Errorf("close test resource: %v", err)
		}
	})
	if _, err = restored.Select("session-1", Requirement{Role: Auditor, Capabilities: []string{"security_audit"}}); err != nil {
		t.Fatal(err)
	}
	if _, err = restored.Select("session-1", Requirement{Role: Executor, Capabilities: []string{"python"}}); err == nil {
		t.Fatal("missing capability accepted")
	}
	if err = r.Apply("unknown", "m", u, time.Now()); err == nil {
		t.Fatal("unregistered identity accepted")
	}
}
func TestConcurrentRevisionCAS(t *testing.T) {
	r, err := New(filepath.Join(t.TempDir(), "roles.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Errorf("close test resource: %v", err)
		}
	})
	if err := r.Register("id", "session", []Profile{{Executor, "old", nil}}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, id := range []string{"a", "b"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			model := id
			results <- r.Apply("session", id, Update{Revision: 1, Updated: []Patch{{Role: Executor, Model: &model}}}, time.Now())
		}(id)
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatal("CAS accepted concurrent updates")
	}
}
