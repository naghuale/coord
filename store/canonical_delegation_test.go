package store

import (
	"bytes"
	"coord/assignment"
	"coord/ownercredential"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestCoordinatorDelegationRemainsDisabledByDefault(t *testing.T) {
	if CoordinatorDelegationEnabled {
		t.Fatal("production coordinator delegation must remain disabled before explicit cutover")
	}
	if coordinatorDelegationActive() {
		t.Fatal("delegation harness must be off by default")
	}
}

func TestDelegationCommandsRejectedWithoutWriteWhenDisabled(t *testing.T) {
	f := newOwnerCommandFixture(t)
	now := time.Now().UTC()
	payload, err := json.Marshal(ownercredential.DelegationGrantPayload{
		DelegationID: "del-1",
		Scope: ownercredential.DelegationScopePayload{
			Repository: "naghuale/coord", Project: "coord",
			AllowedWorkItems: []string{"issue-93b"}, AllowedActions: []string{"OFFER", "EXPIRE"},
		},
		PrimaryCoordinator: ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1},
		Validity:           ownercredential.DelegationValidityPayload{NotBefore: now, ExpiresAt: now.Add(time.Hour)},
		Successors: []ownercredential.DelegationPrincipalPayload{
			{StableAgentID: "agent-b", SessionID: "codex:b", Generation: 1},
		},
		Evidence: ownercredential.DelegationEvidencePayload{OwnerDecisionRef: "ADR-0010", VisibilityEvidence: "owner-seen"},
	})
	if err != nil {
		t.Fatal(err)
	}
	command := f.command(ownercredential.CommandDelegationGrant, "msg-grant-disabled", "owner-primary", 1, payload)
	assertOwnerCommandRejectedWithoutWrite(t, f, command, assertionFor(command, f.private["owner-primary"], 1), ErrCoordinatorDelegationDisabled)

	for _, kind := range []string{
		ownercredential.CommandDelegationRevoke,
		ownercredential.CommandLossConfirm,
		ownercredential.CommandVisibilityConfirm,
	} {
		body, _ := json.Marshal(map[string]any{"delegationId": "del-1", "reason": "x", "observedAt": now, "issuedAt": now, "expiresAt": now.Add(time.Minute), "singleUse": true})
		cmd := f.command(kind, "msg-"+kind, "owner-primary", 1, body)
		assertOwnerCommandRejectedWithoutWrite(t, f, cmd, assertionFor(cmd, f.private["owner-primary"], 1), ErrCoordinatorDelegationDisabled)
	}
}

