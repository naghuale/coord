// Package store publishes state only after a successful durable commit.
package store

import (
	"coord/fsm"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
)

var ErrPersistence = errors.New("persistence failure")

// Persistence loads and saves the durable state. A durable implementation used
// by a coordinator must also expose Lock() (func() error, error) through its
// concrete type; if it cannot guarantee exclusive ownership, Lock must fail
// closed. In-memory test persistence may omit process locking.
type Persistence interface {
	Load() (map[string]fsm.Run, error)
	Save(map[string]fsm.Run) error
}
type Store struct {
	mu      sync.RWMutex
	runs    map[string]fsm.Run
	p       Persistence
	release func() error
	closed  bool
	failed  error
}

func New(p Persistence) (*Store, error) {
	if p == nil {
		return nil, fmt.Errorf("persistence required")
	}
	var release func() error
	if locker, ok := p.(interface{ Lock() (func() error, error) }); ok {
		var err error
		release, err = locker.Lock()
		if err != nil {
			return nil, err
		}
	}
	r, err := p.Load()
	if err != nil {
		if release != nil {
			_ = release()
		}
		return nil, err
	}
	r, err = copyRuns(r)
	if err != nil {
		if release != nil {
			_ = release()
		}
		return nil, fmt.Errorf("copy loaded state: %w", err)
	}
	return &Store{runs: r, p: p, release: release}, nil
}

// Close releases process ownership after the coordinator loop has stopped.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if s.release != nil {
		return s.release()
	}
	return nil
}
func copyRuns(v map[string]fsm.Run) (map[string]fsm.Run, error) {
	if v == nil {
		return map[string]fsm.Run{}, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out map[string]fsm.Run
	if err = json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func clone(v map[string]fsm.Run) map[string]fsm.Run {
	// Stored state has already passed copyRuns before publication.
	out, _ := copyRuns(v)
	return out
}
func (s *Store) All() map[string]fsm.Run { s.mu.RLock(); defer s.mu.RUnlock(); return clone(s.runs) }

// Get copies only the requested run while keeping nested state isolated.
func (s *Store) Get(id string) (fsm.Run, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.runs[id]
	if !ok {
		return fsm.Run{}, false
	}
	return clone(map[string]fsm.Run{id: r})[id], true
}

// Update serializes mutations and persists them before exposing them.
func (s *Store) Update(fn func(map[string]fsm.Run) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("store closed")
	}
	if s.failed != nil {
		return fmt.Errorf("store requires reopen after durability failure: %w", s.failed)
	}
	next := clone(s.runs)
	if err := fn(next); err != nil {
		return err
	}
	committed, err := copyRuns(next)
	if err != nil {
		return fmt.Errorf("copy updated state: %w", err)
	}
	// Neither a callback nor a persistence implementation owns published memory.
	if err := s.p.Save(clone(committed)); err != nil {
		// Save may have renamed its snapshot before returning an fsync error.
		// Further writes from old memory could erase that possibly committed state.
		s.failed = fmt.Errorf("%w: %w", ErrPersistence, err)
		return s.failed
	}
	s.runs = committed
	return nil
}
