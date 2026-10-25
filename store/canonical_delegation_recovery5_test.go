package store

import (
	"bytes"
	"coord/assignment"
	"coord/ownercredential"
	"errors"
	"strings"
	"testing"
	"time"
)

func recovery5AddOtherCoordinatorAssignment(t *testing.T, f *ownerCommandFixture, grantID string, now time.Time) assignment.Principal {
	t.Helper()
	other := assignment.Principal{SessionID: "deepseek:d", AgentID: "agent-d", Generation: 1}
	key := make([]byte, 32)
	for i := range key {
		key[i] = 'd'
	}
	if err := f.s.RegisterAgentSession(other.SessionID, other.AgentID, key); err != nil {
		t.Fatal(err)
	}
	grant := f.s.state.CoordinatorDelegations[grantID]
	binding := &assignment.DelegationBinding{GrantID: grant.DelegationID, GrantGeneration: grant.Generation, Repository: grant.Scope.Repository, Project: grant.Scope.Project, AllowedWorkItem: "issue-93b"}
	if err := f.s.UpdateCanonical(func(state *CanonicalState) error {
		target := assignment.Principal{SessionID: "codex:worker", AgentID: "worker", Generation: 1}
		offered := assignment.Record{
			AssignmentID: "asg-r5-d", State: assignment.Offered,
			Issuer: other.SessionID, IssuerAgentID: other.AgentID, IssuerGeneration: other.Generation,
			CurrentCoordinator: other.SessionID, CoordinatorAgentID: other.AgentID, CoordinatorGeneration: other.Generation,
			HandoffEvidence: []assignment.Handoff{}, Assignee: target.SessionID, StableAgentID: target.AgentID,
			SessionID: target.SessionID, AssigneeGeneration: target.Generation, Role: "IMPLEMENTER",
			SubjectSHA: strings.Repeat("b", 40), IssuedAt: now.Add(-time.Minute), ValidUntil: now.Add(time.Hour),
			AdmissionBasis:     assignment.AdmissionBasis{Kind: "POOL", Reference: "issue-93b", TargetSessionID: target.SessionID, TargetAgentID: target.AgentID, Role: "IMPLEMENTER", SubjectSHA: strings.Repeat("b", 40)},
			DelegationBinding:  binding,
			VisibilityEvidence: assignment.VisibilityEvidence{Reference: "vis-r5", SessionID: target.SessionID, ObservedAt: now.Add(-time.Minute)},
			Revision:           1, UpdatedAt: now.Add(-time.Minute),
		}
		if err := state.AssignmentLedger.Append(assignment.Event{AssignmentID: offered.AssignmentID, Revision: 1, Kind: "OFFER", Actor: other, EvidenceRef: "seed-r5-offer", At: offered.UpdatedAt, Record: offered}); err != nil {
			return err
		}
		acked := offered
		acked.State, acked.Revision, acked.AcknowledgedAt, acked.UpdatedAt = assignment.Acked, 2, now.Add(-50*time.Second), now.Add(-50*time.Second)
		if err := state.AssignmentLedger.Append(assignment.Event{AssignmentID: acked.AssignmentID, Revision: 2, Kind: "ACK", Actor: target, EvidenceRef: "seed-r5-ack", At: acked.UpdatedAt, Record: acked}); err != nil {
			return err
		}
		active := acked
		active.State, active.Revision, active.UpdatedAt = assignment.Active, 3, now.Add(-40*time.Second)
		if err := state.AssignmentLedger.Append(assignment.Event{AssignmentID: active.AssignmentID, Revision: 3, Kind: "ACTIVATE", Actor: other, EvidenceRef: "seed-r5-active", At: active.UpdatedAt, Record: active}); err != nil {
			return err
		}
		return nil
	}); err != nil {
		t.Fatalf("seed second coordinator's grant-bound assignment: %v", err)
	}
	return other
}

