package agentsession

import (
	"crypto/ed25519"
	"strings"
	"testing"
	"time"
)

type fakeClock struct{ now time.Time }

func (c fakeClock) Now() time.Time { return c.now }

func historyFixture() (map[string]Session, []Event) {
	address := "opencode:worker-1"
	key1 := ed25519.PublicKey(strings.Repeat("\x01", ed25519.PublicKeySize))
	key2 := ed25519.PublicKey(strings.Repeat("\x02", ed25519.PublicKeySize))
	clock := fakeClock{now: time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC)}
	t0 := clock.Now()
	events := []Event{
		{ID: EventID(address, 1), Address: address, AgentID: "agent-1", Sequence: 1, Type: Registered, Generation: 1, PublicKey: key1, CreatedAt: t0},
		{ID: EventID(address, 2), Address: address, AgentID: "agent-1", Sequence: 2, Type: KeyRotated, Generation: 2, PublicKey: key2, CreatedAt: t0.Add(time.Minute)},
		{ID: EventID(address, 3), Address: address, AgentID: "agent-1", Sequence: 3, Type: Revoked, Generation: 2, Reason: "address retired", CreatedAt: t0.Add(2 * time.Minute)},
	}
	sessions := map[string]Session{address: {
		Address: address, AgentID: "agent-1", Generation: 2, PublicKey: key2,
		RegisteredAt: t0, UpdatedAt: t0.Add(2 * time.Minute), RevokedAt: t0.Add(2 * time.Minute),
		RevocationReason: "address retired",
	}}
	return sessions, events
}

func TestValidateHistoryRebuildsRegisteredRotatedAndRevokedSession(t *testing.T) {
	sessions, events := historyFixture()
	if err := ValidateHistory(sessions, events); err != nil {
		t.Fatal(err)
	}
	if sessions["opencode:worker-1"].Active() {
		t.Fatal("revoked address is active")
	}
}

func TestValidateHistoryRejectsProjectionThatDisagreesWithEvents(t *testing.T) {
	sessions, events := historyFixture()
	session := sessions["opencode:worker-1"]
	session.AgentID = "forged-agent"
	sessions[session.Address] = session
	if err := ValidateHistory(sessions, events); err == nil || !strings.Contains(err.Error(), "differs from event history") {
		t.Fatalf("error=%v, want projection/history mismatch", err)
	}
}

func TestValidateHistoryRejectsProjectionWithDifferentPublicKey(t *testing.T) {
	sessions, events := historyFixture()
	session := sessions[events[0].Address]
	session.PublicKey = ed25519.PublicKey(strings.Repeat("\x09", ed25519.PublicKeySize))
	sessions[session.Address] = session
	if err := ValidateHistory(sessions, events); err == nil || !strings.Contains(err.Error(), "differs from event history") {
		t.Fatalf("error=%v, want public key projection/history mismatch", err)
	}
}

func TestValidateHistoryRejectsEventsWithoutCurrentProjection(t *testing.T) {
	_, events := historyFixture()
	if err := ValidateHistory(map[string]Session{}, events[:1]); err == nil || !strings.Contains(err.Error(), "projections do not match event history") {
		t.Fatalf("error=%v, want missing projection rejection", err)
	}
}

func TestValidateHistoryRejectsSkippedGeneration(t *testing.T) {
	sessions, events := historyFixture()
	events[1].Generation = 3
	if err := ValidateHistory(sessions, events); err == nil || !strings.Contains(err.Error(), "increment generation by one") {
		t.Fatalf("error=%v, want generation increment rejection", err)
	}
}

func TestValidateHistoryRejectsRevocationWithWrongGeneration(t *testing.T) {
	sessions, events := historyFixture()
	events[2].Generation = 1
	if err := ValidateHistory(sessions, events); err == nil || !strings.Contains(err.Error(), "revocation must match its active binding") {
		t.Fatalf("error=%v, want stale-generation revocation rejection", err)
	}
}

func TestValidateHistoryRejectsRotationWithoutChangingPublicKey(t *testing.T) {
	sessions, events := historyFixture()
	events[1].PublicKey = append(ed25519.PublicKey(nil), events[0].PublicKey...)
	if err := ValidateHistory(sessions, events); err == nil || !strings.Contains(err.Error(), "must change the public key") {
		t.Fatalf("error=%v, want same-key rotation rejection", err)
	}
}

