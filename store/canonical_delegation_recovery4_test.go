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

func recovery4Principals() (assignment.Principal, assignment.Principal, assignment.Principal) {
	return assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1},
		assignment.Principal{SessionID: "codex:b", AgentID: "agent-b", Generation: 1},
		assignment.Principal{SessionID: "gemini:c", AgentID: "agent-c", Generation: 1}
}

func recovery4Setup(t *testing.T, grantID string, withCVisibility bool) (*ownerCommandFixture, time.Time, assignment.Principal, assignment.Principal, assignment.Principal) {
	t.Helper()
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	a, b, c := recovery4Principals()
	grantDelegationWith(t, f, grantID, now, delegationOpts{successors: []ownercredential.DelegationPrincipalPayload{
		{StableAgentID: b.AgentID, SessionID: b.SessionID, Generation: b.Generation},
		{StableAgentID: c.AgentID, SessionID: c.SessionID, Generation: c.Generation},
	}})
	seedDelegatedAssignment(t, f, "asg-r4-x", a, now)
	seedDelegatedAssignment(t, f, "asg-r4-y", a, now)
	registerSession(t, f, b.SessionID, b.AgentID)
	registerSession(t, f, c.SessionID, c.AgentID)
	confirmVisibility(t, f, grantID, "vis-r4-b", ownercredential.DelegationPrincipalPayload{StableAgentID: b.AgentID, SessionID: b.SessionID, Generation: b.Generation}, now)
	if withCVisibility {
		confirmVisibility(t, f, grantID, "vis-r4-c", ownercredential.DelegationPrincipalPayload{StableAgentID: c.AgentID, SessionID: c.SessionID, Generation: c.Generation}, now)
	}
	lossConfirm(t, f, grantID, "loss-r4-a", ownercredential.DelegationPrincipalPayload{StableAgentID: a.AgentID, SessionID: a.SessionID, Generation: a.Generation}, now)
	return f, now, a, b, c
}

func recovery4Handoff(t *testing.T, f *ownerCommandFixture, from, to assignment.Principal, id, evidence string) error {
	t.Helper()
	r := f.s.state.AssignmentLedger.Records[id]
	return f.s.HandoffAssignment(from, to, id, r.Revision, evidence)
}