func forgeRecoverySiblingWithoutReconsuming(t *testing.T, state *CanonicalState, grantID, assignmentID string, from, to assignment.Principal, visibilityID string, now time.Time) {
	t.Helper()
	prior := state.AssignmentLedger.Records[assignmentID]
	if !now.After(prior.UpdatedAt) {
		now = prior.UpdatedAt.Add(time.Nanosecond)
	}
	next := prior
	next.CurrentCoordinator, next.CoordinatorAgentID, next.CoordinatorGeneration = to.SessionID, to.AgentID, to.Generation
	next.CoordinatorLost = false
	next.HandoffEvidence = append(append([]assignment.Handoff(nil), prior.HandoffEvidence...), assignment.Handoff{From: from, To: to, EvidenceRef: "forged-r5-sibling", At: now})
	state.Revision++
	if err := appendAssignment(state, to, prior, next, "HANDOFF", "forged-r5-sibling", now); err != nil {
		t.Fatalf("append forged sibling assignment event: %v", err)
	}
	grant := state.CoordinatorDelegations[grantID]
	fromP, toP := principalFromAssignment(from), principalFromAssignment(to)
	grant.ActingCoordinator = &toP
	grant.History = append(grant.History, DelegationHistoryEntry{Kind: DelegationHistoryHandoff, At: now, AssignmentID: assignmentID, From: &fromP, To: &toP, ReceiptDig: visibilityID, CanonicalRevision: state.Revision})
	state.CoordinatorDelegations[grantID] = grant
}

func TestRecoveryG4_NewLossCaptureRequiresCurrentActingSuccessor(t *testing.T) {
	t.Run("live_rejects_other_successor_then_accepts_fresh_visibility", func(t *testing.T) {
		f, now, a, b, c := recovery4Setup(t, "del-r5-capture-live", true)
		d := recovery5AddOtherCoordinatorAssignment(t, f, "del-r5-capture-live", now)
		if err := recovery4Handoff(t, f, a, b, "asg-r4-x", "r5-first-capture-b"); err != nil {
			t.Fatalf("initial handoff to B: %v", err)
		}
		lossConfirm(t, f, "del-r5-capture-live", "loss-r5-d-live", ownercredential.DelegationPrincipalPayload{StableAgentID: d.AgentID, SessionID: d.SessionID, Generation: d.Generation}, now)
		state, err := copyCanonicalState(f.s.state)
		if err != nil {
			t.Fatal(err)
		}
		if err := enforceDelegatedHandoff(&state, d, c, "asg-r5-d", now); !errors.Is(err, ErrCoordinatorDelegationInvalid) || !strings.Contains(err.Error(), "new loss capture must use the acting successor") {
			t.Fatalf("live enforcement allowed a different successor to capture new loss: %v", err)
		}
		before := ownerStateBytes(t, f.s)
		if err := recovery4Handoff(t, f, d, c, "asg-r5-d", "wrong-successor-for-new-loss"); !errors.Is(err, ErrCoordinatorDelegationInvalid) || !strings.Contains(err.Error(), "new loss capture must use the acting successor") {
			t.Fatalf("new loss captured by a different successor: %v", err)
		}
		if !bytes.Equal(before, ownerStateBytes(t, f.s)) {
			t.Fatal("rejected new-loss capture changed canonical bytes")
		}
		confirmVisibility(t, f, "del-r5-capture-live", "vis-r5-b-fresh", ownercredential.DelegationPrincipalPayload{StableAgentID: b.AgentID, SessionID: b.SessionID, Generation: b.Generation}, now)
		if err := recovery4Handoff(t, f, d, b, "asg-r5-d", "new-loss-capture-by-acting-b"); err != nil {
			t.Fatalf("new loss capture by current acting successor with fresh visibility: %v", err)
		}
	})

	t.Run("reload_rejects_other_successor_for_new_loss", func(t *testing.T) {
		f, now, a, b, c := recovery4Setup(t, "del-r5-capture-reload", true)
		d := recovery5AddOtherCoordinatorAssignment(t, f, "del-r5-capture-reload", now)
		if err := recovery4Handoff(t, f, a, b, "asg-r4-x", "r5-reload-first-capture-b"); err != nil {
			t.Fatalf("initial handoff to B: %v", err)
		}
		lossConfirm(t, f, "del-r5-capture-reload", "loss-r5-d-reload", ownercredential.DelegationPrincipalPayload{StableAgentID: d.AgentID, SessionID: d.SessionID, Generation: d.Generation}, now)
		snapshot, err := copyCanonicalState(f.s.state)
		if err != nil {
			t.Fatal(err)
		}
		forgeRecoveryHandoff(t, &snapshot, "del-r5-capture-reload", "asg-r5-d", d, c, "vis-r4-c", now)
		assertRecoveryReloadRejectsWithText(t, snapshot, "new loss captured by a successor other than the current actor", "new loss capture must use the acting successor")
	})
}

