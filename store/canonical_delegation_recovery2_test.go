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

func TestRecoveryR1_OneLossCoversEachListedAssignment(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-r1-multi", now)
	coord := assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}
	next := assignment.Principal{SessionID: "codex:b", AgentID: "agent-b", Generation: 1}
	seedDelegatedAssignment(t, f, "asg-r1-x", coord, now)
	seedDelegatedAssignment(t, f, "asg-r1-y", coord, now)
	registerSession(t, f, next.SessionID, next.AgentID)
	confirmVisibility(t, f, "del-r1-multi", "vis-r1-x", ownercredential.DelegationPrincipalPayload{StableAgentID: next.AgentID, SessionID: next.SessionID, Generation: next.Generation}, now)
	confirmVisibility(t, f, "del-r1-multi", "vis-r1-y", ownercredential.DelegationPrincipalPayload{StableAgentID: next.AgentID, SessionID: next.SessionID, Generation: next.Generation}, now)
	lossConfirm(t, f, "del-r1-multi", "loss-r1-multi", ownercredential.DelegationPrincipalPayload{StableAgentID: coord.AgentID, SessionID: coord.SessionID, Generation: coord.Generation}, now)

	x := f.s.state.AssignmentLedger.Records["asg-r1-x"]
	if err := f.s.HandoffAssignment(coord, next, x.AssignmentID, x.Revision, "handoff-r1-x"); err != nil {
		t.Fatalf("one_loss_two_assignments: first handoff: %v", err)
	}
	snapshotAfterFirst, err := copyCanonicalState(f.s.state)
	if err != nil {
		t.Fatal(err)
	}
	firstDelegation := snapshotAfterFirst.CoordinatorDelegations["del-r1-multi"]
	var firstHandoff *DelegationHistoryEntry
	for i := range firstDelegation.History {
		if firstDelegation.History[i].Kind == DelegationHistoryHandoff && firstDelegation.History[i].AssignmentID == "asg-r1-x" {
			firstHandoff = &firstDelegation.History[i]
		}
	}
	if firstHandoff == nil {
		t.Fatal("positive control did not record first handoff")
	}
	t.Run("same_assignment_second_handoff_replay_rejected", func(t *testing.T) {
		forged := firstDelegation
		forged.History = append(append([]DelegationHistoryEntry(nil), firstDelegation.History...), *firstHandoff)
		if err := validateDelegationDerivedState(snapshotAfterFirst, forged); !errors.Is(err, ErrCoordinatorDelegationInvalid) || !strings.Contains(err.Error(), "assignment already handed off for loss") {
			t.Fatalf("same assignment was handed off twice for one loss: %v", err)
		}
	})
	beforeDuplicate := ownerStateBytes(t, f.s)
	x = f.s.state.AssignmentLedger.Records["asg-r1-x"]
	if err := f.s.HandoffAssignment(coord, next, x.AssignmentID, x.Revision, "duplicate-handoff-r1-x"); err == nil {
		t.Fatal("same_assignment_second_handoff accepted")
	}
	if !bytes.Equal(beforeDuplicate, ownerStateBytes(t, f.s)) {
		t.Fatal("same_assignment_second_handoff changed canonical state")
	}

	// Reload between sibling handoffs: the history-derived state must retain the
	// unconsumed assignment in the same OWNER loss after the first sibling moved.
	snapshot, err := copyCanonicalState(f.s.state)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateCanonicalState(snapshot); err != nil {
		t.Fatalf("snapshot after first sibling handoff rejected: %v", err)
	}
	loaded, err := NewCanonical(&canonicalMemory{state: snapshot})
	if err != nil {
		t.Fatalf("reload after first sibling handoff: %v", err)
	}
	t.Cleanup(func() {
		if err := loaded.Close(); err != nil {
			t.Errorf("close reloaded canonical store: %v", err)
		}
	})
	y := loaded.state.AssignmentLedger.Records["asg-r1-y"]
	if err := loaded.HandoffAssignment(coord, next, y.AssignmentID, y.Revision, "handoff-r1-y"); err != nil {
		t.Fatalf("one_loss_two_assignments after reload: second handoff: %v", err)
	}
	final, err := copyCanonicalState(loaded.state)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateCanonicalState(final); err != nil {
		t.Fatalf("snapshot after both sibling handoffs rejected: %v", err)
	}
	if _, err := NewCanonical(&canonicalMemory{state: final}); err != nil {
		t.Fatalf("reload after both sibling handoffs: %v", err)
	}
}

