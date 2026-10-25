package store

import (
	"bytes"
	"coord/assignment"
	"coord/ownercredential"
	"coord/task"
	"coord/taskid"
	"encoding/json"
	"errors"
	"reflect"
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

	outID := "asg-f1-out"
	coord := assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}
	target := assignment.Principal{SessionID: "codex:worker", AgentID: "worker", Generation: 1}
	sha := "cccccccccccccccccccccccccccccccccccccccc"
	rOut := assignment.Record{
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
	pokeAssignmentRecord(t, f, rOut)
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
	pokeAssignmentRecord(t, f, offered)
	lossPayload, _ := json.Marshal(ownercredential.LossConfirmPayload{
		DelegationID:  "del-f1-loss",
		Coordinator:   ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1},
		AssignmentIDs: []string{"asg-f1-oos"},
		Reason:        "hung", ObservedAt: now,
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

	t.Run("same_loss_second_application", func(t *testing.T) {
		grant := f.s.state.CoordinatorDelegations["del-f5"]
		if !grant.primaryRestartConsumedLoss("loss-f5-1") {
			t.Fatal("lossRecordID was not marked consumed by the committed restart")
		}
		if _, ok := grant.latestUnconsumedLoss(); ok {
			t.Fatal("a consumed loss remains available for another primaryRestart")
		}
	})

	t.Run("same_loss_new_command_id", func(t *testing.T) {
		grant := f.s.state.CoordinatorDelegations["del-f5"]
		count := 0
		for _, h := range grant.History {
			if h.Kind == DelegationHistoryPrimaryRestart && h.ReceiptID == "loss-f5-1" {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("same loss has %d restart records, want exactly one", count)
		}
	})

	t.Run("same_loss_new_visibility_only", func(t *testing.T) {
		grant := f.s.state.CoordinatorDelegations["del-f5"]
		if grant.primaryRestartConsumedLoss("loss-f5-1") == false {
			t.Fatal("new visibility must not unconsume the prior loss")
		}
	})

	t.Run("same_loss_new_session_only", func(t *testing.T) {
		grant := f.s.state.CoordinatorDelegations["del-f5"]
		if _, ok := grant.latestUnconsumedLoss(); ok {
			t.Fatal("a new session alone must not create a new loss")
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

func pokeAssignmentRecord(t *testing.T, f *ownerCommandFixture, r assignment.Record) {
	t.Helper()
	f.s.mu.Lock()
	defer f.s.mu.Unlock()
	if f.s.state.AssignmentLedger == nil {
		f.s.state.AssignmentLedger = assignment.Empty()
	}
	f.s.state.AssignmentLedger.Records[r.AssignmentID] = r
	f.s.state.AssignmentLedger.Events = append(f.s.state.AssignmentLedger.Events, assignment.Event{
		AssignmentID: r.AssignmentID, Revision: r.Revision, Kind: "OFFER",
		Actor: r.Coordinator(), EvidenceRef: "poke", At: r.UpdatedAt, Record: r,
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

	for _, tc := range []struct {
		name string
		edit func(*assignment.DelegationBinding)
	}{
		{"offer_claim_grant_id", func(b *assignment.DelegationBinding) { b.GrantID = "del-b" }},
		{"offer_claim_generation", func(b *assignment.DelegationBinding) { b.GrantGeneration = 2 }},
		{"offer_claim_repository", func(b *assignment.DelegationBinding) { b.Repository = "other/repo" }},
		{"offer_claim_project", func(b *assignment.DelegationBinding) { b.Project = "other-project" }},
		{"offer_claim_work_item", func(b *assignment.DelegationBinding) { b.AllowedWorkItem = "issue-outside-grant" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f2 := newOwnerCommandFixture(t)
			now2 := f2.s.now().UTC()
			grantDelegation(t, f2, "del-a", now2)
			registerSession(t, f2, actor.SessionID, actor.AgentID)
			target := assignment.Principal{SessionID: "codex:worker", AgentID: "worker", Generation: 1}
			registerSession(t, f2, target.SessionID, target.AgentID)
			sha := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
			offer := assignment.Record{
				AssignmentID: "asg-claim", State: assignment.Offered,
				Issuer: actor.SessionID, IssuerAgentID: actor.AgentID, IssuerGeneration: 1,
				CurrentCoordinator: actor.SessionID, CoordinatorAgentID: actor.AgentID, CoordinatorGeneration: 1,
				HandoffEvidence: []assignment.Handoff{}, Assignee: target.SessionID, StableAgentID: target.AgentID,
				SessionID: target.SessionID, AssigneeGeneration: 1, Role: "IMPLEMENTER", SubjectSHA: sha,
				IssuedAt: now2.Add(-time.Minute), ValidUntil: now2.Add(time.Hour),
				AdmissionBasis:     assignment.AdmissionBasis{Kind: "POOL", Reference: "issue-93b", TargetSessionID: target.SessionID, TargetAgentID: target.AgentID, Role: "IMPLEMENTER", SubjectSHA: sha},
				VisibilityEvidence: assignment.VisibilityEvidence{Reference: "vis", SessionID: target.SessionID, ObservedAt: now2.Add(-time.Minute)},
				Revision:           1, UpdatedAt: now2.Add(-time.Minute),
				DelegationBinding: &assignment.DelegationBinding{GrantID: "del-a", GrantGeneration: 1, Repository: "naghuale/coord", Project: "coord", AllowedWorkItem: "issue-93b"},
			}
			tc.edit(offer.DelegationBinding)
			before := ownerStateBytes(t, f2.s)
			saves := f2.p.saveCalls
			if err := f2.s.OfferAssignment(actor, offer, "caller-claim"); !errors.Is(err, ErrCoordinatorDelegationInvalid) {
				t.Fatalf("mismatched caller binding err=%v", err)
			}
			if f2.p.saveCalls != saves || !bytes.Equal(before, ownerStateBytes(t, f2.s)) {
				t.Fatal("rejected caller binding changed canonical state")
			}
		})
	}

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

	for _, tc := range []struct {
		name string
		edit func(*assignment.DelegationBinding)
	}{
		{"grant_generation", func(b *assignment.DelegationBinding) { b.GrantGeneration++ }},
		{"repository", func(b *assignment.DelegationBinding) { b.Repository = "other/repository" }},
		{"project", func(b *assignment.DelegationBinding) { b.Project = "other-project" }},
		{"work_item", func(b *assignment.DelegationBinding) { b.AllowedWorkItem = "other-work-item" }},
	} {
		t.Run("snapshot_substitutes_"+tc.name, func(t *testing.T) {
			f2 := newOwnerCommandFixture(t)
			now2 := f2.s.now().UTC()
			grantDelegation(t, f2, "del-a", now2)
			seedDelegatedAssignment(t, f2, "asg-a", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now2)
			snapshot, err := copyCanonicalState(f2.s.state)
			if err != nil {
				t.Fatal(err)
			}
			if err := ValidateCanonicalState(snapshot); err != nil {
				t.Fatalf("positive snapshot control rejected: %v", err)
			}
			ledger := *snapshot.AssignmentLedger
			ledger.Records = make(map[string]assignment.Record, len(snapshot.AssignmentLedger.Records))
			for id, record := range snapshot.AssignmentLedger.Records {
				if record.DelegationBinding != nil {
					binding := *record.DelegationBinding
					tc.edit(&binding)
					record.DelegationBinding = &binding
				}
				ledger.Records[id] = record
			}
			ledger.Events = append([]assignment.Event(nil), snapshot.AssignmentLedger.Events...)
			for i := range ledger.Events {
				if ledger.Events[i].Record.DelegationBinding != nil {
					binding := *ledger.Events[i].Record.DelegationBinding
					tc.edit(&binding)
					ledger.Events[i].Record.DelegationBinding = &binding
				}
			}
			snapshot.AssignmentLedger = &ledger
			if err := ValidateCanonicalState(snapshot); !errors.Is(err, ErrCoordinatorDelegationInvalid) {
				t.Fatalf("modified snapshot binding err=%v, want delegation invalid", err)
			}
			if _, err := NewCanonical(&canonicalMemory{state: snapshot}); !errors.Is(err, ErrCoordinatorDelegationInvalid) {
				t.Fatalf("NewCanonical modified snapshot binding err=%v, want delegation invalid", err)
			}
		})
	}

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

func TestRecoveryA93B1_LossReceiptAndEventsShareExactCommit(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-recovery-bind", now)
	seedDelegatedAssignment(t, f, "asg-recovery-bind", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now)
	lossConfirm(t, f, "del-recovery-bind", "loss-recovery-bind", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1}, now)

	state := f.s.state
	event := state.AssignmentLedger.Events[len(state.AssignmentLedger.Events)-1]
	receipt := state.OwnerCredentials["owner-1"].Receipts["loss-recovery-bind"]
	binding := receipt.OwnerLossBinding
	if binding == nil || receipt.CanonicalRevision != state.Revision || event.OwnerCommitRevision != state.Revision || event.OwnerDelegationID != binding.DelegationID || event.OwnerGrantGeneration != binding.GrantGeneration || binding.DelegationID != "del-recovery-bind" || binding.Generation != 1 || binding.SessionID != "claude:a" || binding.StableAgentID != "agent-a" || !reflect.DeepEqual(binding.AssignmentIDs, []string{"asg-recovery-bind"}) {
		t.Fatalf("receipt/event commit binding mismatch: receipt=%+v event=%+v canonicalRevision=%d", receipt, event, state.Revision)
	}
	if err := ValidateCanonicalState(state); err != nil {
		t.Fatalf("valid committed loss rejected: %v", err)
	}
}

func TestRecoveryA93B1_SignedAssignmentSetMustMatchLossEffect(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-signed-set", now)
	seedDelegatedAssignment(t, f, "asg-signed-set", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now)
	payload, err := json.Marshal(ownercredential.LossConfirmPayload{
		DelegationID:  "del-signed-set",
		Coordinator:   ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1},
		AssignmentIDs: []string{"different-assignment"}, Reason: "lost", ObservedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	cmd := f.command(ownercredential.CommandLossConfirm, "loss-signed-set-bad", "owner-primary", f.s.state.OwnerCredentials["owner-1"].CredentialRevision, payload)
	sign := f.s.state.OwnerCredentials["owner-1"].Credentials["owner-primary"].SignCount + 1
	before := ownerStateBytes(t, f.s)
	saves := f.p.saveCalls
	if _, _, err := f.s.ApplyOwnerCredentialCommand(cmd.ChallengeID, cmd, assertionFor(cmd, f.private["owner-primary"], sign)); !errors.Is(err, ErrCoordinatorDelegationInvalid) {
		t.Fatalf("signed assignment substitution err=%v", err)
	}
	if f.p.saveCalls != saves || !bytes.Equal(before, ownerStateBytes(t, f.s)) {
		t.Fatal("mismatched signed assignment set wrote state")
	}

	f2 := newOwnerCommandFixture(t)
	now2 := f2.s.now().UTC()
	grantDelegation(t, f2, "del-signed-set", now2)
	seedDelegatedAssignment(t, f2, "asg-signed-set", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now2)
	lossConfirm(t, f2, "del-signed-set", "loss-signed-set-good", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1}, now2)
	receipt := f2.s.state.OwnerCredentials["owner-1"].Receipts["loss-signed-set-good"]
	if receipt.OwnerLossBinding == nil || !reflect.DeepEqual(receipt.OwnerLossBinding.AssignmentIDs, []string{"asg-signed-set"}) {
		t.Fatalf("positive signed assignment binding: %+v", receipt.OwnerLossBinding)
	}
}

func TestRecoveryA93B3_LossConfirmRejectsUnknownExpiryWithoutWrite(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-unknown-loss-field", now)
	seedDelegatedAssignment(t, f, "asg-unknown-loss-field", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now)
	payload := []byte(`{"delegationId":"del-unknown-loss-field","coordinator":{"stableAgentId":"agent-a","sessionId":"claude:a","generation":1},"assignmentIds":["asg-unknown-loss-field"],"reason":"lost","observedAt":"` + now.Format(time.RFC3339Nano) + `","expiresAt":"` + now.Add(24*time.Hour).Format(time.RFC3339Nano) + `"}`)
	cmd := f.command(ownercredential.CommandLossConfirm, "loss-unknown-expiry", "owner-primary", f.s.state.OwnerCredentials["owner-1"].CredentialRevision, payload)
	sign := f.s.state.OwnerCredentials["owner-1"].Credentials["owner-primary"].SignCount + 1
	before := ownerStateBytes(t, f.s)
	saves := f.p.saveCalls
	if _, _, err := f.s.ApplyOwnerCredentialCommand(cmd.ChallengeID, cmd, assertionFor(cmd, f.private["owner-primary"], sign)); !errors.Is(err, ErrCoordinatorDelegationInvalid) {
		t.Fatalf("unknown expiry extension field err=%v", err)
	}
	if f.p.saveCalls != saves || !bytes.Equal(before, ownerStateBytes(t, f.s)) {
		t.Fatal("unknown expiry extension field changed canonical state or wrote a receipt")
	}
}

func TestRecoveryA93B3_DuplicateOwnerLossCannotReuseOpenAssignment(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-duplicate-loss", now)
	seedDelegatedAssignment(t, f, "asg-duplicate-loss", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now)
	coord := ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1}
	lossConfirm(t, f, "del-duplicate-loss", "loss-duplicate-first", coord, now)
	payload, err := json.Marshal(ownercredential.LossConfirmPayload{
		DelegationID: "del-duplicate-loss", Coordinator: coord,
		AssignmentIDs: []string{"asg-duplicate-loss"}, Reason: "duplicate", ObservedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	cmd := f.command(ownercredential.CommandLossConfirm, "loss-duplicate-second", "owner-primary", f.s.state.OwnerCredentials["owner-1"].CredentialRevision, payload)
	sign := f.s.state.OwnerCredentials["owner-1"].Credentials["owner-primary"].SignCount + 1
	before := ownerStateBytes(t, f.s)
	saves := f.p.saveCalls
	if _, _, err := f.s.ApplyOwnerCredentialCommand(cmd.ChallengeID, cmd, assertionFor(cmd, f.private["owner-primary"], sign)); !errors.Is(err, ErrCoordinatorDelegationInvalid) {
		t.Fatalf("duplicate loss for an already-lost assignment err=%v", err)
	}
	if f.p.saveCalls != saves || !bytes.Equal(before, ownerStateBytes(t, f.s)) {
		t.Fatal("duplicate loss changed state or wrote a receipt")
	}
}

func TestRecoveryA93B1_GenericWriterCannotReuseOwnerLossReceipt(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-recovery-reuse", now)
	seedDelegatedAssignment(t, f, "asg-recovery-reuse", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now)
	lossConfirm(t, f, "del-recovery-reuse", "loss-recovery-reuse", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1}, now)
	before := ownerStateBytes(t, f.s)
	saves := f.p.saveCalls
	err := f.s.UpdateCanonical(func(state *CanonicalState) error {
		// Model a forged second loss using the old signed receipt. This direct
		// event insertion deliberately bypasses Ledger.Append so the writer gate
		// itself must reject the authority-bearing event.
		prior := state.AssignmentLedger.Events[len(state.AssignmentLedger.Events)-1]
		prior.AssignmentID = "forged-second-assignment"
		prior.Record.AssignmentID = prior.AssignmentID
		state.AssignmentLedger.Events = append(state.AssignmentLedger.Events, prior)
		return nil
	})
	if !errors.Is(err, ErrOwnerCommandRequired) || f.p.saveCalls != saves || !bytes.Equal(before, ownerStateBytes(t, f.s)) {
		t.Fatalf("generic receipt reuse err=%v saves=%d->%d stateChanged=%v", err, saves, f.p.saveCalls, !bytes.Equal(before, ownerStateBytes(t, f.s)))
	}
}

func TestRecoveryA93B1_InboxWriterCannotAppendOwnerCredentialLoss(t *testing.T) {
	p := &canonicalMemory{state: taskStateWithRun()}
	s, err := NewCanonical(p)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	id := taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	before, err := copyCanonicalState(s.state)
	if err != nil {
		t.Fatal(err)
	}
	saves := p.saveCalls
	_, _, err = s.ApplyInboxCommand("agent", "message-owner-loss", id, 2, func(state *CanonicalState) (InboxReceipt, error) {
		if state.AssignmentLedger == nil {
			state.AssignmentLedger = assignment.Empty()
		}
		state.AssignmentLedger.Events = append(state.AssignmentLedger.Events, assignment.Event{Kind: "COORDINATOR_LOST", ActorSource: assignment.ActorSourceOwnerCredential})
		return InboxReceipt{}, nil
	})
	if !errors.Is(err, ErrOwnerCommandRequired) {
		t.Fatalf("inbox owner loss err=%v", err)
	}
	if p.saveCalls != saves || !reflect.DeepEqual(before, s.state) {
		t.Fatal("rejected inbox owner loss changed state or saved")
	}
}

func TestRecoveryA93B1_SnapshotRejectsReceiptContextSubstitution(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*CanonicalState)
	}{
		{"commit_revision", func(s *CanonicalState) {
			s.AssignmentLedger.Events[len(s.AssignmentLedger.Events)-1].OwnerCommitRevision--
		}},
		{"delegation", func(s *CanonicalState) {
			s.AssignmentLedger.Events[len(s.AssignmentLedger.Events)-1].OwnerDelegationID = "other-grant"
		}},
		{"coordinator_generation", func(s *CanonicalState) {
			e := &s.AssignmentLedger.Events[len(s.AssignmentLedger.Events)-1]
			e.Record.CoordinatorGeneration++
			e.OwnerGrantGeneration++
		}},
		{"receipt_stable_agent", func(s *CanonicalState) {
			s.OwnerCredentials["owner-1"].Receipts["loss-recovery-snapshot"].OwnerLossBinding.StableAgentID = "other-agent"
		}},
		{"receipt_session", func(s *CanonicalState) {
			s.OwnerCredentials["owner-1"].Receipts["loss-recovery-snapshot"].OwnerLossBinding.SessionID = "other:session"
		}},
		{"receipt_generation", func(s *CanonicalState) {
			s.OwnerCredentials["owner-1"].Receipts["loss-recovery-snapshot"].OwnerLossBinding.Generation++
		}},
		{"receipt_grant_generation", func(s *CanonicalState) {
			s.OwnerCredentials["owner-1"].Receipts["loss-recovery-snapshot"].OwnerLossBinding.GrantGeneration++
		}},
		{"receipt_assignment_set", func(s *CanonicalState) {
			s.OwnerCredentials["owner-1"].Receipts["loss-recovery-snapshot"].OwnerLossBinding.AssignmentIDs = []string{"unrelated"}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			enableDelegationHarness(t)
			f := newOwnerCommandFixture(t)
			now := f.s.now().UTC()
			grantDelegation(t, f, "del-recovery-snapshot", now)
			seedDelegatedAssignment(t, f, "asg-recovery-snapshot", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now)
			lossConfirm(t, f, "del-recovery-snapshot", "loss-recovery-snapshot", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1}, now)
			snapshot, err := copyCanonicalState(f.s.state)
			if err != nil {
				t.Fatal(err)
			}
			if err := ValidateCanonicalState(snapshot); err != nil {
				t.Fatalf("positive OwnerCredential receipt snapshot control invalid: %v", err)
			}
			tc.mutate(&snapshot)
			if err := ValidateCanonicalState(snapshot); err == nil {
				t.Fatal("substituted loss context accepted")
			}
		})
	}
}

func TestRecoveryA93B2_SnapshotControlStateIsDerivedFromHistory(t *testing.T) {
	for _, name := range []string{"foreign_acting_coordinator", "lost_coordinator_removed_from_bar", "consumed_visibility_reset", "primary_restart_history_removed", "grant_without_owner_receipt", "revocation_removed"} {
		t.Run(name, func(t *testing.T) {
			enableDelegationHarness(t)
			f := newOwnerCommandFixture(t)
			now := f.s.now().UTC()
			grantDelegation(t, f, "del-derived", now)
			if name != "foreign_acting_coordinator" && name != "grant_without_owner_receipt" {
				seedDelegatedAssignment(t, f, "asg-derived", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now)
			}
			if name == "lost_coordinator_removed_from_bar" || name == "consumed_visibility_reset" || name == "primary_restart_history_removed" {
				lossConfirm(t, f, "del-derived", "loss-derived", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1}, now)
			}
			if name == "consumed_visibility_reset" || name == "primary_restart_history_removed" {
				registerSession(t, f, "claude:a2", "agent-a")
				bumpSessionGeneration(t, f, "claude:a2", 2)
				confirmVisibility(t, f, "del-derived", "vis-derived", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a2", Generation: 2}, now)
				if err := f.s.ApplyPrimaryRestart("del-derived", assignment.Principal{SessionID: "claude:a2", AgentID: "agent-a", Generation: 2}, "restart-derived"); err != nil {
					t.Fatalf("valid restart setup: %v", err)
				}
			}
			if name == "revocation_removed" {
				payload, _ := json.Marshal(ownercredential.DelegationRevokePayload{DelegationID: "del-derived", Reason: "revoke"})
				rev := f.s.state.OwnerCredentials["owner-1"].CredentialRevision
				cmd := f.command(ownercredential.CommandDelegationRevoke, "revoke-derived", "owner-primary", rev, payload)
				sign := f.s.state.OwnerCredentials["owner-1"].Credentials["owner-primary"].SignCount + 1
				if _, _, err := f.s.ApplyOwnerCredentialCommand(cmd.ChallengeID, cmd, assertionFor(cmd, f.private["owner-primary"], sign)); err != nil {
					t.Fatalf("valid revoke setup: %v", err)
				}
			}
			snapshot, err := copyCanonicalState(f.s.state)
			if err != nil {
				t.Fatal(err)
			}
			if err := ValidateCanonicalState(snapshot); err != nil {
				t.Fatalf("positive derived-state snapshot control invalid: %v", err)
			}
			d := snapshot.CoordinatorDelegations["del-derived"]
			switch name {
			case "foreign_acting_coordinator":
				outsider := DelegationPrincipal{StableAgentID: "intruder", SessionID: "foreign:1", Generation: 1}
				d.ActingCoordinator = &outsider
			case "lost_coordinator_removed_from_bar":
				d.BarredCoordinators = nil
			case "consumed_visibility_reset":
				for i := range d.VisibilityReceipts {
					d.VisibilityReceipts[i].ConsumedAt = time.Time{}
					d.VisibilityReceipts[i].ConsumedBy = ""
				}
			case "primary_restart_history_removed":
				filtered := d.History[:0]
				for _, h := range d.History {
					if h.Kind != DelegationHistoryPrimaryRestart {
						filtered = append(filtered, h)
					}
				}
				d.History = filtered
			case "grant_without_owner_receipt":
				agg := snapshot.OwnerCredentials["owner-1"]
				delete(agg.Receipts, "grant-del-derived")
				snapshot.OwnerCredentials["owner-1"] = agg
			case "revocation_removed":
				d.RevokedAt = time.Time{}
				d.RevokedBy = ""
			}
			snapshot.CoordinatorDelegations["del-derived"] = d
			if err := ValidateCanonicalState(snapshot); err == nil {
				t.Fatal("snapshot state not derived from history was accepted")
			}
			if loaded, err := NewCanonical(&canonicalMemory{state: snapshot}); err == nil {
				loaded.Close()
				t.Fatal("NewCanonical loaded snapshot state not derived from history")
			}
		})
	}
}

func TestRecoveryA93B2_SnapshotRejectsDelegationEnvelopeDrift(t *testing.T) {
	for _, name := range []string{"active_scope_overlap", "map_key_mismatch", "delegations_in_old_schema", "missing_assignment_binding"} {
		t.Run(name, func(t *testing.T) {
			enableDelegationHarness(t)
			f := newOwnerCommandFixture(t)
			now := f.s.now().UTC()
			grantDelegation(t, f, "del-envelope-a", now)
			if name == "active_scope_overlap" {
				grantDelegationWith(t, f, "del-envelope-b", now, delegationOpts{project: "coord-secondary"})
			}
			if name == "missing_assignment_binding" {
				seedDelegatedAssignment(t, f, "asg-envelope", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now)
			}
			snapshot, err := copyCanonicalState(f.s.state)
			if err != nil {
				t.Fatal(err)
			}
			if err := ValidateCanonicalState(snapshot); err != nil {
				t.Fatalf("positive snapshot control invalid: %v", err)
			}
			switch name {
			case "active_scope_overlap":
				d := snapshot.CoordinatorDelegations["del-envelope-b"]
				d.Scope.Project = "coord"
				snapshot.CoordinatorDelegations[d.DelegationID] = d
			case "map_key_mismatch":
				d := snapshot.CoordinatorDelegations["del-envelope-a"]
				delete(snapshot.CoordinatorDelegations, d.DelegationID)
				snapshot.CoordinatorDelegations["wrong-key"] = d
			case "delegations_in_old_schema":
				snapshot.SchemaVersion = 5
			case "missing_assignment_binding":
				ledger := *snapshot.AssignmentLedger
				ledger.Records = make(map[string]assignment.Record, len(snapshot.AssignmentLedger.Records))
				for id, record := range snapshot.AssignmentLedger.Records {
					record.DelegationBinding = nil
					ledger.Records[id] = record
				}
				ledger.Events = append([]assignment.Event(nil), snapshot.AssignmentLedger.Events...)
				for i := range ledger.Events {
					ledger.Events[i].Record.DelegationBinding = nil
				}
				snapshot.AssignmentLedger = &ledger
			}
			if err := ValidateCanonicalState(snapshot); err == nil {
				t.Fatal("invalid delegation envelope accepted")
			}
			if loaded, err := NewCanonical(&canonicalMemory{state: snapshot}); err == nil {
				loaded.Close()
				t.Fatal("NewCanonical loaded invalid delegation envelope")
			}
		})
	}
}

func TestRecoveryA93B4_DisabledOfferRejectsDelegationBindingClaim(t *testing.T) {
	if coordinatorDelegationActive() {
		t.Fatal("test requires production delegation disabled")
	}
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	actor := assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}
	seedDelegatedAssignment(t, f, "asg-disabled-seed", actor, now)
	r := f.s.state.AssignmentLedger.Records["asg-disabled-seed"]
	r.AssignmentID = "asg-disabled-claim"
	r.State = assignment.Offered
	r.Revision = 1
	r.AcknowledgedAt = time.Time{}
	r.WorkStartedAt = time.Time{}
	r.AcceptedResult = nil
	r.DelegationBinding = &assignment.DelegationBinding{GrantID: "invented-grant", GrantGeneration: 1, Repository: "r", Project: "p", AllowedWorkItem: "issue-93b"}
	r.UpdatedAt = now.Add(-time.Minute)
	before := ownerStateBytes(t, f.s)
	if err := f.s.OfferAssignment(actor, r, "forged-binding"); !errors.Is(err, ErrCoordinatorDelegationDisabled) {
		t.Fatalf("offer err=%v", err)
	}
	if !bytes.Equal(before, ownerStateBytes(t, f.s)) {
		t.Fatal("disabled offer persisted claimed delegation binding")
	}
}

func TestF8_GenericWriterCannotWidenActiveGrant(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-f8", now)
	seedDelegatedAssignment(t, f, "asg-f8", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now)
	before := ownerStateBytes(t, f.s)
	err := f.s.UpdateCanonical(func(state *CanonicalState) error {
		rec := state.CoordinatorDelegations["del-f8"]
		rec.Scope.AllowedWorkItems = append(append([]string{}, rec.Scope.AllowedWorkItems...), "issue-outside-original-grant")
		state.CoordinatorDelegations["del-f8"] = rec
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "grant authority is immutable via generic writer") || !bytes.Equal(before, ownerStateBytes(t, f.s)) {
		t.Fatalf("generic widen err=%v", err)
	}
}

func TestF9_SnapshotRejectsOutOfScopeWorkItem(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-f9", now)
	seedDelegatedAssignment(t, f, "asg-f9", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now)
	snap := f.s.state
	ledger := *snap.AssignmentLedger
	records := map[string]assignment.Record{}
	events := make([]assignment.Event, len(ledger.Events))
	copy(events, ledger.Events)
	for id, rec := range ledger.Records {
		copied := rec
		if rec.DelegationBinding != nil {
			b := *rec.DelegationBinding
			copied.DelegationBinding = &b
		}
		records[id] = copied
	}
	rec := records["asg-f9"]
	rec.AdmissionBasis.Reference = "other-issue"
	b := *rec.DelegationBinding
	b.AllowedWorkItem = "other-issue"
	rec.DelegationBinding = &b
	records["asg-f9"] = rec
	for i, ev := range events {
		if ev.AssignmentID != "asg-f9" {
			continue
		}
		er := ev.Record
		er.AdmissionBasis.Reference = "other-issue"
		eb := *er.DelegationBinding
		eb.AllowedWorkItem = "other-issue"
		er.DelegationBinding = &eb
		ev.Record = er
		events[i] = ev
	}
	ledger.Records = records
	ledger.Events = events
	snap.AssignmentLedger = &ledger
	err := ValidateCanonicalState(snap)
	if err == nil || !strings.Contains(err.Error(), "outside allowedWorkItems") {
		t.Fatalf("out-of-scope snapshot err=%v", err)
	}
}

func TestF10_OfferBindsExplicitGrantWhenActorHasTwo(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-a", now)
	payload, _ := json.Marshal(ownercredential.DelegationGrantPayload{
		DelegationID: "del-b",
		Scope: ownercredential.DelegationScopePayload{
			Repository: "other/repo", Project: "other",
			AllowedWorkItems: []string{"issue-93b"}, AllowedActions: []string{"OFFER", "VOID", "EXPIRE", "HANDOFF"},
		},
		PrimaryCoordinator: ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1},
		Validity:           ownercredential.DelegationValidityPayload{NotBefore: now.Add(-time.Minute), ExpiresAt: now.Add(2 * time.Hour)},
		Successors:         []ownercredential.DelegationPrincipalPayload{{StableAgentID: "agent-b", SessionID: "codex:b", Generation: 1}},
		Evidence:           ownercredential.DelegationEvidencePayload{OwnerDecisionRef: "ADR-0010", VisibilityEvidence: "owner-seen"},
	})
	rev := f.s.state.OwnerCredentials["owner-1"].CredentialRevision
	cmd := f.command(ownercredential.CommandDelegationGrant, "grant-del-b", "owner-primary", rev, payload)
	sign := f.s.state.OwnerCredentials["owner-1"].Credentials["owner-primary"].SignCount + 1
	if _, _, err := f.s.ApplyOwnerCredentialCommand(cmd.ChallengeID, cmd, assertionFor(cmd, f.private["owner-primary"], sign)); err != nil {
		t.Fatal(err)
	}
	actor := assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}
	registerSession(t, f, actor.SessionID, actor.AgentID)
	registerSession(t, f, "codex:worker", "worker")
	target := assignment.Principal{SessionID: "codex:worker", AgentID: "worker", Generation: 1}
	sha := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	open := func(id string, claim *assignment.DelegationBinding) assignment.Record {
		return assignment.Record{
			AssignmentID: id, State: assignment.Offered,
			Issuer: actor.SessionID, IssuerAgentID: actor.AgentID, IssuerGeneration: 1,
			CurrentCoordinator: actor.SessionID, CoordinatorAgentID: actor.AgentID, CoordinatorGeneration: 1,
			HandoffEvidence: []assignment.Handoff{},
			Assignee:        target.SessionID, StableAgentID: target.AgentID, SessionID: target.SessionID, AssigneeGeneration: 1,
			Role: "IMPLEMENTER", SubjectSHA: sha, DelegationBinding: claim,
			IssuedAt: now.Add(-time.Minute), ValidUntil: now.Add(time.Hour),
			AdmissionBasis:     assignment.AdmissionBasis{Kind: "POOL", Reference: "issue-93b", TargetSessionID: target.SessionID, TargetAgentID: target.AgentID, Role: "IMPLEMENTER", SubjectSHA: sha},
			VisibilityEvidence: assignment.VisibilityEvidence{Reference: "vis", SessionID: target.SessionID, ObservedAt: now.Add(-time.Minute)},
			Revision:           1, UpdatedAt: now.Add(-time.Minute),
		}
	}
	if err := f.s.OfferAssignment(actor, open("asg-ambig", nil), "ambig"); err == nil || !strings.Contains(err.Error(), "multiple grants") {
		t.Fatalf("ambiguous offer err=%v", err)
	}
	if err := f.s.OfferAssignment(actor, open("asg-a", &assignment.DelegationBinding{GrantID: "del-a"}), "offer-a"); err != nil {
		t.Fatal(err)
	}
	if b := f.s.state.AssignmentLedger.Records["asg-a"].DelegationBinding; b == nil || b.GrantID != "del-a" || b.Repository != "naghuale/coord" || b.Project != "coord" {
		t.Fatalf("del-a binding: %+v", f.s.state.AssignmentLedger.Records["asg-a"].DelegationBinding)
	}
	if err := f.s.OfferAssignment(actor, open("asg-b", &assignment.DelegationBinding{GrantID: "del-b"}), "offer-b"); err != nil {
		t.Fatal(err)
	}
	if b := f.s.state.AssignmentLedger.Records["asg-b"].DelegationBinding; b == nil || b.GrantID != "del-b" || b.Repository != "other/repo" || b.Project != "other" {
		t.Fatalf("del-b binding: %+v", f.s.state.AssignmentLedger.Records["asg-b"].DelegationBinding)
	}
}

func TestF11_GenericWriterCannotReplaceActingCoordinator(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-f11", now)
	seedDelegatedAssignment(t, f, "asg-f11", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now)
	succ := ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-b", SessionID: "codex:b", Generation: 1}
	confirmVisibility(t, f, "del-f11", "vis-f11-b", succ, now)
	registerSession(t, f, "codex:b", "agent-b")
	registerSession(t, f, "codex:attacker", "attacker")

	attacker := DelegationPrincipal{StableAgentID: "attacker", SessionID: "codex:attacker", Generation: 1}
	successor := DelegationPrincipal{StableAgentID: "agent-b", SessionID: "codex:b", Generation: 1}

	assertReject := func(t *testing.T, err error, before []byte, rev, seq, credRev uint64, events int, grantGen uint64, want string) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("err=%v want containing %q", err, want)
		}
		if !bytes.Equal(before, ownerStateBytes(t, f.s)) {
			t.Fatal("canonical bytes changed")
		}
		if f.s.state.Revision != rev {
			t.Fatalf("revision %d -> %d", rev, f.s.state.Revision)
		}
		agg := f.s.state.OwnerCredentials["owner-1"]
		if agg.CommandSequence != seq || agg.CredentialRevision != credRev {
			t.Fatalf("sequence/revision changed seq=%d/%d cred=%d/%d", agg.CommandSequence, seq, agg.CredentialRevision, credRev)
		}
		n := 0
		if f.s.state.AssignmentLedger != nil {
			n = len(f.s.state.AssignmentLedger.Events)
		}
		if n != events {
			t.Fatalf("events %d -> %d", events, n)
		}
		if f.s.state.CoordinatorDelegations["del-f11"].Generation != grantGen {
			t.Fatal("grant generation changed")
		}
	}
	snap := func(t *testing.T) ([]byte, uint64, uint64, uint64, int, uint64) {
		t.Helper()
		agg := f.s.state.OwnerCredentials["owner-1"]
		n := 0
		if f.s.state.AssignmentLedger != nil {
			n = len(f.s.state.AssignmentLedger.Events)
		}
		return ownerStateBytes(t, f.s), f.s.state.Revision, agg.CommandSequence, agg.CredentialRevision, n, f.s.state.CoordinatorDelegations["del-f11"].Generation
	}
	setActing := func(p DelegationPrincipal) func(*CanonicalState) error {
		return func(state *CanonicalState) error {
			rec := state.CoordinatorDelegations["del-f11"]
			rec.ActingCoordinator = &p
			state.CoordinatorDelegations["del-f11"] = rec
			return nil
		}
	}

	t.Run("generic_arbitrary_acting", func(t *testing.T) {
		before, rev, seq, cred, events, gen := snap(t)
		err := f.s.UpdateCanonical(setActing(attacker))
		assertReject(t, err, before, rev, seq, cred, events, gen, "grant authority is immutable via generic writer")
	})
	t.Run("generic_successor_without_handoff", func(t *testing.T) {
		before, rev, seq, cred, events, gen := snap(t)
		err := f.s.UpdateCanonical(setActing(successor))
		assertReject(t, err, before, rev, seq, cred, events, gen, "grant authority is immutable via generic writer")
	})
	t.Run("generic_restart_without_history", func(t *testing.T) {
		restart := DelegationPrincipal{StableAgentID: "agent-a", SessionID: "claude:a2", Generation: 2}
		before, rev, seq, cred, events, gen := snap(t)
		err := f.s.UpdateCanonical(setActing(restart))
		assertReject(t, err, before, rev, seq, cred, events, gen, "grant authority is immutable via generic writer")
	})
	t.Run("generic_no_visibility_receipt", func(t *testing.T) {
		before, rev, seq, cred, events, gen := snap(t)
		err := f.s.UpdateCanonical(func(state *CanonicalState) error {
			rec := state.CoordinatorDelegations["del-f11"]
			rec.ActingCoordinator = &successor
			from := rec.PrimaryCoordinator
			rec.History = append(rec.History, DelegationHistoryEntry{
				Kind: DelegationHistoryHandoff, At: now, AssignmentID: "asg-f11", From: &from, To: &successor,
			})
			state.CoordinatorDelegations["del-f11"] = rec
			return nil
		})
		assertReject(t, err, before, rev, seq, cred, events, gen, "grant authority is immutable via generic writer")
	})
	t.Run("generic_reset_consumed_visibility", func(t *testing.T) {
		lossConfirm(t, f, "del-f11", "loss-f11", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1}, now)
		prev := assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}
		next := assignment.Principal{SessionID: "codex:b", AgentID: "agent-b", Generation: 1}
		revAsg := f.s.state.AssignmentLedger.Records["asg-f11"].Revision
		if err := f.s.HandoffAssignment(prev, next, "asg-f11", revAsg, "f11-pos-handoff"); err != nil {
			t.Fatal(err)
		}
		before, rev, seq, cred, events, gen := snap(t)
		err := f.s.UpdateCanonical(func(state *CanonicalState) error {
			rec := state.CoordinatorDelegations["del-f11"]
			for i := range rec.VisibilityReceipts {
				rec.VisibilityReceipts[i].ConsumedAt = time.Time{}
				rec.VisibilityReceipts[i].ConsumedBy = ""
			}
			state.CoordinatorDelegations["del-f11"] = rec
			return nil
		})
		assertReject(t, err, before, rev, seq, cred, events, gen, "grant authority is immutable via generic writer")
	})
}