func TestRecoveryG4_NewLossEndsNoVisibilitySiblingContext(t *testing.T) {
	t.Run("live_requires_fresh_capture_after_separate_loss", func(t *testing.T) {
		f, now, a, b, _ := recovery4Setup(t, "del-r5-context-live", false)
		d := recovery5AddOtherCoordinatorAssignment(t, f, "del-r5-context-live", now)
		if err := recovery4Handoff(t, f, a, b, "asg-r4-x", "r5-context-first-capture-b"); err != nil {
			t.Fatalf("initial handoff to B: %v", err)
		}
		lossConfirm(t, f, "del-r5-context-live", "loss-r5-context-d", ownercredential.DelegationPrincipalPayload{StableAgentID: d.AgentID, SessionID: d.SessionID, Generation: d.Generation}, now)
		state, err := copyCanonicalState(f.s.state)
		if err != nil {
			t.Fatal(err)
		}
		if err := enforceDelegatedHandoff(&state, a, b, "asg-r4-y", now); !errors.Is(err, ErrCoordinatorDelegationInvalid) || !strings.Contains(err.Error(), "another unbarred coordinator is acting") {
			t.Fatalf("live enforcement retained pre-loss sibling context: %v", err)
		}
		before := ownerStateBytes(t, f.s)
		err = recovery4Handoff(t, f, a, b, "asg-r4-y", "stale-sibling-context-after-new-loss")
		if !errors.Is(err, ErrCoordinatorDelegationInvalid) || !strings.Contains(err.Error(), "another unbarred coordinator is acting") {
			t.Fatalf("sibling reused old no-visibility context after distinct loss: %v", err)
		}
		if !bytes.Equal(before, ownerStateBytes(t, f.s)) {
			t.Fatal("rejected stale sibling context changed canonical bytes")
		}
	})

	t.Run("reload_rejects_sibling_using_context_before_new_loss", func(t *testing.T) {
		f, now, a, b, _ := recovery4Setup(t, "del-r5-context-reload", false)
		d := recovery5AddOtherCoordinatorAssignment(t, f, "del-r5-context-reload", now)
		if err := recovery4Handoff(t, f, a, b, "asg-r4-x", "r5-context-reload-first-capture-b"); err != nil {
			t.Fatalf("initial handoff to B: %v", err)
		}
		lossConfirm(t, f, "del-r5-context-reload", "loss-r5-context-reload-d", ownercredential.DelegationPrincipalPayload{StableAgentID: d.AgentID, SessionID: d.SessionID, Generation: d.Generation}, now)
		snapshot, err := copyCanonicalState(f.s.state)
		if err != nil {
			t.Fatal(err)
		}
		forgeRecoverySiblingWithoutReconsuming(t, &snapshot, "del-r5-context-reload", "asg-r4-y", a, b, "vis-r4-b", now)
		assertRecoveryReloadRejectsWithText(t, snapshot, "sibling reused before new loss context reset", "another unbarred coordinator is acting")
	})
}

func TestRecoveryG4_SiblingHandoffMustReuseSelectedVisibilityOnReload(t *testing.T) {
	t.Run("sibling_handoff_different_visibility_receipt", func(t *testing.T) {
		f, now, a, b, _ := recovery4Setup(t, "del-r5-visibility-binding-negative", true)
		if err := recovery4Handoff(t, f, a, b, "asg-r4-x", "r5-visibility-first-capture-negative"); err != nil {
			t.Fatalf("initial handoff to B: %v", err)
		}
		snapshot, err := copyCanonicalState(f.s.state)
		if err != nil {
			t.Fatal(err)
		}
		forgeRecoverySiblingWithoutReconsuming(t, &snapshot, "del-r5-visibility-binding-negative", "asg-r4-y", a, b, "vis-r4-c", now)
		assertRecoveryReloadRejectsWithText(t, snapshot, "sibling cites different unconsumed visibility", "sibling handoff must retain selected successor and visibility")
	})

	t.Run("sibling_handoff_same_visibility_receipt", func(t *testing.T) {
		f, now, a, b, _ := recovery4Setup(t, "del-r5-visibility-binding-positive", false)
		if err := recovery4Handoff(t, f, a, b, "asg-r4-x", "r5-visibility-first-capture-positive"); err != nil {
			t.Fatalf("initial handoff to B: %v", err)
		}
		snapshot, err := copyCanonicalState(f.s.state)
		if err != nil {
			t.Fatal(err)
		}
		forgeRecoverySiblingWithoutReconsuming(t, &snapshot, "del-r5-visibility-binding-positive", "asg-r4-y", a, b, "vis-r4-b", now)
		if err := ValidateCanonicalState(snapshot); err != nil {
			t.Fatalf("same selected visibility receipt sibling rejected: %v", err)
		}
		if _, err := NewCanonical(&canonicalMemory{state: snapshot}); err != nil {
			t.Fatalf("reload of sibling with same visibility receipt: %v", err)
		}
	})
}