func TestDelegationGrantRevokeExactRetryAndCutoverInert(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	payload, err := json.Marshal(ownercredential.DelegationGrantPayload{
		DelegationID: "del-grant-1",
		Scope: ownercredential.DelegationScopePayload{
			Repository: "naghuale/coord", Project: "coord",
			AllowedWorkItems: []string{"issue-93b"}, AllowedActions: []string{"OFFER", "VOID", "EXPIRE"},
		},
		PrimaryCoordinator: ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1},
		Validity:           ownercredential.DelegationValidityPayload{NotBefore: now.Add(-time.Minute), ExpiresAt: now.Add(2 * time.Hour)},
		Successors: []ownercredential.DelegationPrincipalPayload{
			{StableAgentID: "agent-b", SessionID: "codex:b", Generation: 1},
		},
		PrimaryRestart:       &ownercredential.PrimaryRestartPayload{StableAgentID: "agent-a"},
		Evidence:             ownercredential.DelegationEvidencePayload{OwnerDecisionRef: "ADR-0010-ACCEPTANCE", VisibilityEvidence: "owner-confirm-1"},
		CutoverAssignmentIDs: []string{"asg-cutover-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	command := f.command(ownercredential.CommandDelegationGrant, "msg-grant-1", "owner-primary", 1, payload)
	receipt, replayed, err := f.s.ApplyOwnerCredentialCommand(command.ChallengeID, command, assertionFor(command, f.private["owner-primary"], 1))
	if err != nil || replayed || receipt.CommandKind != ownercredential.CommandDelegationGrant {
		t.Fatalf("grant err=%v replayed=%v receipt=%+v", err, replayed, receipt)
	}
	snap, err := f.s.CanonicalSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	rec, ok := snap.CoordinatorDelegations["del-grant-1"]
	if !ok || !rec.Active(now) || len(rec.CutoverAssignmentIDs) != 1 || rec.CutoverAssignmentIDs[0] != "asg-cutover-1" {
		t.Fatalf("grant not stored: %+v", rec)
	}
	if CoordinatorDelegationEnabled {
		t.Fatal("cutover ids must remain inert while production const is false")
	}
	savesAfterGrant := f.p.saveCalls
	retry, replayed, err := f.s.ApplyOwnerCredentialCommand(command.ChallengeID, command, assertionFor(command, f.private["owner-primary"], 1))
	if err != nil || !replayed || retry.MessageID != receipt.MessageID || retry.CommandDigest != receipt.CommandDigest {
		t.Fatalf("exact retry err=%v replayed=%v retry=%+v", err, replayed, retry)
	}
	if f.p.saveCalls != savesAfterGrant {
		t.Fatalf("exact retry saved again: before=%d after=%d", savesAfterGrant, f.p.saveCalls)
	}

	revokePayload, _ := json.Marshal(ownercredential.DelegationRevokePayload{DelegationID: "del-grant-1", Reason: "owner-revoke"})
	revoke := f.command(ownercredential.CommandDelegationRevoke, "msg-revoke-1", "owner-primary", 1, revokePayload)
	if _, _, err := f.s.ApplyOwnerCredentialCommand(revoke.ChallengeID, revoke, assertionFor(revoke, f.private["owner-primary"], 2)); err != nil {
		t.Fatal(err)
	}
	snap, _ = f.s.CanonicalSnapshot()
	if snap.CoordinatorDelegations["del-grant-1"].RevokedAt.IsZero() {
		t.Fatal("revoke did not mark revokedAt")
	}
}

func TestLossConfirmBarsStillRegisteredAndRequiresOpenWork(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-loss-1", now)

	// No open work -> reject.
	lossPayload, _ := json.Marshal(ownercredential.LossConfirmPayload{
		DelegationID: "del-loss-1",
		Coordinator:  ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1},
		Reason:       "hung", ObservedAt: now,
	})
	loss := f.command(ownercredential.CommandLossConfirm, "msg-loss-none", "owner-primary", 1, lossPayload)
	assertOwnerCommandRejectedWithoutWrite(t, f, loss, assertionFor(loss, f.private["owner-primary"], 2), ErrCoordinatorDelegationInvalid)

	// Seed an open assignment under the primary.
	seedDelegatedAssignment(t, f, "asg-1", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now)
	lossPayload, _ = json.Marshal(ownercredential.LossConfirmPayload{
		DelegationID:  "del-loss-1",
		Coordinator:   ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1},
		AssignmentIDs: []string{"asg-1"}, Reason: "hung", ObservedAt: now,
	})
	loss2 := f.command(ownercredential.CommandLossConfirm, "msg-loss-ok", "owner-primary", 1, lossPayload)
	if _, _, err := f.s.ApplyOwnerCredentialCommand(loss2.ChallengeID, loss2, assertionFor(loss2, f.private["owner-primary"], 2)); err != nil {
		t.Fatal(err)
	}
	snap, _ := f.s.CanonicalSnapshot()
	rec := snap.CoordinatorDelegations["del-loss-1"]
	if !rec.IsBarred(DelegationPrincipal{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1}) {
		t.Fatal("still-registered coordinator was not barred")
	}
	r := snap.AssignmentLedger.Records["asg-1"]
	if !r.CoordinatorLost {
		t.Fatal("assignment not marked coordinatorLost")
	}
	var lost assignment.Event
	for i := len(snap.AssignmentLedger.Events) - 1; i >= 0; i-- {
		if snap.AssignmentLedger.Events[i].Kind == "COORDINATOR_LOST" {
			lost = snap.AssignmentLedger.Events[i]
			break
		}
	}
	if lost.ActorSource != assignment.ActorSourceOwnerCredential || lost.OwnerReceiptMessageID != loss2.MessageID || lost.OwnerReceiptDigest != loss2.Digest {
		t.Fatalf("OWNER_CREDENTIAL loss binding missing: %+v", lost)
	}
}

