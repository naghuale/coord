package store

import (
	"bytes"
	"coord/assignment"
	"coord/ownercredential"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"
)

// Positive control: grant + matching triple + open clock + first eligible successor.
func TestDelegationPositiveControlGrantHandoffSucceeds(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegationWith(t, f, "del-pos", now, delegationOpts{
		successors: []ownercredential.DelegationPrincipalPayload{
			{StableAgentID: "agent-b", SessionID: "codex:b", Generation: 1},
		},
		actions: []string{"OFFER", "VOID", "EXPIRE", "HANDOFF", "ACTIVATE", "ACCEPT_RESULT"},
	})
	seedDelegatedAssignment(t, f, "asg-pos", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now)
	confirmVisibility(t, f, "del-pos", "msg-vis-pos", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-b", SessionID: "codex:b", Generation: 1}, now)
	registerSession(t, f, "codex:b", "agent-b")
	lossConfirm(t, f, "del-pos", "msg-loss-pos", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1}, now)
	prev := assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}
	next := assignment.Principal{SessionID: "codex:b", AgentID: "agent-b", Generation: 1}
	rev := f.s.state.AssignmentLedger.Records["asg-pos"].Revision
	if err := f.s.HandoffAssignment(prev, next, "asg-pos", rev, "pos-handoff"); err != nil {
		t.Fatal(err)
	}
	r := f.s.state.AssignmentLedger.Records["asg-pos"]
	if r.Coordinator() != next || r.CoordinatorLost {
		t.Fatalf("handoff failed: %+v", r)
	}
}

func TestDelegationHandoffRejectsSessionOutsideSuccessorList(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-1", now)
	seedDelegatedAssignment(t, f, "asg-1", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now)
	lossConfirm(t, f, "del-1", "msg-loss-1", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1}, now)
	registerSession(t, f, "outsider:x", "outsider")
	before := ownerStateBytes(t, f.s)
	outsider := assignment.Principal{SessionID: "outsider:x", AgentID: "outsider", Generation: 1}
	prev := assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}
	rev := f.s.state.AssignmentLedger.Records["asg-1"].Revision
	if err := f.s.HandoffAssignment(prev, outsider, "asg-1", rev, "outside"); !errors.Is(err, ErrCoordinatorDelegationInvalid) {
		t.Fatalf("err=%v", err)
	}
	if !bytes.Equal(before, ownerStateBytes(t, f.s)) {
		t.Fatal("state changed")
	}
}

func TestDelegationHandoffRejectsLossObserverAsSuccessor(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	obs := assignment.Principal{SessionID: "obs:1", AgentID: "observer", Generation: 1}
	grantDelegationWith(t, f, "del-obs", now, delegationOpts{
		successors: []ownercredential.DelegationPrincipalPayload{
			{StableAgentID: "observer", SessionID: "obs:1", Generation: 1},
			{StableAgentID: "agent-b", SessionID: "codex:b", Generation: 1},
		},
	})
	seedDelegatedAssignment(t, f, "asg-obs", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now)
	registerSession(t, f, "obs:1", "observer")
	registerSession(t, f, "codex:b", "agent-b")
	// Path (a): observer records loss — but grant names observer as successor, so observe itself rejects.
	if err := f.s.ObserveAssignmentCoordinatorLoss(obs, assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, "path-a"); !errors.Is(err, ErrCoordinatorDelegationInvalid) {
		t.Fatalf("observer-as-successor observe err=%v", err)
	}
}

func TestDelegationHandoffRejectsAffectedTargetAsSuccessor(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	// Target of seeded assignment is worker@codex:worker.
	grantDelegationWith(t, f, "del-tgt", now, delegationOpts{
		successors: []ownercredential.DelegationPrincipalPayload{
			{StableAgentID: "worker", SessionID: "codex:worker", Generation: 1},
		},
	})
	seedDelegatedAssignment(t, f, "asg-tgt", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now)
	confirmVisibility(t, f, "del-tgt", "vis-tgt", ownercredential.DelegationPrincipalPayload{StableAgentID: "worker", SessionID: "codex:worker", Generation: 1}, now)
	lossConfirm(t, f, "del-tgt", "loss-tgt", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1}, now)
	prev := assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}
	target := assignment.Principal{SessionID: "codex:worker", AgentID: "worker", Generation: 1}
	rev := f.s.state.AssignmentLedger.Records["asg-tgt"].Revision
	if err := f.s.HandoffAssignment(prev, target, "asg-tgt", rev, "target-handoff"); err == nil {
		t.Fatal("target successor accepted")
	}
}

func TestDelegationRejectsMismatchedGenerationNoInheritance(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-gen", now)
	seedDelegatedAssignment(t, f, "asg-gen", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now)
	offerDelegatedCopy(t, f, "asg-gen-valid-control", "asg-gen", now)
	// Rotate session to generation 2 — new gen does not inherit.
	if err := f.s.RotateAgentSessionKey("claude:a", ed25519.PublicKey(strings.Repeat("z", 32))); err != nil {
		t.Fatal(err)
	}
	newActor := assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 2}
	newOffer := f.s.state.AssignmentLedger.Records["asg-gen"]
	newOffer.AssignmentID = "asg-gen-stale-generation"
	newOffer.State = assignment.Offered
	newOffer.Revision = 1
	newOffer.AcknowledgedAt = time.Time{}
	newOffer.WorkStartedAt = time.Time{}
	newOffer.AcceptedResult = nil
	newOffer.CoordinatorLost = false
	newOffer.Issuer, newOffer.IssuerAgentID, newOffer.IssuerGeneration = newActor.SessionID, newActor.AgentID, newActor.Generation
	newOffer.CurrentCoordinator, newOffer.CoordinatorAgentID, newOffer.CoordinatorGeneration = newActor.SessionID, newActor.AgentID, newActor.Generation
	newOffer.DelegationBinding = nil
	newOffer.AdmissionBasis.Reference = "issue-93b"
	newOffer.UpdatedAt = now.Add(-time.Minute)
	before := ownerStateBytes(t, f.s)
	if err := f.s.OfferAssignment(newActor, newOffer, "gen-inherit"); !errors.Is(err, ErrCoordinatorDelegationInvalid) {
		t.Fatalf("stale generation offer err=%v", err)
	}
	if !bytes.Equal(before, ownerStateBytes(t, f.s)) {
		t.Fatal("state changed")
	}
}

func TestRecoveryA93B3_DelegatedMutationRequiresExactActingPrincipal(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-exact-acting", now)
	seedDelegatedAssignment(t, f, "asg-exact-acting", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now)
	offerDelegatedCopy(t, f, "asg-exact-acting-offered", "asg-exact-acting", now)
	record := f.s.state.AssignmentLedger.Records["asg-exact-acting-offered"]
	valid := assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}
	if err := enforceDelegatedMutation(f.s.state, valid, DelegatedActionVoid, record, now); err != nil {
		t.Fatalf("exact acting principal positive control: %v", err)
	}
	for _, tc := range []struct {
		name  string
		actor assignment.Principal
	}{
		{"session_mismatch", assignment.Principal{SessionID: "other:session", AgentID: "agent-a", Generation: 1}},
		{"stable_agent_mismatch", assignment.Principal{SessionID: "claude:a", AgentID: "other-agent", Generation: 1}},
		{"generation_mismatch", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := enforceDelegatedMutation(f.s.state, tc.actor, DelegatedActionVoid, record, now); !errors.Is(err, ErrCoordinatorDelegationInvalid) {
				t.Fatalf("non-acting principal err=%v, want exact-acting rejection", err)
			}
		})
	}
}

func TestDelegationRejectsBeforeNotBeforeAndAtExpiresAt(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	base := f.s.now().UTC()
	// Grant not yet open.
	payload, _ := json.Marshal(ownercredential.DelegationGrantPayload{
		DelegationID: "del-clock",
		Scope: ownercredential.DelegationScopePayload{
			Repository: "naghuale/coord", Project: "coord",
			AllowedWorkItems: []string{"issue-93b"}, AllowedActions: []string{"OFFER", "VOID"},
		},
		PrimaryCoordinator: ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1},
		Validity:           ownercredential.DelegationValidityPayload{NotBefore: base.Add(time.Hour), ExpiresAt: base.Add(2 * time.Hour)},
		Successors:         []ownercredential.DelegationPrincipalPayload{{StableAgentID: "agent-b", SessionID: "codex:b", Generation: 1}},
		Evidence:           ownercredential.DelegationEvidencePayload{OwnerDecisionRef: "x", VisibilityEvidence: "y"},
	})
	rev := f.s.state.OwnerCredentials["owner-1"].CredentialRevision
	cmd := f.command(ownercredential.CommandDelegationGrant, "grant-clock", "owner-primary", rev, payload)
	sign := f.s.state.OwnerCredentials["owner-1"].Credentials["owner-primary"].SignCount + 1
	if _, _, err := f.s.ApplyOwnerCredentialCommand(cmd.ChallengeID, cmd, assertionFor(cmd, f.private["owner-primary"], sign)); err != nil {
		t.Fatal(err)
	}
	registerSession(t, f, "claude:a", "agent-a")
	actor := assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}
	offer := assignment.Record{
		AssignmentID: "asg-clock", State: assignment.Offered,
		Issuer: actor.SessionID, IssuerAgentID: actor.AgentID, IssuerGeneration: actor.Generation,
		CurrentCoordinator: actor.SessionID, CoordinatorAgentID: actor.AgentID, CoordinatorGeneration: actor.Generation,
		HandoffEvidence: []assignment.Handoff{},
		Assignee:        "codex:worker", StableAgentID: "worker", SessionID: "codex:worker", AssigneeGeneration: 1,
		Role: "IMPLEMENTER", SubjectSHA: strings.Repeat("c", 40),
		IssuedAt: base.Add(-time.Minute), ValidUntil: base.Add(3 * time.Hour),
		AdmissionBasis:     assignment.AdmissionBasis{Kind: "POOL", Reference: "issue-93b", TargetSessionID: "codex:worker", TargetAgentID: "worker", Role: "IMPLEMENTER", SubjectSHA: strings.Repeat("c", 40)},
		VisibilityEvidence: assignment.VisibilityEvidence{Reference: "v", SessionID: "codex:worker", ObservedAt: base.Add(-time.Minute)},
		Revision:           1, UpdatedAt: base,
	}
	registerSession(t, f, "codex:worker", "worker")
	if err := f.s.OfferAssignment(actor, offer, "before-notBefore"); err == nil {
		t.Fatal("offer accepted before notBefore")
	}
	rec := f.s.state.CoordinatorDelegations["del-clock"]
	if rec.Active(base.Add(2 * time.Hour)) {
		t.Fatal("grant still active at expiresAt")
	}
	if rec.Validity.OpenAt(base.Add(2 * time.Hour)) {
		t.Fatal("OpenAt true at expiresAt")
	}
}

