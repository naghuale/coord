// Package agentsession models Coord-owned bindings between a transport address
// and a stable agent identity. It contains no transport or storage dependency.
package agentsession

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

type EventType string

const (
	Registered EventType = "AGENT_SESSION_REGISTERED"
	KeyRotated EventType = "AGENT_SESSION_KEY_ROTATED"
	Revoked    EventType = "AGENT_SESSION_REVOKED"
)

var ErrGenerationExhausted = errors.New("agent session generation exhausted")

// Session is the current projection of one registered transport address.
// Address is the immutable key: changing it creates a new Session. PublicKey
// is public material used to verify signed frames, never a private credential.
type Session struct {
	Address          string            `json:"address"`
	AgentID          string            `json:"agent_id"`
	Generation       uint64            `json:"generation"`
	PublicKey        ed25519.PublicKey `json:"public_key"`
	RegisteredAt     time.Time         `json:"registered_at"`
	UpdatedAt        time.Time         `json:"updated_at"`
	RevokedAt        time.Time         `json:"revoked_at,omitempty"`
	RevocationReason string            `json:"revocation_reason,omitempty"`
}

func (s Session) Active() bool { return s.RevokedAt.IsZero() }

// Event is one immutable fact in a session's own event stream. Registration
// and key rotation carry the public key; revocation carries its reason.
type Event struct {
	ID         string            `json:"id"`
	Address    string            `json:"address"`
	AgentID    string            `json:"agent_id"`
	Sequence   uint64            `json:"sequence"`
	Type       EventType         `json:"type"`
	Generation uint64            `json:"generation"`
	PublicKey  ed25519.PublicKey `json:"public_key,omitempty"`
	Reason     string            `json:"reason,omitempty"`
	CreatedAt  time.Time         `json:"created_at"`
}

func EventID(address string, sequence uint64) string {
	return fmt.Sprintf("agent-session/%d:%s/event/%d", len(address), address, sequence)
}

// ValidateHistory checks that events form valid per-address histories and
// exactly reproduce the current projections.
func ValidateHistory(sessions map[string]Session, events []Event) error {
	if sessions == nil || events == nil {
		return fmt.Errorf("AgentSession collections must be present")
	}
	current := make(map[string]Session, len(sessions))
	sequences := make(map[string]uint64)
	seenIDs := make(map[string]struct{}, len(events))
	for i, event := range events {
		if err := validateEvent(event); err != nil {
			return fmt.Errorf("invalid AgentSession event %d: %w", i, err)
		}
		if _, exists := seenIDs[event.ID]; exists {
			return fmt.Errorf("duplicate AgentSession event ID %q", event.ID)
		}
		seenIDs[event.ID] = struct{}{}
		wantSequence := sequences[event.Address] + 1
		if event.Sequence != wantSequence {
			return fmt.Errorf("AgentSession %q event sequence: got %d, want %d", event.Address, event.Sequence, wantSequence)
		}
		sequences[event.Address] = event.Sequence
		prior, exists := current[event.Address]
		if exists && event.CreatedAt.Before(prior.UpdatedAt) {
			return fmt.Errorf("AgentSession %q event time precedes prior event", event.Address)
		}
		switch event.Type {
		case Registered:
			if exists || event.Sequence != 1 || event.Generation != 1 {
				return fmt.Errorf("AgentSession %q registration must be its first event at generation 1", event.Address)
			}
			current[event.Address] = Session{
				Address: event.Address, AgentID: event.AgentID, Generation: event.Generation,
				PublicKey: cloneKey(event.PublicKey), RegisteredAt: event.CreatedAt, UpdatedAt: event.CreatedAt,
			}
		case KeyRotated:
			if !exists || !prior.Active() || event.AgentID != prior.AgentID {
				return fmt.Errorf("AgentSession %q key rotation requires its active identity", event.Address)
			}
			if prior.Generation == math.MaxUint64 {
				return ErrGenerationExhausted
			}
			if event.Generation != prior.Generation+1 {
				return fmt.Errorf("AgentSession %q key rotation must increment generation by one", event.Address)
			}
			if bytes.Equal(event.PublicKey, prior.PublicKey) {
				return fmt.Errorf("AgentSession %q key rotation must change the public key", event.Address)
			}
			prior.Generation = event.Generation
			prior.PublicKey = cloneKey(event.PublicKey)
			prior.UpdatedAt = event.CreatedAt
			current[event.Address] = prior
		case Revoked:
			if !exists || !prior.Active() || event.AgentID != prior.AgentID || event.Generation != prior.Generation {
				return fmt.Errorf("AgentSession %q revocation must match its active binding", event.Address)
			}
			prior.RevokedAt = event.CreatedAt
			prior.RevocationReason = event.Reason
			prior.UpdatedAt = event.CreatedAt
			current[event.Address] = prior
		}
	}
	if len(current) != len(sessions) {
		return fmt.Errorf("AgentSession projections do not match event history")
	}
	for address, session := range sessions {
		if address != session.Address {
			return fmt.Errorf("AgentSession map key %q does not match address %q", address, session.Address)
		}
		if err := validateSession(session); err != nil {
			return fmt.Errorf("invalid AgentSession %q: %w", address, err)
		}
		replayed, exists := current[address]
		if !exists || !equalSession(session, replayed) {
			return fmt.Errorf("AgentSession %q projection differs from event history", address)
		}
	}
	return nil
}