func TestVisibilityConfirmRejectsOffListAndDoesNotChangeScope(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-vis-1", now)
	before, _ := f.s.CanonicalSnapshot()
	beforeScope := before.CoordinatorDelegations["del-vis-1"].Scope

	bad, _ := json.Marshal(ownercredential.VisibilityConfirmPayload{
		DelegationID: "del-vis-1",
		Successor:    &ownercredential.DelegationPrincipalPayload{StableAgentID: "outsider", SessionID: "x:y", Generation: 9},
		IssuedAt:     now, ExpiresAt: now.Add(time.Minute), SingleUse: true,
	})
	cmd := f.command(ownercredential.CommandVisibilityConfirm, "msg-vis-bad", "owner-primary", 1, bad)
	assertOwnerCommandRejectedWithoutWrite(t, f, cmd, assertionFor(cmd, f.private["owner-primary"], 2), ErrCoordinatorDelegationInvalid)

	okPayload, _ := json.Marshal(ownercredential.VisibilityConfirmPayload{
		DelegationID: "del-vis-1",
		Successor:    &ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-b", SessionID: "codex:b", Generation: 1},
		IssuedAt:     now, ExpiresAt: now.Add(time.Minute), SingleUse: true,
	})
	okCmd := f.command(ownercredential.CommandVisibilityConfirm, "msg-vis-ok", "owner-primary", 1, okPayload)
	if _, _, err := f.s.ApplyOwnerCredentialCommand(okCmd.ChallengeID, okCmd, assertionFor(okCmd, f.private["owner-primary"], 2)); err != nil {
		t.Fatal(err)
	}
	after, _ := f.s.CanonicalSnapshot()
	rec := after.CoordinatorDelegations["del-vis-1"]
	if len(rec.VisibilityReceipts) != 1 || !bytes.Equal(mustJSON(t, rec.Scope), mustJSON(t, beforeScope)) {
		t.Fatalf("visibility changed scope or failed to store receipt: %+v", rec)
	}
	if !rec.Validity.ExpiresAt.Equal(before.CoordinatorDelegations["del-vis-1"].Validity.ExpiresAt) || len(rec.Successors) != len(before.CoordinatorDelegations["del-vis-1"].Successors) {
		t.Fatal("visibility.confirm must not change expiry or successor list")
	}
}

func TestSnapshotReloadRejectsOwnerCredentialLossWithoutReceipt(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*CanonicalState)
	}{
		{"receipt_missing", func(s *CanonicalState) { delete(s.OwnerCredentials["owner-1"].Receipts, "loss-no-receipt") }},
		{"receipt_digest_mismatch", func(s *CanonicalState) {
			a := s.OwnerCredentials["owner-1"]
			r := a.Receipts["loss-no-receipt"]
			r.CommandDigest = strings.Repeat("f", 64)
			a.Receipts["loss-no-receipt"] = r
			s.OwnerCredentials["owner-1"] = a
		}},
		{"receipt_kind_mismatch", func(s *CanonicalState) {
			a := s.OwnerCredentials["owner-1"]
			r := a.Receipts["loss-no-receipt"]
			r.CommandKind = ownercredential.CommandDelegationGrant
			a.Receipts["loss-no-receipt"] = r
			s.OwnerCredentials["owner-1"] = a
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			enableDelegationHarness(t)
			f := newOwnerCommandFixture(t)
			now := f.s.now().UTC()
			grantDelegation(t, f, "del-no-receipt", now)
			seedDelegatedAssignment(t, f, "asg-no-receipt", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now)
			lossConfirm(t, f, "del-no-receipt", "loss-no-receipt", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1}, now)
			snapshot, err := copyCanonicalState(f.s.state)
			if err != nil {
				t.Fatal(err)
			}
			if err := ValidateCanonicalState(snapshot); err != nil {
				t.Fatalf("positive receipt control rejected: %v", err)
			}
			tc.mutate(&snapshot)
			if err := ValidateCanonicalState(snapshot); !errors.Is(err, ErrCoordinatorDelegationInvalid) {
				t.Fatalf("loss with missing or mismatched OwnerCredential receipt err=%v, want delegation invalid", err)
			}
			if _, err := NewCanonical(&canonicalMemory{state: snapshot}); !errors.Is(err, ErrCoordinatorDelegationInvalid) {
				t.Fatalf("NewCanonical loss without valid OwnerCredential receipt err=%v, want delegation invalid", err)
			}
		})
	}
}