func TestF11_HandoffSeamRejectsUnvalidatedActingDelta(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-f11s", now)
	seedDelegatedAssignment(t, f, "asg-f11s", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now)
	confirmVisibility(t, f, "del-f11s", "vis-f11s-b", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-b", SessionID: "codex:b", Generation: 1}, now)
	registerSession(t, f, "claude:a2", "agent-a")
	bumpSessionGeneration(t, f, "claude:a2", 2)
	confirmVisibility(t, f, "del-f11s", "vis-f11s-pr", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a2", Generation: 2}, now)
	registerSession(t, f, "codex:b", "agent-b")
	lossConfirm(t, f, "del-f11s", "loss-f11s", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1}, now)

	successor := DelegationPrincipal{StableAgentID: "agent-b", SessionID: "codex:b", Generation: 1}
	from := DelegationPrincipal{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1}
	consume := func(rec *DelegationRecord, msgID, consumedBy string) {
		for i := range rec.VisibilityReceipts {
			if rec.VisibilityReceipts[i].MessageID == msgID {
				rec.VisibilityReceipts[i].ConsumedAt = now
				rec.VisibilityReceipts[i].ConsumedBy = consumedBy
			}
		}
	}
	assertReject := func(t *testing.T, err error, before []byte, rev, seq uint64, events int, want string) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("err=%v want containing %q", err, want)
		}
		if !bytes.Equal(before, ownerStateBytes(t, f.s)) {
			t.Fatal("canonical bytes changed")
		}
		if f.s.state.Revision != rev {
			t.Fatal("revision changed")
		}
		if f.s.state.OwnerCredentials["owner-1"].CommandSequence != seq {
			t.Fatal("sequence changed")
		}
		if len(f.s.state.AssignmentLedger.Events) != events {
			t.Fatal("event written")
		}
		if f.s.state.CoordinatorDelegations["del-f11s"].Generation != 1 {
			t.Fatal("grant generation changed")
		}
	}

	t.Run("no_handoff_history", func(t *testing.T) {
		before := ownerStateBytes(t, f.s)
		rev, seq, events := f.s.state.Revision, f.s.state.OwnerCredentials["owner-1"].CommandSequence, len(f.s.state.AssignmentLedger.Events)
		err := f.s.commitCanonical(delegationCommitHandoff, func(state *CanonicalState) error {
			rec := state.CoordinatorDelegations["del-f11s"]
			rec.ActingCoordinator = &successor
			consume(&rec, "vis-f11s-b", "asg-f11s")
			state.CoordinatorDelegations["del-f11s"] = rec
			return nil
		})
		assertReject(t, err, before, rev, seq, events, "handoff history required")
	})
	t.Run("no_visibility_receipt", func(t *testing.T) {
		before := ownerStateBytes(t, f.s)
		rev, seq, events := f.s.state.Revision, f.s.state.OwnerCredentials["owner-1"].CommandSequence, len(f.s.state.AssignmentLedger.Events)
		err := f.s.commitCanonical(delegationCommitHandoff, func(state *CanonicalState) error {
			rec := state.CoordinatorDelegations["del-f11s"]
			rec.ActingCoordinator = &successor
			rec.History = append(rec.History, DelegationHistoryEntry{
				Kind: DelegationHistoryHandoff, At: now, AssignmentID: "asg-f11s", From: &from, To: &successor,
			})
			asg := state.AssignmentLedger.Records["asg-f11s"]
			asg.CurrentCoordinator, asg.CoordinatorAgentID, asg.CoordinatorGeneration = successor.SessionID, successor.StableAgentID, successor.Generation
			state.AssignmentLedger.Records["asg-f11s"] = asg
			state.CoordinatorDelegations["del-f11s"] = rec
			return nil
		})
		assertReject(t, err, before, rev, seq, events, "visibility receipt required")
	})
	t.Run("receipt_wrong_successor", func(t *testing.T) {
		before := ownerStateBytes(t, f.s)
		rev, seq, events := f.s.state.Revision, f.s.state.OwnerCredentials["owner-1"].CommandSequence, len(f.s.state.AssignmentLedger.Events)
		err := f.s.commitCanonical(delegationCommitHandoff, func(state *CanonicalState) error {
			rec := state.CoordinatorDelegations["del-f11s"]
			rec.ActingCoordinator = &successor
			rec.History = append(rec.History, DelegationHistoryEntry{
				Kind: DelegationHistoryHandoff, At: now, AssignmentID: "asg-f11s", From: &from, To: &successor,
			})
			consume(&rec, "vis-f11s-pr", "asg-f11s")
			asg := state.AssignmentLedger.Records["asg-f11s"]
			asg.CurrentCoordinator, asg.CoordinatorAgentID, asg.CoordinatorGeneration = successor.SessionID, successor.StableAgentID, successor.Generation
			state.AssignmentLedger.Records["asg-f11s"] = asg
			state.CoordinatorDelegations["del-f11s"] = rec
			return nil
		})
		assertReject(t, err, before, rev, seq, events, "visibility receipt successor mismatch")
	})
	t.Run("receipt_wrong_assignment", func(t *testing.T) {
		before := ownerStateBytes(t, f.s)
		rev, seq, events := f.s.state.Revision, f.s.state.OwnerCredentials["owner-1"].CommandSequence, len(f.s.state.AssignmentLedger.Events)
		err := f.s.commitCanonical(delegationCommitHandoff, func(state *CanonicalState) error {
			rec := state.CoordinatorDelegations["del-f11s"]
			rec.ActingCoordinator = &successor
			rec.History = append(rec.History, DelegationHistoryEntry{
				Kind: DelegationHistoryHandoff, At: now, AssignmentID: "asg-f11s", From: &from, To: &successor,
			})
			consume(&rec, "vis-f11s-b", "asg-other")
			asg := state.AssignmentLedger.Records["asg-f11s"]
			asg.CurrentCoordinator, asg.CoordinatorAgentID, asg.CoordinatorGeneration = successor.SessionID, successor.StableAgentID, successor.Generation
			state.AssignmentLedger.Records["asg-f11s"] = asg
			state.CoordinatorDelegations["del-f11s"] = rec
			return nil
		})
		assertReject(t, err, before, rev, seq, events, "visibility receipt assignment mismatch")
	})
}

