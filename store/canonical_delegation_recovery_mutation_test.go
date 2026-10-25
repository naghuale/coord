package store

import (
	"bytes"
	"coord/assignment"
	"coord/ownercredential"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

func recoveryLossSnapshot(t *testing.T, delegationID, assignmentID string) (*ownerCommandFixture, time.Time, CanonicalState) {
	t.Helper()
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, delegationID, now)
	seedDelegatedAssignment(t, f, assignmentID, assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now)
	lossConfirm(t, f, delegationID, "loss-"+delegationID, ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1}, now)
	snapshot, err := copyCanonicalState(f.s.state)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateCanonicalState(snapshot); err != nil {
		t.Fatalf("positive snapshot control invalid: %v", err)
	}
	return f, now, snapshot
}

func TestRecoveryA93B3_LossConfirmUsesExactScopedAssignmentSet(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-loss-exact-set", now)
	coord := assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}
	seedDelegatedAssignment(t, f, "asg-in-scope", coord, now)
	registerSession(t, f, coord.SessionID, coord.AgentID)
	target := assignment.Principal{SessionID: "codex:worker", AgentID: "worker", Generation: 1}
	registerSession(t, f, target.SessionID, target.AgentID)
	out := assignment.Record{
		AssignmentID: "asg-out-of-scope", State: assignment.Offered,
		Issuer: coord.SessionID, IssuerAgentID: coord.AgentID, IssuerGeneration: coord.Generation,
		CurrentCoordinator: coord.SessionID, CoordinatorAgentID: coord.AgentID, CoordinatorGeneration: coord.Generation,
		HandoffEvidence: []assignment.Handoff{}, Assignee: target.SessionID, StableAgentID: target.AgentID,
		SessionID: target.SessionID, AssigneeGeneration: target.Generation, Role: "IMPLEMENTER",
		SubjectSHA: "dddddddddddddddddddddddddddddddddddddddd",
		IssuedAt:   now.Add(-time.Minute), ValidUntil: now.Add(time.Hour),
		AdmissionBasis:     assignment.AdmissionBasis{Kind: "POOL", Reference: "other-issue", TargetSessionID: target.SessionID, TargetAgentID: target.AgentID, Role: "IMPLEMENTER", SubjectSHA: "dddddddddddddddddddddddddddddddddddddddd"},
		VisibilityEvidence: assignment.VisibilityEvidence{Reference: "vis-oos", SessionID: target.SessionID, ObservedAt: now.Add(-time.Minute)},
		DelegationBinding:  &assignment.DelegationBinding{GrantID: "del-loss-exact-set", GrantGeneration: 1, Repository: "naghuale/coord", Project: "coord", AllowedWorkItem: "other-issue"},
		Revision:           1, UpdatedAt: now.Add(-time.Minute),
	}
	pokeAssignmentRecord(t, f, out)
	beforeOut := f.s.state.AssignmentLedger.Records[out.AssignmentID]
	payload, err := json.Marshal(ownercredential.LossConfirmPayload{
		DelegationID: "del-loss-exact-set", Coordinator: ownercredential.DelegationPrincipalPayload{StableAgentID: coord.AgentID, SessionID: coord.SessionID, Generation: coord.Generation},
		AssignmentIDs: []string{"asg-in-scope"}, Reason: "only the in-scope assignment", ObservedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	cmd := ownercredential.Command{Kind: ownercredential.CommandLossConfirm, MessageID: "loss-exact-set", PrincipalID: "owner-1", CredentialID: "owner-primary", Payload: payload, Digest: "digest-exact-set"}
	state, err := copyCanonicalState(f.s.state)
	if err != nil {
		t.Fatal(err)
	}
	if err := applyLossConfirm(&state, cmd, now); err != nil {
		t.Fatalf("exact in-scope loss failed: %v", err)
	}
	if !state.AssignmentLedger.Records["asg-in-scope"].CoordinatorLost {
		t.Fatal("positive control did not record the in-scope loss")
	}
	if got := state.AssignmentLedger.Records[out.AssignmentID]; !reflect.DeepEqual(got, beforeOut) {
		t.Fatalf("loss.confirm changed out-of-scope assignment: before=%+v after=%+v", beforeOut, got)
	}
}

func TestRecoveryA93B3_LossConfirmCannotSelectUnboundAssignment(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-loss-unbound", now)
	coord := assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}
	seedDelegatedAssignment(t, f, "asg-bound", coord, now)
	registerSession(t, f, coord.SessionID, coord.AgentID)
	target := assignment.Principal{SessionID: "codex:worker", AgentID: "worker", Generation: 1}
	registerSession(t, f, target.SessionID, target.AgentID)
	unbound := assignment.Record{
		AssignmentID: "asg-unbound", State: assignment.Offered,
		Issuer: coord.SessionID, IssuerAgentID: coord.AgentID, IssuerGeneration: coord.Generation,
		CurrentCoordinator: coord.SessionID, CoordinatorAgentID: coord.AgentID, CoordinatorGeneration: coord.Generation,
		HandoffEvidence: []assignment.Handoff{}, Assignee: target.SessionID, StableAgentID: target.AgentID,
		SessionID: target.SessionID, AssigneeGeneration: target.Generation, Role: "IMPLEMENTER",
		SubjectSHA: "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
		IssuedAt:   now.Add(-time.Minute), ValidUntil: now.Add(time.Hour),
		AdmissionBasis:     assignment.AdmissionBasis{Kind: "POOL", Reference: "issue-93b", TargetSessionID: target.SessionID, TargetAgentID: target.AgentID, Role: "IMPLEMENTER", SubjectSHA: "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"},
		VisibilityEvidence: assignment.VisibilityEvidence{Reference: "vis-unbound", SessionID: target.SessionID, ObservedAt: now.Add(-time.Minute)},
		Revision:           1, UpdatedAt: now.Add(-time.Minute),
	}
	pokeAssignmentRecord(t, f, unbound)
	payload, err := json.Marshal(ownercredential.LossConfirmPayload{
		DelegationID: "del-loss-unbound", Coordinator: ownercredential.DelegationPrincipalPayload{StableAgentID: coord.AgentID, SessionID: coord.SessionID, Generation: coord.Generation},
		AssignmentIDs: []string{"asg-bound", "asg-unbound"}, Reason: "unbound ids are not in the grant", ObservedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	cmd := ownercredential.Command{Kind: ownercredential.CommandLossConfirm, MessageID: "loss-unbound-set", PrincipalID: "owner-1", CredentialID: "owner-primary", Payload: payload, Digest: "digest-unbound-set"}
	state, err := copyCanonicalState(f.s.state)
	if err != nil {
		t.Fatal(err)
	}
	before, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := applyLossConfirm(&state, cmd, now); !errors.Is(err, ErrCoordinatorDelegationInvalid) {
		t.Fatalf("loss.confirm selected unbound assignment: err=%v", err)
	}
	after, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("rejected unbound assignment selection changed canonical state")
	}
}

func TestRecoveryA93B2_SchemaFiveRejectsDelegationState(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-schema-v5", now)
	valid, err := copyCanonicalState(f.s.state)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateCanonicalState(valid); err != nil {
		t.Fatalf("schema v6 positive control rejected: %v", err)
	}
	legacy, err := copyCanonicalState(valid)
	if err != nil {
		t.Fatal(err)
	}
	legacy.SchemaVersion = 5
	if err := ValidateCanonicalState(legacy); err == nil || !strings.Contains(err.Error(), "schema 5 cannot contain CoordinatorDelegations") {
		t.Fatalf("schema v5 with v6 delegation state err=%v", err)
	}
	if _, err := NewCanonical(&canonicalMemory{state: legacy}); err == nil {
		t.Fatal("NewCanonical loaded schema v5 with delegation state")
	}
}

func TestRecoveryA93B1_AssignmentBindingRequiresAllowedWorkItem(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-binding-workitem", now)
	seedDelegatedAssignment(t, f, "asg-binding-workitem", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now)
	state, err := copyCanonicalState(f.s.state)
	if err != nil {
		t.Fatal(err)
	}
	record := state.AssignmentLedger.Records["asg-binding-workitem"]
	if _, err := assignmentBoundGrant(state, record); err != nil {
		t.Fatalf("positive exact binding rejected: %v", err)
	}
	record.AdmissionBasis.Reference = "issue-93b-drift"
	if _, err := assignmentBoundGrant(state, record); err == nil || !strings.Contains(err.Error(), "differs from admission reference") {
		t.Fatalf("assignment bound to disallowed work item err=%v", err)
	}
}

func TestRecoveryA93B1_OfferBindingRequiresCurrentActingPrincipal(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-offer-acting", now)
	state, err := copyCanonicalState(f.s.state)
	if err != nil {
		t.Fatal(err)
	}
	goodActor := assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}
	record := assignment.Record{AssignmentID: "asg-offer-acting", AdmissionBasis: assignment.AdmissionBasis{Reference: "issue-93b"}, DelegationBinding: &assignment.DelegationBinding{GrantID: "del-offer-acting"}}
	if err := bindAssignmentScopeFromGrant(state, goodActor, &record, now); err != nil {
		t.Fatalf("positive current acting principal rejected: %v", err)
	}
	badActor := assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 2}
	claimed := assignment.Record{AssignmentID: "asg-offer-acting-bad", AdmissionBasis: assignment.AdmissionBasis{Reference: "issue-93b"}, DelegationBinding: &assignment.DelegationBinding{GrantID: "del-offer-acting"}}
	before, err := json.Marshal(claimed)
	if err != nil {
		t.Fatal(err)
	}
	if err := bindAssignmentScopeFromGrant(state, badActor, &claimed, now); !errors.Is(err, ErrCoordinatorDelegationInvalid) {
		t.Fatalf("non-acting successor claimed offer authority: %v", err)
	}
	after, err := json.Marshal(claimed)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("rejected offer claim changed assignment binding")
	}
}