func TestDelegationRevokeBlocksPrimaryAndSuccessorsNextCommand(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-rev", now)
	seedDelegatedAssignment(t, f, "asg-rev", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now)
	offerDelegatedCopy(t, f, "asg-rev-positive", "asg-rev", now)
	offerDelegatedCopy(t, f, "asg-rev-offered", "asg-rev", now)
	actor := assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}
	positive := f.s.state.AssignmentLedger.Records["asg-rev-positive"]
	if err := f.s.ChangeAssignment(actor, positive.AssignmentID, positive.Revision, "VOID", "allowed-before-revoke"); err != nil {
		t.Fatalf("unrevoked legal VOID: %v", err)
	}
	offered := f.s.state.AssignmentLedger.Records["asg-rev-offered"]
	if err := enforceDelegatedMutation(f.s.state, actor, "VOID", offered, now); err != nil {
		t.Fatalf("unrevoked VOID positive control: %v", err)
	}
	revokePayload, _ := json.Marshal(ownercredential.DelegationRevokePayload{DelegationID: "del-rev", Reason: "done"})
	rev := f.s.state.OwnerCredentials["owner-1"].CredentialRevision
	cmd := f.command(ownercredential.CommandDelegationRevoke, "revoke-rev", "owner-primary", rev, revokePayload)
	sign := f.s.state.OwnerCredentials["owner-1"].Credentials["owner-primary"].SignCount + 1
	if _, _, err := f.s.ApplyOwnerCredentialCommand(cmd.ChallengeID, cmd, assertionFor(cmd, f.private["owner-primary"], sign)); err != nil {
		t.Fatal(err)
	}
	r := f.s.state.AssignmentLedger.Records["asg-rev-offered"]
	before := ownerStateBytes(t, f.s)
	if err := f.s.ChangeAssignment(actor, "asg-rev-offered", r.Revision, "VOID", "post-revoke"); !errors.Is(err, ErrCoordinatorDelegationExhausted) {
		t.Fatalf("revoked primary action err=%v", err)
	}
	if !bytes.Equal(before, ownerStateBytes(t, f.s)) {
		t.Fatal("revoked action changed state")
	}
	for _, candidate := range f.s.state.CoordinatorDelegations["del-rev"].Successors {
		if err := enforceDelegatedMutation(f.s.state, assignment.Principal{SessionID: candidate.SessionID, AgentID: candidate.StableAgentID, Generation: candidate.Generation}, "VOID", r, now); !errors.Is(err, ErrCoordinatorDelegationExhausted) {
			t.Fatalf("revoked successor action err=%v", err)
		}
	}
}

func TestDelegationCoordinatorCannotExtendScopeListOrSecondGrant(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-ext", now)
	// Attempt a second overlapping grant (simulates coordinator trying to mint power).
	payload, _ := json.Marshal(ownercredential.DelegationGrantPayload{
		DelegationID: "del-ext-2",
		Scope: ownercredential.DelegationScopePayload{
			Repository: "naghuale/coord", Project: "coord",
			AllowedWorkItems: []string{"wider"}, AllowedActions: []string{"OFFER", "VOID", "EXPIRE", "HANDOFF", "SUBDELEGATE"},
		},
		PrimaryCoordinator: ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1},
		Validity:           ownercredential.DelegationValidityPayload{NotBefore: now, ExpiresAt: now.Add(24 * time.Hour)},
		Successors:         []ownercredential.DelegationPrincipalPayload{{StableAgentID: "agent-b", SessionID: "codex:b", Generation: 1}},
		Evidence:           ownercredential.DelegationEvidencePayload{OwnerDecisionRef: "x", VisibilityEvidence: "y"},
	})
	rev := f.s.state.OwnerCredentials["owner-1"].CredentialRevision
	cmd := f.command(ownercredential.CommandDelegationGrant, "grant-ext-2", "owner-primary", rev, payload)
	sign := f.s.state.OwnerCredentials["owner-1"].Credentials["owner-primary"].SignCount + 1
	if _, _, err := f.s.ApplyOwnerCredentialCommand(cmd.ChallengeID, cmd, assertionFor(cmd, f.private["owner-primary"], sign)); !errors.Is(err, ErrCoordinatorDelegationInvalid) {
		t.Fatalf("second grant err=%v", err)
	}
	// Loss.confirm with unknown scope field rejected.
	body := []byte(`{"delegationId":"del-ext","coordinator":{"stableAgentId":"agent-a","sessionId":"claude:a","generation":1},"reason":"x","observedAt":"` + now.Format(time.RFC3339Nano) + `","scope":{"repository":"other"}}`)
	loss := f.command(ownercredential.CommandLossConfirm, "loss-scope", "owner-primary", rev, body)
	sign = f.s.state.OwnerCredentials["owner-1"].Credentials["owner-primary"].SignCount + 1
	assertOwnerCommandRejectedWithoutWrite(t, f, loss, assertionFor(loss, f.private["owner-primary"], sign), ErrCoordinatorDelegationInvalid)
}

func TestDelegationRejectsSubdelegation(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegationWith(t, f, "del-sub", now, delegationOpts{
		actions: []string{"OFFER", "VOID", "EXPIRE", "HANDOFF", "SUBDELEGATE"},
	})
	seedDelegatedAssignment(t, f, "asg-sub", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now)
	actor := assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}
	r := f.s.state.AssignmentLedger.Records["asg-sub"]
	// SUBDELEGATE is forbidden even if listed in allowlist.
	if err := enforceDelegatedMutation(f.s.state, actor, "SUBDELEGATE", r, now); !errors.Is(err, ErrCoordinatorDelegationInvalid) {
		t.Fatalf("subdelegation err=%v", err)
	}
}

func TestDelegationExhaustionBlocksOfferAcceptAndAutoCapture(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-exh", now)
	seedDelegatedAssignment(t, f, "asg-exh", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now)
	lossConfirm(t, f, "del-exh", "loss-exh", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1}, now)
	// No visibility → successor skipped → exhaustion.
	_, skips, err := SelectDelegationSuccessor(f.s.state, f.s.state.CoordinatorDelegations["del-exh"], "asg-exh", now)
	if !errors.Is(err, ErrCoordinatorDelegationExhausted) {
		t.Fatalf("err=%v skips=%v", err, skips)
	}
	actor := assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}
	if err := f.s.OfferAssignment(actor, assignment.Record{
		AssignmentID: "asg-exh-2", State: assignment.Offered,
		Issuer: actor.SessionID, IssuerAgentID: actor.AgentID, IssuerGeneration: 1,
		CurrentCoordinator: actor.SessionID, CoordinatorAgentID: actor.AgentID, CoordinatorGeneration: 1,
		HandoffEvidence: []assignment.Handoff{},
		Assignee:        "codex:worker", StableAgentID: "worker", SessionID: "codex:worker", AssigneeGeneration: 1,
		Role: "IMPLEMENTER", SubjectSHA: strings.Repeat("d", 40),
		IssuedAt: now.Add(-time.Minute), ValidUntil: now.Add(time.Hour),
		AdmissionBasis:     assignment.AdmissionBasis{Kind: "POOL", Reference: "issue-93b", TargetSessionID: "codex:worker", TargetAgentID: "worker", Role: "IMPLEMENTER", SubjectSHA: strings.Repeat("d", 40)},
		VisibilityEvidence: assignment.VisibilityEvidence{Reference: "v", SessionID: "codex:worker", ObservedAt: now.Add(-time.Minute)},
		Revision:           1, UpdatedAt: now,
	}, "exhausted-offer"); err == nil {
		t.Fatal("offer after exhaustion accepted")
	}
}

func TestDelegationHandoffSameRevisionOnlyOneSuccessorWins(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegationWith(t, f, "del-cas", now, delegationOpts{
		successors: []ownercredential.DelegationPrincipalPayload{
			{StableAgentID: "agent-b", SessionID: "codex:b", Generation: 1},
			{StableAgentID: "agent-c", SessionID: "codex:c", Generation: 1},
		},
	})
	seedDelegatedAssignment(t, f, "asg-cas", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now)
	registerSession(t, f, "codex:b", "agent-b")
	registerSession(t, f, "codex:c", "agent-c")
	confirmVisibility(t, f, "del-cas", "vis-b", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-b", SessionID: "codex:b", Generation: 1}, now)
	confirmVisibility(t, f, "del-cas", "vis-c", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-c", SessionID: "codex:c", Generation: 1}, now)
	lossConfirm(t, f, "del-cas", "loss-cas", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1}, now)
	prev := assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}
	b := assignment.Principal{SessionID: "codex:b", AgentID: "agent-b", Generation: 1}
	c := assignment.Principal{SessionID: "codex:c", AgentID: "agent-c", Generation: 1}
	rev := f.s.state.AssignmentLedger.Records["asg-cas"].Revision
	if err := f.s.HandoffAssignment(prev, b, "asg-cas", rev, "first"); err != nil {
		t.Fatal(err)
	}
	if err := f.s.HandoffAssignment(prev, c, "asg-cas", rev, "second"); err == nil {
		t.Fatal("second handoff same revision accepted")
	}
}