func grantDelegation(t *testing.T, f *ownerCommandFixture, id string, now time.Time) {
	t.Helper()
	payload, err := json.Marshal(ownercredential.DelegationGrantPayload{
		DelegationID: id,
		Scope: ownercredential.DelegationScopePayload{
			Repository: "naghuale/coord", Project: "coord",
			AllowedWorkItems: []string{"issue-93b"}, AllowedActions: []string{"OFFER", "VOID", "EXPIRE", "HANDOFF"},
		},
		PrimaryCoordinator: ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1},
		Validity:           ownercredential.DelegationValidityPayload{NotBefore: now.Add(-time.Minute), ExpiresAt: now.Add(2 * time.Hour)},
		Successors: []ownercredential.DelegationPrincipalPayload{
			{StableAgentID: "agent-b", SessionID: "codex:b", Generation: 1},
		},
		PrimaryRestart: &ownercredential.PrimaryRestartPayload{StableAgentID: "agent-a"},
		Evidence:       ownercredential.DelegationEvidencePayload{OwnerDecisionRef: "ADR-0010", VisibilityEvidence: "owner-seen"},
	})
	if err != nil {
		t.Fatal(err)
	}
	rev := f.s.state.OwnerCredentials["owner-1"].CredentialRevision
	command := f.command(ownercredential.CommandDelegationGrant, "grant-"+id, "owner-primary", rev, payload)
	signCount := f.s.state.OwnerCredentials["owner-1"].Credentials["owner-primary"].SignCount + 1
	if _, _, err := f.s.ApplyOwnerCredentialCommand(command.ChallengeID, command, assertionFor(command, f.private["owner-primary"], signCount)); err != nil {
		t.Fatal(err)
	}
}