func TestRecoveryG4_HandoffCommitSeamRequiresAlreadyActingSuccessorForSibling(t *testing.T) {
	f, now, a, b, c := recovery4Setup(t, "del-r5-seam", true)
	if err := recovery4Handoff(t, f, a, b, "asg-r4-x", "r5-seam-first-capture"); err != nil {
		t.Fatalf("initial handoff to B: %v", err)
	}
	before := f.s.state.CoordinatorDelegations["del-r5-seam"]
	makeAfter := func(to assignment.Principal) (CanonicalState, DelegationRecord) {
		t.Helper()
		next, err := copyCanonicalState(f.s.state)
		if err != nil {
			t.Fatal(err)
		}
		record := next.AssignmentLedger.Records["asg-r4-y"]
		record.CurrentCoordinator, record.CoordinatorAgentID, record.CoordinatorGeneration = to.SessionID, to.AgentID, to.Generation
		record.CoordinatorLost = false
		next.AssignmentLedger.Records[record.AssignmentID] = record
		after := before
		toP, fromP := principalFromAssignment(to), principalFromAssignment(a)
		after.ActingCoordinator = &toP
		after.History = append(append([]DelegationHistoryEntry(nil), before.History...), DelegationHistoryEntry{
			Kind: DelegationHistoryHandoff, At: now, AssignmentID: record.AssignmentID,
			From: &fromP, To: &toP, ReceiptDig: "vis-r4-b", CanonicalRevision: next.Revision + 1,
		})
		return next, after
	}

	t.Run("rejects_sibling_delta_targeting_other_coordinator", func(t *testing.T) {
		next, after := makeAfter(c)
		if err := validateHandoffGrantDelta(next, before, after); err == nil || !strings.Contains(err.Error(), "visibility receipt required") {
			t.Fatalf("handoff seam accepted no-consumption delta to non-acting C: %v", err)
		}
	})
	t.Run("accepts_exact_already_acting_sibling", func(t *testing.T) {
		next, after := makeAfter(b)
		if err := validateHandoffGrantDelta(next, before, after); err != nil {
			t.Fatalf("handoff seam rejected exact same-successor sibling control: %v", err)
		}
	})
}

func TestRecoveryG4_HandoffRequiresLossHistoryToCoverAssignment(t *testing.T) {
	t.Run("rejects_receipted_loss_missing_from_delegation_history", func(t *testing.T) {
		enableDelegationHarness(t)
		f := newOwnerCommandFixture(t)
		now := f.s.now().UTC()
		grantDelegation(t, f, "del-r5-coverage", now)
		a := assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}
		b := assignment.Principal{SessionID: "codex:b", AgentID: "agent-b", Generation: 1}
		seedDelegatedAssignment(t, f, "asg-r5-coverage", a, now)
		registerSession(t, f, b.SessionID, b.AgentID)
		confirmVisibility(t, f, "del-r5-coverage", "vis-r5-coverage", ownercredential.DelegationPrincipalPayload{StableAgentID: b.AgentID, SessionID: b.SessionID, Generation: b.Generation}, now)
		lossConfirm(t, f, "del-r5-coverage", "loss-r5-coverage", ownercredential.DelegationPrincipalPayload{StableAgentID: a.AgentID, SessionID: a.SessionID, Generation: a.Generation}, now)
		state, err := copyCanonicalState(f.s.state)
		if err != nil {
			t.Fatal(err)
		}
		grant := state.CoordinatorDelegations["del-r5-coverage"]
		for i := range grant.History {
			if grant.History[i].Kind == DelegationHistoryLossRecorded {
				grant.History[i].AssignmentIDs = nil
				break
			}
		}
		state.CoordinatorDelegations[grant.DelegationID] = grant
		if err := enforceDelegatedHandoff(&state, a, b, "asg-r5-coverage", now); err == nil || !strings.Contains(err.Error(), "OWNER loss history does not cover assignment") {
			t.Fatalf("Owner receipt without matching delegation loss history err=%v", err)
		}

		f2, _, a2, b2, _ := recovery4Setup(t, "del-r5-coverage-positive", false)
		if err := recovery4Handoff(t, f2, a2, b2, "asg-r4-x", "covered-loss-positive-control"); err != nil {
			t.Fatalf("exact receipted and recorded loss positive control: %v", err)
		}
	})
}