func TestDelegationRejectsRetargetedGrantOrHandoffEvidence(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-ret", now)
	seedDelegatedAssignment(t, f, "asg-ret", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now)
	confirmVisibility(t, f, "del-ret", "vis-ret", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-b", SessionID: "codex:b", Generation: 1}, now)
	lossConfirm(t, f, "del-ret", "loss-ret", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1}, now)
	registerSession(t, f, "codex:b", "agent-b")
	registerSession(t, f, "other:z", "other")
	prev := assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}
	wrong := assignment.Principal{SessionID: "other:z", AgentID: "other", Generation: 1}
	rev := f.s.state.AssignmentLedger.Records["asg-ret"].Revision
	if err := f.s.HandoffAssignment(prev, wrong, "asg-ret", rev, "retargeted"); err == nil {
		t.Fatal("retargeted handoff accepted")
	}
}

func TestDelegationRejectsFirstLiveSessionCapture(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-s3", now)
	seedDelegatedAssignment(t, f, "asg-s3", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now)
	lossConfirm(t, f, "del-s3", "loss-s3", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1}, now)
	registerSession(t, f, "live:1", "live-agent")
	live := assignment.Principal{SessionID: "live:1", AgentID: "live-agent", Generation: 1}
	prev := assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}
	rev := f.s.state.AssignmentLedger.Records["asg-s3"].Revision
	if err := f.s.HandoffAssignment(prev, live, "asg-s3", rev, "s3-capture"); err == nil {
		t.Fatal("first live session capture accepted")
	}
}

func TestDelegationSkipsSuccessorWithoutVisibilityThenExhausts(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-vis-skip", now)
	seedDelegatedAssignment(t, f, "asg-vis-skip", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now)
	registerSession(t, f, "codex:b", "agent-b")
	lossConfirm(t, f, "del-vis-skip", "loss-vis-skip", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1}, now)
	_, skips, err := SelectDelegationSuccessor(f.s.state, f.s.state.CoordinatorDelegations["del-vis-skip"], "asg-vis-skip", now)
	if !errors.Is(err, ErrCoordinatorDelegationExhausted) {
		t.Fatalf("err=%v", err)
	}
	if len(skips) == 0 || skips[0].Reason != string(SkipNoVisibility) {
		t.Fatalf("skips=%+v", skips)
	}
}

func TestRecoveryA93B3_HandoffSkipsBarredSuccessorInListOrder(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegationWith(t, f, "del-skip-barred", now, delegationOpts{successors: []ownercredential.DelegationPrincipalPayload{
		{StableAgentID: "agent-b", SessionID: "codex:b", Generation: 1},
		{StableAgentID: "agent-c", SessionID: "gemini:c", Generation: 1},
	}})
	seedDelegatedAssignment(t, f, "asg-skip-barred", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now)
	registerSession(t, f, "codex:b", "agent-b")
	registerSession(t, f, "gemini:c", "agent-c")
	confirmVisibility(t, f, "del-skip-barred", "vis-skip-barred-b", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-b", SessionID: "codex:b", Generation: 1}, now)
	confirmVisibility(t, f, "del-skip-barred", "vis-skip-barred-c", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-c", SessionID: "gemini:c", Generation: 1}, now)
	coordA := ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1}
	lossConfirm(t, f, "del-skip-barred", "loss-skip-barred-a", coordA, now)
	prevA := assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}
	coordB := assignment.Principal{SessionID: "codex:b", AgentID: "agent-b", Generation: 1}
	if err := f.s.HandoffAssignment(prevA, coordB, "asg-skip-barred", f.s.state.AssignmentLedger.Records["asg-skip-barred"].Revision, "handoff-a-b"); err != nil {
		t.Fatalf("first successor handoff: %v", err)
	}
	lossConfirm(t, f, "del-skip-barred", "loss-skip-barred-b", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-b", SessionID: "codex:b", Generation: 1}, now)
	confirmVisibility(t, f, "del-skip-barred", "vis-skip-barred-b-again", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-b", SessionID: "codex:b", Generation: 1}, now)
	coordC := assignment.Principal{SessionID: "gemini:c", AgentID: "agent-c", Generation: 1}
	if err := f.s.HandoffAssignment(coordB, coordC, "asg-skip-barred", f.s.state.AssignmentLedger.Records["asg-skip-barred"].Revision, "handoff-b-c"); err != nil {
		t.Fatalf("barred first successor was not skipped: %v", err)
	}
	if got := f.s.state.AssignmentLedger.Records["asg-skip-barred"].Coordinator(); got != coordC {
		t.Fatalf("handoff selected %v, want %v", got, coordC)
	}
}

func TestRecoveryA93B3_HandoffSkipsInactiveSuccessor(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegationWith(t, f, "del-skip-inactive", now, delegationOpts{successors: []ownercredential.DelegationPrincipalPayload{
		{StableAgentID: "agent-b", SessionID: "codex:b", Generation: 1},
		{StableAgentID: "agent-c", SessionID: "gemini:c", Generation: 1},
	}})
	seedDelegatedAssignment(t, f, "asg-skip-inactive", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now)
	registerSession(t, f, "codex:b", "agent-b")
	registerSession(t, f, "gemini:c", "agent-c")
	if err := f.s.ReplaceAgentSessionAddress("codex:b", "codex:b2", ed25519.PublicKey(strings.Repeat("k", 32)), "successor session retired"); err != nil {
		t.Fatal(err)
	}
	confirmVisibility(t, f, "del-skip-inactive", "vis-skip-inactive-b", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-b", SessionID: "codex:b", Generation: 1}, now)
	confirmVisibility(t, f, "del-skip-inactive", "vis-skip-inactive-c", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-c", SessionID: "gemini:c", Generation: 1}, now)
	lossConfirm(t, f, "del-skip-inactive", "loss-skip-inactive", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1}, now)
	prev := assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}
	want := assignment.Principal{SessionID: "gemini:c", AgentID: "agent-c", Generation: 1}
	if err := f.s.HandoffAssignment(prev, want, "asg-skip-inactive", f.s.state.AssignmentLedger.Records["asg-skip-inactive"].Revision, "skip-retired-successor"); err != nil {
		t.Fatalf("inactive first successor was not skipped: %v", err)
	}
	if got := f.s.state.AssignmentLedger.Records["asg-skip-inactive"].Coordinator(); got != want {
		t.Fatalf("handoff selected %v, want %v", got, want)
	}
}

func TestRecoveryA93B3_HandoffRejectsExpiredVisibilityAfterExhaustion(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-expired-vis", now)
	seedDelegatedAssignment(t, f, "asg-expired-vis", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now)
	registerSession(t, f, "codex:b", "agent-b")
	payload, err := json.Marshal(ownercredential.VisibilityConfirmPayload{
		DelegationID: "del-expired-vis", Successor: &ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-b", SessionID: "codex:b", Generation: 1},
		IssuedAt: now, ExpiresAt: now.Add(time.Minute), SingleUse: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	cmd := f.command(ownercredential.CommandVisibilityConfirm, "vis-expired", "owner-primary", f.s.state.OwnerCredentials["owner-1"].CredentialRevision, payload)
	sign := f.s.state.OwnerCredentials["owner-1"].Credentials["owner-primary"].SignCount + 1
	if _, _, err := f.s.ApplyOwnerCredentialCommand(cmd.ChallengeID, cmd, assertionFor(cmd, f.private["owner-primary"], sign)); err != nil {
		t.Fatal(err)
	}
	lossConfirm(t, f, "del-expired-vis", "loss-expired-vis", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1}, now)
	f.s.now = func() time.Time { return now.Add(2 * time.Minute) }
	prev := assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}
	want := assignment.Principal{SessionID: "codex:b", AgentID: "agent-b", Generation: 1}
	before := ownerStateBytes(t, f.s)
	if err := f.s.HandoffAssignment(prev, want, "asg-expired-vis", f.s.state.AssignmentLedger.Records["asg-expired-vis"].Revision, "expired-visibility"); !errors.Is(err, ErrCoordinatorDelegationExhausted) && !errors.Is(err, ErrCoordinatorDelegationInvalid) {
		t.Fatalf("expired visibility handoff err=%v", err)
	}
	if !bytes.Equal(before, ownerStateBytes(t, f.s)) {
		t.Fatal("expired visibility was consumed or state changed")
	}
}

func TestDelegationCoordinatorCannotPassOverFailOrUnknownOrSelfAccept(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegationWith(t, f, "del-pass", now, delegationOpts{
		actions: []string{"OFFER", "VOID", "EXPIRE", "HANDOFF", "PASS_OVER_FAIL", "SELF_ACCEPT"},
	})
	seedDelegatedAssignment(t, f, "asg-pass", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now)
	actor := assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}
	r := f.s.state.AssignmentLedger.Records["asg-pass"]
	for _, action := range []string{"PASS_OVER_FAIL", "PASS_OVER_UNKNOWN", "SELF_ACCEPT"} {
		if err := enforceDelegatedMutation(f.s.state, actor, action, r, now); !errors.Is(err, ErrCoordinatorDelegationInvalid) {
			t.Fatalf("%s err=%v", action, err)
		}
	}
}

func TestDelegationCannotLifecycleOwnerCredential(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegationWith(t, f, "del-oc", now, delegationOpts{
		actions: []string{"OFFER", "OWNER_CREDENTIAL_LIFECYCLE"},
	})
	seedDelegatedAssignment(t, f, "asg-oc", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now)
	actor := assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}
	r := f.s.state.AssignmentLedger.Records["asg-oc"]
	if err := enforceDelegatedMutation(f.s.state, actor, "OWNER_CREDENTIAL_LIFECYCLE", r, now); !errors.Is(err, ErrCoordinatorDelegationInvalid) {
		t.Fatalf("err=%v", err)
	}
}

func TestDelegationCannotRegisterOrRotateAgentSession(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegationWith(t, f, "del-as", now, delegationOpts{
		actions: []string{"OFFER", "AGENT_SESSION_REGISTER", "AGENT_SESSION_ROTATE"},
	})
	seedDelegatedAssignment(t, f, "asg-as", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now)
	actor := assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}
	r := f.s.state.AssignmentLedger.Records["asg-as"]
	for _, action := range []string{"AGENT_SESSION_REGISTER", "AGENT_SESSION_ROTATE", "AGENT_SESSION_REPLACE"} {
		if err := enforceDelegatedMutation(f.s.state, actor, action, r, now); !errors.Is(err, ErrCoordinatorDelegationInvalid) {
			t.Fatalf("%s err=%v", action, err)
		}
	}
}

