package store

import (
	"coord/agentsession"
	"crypto/ed25519"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

type canonicalTestClock struct{ now time.Time }

func (c *canonicalTestClock) Now() time.Time          { return c.now }
func (c *canonicalTestClock) advance(d time.Duration) { c.now = c.now.Add(d) }

func newCanonicalWithTestClock(t *testing.T, p CanonicalPersistence, clock *canonicalTestClock) *CanonicalStore {
	t.Helper()
	s, err := NewCanonicalWithClock(p, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func sessionTestPrivateKey(fill byte) ed25519.PrivateKey {
	return ed25519.NewKeyFromSeed([]byte(strings.Repeat(string([]byte{fill}), ed25519.SeedSize)))
}

func sessionTestKey(fill byte) ed25519.PublicKey {
	return sessionTestPrivateKey(fill).Public().(ed25519.PublicKey)
}

func TestCanonicalAgentSessionRegistrationPersistsOperatorBinding(t *testing.T) {
	clock := &canonicalTestClock{now: time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)}
	p := &canonicalMemory{state: EmptyCanonicalState()}
	s := newCanonicalWithTestClock(t, p, clock)
	key := sessionTestKey(0x11)
	if err := s.RegisterAgentSession("opencode:worker-1", "agent-1", key); err != nil {
		t.Fatal(err)
	}
	if p.saveCalls != 1 {
		t.Fatalf("durable saves=%d, want one", p.saveCalls)
	}
	session := p.state.AgentSessions["opencode:worker-1"]
	if session.AgentID != "agent-1" || session.Generation != 1 || !session.Active() || !session.RegisteredAt.Equal(clock.now) || string(session.PublicKey) != string(key) {
		t.Fatalf("persisted session=%+v", session)
	}
	if len(p.state.AgentSessionEvents) != 1 || p.state.AgentSessionEvents[0].Type != agentsession.Registered || !p.state.AgentSessionEvents[0].CreatedAt.Equal(clock.now) {
		t.Fatalf("persisted history=%+v", p.state.AgentSessionEvents)
	}
	if err := agentsession.ValidateHistory(p.state.AgentSessions, p.state.AgentSessionEvents); err != nil {
		t.Fatalf("persisted registration history invalid: %v", err)
	}
	if err := s.RegisterAgentSession("opencode:worker-1", "agent-1", sessionTestKey(0x12)); !errors.Is(err, ErrAgentSessionAddressExists) {
		t.Fatalf("duplicate registration error=%v", err)
	}
	if p.saveCalls != 1 {
		t.Fatalf("duplicate registration made a durable write: saves=%d", p.saveCalls)
	}
}

func TestCanonicalAgentSessionKeyRotationIncrementsGenerationAtomically(t *testing.T) {
	clock := &canonicalTestClock{now: time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)}
	p := &canonicalMemory{state: EmptyCanonicalState()}
	s := newCanonicalWithTestClock(t, p, clock)
	oldKey, newKey := sessionTestKey(0x21), sessionTestKey(0x22)
	if err := s.RegisterAgentSession("claude:worker-2", "agent-2", oldKey); err != nil {
		t.Fatal(err)
	}
	clock.advance(time.Minute)
	if err := s.RotateAgentSessionKey("claude:worker-2", newKey); err != nil {
		t.Fatal(err)
	}
	session := p.state.AgentSessions["claude:worker-2"]
	if session.Generation != 2 || !session.Active() || string(session.PublicKey) != string(newKey) || !session.UpdatedAt.Equal(clock.now) {
		t.Fatalf("rotated projection=%+v", session)
	}
	if p.saveCalls != 2 || len(p.state.AgentSessionEvents) != 2 {
		t.Fatalf("saves=%d events=%d, want two commits and two events", p.saveCalls, len(p.state.AgentSessionEvents))
	}
	rotation := p.state.AgentSessionEvents[1]
	if rotation.Type != agentsession.KeyRotated || rotation.Generation != 2 || string(rotation.PublicKey) != string(newKey) || !rotation.CreatedAt.Equal(clock.now) {
		t.Fatalf("rotation event=%+v", rotation)
	}
	if err := agentsession.ValidateHistory(p.state.AgentSessions, p.state.AgentSessionEvents); err != nil {
		t.Fatalf("rotated history invalid: %v", err)
	}
	if err := s.RotateAgentSessionKey("claude:worker-2", newKey); !errors.Is(err, ErrAgentSessionSameKey) {
		t.Fatalf("same-key rotation error=%v", err)
	}
	if p.saveCalls != 2 {
		t.Fatalf("rejected rotation made a durable write: saves=%d", p.saveCalls)
	}
}

func TestCanonicalAgentSessionRotationRejectsRevokedBindingWithoutWrite(t *testing.T) {
	clock := &canonicalTestClock{now: time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)}
	p := &canonicalMemory{state: EmptyCanonicalState()}
	s := newCanonicalWithTestClock(t, p, clock)
	if err := s.RegisterAgentSession("claude:retired", "agent-2b", sessionTestKey(0x23)); err != nil {
		t.Fatal(err)
	}
	clock.advance(time.Minute)
	if err := s.ReplaceAgentSessionAddress("claude:retired", "claude:current", sessionTestKey(0x24), "address changed"); err != nil {
		t.Fatal(err)
	}
	before, err := copyCanonicalState(p.state)
	if err != nil {
		t.Fatal(err)
	}
	saves := p.saveCalls
	err = s.RotateAgentSessionKey("claude:retired", sessionTestKey(0x25))
	if !errors.Is(err, ErrAgentSessionNotActive) {
		t.Fatalf("rotation of revoked binding error=%v, want ErrAgentSessionNotActive", err)
	}
	if p.saveCalls != saves || !reflect.DeepEqual(before, p.state) {
		t.Fatal("rejected rotation changed state or performed a durable write")
	}
}