// ValidateTransition requires the prior session history to remain an exact
// prefix. Snapshot validation then checks that new events produce the new view.
func ValidateTransition(previous, next map[string]Session, previousEvents, nextEvents []Event) error {
	if len(nextEvents) < len(previousEvents) {
		return fmt.Errorf("AgentSession event history cannot shrink")
	}
	for i, event := range previousEvents {
		if !equalEvent(event, nextEvents[i]) {
			return fmt.Errorf("AgentSession event history is append-only")
		}
	}
	for address, session := range previous {
		if nextSession, ok := next[address]; !ok || !equalSession(session, nextSession) && len(nextEvents) == len(previousEvents) {
			return fmt.Errorf("AgentSession %q changed without a new event", address)
		}
	}
	return ValidateHistory(next, nextEvents)
}

func validateSession(s Session) error {
	if strings.TrimSpace(s.Address) == "" || strings.TrimSpace(s.Address) != s.Address ||
		strings.TrimSpace(s.AgentID) == "" || strings.TrimSpace(s.AgentID) != s.AgentID ||
		s.Generation == 0 || len(s.PublicKey) != ed25519.PublicKeySize || s.RegisteredAt.IsZero() || s.UpdatedAt.IsZero() {
		return fmt.Errorf("address, AgentID, generation, public key and timestamps are required")
	}
	if s.UpdatedAt.Before(s.RegisteredAt) {
		return fmt.Errorf("updated time precedes registration")
	}
	if s.Active() && s.RevocationReason != "" {
		return fmt.Errorf("active AgentSession has a revocation reason")
	}
	if !s.Active() && (s.RevokedAt.Before(s.RegisteredAt) || s.RevokedAt.After(s.UpdatedAt) || strings.TrimSpace(s.RevocationReason) == "" || strings.TrimSpace(s.RevocationReason) != s.RevocationReason) {
		return fmt.Errorf("revoked AgentSession requires a valid revocation time and reason")
	}
	return nil
}

func validateEvent(e Event) error {
	if e.ID != EventID(e.Address, e.Sequence) || strings.TrimSpace(e.Address) == "" || strings.TrimSpace(e.Address) != e.Address ||
		strings.TrimSpace(e.AgentID) == "" || strings.TrimSpace(e.AgentID) != e.AgentID || e.Sequence == 0 || e.Generation == 0 || e.CreatedAt.IsZero() {
		return fmt.Errorf("identity, sequence, generation and creation time are required")
	}
	switch e.Type {
	case Registered, KeyRotated:
		if len(e.PublicKey) != ed25519.PublicKeySize || e.Reason != "" {
			return fmt.Errorf("registration and key rotation require a public key and no reason")
		}
	case Revoked:
		if len(e.PublicKey) != 0 || strings.TrimSpace(e.Reason) == "" || strings.TrimSpace(e.Reason) != e.Reason {
			return fmt.Errorf("revocation requires a reason and no public key")
		}
	default:
		return fmt.Errorf("unsupported event type %q", e.Type)
	}
	return nil
}

func cloneKey(key ed25519.PublicKey) ed25519.PublicKey {
	return append(ed25519.PublicKey(nil), key...)
}

func equalSession(a, b Session) bool {
	return a.Address == b.Address && a.AgentID == b.AgentID && a.Generation == b.Generation &&
		string(a.PublicKey) == string(b.PublicKey) && a.RegisteredAt.Equal(b.RegisteredAt) && a.UpdatedAt.Equal(b.UpdatedAt) &&
		a.RevokedAt.Equal(b.RevokedAt) && a.RevocationReason == b.RevocationReason
}

func equalEvent(a, b Event) bool {
	return a.ID == b.ID && a.Address == b.Address && a.AgentID == b.AgentID && a.Sequence == b.Sequence &&
		a.Type == b.Type && a.Generation == b.Generation && string(a.PublicKey) == string(b.PublicKey) &&
		a.Reason == b.Reason && a.CreatedAt.Equal(b.CreatedAt)
}