func TestDelegationRejectsActionOutsideAllowedActions(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegationWith(t, f, "del-al", now, delegationOpts{actions: []string{"OFFER"}})
	actor := assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}
	seedDelegatedAssignment(t, f, "asg-al-seed", actor, now)
	offerDelegatedCopy(t, f, "asg-al", "asg-al-seed", now)
	r := f.s.state.AssignmentLedger.Records["asg-al"]
	if r.State != assignment.Offered {
		t.Fatalf("control state=%s", r.State)
	}
	before := ownerStateBytes(t, f.s)
	if err := f.s.ChangeAssignment(actor, "asg-al", r.Revision, "VOID", "not-allowed"); !errors.Is(err, ErrCoordinatorDelegationInvalid) {
		t.Fatalf("illegal action err=%v", err)
	}
	if !bytes.Equal(before, ownerStateBytes(t, f.s)) {
		t.Fatal("disallowed action changed state")
	}
	if err := enforceDelegatedMutation(f.s.state, actor, "OFFER", r, now); err != nil {
		t.Fatalf("positive allowed-action control: %v", err)
	}
}

func TestAbsenceOfDelegationDoesNotMakeLiveSessionCoordinator(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	// No grant created. Live session cannot claim coordinator via delegation path.
	if _, ok := findGrantForActingCoordinator(f.s.state, assignment.Principal{SessionID: "live:1", AgentID: "live", Generation: 1}, f.s.now().UTC()); ok {
		t.Fatal("absent grant minted acting coordinator")
	}
}

func TestPrimaryRestartRejectsWithoutFreshVisibility(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-pr1", now)
	seedDelegatedAssignment(t, f, "asg-pr1", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now)
	lossConfirm(t, f, "del-pr1", "loss-pr1", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1}, now)
	registerSession(t, f, "claude:a2", "agent-a")
	bumpSessionGeneration(t, f, "claude:a2", 2)
	if err := f.s.ApplyPrimaryRestart("del-pr1", assignment.Principal{SessionID: "claude:a2", AgentID: "agent-a", Generation: 2}, "no-vis"); !errors.Is(err, ErrCoordinatorDelegationInvalid) {
		t.Fatalf("err=%v", err)
	}
}

func TestPrimaryRestartRejectsOldSessionOrStillValidGeneration(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-pr2", now)
	seedDelegatedAssignment(t, f, "asg-pr2", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now)
	lossConfirm(t, f, "del-pr2", "loss-pr2", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1}, now)
	confirmPrimaryRestartVisibility(t, f, "del-pr2", "vis-pr2", "agent-a", now)
	// Old sessionId rejected even with a bumped generation claim.
	if err := f.s.RotateAgentSessionKey("claude:a", ed25519.PublicKey(strings.Repeat("q", 32))); err != nil {
		t.Fatal(err)
	}
	if err := f.s.ApplyPrimaryRestart("del-pr2", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 2}, "old-sess"); err == nil {
		t.Fatal("old session accepted")
	}
}

func TestPrimaryRestartRejectsWhilePredecessorStillActing(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-pr3", now)
	seedDelegatedAssignment(t, f, "asg-pr3", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now)
	registerSession(t, f, "codex:b", "agent-b")
	confirmVisibility(t, f, "del-pr3", "vis-pr3-b", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-b", SessionID: "codex:b", Generation: 1}, now)
	lossConfirm(t, f, "del-pr3", "loss-pr3-a", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1}, now)
	prev := assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}
	if err := f.s.HandoffAssignment(prev, assignment.Principal{SessionID: "codex:b", AgentID: "agent-b", Generation: 1}, "asg-pr3", f.s.state.AssignmentLedger.Records["asg-pr3"].Revision, "handoff-pr3"); err != nil {
		t.Fatalf("valid successor handoff setup: %v", err)
	}
	registerSession(t, f, "claude:a2", "agent-a")
	bumpSessionGeneration(t, f, "claude:a2", 2)
	confirmPrimaryRestartVisibility(t, f, "del-pr3", "vis-pr3-restart", "agent-a", now)
	before := ownerStateBytes(t, f.s)
	if err := f.s.ApplyPrimaryRestart("del-pr3", assignment.Principal{SessionID: "claude:a2", AgentID: "agent-a", Generation: 2}, "dual"); !errors.Is(err, ErrCoordinatorDelegationInvalid) {
		t.Fatalf("restart while successor acts err=%v", err)
	}
	if !bytes.Equal(before, ownerStateBytes(t, f.s)) {
		t.Fatal("blocked primary restart changed state")
	}
	// After the acting successor is separately lost, the same fresh primary
	// session and still-unconsumed visibility confirmation are a positive control.
	lossConfirm(t, f, "del-pr3", "loss-pr3-b", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-b", SessionID: "codex:b", Generation: 1}, now)
	if err := f.s.ApplyPrimaryRestart("del-pr3", assignment.Principal{SessionID: "claude:a2", AgentID: "agent-a", Generation: 2}, "after-successor-loss"); err != nil {
		t.Fatalf("primary restart after successor loss positive control: %v", err)
	}
}

func TestRecoveryA93B3_PrimaryRestartRejectsWhileSuccessorStillActing(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-restart-successor-active", now)
	seedDelegatedAssignment(t, f, "asg-restart-successor-active", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now)
	lossConfirm(t, f, "del-restart-successor-active", "loss-restart-successor-active", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1}, now)
	registerSession(t, f, "claude:a2", "agent-a")
	bumpSessionGeneration(t, f, "claude:a2", 2)
	confirmPrimaryRestartVisibility(t, f, "del-restart-successor-active", "vis-restart-successor-active", "agent-a", now)

	// Model a corrupted in-memory state where a listed successor is still
	// acting despite a pending loss. A primary restart must reject before it can
	// replace that coordinator; the normal successful path is the positive
	// control after restoring the loss-derived state.
	rec := f.s.state.CoordinatorDelegations["del-restart-successor-active"]
	actingSuccessor := DelegationPrincipal{StableAgentID: "agent-b", SessionID: "codex:b", Generation: 1}
	rec.ActingCoordinator = &actingSuccessor
	f.s.state.CoordinatorDelegations["del-restart-successor-active"] = rec
	before := ownerStateBytes(t, f.s)
	saves := f.p.saveCalls
	err := f.s.ApplyPrimaryRestart("del-restart-successor-active", assignment.Principal{SessionID: "claude:a2", AgentID: "agent-a", Generation: 2}, "restart-while-successor-acts")
	if !errors.Is(err, ErrCoordinatorDelegationInvalid) {
		t.Fatalf("restart while a successor remains acting err=%v", err)
	}
	if f.p.saveCalls != saves || !bytes.Equal(before, ownerStateBytes(t, f.s)) {
		t.Fatal("blocked restart while successor acting changed canonical state")
	}

	rec = f.s.state.CoordinatorDelegations["del-restart-successor-active"]
	rec.ActingCoordinator = nil
	f.s.state.CoordinatorDelegations["del-restart-successor-active"] = rec
	if err := f.s.ApplyPrimaryRestart("del-restart-successor-active", assignment.Principal{SessionID: "claude:a2", AgentID: "agent-a", Generation: 2}, "restart-after-successor-stopped"); err != nil {
		t.Fatalf("primary restart after restoring loss-derived state: %v", err)
	}
}

func TestRecoveryA93B3_PrimaryRestartRequiresRegisteredFreshSession(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-unregistered-restart", now)
	seedDelegatedAssignment(t, f, "asg-unregistered-restart", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now)
	lossConfirm(t, f, "del-unregistered-restart", "loss-unregistered-restart", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1}, now)
	confirmPrimaryRestartVisibility(t, f, "del-unregistered-restart", "vis-unregistered-restart", "agent-a", now)
	newSession := assignment.Principal{SessionID: "claude:never-registered", AgentID: "agent-a", Generation: 2}
	before := ownerStateBytes(t, f.s)
	if err := f.s.ApplyPrimaryRestart("del-unregistered-restart", newSession, "unregistered"); !errors.Is(err, ErrCoordinatorDelegationInvalid) {
		t.Fatalf("unregistered restart err=%v", err)
	}
	if !bytes.Equal(before, ownerStateBytes(t, f.s)) {
		t.Fatal("unregistered restart changed state")
	}
	registerSession(t, f, newSession.SessionID, newSession.AgentID)
	bumpSessionGeneration(t, f, newSession.SessionID, newSession.Generation)
	if err := f.s.ApplyPrimaryRestart("del-unregistered-restart", newSession, "registered-positive-control"); err != nil {
		t.Fatalf("registered fresh session positive control: %v", err)
	}
}

func TestRecoveryA93B3_PrimaryRestartGenerationMustAdvanceAcrossLosses(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-generation-history", now)
	seedDelegatedAssignment(t, f, "asg-generation-first", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now)
	lossConfirm(t, f, "del-generation-history", "loss-generation-first", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1}, now)
	registerSession(t, f, "claude:a2", "agent-a")
	bumpSessionGeneration(t, f, "claude:a2", 2)
	confirmPrimaryRestartVisibility(t, f, "del-generation-history", "vis-generation-first", "agent-a", now)
	if err := f.s.ApplyPrimaryRestart("del-generation-history", assignment.Principal{SessionID: "claude:a2", AgentID: "agent-a", Generation: 2}, "restart-generation-2"); err != nil {
		t.Fatalf("first restart positive control: %v", err)
	}
	seedDelegatedAssignment(t, f, "asg-generation-second", assignment.Principal{SessionID: "claude:a2", AgentID: "agent-a", Generation: 2}, now)
	lossConfirm(t, f, "del-generation-history", "loss-generation-second", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a2", Generation: 2}, now)
	registerSession(t, f, "claude:a3", "agent-a")
	bumpSessionGeneration(t, f, "claude:a3", 2)
	confirmPrimaryRestartVisibility(t, f, "del-generation-history", "vis-generation-stale", "agent-a", now)
	stale := assignment.Principal{SessionID: "claude:a3", AgentID: "agent-a", Generation: 2}
	before := ownerStateBytes(t, f.s)
	if err := f.s.ApplyPrimaryRestart("del-generation-history", stale, "restart-stale-generation"); !errors.Is(err, ErrCoordinatorDelegationInvalid) {
		t.Fatalf("generation 2 reused after prior generation 2 err=%v", err)
	}
	if !bytes.Equal(before, ownerStateBytes(t, f.s)) {
		t.Fatal("stale generation restart changed state")
	}
	if err := f.s.RotateAgentSessionKey("claude:a3", ed25519.PublicKey(strings.Repeat("r", 32))); err != nil {
		t.Fatal(err)
	}
	fresh := assignment.Principal{SessionID: "claude:a3", AgentID: "agent-a", Generation: 3}
	if err := f.s.ApplyPrimaryRestart("del-generation-history", fresh, "restart-generation-3"); err != nil {
		t.Fatalf("generation 3 positive control: %v", err)
	}
}