func TestF11_HandoffSeamFreezesGrantScopeValidityAndSuccessors(t *testing.T) {
	for _, field := range []string{"repository", "validity", "successors"} {
		t.Run(field, func(t *testing.T) {
			enableDelegationHarness(t)
			f := newOwnerCommandFixture(t)
			now := f.s.now().UTC()
			grantDelegation(t, f, "del-f11-freeze", now)
			seedDelegatedAssignment(t, f, "asg-f11-freeze", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now)
			registerSession(t, f, "codex:b", "agent-b")
			confirmVisibility(t, f, "del-f11-freeze", "vis-f11-freeze", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-b", SessionID: "codex:b", Generation: 1}, now)
			lossConfirm(t, f, "del-f11-freeze", "loss-f11-freeze", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1}, now)
			handoffAt := f.s.now().UTC()
			prev := assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}
			next := assignment.Principal{SessionID: "codex:b", AgentID: "agent-b", Generation: 1}
			before := ownerStateBytes(t, f.s)
			saves := f.p.saveCalls
			err := f.s.commitCanonical(delegationCommitHandoff, func(state *CanonicalState) error {
				id := "asg-f11-freeze"
				record := state.AssignmentLedger.Records[id]
				if err := enforceDelegatedHandoff(state, prev, next, id, handoffAt); err != nil {
					return err
				}
				nextRecord := record
				nextRecord.CurrentCoordinator, nextRecord.CoordinatorAgentID, nextRecord.CoordinatorGeneration = next.SessionID, next.AgentID, next.Generation
				nextRecord.HandoffEvidence = append(append([]assignment.Handoff{}, record.HandoffEvidence...), assignment.Handoff{From: prev, To: next, EvidenceRef: "seam-freeze", At: handoffAt})
				nextRecord.CoordinatorLost = false
				if err := appendAssignment(state, next, record, nextRecord, "HANDOFF", "seam-freeze", handoffAt); err != nil {
					return err
				}
				grant := state.CoordinatorDelegations["del-f11-freeze"]
				switch field {
				case "repository":
					grant.Scope.Repository = "attacker/other"
				case "validity":
					grant.Validity.ExpiresAt = grant.Validity.ExpiresAt.Add(time.Hour)
				case "successors":
					grant.Successors[0].SessionID = "codex:other"
				}
				state.CoordinatorDelegations["del-f11-freeze"] = grant
				return nil
			})
			if err == nil || !strings.Contains(err.Error(), "grant authority is immutable") {
				t.Fatalf("handoff seam changed %s err=%v", field, err)
			}
			if f.p.saveCalls != saves || !bytes.Equal(before, ownerStateBytes(t, f.s)) {
				t.Fatalf("rejected %s mutation changed canonical state or saved", field)
			}
			if err := f.s.HandoffAssignment(prev, next, "asg-f11-freeze", f.s.state.AssignmentLedger.Records["asg-f11-freeze"].Revision, "frozen-grant-positive"); err != nil {
				t.Fatalf("valid handoff positive control after rejected %s mutation: %v", field, err)
			}
		})
	}
}

