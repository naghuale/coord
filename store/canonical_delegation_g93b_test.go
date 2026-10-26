package store

import (
	"bytes"
	"coord/assignment"
	"coord/ownercredential"
	"coord/task"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// G93b-1: at const=false, CoordinatorLost without an open AGENT_SESSION observer
// must reject handoff exactly as on main (!open => reject).
func TestG93b1_ConstFalseHandoffWithoutOpenObserverRejected(t *testing.T) {
	if CoordinatorDelegationEnabled {
		t.Fatal("production const must be false")
	}
	if coordinatorDelegationActive() {
		t.Fatal("harness must be off for this parity pin")
	}
	f := newLedgerFixture(t)
	f.activate(t)
	next := assignment.Principal{SessionID: "claude:coord-2", AgentID: "coordinator", Generation: 1}
	if err := f.s.RegisterAgentSession(next.SessionID, next.AgentID, ed25519.PublicKey(strings.Repeat("c", 32))); err != nil {
		t.Fatal(err)
	}

	// Force CoordinatorLost without a COORDINATOR_LOST event so
	// OpenCoordinatorLossObserver reports !open (main reject path).
	f.s.mu.Lock()
	r := f.s.state.AssignmentLedger.Records[f.id]
	r.CoordinatorLost = true
	f.s.state.AssignmentLedger.Records[f.id] = r
	f.s.mu.Unlock()

	before, err := f.s.CanonicalSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if _, open := before.AssignmentLedger.OpenCoordinatorLossObserver(f.id); open {
		t.Fatal("precondition: observer interval must be closed/absent")
	}
	if err := f.s.HandoffAssignment(f.coord, next, f.id, f.record(t).Revision, "g93b1-should-reject"); !errors.Is(err, assignment.ErrInvalid) {
		t.Fatalf("const=false !open handoff want ErrInvalid, got %v", err)
	}
	after, err := f.s.CanonicalSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if after.Revision != before.Revision {
		t.Fatal("rejected handoff mutated revision")
	}
}

// G93b-1: const=false even with a forged-looking OWNER_CREDENTIAL receipt in
// state must still reject !open handoff (main behaviour; gate requires active).
func TestG93b1_ConstFalseRejectsEvenWithOwnerReceiptPresent(t *testing.T) {
	if CoordinatorDelegationEnabled || coordinatorDelegationActive() {
		t.Fatal("const/harness must be off")
	}
	f := newLedgerFixture(t)
	f.activate(t)
	next := assignment.Principal{SessionID: "claude:coord-2", AgentID: "coordinator", Generation: 1}
	if err := f.s.RegisterAgentSession(next.SessionID, next.AgentID, ed25519.PublicKey(strings.Repeat("c", 32))); err != nil {
		t.Fatal(err)
	}
	// Mark lost without open AGENT_SESSION observer.
	f.s.mu.Lock()
	r := f.s.state.AssignmentLedger.Records[f.id]
	r.CoordinatorLost = true
	f.s.state.AssignmentLedger.Records[f.id] = r
	f.s.mu.Unlock()
	if ownerCredentialLossAllowsHandoffWithoutObserver(f.s.state, f.id) {
		t.Fatal("const=false must not allow OWNER_CREDENTIAL !open relaxation")
	}
	if err := f.s.HandoffAssignment(f.coord, next, f.id, f.record(t).Revision, "const-false-with-receipt-shape"); !errors.Is(err, assignment.ErrInvalid) {
		t.Fatalf("want ErrInvalid, got %v", err)
	}
}

// G93b-1 harness: OWNER_CREDENTIAL loss has !open observer by design; with
// delegation active and a verified same-commit receipt, handoff still works.
func TestG93b1_HarnessOwnerCredentialHandoffWithoutObserverSucceeds(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	grantDelegationWith(t, f, "del-g93b1", now, delegationOpts{
		successors: []ownercredential.DelegationPrincipalPayload{
			{StableAgentID: "agent-b", SessionID: "codex:b", Generation: 1},
		},
		actions: []string{"OFFER", "HANDOFF", "ACTIVATE", "ACCEPT_RESULT"},
	})
	seedDelegatedAssignment(t, f, "asg-g93b1", assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}, now)
	confirmVisibility(t, f, "del-g93b1", "msg-vis-g93b1", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-b", SessionID: "codex:b", Generation: 1}, now)
	registerSession(t, f, "codex:b", "agent-b")
	lossConfirm(t, f, "del-g93b1", "msg-loss-g93b1", ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1}, now)

	snap, _ := f.s.CanonicalSnapshot()
	if _, open := snap.AssignmentLedger.OpenCoordinatorLossObserver("asg-g93b1"); open {
		t.Fatal("OWNER_CREDENTIAL loss must not open an AGENT_SESSION observer interval")
	}
	if !ownerCredentialLossAllowsHandoffWithoutObserver(snap, "asg-g93b1") {
		t.Fatal("verified OWNER_CREDENTIAL receipt must authorize !open handoff under harness")
	}
	prev := assignment.Principal{SessionID: "claude:a", AgentID: "agent-a", Generation: 1}
	next := assignment.Principal{SessionID: "codex:b", AgentID: "agent-b", Generation: 1}
	rev := f.s.state.AssignmentLedger.Records["asg-g93b1"].Revision
	if err := f.s.HandoffAssignment(prev, next, "asg-g93b1", rev, "g93b1-owner-handoff"); err != nil {
		t.Fatal(err)
	}
}

