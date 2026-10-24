package store

import (
	"bytes"
	"coord/assignment"
	"coord/ownercredential"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// --- F1: AllowedWorkItems / repository / project enforced at mutation + loss ---

func TestF1_OutOfScopeWorkItemRejectedOnMutation(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-f1-mut", now)
	seedDelegatedAssignment(t, f, "asg-f1-in", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now)

	// Seed an out-of-scope assignment (Reference not in AllowedWorkItems) via UpdateCanonical.
	outID := "asg-f1-out"
	if err := f.s.UpdateCanonical(func(state *CanonicalState) error {
		coord := assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}
		target := assignment.Principal{SessionID: "codex:worker", AgentID: "worker", Generation: 1}
		sha := "cccccccccccccccccccccccccccccccccccccccc"
		r := assignment.Record{
			AssignmentID: outID, State: assignment.Offered,
			Issuer: coord.SessionID, IssuerAgentID: coord.AgentID, IssuerGeneration: 1,
			CurrentCoordinator: coord.SessionID, CoordinatorAgentID: coord.AgentID, CoordinatorGeneration: 1,
			HandoffEvidence: []assignment.Handoff{},
			Assignee:        target.SessionID, StableAgentID: target.AgentID, SessionID: target.SessionID, AssigneeGeneration: 1,
			Role: "IMPLEMENTER", SubjectSHA: sha,
			IssuedAt: now.Add(-time.Minute), ValidUntil: now.Add(time.Hour),
			AdmissionBasis:     assignment.AdmissionBasis{Kind: "POOL", Reference: "other-issue", TargetSessionID: target.SessionID, TargetAgentID: target.AgentID, Role: "IMPLEMENTER", SubjectSHA: sha},
			DelegationBinding:  &assignment.DelegationBinding{GrantID: "del-f1-mut", GrantGeneration: 1, Repository: "naghuale/coord", Project: "coord", AllowedWorkItem: "other-issue"},
			VisibilityEvidence: assignment.VisibilityEvidence{Reference: "vis", SessionID: target.SessionID, ObservedAt: now.Add(-time.Minute)},
			Revision:           1, UpdatedAt: now,
		}
		return state.AssignmentLedger.Append(assignment.Event{AssignmentID: outID, Revision: 1, Kind: "OFFER", Actor: coord, EvidenceRef: "seed-out", At: now, Record: r})
	}); err != nil {
		t.Fatal(err)
	}
	actor := assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}
	r := f.s.state.AssignmentLedger.Records[outID]
	before := ownerStateBytes(t, f.s)
	if err := f.s.ChangeAssignment(actor, outID, r.Revision, "VOID", "out-of-scope"); !errors.Is(err, ErrCoordinatorDelegationInvalid) {
		t.Fatalf("out-of-scope VOID err=%v", err)
	}
	if !bytes.Equal(before, ownerStateBytes(t, f.s)) {
		t.Fatal("out-of-scope mutation changed state")
	}
	_ = f.s.state.AssignmentLedger.Records["asg-f1-in"] // in-scope still present
}

func TestF1_LossConfirmIgnoresOutOfScopeAssignments(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-f1-loss", now)
	coord := assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}
	registerSession(t, f, coord.SessionID, coord.AgentID)
	registerSession(t, f, "codex:worker", "worker")

	// Only out-of-scope open work for the named triple → no open delegated work.
	if err := f.s.UpdateCanonical(func(state *CanonicalState) error {
		if state.AssignmentLedger == nil {
			state.AssignmentLedger = assignment.Empty()
		}
		target := assignment.Principal{SessionID: "codex:worker", AgentID: "worker", Generation: 1}
		sha := "dddddddddddddddddddddddddddddddddddddddd"
		offered := assignment.Record{
			AssignmentID: "asg-f1-oos", State: assignment.Offered,
			Issuer: coord.SessionID, IssuerAgentID: coord.AgentID, IssuerGeneration: 1,
			CurrentCoordinator: coord.SessionID, CoordinatorAgentID: coord.AgentID, CoordinatorGeneration: 1,
			HandoffEvidence: []assignment.Handoff{},
			Assignee:        target.SessionID, StableAgentID: target.AgentID, SessionID: target.SessionID, AssigneeGeneration: 1,
			Role: "IMPLEMENTER", SubjectSHA: sha,
			IssuedAt: now.Add(-time.Minute), ValidUntil: now.Add(time.Hour),
			AdmissionBasis:     assignment.AdmissionBasis{Kind: "POOL", Reference: "other-issue", TargetSessionID: target.SessionID, TargetAgentID: target.AgentID, Role: "IMPLEMENTER", SubjectSHA: sha},
			DelegationBinding:  &assignment.DelegationBinding{GrantID: "del-f1-loss", GrantGeneration: 1, Repository: "naghuale/coord", Project: "coord", AllowedWorkItem: "other-issue"},
			VisibilityEvidence: assignment.VisibilityEvidence{Reference: "vis", SessionID: target.SessionID, ObservedAt: now.Add(-time.Minute)},
			Revision:           1, UpdatedAt: now.Add(-time.Minute),
		}
		if err := state.AssignmentLedger.Append(assignment.Event{AssignmentID: "asg-f1-oos", Revision: 1, Kind: "OFFER", Actor: coord, EvidenceRef: "seed", At: offered.UpdatedAt, Record: offered}); err != nil {
			return err
		}
		acked := offered
		acked.State = assignment.Acked
		acked.Revision = 2
		acked.AcknowledgedAt = now.Add(-50 * time.Second)
		acked.UpdatedAt = acked.AcknowledgedAt
		if err := state.AssignmentLedger.Append(assignment.Event{AssignmentID: "asg-f1-oos", Revision: 2, Kind: "ACK", Actor: target, EvidenceRef: "ack", At: acked.UpdatedAt, Record: acked}); err != nil {
			return err
		}
		active := acked
		active.State = assignment.Active
		active.Revision = 3
		active.UpdatedAt = now.Add(-40 * time.Second)
		return state.AssignmentLedger.Append(assignment.Event{AssignmentID: "asg-f1-oos", Revision: 3, Kind: "ACTIVATE", Actor: coord, EvidenceRef: "act", At: active.UpdatedAt, Record: active})
	}); err != nil {
		t.Fatal(err)
	}
	lossPayload, _ := json.Marshal(ownercredential.LossConfirmPayload{
		DelegationID: "del-f1-loss",
		Coordinator:  ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1},
		Reason:       "hung", ObservedAt: now,
	})
	rev := f.s.state.OwnerCredentials["owner-1"].CredentialRevision
	cmd := f.command(ownercredential.CommandLossConfirm, "msg-f1-loss-oos", "owner-primary", rev, lossPayload)
	sign := f.s.state.OwnerCredentials["owner-1"].Credentials["owner-primary"].SignCount + 1
	assertOwnerCommandRejectedWithoutWrite(t, f, cmd, assertionFor(cmd, f.private["owner-primary"], sign), ErrCoordinatorDelegationInvalid)
	if f.s.state.AssignmentLedger.Records["asg-f1-oos"].CoordinatorLost {
		t.Fatal("out-of-scope assignment marked lost")
	}
}

