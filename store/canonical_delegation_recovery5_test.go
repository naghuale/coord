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
	other := assignment.Principal{SessionID: "deepseek:d", AgentID: "agent-d", Generation: 1}
	recovery5AddGrantBoundAssignment(t, f, grantID, "asg-r5-d", other, now)
	return other
}

func recovery5AddGrantBoundAssignment(t *testing.T, f *ownerCommandFixture, grantID, assignmentID string, actor assignment.Principal, now time.Time) {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte('a' + i%20)
	}
	if _, exists := f.s.state.AgentSessions[actor.SessionID]; !exists {
		if err := f.s.RegisterAgentSession(actor.SessionID, actor.AgentID, key); err != nil {
			t.Fatal(err)
		}
	}
	grant := f.s.state.CoordinatorDelegations[grantID]
	binding := &assignment.DelegationBinding{GrantID: grant.DelegationID, GrantGeneration: grant.Generation, Repository: grant.Scope.Repository, Project: grant.Scope.Project, AllowedWorkItem: "issue-93b"}
	if err := f.s.UpdateCanonical(func(state *CanonicalState) error {
		target := assignment.Principal{SessionID: "codex:worker", AgentID: "worker", Generation: 1}
		offered := assignment.Record{
			AssignmentID: assignmentID, State: assignment.Offered,
			Issuer: actor.SessionID, IssuerAgentID: actor.AgentID, IssuerGeneration: actor.Generation,
			CurrentCoordinator: actor.SessionID, CoordinatorAgentID: actor.AgentID, CoordinatorGeneration: actor.Generation,
			HandoffEvidence: []assignment.Handoff{}, Assignee: target.SessionID, StableAgentID: target.AgentID,
			SessionID: target.SessionID, AssigneeGeneration: target.Generation, Role: "IMPLEMENTER",
			SubjectSHA: strings.Repeat("b", 40), IssuedAt: now.Add(-time.Minute), ValidUntil: now.Add(time.Hour),
			AdmissionBasis:     assignment.AdmissionBasis{Kind: "POOL", Reference: "issue-93b", TargetSessionID: target.SessionID, TargetAgentID: target.AgentID, Role: "IMPLEMENTER", SubjectSHA: strings.Repeat("b", 40)},
			DelegationBinding:  binding,
			VisibilityEvidence: assignment.VisibilityEvidence{Reference: "vis-r5", SessionID: target.SessionID, ObservedAt: now.Add(-time.Minute)},
			Revision:           1, UpdatedAt: now.Add(-time.Minute),
		}
		if err := state.AssignmentLedger.Append(assignment.Event{AssignmentID: offered.AssignmentID, Revision: 1, Kind: "OFFER", Actor: actor, EvidenceRef: "seed-r5-offer", At: offered.UpdatedAt, Record: offered}); err != nil {
			return err
		}
		acked := offered
		acked.State, acked.Revision, acked.AcknowledgedAt, acked.UpdatedAt = assignment.Acked, 2, now.Add(-50*time.Second), now.Add(-50*time.Second)
		if err := state.AssignmentLedger.Append(assignment.Event{AssignmentID: acked.AssignmentID, Revision: 2, Kind: "ACK", Actor: target, EvidenceRef: "seed-r5-ack", At: acked.UpdatedAt, Record: acked}); err != nil {
			return err
		}
		active := acked
		active.State, active.Revision, active.UpdatedAt = assignment.Active, 3, now.Add(-40*time.Second)
		if err := state.AssignmentLedger.Append(assignment.Event{AssignmentID: active.AssignmentID, Revision: 3, Kind: "ACTIVATE", Actor: actor, EvidenceRef: "seed-r5-active", At: active.UpdatedAt, Record: active}); err != nil {
			return err
		}
		return nil
	}); err != nil {
		t.Fatalf("seed grant-bound assignment: %v", err)
	}
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