func TestRecoveryA93B2_SnapshotRejectsAssignmentOutsideGrantWorkItems(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-snapshot-workitem", now)
	seedDelegatedAssignment(t, f, "asg-snapshot-workitem", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now)
	state, err := copyCanonicalState(f.s.state)
	if err != nil {
		t.Fatal(err)
	}
	mutate := func(record *assignment.Record) {
		record.AdmissionBasis.Reference = "issue-93b-drift"
	}
	current := state.AssignmentLedger.Records["asg-snapshot-workitem"]
	mutate(&current)
	state.AssignmentLedger.Records[current.AssignmentID] = current
	for i := range state.AssignmentLedger.Events {
		if state.AssignmentLedger.Events[i].AssignmentID == current.AssignmentID {
			eventRecord := state.AssignmentLedger.Events[i].Record
			mutate(&eventRecord)
			state.AssignmentLedger.Events[i].Record = eventRecord
		}
	}
	if err := ValidateCanonicalState(state); err == nil || !strings.Contains(err.Error(), "allowedWorkItem drifted from admission reference") {
		t.Fatalf("out-of-grant work item snapshot err=%v", err)
	}
}

func TestRecoveryA93B2_SnapshotRejectsMissingBoundGrant(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-snapshot-missing-grant", now)
	seedDelegatedAssignment(t, f, "asg-snapshot-missing-grant", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now)
	state, err := copyCanonicalState(f.s.state)
	if err != nil {
		t.Fatal(err)
	}
	mutate := func(record *assignment.Record) {
		binding := *record.DelegationBinding
		binding.GrantID = "del-missing"
		record.DelegationBinding = &binding
	}
	current := state.AssignmentLedger.Records["asg-snapshot-missing-grant"]
	mutate(&current)
	state.AssignmentLedger.Records[current.AssignmentID] = current
	for i := range state.AssignmentLedger.Events {
		if state.AssignmentLedger.Events[i].AssignmentID == current.AssignmentID {
			eventRecord := state.AssignmentLedger.Events[i].Record
			mutate(&eventRecord)
			state.AssignmentLedger.Events[i].Record = eventRecord
		}
	}
	if err := ValidateCanonicalState(state); err == nil || !strings.Contains(err.Error(), "grant del-missing missing") {
		t.Fatalf("snapshot with substituted missing grant err=%v", err)
	}
}