func TestRecoveryA1_OneLossSelectsOneSuccessorForAllSiblings(t *testing.T) {
	t.Run("same_successor_no_second_visibility_live_and_reload", func(t *testing.T) {
		f, _, a, b, _ := recovery4Setup(t, "del-r4-one-successor", false)
		if err := recovery4Handoff(t, f, a, b, "asg-r4-x", "handoff-r4-x-to-b"); err != nil {
			t.Fatalf("handoffXToB: %v", err)
		}
		if err := recovery4Handoff(t, f, a, b, "asg-r4-y", "handoff-r4-y-to-b-without-new-visibility"); err != nil {
			t.Fatalf("handoffYToBWithoutNewVisibility: %v", err)
		}
		grant := f.s.state.CoordinatorDelegations["del-r4-one-successor"]
		if grant.CurrentActing() != principalFromAssignment(b) {
			t.Fatalf("sibling handoff changed acting coordinator: %+v", grant.CurrentActing())
		}
		consumed := 0
		for _, v := range grant.VisibilityReceipts {
			if !v.ConsumedAt.IsZero() {
				consumed++
				if v.MessageID != "vis-r4-b" || v.ConsumedBy != "asg-r4-x" {
					t.Fatalf("unexpected visibility consumption: %+v", v)
				}
			}
		}
		if consumed != 1 {
			t.Fatalf("visibility consumed %d times, want exactly once", consumed)
		}
		if err := ValidateCanonicalState(f.s.state); err != nil {
			t.Fatalf("valid same-successor sibling history rejected: %v", err)
		}
		snap, err := copyCanonicalState(f.s.state)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := NewCanonical(&canonicalMemory{state: snap}); err != nil {
			t.Fatalf("reload of same-successor sibling history: %v", err)
		}
	})

	t.Run("first_handoff_requires_fresh_visibility", func(t *testing.T) {
		f, _, a, b, _ := recovery4Setup(t, "del-r4-first-no-vis", false)
		grant := f.s.state.CoordinatorDelegations["del-r4-first-no-vis"]
		grant.VisibilityReceipts = nil
		history := grant.History[:0]
		for _, h := range grant.History {
			if h.Kind != DelegationHistoryVisibility {
				history = append(history, h)
			}
		}
		grant.History = history
		f.s.state.CoordinatorDelegations[grant.DelegationID] = grant
		before := ownerStateBytes(t, f.s)
		if err := recovery4Handoff(t, f, a, b, "asg-r4-x", "first-handoff-no-visibility"); !errors.Is(err, ErrCoordinatorDelegationExhausted) {
			t.Fatalf("first handoff without visibility err=%v", err)
		}
		if !bytes.Equal(before, ownerStateBytes(t, f.s)) {
			t.Fatal("first handoff without visibility changed state")
		}
	})

	t.Run("different_successor_rejected_live_and_reload", func(t *testing.T) {
		f, now, a, b, c := recovery4Setup(t, "del-r4-different-successor", true)
		if err := recovery4Handoff(t, f, a, b, "asg-r4-x", "handoff-r4-x-to-b"); err != nil {
			t.Fatalf("handoffXToB: %v", err)
		}
		before := ownerStateBytes(t, f.s)
		if err := recovery4Handoff(t, f, a, c, "asg-r4-y", "handoff-r4-y-to-c-while-b-active"); !errors.Is(err, ErrCoordinatorDelegationInvalid) || !strings.Contains(err.Error(), "sibling handoff must retain selected successor") {
			t.Fatalf("handoffYToCWhileBActive err=%v", err)
		}
		if !bytes.Equal(before, ownerStateBytes(t, f.s)) {
			t.Fatal("rejected different successor changed canonical state")
		}
		snap, err := copyCanonicalState(f.s.state)
		if err != nil {
			t.Fatal(err)
		}
		forgeRecoveryHandoff(t, &snap, "del-r4-different-successor", "asg-r4-y", a, c, "vis-r4-c", now)
		assertRecoveryReloadRejectsWithText(t, snap, "historically inconsistent sibling successor", "sibling handoff must retain selected successor")
	})

	t.Run("repeat_assignment_and_outside_loss_rejected", func(t *testing.T) {
		f, now, a, b, _ := recovery4Setup(t, "del-r4-repeat-outside", false)
		if err := recovery4Handoff(t, f, a, b, "asg-r4-x", "handoff-r4-x-to-b"); err != nil {
			t.Fatalf("handoffXToB: %v", err)
		}
		record := f.s.state.AssignmentLedger.Records["asg-r4-y"]
		if err := appendRecoveryR1UnlistedAssignment(f.s, record, "asg-r4-outside-loss", now); err != nil {
			t.Fatal(err)
		}
		before := ownerStateBytes(t, f.s)
		if err := recovery4Handoff(t, f, a, b, "asg-r4-x", "repeat-handoff-x"); !errors.Is(err, assignment.ErrInvalid) {
			t.Fatalf("repeatHandoffX err=%v", err)
		}
		if !bytes.Equal(before, ownerStateBytes(t, f.s)) {
			t.Fatal("repeatHandoffX changed canonical state")
		}
		if err := recovery4Handoff(t, f, a, b, "asg-r4-outside-loss", "outside-loss-to-b"); !errors.Is(err, ErrCoordinatorDelegationInvalid) {
			t.Fatalf("assignmentOutsideLossToB err=%v", err)
		}
		if !bytes.Equal(before, ownerStateBytes(t, f.s)) {
			t.Fatal("assignmentOutsideLossToB changed canonical state")
		}
	})

	t.Run("revoked_grant_cannot_finish_sibling", func(t *testing.T) {
		f, _, a, b, _ := recovery4Setup(t, "del-r4-revoked", false)
		if err := recovery4Handoff(t, f, a, b, "asg-r4-x", "handoff-r4-x-to-b"); err != nil {
			t.Fatalf("handoffXToB: %v", err)
		}
		revokeRecoveryR2Grant(t, f, "del-r4-revoked", "revoke-r4")
		before := ownerStateBytes(t, f.s)
		if err := recovery4Handoff(t, f, a, b, "asg-r4-y", "revoked-grant-sibling"); !errors.Is(err, ErrCoordinatorDelegationInvalid) {
			t.Fatalf("revokedGrantRemainingHandoff err=%v", err)
		}
		if !bytes.Equal(before, ownerStateBytes(t, f.s)) {
			t.Fatal("revoked grant sibling handoff changed state")
		}
	})

	t.Run("new_loss_allows_fresh_capture_after_selected_successor_barred", func(t *testing.T) {
		f, now, a, b, c := recovery4Setup(t, "del-r4-successor-loss", false)
		if err := recovery4Handoff(t, f, a, b, "asg-r4-x", "handoff-r4-x-to-b"); err != nil {
			t.Fatalf("handoffXToB: %v", err)
		}
		if err := recovery4Handoff(t, f, a, b, "asg-r4-y", "handoff-r4-y-to-b-no-vis"); err != nil {
			t.Fatalf("handoffYToBWithoutNewVisibility: %v", err)
		}
		lossConfirm(t, f, "del-r4-successor-loss", "loss-r4-b", ownercredential.DelegationPrincipalPayload{StableAgentID: b.AgentID, SessionID: b.SessionID, Generation: b.Generation}, now)
		confirmVisibility(t, f, "del-r4-successor-loss", "vis-r4-c-after-loss", ownercredential.DelegationPrincipalPayload{StableAgentID: c.AgentID, SessionID: c.SessionID, Generation: c.Generation}, now)
		if err := recovery4Handoff(t, f, b, c, "asg-r4-x", "handoff-r4-x-to-c-after-b-loss"); err != nil {
			t.Fatalf("fresh capture after B loss: %v", err)
		}
		if err := recovery4Handoff(t, f, b, c, "asg-r4-y", "handoff-r4-y-to-c-after-b-loss"); err != nil {
			t.Fatalf("sibling under new loss-selected successor: %v", err)
		}
		if err := ValidateCanonicalState(f.s.state); err != nil {
			t.Fatalf("history after new loss capture rejected: %v", err)
		}
	})
}