func forgeRecoveryHandoffWithoutVisibility(t *testing.T, state *CanonicalState, grantID, assignmentID string, from, to assignment.Principal, visibilityID string, now time.Time) {
	t.Helper()
	prior, ok := state.AssignmentLedger.Records[assignmentID]
	if !ok {
		t.Fatalf("forge no-visibility handoff: assignment %s missing", assignmentID)
	}
	if !now.After(prior.UpdatedAt) {
		now = prior.UpdatedAt.Add(time.Nanosecond)
	}
	next := prior
	next.CurrentCoordinator, next.CoordinatorAgentID, next.CoordinatorGeneration = to.SessionID, to.AgentID, to.Generation
	next.CoordinatorLost = false
	next.HandoffEvidence = append(append([]assignment.Handoff(nil), prior.HandoffEvidence...), assignment.Handoff{From: from, To: to, EvidenceRef: "forged-r5-no-visibility-handoff", At: now})
	state.Revision++
	if err := appendAssignment(state, from, prior, next, "HANDOFF", "forged-r5-no-visibility-handoff", now); err != nil {
		t.Fatalf("append forged no-visibility assignment event: %v", err)
	}
	grant := state.CoordinatorDelegations[grantID]
	fromP, toP := principalFromAssignment(from), principalFromAssignment(to)
	grant.ActingCoordinator = &toP
	grant.History = append(grant.History, DelegationHistoryEntry{Kind: DelegationHistoryHandoff, At: now, AssignmentID: assignmentID, From: &fromP, To: &toP, ReceiptDig: visibilityID, CanonicalRevision: state.Revision})
	state.CoordinatorDelegations[grantID] = grant
}

func recovery5FreshCaptureAfterSeparateLoss(t *testing.T, grantID string) (*ownerCommandFixture, time.Time, assignment.Principal, assignment.Principal, assignment.Principal) {
	t.Helper()
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	a, b, c := recovery4Principals()
	grantDelegationWith(t, f, grantID, now, delegationOpts{successors: []ownercredential.DelegationPrincipalPayload{
		{StableAgentID: c.AgentID, SessionID: c.SessionID, Generation: c.Generation},
		{StableAgentID: b.AgentID, SessionID: b.SessionID, Generation: b.Generation},
	}})
	seedDelegatedAssignment(t, f, "asg-r5-fr1-x", a, now)
	seedDelegatedAssignment(t, f, "asg-r5-fr1-y", a, now)
	recovery5AddGrantBoundAssignment(t, f, grantID, "asg-r5-fr1-b", b, now)
	registerSession(t, f, c.SessionID, c.AgentID)
	confirmVisibility(t, f, grantID, "vis-r5-fr1-c", ownercredential.DelegationPrincipalPayload{StableAgentID: c.AgentID, SessionID: c.SessionID, Generation: c.Generation}, now)
	lossConfirm(t, f, grantID, "loss-r5-fr1-a", ownercredential.DelegationPrincipalPayload{StableAgentID: a.AgentID, SessionID: a.SessionID, Generation: a.Generation}, now)
	if err := recovery4Handoff(t, f, a, c, "asg-r5-fr1-x", "fr1-first-capture"); err != nil {
		t.Fatalf("initial A to C capture: %v", err)
	}
	lossConfirm(t, f, grantID, "loss-r5-fr1-b", ownercredential.DelegationPrincipalPayload{StableAgentID: b.AgentID, SessionID: b.SessionID, Generation: b.Generation}, now)
	return f, now, a, b, c
}

func TestRecovery5_FreshCaptureByAlreadySelectedActingSuccessorAfterSeparateLoss(t *testing.T) {
	t.Run("live_and_reload_require_fresh_visibility_and_accept_positive_control", func(t *testing.T) {
		f, now, a, _, c := recovery5FreshCaptureAfterSeparateLoss(t, "del-r5-fr1-live")
		before := ownerStateBytes(t, f.s)
		if err := recovery4Handoff(t, f, a, c, "asg-r5-fr1-y", "fr1-without-fresh-visibility"); err == nil {
			t.Fatal("capture by already selected actor without fresh visibility succeeded")
		}
		if !bytes.Equal(before, ownerStateBytes(t, f.s)) {
			t.Fatal("rejected no-visibility capture changed canonical state")
		}
		stale, err := copyCanonicalState(f.s.state)
		if err != nil {
			t.Fatal(err)
		}
		forgeRecoverySiblingWithoutReconsuming(t, &stale, "del-r5-fr1-live", "asg-r5-fr1-y", a, c, "vis-r5-fr1-c", now)
		if err := ValidateCanonicalState(stale); !errors.Is(err, ErrCoordinatorDelegationInvalid) {
			t.Fatalf("reload accepted sibling after a separate loss without fresh visibility: %v", err)
		}
		if _, err := NewCanonical(&canonicalMemory{state: stale}); !errors.Is(err, ErrCoordinatorDelegationInvalid) {
			t.Fatalf("startup reload accepted sibling after separate loss without fresh visibility: %v", err)
		}

		confirmVisibility(t, f, "del-r5-fr1-live", "vis-r5-fr1-c-after-loss", ownercredential.DelegationPrincipalPayload{StableAgentID: c.AgentID, SessionID: c.SessionID, Generation: c.Generation}, now)
		if err := recovery4Handoff(t, f, a, c, "asg-r5-fr1-y", "fr1-fresh-capture"); err != nil {
			t.Fatalf("fresh capture by currently acting selected successor after separate loss: %v", err)
		}
		if err := ValidateCanonicalState(f.s.state); err != nil {
			t.Fatalf("reload validation rejected fresh-capture positive control: %v", err)
		}
		snapshot, err := copyCanonicalState(f.s.state)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := NewCanonical(&canonicalMemory{state: snapshot}); err != nil {
			t.Fatalf("startup reload rejected fresh-capture positive control: %v", err)
		}
	})
}