func TestRecoveryA93B3_PrimaryRestartGenerationMustExceedPriorPrimaryAfterSuccessorLoss(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-primary-generation-high-water", now)
	seedDelegatedAssignment(t, f, "asg-primary-first-loss", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now)
	lossConfirm(t, f, "del-primary-generation-high-water", "loss-primary-first", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1}, now)
	registerSession(t, f, "claude:a2", "agent-a")
	bumpSessionGeneration(t, f, "claude:a2", 2)
	confirmPrimaryRestartVisibility(t, f, "del-primary-generation-high-water", "vis-primary-first", "agent-a", now)
	if err := f.s.ApplyPrimaryRestart("del-primary-generation-high-water", assignment.Principal{SessionID: "claude:a2", AgentID: "agent-a", Generation: 2}, "primary-generation-2"); err != nil {
		t.Fatalf("initial generation-2 restart positive control: %v", err)
	}

	seedDelegatedAssignment(t, f, "asg-primary-successor-loss", assignment.Principal{SessionID: "claude:a2", AgentID: "agent-a", Generation: 2}, now)
	registerSession(t, f, "codex:b", "agent-b")
	confirmVisibility(t, f, "del-primary-generation-high-water", "vis-successor", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-b", SessionID: "codex:b", Generation: 1}, now)
	lossConfirm(t, f, "del-primary-generation-high-water", "loss-primary-second", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a2", Generation: 2}, now)
	prev := assignment.Principal{SessionID: "claude:a2", AgentID: "agent-a", Generation: 2}
	next := assignment.Principal{SessionID: "codex:b", AgentID: "agent-b", Generation: 1}
	if err := f.s.HandoffAssignment(prev, next, "asg-primary-successor-loss", f.s.state.AssignmentLedger.Records["asg-primary-successor-loss"].Revision, "handoff-to-successor"); err != nil {
		t.Fatalf("successor handoff setup: %v", err)
	}
	lossConfirm(t, f, "del-primary-generation-high-water", "loss-successor", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-b", SessionID: "codex:b", Generation: 1}, now)
	confirmPrimaryRestartVisibility(t, f, "del-primary-generation-high-water", "vis-primary-stale", "agent-a", now)
	registerSession(t, f, "claude:a3", "agent-a")
	bumpSessionGeneration(t, f, "claude:a3", 2)
	stale := assignment.Principal{SessionID: "claude:a3", AgentID: "agent-a", Generation: 2}
	before := ownerStateBytes(t, f.s)
	if err := f.s.ApplyPrimaryRestart("del-primary-generation-high-water", stale, "stale-primary-generation-2"); !errors.Is(err, ErrCoordinatorDelegationInvalid) {
		t.Fatalf("primary generation 2 reused after a lower-generation successor loss err=%v", err)
	}
	if !bytes.Equal(before, ownerStateBytes(t, f.s)) {
		t.Fatal("stale high-water generation restart changed state")
	}
	if err := f.s.RotateAgentSessionKey("claude:a3", ed25519.PublicKey(strings.Repeat("s", 32))); err != nil {
		t.Fatal(err)
	}
	fresh := assignment.Principal{SessionID: "claude:a3", AgentID: "agent-a", Generation: 3}
	if err := f.s.ApplyPrimaryRestart("del-primary-generation-high-water", fresh, "fresh-primary-generation-3"); err != nil {
		t.Fatalf("generation-3 positive control after successor loss: %v", err)
	}
}

func TestRecoveryA93B3_ExpiredGrantRejectsOtherwiseLegalVoid(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	issuedAt := f.s.now().UTC()
	grantDelegation(t, f, "del-expired-void", issuedAt)
	seedDelegatedAssignment(t, f, "asg-expired-void", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, issuedAt)
	offerDelegatedCopy(t, f, "asg-expired-void-open", "asg-expired-void", issuedAt)
	actor := assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}
	offered := f.s.state.AssignmentLedger.Records["asg-expired-void-open"]
	if err := enforceDelegatedMutation(f.s.state, actor, "VOID", offered, issuedAt); err != nil {
		t.Fatalf("unexpired positive control: %v", err)
	}
	expiredAt := f.s.state.CoordinatorDelegations["del-expired-void"].Validity.ExpiresAt
	f.s.now = func() time.Time { return expiredAt }
	before := ownerStateBytes(t, f.s)
	if err := f.s.ChangeAssignment(actor, offered.AssignmentID, offered.Revision, "VOID", "expired"); !errors.Is(err, ErrCoordinatorDelegationExhausted) {
		t.Fatalf("expired grant VOID err=%v", err)
	}
	if !bytes.Equal(before, ownerStateBytes(t, f.s)) {
		t.Fatal("expired grant permitted state change")
	}
}

func TestRecoveryA93B3_HandoffUsesFirstEligibleSuccessor(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegationWith(t, f, "del-ordered", now, delegationOpts{successors: []ownercredential.DelegationPrincipalPayload{{StableAgentID: "agent-b", SessionID: "codex:b", Generation: 1}, {StableAgentID: "agent-c", SessionID: "gemini:c", Generation: 1}}, actions: []string{"OFFER", "VOID", "HANDOFF"}})
	seedDelegatedAssignment(t, f, "asg-ordered", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now)
	for _, target := range []struct{ session, agent string }{{"codex:b", "agent-b"}, {"gemini:c", "agent-c"}} {
		registerSession(t, f, target.session, target.agent)
	}
	confirmVisibility(t, f, "del-ordered", "vis-ordered-b", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-b", SessionID: "codex:b", Generation: 1}, now)
	confirmVisibility(t, f, "del-ordered", "vis-ordered-c", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-c", SessionID: "gemini:c", Generation: 1}, now)
	lossConfirm(t, f, "del-ordered", "loss-ordered", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1}, now)
	prev := assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}
	before := ownerStateBytes(t, f.s)
	if err := f.s.HandoffAssignment(prev, assignment.Principal{SessionID: "gemini:c", AgentID: "agent-c", Generation: 1}, "asg-ordered", f.s.state.AssignmentLedger.Records["asg-ordered"].Revision, "skip-first"); !errors.Is(err, ErrCoordinatorDelegationInvalid) {
		t.Fatalf("second successor handoff err=%v", err)
	}
	if !bytes.Equal(before, ownerStateBytes(t, f.s)) {
		t.Fatal("out-of-order handoff changed state")
	}
	if err := f.s.HandoffAssignment(prev, assignment.Principal{SessionID: "codex:b", AgentID: "agent-b", Generation: 1}, "asg-ordered", f.s.state.AssignmentLedger.Records["asg-ordered"].Revision, "first-successor"); err != nil {
		t.Fatalf("first eligible successor control: %v", err)
	}
}

func TestRestartedSessionCannotObserveOwnPriorLoss(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-pr4", now)
	seedDelegatedAssignment(t, f, "asg-pr4", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now)
	registerSession(t, f, "claude:a2", "agent-a")
	restarted := assignment.Principal{SessionID: "claude:a2", AgentID: "agent-a", Generation: 1}
	prev := assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}
	if err := f.s.ObserveAssignmentCoordinatorLoss(restarted, prev, "self-loss"); !errors.Is(err, ErrCoordinatorDelegationInvalid) {
		t.Fatalf("err=%v", err)
	}
}

func TestDelegatedExpireBeforeValidUntilRejected(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-exp", now)
	seedDelegatedAssignment(t, f, "asg-exp", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now)
	actor := assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}
	r := f.s.state.AssignmentLedger.Records["asg-exp"]
	if err := f.s.ChangeAssignment(actor, "asg-exp", r.Revision, "EXPIRE", "early"); err == nil {
		t.Fatal("early expire accepted")
	}
}

func TestDelegatedVoidRejectsActiveIncludingBeforeWorkStarted(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-void", now)
	seedDelegatedAssignment(t, f, "asg-void", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now)
	actor := assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}
	r := f.s.state.AssignmentLedger.Records["asg-void"]
	// Seeded as ACTIVE.
	if r.State != assignment.Active {
		t.Fatalf("state=%s", r.State)
	}
	if err := f.s.ChangeAssignment(actor, "asg-void", r.Revision, "VOID", "void-active"); !errors.Is(err, ErrCoordinatorDelegationInvalid) {
		t.Fatalf("err=%v", err)
	}
}

func TestUnnamedGenerationCannotContinueWork(t *testing.T) {
	TestDelegationRejectsMismatchedGenerationNoInheritance(t)
}

func TestExhaustionOutsideListCaptureRejected(t *testing.T) {
	TestDelegationRejectsFirstLiveSessionCapture(t)
}

func TestSuccessorCannotReorderOrSelfInsertList(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-list", now)
	body := []byte(`{"delegationId":"del-list","coordinator":{"stableAgentId":"agent-a","sessionId":"claude:a","generation":1},"reason":"x","observedAt":"` + now.Format(time.RFC3339Nano) + `","successors":[{"stableAgentId":"me","sessionId":"x","generation":1}]}`)
	rev := f.s.state.OwnerCredentials["owner-1"].CredentialRevision
	cmd := f.command(ownercredential.CommandLossConfirm, "loss-list-edit", "owner-primary", rev, body)
	sign := f.s.state.OwnerCredentials["owner-1"].Credentials["owner-primary"].SignCount + 1
	assertOwnerCommandRejectedWithoutWrite(t, f, cmd, assertionFor(cmd, f.private["owner-primary"], sign), ErrCoordinatorDelegationInvalid)
	before := f.s.state.CoordinatorDelegations["del-list"]
	if len(before.Successors) != 1 || before.Successors[0].StableAgentID != "agent-b" {
		t.Fatal("successor list mutated")
	}
}