func TestRecoveryR1_LossCannotAuthorizeUnlistedAssignment(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-r1-unlisted", now)
	coord := assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}
	next := assignment.Principal{SessionID: "codex:b", AgentID: "agent-b", Generation: 1}
	seedDelegatedAssignment(t, f, "asg-r1-listed", coord, now)
	registerSession(t, f, next.SessionID, next.AgentID)
	confirmVisibility(t, f, "del-r1-unlisted", "vis-r1-unlisted", ownercredential.DelegationPrincipalPayload{StableAgentID: next.AgentID, SessionID: next.SessionID, Generation: next.Generation}, now)
	lossConfirm(t, f, "del-r1-unlisted", "loss-r1-unlisted", ownercredential.DelegationPrincipalPayload{StableAgentID: coord.AgentID, SessionID: coord.SessionID, Generation: coord.Generation}, now)
	t.Run("loss_for_unrelated_assignment_replay_rejected", func(t *testing.T) {
		state, err := copyCanonicalState(f.s.state)
		if err != nil {
			t.Fatal(err)
		}
		grant := state.CoordinatorDelegations["del-r1-unlisted"]
		from, to := principalFromAssignment(coord), principalFromAssignment(next)
		grant.History = append(grant.History, DelegationHistoryEntry{
			Kind: DelegationHistoryHandoff, At: now, AssignmentID: "asg-not-in-signed-loss-set",
			From: &from, To: &to, ReceiptDig: "unrelated-visibility", CanonicalRevision: state.Revision,
		})
		if err := validateDelegationDerivedState(state, grant); !errors.Is(err, ErrCoordinatorDelegationInvalid) || !strings.Contains(err.Error(), "HANDOFF lacks a corresponding loss") {
			t.Fatalf("loss authorized an assignment outside AssignmentIDs: %v", err)
		}
	})

	// Insert a structurally valid, grant-bound assignment that was not in the
	// signed loss assignment set. With the grant now having no acting principal,
	// it must not be handed off as though the OWNER loss covered it.
	source := f.s.state.AssignmentLedger.Records["asg-r1-listed"]
	if err := appendRecoveryR1UnlistedAssignment(f.s, source, "asg-r1-unlisted", now); err != nil {
		t.Fatal(err)
	}
	unlisted := f.s.state.AssignmentLedger.Records["asg-r1-unlisted"]
	before := ownerStateBytes(t, f.s)
	if err := f.s.HandoffAssignment(coord, next, unlisted.AssignmentID, unlisted.Revision, "unlisted-loss-handoff"); !errors.Is(err, ErrCoordinatorDelegationInvalid) {
		t.Fatalf("loss_for_unrelated_assignment err=%v", err)
	}
	if !bytes.Equal(before, ownerStateBytes(t, f.s)) {
		t.Fatal("loss_for_unrelated_assignment changed canonical state")
	}
}

func appendRecoveryR1UnlistedAssignment(s *CanonicalStore, source assignment.Record, id string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	record := source
	record.AssignmentID = id
	record.State = assignment.Offered
	record.Revision = 1
	record.CoordinatorLost = false
	record.AcknowledgedAt = time.Time{}
	record.WorkStartedAt = time.Time{}
	record.AcceptedResult = nil
	record.HandoffEvidence = []assignment.Handoff{}
	record.IssuedAt = now.Add(-time.Minute)
	record.UpdatedAt = record.IssuedAt
	if err := s.state.AssignmentLedger.Append(assignment.Event{AssignmentID: id, Revision: 1, Kind: "OFFER", Actor: record.Coordinator(), EvidenceRef: "recovery-r1-seed-offer", At: record.UpdatedAt, Record: record}); err != nil {
		return err
	}
	acked := record
	acked.State = assignment.Acked
	acked.Revision = 2
	acked.AcknowledgedAt = now.Add(-50 * time.Second)
	acked.UpdatedAt = acked.AcknowledgedAt
	target := acked.Target()
	if err := s.state.AssignmentLedger.Append(assignment.Event{AssignmentID: id, Revision: 2, Kind: "ACK", Actor: target, EvidenceRef: "recovery-r1-seed-ack", At: acked.UpdatedAt, Record: acked}); err != nil {
		return err
	}
	active := acked
	active.State = assignment.Active
	active.Revision = 3
	active.UpdatedAt = now.Add(-40 * time.Second)
	return s.state.AssignmentLedger.Append(assignment.Event{AssignmentID: id, Revision: 3, Kind: "ACTIVATE", Actor: record.Coordinator(), EvidenceRef: "recovery-r1-seed-activate", At: active.UpdatedAt, Record: active})
}