func recovery5RestartPrimary(t *testing.T, f *ownerCommandFixture, grantID string, now time.Time) (assignment.Principal, assignment.Principal, assignment.Principal, assignment.Principal) {
	t.Helper()
	a, b, c := recovery4Principals()
	a2 := assignment.Principal{SessionID: "claude:a2", AgentID: a.AgentID, Generation: 2}
	registerSession(t, f, a2.SessionID, a2.AgentID)
	bumpSessionGeneration(t, f, a2.SessionID, a2.Generation)
	confirmPrimaryRestartVisibility(t, f, grantID, "vis-r5-primary-restart", a.AgentID, now)
	if err := f.s.ApplyPrimaryRestart(grantID, a2, "r5-primary-restart"); err != nil {
		t.Fatalf("primaryRestart to fresh primary session: %v", err)
	}
	return a, a2, b, c
}

func TestRecovery5_RestartedPrimaryMayReceiveCoveredLostAssignmentsWithoutNewVisibility(t *testing.T) {
	f, now, _, _, _ := recovery4Setup(t, "del-r5-primary-route", false)
	a, a2, _, _ := recovery5RestartPrimary(t, f, "del-r5-primary-route", now)
	if err := recovery4Handoff(t, f, a, a2, "asg-r4-x", "route-lost-x-to-restarted-primary"); err != nil {
		t.Fatalf("route first covered lost assignment to restarted primary without new visibility: %v", err)
	}
	if err := recovery4Handoff(t, f, a, a2, "asg-r4-y", "route-lost-y-to-restarted-primary-no-new-visibility"); err != nil {
		t.Fatalf("route sibling covered lost assignment to restarted primary without new visibility: %v", err)
	}
	if err := ValidateCanonicalState(f.s.state); err != nil {
		t.Fatalf("valid restarted-primary routing state failed validation: %v", err)
	}
	snapshot, err := copyCanonicalState(f.s.state)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewCanonical(&canonicalMemory{state: snapshot}); err != nil {
		t.Fatalf("valid restarted-primary routing state failed reload: %v", err)
	}
}

func recovery5TwoRestartSnapshot(t *testing.T, grantID string) (CanonicalState, time.Time, DelegationPrincipal, DelegationPrincipal, DelegationPrincipal) {
	t.Helper()
	f, now, _, _, _ := recovery4Setup(t, grantID, false)
	a, a2, b, _ := recovery5RestartPrimary(t, f, grantID, now)
	seedDelegatedAssignment(t, f, "asg-r5-n3-between-restarts", a2, now)
	confirmVisibility(t, f, grantID, "vis-r5-n3-to-b", ownercredential.DelegationPrincipalPayload{StableAgentID: b.AgentID, SessionID: b.SessionID, Generation: b.Generation}, now)
	if err := recovery4Handoff(t, f, a2, b, "asg-r5-n3-between-restarts", "move-primary-to-successor-between-restarts"); err != nil {
		t.Fatalf("move acting primary to successor before second restart: %v", err)
	}
	lossConfirm(t, f, grantID, "loss-r5-n3-b", ownercredential.DelegationPrincipalPayload{StableAgentID: b.AgentID, SessionID: b.SessionID, Generation: b.Generation}, now)
	a3 := assignment.Principal{SessionID: "claude:a3", AgentID: a.AgentID, Generation: 3}
	registerSession(t, f, a3.SessionID, a3.AgentID)
	bumpSessionGeneration(t, f, a3.SessionID, a3.Generation)
	confirmPrimaryRestartVisibility(t, f, grantID, "vis-r5-n3-restart-2", a.AgentID, now)
	if err := f.s.ApplyPrimaryRestart(grantID, a3, "restart-2"); err != nil {
		t.Fatalf("second primaryRestart positive control: %v", err)
	}
	snapshot, err := copyCanonicalState(f.s.state)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot, now, principalFromAssignment(a), principalFromAssignment(a2), principalFromAssignment(a3)
}

