package store

import (
	"coord/fsm"
	"errors"
	"testing"
	"time"
)

type memory struct{ runs map[string]fsm.Run }

func (p *memory) Load() (map[string]fsm.Run, error) { return p.runs, nil }
func (p *memory) Save(r map[string]fsm.Run) error   { p.runs = r; return nil }
func TestLoadedAndCommittedStateCannotBeMutatedExternally(t *testing.T) {
	p := &memory{runs: map[string]fsm.Run{"r": {RunID: "r", Task: "original"}}}
	s, err := New(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("close test resource: %v", err)
		}
	})
	p.runs["r"] = fsm.Run{RunID: "r", Task: "changed"}
	if r, _ := s.Get("r"); r.Task != "original" {
		t.Fatal("Load exposes external aliases")
	}
	var captured map[string]fsm.Run
	if err = s.Update(func(r map[string]fsm.Run) error {
		captured = r
		r["r"] = fsm.Run{RunID: "r", Task: "committed"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	captured["r"] = fsm.Run{Task: "changed"}
	p.runs["r"] = fsm.Run{Task: "changed too"}
	if r, _ := s.Get("r"); r.Task != "committed" {
		t.Fatal("Update exposes external aliases")
	}
}
func TestNilPersistenceMapIsInitialized(t *testing.T) {
	s, err := New(&memory{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("close test resource: %v", err)
		}
	})
	if err = s.Update(func(r map[string]fsm.Run) error { r["r"] = fsm.Run{RunID: "r"}; return nil }); err != nil {
		t.Fatal(err)
	}
}
func TestUnserializableUpdateDoesNotPoisonStore(t *testing.T) {
	s, err := New(&memory{runs: map[string]fsm.Run{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("close test resource: %v", err)
		}
	})
	err = s.Update(func(r map[string]fsm.Run) error {
		r["r"] = fsm.Run{RunID: "r", Deadline: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)}
		return nil
	})
	if err == nil {
		t.Fatal("unserializable state committed")
	}
	if len(s.All()) != 0 {
		t.Fatal("failed update changed state")
	}
}

type uncertainPersistence struct {
	memory
	fail bool
}

func (p *uncertainPersistence) Save(r map[string]fsm.Run) error {
	p.runs = r
	if p.fail {
		p.fail = false
		return errors.New("commit outcome uncertain")
	}
	return nil
}
func TestFailedCommitRequiresReopenBeforeFurtherWrites(t *testing.T) {
	p := &uncertainPersistence{memory: memory{runs: map[string]fsm.Run{}}, fail: true}
	s, err := New(p)
	if err != nil {
		t.Fatal(err)
	}
	err = s.Update(func(r map[string]fsm.Run) error { r["r"] = fsm.Run{RunID: "r", Task: "possibly committed"}; return nil })
	if !errors.Is(err, ErrPersistence) {
		t.Fatalf("missing persistence failure: %v", err)
	}
	invoked := false
	err = s.Update(func(r map[string]fsm.Run) error { invoked = true; delete(r, "r"); return nil })
	if !errors.Is(err, ErrPersistence) || invoked {
		t.Fatal("uncertain durable commit could be overwritten from stale memory")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := reopened.Close(); err != nil {
			t.Errorf("close test resource: %v", err)
		}
	})
	if r, ok := reopened.Get("r"); !ok || r.Task != "possibly committed" {
		t.Fatal("reopen did not load durable state")
	}
}