func TestExpiredOrRevokedSuccessorEntryNotApplied(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-skip-rev", now)
	seedDelegatedAssignment(t, f, "asg-skip-rev", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now)
	revokePayload, _ := json.Marshal(ownercredential.DelegationRevokePayload{DelegationID: "del-skip-rev", Reason: "stop"})
	rev := f.s.state.OwnerCredentials["owner-1"].CredentialRevision
	cmd := f.command(ownercredential.CommandDelegationRevoke, "rev-skip", "owner-primary", rev, revokePayload)
	sign := f.s.state.OwnerCredentials["owner-1"].Credentials["owner-primary"].SignCount + 1
	if _, _, err := f.s.ApplyOwnerCredentialCommand(cmd.ChallengeID, cmd, assertionFor(cmd, f.private["owner-primary"], sign)); err != nil {
		t.Fatal(err)
	}
	registerSession(t, f, "codex:b", "agent-b")
	_, _, err := SelectDelegationSuccessor(f.s.state, f.s.state.CoordinatorDelegations["del-skip-rev"], "asg-skip-rev", now)
	if !errors.Is(err, ErrCoordinatorDelegationExhausted) {
		t.Fatalf("err=%v", err)
	}
}

func TestLossConfirmRejectsStaleCoordinatorGeneration(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-stale", now)
	seedDelegatedAssignment(t, f, "asg-stale", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now)
	lossPayload, _ := json.Marshal(ownercredential.LossConfirmPayload{
		DelegationID: "del-stale",
		Coordinator:  ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 99},
		Reason:       "stale", ObservedAt: now,
	})
	rev := f.s.state.OwnerCredentials["owner-1"].CredentialRevision
	cmd := f.command(ownercredential.CommandLossConfirm, "loss-stale", "owner-primary", rev, lossPayload)
	sign := f.s.state.OwnerCredentials["owner-1"].Credentials["owner-primary"].SignCount + 1
	assertOwnerCommandRejectedWithoutWrite(t, f, cmd, assertionFor(cmd, f.private["owner-primary"], sign), ErrCoordinatorDelegationInvalid)
}

func TestLossConfirmAlteredBytesRejected(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-alt", now)
	seedDelegatedAssignment(t, f, "asg-alt", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now)
	assignmentIDs := openDelegatedAssignmentIDs(f, "del-alt", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1})
	lossPayload, _ := json.Marshal(ownercredential.LossConfirmPayload{
		DelegationID:  "del-alt",
		Coordinator:   ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1},
		AssignmentIDs: assignmentIDs,
		Reason:        "hung", ObservedAt: now,
	})
	rev := f.s.state.OwnerCredentials["owner-1"].CredentialRevision
	cmd := f.command(ownercredential.CommandLossConfirm, "loss-alt", "owner-primary", rev, lossPayload)
	sign := f.s.state.OwnerCredentials["owner-1"].Credentials["owner-primary"].SignCount + 1
	if _, _, err := f.s.ApplyOwnerCredentialCommand(cmd.ChallengeID, cmd, assertionFor(cmd, f.private["owner-primary"], sign)); err != nil {
		t.Fatal(err)
	}
	// Same messageID, altered reason bytes → replay reject.
	altered, _ := json.Marshal(ownercredential.LossConfirmPayload{
		DelegationID:  "del-alt",
		Coordinator:   ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1},
		AssignmentIDs: assignmentIDs,
		Reason:        "different", ObservedAt: now,
	})
	// Re-issue challenge with same message id is not possible through IssueOwnerCommandChallenge
	// after receipt exists; apply with mutated command digest path via nonce reuse / altered command.
	cmd2 := f.command(ownercredential.CommandLossConfirm, "loss-alt", "owner-primary", rev, altered)
	sign2 := f.s.state.OwnerCredentials["owner-1"].Credentials["owner-primary"].SignCount + 1
	if _, _, err := f.s.ApplyOwnerCredentialCommand(cmd2.ChallengeID, cmd2, assertionFor(cmd2, f.private["owner-primary"], sign2)); err == nil {
		t.Fatal("altered bytes accepted")
	}
}

func TestOwnerDelegationCommandsExactRetryReturnsPriorReceipt(t *testing.T) {
	TestDelegationGrantRevokeExactRetryAndCutoverInert(t)
}

func TestLossConfirmRejectsScopeChangeInSameCommand(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-sc", now)
	seedDelegatedAssignment(t, f, "asg-sc", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now)
	body := []byte(`{"delegationId":"del-sc","coordinator":{"stableAgentId":"agent-a","sessionId":"claude:a","generation":1},"reason":"x","observedAt":"` + now.Format(time.RFC3339Nano) + `","scope":{"repository":"evil","project":"p","allowedWorkItems":["a"],"allowedActions":["OFFER"]}}`)
	rev := f.s.state.OwnerCredentials["owner-1"].CredentialRevision
	cmd := f.command(ownercredential.CommandLossConfirm, "loss-sc", "owner-primary", rev, body)
	sign := f.s.state.OwnerCredentials["owner-1"].Credentials["owner-primary"].SignCount + 1
	assertOwnerCommandRejectedWithoutWrite(t, f, cmd, assertionFor(cmd, f.private["owner-primary"], sign), ErrCoordinatorDelegationInvalid)
}

func TestPathAObserverCannotBecomeSuccessorForThatLoss(t *testing.T) {
	TestDelegationHandoffRejectsLossObserverAsSuccessor(t)
}

func TestAffectedTargetCannotRecordLossOrBecomeSuccessor(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-at", now)
	seedDelegatedAssignment(t, f, "asg-at", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now)
	target := assignment.Principal{SessionID: "codex:worker", AgentID: "worker", Generation: 1}
	prev := assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}
	if err := f.s.ObserveAssignmentCoordinatorLoss(target, prev, "target-loss"); err == nil {
		t.Fatal("target recorded loss")
	}
	TestDelegationHandoffRejectsAffectedTargetAsSuccessor(t)
}

func TestAfterRestartNextGenerationDoesNotAutoContinue(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-pr5", now)
	seedDelegatedAssignment(t, f, "asg-pr5", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now)
	lossConfirm(t, f, "del-pr5", "loss-pr5", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1}, now)
	registerSession(t, f, "claude:a2", "agent-a")
	bumpSessionGeneration(t, f, "claude:a2", 2)
	confirmVisibility(t, f, "del-pr5", "vis-pr5-trip", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a2", Generation: 2}, now)
	if err := f.s.ApplyPrimaryRestart("del-pr5", assignment.Principal{SessionID: "claude:a2", AgentID: "agent-a", Generation: 2}, "restart-ok"); err != nil {
		t.Fatal(err)
	}
	// Next generation of same session without new loss/visibility must not auto-continue.
	bumpSessionGeneration(t, f, "claude:a2", 3)
	gen3 := assignment.Principal{SessionID: "claude:a2", AgentID: "agent-a", Generation: 3}
	if _, ok := findGrantForActingCoordinator(f.s.state, gen3, now); ok {
		t.Fatal("next generation auto-continued")
	}
}

func TestLossConfirmRejectsWhenNoOpenDelegatedWork(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-nowork", now)
	lossPayload, _ := json.Marshal(ownercredential.LossConfirmPayload{
		DelegationID: "del-nowork",
		Coordinator:  ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1},
		Reason:       "none", ObservedAt: now,
	})
	rev := f.s.state.OwnerCredentials["owner-1"].CredentialRevision
	cmd := f.command(ownercredential.CommandLossConfirm, "loss-nowork", "owner-primary", rev, lossPayload)
	sign := f.s.state.OwnerCredentials["owner-1"].Credentials["owner-primary"].SignCount + 1
	assertOwnerCommandRejectedWithoutWrite(t, f, cmd, assertionFor(cmd, f.private["owner-primary"], sign), ErrCoordinatorDelegationInvalid)
}

func TestLossConfirmRejectsDuplicateOpenOrClosedLoss(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-dup", now)
	seedDelegatedAssignment(t, f, "asg-dup", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now)
	lossConfirm(t, f, "del-dup", "loss-dup-1", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1}, now)
	lossPayload, _ := json.Marshal(ownercredential.LossConfirmPayload{
		DelegationID: "del-dup",
		Coordinator:  ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1},
		Reason:       "again", ObservedAt: now,
	})
	rev := f.s.state.OwnerCredentials["owner-1"].CredentialRevision
	cmd := f.command(ownercredential.CommandLossConfirm, "loss-dup-2", "owner-primary", rev, lossPayload)
	sign := f.s.state.OwnerCredentials["owner-1"].Credentials["owner-primary"].SignCount + 1
	assertOwnerCommandRejectedWithoutWrite(t, f, cmd, assertionFor(cmd, f.private["owner-primary"], sign), ErrCoordinatorDelegationInvalid)
}

func TestLossConfirmRejectsUnrelatedAssignments(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-unrel", now)
	seedDelegatedAssignment(t, f, "asg-unrel-1", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now)
	// Unrelated assignment under a different coordinator.
	other := assignment.Principal{SessionID: "other:1", AgentID: "other-agent", Generation: 1}
	registerSession(t, f, "other:1", "other-agent")
	seedDelegatedAssignment(t, f, "asg-unrel-2", other, now)
	beforeOther := f.s.state.AssignmentLedger.Records["asg-unrel-2"]
	lossConfirm(t, f, "del-unrel", "loss-unrel", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1}, now)
	afterOther := f.s.state.AssignmentLedger.Records["asg-unrel-2"]
	if afterOther.Revision != beforeOther.Revision || afterOther.CoordinatorLost {
		t.Fatal("unrelated assignment mutated")
	}
}