func recovery5RestartAfterSuccessorLossSnapshot(t *testing.T, grantID string) (CanonicalState, assignment.Principal, assignment.Principal) {
	t.Helper()
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	a, b, _ := recovery4Principals()
	grantDelegationWith(t, f, grantID, now, delegationOpts{})
	seedDelegatedAssignment(t, f, "asg-r5-n3-primary-session", a, now)
	registerSession(t, f, b.SessionID, b.AgentID)
	confirmVisibility(t, f, grantID, "vis-r5-n3-primary-session-b", ownercredential.DelegationPrincipalPayload{StableAgentID: b.AgentID, SessionID: b.SessionID, Generation: b.Generation}, now)
	if err := recovery4Handoff(t, f, a, b, "asg-r5-n3-primary-session", "handoff-to-b-before-primary-restart"); err != nil {
		t.Fatalf("handoff to B before primary restart: %v", err)
	}
	lossConfirm(t, f, grantID, "loss-r5-n3-primary-session-b", ownercredential.DelegationPrincipalPayload{StableAgentID: b.AgentID, SessionID: b.SessionID, Generation: b.Generation}, now)
	a2 := assignment.Principal{SessionID: "claude:a2", AgentID: a.AgentID, Generation: 2}
	registerSession(t, f, a2.SessionID, a2.AgentID)
	bumpSessionGeneration(t, f, a2.SessionID, a2.Generation)
	confirmPrimaryRestartVisibility(t, f, grantID, "vis-r5-n3-primary-session-restart", a.AgentID, now)
	if err := f.s.ApplyPrimaryRestart(grantID, a2, "restart-primary-after-b-loss"); err != nil {
		t.Fatalf("primaryRestart after successor loss: %v", err)
	}
	snapshot, err := copyCanonicalState(f.s.state)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot, a, b
}