func TestCanonicalAgentSessionAddressChangeRevokesAndRegistersInOneCommit(t *testing.T) {
	clock := &canonicalTestClock{now: time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)}
	p := &canonicalMemory{state: EmptyCanonicalState()}
	s := newCanonicalWithTestClock(t, p, clock)
	oldKey, newKey := sessionTestKey(0x31), sessionTestKey(0x32)
	if err := s.RegisterAgentSession("opencode:old", "agent-3", oldKey); err != nil {
		t.Fatal(err)
	}
	clock.advance(2 * time.Minute)
	if err := s.ReplaceAgentSessionAddress("opencode:old", "opencode:new", newKey, "session address changed"); err != nil {
		t.Fatal(err)
	}
	old := p.state.AgentSessions["opencode:old"]
	current := p.state.AgentSessions["opencode:new"]
	if old.Active() || old.RevocationReason != "session address changed" || !old.RevokedAt.Equal(clock.now) {
		t.Fatalf("old binding was not revoked: %+v", old)
	}
	if !current.Active() || current.AgentID != "agent-3" || current.Generation != 1 || string(current.PublicKey) != string(newKey) || !current.RegisteredAt.Equal(clock.now) {
		t.Fatalf("new binding was not registered: %+v", current)
	}
	if p.saveCalls != 2 || len(p.state.AgentSessionEvents) != 3 {
		t.Fatalf("saves=%d events=%d; address change must add both events in one commit", p.saveCalls, len(p.state.AgentSessionEvents))
	}
	if got := p.state.AgentSessionEvents[1:]; got[0].Type != agentsession.Revoked || got[1].Type != agentsession.Registered || got[0].AgentID != got[1].AgentID {
		t.Fatalf("address-change events=%+v", got)
	}
	if err := agentsession.ValidateHistory(p.state.AgentSessions, p.state.AgentSessionEvents); err != nil {
		t.Fatalf("address-change history invalid: %v", err)
	}
	if err := s.RegisterAgentSession("opencode:old", "agent-3", sessionTestKey(0x33)); !errors.Is(err, ErrAgentSessionAddressExists) {
		t.Fatalf("reused revoked address error=%v", err)
	}
	if p.saveCalls != 2 {
		t.Fatalf("rejected address reuse made a durable write: saves=%d", p.saveCalls)
	}
}

