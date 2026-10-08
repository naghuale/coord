package store

import (
	"bytes"
	"coord/agentsession"
	"crypto/ed25519"
	"errors"
	"math"
)

var (
	ErrAgentSessionAddressExists = errors.New("AgentSession address already exists")
	ErrAgentSessionNotFound      = errors.New("AgentSession not found")
	ErrAgentSessionNotActive     = errors.New("AgentSession is not active")
	ErrAgentSessionSameKey       = errors.New("AgentSession key rotation requires a different public key")
	ErrAgentSessionSequenceFull  = errors.New("AgentSession event sequence exhausted")
)

// RegisterAgentSession records an operator-approved public key binding in one
// canonical snapshot commit. Authorization of the operator belongs to the
// command boundary that calls this method.
func (s *CanonicalStore) RegisterAgentSession(address, agentID string, publicKey ed25519.PublicKey) error {
	return s.UpdateCanonical(func(state *CanonicalState) error {
		if _, exists := state.AgentSessions[address]; exists {
			return ErrAgentSessionAddressExists
		}
		now := s.now().UTC()
		event := agentsession.Event{
			ID: agentsession.EventID(address, 1), Address: address, AgentID: agentID,
			Sequence: 1, Type: agentsession.Registered, Generation: 1,
			PublicKey: append(ed25519.PublicKey(nil), publicKey...), CreatedAt: now,
		}
		state.AgentSessions[address] = agentsession.Session{
			Address: address, AgentID: agentID, Generation: 1,
			PublicKey: append(ed25519.PublicKey(nil), publicKey...), RegisteredAt: now, UpdatedAt: now,
		}
		state.AgentSessionEvents = append(state.AgentSessionEvents, event)
		return nil
	})
}

// RotateAgentSessionKey records a key rotation for the same address and stable
// AgentID. The session generation advances exactly once.
func (s *CanonicalStore) RotateAgentSessionKey(address string, publicKey ed25519.PublicKey) error {
	return s.UpdateCanonical(func(state *CanonicalState) error {
		prior, exists := state.AgentSessions[address]
		if !exists {
			return ErrAgentSessionNotFound
		}
		if !prior.Active() {
			return ErrAgentSessionNotActive
		}
		if bytes.Equal(prior.PublicKey, publicKey) {
			return ErrAgentSessionSameKey
		}
		if prior.Generation == math.MaxUint64 {
			return agentsession.ErrGenerationExhausted
		}
		sequence, err := nextAgentSessionSequence(state.AgentSessionEvents, address)
		if err != nil {
			return err
		}
		now := s.now().UTC()
		generation := prior.Generation + 1
		event := agentsession.Event{
			ID: agentsession.EventID(address, sequence), Address: address, AgentID: prior.AgentID,
			Sequence: sequence, Type: agentsession.KeyRotated, Generation: generation,
			PublicKey: append(ed25519.PublicKey(nil), publicKey...), CreatedAt: now,
		}
		prior.Generation = generation
		prior.PublicKey = append(ed25519.PublicKey(nil), publicKey...)
		prior.UpdatedAt = now
		state.AgentSessions[address] = prior
		state.AgentSessionEvents = append(state.AgentSessionEvents, event)
		return nil
	})
}

// ReplaceAgentSessionAddress revokes an active binding and registers its
// stable AgentID at a new address in the same canonical commit. Revoked
// addresses remain in history and cannot be reused.
func (s *CanonicalStore) ReplaceAgentSessionAddress(oldAddress, newAddress string, publicKey ed25519.PublicKey, reason string) error {
	return s.UpdateCanonical(func(state *CanonicalState) error {
		prior, exists := state.AgentSessions[oldAddress]
		if !exists {
			return ErrAgentSessionNotFound
		}
		if !prior.Active() {
			return ErrAgentSessionNotActive
		}
		if _, exists := state.AgentSessions[newAddress]; exists {
			return ErrAgentSessionAddressExists
		}
		oldSequence, err := nextAgentSessionSequence(state.AgentSessionEvents, oldAddress)
		if err != nil {
			return err
		}
		now := s.now().UTC()
		revoke := agentsession.Event{
			ID: agentsession.EventID(oldAddress, oldSequence), Address: oldAddress, AgentID: prior.AgentID,
			Sequence: oldSequence, Type: agentsession.Revoked, Generation: prior.Generation,
			Reason: reason, CreatedAt: now,
		}
		registered := agentsession.Event{
			ID: agentsession.EventID(newAddress, 1), Address: newAddress, AgentID: prior.AgentID,
			Sequence: 1, Type: agentsession.Registered, Generation: 1,
			PublicKey: append(ed25519.PublicKey(nil), publicKey...), CreatedAt: now,
		}
		prior.RevokedAt = now
		prior.RevocationReason = reason
		prior.UpdatedAt = now
		state.AgentSessions[oldAddress] = prior
		state.AgentSessions[newAddress] = agentsession.Session{
			Address: newAddress, AgentID: prior.AgentID, Generation: 1,
			PublicKey: append(ed25519.PublicKey(nil), publicKey...), RegisteredAt: now, UpdatedAt: now,
		}
		state.AgentSessionEvents = append(state.AgentSessionEvents, revoke, registered)
		return nil
	})
}

func nextAgentSessionSequence(events []agentsession.Event, address string) (uint64, error) {
	var last uint64
	for _, event := range events {
		if event.Address == address && event.Sequence > last {
			last = event.Sequence
		}
	}
	if last == math.MaxUint64 {
		return 0, ErrAgentSessionSequenceFull
	}
	if last == 0 {
		return 1, nil
	}
	return last + 1, nil
}