func TestRecovery5_ReloadPrimaryRestartMatchesLiveChecks(t *testing.T) {
	t.Run("primary_session_reuse_rejected", func(t *testing.T) {
		snapshot, a, _ := recovery5RestartAfterSuccessorLossSnapshot(t, "del-r5-n3-primary-session")
		grant := snapshot.CoordinatorDelegations["del-r5-n3-primary-session"]
		for i := range grant.History {
			if grant.History[i].Kind == DelegationHistoryPrimaryRestart {
				grant.History[i].To.SessionID = a.SessionID
				grant.ActingCoordinator = grant.History[i].To
			}
		}
		snapshot.CoordinatorDelegations[grant.DelegationID] = grant
		if err := ValidateCanonicalState(snapshot); !errors.Is(err, ErrCoordinatorDelegationInvalid) || !strings.Contains(err.Error(), "PRIMARY_RESTART reuses primary session") {
			t.Fatalf("reload accepted PRIMARY_RESTART to original primary session: %v", err)
		}
	})
	t.Run("fresh_unregistered_restart_session_is_valid_on_reload", func(t *testing.T) {
		snapshot, a, _ := recovery5RestartAfterSuccessorLossSnapshot(t, "del-r5-n3-unregistered-reload")
		grant := snapshot.CoordinatorDelegations["del-r5-n3-unregistered-reload"]
		fresh := DelegationPrincipal{StableAgentID: a.AgentID, SessionID: "claude:unregistered-reload", Generation: 2}
		for i := range grant.History {
			if grant.History[i].Kind == DelegationHistoryPrimaryRestart {
				grant.History[i].To = &fresh
				grant.ActingCoordinator = &fresh
			}
		}
		snapshot.CoordinatorDelegations[grant.DelegationID] = grant
		if _, exists := snapshot.AgentSessions[fresh.SessionID]; exists {
			t.Fatal("positive control unexpectedly has a registered restart session")
		}
		if err := ValidateCanonicalState(snapshot); err != nil {
			t.Fatalf("reload required a registration check that is live-only: %v", err)
		}
	})
	t.Run("grant_without_primary_restart_record_rejected", func(t *testing.T) {
		f, now, _, _, _ := recovery4Setup(t, "del-r5-n3-no-record", false)
		_, _, _, _ = recovery5RestartPrimary(t, f, "del-r5-n3-no-record", now)
		snapshot, err := copyCanonicalState(f.s.state)
		if err != nil {
			t.Fatal(err)
		}
		grant := snapshot.CoordinatorDelegations["del-r5-n3-no-record"]
		grant.PrimaryRestart = nil
		snapshot.CoordinatorDelegations[grant.DelegationID] = grant
		if err := ValidateCanonicalState(snapshot); !errors.Is(err, ErrCoordinatorDelegationInvalid) || !strings.Contains(err.Error(), "no matching configured record") {
			t.Fatalf("reload accepted PRIMARY_RESTART without configured primaryRestart record: %v", err)
		}
	})
	t.Run("generation_must_exceed_highest_primary_generation", func(t *testing.T) {
		snapshot, _, _, a2, a3 := recovery5TwoRestartSnapshot(t, "del-r5-n3-generation-high-water")
		grant := snapshot.CoordinatorDelegations["del-r5-n3-generation-high-water"]
		for i := range grant.History {
			if grant.History[i].Kind == DelegationHistoryPrimaryRestart && grant.History[i].To != nil && grant.History[i].To.Equal(a3) {
				to := *grant.History[i].To
				to.SessionID = "claude:a4"
				to.Generation = a2.Generation
				grant.History[i].To = &to
				grant.ActingCoordinator = &to
			}
		}
		snapshot.CoordinatorDelegations[grant.DelegationID] = grant
		if err := ValidateCanonicalState(snapshot); !errors.Is(err, ErrCoordinatorDelegationInvalid) || !strings.Contains(err.Error(), "generation does not exceed highest primary generation") {
			t.Fatalf("reload accepted generation %d after prior primary generation %d: %v", a2.Generation, a2.Generation, err)
		}
	})
	t.Run("prior_restart_session_cannot_be_reused", func(t *testing.T) {
		snapshot, _, _, a2, a3 := recovery5TwoRestartSnapshot(t, "del-r5-n3-prior-session")
		grant := snapshot.CoordinatorDelegations["del-r5-n3-prior-session"]
		for i := range grant.History {
			if grant.History[i].Kind == DelegationHistoryPrimaryRestart && grant.History[i].To != nil && grant.History[i].To.Equal(a3) {
				to := *grant.History[i].To
				to.SessionID = a2.SessionID
				grant.History[i].To = &to
				grant.ActingCoordinator = &to
			}
		}
		snapshot.CoordinatorDelegations[grant.DelegationID] = grant
		if err := ValidateCanonicalState(snapshot); !errors.Is(err, ErrCoordinatorDelegationInvalid) || !strings.Contains(err.Error(), "reuses a prior restart session") {
			t.Fatalf("reload accepted reuse of prior primaryRestart session %q: %v", a2.SessionID, err)
		}
	})
}

func TestRecovery5_ReloadRejectsSiblingChangingSuccessorWithConsumedVisibility(t *testing.T) {
	f, now, a, b, c := recovery4Setup(t, "del-r5-r01-receipt-reuse", true)
	if err := recovery4Handoff(t, f, a, b, "asg-r4-x", "first-capture-b"); err != nil {
		t.Fatalf("first handoff to selected successor B: %v", err)
	}
	snapshot, err := copyCanonicalState(f.s.state)
	if err != nil {
		t.Fatal(err)
	}
	forgeRecoverySiblingWithoutReconsuming(t, &snapshot, "del-r5-r01-receipt-reuse", "asg-r4-y", a, c, "vis-r4-b", now)
	if err := ValidateCanonicalState(snapshot); !errors.Is(err, ErrCoordinatorDelegationInvalid) || !strings.Contains(err.Error(), "sibling handoff must retain selected successor and visibility") {
		t.Fatalf("reload accepted different successor reusing the first handoff visibility receipt: %v", err)
	}
}