func TestCanonicalAgentSessionAddressChangeFailureDoesNotPartiallyRevoke(t *testing.T) {
	clock := &canonicalTestClock{now: time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)}
	p := &canonicalMemory{state: EmptyCanonicalState()}
	s := newCanonicalWithTestClock(t, p, clock)
	if err := s.RegisterAgentSession("opencode:old", "agent-4", sessionTestKey(0x41)); err != nil {
		t.Fatal(err)
	}
	before, err := copyCanonicalState(p.state)
	if err != nil {
		t.Fatal(err)
	}
	p.fail = true
	clock.advance(time.Minute)
	err = s.ReplaceAgentSessionAddress("opencode:old", "opencode:new", sessionTestKey(0x42), "session address changed")
	if !errors.Is(err, ErrPersistence) {
		t.Fatalf("address-change commit error=%v, want ErrPersistence", err)
	}
	if !reflect.DeepEqual(before, p.state) {
		t.Fatal("failed address-change commit changed the durable projection")
	}
	if p.saveCalls != 2 {
		t.Fatalf("saves=%d, want registration and one failed atomic save", p.saveCalls)
	}
}

func TestCanonicalAgentSessionAddressChangeRejectsInactiveOrExistingAddressWithoutWrite(t *testing.T) {
	clock := &canonicalTestClock{now: time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)}
	p := &canonicalMemory{state: EmptyCanonicalState()}
	s := newCanonicalWithTestClock(t, p, clock)
	if err := s.RegisterAgentSession("opencode:old", "agent-5", sessionTestKey(0x51)); err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterAgentSession("opencode:destination", "agent-6", sessionTestKey(0x52)); err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterAgentSession("opencode:source", "agent-7", sessionTestKey(0x53)); err != nil {
		t.Fatal(err)
	}
	clock.advance(time.Minute)
	if err := s.ReplaceAgentSessionAddress("opencode:old", "opencode:new", sessionTestKey(0x54), "address changed"); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceAgentSessionAddress("opencode:source", "opencode:destination", sessionTestKey(0x55), "address changed"); !errors.Is(err, ErrAgentSessionAddressExists) {
		t.Fatalf("existing active destination error=%v, want ErrAgentSessionAddressExists", err)
	}
	if err := s.ReplaceAgentSessionAddress("opencode:source", "opencode:old", sessionTestKey(0x56), "address changed"); !errors.Is(err, ErrAgentSessionAddressExists) {
		t.Fatalf("existing revoked destination error=%v, want ErrAgentSessionAddressExists", err)
	}
	if err := s.ReplaceAgentSessionAddress("opencode:old", "opencode:another", sessionTestKey(0x57), "address changed"); !errors.Is(err, ErrAgentSessionNotActive) {
		t.Fatalf("replacement from revoked source error=%v, want ErrAgentSessionNotActive", err)
	}
	if p.saveCalls != 4 {
		t.Fatalf("saves=%d, want only three registrations and one successful replacement", p.saveCalls)
	}
	if !p.state.AgentSessions["opencode:source"].Active() || !p.state.AgentSessions["opencode:destination"].Active() {
		t.Fatal("rejected address changes changed the source or destination bindings")
	}
}

func TestNewCanonicalWithClockRejectsNilClock(t *testing.T) {
	p := &canonicalMemory{state: EmptyCanonicalState()}
	if _, err := NewCanonicalWithClock(p, nil); err == nil || !strings.Contains(err.Error(), "canonical clock required") {
		t.Fatalf("nil clock error=%v, want canonical clock required", err)
	}
	if p.loadCalls != 0 || p.saveCalls != 0 {
		t.Fatalf("nil clock touched persistence: loads=%d saves=%d", p.loadCalls, p.saveCalls)
	}
}