// G93b-1 harness: CoordinatorLost without a verified OWNER_CREDENTIAL receipt
// (unconfirmed / AGENT_SESSION-shaped) stays rejected.
func TestG93b1_HarnessUnconfirmedLossHandoffRejected(t *testing.T) {
	enableDelegationHarness(t)
	f := newLedgerFixture(t)
	f.activate(t)
	next := assignment.Principal{SessionID: "claude:coord-2", AgentID: "coordinator", Generation: 1}
	if err := f.s.RegisterAgentSession(next.SessionID, next.AgentID, ed25519.PublicKey(strings.Repeat("c", 32))); err != nil {
		t.Fatal(err)
	}
	f.s.mu.Lock()
	r := f.s.state.AssignmentLedger.Records[f.id]
	r.CoordinatorLost = true
	f.s.state.AssignmentLedger.Records[f.id] = r
	f.s.mu.Unlock()
	if ownerCredentialLossAllowsHandoffWithoutObserver(f.s.state, f.id) {
		t.Fatal("unconfirmed loss must not authorize")
	}
	if err := f.s.HandoffAssignment(f.coord, next, f.id, f.record(t).Revision, "unconfirmed"); !errors.Is(err, assignment.ErrInvalid) {
		t.Fatalf("want ErrInvalid, got %v", err)
	}
}

// G93b-1: Target exclusion never substitutes for the open-observer requirement
// in the old (const=false / unconfirmed) mode — !open still rejects even when
// next != Target.
func TestG93b1_TargetExclusionDoesNotWaiveObserverRequirement(t *testing.T) {
	if coordinatorDelegationActive() {
		t.Fatal("harness must be off")
	}
	f := newLedgerFixture(t)
	f.activate(t)
	next := assignment.Principal{SessionID: "claude:coord-2", AgentID: "coordinator", Generation: 1}
	if err := f.s.RegisterAgentSession(next.SessionID, next.AgentID, ed25519.PublicKey(strings.Repeat("c", 32))); err != nil {
		t.Fatal(err)
	}
	f.s.mu.Lock()
	r := f.s.state.AssignmentLedger.Records[f.id]
	r.CoordinatorLost = true
	f.s.state.AssignmentLedger.Records[f.id] = r
	f.s.mu.Unlock()
	if next == f.agent {
		t.Fatal("precondition: next must differ from Target")
	}
	if err := f.s.HandoffAssignment(f.coord, next, f.id, f.record(t).Revision, "not-target-still-reject"); !errors.Is(err, assignment.ErrInvalid) {
		t.Fatalf("!open must reject even when next!=Target, got %v", err)
	}
}