func TestRecovery5_RestartedPrimaryRoutingFailsClosedAtBoundaries(t *testing.T) {
	t.Run("after_restarted_primary_is_lost", func(t *testing.T) {
		f, now, _, _, _ := recovery4Setup(t, "del-r5-primary-lost", false)
		a, a2, _, _ := recovery5RestartPrimary(t, f, "del-r5-primary-lost", now)
		if err := recovery4Handoff(t, f, a, a2, "asg-r4-x", "route-first-to-primary-before-loss"); err != nil {
			t.Fatal(err)
		}
		lossConfirm(t, f, "del-r5-primary-lost", "loss-r5-a2", ownercredential.DelegationPrincipalPayload{StableAgentID: a2.AgentID, SessionID: a2.SessionID, Generation: a2.Generation}, now)
		before := ownerStateBytes(t, f.s)
		if err := recovery4Handoff(t, f, a, a2, "asg-r4-y", "route-to-now-barred-primary"); err == nil {
			t.Fatal("lost and barred restarted primary received another assignment")
		}
		if !bytes.Equal(before, ownerStateBytes(t, f.s)) {
			t.Fatal("rejected route to barred primary changed canonical state")
		}
	})
	t.Run("after_revoke", func(t *testing.T) {
		f, now, _, _, _ := recovery4Setup(t, "del-r5-primary-revoked", false)
		a, a2, _, _ := recovery5RestartPrimary(t, f, "del-r5-primary-revoked", now)
		revokeRecoveryR2Grant(t, f, "del-r5-primary-revoked", "revoke-r5-primary-route")
		before := ownerStateBytes(t, f.s)
		if err := recovery4Handoff(t, f, a, a2, "asg-r4-x", "route-after-revoke"); err == nil {
			t.Fatal("revoked grant routed lost assignment to restarted primary")
		}
		if !bytes.Equal(before, ownerStateBytes(t, f.s)) {
			t.Fatal("rejected route after revoke changed canonical state")
		}
	})
	t.Run("assignment_without_recorded_loss", func(t *testing.T) {
		f, now, _, _, _ := recovery4Setup(t, "del-r5-primary-unrecorded", false)
		a, a2, _, _ := recovery5RestartPrimary(t, f, "del-r5-primary-unrecorded", now)
		recovery5AddGrantBoundAssignment(t, f, "del-r5-primary-unrecorded", "asg-r5-unrecorded", a, now)
		before := ownerStateBytes(t, f.s)
		if err := recovery4Handoff(t, f, a, a2, "asg-r5-unrecorded", "route-without-recorded-loss"); err == nil {
			t.Fatal("assignment without recorded loss routed to restarted primary")
		}
		if !bytes.Equal(before, ownerStateBytes(t, f.s)) {
			t.Fatal("rejected unrecorded assignment route changed canonical state")
		}
	})
	t.Run("other_grant_assignment", func(t *testing.T) {
		f, now, _, _, _ := recovery4Setup(t, "del-r5-primary-other-grant", false)
		_, a2, _, _ := recovery5RestartPrimary(t, f, "del-r5-primary-other-grant", now)
		otherPrimary := assignment.Principal{SessionID: "deepseek:other-primary", AgentID: "agent-other-primary", Generation: 1}
		registerSession(t, f, otherPrimary.SessionID, otherPrimary.AgentID)
		grantDelegationWith(t, f, "del-r5-primary-other-grant-2", now, delegationOpts{successors: []ownercredential.DelegationPrincipalPayload{
			{StableAgentID: "agent-b", SessionID: "codex:b", Generation: 1},
		}, repository: "elsewhere/repo", project: "elsewhere", primary: ownercredential.DelegationPrincipalPayload{StableAgentID: otherPrimary.AgentID, SessionID: otherPrimary.SessionID, Generation: otherPrimary.Generation}})
		recovery5AddGrantBoundAssignment(t, f, "del-r5-primary-other-grant-2", "asg-r5-other-grant", otherPrimary, now)
		before := ownerStateBytes(t, f.s)
		if err := recovery4Handoff(t, f, otherPrimary, a2, "asg-r5-other-grant", "route-other-grant-to-primary"); err == nil {
			t.Fatal("assignment bound to another grant routed to restarted primary")
		}
		if !bytes.Equal(before, ownerStateBytes(t, f.s)) {
			t.Fatal("rejected other-grant route changed canonical state")
		}
	})
	t.Run("successor_while_restarted_primary_is_acting", func(t *testing.T) {
		f, now, _, _, _ := recovery4Setup(t, "del-r5-primary-successor", true)
		a, _, b, _ := recovery5RestartPrimary(t, f, "del-r5-primary-successor", now)
		before := ownerStateBytes(t, f.s)
		if err := recovery4Handoff(t, f, a, b, "asg-r4-x", "successor-while-primary-acts"); err == nil {
			t.Fatal("successor received lost assignment while restarted primary acts")
		}
		if !bytes.Equal(before, ownerStateBytes(t, f.s)) {
			t.Fatal("rejected successor route changed canonical state")
		}
	})
}

