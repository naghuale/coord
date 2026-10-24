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
	confirmVisibility(t, f, "del-f5", "vis-f5-1", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a2", Generation: 1}, now)

	if err := f.s.ApplyPrimaryRestart("del-f5", assignment.Principal{SessionID: "claude:a2", AgentID: "agent-a", Generation: 1}, "restart-1"); err != nil {
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

	t.Run("same_loss_second_application", func(t *testing.T) {
		registerSession(t, f, "claude:a3", "agent-a")
		confirmVisibility(t, f, "del-f5", "vis-f5-2", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a3", Generation: 1}, now)
		// Acting is a2; bar it so dual-acting check passes, but same lossRecordID.
		if err := f.s.UpdateCanonical(func(state *CanonicalState) error {
			r := state.CoordinatorDelegations["del-f5"]
			acting := DelegationPrincipal{StableAgentID: "agent-a", SessionID: "claude:a2", Generation: 1}
			if !r.IsBarred(acting) {
				r.BarredCoordinators = append(r.BarredCoordinators, acting)
			}
			r.ActingCoordinator = nil
			state.CoordinatorDelegations["del-f5"] = r
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if err := f.s.ApplyPrimaryRestart("del-f5", assignment.Principal{SessionID: "claude:a3", AgentID: "agent-a", Generation: 1}, "restart-2"); !errors.Is(err, ErrCoordinatorDelegationInvalid) {
			t.Fatalf("same loss second application err=%v", err)
		}
	})

	t.Run("same_loss_new_visibility_only", func(t *testing.T) {
		registerSession(t, f, "claude:a4", "agent-a")
		confirmVisibility(t, f, "del-f5", "vis-f5-3", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a4", Generation: 1}, now)
		if err := f.s.ApplyPrimaryRestart("del-f5", assignment.Principal{SessionID: "claude:a4", AgentID: "agent-a", Generation: 1}, "restart-vis-only"); !errors.Is(err, ErrCoordinatorDelegationInvalid) {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("same_loss_new_session_only", func(t *testing.T) {
		registerSession(t, f, "claude:a5", "agent-a")
		// Reuse would need visibility; confirm then still fail on consumed loss.
		confirmVisibility(t, f, "del-f5", "vis-f5-4", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a5", Generation: 1}, now)
		if err := f.s.ApplyPrimaryRestart("del-f5", assignment.Principal{SessionID: "claude:a5", AgentID: "agent-a", Generation: 1}, "restart-sess-only"); !errors.Is(err, ErrCoordinatorDelegationInvalid) {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("new_loss_fresh_inputs_pass", func(t *testing.T) {
		// Fresh fixture: first restart, then a distinct second loss + restart.
		f2 := newOwnerCommandFixture(t)
		now2 := f2.s.now().UTC()
		grantDelegation(t, f2, "del-f5b", now2)
		seedDelegatedAssignment(t, f2, "asg-f5b", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now2)
		lossConfirm(t, f2, "del-f5b", "loss-f5b-1", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1}, now2)
		registerSession(t, f2, "claude:b2", "agent-a")
		confirmVisibility(t, f2, "del-f5b", "vis-f5b-1", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:b2", Generation: 1}, now2)
		if err := f2.s.ApplyPrimaryRestart("del-f5b", assignment.Principal{SessionID: "claude:b2", AgentID: "agent-a", Generation: 1}, "restart-b1"); err != nil {
			t.Fatalf("first restart: %v", err)
		}
		seedDelegatedAssignment(t, f2, "asg-f5b-2", assignment.Principal{SessionID: "claude:b2", AgentID: "agent-a", Generation: 1}, now2)
		lossConfirm(t, f2, "del-f5b", "loss-f5b-2", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:b2", Generation: 1}, now2)
		registerSession(t, f2, "claude:b3", "agent-a")
		confirmVisibility(t, f2, "del-f5b", "vis-f5b-2", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:b3", Generation: 1}, now2)
		if err := f2.s.ApplyPrimaryRestart("del-f5b", assignment.Principal{SessionID: "claude:b3", AgentID: "agent-a", Generation: 1}, "restart-b2"); err != nil {
			t.Fatalf("second distinct loss restart: %v", err)
		}
		rec := f2.s.state.CoordinatorDelegations["del-f5b"]
		bound := false
		for _, h := range rec.History {
			if h.Kind == DelegationHistoryPrimaryRestart && h.ReceiptID == "loss-f5b-2" {
				bound = true
				if h.To == nil || h.To.SessionID != "claude:b3" {
					t.Fatalf("To not bound to new session: %+v", h)
				}
			}
		}
		if !bound {
			t.Fatal("second restart not bound to new lossRecordID")
		}
	})
}
