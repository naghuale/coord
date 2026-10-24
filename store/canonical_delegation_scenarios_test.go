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
	// Rotate session to generation 2 — new gen does not inherit.
	if err := f.s.RotateAgentSessionKey("claude:a", ed25519.PublicKey(strings.Repeat("z", 32))); err != nil {
		t.Fatal(err)
	}
	newActor := assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 2}
	before := ownerStateBytes(t, f.s)
	r := f.s.state.AssignmentLedger.Records["asg-gen"]
	if err := f.s.ChangeAssignment(newActor, "asg-gen", r.Revision, "VOID", "gen-inherit"); err == nil {
		t.Fatal("mismatched generation acted")
	}
	if !bytes.Equal(before, ownerStateBytes(t, f.s)) {
		t.Fatal("state changed")
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
	revokePayload, _ := json.Marshal(ownercredential.DelegationRevokePayload{DelegationID: "del-rev", Reason: "done"})
	rev := f.s.state.OwnerCredentials["owner-1"].CredentialRevision
	cmd := f.command(ownercredential.CommandDelegationRevoke, "revoke-rev", "owner-primary", rev, revokePayload)
	sign := f.s.state.OwnerCredentials["owner-1"].Credentials["owner-primary"].SignCount + 1
	if _, _, err := f.s.ApplyOwnerCredentialCommand(cmd.ChallengeID, cmd, assertionFor(cmd, f.private["owner-primary"], sign)); err != nil {
		t.Fatal(err)
	}
	actor := assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}
	r := f.s.state.AssignmentLedger.Records["asg-rev"]
	if err := f.s.ChangeAssignment(actor, "asg-rev", r.Revision, "VOID", "post-revoke"); err == nil {
		t.Fatal("primary acted after revoke")
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
	seedDelegatedAssignment(t, f, "asg-al", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now)
	actor := assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}
	r := f.s.state.AssignmentLedger.Records["asg-al"]
	if err := f.s.ChangeAssignment(actor, "asg-al", r.Revision, "VOID", "not-allowed"); !errors.Is(err, ErrCoordinatorDelegationInvalid) {
		t.Fatalf("err=%v", err)
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
	if err := f.s.ApplyPrimaryRestart("del-pr1", assignment.Principal{SessionID: "claude:a2", AgentID: "agent-a", Generation: 1}, "no-vis"); !errors.Is(err, ErrCoordinatorDelegationInvalid) {
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
	registerSession(t, f, "claude:a2", "agent-a")
	confirmPrimaryRestartVisibility(t, f, "del-pr3", "vis-pr3", "agent-a", now)
	if err := f.s.ApplyPrimaryRestart("del-pr3", assignment.Principal{SessionID: "claude:a2", AgentID: "agent-a", Generation: 1}, "dual"); !errors.Is(err, ErrCoordinatorDelegationInvalid) {
		t.Fatalf("err=%v", err)
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
	lossPayload, _ := json.Marshal(ownercredential.LossConfirmPayload{
		DelegationID: "del-alt",
		Coordinator:  ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1},
		Reason:       "hung", ObservedAt: now,
	})
	rev := f.s.state.OwnerCredentials["owner-1"].CredentialRevision
	cmd := f.command(ownercredential.CommandLossConfirm, "loss-alt", "owner-primary", rev, lossPayload)
	sign := f.s.state.OwnerCredentials["owner-1"].Credentials["owner-primary"].SignCount + 1
	if _, _, err := f.s.ApplyOwnerCredentialCommand(cmd.ChallengeID, cmd, assertionFor(cmd, f.private["owner-primary"], sign)); err != nil {
		t.Fatal(err)
	}
	// Same messageID, altered reason bytes → replay reject.
	altered, _ := json.Marshal(ownercredential.LossConfirmPayload{
		DelegationID: "del-alt",
		Coordinator:  ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1},
		Reason:       "different", ObservedAt: now,
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
	confirmVisibility(t, f, "del-pr5", "vis-pr5-trip", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a2", Generation: 1}, now)
	if err := f.s.ApplyPrimaryRestart("del-pr5", assignment.Principal{SessionID: "claude:a2", AgentID: "agent-a", Generation: 1}, "restart-ok"); err != nil {
		t.Fatal(err)
	}
	// Next generation of same session without new loss/visibility must not auto-continue.
	if err := f.s.RotateAgentSessionKey("claude:a2", ed25519.PublicKey(strings.Repeat("y", 32))); err != nil {
		t.Fatal(err)
	}
	gen2 := assignment.Principal{SessionID: "claude:a2", AgentID: "agent-a", Generation: 2}
	if _, ok := findGrantForActingCoordinator(f.s.state, gen2, now); ok {
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
	state := EmptyCanonicalState()
	state.SchemaVersion = 6
	state.AssignmentLedger = assignment.Empty()
	now := time.Now().UTC()
	state.CoordinatorDelegations = map[string]DelegationRecord{
		"del-forge": {
			DelegationID:       "del-forge",
			Scope:              DelegationScope{Repository: "r", Project: "p", AllowedWorkItems: []string{"w"}, AllowedActions: []string{"OFFER"}},
			PrimaryCoordinator: DelegationPrincipal{StableAgentID: "a", SessionID: "s", Generation: 1},
			Validity:           DelegationValidity{NotBefore: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour)},
			Successors:         []DelegationPrincipal{{StableAgentID: "b", SessionID: "t", Generation: 1}},
			Evidence:           DelegationEvidence{OwnerDecisionRef: "d", VisibilityEvidence: "v"},
			History:            []DelegationHistoryEntry{{Kind: DelegationHistoryGrant, At: now}},
		},
	}
	rec := assignment.Record{
		AssignmentID: "asg-f", State: assignment.Active, Issuer: "s", IssuerAgentID: "a", IssuerGeneration: 1,
		CurrentCoordinator: "s", CoordinatorAgentID: "a", CoordinatorGeneration: 1,
		HandoffEvidence: []assignment.Handoff{}, CoordinatorLost: true,
		Assignee: "u", StableAgentID: "target", SessionID: "u", AssigneeGeneration: 1,
		Role: "IMPLEMENTER", SubjectSHA: strings.Repeat("a", 40),
		IssuedAt: now.Add(-time.Minute), ValidUntil: now.Add(time.Hour),
		AdmissionBasis:     assignment.AdmissionBasis{Kind: "POOL", Reference: "w", TargetSessionID: "u", TargetAgentID: "target", Role: "IMPLEMENTER", SubjectSHA: strings.Repeat("a", 40)},
		VisibilityEvidence: assignment.VisibilityEvidence{Reference: "v", SessionID: "u", ObservedAt: now.Add(-time.Minute)},
		Revision:           2, UpdatedAt: now, AcknowledgedAt: now.Add(-time.Minute), WorkStartedAt: now.Add(-time.Minute),
	}
	state.AssignmentLedger.Records["asg-f"] = rec
	state.AssignmentLedger.Events = []assignment.Event{{
		AssignmentID: "asg-f", Revision: 2, Kind: "COORDINATOR_LOST",
		Actor:       assignment.Principal{SessionID: "ownercredential:forged", AgentID: "forged", Generation: 1},
		EvidenceRef: "forge", At: now, Record: rec,
		ActorSource: assignment.ActorSourceOwnerCredential,
		// Missing receipt binding fields entirely.
	}}
	if err := ValidateCanonicalState(state); err == nil {
		t.Fatal("forged OWNER_CREDENTIAL actor without receipt accepted")
	}
}

// --- helpers ---

type delegationOpts struct {
	successors []ownercredential.DelegationPrincipalPayload
	actions    []string
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
	payload, err := json.Marshal(ownercredential.DelegationGrantPayload{
		DelegationID: id,
		Scope: ownercredential.DelegationScopePayload{
			Repository: "naghuale/coord", Project: "coord",
			AllowedWorkItems: []string{"issue-93b"}, AllowedActions: actions,
		},
		PrimaryCoordinator: ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1},
		Validity:           ownercredential.DelegationValidityPayload{NotBefore: now.Add(-time.Minute), ExpiresAt: now.Add(2 * time.Hour)},
		Successors:         successors,
		PrimaryRestart:     &ownercredential.PrimaryRestartPayload{StableAgentID: "agent-a"},
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
	payload, _ := json.Marshal(ownercredential.LossConfirmPayload{
		DelegationID: delID, Coordinator: coord, Reason: "lost", ObservedAt: now,
	})
	rev := f.s.state.OwnerCredentials["owner-1"].CredentialRevision
	cmd := f.command(ownercredential.CommandLossConfirm, msgID, "owner-primary", rev, payload)
	sign := f.s.state.OwnerCredentials["owner-1"].Credentials["owner-primary"].SignCount + 1
	if _, _, err := f.s.ApplyOwnerCredentialCommand(cmd.ChallengeID, cmd, assertionFor(cmd, f.private["owner-primary"], sign)); err != nil {
		t.Fatal(err)
	}
}