func TestRecoveryN1_RestartedPrimaryCannotBeDisplacedByOldLoss(t *testing.T) {
	for _, id := range []string{"asg-r4-x", "asg-r4-y"} {
		t.Run("restartPrimaryThenHandoff_"+id, func(t *testing.T) {
			f, now, a, b, _ := recovery4Setup(t, "del-r4-n1-"+id, false)
			registerSession(t, f, "claude:a2", "agent-a")
			bumpSessionGeneration(t, f, "claude:a2", 2)
			confirmPrimaryRestartVisibility(t, f, "del-r4-n1-"+id, "vis-r4-primary-"+id, "agent-a", now)
			a2 := assignment.Principal{SessionID: "claude:a2", AgentID: "agent-a", Generation: 2}
			if err := f.s.ApplyPrimaryRestart("del-r4-n1-"+id, a2, "restart-r4-n1"); err != nil {
				t.Fatalf("primary restart positive setup: %v", err)
			}
			before := ownerStateBytes(t, f.s)
			err := recovery4Handoff(t, f, a, b, id, "old-loss-recapture")
			if !errors.Is(err, ErrCoordinatorDelegationInvalid) || !strings.Contains(err.Error(), "another unbarred coordinator is acting") {
				t.Fatalf("restartPrimaryThenHandoff must fail: %v", err)
			}
			if !bytes.Equal(before, ownerStateBytes(t, f.s)) {
				t.Fatal("rejected handoff changed canonical state")
			}
			// Reusing the pre-restart successor visibility cannot recapture the
			// authority from the now-acting primary.
			fresh := f.s.state.AssignmentLedger.Records[id]
			fresh.AssignmentID = "asg-r4-primary-still-acting-" + id
			fresh.State = assignment.Offered
			fresh.Revision = 1
			fresh.Issuer, fresh.IssuerAgentID, fresh.IssuerGeneration = a2.SessionID, a2.AgentID, a2.Generation
			fresh.CurrentCoordinator, fresh.CoordinatorAgentID, fresh.CoordinatorGeneration = a2.SessionID, a2.AgentID, a2.Generation
			fresh.CoordinatorLost = false
			fresh.DelegationBinding = nil
			fresh.HandoffEvidence = []assignment.Handoff{}
			fresh.AcknowledgedAt, fresh.WorkStartedAt = time.Time{}, time.Time{}
			fresh.AcceptedResult = nil
			fresh.IssuedAt, fresh.UpdatedAt, fresh.ValidUntil = now, now, now.Add(time.Hour)
			if err := f.s.OfferAssignment(a2, fresh, "primary-remains-acting"); err != nil {
				t.Fatalf("restarted primary lost offer authority after rejected handoff: %v", err)
			}
			if f.s.state.AssignmentLedger.Records[fresh.AssignmentID].Coordinator() != a2 {
				t.Fatal("new assignment did not retain restarted primary as coordinator")
			}
		})
	}
}