func TestRecoveryA93B2_DisabledTransitionRejectsDelegationSliceChange(t *testing.T) {
	if coordinatorDelegationActive() {
		t.Fatal("test requires the production-disabled mode")
	}
	previous := EmptyCanonicalState()
	next := EmptyCanonicalState()
	next.CoordinatorDelegations = map[string]DelegationRecord{
		"del-forged": {DelegationID: "del-forged"},
	}
	if err := ValidateCanonicalTransition(previous, next); !errors.Is(err, ErrCoordinatorDelegationDisabled) {
		t.Fatalf("disabled canonical transition allowed delegation insert: %v", err)
	}
}

func TestRecoveryA93B2_DisabledHandoffHelperFailsClosed(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-disabled-handoff", now)
	seedDelegatedAssignment(t, f, "asg-disabled-handoff", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now)
	lossConfirm(t, f, "del-disabled-handoff", "loss-disabled-handoff", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1}, now)
	state, err := copyCanonicalState(f.s.state)
	if err != nil {
		t.Fatal(err)
	}
	delegationHarnessEnabled = false
	if ownerCredentialLossAllowsHandoffWithoutObserver(state, "asg-disabled-handoff") {
		t.Fatal("production-disabled handoff helper accepted OWNER loss without observer")
	}
}

func TestRecoveryA93B2_DisabledPrimaryRestartCannotCommitThroughWriter(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-disabled-restart", now)
	seedDelegatedAssignment(t, f, "asg-disabled-restart", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now)
	lossConfirm(t, f, "del-disabled-restart", "loss-disabled-restart", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1}, now)
	if err := f.s.RegisterAgentSession("codex:fresh", "agent-a", ed25519.PublicKey(strings.Repeat("z", 32))); err != nil {
		t.Fatal(err)
	}
	if err := f.s.RotateAgentSessionKey("codex:fresh", ed25519.PublicKey(strings.Repeat("y", 32))); err != nil {
		t.Fatal(err)
	}
	confirmVisibility(t, f, "del-disabled-restart", "vis-disabled-restart", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "codex:fresh", Generation: 2}, now)
	before := ownerStateBytes(t, f.s)
	saves := f.p.saveCalls
	delegationHarnessEnabled = false
	if err := f.s.ApplyPrimaryRestart("del-disabled-restart", assignment.Principal{SessionID: "codex:fresh", AgentID: "agent-a", Generation: 2}, "vis-disabled-restart"); !errors.Is(err, ErrCoordinatorDelegationDisabled) {
		t.Fatalf("disabled primary restart err=%v", err)
	}
	if f.p.saveCalls != saves || !bytes.Equal(before, ownerStateBytes(t, f.s)) {
		t.Fatal("disabled primary restart changed or saved canonical state")
	}
}

func TestRecoveryA93BMutationKillers_LossReceiptHistory(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *CanonicalState)
	}{
		{"receipt_same_commit_revision", func(t *testing.T, s *CanonicalState) {
			for i := range s.AssignmentLedger.Events {
				e := &s.AssignmentLedger.Events[i]
				if e.Kind == "COORDINATOR_LOST" && e.EffectiveActorSource() == assignment.ActorSourceOwnerCredential {
					agg := s.OwnerCredentials[e.OwnerPrincipalID]
					receipt := agg.Receipts[e.OwnerReceiptMessageID]
					if e.OwnerCommitRevision < 2 {
						t.Fatal("test setup cannot lower receipt commit revision safely")
					}
					receipt.CanonicalRevision = e.OwnerCommitRevision - 1
					agg.Receipts[receipt.MessageID] = receipt
					s.OwnerCredentials[e.OwnerPrincipalID] = agg
					return
				}
			}
			t.Fatal("setup has no OWNER loss event")
		}},
		{"duplicate_loss_history_entry", func(t *testing.T, s *CanonicalState) {
			d := s.CoordinatorDelegations["del-n06"]
			for _, h := range d.History {
				if h.Kind == DelegationHistoryLossRecorded {
					d.History = append(d.History, h)
					s.CoordinatorDelegations[d.DelegationID] = d
					return
				}
			}
			t.Fatal("setup has no loss history")
		}},
		{"owner_loss_actor_mismatch", func(t *testing.T, s *CanonicalState) {
			for i := range s.AssignmentLedger.Events {
				e := &s.AssignmentLedger.Events[i]
				if e.Kind == "COORDINATOR_LOST" && e.EffectiveActorSource() == assignment.ActorSourceOwnerCredential {
					e.Actor.SessionID = "forged-owner-session"
					e.Actor.AgentID = "forged-owner"
					e.Actor.Generation = 7
					return
				}
			}
			t.Fatal("setup has no OWNER loss event")
		}},
		{"owner_loss_event_missing_from_ledger", func(t *testing.T, s *CanonicalState) {
			events := s.AssignmentLedger.Events
			for i, e := range events {
				if e.Kind != "COORDINATOR_LOST" || e.EffectiveActorSource() != assignment.ActorSourceOwnerCredential {
					continue
				}
				if i == 0 || events[i-1].AssignmentID != e.AssignmentID {
					t.Fatalf("unexpected event order around loss for %s", e.AssignmentID)
				}
				ledger := *s.AssignmentLedger
				ledger.Events = append(append([]assignment.Event(nil), events[:i]...), events[i+1:]...)
				ledger.Records = make(map[string]assignment.Record, len(s.AssignmentLedger.Records))
				for id, r := range s.AssignmentLedger.Records {
					ledger.Records[id] = r
				}
				ledger.Records[e.AssignmentID] = events[i-1].Record
				s.AssignmentLedger = &ledger
				return
			}
			t.Fatal("setup has no OWNER loss event")
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			id := "del-n06"
			if tc.name != "duplicate_loss_history_entry" {
				id = "del-" + strings.ReplaceAll(tc.name, "_", "-")
			}
			_, _, snapshot := recoveryLossSnapshot(t, id, "asg-"+strings.TrimPrefix(id, "del-"))
			tc.mutate(t, &snapshot)
			if err := ValidateCanonicalState(snapshot); err == nil {
				t.Fatal("malformed receipt/history snapshot accepted")
			} else if tc.name == "owner_loss_actor_mismatch" && !strings.Contains(err.Error(), "OWNER_CREDENTIAL loss actor mismatch") {
				t.Fatalf("owner actor mismatch error=%v, want exact actor check", err)
			}
			if loaded, err := NewCanonical(&canonicalMemory{state: snapshot}); err == nil {
				if closeErr := loaded.Close(); closeErr != nil {
					t.Errorf("close loaded store: %v", closeErr)
				}
				t.Fatal("NewCanonical accepted malformed receipt/history snapshot")
			}
		})
	}
}

