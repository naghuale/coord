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

func TestRecoveryG3_Q02_HandoffFromMustMatchCoveredLoss(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-g3-q02", now)
	coord := assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}
	next := assignment.Principal{SessionID: "codex:b", AgentID: "agent-b", Generation: 1}
	seedDelegatedAssignment(t, f, "asg-g3-q02", coord, now)
	registerSession(t, f, next.SessionID, next.AgentID)
	confirmVisibility(t, f, "del-g3-q02", "vis-g3-q02", ownercredential.DelegationPrincipalPayload{StableAgentID: next.AgentID, SessionID: next.SessionID, Generation: next.Generation}, now)
	lossConfirm(t, f, "del-g3-q02", "loss-g3-q02", ownercredential.DelegationPrincipalPayload{StableAgentID: coord.AgentID, SessionID: coord.SessionID, Generation: coord.Generation}, now)
	if err := f.s.HandoffAssignment(coord, next, "asg-g3-q02", f.s.state.AssignmentLedger.Records["asg-g3-q02"].Revision, "handoff-g3-q02"); err != nil {
		t.Fatalf("positive control handoff: %v", err)
	}
	state, err := copyCanonicalState(f.s.state)
	if err != nil {
		t.Fatal(err)
	}
	grant := state.CoordinatorDelegations["del-g3-q02"]
	if err := validateDelegationDerivedState(state, grant); err != nil {
		t.Fatalf("positive control valid history: %v", err)
	}
	found := false
	for i := range grant.History {
		if grant.History[i].Kind == DelegationHistoryHandoff {
			wrong := DelegationPrincipal{StableAgentID: "outsider", SessionID: "other:session", Generation: 1}
			grant.History[i].From = &wrong
			found = true
		}
	}
	if !found {
		t.Fatal("test setup has no handoff history")
	}
	if err := validateDelegationDerivedState(state, grant); !errors.Is(err, ErrCoordinatorDelegationInvalid) || !strings.Contains(err.Error(), "HANDOFF lacks a corresponding loss") {
		t.Fatalf("handoff with a different From must not borrow the covered loss: %v", err)
	}
}

func TestRecoveryG3_Q05_ActiveBarredCoordinatorIsRejected(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-g3-q05", now)
	state, err := copyCanonicalState(f.s.state)
	if err != nil {
		t.Fatal(err)
	}
	actor := assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}
	grant := state.CoordinatorDelegations["del-g3-q05"]
	grant.BarredCoordinators = []DelegationPrincipal{principalFromAssignment(actor)}
	state.CoordinatorDelegations[grant.DelegationID] = grant
	if err := enforceDelegatedOfferOrAccept(state, actor, DelegatedActionOffer, now); !errors.Is(err, ErrCoordinatorDelegationExhausted) {
		t.Fatalf("active barred coordinator offer err=%v", err)
	}
	grant.BarredCoordinators = nil
	state.CoordinatorDelegations[grant.DelegationID] = grant
	if err := enforceDelegatedOfferOrAccept(state, actor, DelegatedActionOffer, now); err != nil {
		t.Fatalf("positive control active unbarred coordinator: %v", err)
	}
}

func TestRecoveryG3_Q06_InactiveExactCoordinatorIsRejected(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-g3-q06", now)
	state, err := copyCanonicalState(f.s.state)
	if err != nil {
		t.Fatal(err)
	}
	actor := assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}
	grant := state.CoordinatorDelegations["del-g3-q06"]
	grant.RevokedAt = now
	state.CoordinatorDelegations[grant.DelegationID] = grant
	if err := enforceDelegatedOfferOrAccept(state, actor, DelegatedActionOffer, now); !errors.Is(err, ErrCoordinatorDelegationExhausted) {
		t.Fatalf("inactive exact coordinator offer err=%v", err)
	}
	grant.RevokedAt = time.Time{}
	state.CoordinatorDelegations[grant.DelegationID] = grant
	if err := enforceDelegatedOfferOrAccept(state, actor, DelegatedActionOffer, now); err != nil {
		t.Fatalf("positive control active exact coordinator: %v", err)
	}
}