func TestRecoveryN1_RestartedPrimaryHandoffSnapshotRejected(t *testing.T) {
	f, now, a, b, _ := recovery4Setup(t, "del-r4-n1-reload", false)
	registerSession(t, f, "claude:a2", "agent-a")
	bumpSessionGeneration(t, f, "claude:a2", 2)
	confirmPrimaryRestartVisibility(t, f, "del-r4-n1-reload", "vis-r4-primary-reload", "agent-a", now)
	if err := f.s.ApplyPrimaryRestart("del-r4-n1-reload", assignment.Principal{SessionID: "claude:a2", AgentID: "agent-a", Generation: 2}, "restart-r4-n1-reload"); err != nil {
		t.Fatalf("primary restart setup: %v", err)
	}
	snap, err := copyCanonicalState(f.s.state)
	if err != nil {
		t.Fatal(err)
	}
	forgeRecoveryHandoff(t, &snap, "del-r4-n1-reload", "asg-r4-x", a, b, "vis-r4-b", now)
	assertRecoveryReloadRejectsWithText(t, snap, "handoff covered by old loss while restarted primary acts", "another unbarred coordinator is acting")
}

func TestRecoveryN2_PrimaryRestartCannotLoadWhileSuccessorActs(t *testing.T) {
	t.Run("q14_same_loss_after_sibling_handoff", func(t *testing.T) {
		f, now, a, b, _ := recovery4Setup(t, "del-r4-q14", false)
		if err := recovery4Handoff(t, f, a, b, "asg-r4-x", "handoff-q14-x-to-b"); err != nil {
			t.Fatalf("handoff setup: %v", err)
		}
		registerSession(t, f, "claude:a2", "agent-a")
		bumpSessionGeneration(t, f, "claude:a2", 2)
		confirmPrimaryRestartVisibility(t, f, "del-r4-q14", "vis-r4-q14-primary", "agent-a", now)
		before := ownerStateBytes(t, f.s)
		if err := f.s.ApplyPrimaryRestart("del-r4-q14", assignment.Principal{SessionID: "claude:a2", AgentID: "agent-a", Generation: 2}, "restart-q14-live"); !errors.Is(err, ErrCoordinatorDelegationInvalid) || !strings.Contains(err.Error(), "predecessor still acting") {
			t.Fatalf("primaryRestartWhileSuccessorActing live err=%v", err)
		}
		if !bytes.Equal(before, ownerStateBytes(t, f.s)) {
			t.Fatal("rejected live primaryRestart changed canonical state")
		}
		snap, err := copyCanonicalState(f.s.state)
		if err != nil {
			t.Fatal(err)
		}
		forgeRecoveryPrimaryRestart(t, &snap, "del-r4-q14", "vis-r4-q14-primary", assignment.Principal{SessionID: "claude:a2", AgentID: "agent-a", Generation: 2}, "restart-q14-forged", now)
		assertRecoveryReloadRejects(t, snap, "q14 primaryRestart while successor B acts")
	})

	t.Run("q13_after_new_loss_sibling_capture", func(t *testing.T) {
		f, now, a, b, c := recovery4Setup(t, "del-r4-q13", false)
		if err := recovery4Handoff(t, f, a, b, "asg-r4-x", "handoff-q13-x-to-b"); err != nil {
			t.Fatalf("first handoff: %v", err)
		}
		lossConfirm(t, f, "del-r4-q13", "loss-r4-q13-b", ownercredential.DelegationPrincipalPayload{StableAgentID: b.AgentID, SessionID: b.SessionID, Generation: b.Generation}, now)
		confirmVisibility(t, f, "del-r4-q13", "vis-r4-q13-y-c", ownercredential.DelegationPrincipalPayload{StableAgentID: c.AgentID, SessionID: c.SessionID, Generation: c.Generation}, now)
		if err := recovery4Handoff(t, f, a, c, "asg-r4-y", "handoff-q13-y-to-c"); err != nil {
			t.Fatalf("fresh capture for remaining assignment after B loss: %v", err)
		}
		registerSession(t, f, "claude:a2", "agent-a")
		bumpSessionGeneration(t, f, "claude:a2", 2)
		confirmPrimaryRestartVisibility(t, f, "del-r4-q13", "vis-r4-q13-primary", "agent-a", now)
		// The live path must reject a restart of L2 while C is acting; reload must
		// reject the same otherwise well-formed history even if that transition is forged.
		if err := f.s.ApplyPrimaryRestart("del-r4-q13", assignment.Principal{SessionID: "claude:a2", AgentID: "agent-a", Generation: 2}, "restart-q13-live"); !errors.Is(err, ErrCoordinatorDelegationInvalid) || !strings.Contains(err.Error(), "predecessor still acting") {
			t.Fatalf("primaryRestartWhileSuccessorActing live err=%v", err)
		}
		snap, err := copyCanonicalState(f.s.state)
		if err != nil {
			t.Fatal(err)
		}
		forgeRecoveryPrimaryRestart(t, &snap, "del-r4-q13", "vis-r4-q13-primary", assignment.Principal{SessionID: "claude:a2", AgentID: "agent-a", Generation: 2}, "restart-q13-forged", now)
		assertRecoveryReloadRejectsWithText(t, snap, "q13 primaryRestart while successor C acts", "PRIMARY_RESTART while another coordinator is acting")
	})
}