func TestRecoveryA93B1_GenericWriterGuardRejectsOwnerLossEvent(t *testing.T) {
	previous := assignment.Empty()
	next := assignment.Empty()
	next.Events = append(next.Events, assignment.Event{
		AssignmentID: "existing-assignment", Kind: "COORDINATOR_LOST",
		ActorSource: assignment.ActorSourceOwnerCredential,
	})
	if err := rejectGenericOwnerLossEvents(previous, next); !errors.Is(err, ErrOwnerCommandRequired) {
		t.Fatalf("generic writer guard error=%v, want OWNER_COMMAND_REQUIRED", err)
	}
	positive := assignment.Empty()
	if err := rejectGenericOwnerLossEvents(previous, positive); err != nil {
		t.Fatalf("empty ordinary generic-writer delta rejected: %v", err)
	}
}

func TestRecoveryA93BMutationKillers_ReceiptCannotBeReusedAcrossOwnerPrincipalContexts(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-n03", now)
	coord := assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}
	seedDelegatedAssignment(t, f, "asg-n03-a", coord, now)
	seedDelegatedAssignment(t, f, "asg-n03-b", coord, now)
	lossConfirm(t, f, "del-n03", "loss-n03", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1}, now)
	snapshot, err := copyCanonicalState(f.s.state)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateCanonicalState(snapshot); err != nil {
		t.Fatalf("positive two-assignment loss snapshot invalid: %v", err)
	}
	// Model the same durable receipt copied into a second OwnerCredential
	// principal, and an event claiming that second principal. A receipt's
	// message ID is not a cross-principal authorization token.
	second := snapshot.OwnerCredentials["owner-1"]
	second.PrincipalID = "owner-2"
	snapshot.OwnerCredentials["owner-2"] = second
	changed := false
	for i := range snapshot.AssignmentLedger.Events {
		e := &snapshot.AssignmentLedger.Events[i]
		if e.Kind == "COORDINATOR_LOST" && e.AssignmentID == "asg-n03-b" {
			e.OwnerPrincipalID = "owner-2"
			e.Actor.SessionID = "ownercredential:owner-2"
			e.Actor.AgentID = "owner-2"
			changed = true
			break
		}
	}
	if !changed {
		t.Fatal("setup has no second matching OWNER loss event")
	}
	if err := ValidateCanonicalState(snapshot); err == nil || !strings.Contains(err.Error(), "reused across loss contexts") {
		t.Fatalf("cross-principal receipt reuse err=%v", err)
	}
}

func TestRecoveryA93BMutationKillers_HandoffGateRequiresExactLossBinding(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-n15", now)
	seedDelegatedAssignment(t, f, "asg-n15", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now)
	lossConfirm(t, f, "del-n15", "loss-del-n15", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1}, now)
	state, err := copyCanonicalState(f.s.state)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateCanonicalState(state); err != nil {
		t.Fatalf("positive exact-binding control invalid: %v", err)
	}
	if !ownerCredentialLossAllowsHandoffWithoutObserver(state, "asg-n15") {
		t.Fatal("exact same-commit owner loss did not enable the harness handoff gate")
	}

	for _, tc := range []struct {
		name   string
		mutate func(*CanonicalState)
	}{
		{"coordinator_triple", func(s *CanonicalState) {
			receipt := s.OwnerCredentials["owner-1"].Receipts["loss-del-n15"]
			receipt.OwnerLossBinding.SessionID = "other-session"
			agg := s.OwnerCredentials["owner-1"]
			agg.Receipts["loss-del-n15"] = receipt
			s.OwnerCredentials["owner-1"] = agg
		}},
		{"assignment_membership", func(s *CanonicalState) {
			receipt := s.OwnerCredentials["owner-1"].Receipts["loss-del-n15"]
			receipt.OwnerLossBinding.AssignmentIDs = []string{"other-assignment"}
			agg := s.OwnerCredentials["owner-1"]
			agg.Receipts["loss-del-n15"] = receipt
			s.OwnerCredentials["owner-1"] = agg
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mutated, err := copyCanonicalState(state)
			if err != nil {
				t.Fatal(err)
			}
			tc.mutate(&mutated)
			if ownerCredentialLossAllowsHandoffWithoutObserver(mutated, "asg-n15") {
				t.Fatal("handoff gate accepted a loss receipt bound to another context")
			}
		})
	}
}