func seedDelegatedAssignment(t *testing.T, f *ownerCommandFixture, id string, coordinator assignment.Principal, now time.Time) {
	t.Helper()
	target := assignment.Principal{SessionID: "codex:worker", AgentID: "worker", Generation: 1}
	for i, p := range []assignment.Principal{coordinator, target} {
		if _, ok := f.s.state.AgentSessions[p.SessionID]; ok {
			continue
		}
		key := ed25519.PublicKey(strings.Repeat(string(rune('a'+i)), 32))
		if err := f.s.RegisterAgentSession(p.SessionID, p.AgentID, key); err != nil {
			t.Fatal(err)
		}
	}
	sha := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if err := f.s.UpdateCanonical(func(state *CanonicalState) error {
		if state.AssignmentLedger == nil {
			state.AssignmentLedger = assignment.Empty()
		}
		grant, underGrant := findGrantForActingCoordinator(*state, coordinator, now)
		offered := assignment.Record{
			AssignmentID: id, State: assignment.Offered,
			Issuer: coordinator.SessionID, IssuerAgentID: coordinator.AgentID, IssuerGeneration: coordinator.Generation,
			CurrentCoordinator: coordinator.SessionID, CoordinatorAgentID: coordinator.AgentID, CoordinatorGeneration: coordinator.Generation,
			HandoffEvidence: []assignment.Handoff{},
			Assignee:        target.SessionID, StableAgentID: target.AgentID, SessionID: target.SessionID, AssigneeGeneration: target.Generation,
			Role: "IMPLEMENTER", SubjectSHA: sha,
			IssuedAt: now.Add(-time.Minute), ValidUntil: now.Add(time.Hour),
			AdmissionBasis: assignment.AdmissionBasis{Kind: "POOL", Reference: "issue-93b", TargetSessionID: target.SessionID, TargetAgentID: target.AgentID, Role: "IMPLEMENTER", SubjectSHA: sha},
			DelegationBinding: func() *assignment.DelegationBinding {
				if !underGrant {
					return nil
				}
				return &assignment.DelegationBinding{GrantID: grant.DelegationID, GrantGeneration: grant.Generation, Repository: grant.Scope.Repository, Project: grant.Scope.Project, AllowedWorkItem: "issue-93b"}
			}(),
			VisibilityEvidence: assignment.VisibilityEvidence{Reference: "vis", SessionID: target.SessionID, ObservedAt: now.Add(-time.Minute)},
			Revision:           1, UpdatedAt: now.Add(-time.Minute),
		}
		if err := state.AssignmentLedger.Append(assignment.Event{AssignmentID: id, Revision: 1, Kind: "OFFER", Actor: coordinator, EvidenceRef: "seed-offer", At: offered.UpdatedAt, Record: offered}); err != nil {
			return err
		}
		acked := offered
		acked.State = assignment.Acked
		acked.Revision = 2
		acked.AcknowledgedAt = now.Add(-50 * time.Second)
		acked.UpdatedAt = acked.AcknowledgedAt
		if err := state.AssignmentLedger.Append(assignment.Event{AssignmentID: id, Revision: 2, Kind: "ACK", Actor: target, EvidenceRef: "seed-ack", At: acked.UpdatedAt, Record: acked}); err != nil {
			return err
		}
		active := acked
		active.State = assignment.Active
		active.Revision = 3
		active.UpdatedAt = now.Add(-40 * time.Second)
		return state.AssignmentLedger.Append(assignment.Event{AssignmentID: id, Revision: 3, Kind: "ACTIVATE", Actor: coordinator, EvidenceRef: "seed-act", At: active.UpdatedAt, Record: active})
	}); err != nil {
		t.Fatal(err)
	}
}

func offerDelegatedCopy(t *testing.T, f *ownerCommandFixture, newID, sourceID string, now time.Time) {
	t.Helper()
	actor := assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}
	r := f.s.state.AssignmentLedger.Records[sourceID]
	r.AssignmentID = newID
	r.State = assignment.Offered
	r.Revision = 1
	r.AcknowledgedAt = time.Time{}
	r.WorkStartedAt = time.Time{}
	r.AcceptedResult = nil
	r.CoordinatorLost = false
	r.UpdatedAt = now.Add(-time.Minute)
	r.IssuedAt = now.Add(-time.Minute)
	if err := f.s.OfferAssignment(actor, r, "offer-control"); err != nil {
		t.Fatalf("valid OFFER control: %v", err)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestDelegationOverlapGrantRejected(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-a", now)
	payload, _ := json.Marshal(ownercredential.DelegationGrantPayload{
		DelegationID: "del-b",
		Scope: ownercredential.DelegationScopePayload{
			Repository: "naghuale/coord", Project: "coord",
			AllowedWorkItems: []string{"other"}, AllowedActions: []string{"OFFER"},
		},
		PrimaryCoordinator: ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1},
		Validity:           ownercredential.DelegationValidityPayload{NotBefore: now, ExpiresAt: now.Add(time.Hour)},
		Successors:         []ownercredential.DelegationPrincipalPayload{{StableAgentID: "agent-b", SessionID: "codex:b", Generation: 1}},
		Evidence:           ownercredential.DelegationEvidencePayload{OwnerDecisionRef: "x", VisibilityEvidence: "y"},
	})
	rev := f.s.state.OwnerCredentials["owner-1"].CredentialRevision
	cmd := f.command(ownercredential.CommandDelegationGrant, "msg-overlap", "owner-primary", rev, payload)
	sign := f.s.state.OwnerCredentials["owner-1"].Credentials["owner-primary"].SignCount + 1
	if _, _, err := f.s.ApplyOwnerCredentialCommand(cmd.ChallengeID, cmd, assertionFor(cmd, f.private["owner-primary"], sign)); !errors.Is(err, ErrCoordinatorDelegationInvalid) {
		t.Fatalf("overlap err=%v", err)
	}
}