// --- F2: generic UpdateCanonical cannot insert grants while disabled ---

func TestF2_UpdateCanonicalRejectsDelegationInsertWhileDisabled(t *testing.T) {
	if coordinatorDelegationActive() {
		t.Fatal("harness must be off")
	}
	f := newOwnerCommandFixture(t)
	before := ownerStateBytes(t, f.s)
	now := f.s.now().UTC()
	err := f.s.UpdateCanonical(func(state *CanonicalState) error {
		if state.CoordinatorDelegations == nil {
			state.CoordinatorDelegations = map[string]DelegationRecord{}
		}
		state.CoordinatorDelegations["forged"] = DelegationRecord{
			DelegationID:       "forged",
			Scope:              DelegationScope{Repository: "naghuale/coord", Project: "coord", AllowedWorkItems: []string{"issue-93b"}, AllowedActions: []string{"OFFER"}},
			PrimaryCoordinator: DelegationPrincipal{StableAgentID: "a", SessionID: "s", Generation: 1},
			Validity:           DelegationValidity{NotBefore: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour)},
			Successors:         []DelegationPrincipal{{StableAgentID: "b", SessionID: "t", Generation: 1}},
			Evidence:           DelegationEvidence{OwnerDecisionRef: "d", VisibilityEvidence: "v"},
			History:            []DelegationHistoryEntry{{Kind: DelegationHistoryGrant, At: now}},
		}
		return nil
	})
	if !errors.Is(err, ErrCoordinatorDelegationDisabled) {
		t.Fatalf("err=%v", err)
	}
	if !bytes.Equal(before, ownerStateBytes(t, f.s)) {
		t.Fatal("generic writer mutated snapshot while disabled")
	}
}

// --- F3: disabled load rejects delegation snapshot / OWNER_CREDENTIAL loss ---

func TestF3_DisabledLoadRejectsDelegationSnapshot(t *testing.T) {
	if coordinatorDelegationActive() {
		t.Fatal("harness must be off")
	}
	state := EmptyCanonicalState()
	state.SchemaVersion = 6
	now := time.Now().UTC()
	state.CoordinatorDelegations = map[string]DelegationRecord{
		"del-f3": {
			DelegationID:       "del-f3",
			Scope:              DelegationScope{Repository: "r", Project: "p", AllowedWorkItems: []string{"w"}, AllowedActions: []string{"OFFER"}},
			PrimaryCoordinator: DelegationPrincipal{StableAgentID: "a", SessionID: "s", Generation: 1},
			Validity:           DelegationValidity{NotBefore: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour)},
			Successors:         []DelegationPrincipal{{StableAgentID: "b", SessionID: "t", Generation: 1}},
			Evidence:           DelegationEvidence{OwnerDecisionRef: "d", VisibilityEvidence: "v"},
			History:            []DelegationHistoryEntry{{Kind: DelegationHistoryGrant, At: now}},
		},
	}
	if err := ValidateCanonicalState(state); !errors.Is(err, ErrCoordinatorDelegationDisabled) {
		t.Fatalf("err=%v", err)
	}
	p := &canonicalMemory{state: state}
	if _, err := NewCanonical(p); !errors.Is(err, ErrCoordinatorDelegationDisabled) {
		t.Fatalf("NewCanonical err=%v", err)
	}
}

func TestF3_DisabledLoadRejectsOwnerCredentialLoss(t *testing.T) {
	if coordinatorDelegationActive() {
		t.Fatal("harness must be off")
	}
	state := EmptyCanonicalState()
	state.SchemaVersion = 6
	state.AssignmentLedger = assignment.Empty()
	now := time.Now().UTC()
	sha := strings.Repeat("a", 40)
	offered := assignment.Record{
		AssignmentID: "asg-f3", State: assignment.Offered, Issuer: "s", IssuerAgentID: "a", IssuerGeneration: 1,
		CurrentCoordinator: "s", CoordinatorAgentID: "a", CoordinatorGeneration: 1,
		HandoffEvidence: []assignment.Handoff{},
		Assignee:        "u", StableAgentID: "target", SessionID: "u", AssigneeGeneration: 1,
		Role: "IMPLEMENTER", SubjectSHA: sha,
		IssuedAt: now.Add(-time.Minute), ValidUntil: now.Add(time.Hour),
		AdmissionBasis:     assignment.AdmissionBasis{Kind: "POOL", Reference: "w", TargetSessionID: "u", TargetAgentID: "target", Role: "IMPLEMENTER", SubjectSHA: sha},
		VisibilityEvidence: assignment.VisibilityEvidence{Reference: "v", SessionID: "u", ObservedAt: now.Add(-time.Minute)},
		Revision:           1, UpdatedAt: now.Add(-time.Minute),
	}
	active := offered
	active.State = assignment.Active
	active.Revision = 2
	active.AcknowledgedAt = now.Add(-50 * time.Second)
	active.UpdatedAt = now.Add(-40 * time.Second)
	active.CoordinatorLost = true
	active.Revision = 3
	active.UpdatedAt = now
	state.AssignmentLedger.Records["asg-f3"] = active
	state.AssignmentLedger.Events = []assignment.Event{
		{AssignmentID: "asg-f3", Revision: 1, Kind: "OFFER", Actor: assignment.Principal{SessionID: "s", AgentID: "a", Generation: 1}, EvidenceRef: "o", At: offered.UpdatedAt, Record: offered},
		{AssignmentID: "asg-f3", Revision: 2, Kind: "ACK", Actor: assignment.Principal{SessionID: "u", AgentID: "target", Generation: 1}, EvidenceRef: "a", At: active.AcknowledgedAt, Record: func() assignment.Record {
			r := offered
			r.State = assignment.Acked
			r.Revision = 2
			r.AcknowledgedAt = active.AcknowledgedAt
			r.UpdatedAt = active.AcknowledgedAt
			return r
		}()},
		{AssignmentID: "asg-f3", Revision: 3, Kind: "COORDINATOR_LOST",
			Actor:       assignment.Principal{SessionID: "ownercredential:owner", AgentID: "owner", Generation: 1},
			EvidenceRef: "x", At: now, Record: active,
			ActorSource:      assignment.ActorSourceOwnerCredential,
			OwnerPrincipalID: "owner", OwnerCredentialID: "key",
			OwnerReceiptMessageID: "r", OwnerReceiptDigest: strings.Repeat("a", 64),
		},
	}
	if err := ValidateCanonicalState(state); !errors.Is(err, ErrCoordinatorDelegationDisabled) {
		t.Fatalf("err=%v", err)
	}
}