func TestF11_PrimaryRestartSeamRejectsUnvalidatedActingDelta(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-f11r", now)
	seedDelegatedAssignment(t, f, "asg-f11r", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now)
	lossConfirm(t, f, "del-f11r", "loss-f11r", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1}, now)
	registerSession(t, f, "claude:a2", "agent-a")
	bumpSessionGeneration(t, f, "claude:a2", 2)
	confirmVisibility(t, f, "del-f11r", "vis-f11r", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a2", Generation: 2}, now)

	to := DelegationPrincipal{StableAgentID: "agent-a", SessionID: "claude:a2", Generation: 2}
	from := DelegationPrincipal{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1}
	consume := func(rec *DelegationRecord, msgID, consumedBy string) {
		for i := range rec.VisibilityReceipts {
			if rec.VisibilityReceipts[i].MessageID == msgID {
				rec.VisibilityReceipts[i].ConsumedAt = now
				rec.VisibilityReceipts[i].ConsumedBy = consumedBy
			}
		}
	}
	assertReject := func(t *testing.T, err error, before []byte, want string) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("err=%v want containing %q", err, want)
		}
		if !bytes.Equal(before, ownerStateBytes(t, f.s)) {
			t.Fatal("canonical bytes changed")
		}
	}

	t.Run("no_restart_history", func(t *testing.T) {
		before := ownerStateBytes(t, f.s)
		err := f.s.commitCanonical(delegationCommitPrimaryRestart, func(state *CanonicalState) error {
			rec := state.CoordinatorDelegations["del-f11r"]
			rec.ActingCoordinator = &to
			consume(&rec, "vis-f11r", "restart-1")
			state.CoordinatorDelegations["del-f11r"] = rec
			return nil
		})
		assertReject(t, err, before, "primaryRestart history required")
	})
	t.Run("no_visibility_receipt", func(t *testing.T) {
		before := ownerStateBytes(t, f.s)
		err := f.s.commitCanonical(delegationCommitPrimaryRestart, func(state *CanonicalState) error {
			rec := state.CoordinatorDelegations["del-f11r"]
			rec.ActingCoordinator = &to
			rec.History = append(rec.History, DelegationHistoryEntry{
				Kind: DelegationHistoryPrimaryRestart, At: now, From: &from, To: &to,
				AssignmentID: "asg-f11r", ReceiptID: "loss-f11r", ReceiptDig: "vis-f11r",
			})
			state.CoordinatorDelegations["del-f11r"] = rec
			return nil
		})
		assertReject(t, err, before, "visibility receipt required")
	})
	t.Run("receipt_wrong_loss_record", func(t *testing.T) {
		before := ownerStateBytes(t, f.s)
		err := f.s.commitCanonical(delegationCommitPrimaryRestart, func(state *CanonicalState) error {
			rec := state.CoordinatorDelegations["del-f11r"]
			rec.ActingCoordinator = &to
			rec.History = append(rec.History, DelegationHistoryEntry{
				Kind: DelegationHistoryPrimaryRestart, At: now, From: &from, To: &to,
				AssignmentID: "asg-f11r", ReceiptID: "loss-other", ReceiptDig: "vis-f11r",
			})
			consume(&rec, "vis-f11r", "restart-1")
			state.CoordinatorDelegations["del-f11r"] = rec
			return nil
		})
		assertReject(t, err, before, "visibility receipt loss record mismatch")
	})
	t.Run("receipt_wrong_assignment", func(t *testing.T) {
		before := ownerStateBytes(t, f.s)
		err := f.s.commitCanonical(delegationCommitPrimaryRestart, func(state *CanonicalState) error {
			rec := state.CoordinatorDelegations["del-f11r"]
			rec.ActingCoordinator = &to
			rec.History = append(rec.History, DelegationHistoryEntry{
				Kind: DelegationHistoryPrimaryRestart, At: now, From: &from, To: &to,
				AssignmentID: "asg-other", ReceiptID: "loss-f11r", ReceiptDig: "vis-f11r",
			})
			consume(&rec, "vis-f11r", "restart-1")
			state.CoordinatorDelegations["del-f11r"] = rec
			return nil
		})
		assertReject(t, err, before, "visibility receipt assignment mismatch")
	})
	t.Run("receipt_wrong_generation", func(t *testing.T) {
		to3 := to
		to3.Generation = 3
		before := ownerStateBytes(t, f.s)
		err := f.s.commitCanonical(delegationCommitPrimaryRestart, func(state *CanonicalState) error {
			rec := state.CoordinatorDelegations["del-f11r"]
			rec.ActingCoordinator = &to3
			rec.History = append(rec.History, DelegationHistoryEntry{
				Kind: DelegationHistoryPrimaryRestart, At: now, From: &from, To: &to3,
				AssignmentID: "asg-f11r", ReceiptID: "loss-f11r", ReceiptDig: "vis-f11r",
			})
			consume(&rec, "vis-f11r", "restart-1")
			state.CoordinatorDelegations["del-f11r"] = rec
			return nil
		})
		assertReject(t, err, before, "visibility receipt generation mismatch")
	})
}

