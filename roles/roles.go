// Package roles manages stable identities and self-reported capabilities.
package roles

import (
	"coord/internal/atomicfile"
	"coord/internal/filelock"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"sync"
	"time"
)

var ErrPersistence = errors.New("roles persistence failure")

// ErrRevisionExhausted prevents CAS revisions from wrapping to a prior value.
var ErrRevisionExhausted = errors.New("role revision exhausted")

type Role string

const (
	Coordinator Role = "COORDINATOR"
	Executor    Role = "EXECUTOR"
	Reviewer    Role = "REVIEWER"
	Auditor     Role = "AUDITOR"
)

func (r Role) Valid() bool { return r == Coordinator || r == Executor || r == Reviewer || r == Auditor }

type Profile struct {
	Role         Role     `json:"role"`
	Model        string   `json:"profile"`
	Capabilities []string `json:"capabilities"`
}
type Patch struct {
	Role         Role      `json:"role"`
	Model        *string   `json:"profile,omitempty"`
	Capabilities *[]string `json:"capabilities,omitempty"`
}
type Update struct {
	Revision uint64    `json:"revision"`
	Added    []Profile `json:"added_roles,omitempty"`
	Removed  []Role    `json:"removed_roles,omitempty"`
	Updated  []Patch   `json:"updated_roles,omitempty"`
}
type Agent struct {
	AgentID  string           `json:"agent_id"`
	Session  string           `json:"session"`
	Revision uint64           `json:"revision"`
	Roles    map[Role]Profile `json:"roles"`
	Seen     map[string]bool  `json:"seen"`
}
type Event struct {
	MessageID, AgentID, Session string
	Revision                    uint64
	Timestamp                   time.Time
	Update                      Update
}
type snapshot struct {
	Version int
	Agents  map[string]Agent
	Events  []Event
}
type Registry struct {
	mu      sync.Mutex
	path    string
	state   snapshot
	release func() error
	closed  bool
	failed  error
}

func New(path string) (*Registry, error) {
	if path == "" {
		return nil, fmt.Errorf("roles snapshot path required")
	}
	release, err := filelock.Lock(path + ".lock")
	if err != nil {
		return nil, fmt.Errorf("lock roles snapshot: %w", err)
	}
	opened := false
	defer func() {
		if !opened {
			_ = release()
		}
	}()
	r := &Registry{path: path, release: release, state: snapshot{Version: 1, Agents: map[string]Agent{}, Events: []Event{}}}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		opened = true
		return r, nil
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(b, &r.state); err != nil {
		return nil, err
	}
	if r.state.Version != 1 || r.state.Agents == nil {
		return nil, fmt.Errorf("invalid roles snapshot")
	}
	for session, agent := range r.state.Agents {
		if agent.Session != session || agent.AgentID == "" || agent.Revision == 0 || agent.Roles == nil {
			return nil, fmt.Errorf("invalid roles snapshot agent %q", session)
		}
		for role, profile := range agent.Roles {
			if role != profile.Role {
				return nil, fmt.Errorf("invalid roles snapshot profile")
			}
			if err := validProfile(profile); err != nil {
				return nil, fmt.Errorf("invalid roles snapshot profile: %w", err)
			}
		}
		if agent.Seen == nil {
			agent.Seen = map[string]bool{}
			r.state.Agents[session] = agent
		}
	}
	opened = true
	return r, nil
}

// Close releases process ownership after the coordinator and registry writers stop.
func (r *Registry) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	if r.release != nil {
		return r.release()
	}
	return nil
}

func clone(s snapshot) snapshot {
	b, _ := json.Marshal(s)
	var out snapshot
	_ = json.Unmarshal(b, &out)
	return out
}
func (r *Registry) commit(s snapshot) (err error) {
	defer func() {
		if err != nil {
			err = fmt.Errorf("%w: %w", ErrPersistence, err)
			r.failed = err
		}
	}()
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if err = atomicfile.Write(r.path, b); err != nil {
		return err
	}
	// Publish independent decoded data, not slices and pointers owned by callers.
	var committed snapshot
	if err = json.Unmarshal(b, &committed); err != nil {
		return err
	}
	r.state = committed
	return nil
}
func validProfile(p Profile) error {
	if !p.Role.Valid() || p.Model == "" {
		return fmt.Errorf("invalid role/profile")
	}
	seen := map[string]bool{}
	for _, c := range p.Capabilities {
		if c == "" || seen[c] {
			return fmt.Errorf("invalid capabilities")
		}
		seen[c] = true
	}
	return nil
}