// --- F4: disable gate before receipt replay ---

func TestF4_DisabledRejectsDurableDelegationReceiptReplay(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	payload, err := json.Marshal(ownercredential.DelegationGrantPayload{
		DelegationID: "del-f4",
		Scope: ownercredential.DelegationScopePayload{
			Repository: "naghuale/coord", Project: "coord",
			AllowedWorkItems: []string{"issue-93b"}, AllowedActions: []string{"OFFER"},
		},
		PrimaryCoordinator: ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1},
		Validity:           ownercredential.DelegationValidityPayload{NotBefore: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour)},
		Successors:         []ownercredential.DelegationPrincipalPayload{{StableAgentID: "agent-b", SessionID: "codex:b", Generation: 1}},
		Evidence:           ownercredential.DelegationEvidencePayload{OwnerDecisionRef: "ADR-0010", VisibilityEvidence: "owner-seen"},
	})
	if err != nil {
		t.Fatal(err)
	}
	command := f.command(ownercredential.CommandDelegationGrant, "msg-f4-grant", "owner-primary", 1, payload)
	receipt, replayed, err := f.s.ApplyOwnerCredentialCommand(command.ChallengeID, command, assertionFor(command, f.private["owner-primary"], 1))
	if err != nil || replayed || receipt.MessageID != command.MessageID {
		t.Fatalf("grant err=%v replayed=%v", err, replayed)
	}
	// Exact retry while harness on returns prior receipt.
	retry, replayed, err := f.s.ApplyOwnerCredentialCommand(command.ChallengeID, command, assertionFor(command, f.private["owner-primary"], 1))
	if err != nil || !replayed || retry.MessageID != receipt.MessageID {
		t.Fatalf("enabled replay err=%v replayed=%v", err, replayed)
	}
	// Turn harness off — same durable retry must fail closed before replay.
	delegationHarnessEnabled = false
	before := ownerStateBytes(t, f.s)
	_, _, err = f.s.ApplyOwnerCredentialCommand(command.ChallengeID, command, assertionFor(command, f.private["owner-primary"], 1))
	if !errors.Is(err, ErrCoordinatorDelegationDisabled) {
		t.Fatalf("disabled replay err=%v", err)
	}
	if !bytes.Equal(before, ownerStateBytes(t, f.s)) {
		t.Fatal("disabled replay mutated state")
	}
}

// --- F5: once per distinct lossRecordID ---