func TestValidateHistoryRejectsEventTimeBeforePriorAddressEvent(t *testing.T) {
	sessions, events := historyFixture()
	events[2].CreatedAt = events[1].CreatedAt.Add(-time.Second)
	if err := ValidateHistory(sessions, events); err == nil || !strings.Contains(err.Error(), "event time precedes prior event") {
		t.Fatalf("error=%v, want non-monotonic event time rejection", err)
	}
}

func TestValidateHistoryRejectsEventsAfterRevocation(t *testing.T) {
	sessions, events := historyFixture()
	clock := fakeClock{now: time.Date(2026, 10, 8, 10, 3, 0, 0, time.UTC)}
	events = append(events, Event{
		ID: EventID("opencode:worker-1", 4), Address: "opencode:worker-1", AgentID: "agent-1",
		Sequence: 4, Type: KeyRotated, Generation: 3,
		PublicKey: ed25519.PublicKey(strings.Repeat("\x03", ed25519.PublicKeySize)),
		CreatedAt: clock.Now(),
	})
	if err := ValidateHistory(sessions, events); err == nil || !strings.Contains(err.Error(), "requires its active identity") {
		t.Fatalf("error=%v, want post-revocation event rejection", err)
	}
}

func TestValidateHistoryAllowsOneAgentAcrossMultipleAddresses(t *testing.T) {
	_, firstEvents := historyFixture()
	first := firstEvents[:1]
	secondAddress := "claude:worker-2"
	secondKey := ed25519.PublicKey(strings.Repeat("\x04", ed25519.PublicKeySize))
	clock := fakeClock{now: time.Date(2026, 10, 8, 11, 0, 0, 0, time.UTC)}
	registeredAt := clock.Now()
	firstSession := Session{
		Address: first[0].Address, AgentID: first[0].AgentID, Generation: 1,
		PublicKey: first[0].PublicKey, RegisteredAt: first[0].CreatedAt, UpdatedAt: first[0].CreatedAt,
	}
	secondEvent := Event{
		ID: EventID(secondAddress, 1), Address: secondAddress, AgentID: "agent-1",
		Sequence: 1, Type: Registered, Generation: 1, PublicKey: secondKey, CreatedAt: registeredAt,
	}
	sessions := map[string]Session{
		first[0].Address: firstSession,
		secondAddress: {
			Address: secondAddress, AgentID: "agent-1", Generation: 1,
			PublicKey: secondKey, RegisteredAt: registeredAt, UpdatedAt: registeredAt,
		},
	}
	if err := ValidateHistory(sessions, append(first, secondEvent)); err != nil {
		t.Fatalf("same AgentID on distinct addresses should be valid: %v", err)
	}
}

func TestValidateTransitionAcceptsRevocationAndRejectsProjectionChangeWithoutEvent(t *testing.T) {
	sessions, events := historyFixture()
	previousSessions := map[string]Session{events[0].Address: {
		Address: events[0].Address, AgentID: events[0].AgentID, Generation: 2,
		PublicKey: events[1].PublicKey, RegisteredAt: events[0].CreatedAt,
		UpdatedAt: events[1].CreatedAt,
	}}
	previousEvents := events[:2]
	if err := ValidateHistory(previousSessions, previousEvents); err != nil {
		t.Fatalf("invalid pre-revocation fixture: %v", err)
	}
	if err := ValidateTransition(previousSessions, sessions, previousEvents, events); err != nil {
		t.Fatalf("valid revocation transition rejected: %v", err)
	}
	mutated := map[string]Session{events[0].Address: sessions[events[0].Address]}
	mutatedSession := mutated[events[0].Address]
	mutatedSession.RevocationReason = "rewritten without event"
	mutated[events[0].Address] = mutatedSession
	if err := ValidateTransition(sessions, mutated, events, events); err == nil || !strings.Contains(err.Error(), "changed without a new event") {
		t.Fatalf("projection-only mutation error=%v, want rejection", err)
	}
}

func TestValidateTransitionRejectsRewrittenOrDeletedHistory(t *testing.T) {
	sessions, events := historyFixture()
	if err := ValidateTransition(sessions, sessions, events, events[:2]); err == nil || !strings.Contains(err.Error(), "cannot shrink") {
		t.Fatalf("truncated history error=%v", err)
	}
	rewritten := append([]Event(nil), events...)
	rewritten[0].AgentID = "other-agent"
	if err := ValidateTransition(sessions, sessions, events, rewritten); err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("rewritten history error=%v", err)
	}
}