func TestRecovery5_ReloadRejectsRestartedPrimaryRoutingOutsideOwnerDecision(t *testing.T) {
	t.Run("barred_primary_after_new_loss", func(t *testing.T) {
		f, now, _, _, _ := recovery4Setup(t, "del-r5-reload-barred", false)
		a, a2, _, _ := recovery5RestartPrimary(t, f, "del-r5-reload-barred", now)
		if err := recovery4Handoff(t, f, a, a2, "asg-r4-x", "route-before-a2-loss"); err != nil {
			t.Fatal(err)
		}
		lossConfirm(t, f, "del-r5-reload-barred", "loss-r5-reload-a2", ownercredential.DelegationPrincipalPayload{StableAgentID: a2.AgentID, SessionID: a2.SessionID, Generation: a2.Generation}, now)
		snapshot, err := copyCanonicalState(f.s.state)
		if err != nil {
			t.Fatal(err)
		}
		forgeRecoverySiblingWithoutReconsuming(t, &snapshot, "del-r5-reload-barred", "asg-r4-y", a, a2, "vis-r5-primary-restart", now)
		if err := ValidateCanonicalState(snapshot); !errors.Is(err, ErrCoordinatorDelegationInvalid) {
			t.Fatalf("reload routed assignment to a barred restarted primary: %v", err)
		}
	})
	t.Run("revoked_grant", func(t *testing.T) {
		f, now, _, _, _ := recovery4Setup(t, "del-r5-reload-revoked", false)
		a, a2, _, _ := recovery5RestartPrimary(t, f, "del-r5-reload-revoked", now)
		revokeRecoveryR2Grant(t, f, "del-r5-reload-revoked", "revoke-r5-reload")
		snapshot, err := copyCanonicalState(f.s.state)
		if err != nil {
			t.Fatal(err)
		}
		forgeRecoverySiblingWithoutReconsuming(t, &snapshot, "del-r5-reload-revoked", "asg-r4-x", a, a2, "vis-r5-primary-restart", now)
		if err := ValidateCanonicalState(snapshot); !errors.Is(err, ErrCoordinatorDelegationInvalid) {
			t.Fatalf("reload routed assignment through a revoked grant: %v", err)
		}
	})
	t.Run("assignment_without_recorded_loss", func(t *testing.T) {
		f, now, _, _, _ := recovery4Setup(t, "del-r5-reload-unrecorded", false)
		a, a2, _, _ := recovery5RestartPrimary(t, f, "del-r5-reload-unrecorded", now)
		recovery5AddGrantBoundAssignment(t, f, "del-r5-reload-unrecorded", "asg-r5-reload-unrecorded", a, now)
		snapshot, err := copyCanonicalState(f.s.state)
		if err != nil {
			t.Fatal(err)
		}
		forgeRecoveryHandoffWithoutVisibility(t, &snapshot, "del-r5-reload-unrecorded", "asg-r5-reload-unrecorded", a, a2, "vis-r5-primary-restart", now)
		if err := ValidateCanonicalState(snapshot); !errors.Is(err, ErrCoordinatorDelegationInvalid) {
			t.Fatalf("reload routed an assignment without recorded loss to the restarted primary: %v", err)
		}
	})
	t.Run("assignment_from_another_grant", func(t *testing.T) {
		f, now, _, _, _ := recovery4Setup(t, "del-r5-reload-other-grant", false)
		_, a2, _, _ := recovery5RestartPrimary(t, f, "del-r5-reload-other-grant", now)
		otherPrimary := assignment.Principal{SessionID: "deepseek:reload-other-primary", AgentID: "agent-reload-other", Generation: 1}
		registerSession(t, f, otherPrimary.SessionID, otherPrimary.AgentID)
		grantDelegationWith(t, f, "del-r5-reload-other-grant-2", now, delegationOpts{
			successors: []ownercredential.DelegationPrincipalPayload{{StableAgentID: "agent-b", SessionID: "codex:b", Generation: 1}},
			repository: "other/reload-repo", project: "other-reload",
			primary: ownercredential.DelegationPrincipalPayload{StableAgentID: otherPrimary.AgentID, SessionID: otherPrimary.SessionID, Generation: otherPrimary.Generation},
		})
		recovery5AddGrantBoundAssignment(t, f, "del-r5-reload-other-grant-2", "asg-r5-reload-other-grant", otherPrimary, now)
		snapshot, err := copyCanonicalState(f.s.state)
		if err != nil {
			t.Fatal(err)
		}
		forgeRecoveryHandoffWithoutVisibility(t, &snapshot, "del-r5-reload-other-grant-2", "asg-r5-reload-other-grant", otherPrimary, a2, "vis-r5-primary-restart", now)
		if err := ValidateCanonicalState(snapshot); !errors.Is(err, ErrCoordinatorDelegationInvalid) {
			t.Fatalf("reload routed another grant's assignment to this grant's restarted primary: %v", err)
		}
	})
	t.Run("successor_while_restarted_primary_is_acting", func(t *testing.T) {
		f, now, a, b, _ := recovery4Setup(t, "del-r5-reload-successor", true)
		_, a2, _, _ := recovery5RestartPrimary(t, f, "del-r5-reload-successor", now)
		snapshot, err := copyCanonicalState(f.s.state)
		if err != nil {
			t.Fatal(err)
		}
		forgeRecoveryHandoff(t, &snapshot, "del-r5-reload-successor", "asg-r4-x", a, b, "vis-r4-b", now)
		if err := ValidateCanonicalState(snapshot); !errors.Is(err, ErrCoordinatorDelegationInvalid) || !strings.Contains(err.Error(), "another unbarred coordinator is acting") {
			t.Fatalf("reload accepted a successor while the restarted primary is acting (%s): %v", a2.SessionID, err)
		}
	})
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
		if err := enforceDelegatedHandoff(&state, a, b, "asg-r4-y", now); err == nil {
			t.Fatal("live enforcement allowed pre-loss sibling context without fresh visibility")
		}
		before := ownerStateBytes(t, f.s)
		err = recovery4Handoff(t, f, a, b, "asg-r4-y", "stale-sibling-context-after-new-loss")
		if err == nil {
			t.Fatalf("sibling reused old no-visibility context after distinct loss: %v", err)
		}
		if !bytes.Equal(before, ownerStateBytes(t, f.s)) {
			t.Fatal("rejected stale sibling context changed canonical bytes")
		}
		confirmVisibility(t, f, "del-r5-context-live", "vis-r5-context-b-after-loss", ownercredential.DelegationPrincipalPayload{StableAgentID: b.AgentID, SessionID: b.SessionID, Generation: b.Generation}, now)
		if err := recovery4Handoff(t, f, a, b, "asg-r4-y", "fresh-sibling-capture-after-new-loss"); err != nil {
			t.Fatalf("fresh capture by already acting selected successor after separate loss: %v", err)
		}
		if err := ValidateCanonicalState(f.s.state); err != nil {
			t.Fatalf("fresh capture positive control failed reload validation: %v", err)
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
		if err := ValidateCanonicalState(snapshot); !errors.Is(err, ErrCoordinatorDelegationInvalid) {
			t.Fatalf("reload accepted stale sibling reuse without fresh visibility: %v", err)
		}
		if _, err := NewCanonical(&canonicalMemory{state: snapshot}); !errors.Is(err, ErrCoordinatorDelegationInvalid) {
			t.Fatal("startup reload accepted stale sibling reuse without fresh visibility")
		}
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
		if err := validateHandoffGrantDelta(f.s.state, next, before, after); err == nil || !strings.Contains(err.Error(), "visibility receipt required") {
			t.Fatalf("handoff seam accepted no-consumption delta to non-acting C: %v", err)
		}
	})
	t.Run("accepts_exact_already_acting_sibling", func(t *testing.T) {
		next, after := makeAfter(b)
		if err := validateHandoffGrantDelta(f.s.state, next, before, after); err != nil {
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