func TestLossConfirmRejectsSuccessorListOrExpiryChange(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-lex", now)
	seedDelegatedAssignment(t, f, "asg-lex", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now)
	body := []byte(`{"delegationId":"del-lex","coordinator":{"stableAgentId":"agent-a","sessionId":"claude:a","generation":1},"reason":"x","observedAt":"` + now.Format(time.RFC3339Nano) + `","expiresAt":"2099-01-01T00:00:00Z"}`)
	rev := f.s.state.OwnerCredentials["owner-1"].CredentialRevision
	cmd := f.command(ownercredential.CommandLossConfirm, "loss-lex", "owner-primary", rev, body)
	sign := f.s.state.OwnerCredentials["owner-1"].Credentials["owner-primary"].SignCount + 1
	assertOwnerCommandRejectedWithoutWrite(t, f, cmd, assertionFor(cmd, f.private["owner-primary"], sign), ErrCoordinatorDelegationInvalid)
}

func TestLossConfirmRejectsPickOutsideSuccessorList(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-pick", now)
	seedDelegatedAssignment(t, f, "asg-pick", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now)
	body := []byte(`{"delegationId":"del-pick","coordinator":{"stableAgentId":"agent-a","sessionId":"claude:a","generation":1},"reason":"x","observedAt":"` + now.Format(time.RFC3339Nano) + `","successor":{"stableAgentId":"outsider","sessionId":"x:y","generation":1}}`)
	rev := f.s.state.OwnerCredentials["owner-1"].CredentialRevision
	cmd := f.command(ownercredential.CommandLossConfirm, "loss-pick", "owner-primary", rev, body)
	sign := f.s.state.OwnerCredentials["owner-1"].Credentials["owner-primary"].SignCount + 1
	assertOwnerCommandRejectedWithoutWrite(t, f, cmd, assertionFor(cmd, f.private["owner-primary"], sign), ErrCoordinatorDelegationInvalid)
}

func TestLossConfirmRejectsRevivingTerminalAssignment(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-term", now)
	// Seed offered then VOID via direct ledger (OWNER #90 breadth) under primary.
	seedDelegatedAssignment(t, f, "asg-term", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now)
	if err := f.s.UpdateCanonical(func(state *CanonicalState) error {
		r := state.AssignmentLedger.Records["asg-term"]
		next := r
		next.State = assignment.Void
		next.Revision++
		next.UpdatedAt = now
		return state.AssignmentLedger.Append(assignment.Event{
			AssignmentID: "asg-term", Revision: next.Revision, Kind: "VOID",
			Actor:       assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1},
			EvidenceRef: "owner-void", At: now, Record: next,
		})
	}); err != nil {
		t.Fatal(err)
	}
	lossPayload, _ := json.Marshal(ownercredential.LossConfirmPayload{
		DelegationID: "del-term",
		Coordinator:  ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1},
		Reason:       "revive", ObservedAt: now,
	})
	rev := f.s.state.OwnerCredentials["owner-1"].CredentialRevision
	cmd := f.command(ownercredential.CommandLossConfirm, "loss-term", "owner-primary", rev, lossPayload)
	sign := f.s.state.OwnerCredentials["owner-1"].Credentials["owner-primary"].SignCount + 1
	assertOwnerCommandRejectedWithoutWrite(t, f, cmd, assertionFor(cmd, f.private["owner-primary"], sign), ErrCoordinatorDelegationInvalid)
	if f.s.state.AssignmentLedger.Records["asg-term"].State != assignment.Void {
		t.Fatal("terminal revived")
	}
}

func TestLossConfirmRejectsRoleIndependenceBypass(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-role", now)
	seedDelegatedAssignment(t, f, "asg-role", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now)
	body := []byte(`{"delegationId":"del-role","coordinator":{"stableAgentId":"agent-a","sessionId":"claude:a","generation":1},"reason":"x","observedAt":"` + now.Format(time.RFC3339Nano) + `","bypassRoleIndependence":true}`)
	rev := f.s.state.OwnerCredentials["owner-1"].CredentialRevision
	cmd := f.command(ownercredential.CommandLossConfirm, "loss-role", "owner-primary", rev, body)
	sign := f.s.state.OwnerCredentials["owner-1"].Credentials["owner-primary"].SignCount + 1
	assertOwnerCommandRejectedWithoutWrite(t, f, cmd, assertionFor(cmd, f.private["owner-primary"], sign), ErrCoordinatorDelegationInvalid)
}

func TestLossConfirmBarsStillRegisteredCoordinatorUntilHandoff(t *testing.T) {
	TestLossConfirmBarsStillRegisteredAndRequiresOpenWork(t)
}

func TestSnapshotReloadAcceptsOwnerCredentialLossWithMatchingReceipt(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-ok-receipt", now)
	seedDelegatedAssignment(t, f, "asg-ok-receipt", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now)
	lossConfirm(t, f, "del-ok-receipt", "loss-ok-receipt", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1}, now)
	snap, err := f.s.CanonicalSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateCanonicalState(snap); err != nil {
		t.Fatalf("matching receipt rejected: %v", err)
	}
}

func TestSnapshotReloadRejectsOwnerCredentialLossMissingOrMismatchReceipt(t *testing.T) {
	TestSnapshotReloadRejectsOwnerCredentialLossWithoutReceipt(t)
}

func TestForgedOwnerCredentialActorWithoutReceiptRejected(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-forged-owner", now)
	seedDelegatedAssignment(t, f, "asg-forged-owner", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now)
	lossConfirm(t, f, "del-forged-owner", "loss-forged-owner", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1}, now)
	snapshot, err := copyCanonicalState(f.s.state)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateCanonicalState(snapshot); err != nil {
		t.Fatalf("positive receipt control rejected: %v", err)
	}
	for i := range snapshot.AssignmentLedger.Events {
		e := &snapshot.AssignmentLedger.Events[i]
		if e.Kind == "COORDINATOR_LOST" && e.EffectiveActorSource() == assignment.ActorSourceOwnerCredential {
			e.OwnerReceiptMessageID = "missing-owner-receipt"
			e.EvidenceRef = e.OwnerReceiptMessageID
			break
		}
	}
	if err := ValidateCanonicalState(snapshot); err == nil {
		t.Fatal("forged OWNER_CREDENTIAL loss without a matching receipt accepted")
	}
}