func TestF5_PrimaryRestartOncePerLossEvent(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-f5", now)
	seedDelegatedAssignment(t, f, "asg-f5", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now)
	lossConfirm(t, f, "del-f5", "loss-f5-1", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1}, now)
	registerSession(t, f, "claude:a2", "agent-a")
	// Fresh address registers at generation 1; ADR §8 requires a newly issued generation.
	bumpSessionGeneration(t, f, "claude:a2", 2)
	confirmVisibility(t, f, "del-f5", "vis-f5-1", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a2", Generation: 2}, now)

	if err := f.s.ApplyPrimaryRestart("del-f5", assignment.Principal{SessionID: "claude:a2", AgentID: "agent-a", Generation: 2}, "restart-1"); err != nil {
		t.Fatalf("first restart: %v", err)
	}
	rec := f.s.state.CoordinatorDelegations["del-f5"]
	var restartEntry DelegationHistoryEntry
	for _, h := range rec.History {
		if h.Kind == DelegationHistoryPrimaryRestart {
			restartEntry = h
		}
	}
	if restartEntry.ReceiptID != "loss-f5-1" {
		t.Fatalf("restart not bound to lossRecordID: %+v", restartEntry)
	}
	if restartEntry.ReceiptDig != "vis-f5-1" {
		t.Fatalf("restart not bound to visibility id: %+v", restartEntry)
	}
	if restartEntry.AssignmentID != "asg-f5" {
		t.Fatalf("restart not bound to assignment: %+v", restartEntry)
	}
	if restartEntry.To == nil || restartEntry.To.SessionID != "claude:a2" || restartEntry.To.Generation != 2 {
		t.Fatalf("restart not bound to replacement session/generation: %+v", restartEntry)
	}

	clearActing := func(t *testing.T, store *CanonicalStore, delID, sessionID string) {
		t.Helper()
		if err := store.UpdateCanonical(func(state *CanonicalState) error {
			r := state.CoordinatorDelegations[delID]
			acting := DelegationPrincipal{StableAgentID: "agent-a", SessionID: sessionID, Generation: 2}
			if !r.IsBarred(acting) {
				r.BarredCoordinators = append(r.BarredCoordinators, acting)
			}
			r.ActingCoordinator = nil
			state.CoordinatorDelegations[delID] = r
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("same_loss_second_application", func(t *testing.T) {
		registerSession(t, f, "claude:a3", "agent-a")
		bumpSessionGeneration(t, f, "claude:a3", 3)
		confirmVisibility(t, f, "del-f5", "vis-f5-2", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a3", Generation: 3}, now)
		clearActing(t, f.s, "del-f5", "claude:a2")
		if err := f.s.ApplyPrimaryRestart("del-f5", assignment.Principal{SessionID: "claude:a3", AgentID: "agent-a", Generation: 3}, "restart-2"); !errors.Is(err, ErrCoordinatorDelegationInvalid) {
			t.Fatalf("same loss second application err=%v", err)
		}
	})

	t.Run("same_loss_new_command_id", func(t *testing.T) {
		registerSession(t, f, "claude:a3b", "agent-a")
		bumpSessionGeneration(t, f, "claude:a3b", 3)
		confirmVisibility(t, f, "del-f5", "vis-f5-2b", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a3b", Generation: 3}, now)
		clearActing(t, f.s, "del-f5", "claude:a2")
		// Different evidence/command id must not re-apply the same lossRecordID.
		if err := f.s.ApplyPrimaryRestart("del-f5", assignment.Principal{SessionID: "claude:a3b", AgentID: "agent-a", Generation: 3}, "cmd-id-other"); !errors.Is(err, ErrCoordinatorDelegationInvalid) {
			t.Fatalf("same loss new command id err=%v", err)
		}
	})

	t.Run("same_loss_new_visibility_only", func(t *testing.T) {
		registerSession(t, f, "claude:a4", "agent-a")
		bumpSessionGeneration(t, f, "claude:a4", 3)
		confirmVisibility(t, f, "del-f5", "vis-f5-3", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a4", Generation: 3}, now)
		if err := f.s.ApplyPrimaryRestart("del-f5", assignment.Principal{SessionID: "claude:a4", AgentID: "agent-a", Generation: 3}, "restart-vis-only"); !errors.Is(err, ErrCoordinatorDelegationInvalid) {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("same_loss_new_session_only", func(t *testing.T) {
		registerSession(t, f, "claude:a5", "agent-a")
		bumpSessionGeneration(t, f, "claude:a5", 3)
		confirmVisibility(t, f, "del-f5", "vis-f5-4", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a5", Generation: 3}, now)
		if err := f.s.ApplyPrimaryRestart("del-f5", assignment.Principal{SessionID: "claude:a5", AgentID: "agent-a", Generation: 3}, "restart-sess-only"); !errors.Is(err, ErrCoordinatorDelegationInvalid) {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("receipt_bound_to_loss_assignment_generation_session", func(t *testing.T) {
		if restartEntry.ReceiptID != "loss-f5-1" || restartEntry.AssignmentID != "asg-f5" {
			t.Fatalf("receipt bind loss/assignment: %+v", restartEntry)
		}
		if restartEntry.From == nil || restartEntry.From.Generation != 1 || restartEntry.From.SessionID != "claude:a" {
			t.Fatalf("receipt bind from generation/session: %+v", restartEntry)
		}
		if restartEntry.To == nil || restartEntry.To.Generation != 2 || restartEntry.To.SessionID != "claude:a2" {
			t.Fatalf("receipt bind to generation/session: %+v", restartEntry)
		}
	})

	t.Run("new_loss_old_session", func(t *testing.T) {
		f2 := newOwnerCommandFixture(t)
		now2 := f2.s.now().UTC()
		grantDelegation(t, f2, "del-f5-os", now2)
		seedDelegatedAssignment(t, f2, "asg-f5-os", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now2)
		lossConfirm(t, f2, "del-f5-os", "loss-f5-os-1", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1}, now2)
		registerSession(t, f2, "claude:os2", "agent-a")
		bumpSessionGeneration(t, f2, "claude:os2", 2)
		confirmVisibility(t, f2, "del-f5-os", "vis-f5-os-1", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:os2", Generation: 2}, now2)
		if err := f2.s.ApplyPrimaryRestart("del-f5-os", assignment.Principal{SessionID: "claude:os2", AgentID: "agent-a", Generation: 2}, "restart-os1"); err != nil {
			t.Fatalf("first restart: %v", err)
		}
		seedDelegatedAssignment(t, f2, "asg-f5-os-2", assignment.Principal{SessionID: "claude:os2", AgentID: "agent-a", Generation: 2}, now2)
		lossConfirm(t, f2, "del-f5-os", "loss-f5-os-2", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:os2", Generation: 2}, now2)
		// Same lost session id, but a generation newer than every bound generation,
		// so only the session guard can reject.
		bumpSessionGeneration(t, f2, "claude:os2", 3)
		confirmVisibility(t, f2, "del-f5-os", "vis-f5-os-2", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:os2", Generation: 3}, now2)
		if err := f2.s.ApplyPrimaryRestart("del-f5-os", assignment.Principal{SessionID: "claude:os2", AgentID: "agent-a", Generation: 3}, "restart-old-sess"); err == nil {
			t.Fatal("new loss with old session accepted")
		}
	})

	t.Run("new_loss_old_generation", func(t *testing.T) {
		f2 := newOwnerCommandFixture(t)
		now2 := f2.s.now().UTC()
		grantDelegation(t, f2, "del-f5-og", now2)
		seedDelegatedAssignment(t, f2, "asg-f5-og", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now2)
		lossConfirm(t, f2, "del-f5-og", "loss-f5-og-1", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1}, now2)
		// Distinct fresh session. Generation stays 1, equal to the lost generation.
		// The session guard does not apply; only the generation rule rejects.
		registerSession(t, f2, "claude:og-fresh", "agent-a")
		confirmVisibility(t, f2, "del-f5-og", "vis-f5-og-1", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:og-fresh", Generation: 1}, now2)
		if err := f2.s.ApplyPrimaryRestart("del-f5-og", assignment.Principal{SessionID: "claude:og-fresh", AgentID: "agent-a", Generation: 1}, "restart-old-gen"); err == nil {
			t.Fatal("distinct session with old generation accepted")
		}
	})

	t.Run("new_loss_old_visibility_confirmation", func(t *testing.T) {
		f2 := newOwnerCommandFixture(t)
		now2 := f2.s.now().UTC()
		grantDelegation(t, f2, "del-f5-ov", now2)
		seedDelegatedAssignment(t, f2, "asg-f5-ov", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now2)
		lossConfirm(t, f2, "del-f5-ov", "loss-f5-ov-1", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1}, now2)
		registerSession(t, f2, "claude:ov2", "agent-a")
		bumpSessionGeneration(t, f2, "claude:ov2", 2)
		confirmVisibility(t, f2, "del-f5-ov", "vis-f5-ov-1", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:ov2", Generation: 2}, now2)
		if err := f2.s.ApplyPrimaryRestart("del-f5-ov", assignment.Principal{SessionID: "claude:ov2", AgentID: "agent-a", Generation: 2}, "restart-ov1"); err != nil {
			t.Fatalf("first restart: %v", err)
		}
		seedDelegatedAssignment(t, f2, "asg-f5-ov-2", assignment.Principal{SessionID: "claude:ov2", AgentID: "agent-a", Generation: 2}, now2)
		lossConfirm(t, f2, "del-f5-ov", "loss-f5-ov-2", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:ov2", Generation: 2}, now2)
		// Fresh session and a newer generation, but no new visibility.
		registerSession(t, f2, "claude:ov3", "agent-a")
		bumpSessionGeneration(t, f2, "claude:ov3", 3)
		if err := f2.s.ApplyPrimaryRestart("del-f5-ov", assignment.Principal{SessionID: "claude:ov3", AgentID: "agent-a", Generation: 3}, "restart-old-vis"); !errors.Is(err, ErrCoordinatorDelegationInvalid) {
			t.Fatalf("new loss with consumed visibility err=%v", err)
		}
	})

	t.Run("new_loss_fresh_inputs_pass", func(t *testing.T) {
		f2 := newOwnerCommandFixture(t)
		now2 := f2.s.now().UTC()
		grantDelegation(t, f2, "del-f5b", now2)
		seedDelegatedAssignment(t, f2, "asg-f5b", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now2)
		lossConfirm(t, f2, "del-f5b", "loss-f5b-1", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1}, now2)
		registerSession(t, f2, "claude:b2", "agent-a")
		bumpSessionGeneration(t, f2, "claude:b2", 2)
		confirmVisibility(t, f2, "del-f5b", "vis-f5b-1", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:b2", Generation: 2}, now2)
		if err := f2.s.ApplyPrimaryRestart("del-f5b", assignment.Principal{SessionID: "claude:b2", AgentID: "agent-a", Generation: 2}, "restart-b1"); err != nil {
			t.Fatalf("first restart: %v", err)
		}
		seedDelegatedAssignment(t, f2, "asg-f5b-2", assignment.Principal{SessionID: "claude:b2", AgentID: "agent-a", Generation: 2}, now2)
		lossConfirm(t, f2, "del-f5b", "loss-f5b-2", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:b2", Generation: 2}, now2)
		registerSession(t, f2, "claude:b3", "agent-a")
		bumpSessionGeneration(t, f2, "claude:b3", 3)
		confirmVisibility(t, f2, "del-f5b", "vis-f5b-2", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:b3", Generation: 3}, now2)
		if err := f2.s.ApplyPrimaryRestart("del-f5b", assignment.Principal{SessionID: "claude:b3", AgentID: "agent-a", Generation: 3}, "restart-b2"); err != nil {
			t.Fatalf("second distinct loss restart: %v", err)
		}
		rec := f2.s.state.CoordinatorDelegations["del-f5b"]
		bound := false
		for _, h := range rec.History {
			if h.Kind == DelegationHistoryPrimaryRestart && h.ReceiptID == "loss-f5b-2" {
				bound = true
				if h.To == nil || h.To.SessionID != "claude:b3" || h.To.Generation != 3 {
					t.Fatalf("To not bound to new session/generation: %+v", h)
				}
				if h.AssignmentID == "" {
					t.Fatalf("AssignmentID not bound: %+v", h)
				}
			}
		}
		if !bound {
			t.Fatal("second restart not bound to new lossRecordID")
		}
	})
}

// rejectPublicVoid pokes one invalid binding into a live OFFERED assignment,
// then calls ChangeAssignment. The assertion requires that public VOID's
// stable rejection text. Canonical bytes, events, and receipts stay unchanged.
func rejectPublicVoid(t *testing.T, actor assignment.Principal, poke func(*CanonicalState, string), want string) {
	t.Helper()
	f2 := newOwnerCommandFixture(t)
	now2 := f2.s.now().UTC()
	grantDelegation(t, f2, "del-a", now2)
	seedDelegatedAssignment(t, f2, "asg-seed", actor, now2)
	target := assignment.Principal{SessionID: "codex:worker", AgentID: "worker", Generation: 1}
	sha := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	id := "asg-open"
	offer := assignment.Record{
		AssignmentID: id, State: assignment.Offered,
		Issuer: actor.SessionID, IssuerAgentID: actor.AgentID, IssuerGeneration: actor.Generation,
		CurrentCoordinator: actor.SessionID, CoordinatorAgentID: actor.AgentID, CoordinatorGeneration: actor.Generation,
		HandoffEvidence: []assignment.Handoff{},
		Assignee:        target.SessionID, StableAgentID: target.AgentID, SessionID: target.SessionID, AssigneeGeneration: 1,
		Role: "IMPLEMENTER", SubjectSHA: sha,
		IssuedAt: now2.Add(-time.Minute), ValidUntil: now2.Add(time.Hour),
		AdmissionBasis:     assignment.AdmissionBasis{Kind: "POOL", Reference: "issue-93b", TargetSessionID: target.SessionID, TargetAgentID: target.AgentID, Role: "IMPLEMENTER", SubjectSHA: sha},
		VisibilityEvidence: assignment.VisibilityEvidence{Reference: "vis", SessionID: target.SessionID, ObservedAt: now2.Add(-time.Minute)},
		Revision:           1, UpdatedAt: now2.Add(-time.Minute),
	}
	if err := f2.s.OfferAssignment(actor, offer, "offer-open"); err != nil {
		t.Fatal(err)
	}
	f2.s.mu.Lock()
	poke(&f2.s.state, id)
	f2.s.mu.Unlock()
	before := ownerStateBytes(t, f2.s)
	nEvents := len(f2.s.state.AssignmentLedger.Events)
	rec := f2.s.state.AssignmentLedger.Records[id]
	err := f2.s.ChangeAssignment(actor, id, rec.Revision, "VOID", "probe")
	if err == nil || !strings.Contains(err.Error(), want) || !bytes.Equal(before, ownerStateBytes(t, f2.s)) || len(f2.s.state.AssignmentLedger.Events) != nEvents {
		t.Fatalf("want %q, err=%v", want, err)
	}
}

func TestF1_ExactGrantBinding(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-a", now)
	seedDelegatedAssignment(t, f, "asg-a", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now)
	actor := assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}

	t.Run("positive_exact_grant", func(t *testing.T) {
		target := assignment.Principal{SessionID: "codex:worker", AgentID: "worker", Generation: 1}
		sha := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		offer := assignment.Record{
			AssignmentID: "asg-pos", State: assignment.Offered,
			Issuer: actor.SessionID, IssuerAgentID: actor.AgentID, IssuerGeneration: 1,
			CurrentCoordinator: actor.SessionID, CoordinatorAgentID: actor.AgentID, CoordinatorGeneration: 1,
			HandoffEvidence: []assignment.Handoff{},
			Assignee:        target.SessionID, StableAgentID: target.AgentID, SessionID: target.SessionID, AssigneeGeneration: 1,
			Role: "IMPLEMENTER", SubjectSHA: sha,
			IssuedAt: now.Add(-time.Minute), ValidUntil: now.Add(time.Hour),
			AdmissionBasis:     assignment.AdmissionBasis{Kind: "POOL", Reference: "issue-93b", TargetSessionID: target.SessionID, TargetAgentID: target.AgentID, Role: "IMPLEMENTER", SubjectSHA: sha},
			VisibilityEvidence: assignment.VisibilityEvidence{Reference: "vis", SessionID: target.SessionID, ObservedAt: now.Add(-time.Minute)},
			Revision:           1, UpdatedAt: now.Add(-time.Minute),
		}
		if err := f.s.OfferAssignment(actor, offer, "offer-pos"); err != nil {
			t.Fatal(err)
		}
		rec := f.s.state.AssignmentLedger.Records["asg-pos"]
		b := rec.DelegationBinding
		if b == nil || b.GrantID != "del-a" || b.GrantGeneration != 1 || b.Repository != "naghuale/coord" || b.Project != "coord" || b.AllowedWorkItem != "issue-93b" {
			t.Fatalf("binding: %+v", b)
		}
		if err := f.s.ChangeAssignment(actor, "asg-pos", rec.Revision, "VOID", "ok"); err != nil {
			t.Fatal(err)
		}
		voided := f.s.state.AssignmentLedger.Records["asg-pos"]
		if voided.State != assignment.Void {
			t.Fatal("exact grant did not void")
		}
		ev := f.s.state.AssignmentLedger.Events[len(f.s.state.AssignmentLedger.Events)-1]
		if ev.AssignmentID != "asg-pos" || ev.Kind != "VOID" || ev.Revision != voided.Revision {
			t.Fatalf("void event: %+v", ev)
		}
	})

	t.Run("item_allowed_only_by_other_grant", func(t *testing.T) {
		rejectPublicVoid(t, actor, func(state *CanonicalState, id string) {
			other := state.CoordinatorDelegations["del-a"]
			other.DelegationID = "del-b"
			other.Scope.AllowedWorkItems = []string{"only-b"}
			state.CoordinatorDelegations["del-b"] = other
			r := state.AssignmentLedger.Records[id]
			r.AdmissionBasis.Reference = "only-b"
			b := *r.DelegationBinding
			b.AllowedWorkItem = "only-b"
			r.DelegationBinding = &b
			state.AssignmentLedger.Records[id] = r
		}, "outside allowedWorkItems")
	})

	t.Run("supplied_grant_b", func(t *testing.T) {
		f2 := newOwnerCommandFixture(t)
		now2 := f2.s.now().UTC()
		grantDelegation(t, f2, "del-a", now2)
		seedDelegatedAssignment(t, f2, "asg-a", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now2)
		rec := f2.s.state.AssignmentLedger.Records["asg-a"]
		before := ownerStateBytes(t, f2.s)
		err := f2.s.changeAssignment(actor, "asg-a", rec.Revision, "VOID", "claim-b", scopeClaim{
			grantID: "del-b", grantGeneration: 1, repository: "naghuale/coord", project: "coord", workItem: "issue-93b", set: true,
		})
		if err == nil || !bytes.Equal(before, ownerStateBytes(t, f2.s)) {
			t.Fatalf("supplied grant B err=%v", err)
		}
	})

	t.Run("grant_generation_mismatch", func(t *testing.T) {
		rejectPublicVoid(t, actor, func(state *CanonicalState, id string) {
			g := state.CoordinatorDelegations["del-a"]
			g.Generation = 2
			state.CoordinatorDelegations["del-a"] = g
			_ = id
		}, "generation mismatch")
	})

	t.Run("revoked_grant_replacement_ignored", func(t *testing.T) {
		f2 := newOwnerCommandFixture(t)
		now2 := f2.s.now().UTC()
		grantDelegation(t, f2, "del-a", now2)
		seedDelegatedAssignment(t, f2, "asg-a", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now2)
		rev, _ := json.Marshal(ownercredential.DelegationRevokePayload{DelegationID: "del-a", Reason: "rotate"})
		revCmd := f2.command(ownercredential.CommandDelegationRevoke, "revoke-a", "owner-primary", f2.s.state.OwnerCredentials["owner-1"].CredentialRevision, rev)
		sign := f2.s.state.OwnerCredentials["owner-1"].Credentials["owner-primary"].SignCount + 1
		if _, _, err := f2.s.ApplyOwnerCredentialCommand(revCmd.ChallengeID, revCmd, assertionFor(revCmd, f2.private["owner-primary"], sign)); err != nil {
			t.Fatal(err)
		}
		grantDelegation(t, f2, "del-b", now2)
		rec := f2.s.state.AssignmentLedger.Records["asg-a"]
		before := append([]byte(nil), mustJSON(t, rec)...)
		if err := f2.s.ChangeAssignment(actor, "asg-a", rec.Revision, "VOID", "via-replacement"); err == nil {
			t.Fatal("revoked grant accepted via replacement")
		}
		if !bytes.Equal(before, mustJSON(t, f2.s.state.AssignmentLedger.Records["asg-a"])) {
			t.Fatal("assignment bytes changed")
		}
	})

	t.Run("replay_substitutes_grant_id", func(t *testing.T) {
		f2 := newOwnerCommandFixture(t)
		now2 := f2.s.now().UTC()
		grantDelegation(t, f2, "del-a", now2)
		seedDelegatedAssignment(t, f2, "asg-a", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now2)
		payload, _ := json.Marshal(ownercredential.LossConfirmPayload{
			DelegationID: "del-a", GrantID: "del-b",
			Coordinator: ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1},
			Reason:      "lost", ObservedAt: now2,
		})
		cmd := f2.command(ownercredential.CommandLossConfirm, "loss-sub", "owner-primary", f2.s.state.OwnerCredentials["owner-1"].CredentialRevision, payload)
		sign := f2.s.state.OwnerCredentials["owner-1"].Credentials["owner-primary"].SignCount + 1
		before := ownerStateBytes(t, f2.s)
		assertOwnerCommandRejectedWithoutWrite(t, f2, cmd, assertionFor(cmd, f2.private["owner-primary"], sign), ErrCoordinatorDelegationInvalid)
		if !bytes.Equal(before, ownerStateBytes(t, f2.s)) {
			t.Fatal("replay substitution wrote state")
		}
	})

	t.Run("snapshot_substitutes_grant_binding", func(t *testing.T) {
		f2 := newOwnerCommandFixture(t)
		now2 := f2.s.now().UTC()
		grantDelegation(t, f2, "del-a", now2)
		seedDelegatedAssignment(t, f2, "asg-a", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now2)
		snap := f2.s.state
		ledger := *f2.s.state.AssignmentLedger
		records := map[string]assignment.Record{}
		for id, rec := range ledger.Records {
			records[id] = rec
		}
		rec := records["asg-a"]
		binding := *rec.DelegationBinding
		binding.GrantID = "del-b"
		rec.DelegationBinding = &binding
		records["asg-a"] = rec
		ledger.Records = records
		snap.AssignmentLedger = &ledger
		if err := ValidateCanonicalState(snap); err == nil {
			t.Fatal("substituted grant binding accepted")
		}
	})

	t.Run("missing_binding", func(t *testing.T) {
		rejectPublicVoid(t, actor, func(state *CanonicalState, id string) {
			r := state.AssignmentLedger.Records[id]
			r.DelegationBinding = nil
			state.AssignmentLedger.Records[id] = r
		}, "persisted assignment grant binding required")
	})

	t.Run("loss_batch_boundary", func(t *testing.T) {
		f2 := newOwnerCommandFixture(t)
		now2 := f2.s.now().UTC()
		grantDelegation(t, f2, "del-a", now2)
		seedDelegatedAssignment(t, f2, "asg-in", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now2)
		other := assignment.Principal{SessionID: "other:1", AgentID: "other-agent", Generation: 1}
		registerSession(t, f2, "other:1", "other-agent")
		seedDelegatedAssignment(t, f2, "asg-out", other, now2)
		beforeOut := mustJSON(t, f2.s.state.AssignmentLedger.Records["asg-out"])
		lossConfirm(t, f2, "del-a", "loss-batch", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1}, now2)
		if !f2.s.state.AssignmentLedger.Records["asg-in"].CoordinatorLost {
			t.Fatal("in-scope assignment not marked")
		}
		if !bytes.Equal(beforeOut, mustJSON(t, f2.s.state.AssignmentLedger.Records["asg-out"])) {
			t.Fatal("out-of-scope assignment bytes changed")
		}
		// Zero matches: a second loss of the same triple has no open in-scope work.
		payload, _ := json.Marshal(ownercredential.LossConfirmPayload{
			DelegationID: "del-a",
			Coordinator:  ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1},
			Reason:       "again", ObservedAt: now2,
		})
		cmd := f2.command(ownercredential.CommandLossConfirm, "loss-zero", "owner-primary", f2.s.state.OwnerCredentials["owner-1"].CredentialRevision, payload)
		sign := f2.s.state.OwnerCredentials["owner-1"].Credentials["owner-primary"].SignCount + 1
		before := ownerStateBytes(t, f2.s)
		assertOwnerCommandRejectedWithoutWrite(t, f2, cmd, assertionFor(cmd, f2.private["owner-primary"], sign), ErrCoordinatorDelegationInvalid)
		if !bytes.Equal(before, ownerStateBytes(t, f2.s)) {
			t.Fatal("zero-match loss wrote state")
		}
	})

	t.Run("loss_poison_rejects_whole_batch", func(t *testing.T) {
		f2 := newOwnerCommandFixture(t)
		now2 := f2.s.now().UTC()
		grantDelegation(t, f2, "del-a", now2)
		coord := assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}
		seedDelegatedAssignment(t, f2, "asg-ok", coord, now2)
		seedDelegatedAssignment(t, f2, "asg-bad", coord, now2)
		liveBefore := ownerStateBytes(t, f2.s)

		state := f2.s.state
		ledger := *f2.s.state.AssignmentLedger
		ledger.Events = append([]assignment.Event(nil), ledger.Events...)
		records := map[string]assignment.Record{}
		for id, rec := range ledger.Records {
			copied := rec
			if rec.DelegationBinding != nil {
				b := *rec.DelegationBinding
				copied.DelegationBinding = &b
			}
			records[id] = copied
		}
		bad := records["asg-bad"]
		bad.DelegationBinding.GrantGeneration = 9
		records["asg-bad"] = bad
		ledger.Records = records
		state.AssignmentLedger = &ledger
		dels := map[string]DelegationRecord{}
		for id, d := range f2.s.state.CoordinatorDelegations {
			dels[id] = d
		}
		state.CoordinatorDelegations = dels

		beforeOK := mustJSON(t, records["asg-ok"])
		beforeBad := mustJSON(t, records["asg-bad"])
		payload, _ := json.Marshal(ownercredential.LossConfirmPayload{
			DelegationID: "del-a",
			Coordinator:  ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1},
			Reason:       "lost", ObservedAt: now2,
		})
		err := applyLossConfirm(&state, ownercredential.Command{
			Kind: ownercredential.CommandLossConfirm, MessageID: "loss-poison",
			PrincipalID: "owner-primary", CredentialID: "owner-primary",
			Payload: payload, Digest: "digest-poison",
		}, now2)
		if err == nil || !strings.Contains(err.Error(), "grant binding invalid") {
			t.Fatalf("poisoned batch err=%v", err)
		}
		if !bytes.Equal(beforeOK, mustJSON(t, state.AssignmentLedger.Records["asg-ok"])) ||
			!bytes.Equal(beforeBad, mustJSON(t, state.AssignmentLedger.Records["asg-bad"])) {
			t.Fatal("poisoned batch wrote an assignment")
		}
		if !bytes.Equal(liveBefore, ownerStateBytes(t, f2.s)) {
			t.Fatal("live state changed")
		}
	})

	mutateBinding := func(set func(*assignment.DelegationBinding)) func(*CanonicalState, string) {
		return func(state *CanonicalState, id string) {
			r := state.AssignmentLedger.Records[id]
			b := *r.DelegationBinding
			set(&b)
			r.DelegationBinding = &b
			state.AssignmentLedger.Records[id] = r
		}
	}
	t.Run("repository_mismatch", func(t *testing.T) {
		rejectPublicVoid(t, actor, mutateBinding(func(b *assignment.DelegationBinding) { b.Repository = "other/repo" }), "repository/project mismatch")
	})
	t.Run("project_mismatch", func(t *testing.T) {
		rejectPublicVoid(t, actor, mutateBinding(func(b *assignment.DelegationBinding) { b.Project = "other" }), "repository/project mismatch")
	})
	t.Run("missing_persisted_repo", func(t *testing.T) {
		rejectPublicVoid(t, actor, mutateBinding(func(b *assignment.DelegationBinding) { b.Repository = "" }), "persisted assignment grant binding required")
	})
	t.Run("missing_persisted_project", func(t *testing.T) {
		rejectPublicVoid(t, actor, mutateBinding(func(b *assignment.DelegationBinding) { b.Project = "" }), "persisted assignment grant binding required")
	})

	rejectClaim := func(t *testing.T, claim scopeClaim) {
		t.Helper()
		f2 := newOwnerCommandFixture(t)
		now2 := f2.s.now().UTC()
		grantDelegation(t, f2, "del-a", now2)
		seedDelegatedAssignment(t, f2, "asg-a", actor, now2)
		rec := f2.s.state.AssignmentLedger.Records["asg-a"]
		before := ownerStateBytes(t, f2.s)
		err := f2.s.changeAssignment(actor, "asg-a", rec.Revision, "VOID", "claim", claim)
		if err == nil || !strings.Contains(err.Error(), "command grant binding differs") || !bytes.Equal(before, ownerStateBytes(t, f2.s)) {
			t.Fatalf("err=%v", err)
		}
	}
	t.Run("command_repo_differs", func(t *testing.T) {
		rejectClaim(t, scopeClaim{grantID: "del-a", grantGeneration: 1, repository: "other/repo", project: "coord", workItem: "issue-93b", set: true})
	})
	t.Run("command_project_differs", func(t *testing.T) {
		rejectClaim(t, scopeClaim{grantID: "del-a", grantGeneration: 1, repository: "naghuale/coord", project: "other", workItem: "issue-93b", set: true})
	})

	t.Run("replay_scope_substitution", func(t *testing.T) {
		f2 := newOwnerCommandFixture(t)
		now2 := f2.s.now().UTC()
		grantDelegation(t, f2, "del-a", now2)
		seedDelegatedAssignment(t, f2, "asg-a", actor, now2)
		payload, _ := json.Marshal(ownercredential.LossConfirmPayload{
			DelegationID: "del-a", Repository: "other/repo", Project: "coord",
			Coordinator: ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1},
			Reason:      "lost", ObservedAt: now2,
		})
		cmd := f2.command(ownercredential.CommandLossConfirm, "loss-scope", "owner-primary", f2.s.state.OwnerCredentials["owner-1"].CredentialRevision, payload)
		sign := f2.s.state.OwnerCredentials["owner-1"].Credentials["owner-primary"].SignCount + 1
		before := ownerStateBytes(t, f2.s)
		assertOwnerCommandRejectedWithoutWrite(t, f2, cmd, assertionFor(cmd, f2.private["owner-primary"], sign), ErrCoordinatorDelegationInvalid)
		if !bytes.Equal(before, ownerStateBytes(t, f2.s)) {
			t.Fatal("replay scope substitution wrote state")
		}
	})

	t.Run("snapshot_scope_substitution", func(t *testing.T) {
		f2 := newOwnerCommandFixture(t)
		now2 := f2.s.now().UTC()
		grantDelegation(t, f2, "del-a", now2)
		seedDelegatedAssignment(t, f2, "asg-a", actor, now2)
		snap := f2.s.state
		ledger := *snap.AssignmentLedger
		records := map[string]assignment.Record{}
		for id, rec := range ledger.Records {
			copied := rec
			if rec.DelegationBinding != nil {
				b := *rec.DelegationBinding
				copied.DelegationBinding = &b
			}
			records[id] = copied
		}
		rec := records["asg-a"]
		rec.DelegationBinding.Repository = "other/repo"
		records["asg-a"] = rec
		ledger.Records = records
		snap.AssignmentLedger = &ledger
		if err := ValidateCanonicalState(snap); err == nil {
			t.Fatal("substituted snapshot repository accepted")
		}
	})
}