func TestRecoveryA93B1_AssignmentIDsAreSignedReplayBoundAndSnapshotBound(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-assignment-digest", now)
	seedDelegatedAssignment(t, f, "asg-assignment-digest", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now)
	coord := ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1}
	makePayload := func(ids []string) []byte {
		body, err := json.Marshal(ownercredential.LossConfirmPayload{DelegationID: "del-assignment-digest", Coordinator: coord, AssignmentIDs: ids, Reason: "signed set", ObservedAt: now})
		if err != nil {
			t.Fatal(err)
		}
		return body
	}
	encoded := makePayload([]string{"asg-assignment-digest"})
	var decoded ownercredential.LossConfirmPayload
	if err := json.Unmarshal(encoded, &decoded); err != nil || len(decoded.AssignmentIDs) != 1 || decoded.AssignmentIDs[0] != "asg-assignment-digest" {
		t.Fatalf("AssignmentIDs JSON round trip: %+v, err=%v", decoded, err)
	}
	cmd := f.command(ownercredential.CommandLossConfirm, "loss-assignment-digest", "owner-primary", f.s.state.OwnerCredentials["owner-1"].CredentialRevision, encoded)
	beforeDigest, err := cmd.DigestBytes()
	if err != nil {
		t.Fatal(err)
	}
	changed := cmd
	changed.Payload = makePayload([]string{"different-assignment"})
	afterDigest, err := changed.DigestBytes()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(beforeDigest, afterDigest) {
		t.Fatal("changing signed AssignmentIDs did not change the command digest")
	}

	assertion := assertionFor(cmd, f.private["owner-primary"], f.s.state.OwnerCredentials["owner-1"].Credentials["owner-primary"].SignCount+1)
	first, replayed, err := f.s.ApplyOwnerCredentialCommand(cmd.ChallengeID, cmd, assertion)
	if err != nil || replayed {
		t.Fatalf("first exact loss command receipt=%+v replayed=%v err=%v", first, replayed, err)
	}
	saves := f.p.saveCalls
	replayedReceipt, replayed, err := f.s.ApplyOwnerCredentialCommand(cmd.ChallengeID, cmd, assertion)
	if err != nil || !replayed || !reflect.DeepEqual(first, replayedReceipt) || f.p.saveCalls != saves {
		t.Fatalf("exact byte retry receipt=%+v replayed=%v saves=%d/%d err=%v", replayedReceipt, replayed, f.p.saveCalls, saves, err)
	}
	snapshot, err := copyCanonicalState(f.s.state)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateCanonicalState(snapshot); err != nil {
		t.Fatalf("valid signed-set snapshot rejected: %v", err)
	}
	stored := snapshot.OwnerCredentials["owner-1"].Receipts["loss-assignment-digest"].OwnerLossBinding
	if stored == nil || !reflect.DeepEqual(stored.AssignmentIDs, []string{"asg-assignment-digest"}) {
		t.Fatalf("snapshot lost the signed assignment set: %+v", stored)
	}
}

func TestRecoveryA93B1_AssignmentIDsMissingOrEmptyFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name string
		ids  []string
	}{
		{name: "missing_field", ids: nil},
		{name: "empty_field", ids: []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			enableDelegationHarness(t)
			f := newOwnerCommandFixture(t)
			now := f.s.now().UTC()
			grantDelegation(t, f, "del-missing-ids", now)
			seedDelegatedAssignment(t, f, "asg-missing-ids", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now)
			payloadValue := map[string]any{
				"delegationId": "del-missing-ids",
				"coordinator":  ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1},
				"reason":       "missing signed set",
				"observedAt":   now,
			}
			if tc.name != "missing_field" {
				payloadValue["assignmentIds"] = tc.ids
			}
			payload, err := json.Marshal(payloadValue)
			if err != nil {
				t.Fatal(err)
			}
			cmd := f.command(ownercredential.CommandLossConfirm, "loss-missing-ids", "owner-primary", f.s.state.OwnerCredentials["owner-1"].CredentialRevision, payload)
			before := ownerStateBytes(t, f.s)
			saves := f.p.saveCalls
			sign := f.s.state.OwnerCredentials["owner-1"].Credentials["owner-primary"].SignCount + 1
			if _, _, err := f.s.ApplyOwnerCredentialCommand(cmd.ChallengeID, cmd, assertionFor(cmd, f.private["owner-primary"], sign)); err == nil || !strings.Contains(err.Error(), "no open delegated work") && !strings.Contains(err.Error(), "assignmentIds") {
				t.Fatalf("missing/empty assignment set err=%v", err)
			}
			if saves != f.p.saveCalls || !bytes.Equal(before, ownerStateBytes(t, f.s)) {
				t.Fatal("missing/empty signed assignment set changed canonical state")
			}
		})
	}
}