// Register is an operator action; messages cannot change agent_id.
func (r *Registry) Register(id, session string, profiles []Profile) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return fmt.Errorf("role registry closed")
	}
	if r.failed != nil {
		return fmt.Errorf("registry requires reopen after durability failure: %w", r.failed)
	}
	if id == "" || session == "" {
		return fmt.Errorf("identity required")
	}
	s := clone(r.state)
	if _, ok := s.Agents[session]; ok {
		return fmt.Errorf("session already registered")
	}
	a := Agent{AgentID: id, Session: session, Revision: 1, Roles: map[Role]Profile{}, Seen: map[string]bool{}}
	for _, p := range profiles {
		if err := validProfile(p); err != nil {
			return err
		}
		if _, ok := a.Roles[p.Role]; ok {
			return fmt.Errorf("duplicate role")
		}
		a.Roles[p.Role] = p
	}
	s.Agents[session] = a
	return r.commit(s)
}
func (u Update) Validate() error {
	if u.Revision == 0 || len(u.Added)+len(u.Removed)+len(u.Updated) == 0 {
		return fmt.Errorf("empty role update or missing revision")
	}
	seen := map[Role]bool{}
	check := func(role Role) error {
		if !role.Valid() || seen[role] {
			return fmt.Errorf("invalid or repeated role operation")
		}
		seen[role] = true
		return nil
	}
	for _, p := range u.Added {
		if err := check(p.Role); err != nil {
			return err
		}
		if err := validProfile(p); err != nil {
			return err
		}
	}
	for _, role := range u.Removed {
		if err := check(role); err != nil {
			return err
		}
	}
	for _, p := range u.Updated {
		if err := check(p.Role); err != nil {
			return err
		}
		if p.Model == nil && p.Capabilities == nil {
			return fmt.Errorf("empty role patch")
		}
		if p.Model != nil && *p.Model == "" {
			return fmt.Errorf("empty profile")
		}
	}
	return nil
}

// Apply persists every operation and its audit event in one atomic snapshot.
func (r *Registry) Apply(session, messageID string, u Update, now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return fmt.Errorf("role registry closed")
	}
	if r.failed != nil {
		return fmt.Errorf("registry requires reopen after durability failure: %w", r.failed)
	}
	if err := u.Validate(); err != nil {
		return err
	}
	if messageID == "" {
		return fmt.Errorf("message ID required")
	}
	s := clone(r.state)
	a, ok := s.Agents[session]
	if !ok {
		return fmt.Errorf("unregistered session")
	}
	if a.Seen[messageID] {
		return nil
	}
	if a.Revision != u.Revision {
		return fmt.Errorf("stale role revision")
	}
	if a.Revision == math.MaxUint64 {
		return ErrRevisionExhausted
	}
	for _, p := range u.Added {
		if _, ok := a.Roles[p.Role]; ok {
			return fmt.Errorf("role already exists")
		}
		a.Roles[p.Role] = p
	}
	for _, role := range u.Removed {
		if _, ok := a.Roles[role]; !ok {
			return fmt.Errorf("role does not exist")
		}
		delete(a.Roles, role)
	}
	for _, patch := range u.Updated {
		p, ok := a.Roles[patch.Role]
		if !ok {
			return fmt.Errorf("role does not exist")
		}
		if patch.Model != nil {
			p.Model = *patch.Model
		}
		if patch.Capabilities != nil {
			p.Capabilities = *patch.Capabilities
		}
		if err := validProfile(p); err != nil {
			return err
		}
		a.Roles[p.Role] = p
	}
	a.Revision++
	a.Seen[messageID] = true
	s.Agents[session] = a
	s.Events = append(s.Events, Event{messageID, a.AgentID, session, a.Revision, now, u})
	return r.commit(s)
}

type Requirement struct {
	Role         Role
	Model        string
	Capabilities []string
}
type Selection struct {
	AgentID, Session string
	Revision         uint64
	Profile          Profile
}

func (r *Registry) Select(session string, req Requirement) (Selection, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return Selection{}, fmt.Errorf("role registry closed")
	}
	if r.failed != nil {
		return Selection{}, fmt.Errorf("registry requires reopen after durability failure: %w", r.failed)
	}
	a, ok := r.state.Agents[session]
	if !ok {
		return Selection{}, fmt.Errorf("unregistered session")
	}
	p, ok := a.Roles[req.Role]
	if !ok || (req.Model != "" && p.Model != req.Model) {
		return Selection{}, fmt.Errorf("role/profile mismatch")
	}
	caps := map[string]bool{}
	for _, c := range p.Capabilities {
		caps[c] = true
	}
	for _, c := range req.Capabilities {
		if !caps[c] {
			return Selection{}, fmt.Errorf("capability mismatch: %s", c)
		}
	}
	p.Capabilities = append([]string{}, p.Capabilities...)
	return Selection{a.AgentID, session, a.Revision, p}, nil
}
func (r *Registry) Agent(session string) (Agent, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := clone(r.state).Agents[session]
	return a, ok
}
func (r *Registry) Events() []Event { r.mu.Lock(); defer r.mu.Unlock(); return clone(r.state).Events }