func TestRecoveryG3_Q08_HandoffPredecessorMustBelongToBoundGrant(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-g3-q08", now)
	coord := assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}
	next := assignment.Principal{SessionID: "codex:b", AgentID: "agent-b", Generation: 1}
	seedDelegatedAssignment(t, f, "asg-g3-q08", coord, now)
	registerSession(t, f, next.SessionID, next.AgentID)
	confirmVisibility(t, f, "del-g3-q08", "vis-g3-q08", ownercredential.DelegationPrincipalPayload{StableAgentID: next.AgentID, SessionID: next.SessionID, Generation: next.Generation}, now)
	lossConfirm(t, f, "del-g3-q08", "loss-g3-q08", ownercredential.DelegationPrincipalPayload{StableAgentID: coord.AgentID, SessionID: coord.SessionID, Generation: coord.Generation}, now)
	state, err := copyCanonicalState(f.s.state)
	if err != nil {
		t.Fatal(err)
	}
	before, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	outsider := assignment.Principal{SessionID: "outsider:session", AgentID: "outsider", Generation: 1}
	err = enforceDelegatedHandoff(&state, outsider, next, "asg-g3-q08", now)
	if !errors.Is(err, ErrCoordinatorDelegationInvalid) || !strings.Contains(err.Error(), "predecessor is outside bound grant") {
		t.Fatalf("outside-grant predecessor err=%v", err)
	}
	after, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("rejected outside-grant predecessor changed state")
	}
}

func TestRecoveryG3_Q09_NonactingPredecessorNeedsLoss(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-g3-q09", now)
	coord := assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}
	next := assignment.Principal{SessionID: "codex:b", AgentID: "agent-b", Generation: 1}
	seedDelegatedAssignment(t, f, "asg-g3-q09", coord, now)
	registerSession(t, f, next.SessionID, next.AgentID)
	confirmVisibility(t, f, "del-g3-q09", "vis-g3-q09", ownercredential.DelegationPrincipalPayload{StableAgentID: next.AgentID, SessionID: next.SessionID, Generation: next.Generation}, now)
	state, err := copyCanonicalState(f.s.state)
	if err != nil {
		t.Fatal(err)
	}
	before, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	err = enforceDelegatedHandoff(&state, next, next, "asg-g3-q09", now)
	if !errors.Is(err, ErrCoordinatorDelegationInvalid) || !strings.Contains(err.Error(), "assignment has no matching loss") {
		t.Fatalf("nonacting predecessor without loss err=%v", err)
	}
	after, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("rejected nonacting predecessor changed state")
	}
}

func TestRecoveryG3_Q10_UnboundCoveredAssignmentRequiresGrantBinding(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-g3-q10", now)
	coord := assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}
	next := assignment.Principal{SessionID: "codex:b", AgentID: "agent-b", Generation: 1}
	seedDelegatedAssignment(t, f, "asg-g3-q10", coord, now)
	registerSession(t, f, next.SessionID, next.AgentID)
	confirmVisibility(t, f, "del-g3-q10", "vis-g3-q10", ownercredential.DelegationPrincipalPayload{StableAgentID: next.AgentID, SessionID: next.SessionID, Generation: next.Generation}, now)
	state, err := copyCanonicalState(f.s.state)
	if err != nil {
		t.Fatal(err)
	}
	record := state.AssignmentLedger.Records["asg-g3-q10"]
	record.DelegationBinding = nil
	state.AssignmentLedger.Records[record.AssignmentID] = record
	before, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := enforceDelegatedHandoff(&state, coord, next, record.AssignmentID, now); !errors.Is(err, ErrCoordinatorDelegationInvalid) || !strings.Contains(err.Error(), "requires persisted grant binding") {
		t.Fatalf("unbound covered assignment handoff err=%v", err)
	}
	after, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("rejected unbound assignment changed state")
	}
}