func TestRecoveryR2_RevokeThenRegrantSamePrimary(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	actor := assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}
	grantDelegation(t, f, "del-r2-old", now)
	seedDelegatedAssignment(t, f, "asg-r2-old", actor, now)
	old := f.s.state.AssignmentLedger.Records["asg-r2-old"]
	revokeRecoveryR2Grant(t, f, "del-r2-old", "revoke-r2-old")

	beforeOldAction := ownerStateBytes(t, f.s)
	if err := f.s.ChangeAssignment(actor, old.AssignmentID, old.Revision, "VOID", "old-grant-revoked"); !errors.Is(err, ErrCoordinatorDelegationExhausted) {
		t.Fatalf("old_revoked_grant_action err=%v", err)
	}
	if !bytes.Equal(beforeOldAction, ownerStateBytes(t, f.s)) {
		t.Fatal("old_revoked_grant_action changed state")
	}

	grantDelegation(t, f, "del-r2-new", now)
	beforeOldUnderReplacement := ownerStateBytes(t, f.s)
	if err := f.s.ChangeAssignment(actor, old.AssignmentID, old.Revision, "VOID", "old-assignment-not-transferred"); !errors.Is(err, ErrCoordinatorDelegationExhausted) {
		t.Fatalf("old_assignment_under_new_grant error=%v", err)
	}
	if !bytes.Equal(beforeOldUnderReplacement, ownerStateBytes(t, f.s)) {
		t.Fatal("old_assignment_under_new_grant changed state")
	}

	base := old
	base.DelegationBinding = nil
	base.CoordinatorLost = false
	base.HandoffEvidence = []assignment.Handoff{}
	base.State = assignment.Offered
	base.AcknowledgedAt = time.Time{}
	base.WorkStartedAt = time.Time{}
	base.AcceptedResult = nil
	base.Revision = 1
	for i := 1; i <= 40; i++ {
		id := "asg-r2-new-" + string(rune('A'+i-1))
		r := base
		r.AssignmentID = id
		r.AdmissionBasis.Reference = "issue-93b"
		r.IssuedAt = now.Add(-time.Minute)
		r.UpdatedAt = r.IssuedAt
		if err := f.s.OfferAssignment(actor, r, "regrant-offer"); err != nil {
			t.Fatalf("forty_repeated_valid_actions_on_new_grant offer %d: %v", i, err)
		}
		offered := f.s.state.AssignmentLedger.Records[id]
		if offered.DelegationBinding == nil || offered.DelegationBinding.GrantID != "del-r2-new" {
			t.Fatalf("new_assignment_under_new_grant %s binding=%+v", id, offered.DelegationBinding)
		}
		if err := f.s.ChangeAssignment(actor, id, offered.Revision, "VOID", "regrant-void"); err != nil {
			t.Fatalf("forty_repeated_valid_actions_on_new_grant VOID %d: %v", i, err)
		}
	}
}

func revokeRecoveryR2Grant(t *testing.T, f *ownerCommandFixture, id, messageID string) {
	t.Helper()
	payload, err := json.Marshal(ownercredential.DelegationRevokePayload{DelegationID: id, Reason: "replace delegated authority"})
	if err != nil {
		t.Fatal(err)
	}
	revision := f.s.state.OwnerCredentials["owner-1"].CredentialRevision
	command := f.command(ownercredential.CommandDelegationRevoke, messageID, "owner-primary", revision, payload)
	signCount := f.s.state.OwnerCredentials["owner-1"].Credentials["owner-primary"].SignCount + 1
	if _, _, err := f.s.ApplyOwnerCredentialCommand(command.ChallengeID, command, assertionFor(command, f.private["owner-primary"], signCount)); err != nil {
		t.Fatalf("revoke %s: %v", id, err)
	}
}