func TestRecoveryA93B5_DelegationCommandsAuthenticateBytesAndNonce(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-auth-negative", now)
	payloadFor := func(expires time.Time) []byte {
		body, err := json.Marshal(ownercredential.VisibilityConfirmPayload{DelegationID: "del-auth-negative", Successor: &ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-b", SessionID: "codex:b", Generation: 1}, IssuedAt: now, ExpiresAt: expires, SingleUse: true})
		if err != nil {
			t.Fatal(err)
		}
		return body
	}
	request := func(message string, expires time.Time) ownercredential.Command {
		return f.command(ownercredential.CommandVisibilityConfirm, message, "owner-primary", f.s.state.OwnerCredentials["owner-1"].CredentialRevision, payloadFor(expires))
	}

	t.Run("bad_signature", func(t *testing.T) {
		cmd := request("visibility-bad-signature", now.Add(time.Minute))
		before := ownerStateBytes(t, f.s)
		saves := f.p.saveCalls
		bad := assertionFor(cmd, f.private["owner-backup"], f.s.state.OwnerCredentials["owner-1"].Credentials["owner-primary"].SignCount+1)
		if _, _, err := f.s.ApplyOwnerCredentialCommand(cmd.ChallengeID, cmd, bad); !errors.Is(err, ErrOwnerCommandInvalid) {
			t.Fatalf("bad signature err=%v", err)
		}
		if f.p.saveCalls != saves || !bytes.Equal(before, ownerStateBytes(t, f.s)) {
			t.Fatal("bad signature changed canonical state")
		}
	})

	t.Run("altered_signed_payload", func(t *testing.T) {
		cmd := request("visibility-altered-bytes", now.Add(2*time.Minute))
		before := ownerStateBytes(t, f.s)
		saves := f.p.saveCalls
		cmd.Payload = payloadFor(now.Add(3 * time.Minute))
		if _, _, err := f.s.ApplyOwnerCredentialCommand(cmd.ChallengeID, cmd, assertionFor(cmd, f.private["owner-primary"], f.s.state.OwnerCredentials["owner-1"].Credentials["owner-primary"].SignCount+1)); !errors.Is(err, ErrOwnerChallengeUnknown) {
			t.Fatalf("altered payload err=%v", err)
		}
		if f.p.saveCalls != saves || !bytes.Equal(before, ownerStateBytes(t, f.s)) {
			t.Fatal("altered bytes changed canonical state")
		}
	})

	t.Run("reused_nonce_for_distinct_command", func(t *testing.T) {
		first := request("visibility-nonce-first", now.Add(4*time.Minute))
		firstSign := f.s.state.OwnerCredentials["owner-1"].Credentials["owner-primary"].SignCount + 1
		firstReceipt, replayed, err := f.s.ApplyOwnerCredentialCommand(first.ChallengeID, first, assertionFor(first, f.private["owner-primary"], firstSign))
		if err != nil || replayed {
			t.Fatalf("positive first command receipt=%+v replay=%v err=%v", firstReceipt, replayed, err)
		}
		second := request("visibility-nonce-second", now.Add(5*time.Minute))
		second.Nonce = firstReceipt.Nonce
		data, err := second.DigestBytes()
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(data)
		second.Digest = hex.EncodeToString(digest[:])
		second.WebAuthnChallenge = base64.RawURLEncoding.EncodeToString(digest[:])
		f.s.mu.Lock()
		f.s.ownerChallenges[second.ChallengeID] = second
		f.s.mu.Unlock()
		before := ownerStateBytes(t, f.s)
		saves := f.p.saveCalls
		sign := f.s.state.OwnerCredentials["owner-1"].Credentials["owner-primary"].SignCount + 1
		if _, _, err := f.s.ApplyOwnerCredentialCommand(second.ChallengeID, second, assertionFor(second, f.private["owner-primary"], sign)); !errors.Is(err, ErrOwnerCommandReplay) {
			t.Fatalf("reused nonce err=%v", err)
		}
		if f.p.saveCalls != saves || !bytes.Equal(before, ownerStateBytes(t, f.s)) {
			t.Fatal("reused nonce changed canonical state")
		}
	})

	t.Run("durable_exact_retry_still_verifies_signature", func(t *testing.T) {
		cmd := request("visibility-retry-signature", now.Add(6*time.Minute))
		sign := f.s.state.OwnerCredentials["owner-1"].Credentials["owner-primary"].SignCount + 1
		first, replayed, err := f.s.ApplyOwnerCredentialCommand(cmd.ChallengeID, cmd, assertionFor(cmd, f.private["owner-primary"], sign))
		if err != nil || replayed || first.MessageID != cmd.MessageID {
			t.Fatalf("initial command receipt=%+v replayed=%v err=%v", first, replayed, err)
		}
		before := ownerStateBytes(t, f.s)
		saves := f.p.saveCalls
		bad := assertionFor(cmd, f.private["owner-backup"], sign)
		if _, replayed, err := f.s.ApplyOwnerCredentialCommand(cmd.ChallengeID, cmd, bad); !errors.Is(err, ErrOwnerCommandInvalid) || replayed {
			t.Fatalf("durable retry with invalid signature replayed=%v err=%v", replayed, err)
		}
		if f.p.saveCalls != saves || !bytes.Equal(before, ownerStateBytes(t, f.s)) {
			t.Fatal("invalid durable retry changed canonical state")
		}
	})
}

func TestRecoveryA93B3_DelegationSaveFailurePublishesNothing(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegation(t, f, "del-save-failure", now)
	confirmVisibility(t, f, "del-save-failure", "visibility-save-positive", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-b", SessionID: "codex:b", Generation: 1}, now)
	payload, _ := json.Marshal(ownercredential.VisibilityConfirmPayload{DelegationID: "del-save-failure", Successor: &ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-b", SessionID: "codex:b", Generation: 1}, IssuedAt: now, ExpiresAt: now.Add(time.Minute), SingleUse: true})
	revision := f.s.state.OwnerCredentials["owner-1"].CredentialRevision
	cmd := f.command(ownercredential.CommandVisibilityConfirm, "visibility-save-failure", "owner-primary", revision, payload)
	beforeStore := ownerStateBytes(t, f.s)
	beforeDisk, err := json.Marshal(f.p.state)
	if err != nil {
		t.Fatal(err)
	}
	saves := f.p.saveCalls
	f.p.fail = true
	sign := f.s.state.OwnerCredentials["owner-1"].Credentials["owner-primary"].SignCount + 1
	if _, _, err := f.s.ApplyOwnerCredentialCommand(cmd.ChallengeID, cmd, assertionFor(cmd, f.private["owner-primary"], sign)); !errors.Is(err, ErrPersistence) {
		t.Fatalf("failed Save err=%v", err)
	}
	afterDisk, err := json.Marshal(f.p.state)
	if err != nil {
		t.Fatal(err)
	}
	if f.p.saveCalls != saves+1 || !bytes.Equal(beforeStore, ownerStateBytes(t, f.s)) || !bytes.Equal(beforeDisk, afterDisk) {
		t.Fatal("failed delegation Save published in-memory or persisted state")
	}
	// A persistence failure leaves durability uncertain. The same live store
	// must remain poisoned until reopened instead of retrying and claiming a
	// commit whose first Save may have partially reached durable storage.
	f.p.fail = false
	if _, _, err := f.s.ApplyOwnerCredentialCommand(cmd.ChallengeID, cmd, assertionFor(cmd, f.private["owner-primary"], sign)); !errors.Is(err, ErrPersistence) {
		t.Fatalf("retry after delegation Save failure err=%v, want latched persistence error", err)
	}
	if f.p.saveCalls != saves+1 || !bytes.Equal(beforeStore, ownerStateBytes(t, f.s)) {
		t.Fatal("retry after failed Save wrote or published state")
	}
}

// --- helpers ---

type delegationOpts struct {
	successors []ownercredential.DelegationPrincipalPayload
	actions    []string
	repository string
	project    string
	primary    ownercredential.DelegationPrincipalPayload
}

func grantDelegationWith(t *testing.T, f *ownerCommandFixture, id string, now time.Time, opts delegationOpts) {
	t.Helper()
	successors := opts.successors
	if len(successors) == 0 {
		successors = []ownercredential.DelegationPrincipalPayload{{StableAgentID: "agent-b", SessionID: "codex:b", Generation: 1}}
	}
	actions := opts.actions
	if len(actions) == 0 {
		actions = []string{"OFFER", "VOID", "EXPIRE", "HANDOFF", "ACTIVATE", "ACCEPT_RESULT"}
	}
	repository, project := opts.repository, opts.project
	if repository == "" {
		repository = "naghuale/coord"
	}
	if project == "" {
		project = "coord"
	}
	primary := opts.primary
	if primary.StableAgentID == "" {
		primary = ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1}
	}
	payload, err := json.Marshal(ownercredential.DelegationGrantPayload{
		DelegationID: id,
		Scope: ownercredential.DelegationScopePayload{
			Repository: repository, Project: project,
			AllowedWorkItems: []string{"issue-93b"}, AllowedActions: actions,
		},
		PrimaryCoordinator: primary,
		Validity:           ownercredential.DelegationValidityPayload{NotBefore: now.Add(-time.Minute), ExpiresAt: now.Add(2 * time.Hour)},
		Successors:         successors,
		PrimaryRestart:     &ownercredential.PrimaryRestartPayload{StableAgentID: primary.StableAgentID},
		Evidence:           ownercredential.DelegationEvidencePayload{OwnerDecisionRef: "ADR-0010", VisibilityEvidence: "owner-seen"},
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

func registerSession(t *testing.T, f *ownerCommandFixture, sessionID, agentID string) {
	t.Helper()
	if _, ok := f.s.state.AgentSessions[sessionID]; ok {
		return
	}
	key := ed25519.PublicKey(strings.Repeat("b", 32))
	if err := f.s.RegisterAgentSession(sessionID, agentID, key); err != nil {
		t.Fatal(err)
	}
}

// bumpSessionGeneration key-rotates sessionID until its AgentSession generation
// equals target. New addresses register at generation 1; a primaryRestart
// "new generation" is a strictly higher generation on that fresh address.
func bumpSessionGeneration(t *testing.T, f *ownerCommandFixture, sessionID string, target uint64) {
	t.Helper()
	sess, ok := f.s.state.AgentSessions[sessionID]
	if !ok {
		t.Fatalf("session %s missing", sessionID)
	}
	n := byte(0)
	for sess.Generation < target {
		n++
		key := make(ed25519.PublicKey, ed25519.PublicKeySize)
		for i := range key {
			key[i] = byte(sess.Generation) + n + byte(i)
		}
		if err := f.s.RotateAgentSessionKey(sessionID, key); err != nil {
			t.Fatal(err)
		}
		sess = f.s.state.AgentSessions[sessionID]
	}
}

func confirmVisibility(t *testing.T, f *ownerCommandFixture, delID, msgID string, succ ownercredential.DelegationPrincipalPayload, now time.Time) {
	t.Helper()
	payload, _ := json.Marshal(ownercredential.VisibilityConfirmPayload{
		DelegationID: delID, Successor: &succ,
		IssuedAt: now, ExpiresAt: now.Add(time.Hour), SingleUse: true,
	})
	rev := f.s.state.OwnerCredentials["owner-1"].CredentialRevision
	cmd := f.command(ownercredential.CommandVisibilityConfirm, msgID, "owner-primary", rev, payload)
	sign := f.s.state.OwnerCredentials["owner-1"].Credentials["owner-primary"].SignCount + 1
	if _, _, err := f.s.ApplyOwnerCredentialCommand(cmd.ChallengeID, cmd, assertionFor(cmd, f.private["owner-primary"], sign)); err != nil {
		t.Fatal(err)
	}
}

func confirmPrimaryRestartVisibility(t *testing.T, f *ownerCommandFixture, delID, msgID, stableAgentID string, now time.Time) {
	t.Helper()
	payload, _ := json.Marshal(ownercredential.VisibilityConfirmPayload{
		DelegationID: delID, StableAgentID: stableAgentID,
		IssuedAt: now, ExpiresAt: now.Add(time.Hour), SingleUse: true,
	})
	rev := f.s.state.OwnerCredentials["owner-1"].CredentialRevision
	cmd := f.command(ownercredential.CommandVisibilityConfirm, msgID, "owner-primary", rev, payload)
	sign := f.s.state.OwnerCredentials["owner-1"].Credentials["owner-primary"].SignCount + 1
	if _, _, err := f.s.ApplyOwnerCredentialCommand(cmd.ChallengeID, cmd, assertionFor(cmd, f.private["owner-primary"], sign)); err != nil {
		t.Fatal(err)
	}
}

func lossConfirm(t *testing.T, f *ownerCommandFixture, delID, msgID string, coord ownercredential.DelegationPrincipalPayload, now time.Time) {
	t.Helper()
	assignmentIDs := openDelegatedAssignmentIDs(f, delID, coord)
	payload, _ := json.Marshal(ownercredential.LossConfirmPayload{
		DelegationID: delID, Coordinator: coord, AssignmentIDs: assignmentIDs, Reason: "lost", ObservedAt: now,
	})
	rev := f.s.state.OwnerCredentials["owner-1"].CredentialRevision
	cmd := f.command(ownercredential.CommandLossConfirm, msgID, "owner-primary", rev, payload)
	sign := f.s.state.OwnerCredentials["owner-1"].Credentials["owner-primary"].SignCount + 1
	if _, _, err := f.s.ApplyOwnerCredentialCommand(cmd.ChallengeID, cmd, assertionFor(cmd, f.private["owner-primary"], sign)); err != nil {
		t.Fatal(err)
	}
}

func openDelegatedAssignmentIDs(f *ownerCommandFixture, delID string, coord ownercredential.DelegationPrincipalPayload) []string {
	ids := make([]string, 0)
	if f.s.state.AssignmentLedger != nil {
		for id, record := range f.s.state.AssignmentLedger.Records {
			binding := record.DelegationBinding
			principal := record.Coordinator()
			if !record.State.Terminal() && !record.CoordinatorLost && binding != nil && binding.GrantID == delID && principal.AgentID == coord.StableAgentID && principal.SessionID == coord.SessionID && principal.Generation == coord.Generation {
				ids = append(ids, id)
			}
		}
	}
	sort.Strings(ids)
	return ids
}