func TestRecoveryG3_Q11_BoundGrantResolutionErrorIsReturned(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-g3-q11", now)
	coord := assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}
	next := assignment.Principal{SessionID: "codex:b", AgentID: "agent-b", Generation: 1}
	seedDelegatedAssignment(t, f, "asg-g3-q11", coord, now)
	registerSession(t, f, next.SessionID, next.AgentID)
	confirmVisibility(t, f, "del-g3-q11", "vis-g3-q11", ownercredential.DelegationPrincipalPayload{StableAgentID: next.AgentID, SessionID: next.SessionID, Generation: next.Generation}, now)
	state, err := copyCanonicalState(f.s.state)
	if err != nil {
		t.Fatal(err)
	}
	record := state.AssignmentLedger.Records["asg-g3-q11"]
	record.DelegationBinding.GrantID = "missing-grant"
	state.AssignmentLedger.Records[record.AssignmentID] = record
	before, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	err = enforceDelegatedHandoff(&state, coord, next, record.AssignmentID, now)
	if !errors.Is(err, ErrCoordinatorDelegationInvalid) || !strings.Contains(err.Error(), "originating grant") {
		t.Fatalf("bound grant resolution error was not propagated: %v", err)
	}
	after, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("rejected missing bound grant changed state")
	}
}

func TestRecoveryG3_Q12_InactiveHistoricalGrantDoesNotCaptureNewSession(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-g3-q12", now)
	seedDelegatedAssignment(t, f, "asg-g3-q12-old", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now)
	revokeRecoveryR2Grant(t, f, "del-g3-q12", "revoke-g3-q12")
	old := f.s.state.AssignmentLedger.Records["asg-g3-q12-old"]
	oldUse := old
	oldUse.AssignmentID = "asg-g3-q12-old-grant-use"
	oldUse.State = assignment.Offered
	oldUse.Revision = 1
	oldUse.CoordinatorLost = false
	oldUse.AcknowledgedAt = time.Time{}
	oldUse.WorkStartedAt = time.Time{}
	oldUse.AcceptedResult = nil
	oldUse.HandoffEvidence = []assignment.Handoff{}
	oldUse.IssuedAt = now.Add(-time.Minute)
	oldUse.UpdatedAt = oldUse.IssuedAt
	beforeOldGrantUse := ownerStateBytes(t, f.s)
	if err := f.s.OfferAssignment(old.Coordinator(), oldUse, "old-grant-after-revoke"); !errors.Is(err, ErrCoordinatorDelegationExhausted) {
		t.Fatalf("using old grant after revoke err=%v", err)
	}
	if !bytes.Equal(beforeOldGrantUse, ownerStateBytes(t, f.s)) {
		t.Fatal("rejected old-grant offer changed canonical state")
	}
	if err := f.s.ReplaceAgentSessionAddress("claude:a", "claude:a-new", ed25519.PublicKey(strings.Repeat("q", ed25519.PublicKeySize)), "test fresh address after delegation revoke"); err != nil {
		t.Fatal(err)
	}
	actor := assignment.Principal{SessionID: "claude:a-new", AgentID: "agent-a", Generation: 1}
	base := assignment.Record{
		AssignmentID: "asg-g3-q12-former", State: assignment.Offered,
		Issuer: actor.SessionID, IssuerAgentID: actor.AgentID, IssuerGeneration: actor.Generation,
		CurrentCoordinator: actor.SessionID, CoordinatorAgentID: actor.AgentID, CoordinatorGeneration: actor.Generation,
		HandoffEvidence: []assignment.Handoff{},
		Assignee:        "codex:worker", StableAgentID: "worker", SessionID: "codex:worker", AssigneeGeneration: 1,
		Role: "IMPLEMENTER", SubjectSHA: strings.Repeat("b", 40),
		IssuedAt: now.Add(-time.Minute), ValidUntil: now.Add(time.Hour), Revision: 1,
		AdmissionBasis:     assignment.AdmissionBasis{Kind: "POOL", Reference: "issue-93b", TargetSessionID: "codex:worker", TargetAgentID: "worker", Role: "IMPLEMENTER", SubjectSHA: strings.Repeat("b", 40)},
		VisibilityEvidence: assignment.VisibilityEvidence{Reference: "g3-q12-visible", SessionID: "codex:worker", ObservedAt: now.Add(-time.Minute)},
	}
	if err := f.s.OfferAssignment(actor, base, "former-grant-unbound-offer"); err != nil {
		t.Fatalf("#90-compatible unbound offer by new session of formerly delegated stable agent: %v", err)
	}
	got := f.s.state.AssignmentLedger.Records[base.AssignmentID]
	if got.DelegationBinding != nil {
		t.Fatalf("expired/revoked historical grant captured new #90 assignment: %+v", got.DelegationBinding)
	}
	if err := f.s.ChangeAssignment(actor, got.AssignmentID, got.Revision, "VOID", "former-grant-unbound-void"); err != nil {
		t.Fatalf("#90-compatible unbound mutation after inactive grant: %v", err)
	}
	// An unrelated, never-delegated coordinator is the positive #90 control.
	other := assignment.Principal{SessionID: "claude:other", AgentID: "agent-other", Generation: 1}
	registerSession(t, f, other.SessionID, other.AgentID)
	base.AssignmentID = "asg-g3-q12-unrelated"
	base.Issuer, base.IssuerAgentID, base.IssuerGeneration = other.SessionID, other.AgentID, other.Generation
	base.CurrentCoordinator, base.CoordinatorAgentID, base.CoordinatorGeneration = other.SessionID, other.AgentID, other.Generation
	base.AdmissionBasis.TargetSessionID = "codex:worker"
	if err := f.s.OfferAssignment(other, base, "ordinary-no-grant-offer"); err != nil {
		t.Fatalf("positive control ordinary #90 offer: %v", err)
	}
	if f.s.state.AssignmentLedger.Records[base.AssignmentID].DelegationBinding != nil {
		t.Fatal("ordinary #90 positive control was unexpectedly bound")
	}
}

