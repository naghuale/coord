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
func clone(v map[string]fsm.Run) map[string]fsm.Run {
	b, _ := json.Marshal(v)
	var out map[string]fsm.Run
	_ = json.Unmarshal(b, &out)
	return out
}
func (s *Store) All() map[string]fsm.Run       { s.mu.RLock(); defer s.mu.RUnlock(); return clone(s.runs) }
func (s *Store) Get(id string) (fsm.Run, bool) { r, ok := s.All()[id]; return r, ok }

// Update serializes mutations and persists them before exposing them.
func (s *Store) Update(fn func(map[string]fsm.Run) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("store closed")
	}
	next := clone(s.runs)
	if err := fn(next); err != nil {
		return err
	}
	if err := s.p.Save(next); err != nil {
		return fmt.Errorf("%w: %w", ErrPersistence, err)
	}
	s.runs = next
	return nil
}