func TestRecoveryA93BMutationKillers_UnconsumedVisibilityCannotBeInvented(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-n10", now)
	confirmVisibility(t, f, "del-n10", "vis-n10", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-b", SessionID: "codex:b", Generation: 1}, now)
	snapshot, err := copyCanonicalState(f.s.state)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateCanonicalState(snapshot); err != nil {
		t.Fatalf("positive unconsumed visibility control invalid: %v", err)
	}
	d := snapshot.CoordinatorDelegations["del-n10"]
	if len(d.VisibilityReceipts) != 1 || !d.VisibilityReceipts[0].ConsumedAt.IsZero() {
		t.Fatalf("setup visibility is not fresh/unconsumed: %+v", d.VisibilityReceipts)
	}
	d.VisibilityReceipts[0].ConsumedAt = now
	d.VisibilityReceipts[0].ConsumedBy = "forged-consumer"
	snapshot.CoordinatorDelegations[d.DelegationID] = d
	if err := ValidateCanonicalState(snapshot); err == nil || !strings.Contains(err.Error(), "visibility consumption differs from history") {
		t.Fatalf("fabricated visibility consumption err=%v, want exact derived-state rejection", err)
	}
}

func TestRecoveryA93BMutationKillers_GrantReceiptKindRequired(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-n11", now)
	snapshot, err := copyCanonicalState(f.s.state)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateCanonicalState(snapshot); err != nil {
		t.Fatalf("positive grant receipt control invalid: %v", err)
	}
	d := snapshot.CoordinatorDelegations["del-n11"]
	grantID := d.History[0].ReceiptID
	agg := snapshot.OwnerCredentials[d.History[0].Actor]
	receipt, ok := agg.Receipts[grantID]
	if !ok || receipt.CommandKind != ownercredential.CommandDelegationGrant {
		t.Fatalf("grant receipt setup invalid: %+v, present=%v", receipt, ok)
	}
	receipt.CommandKind = ownercredential.CommandVisibilityConfirm
	agg.Receipts[grantID] = receipt
	snapshot.OwnerCredentials[d.History[0].Actor] = agg
	if err := ValidateCanonicalState(snapshot); err == nil || !strings.Contains(err.Error(), "delegation history receipt mismatch") {
		t.Fatalf("grant history without GRANT receipt kind err=%v", err)
	}
}

func TestRecoveryA93BMutationKillers_PrimaryRestartLossConsumption(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-n12", now)
	seedDelegatedAssignment(t, f, "asg-n12", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now)
	lossConfirm(t, f, "del-n12", "loss-del-n12", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1}, now)
	registerSession(t, f, "claude:a2", "agent-a")
	bumpSessionGeneration(t, f, "claude:a2", 2)
	confirmVisibility(t, f, "del-n12", "vis-n12-a2", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a2", Generation: 2}, now)
	if err := f.s.ApplyPrimaryRestart("del-n12", assignment.Principal{SessionID: "claude:a2", AgentID: "agent-a", Generation: 2}, "restart-n12-a2"); err != nil {
		t.Fatalf("positive first restart: %v", err)
	}
	registerSession(t, f, "claude:a3", "agent-a")
	bumpSessionGeneration(t, f, "claude:a3", 3)
	confirmVisibility(t, f, "del-n12", "vis-n12-a3", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a3", Generation: 3}, now)
	snapshot, err := copyCanonicalState(f.s.state)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateCanonicalState(snapshot); err != nil {
		t.Fatalf("positive restart snapshot control invalid: %v", err)
	}
	d := snapshot.CoordinatorDelegations["del-n12"]
	var first *DelegationHistoryEntry
	for i := range d.History {
		if d.History[i].Kind == DelegationHistoryPrimaryRestart {
			first = &d.History[i]
			break
		}
	}
	if first == nil {
		t.Fatal("setup has no primary restart")
	}
	to := DelegationPrincipal{StableAgentID: "agent-a", SessionID: "claude:a3", Generation: 3}
	for i := range d.VisibilityReceipts {
		if d.VisibilityReceipts[i].MessageID == "vis-n12-a3" {
			d.VisibilityReceipts[i].ConsumedAt = now
			d.VisibilityReceipts[i].ConsumedBy = "restart-n12-a3"
		}
	}
	d.History = append(d.History, DelegationHistoryEntry{
		Kind: DelegationHistoryPrimaryRestart, At: now, Actor: "claude:a3", Reason: "restart-n12-a3",
		AssignmentID: first.AssignmentID, From: first.From, To: &to, ReceiptID: first.ReceiptID,
		ReceiptDig: "vis-n12-a3", CanonicalRevision: snapshot.Revision,
	})
	d.ActingCoordinator = &to
	snapshot.CoordinatorDelegations[d.DelegationID] = d
	if err := ValidateCanonicalState(snapshot); err == nil {
		t.Fatal("second primary restart reused a consumed loss record")
	}
}

func TestRecoveryA93BMutationKillers_HandoffCannotFollowRevokeWithoutLoss(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-n13", now)
	registerSession(t, f, "codex:b", "agent-b")
	coordB := ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-b", SessionID: "codex:b", Generation: 1}
	confirmVisibility(t, f, "del-n13", "vis-n13", coordB, now)
	payload, _ := json.Marshal(ownercredential.DelegationRevokePayload{DelegationID: "del-n13", Reason: "revoke"})
	cmd := f.command(ownercredential.CommandDelegationRevoke, "revoke-del-n13", "owner-primary", f.s.state.OwnerCredentials["owner-1"].CredentialRevision, payload)
	sign := f.s.state.OwnerCredentials["owner-1"].Credentials["owner-primary"].SignCount + 1
	if _, _, err := f.s.ApplyOwnerCredentialCommand(cmd.ChallengeID, cmd, assertionFor(cmd, f.private["owner-primary"], sign)); err != nil {
		t.Fatalf("valid revoke setup: %v", err)
	}
	snapshot, err := copyCanonicalState(f.s.state)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateCanonicalState(snapshot); err != nil {
		t.Fatalf("positive revoked snapshot control invalid: %v", err)
	}
	d := snapshot.CoordinatorDelegations["del-n13"]
	from := d.PrimaryCoordinator
	to := DelegationPrincipal{StableAgentID: coordB.StableAgentID, SessionID: coordB.SessionID, Generation: coordB.Generation}
	for i := range d.VisibilityReceipts {
		if d.VisibilityReceipts[i].MessageID == "vis-n13" {
			d.VisibilityReceipts[i].ConsumedAt = now
			d.VisibilityReceipts[i].ConsumedBy = "forged-handoff"
		}
	}
	d.History = append(d.History, DelegationHistoryEntry{Kind: DelegationHistoryHandoff, At: now, Actor: from.SessionID, AssignmentID: "forged-handoff", From: &from, To: &to, ReceiptDig: "vis-n13", CanonicalRevision: snapshot.Revision})
	d.ActingCoordinator = &to
	snapshot.CoordinatorDelegations[d.DelegationID] = d
	if err := ValidateCanonicalState(snapshot); err == nil || !strings.Contains(err.Error(), "HANDOFF lacks a corresponding loss") {
		t.Fatalf("handoff after revoke without loss err=%v", err)
	}
}

func TestRecoveryA93BMutationKillers_HistoryCommitRevisionBound(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegationWith(t, f, "del-n14", now, delegationOpts{successors: []ownercredential.DelegationPrincipalPayload{
		{StableAgentID: "agent-b", SessionID: "codex:b", Generation: 1},
		{StableAgentID: "agent-c", SessionID: "gemini:c", Generation: 1},
	}})
	seedDelegatedAssignment(t, f, "asg-n14", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now)
	registerSession(t, f, "codex:b", "agent-b")
	registerSession(t, f, "gemini:c", "agent-c")
	if err := f.s.ReplaceAgentSessionAddress("codex:b", "codex:b2", ed25519.PublicKey(strings.Repeat("k", 32)), "retire first successor"); err != nil {
		t.Fatal(err)
	}
	confirmVisibility(t, f, "del-n14", "vis-n14-b", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-b", SessionID: "codex:b", Generation: 1}, now)
	confirmVisibility(t, f, "del-n14", "vis-n14-c", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-c", SessionID: "gemini:c", Generation: 1}, now)
	lossConfirm(t, f, "del-n14", "loss-n14", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1}, now)
	if err := f.s.HandoffAssignment(assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, assignment.Principal{SessionID: "gemini:c", AgentID: "agent-c", Generation: 1}, "asg-n14", f.s.state.AssignmentLedger.Records["asg-n14"].Revision, "skip-retired-b"); err != nil {
		t.Fatalf("positive handoff after skipping inactive successor: %v", err)
	}
	snapshot, err := copyCanonicalState(f.s.state)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateCanonicalState(snapshot); err != nil {
		t.Fatalf("positive successor-skip snapshot invalid: %v", err)
	}
	d := snapshot.CoordinatorDelegations["del-n14"]
	found := false
	for i := range d.History {
		if d.History[i].Kind == DelegationHistorySuccessorSkipped {
			d.History[i].CanonicalRevision = snapshot.Revision + 1
			found = true
			break
		}
	}
	if !found {
		t.Fatal("setup has no SUCCESSOR_SKIPPED history")
	}
	snapshot.CoordinatorDelegations[d.DelegationID] = d
	if err := ValidateCanonicalState(snapshot); err == nil || !strings.Contains(err.Error(), "history lacks canonical commit binding") {
		t.Fatalf("unbound history revision err=%v", err)
	}
}

func TestRecoveryA93B2_OrphanOwnerLossEventRequiresReceiptOnSnapshot(t *testing.T) {
	enableDelegationHarness(t)
	_, _, snapshot := recoveryLossSnapshot(t, "del-orphan-loss", "asg-orphan-loss")
	if err := ValidateCanonicalState(snapshot); err != nil {
		t.Fatalf("positive committed loss snapshot invalid: %v", err)
	}
	// Keep a structurally valid assignment ledger but remove the delegation's
	// LOSS_RECORDED derivation. The OWNER_CREDENTIAL event must still be rejected
	// unless the canonical snapshot contains its actual OwnerCredential receipt.
	d := snapshot.CoordinatorDelegations["del-orphan-loss"]
	history := make([]DelegationHistoryEntry, 0, len(d.History))
	for _, h := range d.History {
		if h.Kind != DelegationHistoryLossRecorded {
			history = append(history, h)
		}
	}
	d.History = history
	d.BarredCoordinators = nil
	primary := d.PrimaryCoordinator
	d.ActingCoordinator = &primary
	snapshot.CoordinatorDelegations[d.DelegationID] = d
	for i := range snapshot.AssignmentLedger.Events {
		e := &snapshot.AssignmentLedger.Events[i]
		if e.Kind == "COORDINATOR_LOST" && e.OwnerReceiptMessageID == "loss-del-orphan-loss" {
			e.OwnerReceiptMessageID = "missing-loss-receipt"
			break
		}
	}
	if err := ValidateCanonicalState(snapshot); err == nil || !strings.Contains(err.Error(), "OWNER_CREDENTIAL loss receipt missing or mismatched") {
		t.Fatalf("orphan OWNER_CREDENTIAL loss event error=%v, want receipt-specific rejection", err)
	}
	if loaded, err := NewCanonical(&canonicalMemory{state: snapshot}); err == nil {
		if closeErr := loaded.Close(); closeErr != nil {
			t.Errorf("close loaded canonical store: %v", closeErr)
		}
		t.Fatal("NewCanonical loaded orphan OWNER_CREDENTIAL loss event")
	}
}

func TestRecoveryA93B1_HandoffGateRequiresDurableReceipt(t *testing.T) {
	_, _, snapshot := recoveryLossSnapshot(t, "del-handoff-receipt", "asg-handoff-receipt")
	if !ownerCredentialLossAllowsHandoffWithoutObserver(snapshot, "asg-handoff-receipt") {
		t.Fatal("positive committed loss receipt did not enable the exact recovery gate")
	}
	delete(snapshot.OwnerCredentials["owner-1"].Receipts, "loss-del-handoff-receipt")
	if ownerCredentialLossAllowsHandoffWithoutObserver(snapshot, "asg-handoff-receipt") {
		t.Fatal("handoff gate accepted OWNER loss after its receipt was removed")
	}
}

func TestRecoveryA93B1_HandoffGateRejectsAgentSessionLoss(t *testing.T) {
	_, _, snapshot := recoveryLossSnapshot(t, "del-handoff-source", "asg-handoff-source")
	if !ownerCredentialLossAllowsHandoffWithoutObserver(snapshot, "asg-handoff-source") {
		t.Fatal("positive OwnerCredential loss control did not enable recovery")
	}
	for i := range snapshot.AssignmentLedger.Events {
		e := &snapshot.AssignmentLedger.Events[i]
		if e.AssignmentID == "asg-handoff-source" && e.Kind == "COORDINATOR_LOST" {
			e.ActorSource = assignment.ActorSourceAgentSession
			break
		}
	}
	if ownerCredentialLossAllowsHandoffWithoutObserver(snapshot, "asg-handoff-source") {
		t.Fatal("handoff gate accepted an AgentSession loss as OWNER-authorized")
	}
}

func TestRecoveryA93B1_AssignmentLedgerRequiresOwnerLossReceiptFields(t *testing.T) {
	enableDelegationHarness(t)
	_, _, snapshot := recoveryLossSnapshot(t, "del-owner-fields", "asg-owner-fields")
	if err := assignment.Validate(snapshot.AssignmentLedger); err != nil {
		t.Fatalf("positive complete OwnerCredential loss ledger rejected: %v", err)
	}
	for i := range snapshot.AssignmentLedger.Events {
		e := &snapshot.AssignmentLedger.Events[i]
		if e.AssignmentID == "asg-owner-fields" && e.Kind == "COORDINATOR_LOST" {
			e.OwnerCredentialID = ""
			break
		}
	}
	if err := assignment.Validate(snapshot.AssignmentLedger); err == nil || !strings.Contains(err.Error(), "OWNER_CREDENTIAL loss requires receipt binding") {
		t.Fatalf("OWNER_CREDENTIAL event without credential id err=%v", err)
	}
}

func TestRecoveryA93BMutationKillers_ReceiptAndLossEventAssignmentSetsAreExact(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-set-drift", now)
	coord := assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}
	seedDelegatedAssignment(t, f, "asg-set-a", coord, now)
	seedDelegatedAssignment(t, f, "asg-set-b", coord, now)
	lossConfirm(t, f, "del-set-drift", "loss-set-drift", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1}, now)
	snapshot, err := copyCanonicalState(f.s.state)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateCanonicalState(snapshot); err != nil {
		t.Fatalf("positive two-assignment exact-set control invalid: %v", err)
	}
	var eventAssignments []string
	for _, event := range snapshot.AssignmentLedger.Events {
		if event.Kind == "COORDINATOR_LOST" && event.OwnerReceiptMessageID == "loss-set-drift" {
			eventAssignments = append(eventAssignments, event.AssignmentID)
		}
	}
	sort.Strings(eventAssignments)
	if !reflect.DeepEqual(eventAssignments, []string{"asg-set-a", "asg-set-b"}) {
		t.Fatalf("setup loss event set=%v, want both signed assignments", eventAssignments)
	}
	agg := snapshot.OwnerCredentials["owner-1"]
	receipt := agg.Receipts["loss-set-drift"]
	receipt.OwnerLossBinding.AssignmentIDs = []string{"asg-set-a"}
	agg.Receipts[receipt.MessageID] = receipt
	snapshot.OwnerCredentials["owner-1"] = agg
	d := snapshot.CoordinatorDelegations["del-set-drift"]
	for i := range d.History {
		if d.History[i].Kind == DelegationHistoryLossRecorded {
			d.History[i].AssignmentIDs = []string{"asg-set-a"}
			d.History[i].AssignmentID = "asg-set-a"
		}
	}
	snapshot.CoordinatorDelegations[d.DelegationID] = d
	if err := ValidateCanonicalState(snapshot); err == nil {
		t.Fatal("receipt/history assignment-set substitution accepted")
	}
}

func TestRecoveryA93BMutationKillers_VisibilityHistoryCannotOutnumberReceipts(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-visibility-count", now)
	successor := ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-b", SessionID: "codex:b", Generation: 1}
	confirmVisibility(t, f, "del-visibility-count", "vis-count-one", successor, now)
	confirmVisibility(t, f, "del-visibility-count", "vis-count-two", successor, now)
	snapshot, err := copyCanonicalState(f.s.state)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateCanonicalState(snapshot); err != nil {
		t.Fatalf("positive two-receipt visibility control invalid: %v", err)
	}
	d := snapshot.CoordinatorDelegations["del-visibility-count"]
	if len(d.VisibilityReceipts) != 2 {
		t.Fatalf("setup has %d visibility receipts, want 2", len(d.VisibilityReceipts))
	}
	d.VisibilityReceipts = d.VisibilityReceipts[:1]
	snapshot.CoordinatorDelegations[d.DelegationID] = d
	if err := ValidateCanonicalState(snapshot); err == nil || !strings.Contains(err.Error(), "visibility receipts differ from Owner history") {
		t.Fatalf("orphan visibility history error=%v, want exact count mismatch", err)
	}
}

func TestRecoveryA93B2_OwnerCommandWriterRejectsInvalidPreexistingSnapshot(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-writer-validation", now)
	valid := f.s.state.CoordinatorDelegations["del-writer-validation"]
	if err := ValidateCanonicalState(f.s.state); err != nil {
		t.Fatalf("positive pre-command state invalid: %v", err)
	}
	outsider := DelegationPrincipal{StableAgentID: "foreign", SessionID: "foreign:session", Generation: 1}
	valid.ActingCoordinator = &outsider
	f.s.state.CoordinatorDelegations[valid.DelegationID] = valid
	before := ownerStateBytes(t, f.s)
	saves := f.p.saveCalls
	payload, err := json.Marshal(ownercredential.VisibilityConfirmPayload{
		DelegationID: valid.DelegationID,
		Successor:    &ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-b", SessionID: "codex:b", Generation: 1},
		IssuedAt:     now, ExpiresAt: now.Add(time.Hour), SingleUse: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	revision := f.s.state.OwnerCredentials["owner-1"].CredentialRevision
	cmd := f.command(ownercredential.CommandVisibilityConfirm, "vis-invalid-prior", "owner-primary", revision, payload)
	sign := f.s.state.OwnerCredentials["owner-1"].Credentials["owner-primary"].SignCount + 1
	_, _, err = f.s.ApplyOwnerCredentialCommand(cmd.ChallengeID, cmd, assertionFor(cmd, f.private["owner-primary"], sign))
	if err == nil || !strings.Contains(err.Error(), "acting coordinator differs from history") {
		t.Fatalf("OWNER command writer accepted invalid preexisting delegation state: %v", err)
	}
	if f.p.saveCalls != saves || !bytes.Equal(before, ownerStateBytes(t, f.s)) {
		t.Fatal("OWNER command writer saved/published after rejecting invalid preexisting state")
	}
}

func TestRecoveryA93B2_DisabledReloadRejectsRealOwnerLossSnapshot(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-disabled-owner-loss", now)
	seedDelegatedAssignment(t, f, "asg-disabled-owner-loss", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now)
	lossConfirm(t, f, "del-disabled-owner-loss", "loss-disabled-owner-loss", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1}, now)
	snapshot, err := copyCanonicalState(f.s.state)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateCanonicalState(snapshot); err != nil {
		t.Fatalf("positive harness snapshot invalid: %v", err)
	}
	delegationHarnessEnabled = false
	if err := ValidateCanonicalState(snapshot); !errors.Is(err, ErrCoordinatorDelegationDisabled) {
		t.Fatalf("disabled production reload of real grant/loss snapshot err=%v", err)
	}
}

func TestRecoveryA93B2_SnapshotRejectsMalformedDelegationRecord(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-malformed-record", now)
	snapshot, err := copyCanonicalState(f.s.state)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateCanonicalState(snapshot); err != nil {
		t.Fatalf("positive valid grant snapshot rejected: %v", err)
	}
	d := snapshot.CoordinatorDelegations["del-malformed-record"]
	d.Validity.ExpiresAt = time.Time{}
	snapshot.CoordinatorDelegations[d.DelegationID] = d
	if err := ValidateCanonicalState(snapshot); err == nil || !strings.Contains(err.Error(), "validity") {
		t.Fatalf("malformed delegation validity error=%v, want record Validate rejection", err)
	}
}
