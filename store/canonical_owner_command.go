package store

import (
	"bytes"
	"coord/ownercredential"
	"coord/task"
	"coord/taskid"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
)

var (
	ErrOwnerChallengeUnknown   = errors.New("OWNER_CHALLENGE_UNKNOWN")
	ErrOwnerChallengeExpired   = errors.New("OWNER_CHALLENGE_EXPIRED")
	ErrOwnerCredentialUnknown  = errors.New("OWNER_CREDENTIAL_UNKNOWN")
	ErrOwnerCredentialInactive = errors.New("OWNER_CREDENTIAL_INACTIVE")
	ErrOwnerCredentialRevision = errors.New("OWNER_CREDENTIAL_REVISION_MISMATCH")
	ErrOwnerCommandReplay      = errors.New("OWNER_COMMAND_REPLAY_MISMATCH")
	ErrOwnerCommandInvalid     = errors.New("OWNER_COMMAND_INVALID")
)

const maxOwnerChallenges = 256

// ConfigureOwnerWebAuthnPolicy pins the localhost WebAuthn RP and origin once
// when coordd constructs its control listener. A later listener cannot silently
// broaden or change the human credential's origin trust.
func (s *CanonicalStore) ConfigureOwnerWebAuthnPolicy(rpID, origin string) error {
	if rpID != "localhost" {
		return fmt.Errorf("owner WebAuthn RP ID must be localhost")
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme != "http" || parsed.Hostname() != "localhost" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("owner WebAuthn origin must be an exact http://localhost[:port] origin")
	}
	if port := parsed.Port(); port != "" {
		for _, r := range port {
			if r < '0' || r > '9' {
				return fmt.Errorf("invalid Owner WebAuthn origin port")
			}
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ownerRPID != "" && (s.ownerRPID != rpID || s.ownerOrigin != origin) {
		return fmt.Errorf("owner WebAuthn policy is already pinned")
	}
	s.ownerRPID, s.ownerOrigin = rpID, origin
	return nil
}

// IssueOwnerCommandChallenge is read-only with respect to canonical state. The
// in-memory challenge is daemon-epoch-bound and disappears on restart.
func (s *CanonicalStore) IssueOwnerCommandChallenge(request ownercredential.CommandRequest) (ownercredential.Command, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ownercredential.Command{}, errors.New("store closed")
	}
	if s.failed != nil {
		return ownercredential.Command{}, fmt.Errorf("store requires reopen after durability failure: %w", s.failed)
	}
	if s.ownerRPID == "" || !ownercredential.SupportedCommandKind(request.Kind) {
		return ownercredential.Command{}, ErrOwnerCommandInvalid
	}
	aggregate, ok := s.state.OwnerCredentials[request.PrincipalID]
	if !ok {
		return ownercredential.Command{}, ErrOwnerCredentialUnknown
	}
	credential, ok := aggregate.Credentials[request.CredentialID]
	if !ok {
		return ownercredential.Command{}, ErrOwnerCredentialUnknown
	}
	if !credential.Active() {
		return ownercredential.Command{}, ErrOwnerCredentialInactive
	}
	if aggregate.CredentialRevision != request.ExpectedCredentialRevision {
		return ownercredential.Command{}, ErrOwnerCredentialRevision
	}
	now := s.now().UTC()
	for id, pending := range s.ownerChallenges {
		if !now.Before(pending.ExpiresAt) {
			delete(s.ownerChallenges, id)
		}
	}
	if len(s.ownerChallenges) >= maxOwnerChallenges {
		return ownercredential.Command{}, fmt.Errorf("owner challenge capacity reached")
	}
	challenge, err := ownercredential.NewChallenge(request, now, ownercredential.ChallengeLifetime)
	if err != nil {
		return ownercredential.Command{}, fmt.Errorf("%w: %v", ErrOwnerCommandInvalid, err)
	}
	s.ownerChallenges[challenge.ChallengeID] = challenge
	return challenge, nil
}

// OwnerCommandChallenge returns the server's exact pending envelope. It does
// not authorize a write and is safe to repeat for clients recovering a response.
func (s *CanonicalStore) OwnerCommandChallenge(challengeID string) (ownercredential.Command, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	challenge, ok := s.ownerChallenges[challengeID]
	if !ok {
		return ownercredential.Command{}, ErrOwnerChallengeUnknown
	}
	if !s.now().Before(challenge.ExpiresAt) {
		return ownercredential.Command{}, ErrOwnerChallengeExpired
	}
	return challenge, nil
}

// ApplyOwnerCredentialCommand handles exactly the credential lifecycle
// commands from COMMAND-BOUNDARY §3.2. The command must byte-match a challenge
// issued by this CanonicalStore; signature verification, current credential
// checks, nonce/replay, effect, event, receipt and Save are serialized under
// the same store lock and committed once.
func (s *CanonicalStore) ApplyOwnerCredentialCommand(challengeID string, submitted ownercredential.Command, assertion ownercredential.Assertion) (ownercredential.Receipt, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ownercredential.Receipt{}, false, errors.New("store closed")
	}
	if s.failed != nil {
		return ownercredential.Receipt{}, false, fmt.Errorf("store requires reopen after durability failure: %w", s.failed)
	}
	// A durable exact retry can be recognized after daemon restart even though
	// the in-memory challenge table was intentionally discarded. The receipt's
	// digest binds every command field, including the original challenge ID.
	if aggregate, exists := s.state.OwnerCredentials[submitted.PrincipalID]; exists {
		if prior, committed := aggregate.Receipts[submitted.MessageID]; committed {
			if challengeID != submitted.ChallengeID || prior.CommandKind != submitted.Kind || prior.CommandDigest != submitted.Digest || prior.Nonce != submitted.Nonce || prior.CredentialID != submitted.CredentialID || prior.ExpectedCredentialRevision != submitted.ExpectedCredentialRevision {
				return ownercredential.Receipt{}, false, ErrOwnerCommandReplay
			}
			credential, ok := aggregate.Credentials[submitted.CredentialID]
			if !ok || s.ownerRPID == "" {
				return ownercredential.Receipt{}, false, ErrOwnerCredentialUnknown
			}
			if err := submitted.Validate(); err != nil {
				return ownercredential.Receipt{}, false, ErrOwnerCommandInvalid
			}
			credential.SignCount = 0 // a durable retry cannot consume the same authenticator counter twice
			if _, err := ownercredential.VerifyAssertion(submitted, credential, assertion, s.ownerRPID, s.ownerOrigin); err != nil {
				return ownercredential.Receipt{}, false, fmt.Errorf("%w: %v", ErrOwnerCommandInvalid, err)
			}
			return prior, true, nil
		}
	}
	challenge, ok := s.ownerChallenges[challengeID]
	if !ok || challenge.ChallengeID != challengeID || !submitted.Same(challenge) {
		return ownercredential.Receipt{}, false, ErrOwnerChallengeUnknown
	}
	if err := challenge.Validate(); err != nil {
		return ownercredential.Receipt{}, false, ErrOwnerCommandInvalid
	}
	if !s.now().Before(challenge.ExpiresAt) {
		return ownercredential.Receipt{}, false, ErrOwnerChallengeExpired
	}
	if !ownercredential.SupportedCommandKind(challenge.Kind) {
		return ownercredential.Receipt{}, false, ErrOwnerCommandInvalid
	}
	// G95-1 lesson: disable gate BEFORE anything route-specific.
	if ownercredential.IsDelegationCommand(challenge.Kind) && !coordinatorDelegationActive() {
		return ownercredential.Receipt{}, false, ErrCoordinatorDelegationDisabled
	}
	aggregate, ok := s.state.OwnerCredentials[challenge.PrincipalID]
	if !ok {
		return ownercredential.Receipt{}, false, ErrOwnerCredentialUnknown
	}
	credential, ok := aggregate.Credentials[challenge.CredentialID]
	if !ok {
		return ownercredential.Receipt{}, false, ErrOwnerCredentialUnknown
	}
	if s.ownerRPID == "" {
		return ownercredential.Receipt{}, false, ErrOwnerCommandInvalid
	}
	if prior, exists := aggregate.Receipts[challenge.MessageID]; exists {
		if prior.CommandKind != challenge.Kind || prior.CommandDigest != challenge.Digest || prior.Nonce != challenge.Nonce || prior.CredentialID != challenge.CredentialID {
			return ownercredential.Receipt{}, false, ErrOwnerCommandReplay
		}
		verifyCredential := credential
		verifyCredential.SignCount = 0 // an exact lost-response retry has already consumed this counter
		if _, err := ownercredential.VerifyAssertion(challenge, verifyCredential, assertion, s.ownerRPID, s.ownerOrigin); err != nil {
			return ownercredential.Receipt{}, false, fmt.Errorf("%w: %v", ErrOwnerCommandInvalid, err)
		}
		return prior, true, nil
	}
	if !credential.Active() {
		return ownercredential.Receipt{}, false, ErrOwnerCredentialInactive
	}
	if aggregate.CredentialRevision != challenge.ExpectedCredentialRevision {
		return ownercredential.Receipt{}, false, ErrOwnerCredentialRevision
	}
	for _, receipt := range aggregate.Receipts {
		if receipt.Nonce == challenge.Nonce {
			return ownercredential.Receipt{}, false, ErrOwnerCommandReplay
		}
	}
	signCount, err := ownercredential.VerifyAssertion(challenge, credential, assertion, s.ownerRPID, s.ownerOrigin)
	if err != nil {
		return ownercredential.Receipt{}, false, fmt.Errorf("%w: %v", ErrOwnerCommandInvalid, err)
	}
	if challenge.Kind == ownercredential.CommandCreateTask {
		return s.applyOwnerTaskCreateLocked(challenge, credential, signCount)
	}
	if ownercredential.IsDelegationCommand(challenge.Kind) {
		return s.applyOwnerDelegationCommandLocked(challenge, credential, signCount)
	}

	now := s.now().UTC()
	next, err := copyCanonicalState(s.state)
	if err != nil {
		return ownercredential.Receipt{}, false, fmt.Errorf("copy canonical state: %w", err)
	}
	aggregate = next.OwnerCredentials[challenge.PrincipalID]
	signer := aggregate.Credentials[challenge.CredentialID]
	signer.SignCount = signCount
	aggregate.Credentials[signer.ID] = signer
	var targetID string
	switch challenge.Kind {
	case ownercredential.CommandRegisterCredential:
		var payload ownercredential.RegisterCredentialPayload
		if err := decodeOwnerPayload(challenge.Payload, &payload); err != nil {
			return ownercredential.Receipt{}, false, err
		}
		if payload.CredentialID == signer.ID || payload.CredentialID == "" || strings.TrimSpace(payload.CredentialID) != payload.CredentialID {
			return ownercredential.Receipt{}, false, ErrOwnerCommandInvalid
		}
		if _, exists := aggregate.Credentials[payload.CredentialID]; exists {
			return ownercredential.Receipt{}, false, ErrOwnerCommandReplay
		}
		newCredential, err := ownercredential.CredentialFromCOSE(payload.CredentialID, payload.COSEPublicKey, now)
		if err != nil {
			return ownercredential.Receipt{}, false, fmt.Errorf("%w: %v", ErrOwnerCommandInvalid, err)
		}
		for _, current := range aggregate.Credentials {
			if current.Fingerprint == newCredential.Fingerprint {
				return ownercredential.Receipt{}, false, ErrOwnerCommandInvalid
			}
		}
		aggregate.Credentials[newCredential.ID] = newCredential
		targetID = newCredential.ID
	case ownercredential.CommandRevokeCredential:
		var payload ownercredential.RevokeCredentialPayload
		if err := decodeOwnerPayload(challenge.Payload, &payload); err != nil {
			return ownercredential.Receipt{}, false, err
		}
		target, exists := aggregate.Credentials[payload.CredentialID]
		if !exists || !target.Active() || payload.CredentialID == "" {
			return ownercredential.Receipt{}, false, ErrOwnerCredentialUnknown
		}
		if aggregate.ActiveCredentialCount() <= 1 {
			return ownercredential.Receipt{}, false, ErrOwnerCommandInvalid
		}
		target.RevokedAt = now
		aggregate.Credentials[target.ID] = target
		targetID = target.ID
	default:
		return ownercredential.Receipt{}, false, ErrOwnerCommandInvalid
	}
	if aggregate.CredentialRevision == ^uint64(0) || aggregate.Sequence == ^uint64(0) {
		return ownercredential.Receipt{}, false, ErrOwnerCredentialRevision
	}
	newRevision, sequence := aggregate.CredentialRevision+1, aggregate.Sequence+1
	kind := ownercredential.Registered
	if challenge.Kind == ownercredential.CommandRevokeCredential {
		kind = ownercredential.Revoked
	}
	if aggregate.CommandSequence == ^uint64(0) {
		return ownercredential.Receipt{}, false, ErrOwnerCredentialRevision
	}
	commandSequence := aggregate.CommandSequence + 1
	receipt := ownercredential.Receipt{MessageID: challenge.MessageID, CommandKind: challenge.Kind, CommandDigest: challenge.Digest, CredentialID: signer.ID, Nonce: challenge.Nonce, ExpectedCredentialRevision: challenge.ExpectedCredentialRevision, CredentialRevision: newRevision, CommandSequence: commandSequence, EventSequence: sequence, SignCount: signCount, RecordedAt: now}
	event := ownercredential.Event{Sequence: sequence, Kind: kind, CredentialID: targetID, CredentialRevision: newRevision, MessageID: challenge.MessageID, CommandDigest: challenge.Digest, Nonce: challenge.Nonce, RecordedAt: now}
	aggregate.CredentialRevision, aggregate.Sequence, aggregate.CommandSequence = newRevision, sequence, commandSequence
	aggregate.Events = append(aggregate.Events, event)
	aggregate.Receipts[receipt.MessageID] = receipt
	if err := aggregate.Validate(); err != nil {
		return ownercredential.Receipt{}, false, err
	}
	next.OwnerCredentials[challenge.PrincipalID] = aggregate
	next.SchemaVersion = CanonicalSchemaVersion
	if s.state.Revision == ^uint64(0) {
		return ownercredential.Receipt{}, false, errors.New("canonical revision exhausted")
	}
	next.Revision = s.state.Revision + 1
	if err := ValidateCanonicalState(next); err != nil {
		return ownercredential.Receipt{}, false, err
	}
	if err := ValidateCanonicalTransition(s.state, next); err != nil {
		return ownercredential.Receipt{}, false, err
	}
	if err := s.validateAssignmentMode(next); err != nil {
		return ownercredential.Receipt{}, false, err
	}
	committed, err := copyCanonicalState(next)
	if err != nil {
		return ownercredential.Receipt{}, false, fmt.Errorf("copy updated canonical state: %w", err)
	}
	if err := s.p.Save(committed); err != nil {
		s.failed = fmt.Errorf("%w: %w", ErrPersistence, err)
		return ownercredential.Receipt{}, false, s.failed
	}
	s.state = committed
	return receipt, false, nil
}