func forgeRecoveryHandoff(t *testing.T, state *CanonicalState, grantID, assignmentID string, from, to assignment.Principal, visibilityID string, now time.Time) {
	t.Helper()
	prior, ok := state.AssignmentLedger.Records[assignmentID]
	if !ok {
		t.Fatalf("forge handoff: assignment %s missing", assignmentID)
	}
	if !now.After(prior.UpdatedAt) {
		now = prior.UpdatedAt.Add(time.Nanosecond)
	}
	next := prior
	next.CurrentCoordinator, next.CoordinatorAgentID, next.CoordinatorGeneration = to.SessionID, to.AgentID, to.Generation
	next.CoordinatorLost = false
	next.HandoffEvidence = append(append([]assignment.Handoff(nil), prior.HandoffEvidence...), assignment.Handoff{From: from, To: to, EvidenceRef: "forged-reload-handoff", At: now})
	state.Revision++
	if err := appendAssignment(state, to, prior, next, "HANDOFF", "forged-reload-handoff", now); err != nil {
		t.Fatalf("forge handoff event: %v", err)
	}
	grant := state.CoordinatorDelegations[grantID]
	fromP, toP := principalFromAssignment(from), principalFromAssignment(to)
	grant.ActingCoordinator = &toP
	grant.History = append(grant.History, DelegationHistoryEntry{Kind: DelegationHistoryHandoff, At: now, AssignmentID: assignmentID, From: &fromP, To: &toP, ReceiptDig: visibilityID, CanonicalRevision: state.Revision})
	for i := range grant.VisibilityReceipts {
		if grant.VisibilityReceipts[i].MessageID == visibilityID {
			grant.VisibilityReceipts[i].ConsumedAt = now
			grant.VisibilityReceipts[i].ConsumedBy = assignmentID
		}
	}
	state.CoordinatorDelegations[grantID] = grant
}

func forgeRecoveryPrimaryRestart(t *testing.T, state *CanonicalState, grantID, visibilityID string, to assignment.Principal, evidence string, now time.Time) {
	t.Helper()
	grant := state.CoordinatorDelegations[grantID]
	var loss *DelegationHistoryEntry
	for i := len(grant.History) - 1; i >= 0; i-- {
		if grant.History[i].Kind == DelegationHistoryLossRecorded {
			loss = &grant.History[i]
			break
		}
	}
	if loss == nil || loss.From == nil {
		t.Fatal("forge primary restart: loss missing")
	}
	state.Revision++
	toP := principalFromAssignment(to)
	grant.ActingCoordinator = &toP
	grant.History = append(grant.History, DelegationHistoryEntry{Kind: DelegationHistoryPrimaryRestart, At: now, AssignmentID: loss.AssignmentID, From: loss.From, To: &toP, ReceiptID: loss.ReceiptID, ReceiptDig: visibilityID, Reason: evidence, CanonicalRevision: state.Revision})
	for i := range grant.VisibilityReceipts {
		if grant.VisibilityReceipts[i].MessageID == visibilityID {
			grant.VisibilityReceipts[i].ConsumedAt = now
			grant.VisibilityReceipts[i].ConsumedBy = evidence
		}
	}
	state.CoordinatorDelegations[grantID] = grant
}