// G93b-2: behavioural parity suite at const=false (design condition 2).
func TestG93b2_ConstFalseParitySuite(t *testing.T) {
	if CoordinatorDelegationEnabled || coordinatorDelegationActive() {
		t.Fatal("parity suite requires const=false and harness off")
	}

	t.Run("owner_offer_activate_accept", func(t *testing.T) {
		f := newLedgerFixture(t)
		f.activate(t)
		if err := f.s.AcceptAssignmentResult(f.coord, f.agent, f.result(t)); err != nil {
			t.Fatal(err)
		}
		if f.record(t).State != assignment.Completed {
			t.Fatal("accept did not complete")
		}
	})

	t.Run("owner_void_expire_breadth", func(t *testing.T) {
		f := newLedgerFixture(t)
		if err := f.s.ChangeAssignment(f.coord, f.id, f.record(t).Revision, "VOID", "owner-void"); err != nil {
			t.Fatal(err)
		}
		f2 := newLedgerFixture(t)
		f2.activate(t)
		before := f2.record(t).Revision
		if err := f2.s.ChangeAssignment(f2.coord, f2.id, before, "EXPIRE", "early-expire"); err == nil {
			t.Fatal("EXPIRE before ValidUntil accepted")
		}
		f2.now = f2.record(t).ValidUntil
		if err := f2.s.ChangeAssignment(f2.coord, f2.id, f2.record(t).Revision, "EXPIRE", "due-expire"); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("path_a_observe_and_handoff_exclusions", func(t *testing.T) {
		f := newLedgerFixture(t)
		f.activate(t)
		observer := registerAssignmentLossObserver(t, f)
		if err := f.s.ObserveAssignmentCoordinatorLoss(observer, f.coord, "parity-loss"); err != nil {
			t.Fatal(err)
		}
		if err := f.s.HandoffAssignment(f.coord, f.agent, f.id, f.record(t).Revision, "to-target"); err == nil {
			t.Fatal("handoff to target accepted")
		}
		if err := f.s.HandoffAssignment(f.coord, observer, f.id, f.record(t).Revision, "to-observer"); err == nil {
			t.Fatal("handoff to observer accepted")
		}
		next := assignment.Principal{SessionID: "claude:coord-2", AgentID: "coordinator", Generation: 1}
		if err := f.s.RegisterAgentSession(next.SessionID, next.AgentID, ed25519.PublicKey(strings.Repeat("c", 32))); err != nil {
			t.Fatal(err)
		}
		if err := f.s.HandoffAssignment(f.coord, next, f.id, f.record(t).Revision, "parity-handoff"); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("delegation_kinds_rejected_agent_session_register_ok", func(t *testing.T) {
		f := newOwnerCommandFixture(t)
		before := ownerStateBytes(t, f.s)
		now := f.s.now().UTC()
		payload, err := json.Marshal(ownercredential.DelegationGrantPayload{
			DelegationID: "del-parity",
			Scope: ownercredential.DelegationScopePayload{
				Repository: "naghuale/coord", Project: "coord",
				AllowedWorkItems: []string{"item-1"}, AllowedActions: []string{"OFFER"},
			},
			PrimaryCoordinator: ownercredential.DelegationPrincipalPayload{StableAgentID: "agent-a", SessionID: "claude:a", Generation: 1},
			Validity:           ownercredential.DelegationValidityPayload{NotBefore: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour)},
			Successors: []ownercredential.DelegationPrincipalPayload{
				{StableAgentID: "agent-b", SessionID: "codex:b", Generation: 1},
			},
			Evidence: ownercredential.DelegationEvidencePayload{OwnerDecisionRef: "ADR-0010", VisibilityEvidence: "owner-seen"},
		})
		if err != nil {
			t.Fatal(err)
		}
		cmd := f.command(ownercredential.CommandDelegationGrant, "msg-parity-grant", "owner-primary", 1, payload)
		assertOwnerCommandRejectedWithoutWrite(t, f, cmd, assertionFor(cmd, f.private["owner-primary"], 2), ErrCoordinatorDelegationDisabled)
		if !bytes.Equal(before, ownerStateBytes(t, f.s)) {
			t.Fatal("delegation grant mutated state at const=false")
		}
		p := assignment.Principal{SessionID: "parity:sess", AgentID: "parity-agent", Generation: 1}
		if err := f.s.RegisterAgentSession(p.SessionID, p.AgentID, ed25519.PublicKey(strings.Repeat("p", 32))); err != nil {
			t.Fatal(err)
		}
	})

	// F7: full #93a/#90 parity dimensions at const=false — register/revoke,
	// task.create, receipts, revision/sequence, and no delegation records.
	t.Run("owner_register_revoke_task_create_receipts_no_delegation", func(t *testing.T) {
		f := newOwnerCommandFixture(t)
		beforeRev := f.s.state.OwnerCredentials["owner-1"].CredentialRevision
		beforeSeq := f.s.state.OwnerCredentials["owner-1"].CommandSequence
		beforeCanonRev := f.s.state.Revision

		pub, _, err := ed25519.GenerateKey(nil)
		if err != nil {
			t.Fatal(err)
		}
		regPayload := registerPayload(t, "owner-parity-third", edDSACOSE(pub))
		reg := f.command(ownercredential.CommandRegisterCredential, "parity-register", "owner-primary", beforeRev, regPayload)
		regReceipt, replayed, err := f.s.ApplyOwnerCredentialCommand(reg.ChallengeID, reg, assertionFor(reg, f.private["owner-primary"], 1))
		if err != nil || replayed || regReceipt.MessageID != reg.MessageID {
			t.Fatalf("register err=%v replayed=%v", err, replayed)
		}
		agg := f.s.state.OwnerCredentials["owner-1"]
		if agg.CredentialRevision != beforeRev+1 || agg.CommandSequence != beforeSeq+1 {
			t.Fatalf("register revision/sequence=%d/%d want %d/%d", agg.CredentialRevision, agg.CommandSequence, beforeRev+1, beforeSeq+1)
		}
		if _, ok := agg.Receipts[reg.MessageID]; !ok {
			t.Fatal("register receipt missing")
		}
		if f.s.state.CoordinatorDelegations != nil {
			t.Fatal("register introduced CoordinatorDelegations")
		}

		revokePayload, _ := json.Marshal(ownercredential.RevokeCredentialPayload{CredentialID: "owner-parity-third"})
		rev := f.command(ownercredential.CommandRevokeCredential, "parity-revoke", "owner-primary", agg.CredentialRevision, revokePayload)
		sign := agg.Credentials["owner-primary"].SignCount + 1
		revReceipt, replayed, err := f.s.ApplyOwnerCredentialCommand(rev.ChallengeID, rev, assertionFor(rev, f.private["owner-primary"], sign))
		if err != nil || replayed || revReceipt.MessageID != rev.MessageID {
			t.Fatalf("revoke err=%v replayed=%v", err, replayed)
		}
		agg = f.s.state.OwnerCredentials["owner-1"]
		if agg.Credentials["owner-parity-third"].RevokedAt.IsZero() {
			t.Fatal("credential not revoked")
		}
		if _, ok := agg.Receipts[rev.MessageID]; !ok {
			t.Fatal("revoke receipt missing")
		}

		taskPayload, _ := json.Marshal(ownercredential.CreateTaskPayload{
			Title: "parity-task", StagePlan: task.StagePlan{Stages: []task.Stage{task.Execution}, PolicyVersion: task.StagePlanPolicyVersion},
		})
		create := f.command(ownercredential.CommandCreateTask, "parity-create-task", "owner-primary", agg.CredentialRevision, taskPayload)
		sign = agg.Credentials["owner-primary"].SignCount + 1
		createReceipt, replayed, err := f.s.ApplyOwnerCredentialCommand(create.ChallengeID, create, assertionFor(create, f.private["owner-primary"], sign))
		if err != nil || replayed || createReceipt.TaskID == "" {
			t.Fatalf("create task err=%v replayed=%v receipt=%+v", err, replayed, createReceipt)
		}
		if f.s.state.Revision <= beforeCanonRev {
			t.Fatal("canonical revision did not advance")
		}
		if f.s.state.CoordinatorDelegations != nil {
			t.Fatal("task create introduced CoordinatorDelegations")
		}
		if len(f.s.state.OwnerCredentials["owner-1"].Receipts) < 3 {
			t.Fatalf("expected register+revoke+create receipts, got %d", len(f.s.state.OwnerCredentials["owner-1"].Receipts))
		}

		// Exact retry of create returns prior receipt, no second effect.
		saves := f.p.saveCalls
		retry, replayed, err := f.s.ApplyOwnerCredentialCommand(create.ChallengeID, create, assertionFor(create, f.private["owner-primary"], sign))
		if err != nil || !replayed || retry.MessageID != createReceipt.MessageID || f.p.saveCalls != saves {
			t.Fatalf("create retry err=%v replayed=%v saves=%d", err, replayed, f.p.saveCalls-saves)
		}
	})
}