// applyOwnerTaskCreateLocked commits the authenticated Task, DomainEvent,
// Inbox receipt and OwnerCredential receipt under the caller-held store lock.
func (s *CanonicalStore) applyOwnerTaskCreateLocked(challenge ownercredential.Command, signer ownercredential.Credential, signCount uint32) (ownercredential.Receipt, bool, error) {
	var payload ownercredential.CreateTaskPayload
	if err := decodeOwnerPayload(challenge.Payload, &payload); err != nil {
		return ownercredential.Receipt{}, false, err
	}
	if strings.TrimSpace(payload.Title) == "" {
		return ownercredential.Receipt{}, false, ErrOwnerCommandInvalid
	}
	if err := payload.StagePlan.Validate(); err != nil {
		return ownercredential.Receipt{}, false, fmt.Errorf("%w: %v", ErrOwnerCommandInvalid, err)
	}
	if err := payload.StagePlan.ValidateRuntimeSupport(); err != nil {
		return ownercredential.Receipt{}, false, fmt.Errorf("%w: %v", ErrOwnerCommandInvalid, err)
	}
	if s.state.Revision == ^uint64(0) {
		return ownercredential.Receipt{}, false, errors.New("canonical revision exhausted")
	}
	if s.state.OwnerCredentials[challenge.PrincipalID].CommandSequence == ^uint64(0) {
		return ownercredential.Receipt{}, false, ErrOwnerCredentialRevision
	}
	id, err := taskid.New()
	if err != nil {
		return ownercredential.Receipt{}, false, fmt.Errorf("generate TaskID: %w", err)
	}
	if _, exists := s.state.Tasks[id]; exists {
		return ownercredential.Receipt{}, false, fmt.Errorf("generated TaskID collision")
	}
	now := s.now().UTC()
	next, err := copyCanonicalState(s.state)
	if err != nil {
		return ownercredential.Receipt{}, false, fmt.Errorf("copy canonical state: %w", err)
	}
	stage := task.Execution
	created := task.Task{ID: id, Title: payload.Title, Description: payload.Description, OwnerID: challenge.PrincipalID, Revision: 1, LastEventSeq: 1, StagePlan: payload.StagePlan, CurrentStage: &stage, Status: task.Pending, CreatedAt: now, UpdatedAt: now}
	if err := created.Validate(); err != nil {
		return ownercredential.Receipt{}, false, fmt.Errorf("%w: %v", ErrOwnerCommandInvalid, err)
	}
	event := task.DomainEvent{ID: task.EventID(id, 1), TaskID: id, EventSeq: 1, TaskRevision: 1, Type: task.TaskCreated, ActorID: challenge.PrincipalID, Summary: "Task created", CreatedAt: now}
	aggregate := next.OwnerCredentials[challenge.PrincipalID]
	currentSigner := aggregate.Credentials[signer.ID]
	currentSigner.SignCount = signCount
	aggregate.Credentials[signer.ID] = currentSigner
	commandSequence := aggregate.CommandSequence + 1
	receipt := ownercredential.Receipt{MessageID: challenge.MessageID, CommandKind: challenge.Kind, CommandDigest: challenge.Digest, CredentialID: signer.ID, TaskID: string(id), Nonce: challenge.Nonce, ExpectedCredentialRevision: challenge.ExpectedCredentialRevision, CredentialRevision: aggregate.CredentialRevision, CommandSequence: commandSequence, SignCount: signCount, RecordedAt: now}
	aggregate.CommandSequence = commandSequence
	aggregate.Receipts[receipt.MessageID] = receipt
	if err := aggregate.Validate(); err != nil {
		return ownercredential.Receipt{}, false, err
	}
	next.OwnerCredentials[challenge.PrincipalID] = aggregate
	next.Tasks[id] = created
	next.Events = append(next.Events, event)
	next.Inbox[InboxKey(challenge.PrincipalID, challenge.MessageID)] = InboxReceipt{ActorID: challenge.PrincipalID, MessageID: challenge.MessageID, OwnerCommandDigest: challenge.Digest, OwnerNonce: challenge.Nonce, OwnerCredentialRevision: challenge.ExpectedCredentialRevision, TaskID: id, EventIDs: []string{event.ID}, ExpectedRevision: 0, CommittedRevision: 1, RecordedAt: now}
	next.SchemaVersion = CanonicalSchemaVersion
	next.Revision = s.state.Revision + 1
	if err := ValidateCanonicalState(next); err != nil {
		return ownercredential.Receipt{}, false, err
	}
	if err := ValidateCanonicalTransition(s.state, next); err != nil {
		return ownercredential.Receipt{}, false, err
	}
	if err := s.validateAssignmentMode(next); err != nil {
		return ownercredential.Receipt{}, false, err
	}
	committed, err := copyCanonicalState(next)
	if err != nil {
		return ownercredential.Receipt{}, false, fmt.Errorf("copy updated canonical state: %w", err)
	}
	if err := s.p.Save(committed); err != nil {
		s.failed = fmt.Errorf("%w: %w", ErrPersistence, err)
		return ownercredential.Receipt{}, false, s.failed
	}
	s.state = committed
	return receipt, false, nil
}

func decodeOwnerPayload(data []byte, target any) error {
	if err := ownercredential.ValidatePayload(data); err != nil {
		return fmt.Errorf("%w: %v", ErrOwnerCommandInvalid, err)
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return fmt.Errorf("%w: invalid command payload: %v", ErrOwnerCommandInvalid, err)
	}
	var extra any
	if err := d.Decode(&extra); errors.Is(err, io.EOF) {
		return nil
	}
	return fmt.Errorf("%w: trailing command payload", ErrOwnerCommandInvalid)
}