func assertRecoveryReloadRejects(t *testing.T, state CanonicalState, scenario string) {
	t.Helper()
	if err := ValidateCanonicalState(state); !errors.Is(err, ErrCoordinatorDelegationInvalid) {
		t.Fatalf("ValidateCanonicalState accepted %s: %v", scenario, err)
	}
	if _, err := NewCanonical(&canonicalMemory{state: state}); !errors.Is(err, ErrCoordinatorDelegationInvalid) {
		t.Fatalf("NewCanonical accepted %s: %v", scenario, err)
	}
}

func assertRecoveryReloadRejectsWithText(t *testing.T, state CanonicalState, scenario, want string) {
	t.Helper()
	if err := ValidateCanonicalState(state); !errors.Is(err, ErrCoordinatorDelegationInvalid) || !strings.Contains(err.Error(), want) {
		t.Fatalf("ValidateCanonicalState %s err=%v, want error containing %q", scenario, err, want)
	}
	if _, err := NewCanonical(&canonicalMemory{state: state}); !errors.Is(err, ErrCoordinatorDelegationInvalid) || !strings.Contains(err.Error(), want) {
		t.Fatalf("NewCanonical %s err=%v, want error containing %q", scenario, err, want)
	}
}

func TestRecoveryA1_BoundGrantAndGenerationDoNotBorrowSiblingCapture(t *testing.T) {
	t.Run("different_grant_requires_its_own_visibility", func(t *testing.T) {
		f, now, a, b, _ := recovery4Setup(t, "del-r4-grant-one", false)
		if err := recovery4Handoff(t, f, a, b, "asg-r4-x", "grant-one-first-handoff"); err != nil {
			t.Fatalf("grant one first handoff: %v", err)
		}
		revokeRecoveryR2Grant(t, f, "del-r4-grant-one", "revoke-r4-grant-one")
		grantDelegationWith(t, f, "del-r4-grant-two", now, delegationOpts{successors: []ownercredential.DelegationPrincipalPayload{{StableAgentID: b.AgentID, SessionID: b.SessionID, Generation: b.Generation}}})
		seedDelegatedAssignment(t, f, "asg-r4-grant-two", a, now)
		lossConfirm(t, f, "del-r4-grant-two", "loss-r4-grant-two", ownercredential.DelegationPrincipalPayload{StableAgentID: a.AgentID, SessionID: a.SessionID, Generation: a.Generation}, now)
		before := ownerStateBytes(t, f.s)
		if err := recovery4Handoff(t, f, a, b, "asg-r4-grant-two", "other-grant-cannot-borrow-capture"); err == nil {
			t.Fatalf("differentGrantAssignmentToB err=%v", err)
		}
		if !bytes.Equal(before, ownerStateBytes(t, f.s)) {
			t.Fatal("different grant borrowing sibling capture changed state")
		}
		confirmVisibility(t, f, "del-r4-grant-two", "vis-r4-grant-two", ownercredential.DelegationPrincipalPayload{StableAgentID: b.AgentID, SessionID: b.SessionID, Generation: b.Generation}, now)
		if err := recovery4Handoff(t, f, a, b, "asg-r4-grant-two", "own-grant-fresh-capture"); err != nil {
			t.Fatalf("same successor with its own grant visibility: %v", err)
		}
	})

	t.Run("different_generation_binding_rejected", func(t *testing.T) {
		f, now, a, b, _ := recovery4Setup(t, "del-r4-generation", false)
		if err := recovery4Handoff(t, f, a, b, "asg-r4-x", "generation-control-handoff"); err != nil {
			t.Fatalf("positive handoff control: %v", err)
		}
		snap, err := copyCanonicalState(f.s.state)
		if err != nil {
			t.Fatal(err)
		}
		record := snap.AssignmentLedger.Records["asg-r4-y"]
		record.DelegationBinding.GrantGeneration++
		snap.AssignmentLedger.Records[record.AssignmentID] = record
		before := mustJSON(t, snap)
		err = enforceDelegatedHandoff(&snap, a, b, record.AssignmentID, now)
		if !errors.Is(err, ErrCoordinatorDelegationInvalid) {
			t.Fatalf("differentGenerationAssignmentToB err=%v", err)
		}
		if !bytes.Equal(before, mustJSON(t, snap)) {
			t.Fatal("generation mismatch rejection changed state")
		}
	})
}
