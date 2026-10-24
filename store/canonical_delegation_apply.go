package store

import (
	"coord/assignment"
	ownercredential "coord/ownercredential"
	"fmt"
	"strings"
	"time"
)

// applyOwnerDelegationCommandLocked commits one #93b OWNER command under the
// caller-held store lock. The disable gate is already checked by the caller.
func (s *CanonicalStore) applyOwnerDelegationCommandLocked(challenge ownercredential.Command, signer ownercredential.Credential, signCount uint32) (ownercredential.Receipt, bool, error) {
	now := s.now().UTC()
	next, err := copyCanonicalState(s.state)
	if err != nil {
		return ownercredential.Receipt{}, false, fmt.Errorf("copy canonical state: %w", err)
	}
	if next.CoordinatorDelegations == nil {
		next.CoordinatorDelegations = map[string]DelegationRecord{}
	}
	aggregate := next.OwnerCredentials[challenge.PrincipalID]
	cred := aggregate.Credentials[challenge.CredentialID]
	cred.SignCount = signCount
	aggregate.Credentials[cred.ID] = cred

	switch challenge.Kind {
	case ownercredential.CommandDelegationGrant:
		if err := applyDelegationGrant(&next, challenge, now); err != nil {
			return ownercredential.Receipt{}, false, err
		}
	case ownercredential.CommandDelegationRevoke:
		if err := applyDelegationRevoke(&next, challenge, now); err != nil {
			return ownercredential.Receipt{}, false, err
		}
	case ownercredential.CommandLossConfirm:
		if err := applyLossConfirm(&next, challenge, now); err != nil {
			return ownercredential.Receipt{}, false, err
		}
	case ownercredential.CommandVisibilityConfirm:
		if err := applyVisibilityConfirm(&next, challenge, now); err != nil {
			return ownercredential.Receipt{}, false, err
		}
	default:
		return ownercredential.Receipt{}, false, ErrOwnerCommandInvalid
	}

	if aggregate.CommandSequence == ^uint64(0) {
		return ownercredential.Receipt{}, false, ErrOwnerCredentialRevision
	}
	commandSequence := aggregate.CommandSequence + 1
	receipt := ownercredential.Receipt{
		MessageID: challenge.MessageID, CommandKind: challenge.Kind, CommandDigest: challenge.Digest,
		CredentialID: signer.ID, Nonce: challenge.Nonce,
		ExpectedCredentialRevision: challenge.ExpectedCredentialRevision,
		CredentialRevision:         aggregate.CredentialRevision,
		CommandSequence:            commandSequence, EventSequence: 0, SignCount: signCount, RecordedAt: now,
	}
	aggregate.CommandSequence = commandSequence
	aggregate.Receipts[receipt.MessageID] = receipt
	if err := aggregate.Validate(); err != nil {
		return ownercredential.Receipt{}, false, err
	}
	next.OwnerCredentials[challenge.PrincipalID] = aggregate
	next.SchemaVersion = CanonicalSchemaVersion
	if s.state.Revision == ^uint64(0) {
		return ownercredential.Receipt{}, false, fmt.Errorf("canonical revision exhausted")
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

func principalFromPayload(p ownercredential.DelegationPrincipalPayload) DelegationPrincipal {
	return DelegationPrincipal{StableAgentID: p.StableAgentID, SessionID: p.SessionID, Generation: p.Generation}
}

func applyDelegationGrant(state *CanonicalState, challenge ownercredential.Command, now time.Time) error {
	var payload ownercredential.DelegationGrantPayload
	if err := decodeOwnerPayload(challenge.Payload, &payload); err != nil {
		return err
	}
	if strings.TrimSpace(payload.DelegationID) == "" || payload.DelegationID != strings.TrimSpace(payload.DelegationID) {
		return fmt.Errorf("%w: delegationId", ErrCoordinatorDelegationInvalid)
	}
	if _, exists := state.CoordinatorDelegations[payload.DelegationID]; exists {
		return fmt.Errorf("%w: delegationId already exists", ErrCoordinatorDelegationInvalid)
	}
	successors := make([]DelegationPrincipal, 0, len(payload.Successors))
	for _, s := range payload.Successors {
		successors = append(successors, principalFromPayload(s))
	}
	rec := DelegationRecord{
		DelegationID:         payload.DelegationID,
		Scope:                DelegationScope{Repository: payload.Scope.Repository, Project: payload.Scope.Project, AllowedWorkItems: append([]string{}, payload.Scope.AllowedWorkItems...), AllowedActions: append([]string{}, payload.Scope.AllowedActions...)},
		PrimaryCoordinator:   principalFromPayload(payload.PrimaryCoordinator),
		Validity:             DelegationValidity{NotBefore: payload.Validity.NotBefore.UTC(), ExpiresAt: payload.Validity.ExpiresAt.UTC()},
		Successors:           successors,
		Evidence:             DelegationEvidence{OwnerDecisionRef: payload.Evidence.OwnerDecisionRef, VisibilityEvidence: payload.Evidence.VisibilityEvidence},
		Generation:           1,
		CutoverAssignmentIDs: append([]string{}, payload.CutoverAssignmentIDs...),
		History: []DelegationHistoryEntry{{
			Kind: DelegationHistoryGrant, At: now, Actor: challenge.PrincipalID,
			ReceiptID: challenge.MessageID, ReceiptDig: challenge.Digest,
		}},
	}
	acting := rec.PrimaryCoordinator
	rec.ActingCoordinator = &acting
	if payload.PrimaryRestart != nil {
		rec.PrimaryRestart = &DelegationPrimaryRestart{StableAgentID: payload.PrimaryRestart.StableAgentID}
	}
	if err := rec.Validate(); err != nil {
		return err
	}
	// One active grant per repository+project.
	for id, existing := range state.CoordinatorDelegations {
		if existing.RevokedAt.IsZero() && existing.Scope.Repository == rec.Scope.Repository && existing.Scope.Project == rec.Scope.Project {
			return fmt.Errorf("%w: overlapping active grant %q", ErrCoordinatorDelegationInvalid, id)
		}
	}
	// cutoverAssignmentIds are storable only through this OWNER grant; inert
	// while production const is false (harness may store them for later tests).
	_ = CoordinatorDelegationEnabled
	state.CoordinatorDelegations[rec.DelegationID] = rec
	return nil
}

func applyDelegationRevoke(state *CanonicalState, challenge ownercredential.Command, now time.Time) error {
	var payload ownercredential.DelegationRevokePayload
	if err := decodeOwnerPayload(challenge.Payload, &payload); err != nil {
		return err
	}
	if payload.DelegationID == "" || strings.TrimSpace(payload.Reason) == "" {
		return fmt.Errorf("%w: revoke requires delegationId and reason", ErrCoordinatorDelegationInvalid)
	}
	rec, ok := state.CoordinatorDelegations[payload.DelegationID]
	if !ok {
		return fmt.Errorf("%w: unknown delegation", ErrCoordinatorDelegationInvalid)
	}
	if !rec.RevokedAt.IsZero() {
		return fmt.Errorf("%w: already revoked", ErrCoordinatorDelegationInvalid)
	}
	rec.RevokedAt = now
	rec.RevokedBy = challenge.PrincipalID
	rec.History = append(rec.History, DelegationHistoryEntry{
		Kind: DelegationHistoryRevoke, At: now, Actor: challenge.PrincipalID, Reason: payload.Reason,
		ReceiptID: challenge.MessageID, ReceiptDig: challenge.Digest,
	})
	rec.ActingCoordinator = nil
	if err := rec.Validate(); err != nil {
		return err
	}
	state.CoordinatorDelegations[payload.DelegationID] = rec
	return nil
}

func applyLossConfirm(state *CanonicalState, challenge ownercredential.Command, now time.Time) error {
	var payload ownercredential.LossConfirmPayload
	if err := decodeOwnerPayload(challenge.Payload, &payload); err != nil {
		// Unknown fields (scope/list/expiry/successor pick/role edits) fail closed.
		return fmt.Errorf("%w: %v", ErrCoordinatorDelegationInvalid, err)
	}
	if payload.DelegationID == "" || strings.TrimSpace(payload.Reason) == "" || payload.ObservedAt.IsZero() {
		return fmt.Errorf("%w: loss.confirm requires delegationId, reason, observedAt", ErrCoordinatorDelegationInvalid)
	}
	if (payload.Repository != "" || payload.Project != "") && (payload.Repository == "" || payload.Project == "") {
		return fmt.Errorf("%w: loss.confirm repository and project must both be set", ErrCoordinatorDelegationInvalid)
	}
	rec, ok := state.CoordinatorDelegations[payload.DelegationID]
	if !ok || !rec.Active(now) {
		return fmt.Errorf("%w: delegation missing, expired or revoked", ErrCoordinatorDelegationInvalid)
	}
	named := principalFromPayload(payload.Coordinator)
	if !named.Valid() {
		return fmt.Errorf("%w: invalid coordinator triple", ErrCoordinatorDelegationInvalid)
	}
	if payload.GrantID != "" && payload.GrantID != rec.DelegationID {
		return fmt.Errorf("%w: command grant id differs from delegation", ErrCoordinatorDelegationInvalid)
	}
	if payload.GrantGeneration != 0 && payload.GrantGeneration != rec.Generation {
		return fmt.Errorf("%w: command grant generation differs from delegation", ErrCoordinatorDelegationInvalid)
	}
	if payload.Repository != "" || payload.Project != "" {
		if payload.Repository != rec.Scope.Repository || payload.Project != rec.Scope.Project {
			return fmt.Errorf("%w: command repository/project differs from delegation scope", ErrCoordinatorDelegationInvalid)
		}
	}

	// mustNot: duplicate open/closed loss for this triple (ADR §9).
	if rec.lossAlreadyRecorded(named) || rec.IsBarred(named) {
		return fmt.Errorf("%w: duplicate open or closed loss", ErrCoordinatorDelegationInvalid)
	}

	// Snapshot scope/list/expiry before any write — loss.confirm must not change them.
	scopeBefore := rec.Scope
	succBefore := append([]DelegationPrincipal{}, rec.Successors...)
	expBefore := rec.Validity.ExpiresAt
	nbBefore := rec.Validity.NotBefore

	// mustNot: no open delegated work for this exact triple inside this grant's
	// repository/project + AllowedWorkItems scope (stale gen → no match).
	if state.AssignmentLedger == nil || state.AssignmentLedger.Records == nil {
		return fmt.Errorf("%w: no open delegated work", ErrCoordinatorDelegationInvalid)
	}
	// Select only assignments bound to this exact grant. A binding that names
	// this grant but disagrees on generation or repository/project poisons the
	// batch. Other grants and unbound records are out of scope and untouched.
	type lossHit struct {
		id  string
		rec assignment.Record
	}
	var hits []lossHit
	for id, r := range state.AssignmentLedger.Records {
		if r.State.Terminal() {
			continue
		}
		b := r.DelegationBinding
		if b == nil || b.GrantID != rec.DelegationID {
			continue
		}
		if b.GrantGeneration != rec.Generation || b.Repository != rec.Scope.Repository || b.Project != rec.Scope.Project || b.AllowedWorkItem != assignmentWorkItem(r) {
			return fmt.Errorf("%w: assignment %s grant binding invalid for %s", ErrCoordinatorDelegationInvalid, id, rec.DelegationID)
		}
		coord := r.Coordinator()
		sameTriple := coord.AgentID == named.StableAgentID && coord.SessionID == named.SessionID && coord.Generation == named.Generation
		if !sameTriple || !rec.Scope.AllowsWorkItem(b.AllowedWorkItem) {
			continue
		}
		if r.CoordinatorLost {
			return fmt.Errorf("%w: duplicate/closed loss for %s", ErrCoordinatorDelegationInvalid, id)
		}
		hits = append(hits, lossHit{id: id, rec: r})
	}
	matched := len(hits)
	if matched == 0 {
		// Distinguish stale generation (same agent/session, different gen on open work)
		// from no open work at all / out-of-scope work items only.
		for _, r := range state.AssignmentLedger.Records {
			if r.State.Terminal() || r.CoordinatorLost || r.DelegationBinding == nil || r.DelegationBinding.GrantID != rec.DelegationID || !rec.Scope.AllowsWorkItem(r.DelegationBinding.AllowedWorkItem) {
				continue
			}
			coord := r.Coordinator()
			if coord.AgentID == named.StableAgentID && coord.SessionID == named.SessionID && coord.Generation != named.Generation {
				return fmt.Errorf("%w: stale coordinator generation", ErrCoordinatorDelegationInvalid)
			}
		}
		return fmt.Errorf("%w: no open delegated work", ErrCoordinatorDelegationInvalid)
	}

	// Record COORDINATOR_LOST with OWNER_CREDENTIAL actor source on each matching
	// in-scope assignment. Out-of-scope or unrelated coordinator assignments are not touched.
	ownerActor := assignment.Principal{
		SessionID:  "ownercredential:" + challenge.PrincipalID,
		AgentID:    challenge.PrincipalID,
		Generation: 1,
	}
	for _, hit := range hits {
		id, r := hit.id, hit.rec
		nextRec := r
		nextRec.Revision++
		nextRec.UpdatedAt = now
		nextRec.CoordinatorLost = true
		ev := assignment.Event{
			AssignmentID: id, Revision: nextRec.Revision, Kind: "COORDINATOR_LOST",
			Actor: ownerActor, EvidenceRef: challenge.MessageID, At: now, Record: nextRec,
			ActorSource:           assignment.ActorSourceOwnerCredential,
			OwnerPrincipalID:      challenge.PrincipalID,
			OwnerCredentialID:     challenge.CredentialID,
			OwnerReceiptMessageID: challenge.MessageID,
			OwnerReceiptDigest:    challenge.Digest,
		}
		if err := state.AssignmentLedger.Append(ev); err != nil {
			return fmt.Errorf("%w: %v", ErrCoordinatorDelegationInvalid, err)
		}
	}

	// Bar the named triple until handoff (still-registered coordinator OK).
	if !rec.IsBarred(named) {
		rec.BarredCoordinators = append(rec.BarredCoordinators, named)
	}
	// lossRecordID = challenge.MessageID; AssignmentID records one matched
	// in-scope assignment for the F5 primaryRestart loss-identity bind.
	lossAssignmentID := ""
	for id, r := range state.AssignmentLedger.Records {
		if !r.CoordinatorLost {
			continue
		}
		coord := r.Coordinator()
		if coord.AgentID == named.StableAgentID && coord.SessionID == named.SessionID && coord.Generation == named.Generation && r.DelegationBinding != nil && r.DelegationBinding.GrantID == rec.DelegationID {
			lossAssignmentID = id
			break
		}
	}
	rec.History = append(rec.History, DelegationHistoryEntry{
		Kind: DelegationHistoryLossRecorded, At: now, Actor: challenge.PrincipalID, Reason: payload.Reason,
		AssignmentID: lossAssignmentID, From: &named, ReceiptID: challenge.MessageID, ReceiptDig: challenge.Digest,
	})
	if acting := rec.CurrentActing(); acting.Equal(named) {
		rec.ActingCoordinator = nil
	}
	// Invariant: loss.confirm never mutates scope, successor list, or validity.
	if !bytesEqualScope(rec.Scope, scopeBefore) || len(rec.Successors) != len(succBefore) ||
		!rec.Validity.ExpiresAt.Equal(expBefore) || !rec.Validity.NotBefore.Equal(nbBefore) {
		return fmt.Errorf("%w: loss.confirm must not change scope/list/expiry", ErrCoordinatorDelegationInvalid)
	}
	for i := range succBefore {
		if !rec.Successors[i].Equal(succBefore[i]) {
			return fmt.Errorf("%w: loss.confirm must not change successor list", ErrCoordinatorDelegationInvalid)
		}
	}
	state.CoordinatorDelegations[payload.DelegationID] = rec
	return nil
}

func bytesEqualScope(a, b DelegationScope) bool {
	if a.Repository != b.Repository || a.Project != b.Project ||
		len(a.AllowedWorkItems) != len(b.AllowedWorkItems) || len(a.AllowedActions) != len(b.AllowedActions) {
		return false
	}
	for i := range a.AllowedWorkItems {
		if a.AllowedWorkItems[i] != b.AllowedWorkItems[i] {
			return false
		}
	}
	for i := range a.AllowedActions {
		if a.AllowedActions[i] != b.AllowedActions[i] {
			return false
		}
	}
	return true
}

func applyVisibilityConfirm(state *CanonicalState, challenge ownercredential.Command, now time.Time) error {
	var payload ownercredential.VisibilityConfirmPayload
	if err := decodeOwnerPayload(challenge.Payload, &payload); err != nil {
		return err
	}
	if payload.DelegationID == "" || !payload.SingleUse || payload.IssuedAt.IsZero() || payload.ExpiresAt.IsZero() || !payload.ExpiresAt.After(payload.IssuedAt) {
		return fmt.Errorf("%w: visibility.confirm payload invalid", ErrCoordinatorDelegationInvalid)
	}
	rec, ok := state.CoordinatorDelegations[payload.DelegationID]
	if !ok || !rec.Active(now) {
		return fmt.Errorf("%w: delegation missing, expired or revoked", ErrCoordinatorDelegationInvalid)
	}
	conf := VisibilityConfirmation{
		MessageID: challenge.MessageID, Digest: challenge.Digest, DelegationID: payload.DelegationID,
		IssuedAt: payload.IssuedAt.UTC(), ExpiresAt: payload.ExpiresAt.UTC(),
		StableAgentID: payload.StableAgentID,
	}
	if payload.Successor != nil {
		conf.Successor = principalFromPayload(*payload.Successor)
		if !conf.Successor.Valid() {
			return fmt.Errorf("%w: invalid successor triple", ErrCoordinatorDelegationInvalid)
		}
		// Must be on the list (exact triple) OR a primaryRestart fresh triple
		// for the primary stableAgentId (new session/generation).
		onList := false
		for _, s := range rec.Successors {
			if s.Equal(conf.Successor) {
				onList = true
				break
			}
		}
		primaryRestartOK := rec.PrimaryRestart != nil &&
			conf.Successor.StableAgentID == rec.PrimaryRestart.StableAgentID &&
			(conf.Successor.SessionID != rec.PrimaryCoordinator.SessionID || conf.Successor.Generation != rec.PrimaryCoordinator.Generation)
		if payload.StableAgentID != "" {
			conf.StableAgentID = payload.StableAgentID
			if rec.PrimaryRestart == nil || payload.StableAgentID != rec.PrimaryRestart.StableAgentID {
				return fmt.Errorf("%w: primaryRestart stableAgentId mismatch", ErrCoordinatorDelegationInvalid)
			}
			primaryRestartOK = true
		}
		if !onList && !primaryRestartOK {
			return fmt.Errorf("%w: successor not on list", ErrCoordinatorDelegationInvalid)
		}
	} else if payload.StableAgentID != "" {
		if rec.PrimaryRestart == nil || rec.PrimaryRestart.StableAgentID != payload.StableAgentID {
			return fmt.Errorf("%w: primaryRestart stableAgentId mismatch", ErrCoordinatorDelegationInvalid)
		}
		conf.StableAgentID = payload.StableAgentID
	} else {
		return fmt.Errorf("%w: successor or primaryRestart required", ErrCoordinatorDelegationInvalid)
	}
	rec.VisibilityReceipts = append(rec.VisibilityReceipts, conf)
	rec.History = append(rec.History, DelegationHistoryEntry{
		Kind: DelegationHistoryVisibility, At: now, Actor: challenge.PrincipalID,
		ReceiptID: challenge.MessageID, ReceiptDig: challenge.Digest,
	})
	state.CoordinatorDelegations[payload.DelegationID] = rec
	return nil
}