func TestF11_ApplyInboxCommandCannotReplaceActingCoordinator(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-f11-inbox", now)
	payload, _ := json.Marshal(ownercredential.CreateTaskPayload{
		Title: "f11-inbox", StagePlan: task.StagePlan{Stages: []task.Stage{task.Execution}, PolicyVersion: task.StagePlanPolicyVersion},
	})
	rev := f.s.state.OwnerCredentials["owner-1"].CredentialRevision
	create := f.command(ownercredential.CommandCreateTask, "create-f11-inbox", "owner-primary", rev, payload)
	sign := f.s.state.OwnerCredentials["owner-1"].Credentials["owner-primary"].SignCount + 1
	created, _, err := f.s.ApplyOwnerCredentialCommand(create.ChallengeID, create, assertionFor(create, f.private["owner-primary"], sign))
	if err != nil || created.TaskID == "" {
		t.Fatalf("create task err=%v receipt=%+v", err, created)
	}
	id := taskid.ID(created.TaskID)
	current := f.s.state.Tasks[id]
	attacker := DelegationPrincipal{StableAgentID: "attacker", SessionID: "codex:attacker", Generation: 1}
	before := ownerStateBytes(t, f.s)
	canonRev := f.s.state.Revision
	seq := f.s.state.OwnerCredentials["owner-1"].CommandSequence
	events := len(f.s.state.Events)
	_, _, err = f.s.ApplyInboxCommand("agent-f11", "msg-f11-inbox", id, current.Revision, func(state *CanonicalState) (InboxReceipt, error) {
		value := state.Tasks[id]
		value.Revision++
		value.LastEventSeq++
		value.UpdatedAt = now.Add(time.Second)
		state.Tasks[id] = value
		event := task.DomainEvent{
			ID: task.EventID(id, value.LastEventSeq), TaskID: id, EventSeq: value.LastEventSeq,
			TaskRevision: value.Revision, Type: "OWNER_DECISION", ActorID: "agent-f11", CreatedAt: value.UpdatedAt,
		}
		state.Events = append(state.Events, event)
		rec := state.CoordinatorDelegations["del-f11-inbox"]
		rec.ActingCoordinator = &attacker
		rec.Scope.AllowedWorkItems = append(append([]string{}, rec.Scope.AllowedWorkItems...), "issue-outside-original-grant")
		state.CoordinatorDelegations["del-f11-inbox"] = rec
		return InboxReceipt{
			ActorID: "agent-f11", MessageID: "msg-f11-inbox", TaskID: id,
			EventIDs: []string{event.ID}, ExpectedRevision: current.Revision, CommittedRevision: value.Revision,
			RecordedAt: value.UpdatedAt,
		}, nil
	})
	if err == nil || !strings.Contains(err.Error(), "grant authority is immutable via generic writer") {
		t.Fatalf("inbox writer err=%v", err)
	}
	if !bytes.Equal(before, ownerStateBytes(t, f.s)) {
		t.Fatal("canonical bytes changed")
	}
	if f.s.state.Revision != canonRev || f.s.state.OwnerCredentials["owner-1"].CommandSequence != seq || len(f.s.state.Events) != events {
		t.Fatal("revision, sequence, or event written")
	}
	if f.s.state.CoordinatorDelegations["del-f11-inbox"].Generation != 1 {
		t.Fatal("grant generation changed")
	}
	if acting := f.s.state.CoordinatorDelegations["del-f11-inbox"].CurrentActing(); acting.StableAgentID == "attacker" {
		t.Fatal("acting coordinator replaced")
	}
}