func TestRecoveryG3_Q13Q14_PrimaryRestartAfterSiblingHandoffStillNeedsNewLoss(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-g3-q13q14", now)
	coord := assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}
	next := assignment.Principal{SessionID: "codex:b", AgentID: "agent-b", Generation: 1}
	seedDelegatedAssignment(t, f, "asg-g3-q13-x", coord, now)
	seedDelegatedAssignment(t, f, "asg-g3-q13-y", coord, now)
	registerSession(t, f, next.SessionID, next.AgentID)
	confirmVisibility(t, f, "del-g3-q13q14", "vis-g3-q13-x", ownercredential.DelegationPrincipalPayload{StableAgentID: next.AgentID, SessionID: next.SessionID, Generation: next.Generation}, now)
	confirmVisibility(t, f, "del-g3-q13q14", "vis-g3-q13-y", ownercredential.DelegationPrincipalPayload{StableAgentID: next.AgentID, SessionID: next.SessionID, Generation: next.Generation}, now)
	lossConfirm(t, f, "del-g3-q13q14", "loss-g3-q13q14", ownercredential.DelegationPrincipalPayload{StableAgentID: coord.AgentID, SessionID: coord.SessionID, Generation: coord.Generation}, now)
	for _, id := range []string{"asg-g3-q13-x", "asg-g3-q13-y"} {
		r := f.s.state.AssignmentLedger.Records[id]
		if err := f.s.HandoffAssignment(coord, next, id, r.Revision, "handoff-"+id); err != nil {
			t.Fatalf("sibling assignment %s handoff: %v", id, err)
		}
	}
	if err := ValidateCanonicalState(f.s.state); err != nil {
		t.Fatalf("positive control complete sibling loss/handoff history: %v", err)
	}
	registerSession(t, f, "claude:a2", "agent-a")
	bumpSessionGeneration(t, f, "claude:a2", 2)
	confirmPrimaryRestartVisibility(t, f, "del-g3-q13q14", "vis-g3-q13-primary", "agent-a", now)
	before := ownerStateBytes(t, f.s)
	saves := f.p.saveCalls
	err := f.s.ApplyPrimaryRestart("del-g3-q13q14", assignment.Principal{SessionID: "claude:a2", AgentID: "agent-a", Generation: 2}, "restart-after-sibling-handoff")
	if !errors.Is(err, ErrCoordinatorDelegationInvalid) || !strings.Contains(err.Error(), "predecessor still acting") {
		t.Fatalf("primaryRestart after sibling handoff must require a new loss: %v", err)
	}
	if f.p.saveCalls != saves || !bytes.Equal(before, ownerStateBytes(t, f.s)) {
		t.Fatal("rejected primaryRestart after sibling handoff changed canonical state")
	}
}
