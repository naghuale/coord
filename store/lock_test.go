package store

import (
	"errors"
	"testing"

	"coord/fsm"
)

type lockedOutPersistence struct {
	memory
	lockErr   error
	loadCalls int
}

func (p *lockedOutPersistence) Lock() (func() error, error) { return nil, p.lockErr }

func (p *lockedOutPersistence) Load() (map[string]fsm.Run, error) {
	p.loadCalls++
	return p.memory.Load()
}

// TestRefusedOwnershipFailsBeforeLoadingState pins that a persistence unable to
// take process ownership stops the store instead of proceeding unlocked.
func TestRefusedOwnershipFailsBeforeLoadingState(t *testing.T) {
	p := &lockedOutPersistence{memory: memory{runs: map[string]fsm.Run{}}, lockErr: errors.New("exclusive lock unavailable")}
	s, err := New(p)
	if err == nil {
		if closeErr := s.Close(); closeErr != nil {
			t.Errorf("close store: %v", closeErr)
		}
		t.Fatal("store opened snapshot without process ownership")
	}
	if p.loadCalls != 0 {
		t.Fatalf("Load called %d times after ownership was refused", p.loadCalls)
	}
}
