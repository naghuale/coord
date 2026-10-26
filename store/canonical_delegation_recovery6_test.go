package store

import (
	"bytes"
	"coord/assignment"
	"coord/ownercredential"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func recovery6Clone(t *testing.T, state CanonicalState) CanonicalState {
	t.Helper()
	copy, err := copyCanonicalState(state)
	if err != nil {
		t.Fatal(err)
	}
	return copy
}

func recovery6Reload(t *testing.T, state CanonicalState, reason string) {
	t.Helper()
	before, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	check := func(err error) {
		t.Helper()
		if reason == "" {
			if err != nil {
				t.Fatalf("positive reload: %v", err)
			}
		} else if !errors.Is(err, ErrCoordinatorDelegationInvalid) || !strings.Contains(err.Error(), reason) {
			t.Fatalf("reload error = %v, want delegation rejection containing %q", err, reason)
		}
	}
	check(ValidateCanonicalState(state))
	p := &canonicalMemory{state: state}
	s, err := NewCanonical(p)
	check(err)
	if s != nil {
		_ = s.Close()
	}
	after, err := json.Marshal(p.state)
	if err != nil {
		t.Fatal(err)
	}
	if p.saveCalls != 0 || !bytes.Equal(before, after) {
		t.Fatal("reload changed persisted bytes or saved state")
	}
}

// These deltas bypass live selection deliberately. They exercise the typed
// callback's validation boundary, rather than relying on an earlier live error.
func recovery6A2Delta(t *testing.T, grantID string) (*ownerCommandFixture, CanonicalState, CanonicalState) {
	t.Helper()
	f, now, _, _, _ := recovery4Setup(t, grantID, false)
	a, a2, _, _ := recovery5RestartPrimary(t, f, grantID, now)
	before := recovery6Clone(t, f.s.state)
	next := recovery6Clone(t, before)
	forgeRecoverySiblingWithoutReconsuming(t, &next, grantID, "asg-r4-x", a, a2, "vis-r5-primary-restart", now)
	return f, before, next
}

func TestRecovery6_A2ReceiptMustBeRestartVisibilityOnReload(t *testing.T) {
	_, before, good := recovery6A2Delta(t, "del-r6-receipt")
	recovery6Reload(t, good, "")
	bad := recovery6Clone(t, good)
	d := bad.CoordinatorDelegations["del-r6-receipt"]
	d.History[len(d.History)-1].ReceiptDig = "vis-r4-b"
	bad.CoordinatorDelegations[d.DelegationID] = d
	recovery6Reload(t, bad, "pre-restart loss must reuse restart visibility")
	// The consumed restart visibility, its owner receipt, and the original loss
	// are otherwise unchanged. The digest alone distinguishes the two deltas.
	if !bytes.Equal(mustMarshalRecovery6(t, before.OwnerCredentials), mustMarshalRecovery6(t, bad.OwnerCredentials)) {
		t.Fatal("negative fixture changed OwnerCredential receipts")
	}
}

func TestRecovery6_UnlostAssignmentCannotUseSelfHandoffToReachA2(t *testing.T) {
	id := "del-r6-unlost-current-primary"
	f, now, _, _, _ := recovery4Setup(t, id, false)
	a, a2, _, _ := recovery5RestartPrimary(t, f, id, now)
	seedDelegatedAssignment(t, f, "asg-r6-unlost-current-primary", a2, now)
	positive := recovery6Clone(t, f.s.state)
	forgeRecoverySiblingWithoutReconsuming(t, &positive, id, "asg-r4-x", a, a2, "vis-r5-primary-restart", now)
	recovery6Reload(t, positive, "")
	// The predecessor IS the current actor, so the generic non-acting-source
	// check would pass. But with no loss, equality to the acting target forces
	// From == To; the assignment boundary independently forbids that event.
	bad := recovery6Clone(t, f.s.state)
	prior := bad.AssignmentLedger.Records["asg-r6-unlost-current-primary"]
	next := prior
	next.HandoffEvidence = append(append([]assignment.Handoff{}, prior.HandoffEvidence...), assignment.Handoff{From: a2, To: a2, EvidenceRef: "self-handoff", At: now})
	if err := appendAssignment(&bad, a2, prior, next, "HANDOFF", "self-handoff", now); !errors.Is(err, assignment.ErrInvalid) || !strings.Contains(err.Error(), "unconfirmed handoff") {
		t.Fatalf("self-handoff reached the A-2 exception despite assignment validation: %v", err)
	}
	before := ownerStateBytes(t, f.s)
	if err := recovery4Handoff(t, f, a2, a2, "asg-r6-unlost-current-primary", "unlost-self-handoff"); !errors.Is(err, ErrCoordinatorDelegationInvalid) || !strings.Contains(err.Error(), "handoff target outside successor list") {
		t.Fatalf("live missing-loss boundary: %v", err)
	}
	if !bytes.Equal(before, ownerStateBytes(t, f.s)) {
		t.Fatal("missing-loss rejection changed canonical state")
	}
}

func mustMarshalRecovery6(t *testing.T, value any) []byte {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestRecovery6_A2CommitSeamChecksEachBoundary(t *testing.T) {
	tests := []struct {
		name   string
		change func(*CanonicalState, *CanonicalState, string)
	}{
		{"wrong_restart_visibility", func(_, next *CanonicalState, id string) {
			d := next.CoordinatorDelegations[id]
			d.History[len(d.History)-1].ReceiptDig = "vis-r4-b"
			next.CoordinatorDelegations[id] = d
		}},
		{"assignment_not_in_loss_history", func(prev, _ *CanonicalState, id string) {
			d := prev.CoordinatorDelegations[id]
			for i := range d.History {
				if d.History[i].Kind == DelegationHistoryLossRecorded {
					d.History[i].AssignmentIDs = []string{"asg-r4-y"}
				}
			}
			prev.CoordinatorDelegations[id] = d
		}},
		{"assignment_no_longer_lost", func(prev, _ *CanonicalState, _ string) {
			r := prev.AssignmentLedger.Records["asg-r4-x"]
			r.CoordinatorLost = false
			prev.AssignmentLedger.Records[r.AssignmentID] = r
		}},
		{"missing_owner_loss_receipt", func(prev, _ *CanonicalState, _ string) {
			a := prev.OwnerCredentials["owner-1"]
			delete(a.Receipts, "loss-r4-a")
			prev.OwnerCredentials["owner-1"] = a
		}},
		{"restarted_primary_barred", func(prev, _ *CanonicalState, id string) {
			d := prev.CoordinatorDelegations[id]
			d.BarredCoordinators = append(d.BarredCoordinators, d.CurrentActing())
			prev.CoordinatorDelegations[id] = d
		}},
		{"target_not_primary_agent", func(prev, _ *CanonicalState, id string) {
			d := prev.CoordinatorDelegations[id]
			d.PrimaryCoordinator.StableAgentID = "a-different-primary-agent"
			prev.CoordinatorDelegations[id] = d
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id := "del-r6-seam-" + tt.name
			_, before, next := recovery6A2Delta(t, id)
			if err := validateHandoffGrantDelta(before, next, before.CoordinatorDelegations[id], next.CoordinatorDelegations[id]); err != nil {
				t.Fatalf("exact restart binding positive control: %v", err)
			}
			tt.change(&before, &next, id)
			bytesBefore, bytesNext := mustMarshalRecovery6(t, before), mustMarshalRecovery6(t, next)
			err := validateHandoffGrantDelta(before, next, before.CoordinatorDelegations[id], next.CoordinatorDelegations[id])
			want := "handoff target outside successor list"
			if tt.name == "wrong_restart_visibility" {
				want = "pre-restart loss must reuse restart visibility"
			}
			if !errors.Is(err, ErrCoordinatorDelegationInvalid) || !strings.Contains(err.Error(), want) {
				t.Fatalf("seam accepted a delta outside the A-2 exception: %v", err)
			}
			if !bytes.Equal(bytesBefore, mustMarshalRecovery6(t, before)) || !bytes.Equal(bytesNext, mustMarshalRecovery6(t, next)) {
				t.Fatal("seam validation changed its inputs")
			}
		})
	}
}

func TestRecovery6_A2CallbackCannotBypassSeam(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind delegationCommitKind
	}{{"generic_writer", delegationCommitGeneric}, {"typed_handoff_callback", delegationCommitHandoff}} {
		t.Run(tc.name, func(t *testing.T) {
			kind := tc.kind
			id := "del-r6-callback-" + tc.name
			f, _, next := recovery6A2Delta(t, id)
			d := next.CoordinatorDelegations[id]
			d.History[len(d.History)-1].ReceiptDig = "vis-r4-b"
			next.CoordinatorDelegations[id] = d
			before := ownerStateBytes(t, f.s)
			p := f.s.p.(*canonicalMemory)
			saves := p.saveCalls
			callback := func(state *CanonicalState) error {
				*state = recovery6Clone(t, next)
				return nil
			}
			var err error
			if kind == delegationCommitGeneric {
				err = f.s.UpdateCanonical(callback)
			} else {
				err = f.s.commitCanonical(kind, callback)
			}
			want := "pre-restart loss must reuse restart visibility"
			if kind == delegationCommitGeneric {
				want = "grant authority is immutable via generic writer"
			}
			if !errors.Is(err, ErrCoordinatorDelegationInvalid) || !strings.Contains(err.Error(), want) {
				t.Fatalf("callback rejection = %v, want %q", err, want)
			}
			if p.saveCalls != saves || !bytes.Equal(before, ownerStateBytes(t, f.s)) {
				t.Fatal("rejected callback wrote state, events or receipts")
			}
			if kind == delegationCommitHandoff {
				good := recovery6Clone(t, next)
				grant := good.CoordinatorDelegations[id]
				grant.History[len(grant.History)-1].ReceiptDig = "vis-r5-primary-restart"
				good.CoordinatorDelegations[id] = grant
				if err := f.s.commitCanonical(kind, func(state *CanonicalState) error { *state = good; return nil }); err != nil {
					t.Fatalf("exact-binding callback positive control: %v", err)
				}
			}
		})
	}
}

func TestRecovery6_CurrentPrimaryRestartEndsWithNewAuthority(t *testing.T) {
	_, state, _ := recovery6A2Delta(t, "del-r6-context")
	d := state.CoordinatorDelegations["del-r6-context"]
	restart, ok := currentPrimaryRestart(d)
	if !ok || restart.To == nil || !restart.To.Equal(d.CurrentActing()) {
		t.Fatal("positive control did not identify the active restart")
	}
	for _, name := range []string{"loss_then_new_primary_capture", "planned_handoff_to_another_primary_session", "snapshot_acting_disagrees", "snapshot_primary_is_barred"} {
		t.Run(name, func(t *testing.T) {
			copy := recovery6Clone(t, state).CoordinatorDelegations[d.DelegationID]
			a2 := copy.CurrentActing()
			a3 := a2
			a3.SessionID, a3.Generation = "claude:primary-without-restart", a2.Generation+1
			switch name {
			case "loss_then_new_primary_capture":
				copy.History = append(copy.History, DelegationHistoryEntry{Kind: DelegationHistoryLossRecorded, From: &a2, AssignmentIDs: []string{"new-primary-assignment"}})
				copy.BarredCoordinators = append(copy.BarredCoordinators, a2)
				fallthrough
			case "planned_handoff_to_another_primary_session":
				copy.History = append(copy.History, DelegationHistoryEntry{Kind: DelegationHistoryHandoff, From: &a2, To: &a3, AssignmentID: "new-primary-assignment", ReceiptDig: "another-visibility"})
				copy.ActingCoordinator = &a3
			case "snapshot_acting_disagrees":
				copy.ActingCoordinator = &a3
			case "snapshot_primary_is_barred":
				copy.BarredCoordinators = append(copy.BarredCoordinators, a2)
			}
			if stale, ok := currentPrimaryRestart(copy); ok || !reflect.DeepEqual(stale, DelegationHistoryEntry{}) {
				t.Fatalf("obsolete restart context survived %s: %+v", name, stale)
			}
		})
	}
}

// A primary-agent successor can be lost without ever having been a restart.
// This isolates session guards from the previous-restart-session guard.
func recovery6NonRestartBarredSession(t *testing.T, walkToB bool) (CanonicalState, string) {
	t.Helper()
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	a, b, _ := recovery4Principals()
	ax := assignment.Principal{AgentID: a.AgentID, SessionID: "claude:listed-primary-session", Generation: 2}
	id := "del-r6-nonrestart-session"
	grantDelegationWith(t, f, id, now, delegationOpts{successors: []ownercredential.DelegationPrincipalPayload{
		{StableAgentID: ax.AgentID, SessionID: ax.SessionID, Generation: ax.Generation},
		{StableAgentID: b.AgentID, SessionID: b.SessionID, Generation: b.Generation},
	}})
	seedDelegatedAssignment(t, f, "asg-r6-session", a, now)
	registerSession(t, f, ax.SessionID, ax.AgentID)
	bumpSessionGeneration(t, f, ax.SessionID, ax.Generation)
	confirmVisibility(t, f, id, "vis-r6-listed-primary", ownercredential.DelegationPrincipalPayload{StableAgentID: ax.AgentID, SessionID: ax.SessionID, Generation: ax.Generation}, now)
	lossConfirm(t, f, id, "loss-r6-original-primary", ownercredential.DelegationPrincipalPayload{StableAgentID: a.AgentID, SessionID: a.SessionID, Generation: a.Generation}, now)
	if err := recovery4Handoff(t, f, a, ax, "asg-r6-session", "listed-primary-not-restart"); err != nil {
		t.Fatal(err)
	}
	lossConfirm(t, f, id, "loss-r6-listed-primary", ownercredential.DelegationPrincipalPayload{StableAgentID: ax.AgentID, SessionID: ax.SessionID, Generation: ax.Generation}, now)
	if walkToB {
		registerSession(t, f, b.SessionID, b.AgentID)
		confirmVisibility(t, f, id, "vis-r6-walk-b", ownercredential.DelegationPrincipalPayload{StableAgentID: b.AgentID, SessionID: b.SessionID, Generation: b.Generation}, now)
		if err := recovery4Handoff(t, f, ax, b, "asg-r6-session", "walk-after-listed-primary-loss"); err != nil {
			t.Fatal(err)
		}
		lossConfirm(t, f, id, "loss-r6-b", ownercredential.DelegationPrincipalPayload{StableAgentID: b.AgentID, SessionID: b.SessionID, Generation: b.Generation}, now)
	}
	a3 := assignment.Principal{AgentID: a.AgentID, SessionID: "claude:fresh-r6-primary", Generation: 3}
	registerSession(t, f, a3.SessionID, a3.AgentID)
	bumpSessionGeneration(t, f, a3.SessionID, a3.Generation)
	confirmPrimaryRestartVisibility(t, f, id, "vis-r6-restart", a.AgentID, now)
	if err := f.s.ApplyPrimaryRestart(id, a3, "r6-fresh-primary"); err != nil {
		t.Fatalf("fresh restart positive control: %v", err)
	}
	return recovery6Clone(t, f.s.state), ax.SessionID
}

func TestRecovery6_ReloadRejectsLostOrBarredSessionWithoutPriorRestart(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		walkToB      bool
	}{{"lost_session", "PRIMARY_RESTART reuses lost session", false}, {"earlier_barred_session", "PRIMARY_RESTART reuses a barred session", true}} {
		t.Run(tc.name, func(t *testing.T) {
			state, barredSession := recovery6NonRestartBarredSession(t, tc.walkToB)
			recovery6Reload(t, state, "")
			d := state.CoordinatorDelegations["del-r6-nonrestart-session"]
			last := &d.History[len(d.History)-1]
			if last.Kind != DelegationHistoryPrimaryRestart || last.To == nil {
				t.Fatal("fixture did not end at restart")
			}
			to := *last.To
			to.SessionID = barredSession
			last.To, d.ActingCoordinator = &to, &to
			state.CoordinatorDelegations[d.DelegationID] = d
			recovery6Reload(t, state, tc.reason)
		})
	}
}

func recovery6PrimarySuccessorFixture(t *testing.T, id string) (*ownerCommandFixture, assignment.Principal, assignment.Principal, assignment.Principal) {
	t.Helper()
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	a, b, _ := recovery4Principals()
	a3 := assignment.Principal{AgentID: a.AgentID, SessionID: "claude:listed-after-restart", Generation: 3}
	grantDelegationWith(t, f, id, now, delegationOpts{successors: []ownercredential.DelegationPrincipalPayload{
		{StableAgentID: a3.AgentID, SessionID: a3.SessionID, Generation: a3.Generation},
		{StableAgentID: b.AgentID, SessionID: b.SessionID, Generation: b.Generation},
	}})
	seedDelegatedAssignment(t, f, "asg-r4-x", a, now)
	seedDelegatedAssignment(t, f, "asg-r4-y", a, now)
	lossConfirm(t, f, id, "loss-r4-a", ownercredential.DelegationPrincipalPayload{StableAgentID: a.AgentID, SessionID: a.SessionID, Generation: a.Generation}, now)
	_, a2, _, _ := recovery5RestartPrimary(t, f, id, now)
	registerSession(t, f, a3.SessionID, a3.AgentID)
	bumpSessionGeneration(t, f, a3.SessionID, a3.Generation)
	confirmVisibility(t, f, id, "vis-r6-a3-capture", ownercredential.DelegationPrincipalPayload{StableAgentID: a3.AgentID, SessionID: a3.SessionID, Generation: a3.Generation}, now)
	return f, a, a2, a3
}

func TestRecovery6_ReloadRestartContextEndsAtPlannedOrLossCapture(t *testing.T) {
	for _, mode := range []string{"planned_handoff", "capture_after_primary_loss"} {
		t.Run(mode, func(t *testing.T) {
			id := "del-r6-context-" + mode
			f, a, a2, a3 := recovery6PrimarySuccessorFixture(t, id)
			now := f.s.now().UTC()
			seedDelegatedAssignment(t, f, "asg-r6-new-authority", a2, now)
			if mode == "capture_after_primary_loss" {
				lossConfirm(t, f, id, "loss-r6-a2-context", ownercredential.DelegationPrincipalPayload{StableAgentID: a2.AgentID, SessionID: a2.SessionID, Generation: a2.Generation}, now)
			}
			if err := recovery4Handoff(t, f, a2, a3, "asg-r6-new-authority", "establish-new-authority"); err != nil {
				t.Fatalf("fresh-visibility successor positive control: %v", err)
			}
			recovery6Reload(t, recovery6Clone(t, f.s.state), "")
			if _, ok := currentPrimaryRestart(f.s.state.CoordinatorDelegations[id]); ok {
				t.Fatal("new authority inherited an earlier primaryRestart context")
			}
			bad := recovery6Clone(t, f.s.state)
			forgeRecoverySiblingWithoutReconsuming(t, &bad, id, "asg-r4-y", a, a3, "vis-r5-primary-restart", now)
			recovery6Reload(t, bad, "another unbarred coordinator is acting")
		})
	}
}

func TestRecovery6_RevokedGrantCannotRecoverThroughFreshVisibilityOnReload(t *testing.T) {
	id := "del-r6-revoke-fresh-visibility"
	f, a, _, a3 := recovery6PrimarySuccessorFixture(t, id)
	revokeRecoveryR2Grant(t, f, id, "revoke-r6-fresh-visibility")
	recovery6Reload(t, recovery6Clone(t, f.s.state), "")
	before := ownerStateBytes(t, f.s)
	saves := f.s.p.(*canonicalMemory).saveCalls
	if err := recovery4Handoff(t, f, a, a3, "asg-r4-y", "live-after-revoke"); !errors.Is(err, ErrCoordinatorDelegationInvalid) || !strings.Contains(err.Error(), "originating grant "+id+" revoked") {
		t.Fatalf("live revoke boundary: %v", err)
	}
	if !bytes.Equal(before, ownerStateBytes(t, f.s)) || saves != f.s.p.(*canonicalMemory).saveCalls {
		t.Fatal("live rejected handoff changed canonical state or wrote a receipt")
	}
	bad := recovery6Clone(t, f.s.state)
	forgeRecoveryHandoff(t, &bad, id, "asg-r4-y", a, a3, "vis-r6-a3-capture", f.s.now().UTC())
	recovery6Reload(t, bad, "authority transition after revocation")
}

func TestRecovery6_RevokeRejectsAuthorityHistory(t *testing.T) {
	id := "del-r6-terminal-revoke"
	f, _, _, _ := recovery6PrimarySuccessorFixture(t, id)
	revokeRecoveryR2Grant(t, f, id, "revoke-r6-terminal")
	recovery6Reload(t, recovery6Clone(t, f.s.state), "")
	for _, kind := range []DelegationHistoryKind{DelegationHistoryHandoff, DelegationHistoryPrimaryRestart} {
		t.Run(string(kind), func(t *testing.T) {
			state := recovery6Clone(t, f.s.state)
			d := state.CoordinatorDelegations[id]
			d.History = append(d.History, DelegationHistoryEntry{Kind: kind, At: f.s.now().UTC(), CanonicalRevision: state.Revision})
			state.CoordinatorDelegations[id] = d
			recovery6Reload(t, state, "authority transition after revocation")
		})
	}
}

func TestRecovery6_RevokeRejectsPrimaryRestartOnLiveAndReload(t *testing.T) {
	id := "del-r6-revoke-restart"
	f, now, a, _, _ := recovery4Setup(t, id, false)
	a2 := assignment.Principal{AgentID: a.AgentID, SessionID: "claude:revoked-restart", Generation: 2}
	registerSession(t, f, a2.SessionID, a2.AgentID)
	bumpSessionGeneration(t, f, a2.SessionID, a2.Generation)
	confirmPrimaryRestartVisibility(t, f, id, "vis-r6-before-revoke", a.AgentID, now)
	revokeRecoveryR2Grant(t, f, id, "revoke-r6-before-restart")
	recovery6Reload(t, recovery6Clone(t, f.s.state), "")
	before := ownerStateBytes(t, f.s)
	saves := f.s.p.(*canonicalMemory).saveCalls
	if err := f.s.ApplyPrimaryRestart(id, a2, "restart-after-revoke"); !errors.Is(err, ErrCoordinatorDelegationInvalid) || !strings.Contains(err.Error(), "grant missing/expired/revoked") {
		t.Fatalf("live restart-after-revoke: %v", err)
	}
	if !bytes.Equal(before, ownerStateBytes(t, f.s)) || saves != f.s.p.(*canonicalMemory).saveCalls {
		t.Fatal("rejected live restart changed state")
	}
	bad := recovery6Clone(t, f.s.state)
	bad.Revision++
	d := bad.CoordinatorDelegations[id]
	var loss DelegationHistoryEntry
	for _, h := range d.History {
		if h.Kind == DelegationHistoryLossRecorded {
			loss = h
		}
	}
	to := principalFromAssignment(a2)
	d.ActingCoordinator = &to
	d.History = append(d.History, DelegationHistoryEntry{Kind: DelegationHistoryPrimaryRestart, At: now, From: loss.From, To: &to, AssignmentID: loss.AssignmentID, ReceiptID: loss.ReceiptID, ReceiptDig: "vis-r6-before-revoke", Reason: "restart-after-revoke", CanonicalRevision: bad.Revision})
	for i := range d.VisibilityReceipts {
		if d.VisibilityReceipts[i].MessageID == "vis-r6-before-revoke" {
			d.VisibilityReceipts[i].ConsumedAt, d.VisibilityReceipts[i].ConsumedBy = now, "restart-after-revoke"
		}
	}
	bad.CoordinatorDelegations[id] = d
	recovery6Reload(t, bad, "authority transition after revocation")
}

func TestRecovery6_RevokePreservesExactRetryAndNewGrantAfterReload(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	a, _, _ := recovery4Principals()
	grantDelegation(t, f, "del-r6-old", now)
	seedDelegatedAssignment(t, f, "asg-r6-old", a, now)
	payload, err := json.Marshal(ownercredential.DelegationRevokePayload{DelegationID: "del-r6-old", Reason: "replace this exact grant"})
	if err != nil {
		t.Fatal(err)
	}
	command := f.command(ownercredential.CommandDelegationRevoke, "revoke-r6-old", "owner-primary", f.s.state.OwnerCredentials["owner-1"].CredentialRevision, payload)
	assertion := assertionFor(command, f.private["owner-primary"], f.s.state.OwnerCredentials["owner-1"].Credentials["owner-primary"].SignCount+1)
	receipt, _, err := f.s.ApplyOwnerCredentialCommand(command.ChallengeID, command, assertion)
	if err != nil {
		t.Fatal(err)
	}
	before := ownerStateBytes(t, f.s)
	saves := f.s.p.(*canonicalMemory).saveCalls
	retry, replayed, err := f.s.ApplyOwnerCredentialCommand(command.ChallengeID, command, assertion)
	if err != nil || !replayed || !reflect.DeepEqual(receipt, retry) {
		t.Fatalf("exact revoke retry: replay=%v receipt=%+v error=%v", replayed, retry, err)
	}
	if !bytes.Equal(before, ownerStateBytes(t, f.s)) || saves != f.s.p.(*canonicalMemory).saveCalls {
		t.Fatal("exact retry appended state, history, event or receipt")
	}
	recovery6Reload(t, recovery6Clone(t, f.s.state), "")
	grantDelegation(t, f, "del-r6-new", now)
	recovery6Reload(t, recovery6Clone(t, f.s.state), "")
	for _, mode := range []string{"live", "reloaded"} {
		t.Run(mode, func(t *testing.T) {
			s := f.s
			if mode == "reloaded" {
				var err error
				s, err = NewCanonical(&canonicalMemory{state: recovery6Clone(t, f.s.state)})
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = s.Close() }()
				s.now = f.s.now
			}
			old := s.state.AssignmentLedger.Records["asg-r6-old"]
			unchanged := ownerStateBytes(t, s)
			if err := s.ChangeAssignment(a, old.AssignmentID, old.Revision, "VOID", "old-binding-after-regrant"); !errors.Is(err, ErrCoordinatorDelegationExhausted) || !strings.Contains(err.Error(), "originating grant revoked or expired") {
				t.Fatalf("old grant unexpectedly inherited replacement authority: %v", err)
			}
			if !bytes.Equal(unchanged, ownerStateBytes(t, s)) {
				t.Fatal("old-grant rejection changed state")
			}
			offer := old
			offer.AssignmentID = "asg-r6-new-" + mode
			offer.DelegationBinding = nil
			offer.State, offer.Revision = assignment.Offered, 1
			offer.AcknowledgedAt, offer.WorkStartedAt = time.Time{}, time.Time{}
			offer.UpdatedAt, offer.IssuedAt = now.Add(-time.Minute), now.Add(-time.Minute)
			if err := s.OfferAssignment(a, offer, "new-grant-offer"); err != nil {
				t.Fatalf("new exact grant offer: %v", err)
			}
			fresh := s.state.AssignmentLedger.Records[offer.AssignmentID]
			if fresh.DelegationBinding == nil || fresh.DelegationBinding.GrantID != "del-r6-new" {
				t.Fatal("new assignment did not bind exact replacement grant")
			}
			if err := s.ChangeAssignment(a, fresh.AssignmentID, fresh.Revision, "VOID", "new-grant-void"); err != nil {
				t.Fatalf("new grant canonical effect: %v", err)
			}
			recovery6Reload(t, recovery6Clone(t, s.state), "")
		})
	}
}

func TestRecovery6_ReloadCannotReenterBarredListedPrimary(t *testing.T) {
	enableDelegationHarness(t)
	f := newOwnerCommandFixture(t)
	now := f.s.now().UTC()
	a, b, _ := recovery4Principals()
	a2 := assignment.Principal{AgentID: a.AgentID, SessionID: "claude:a2", Generation: 2}
	id := "del-r6-barred-listed-primary"
	grantDelegationWith(t, f, id, now, delegationOpts{successors: []ownercredential.DelegationPrincipalPayload{
		{StableAgentID: a2.AgentID, SessionID: a2.SessionID, Generation: a2.Generation},
		{StableAgentID: b.AgentID, SessionID: b.SessionID, Generation: b.Generation},
	}})
	seedDelegatedAssignment(t, f, "asg-r4-x", a, now)
	seedDelegatedAssignment(t, f, "asg-r4-y", a, now)
	lossConfirm(t, f, id, "loss-r4-a", ownercredential.DelegationPrincipalPayload{StableAgentID: a.AgentID, SessionID: a.SessionID, Generation: a.Generation}, now)
	recovery5RestartPrimary(t, f, id, now)
	confirmVisibility(t, f, id, "vis-r6-listed-a2", ownercredential.DelegationPrincipalPayload{StableAgentID: a2.AgentID, SessionID: a2.SessionID, Generation: a2.Generation}, now)
	if err := recovery4Handoff(t, f, a, a2, "asg-r4-x", "listed-a2-positive"); err != nil {
		t.Fatal(err)
	}
	recovery6Reload(t, recovery6Clone(t, f.s.state), "")
	lossConfirm(t, f, id, "loss-r6-listed-a2", ownercredential.DelegationPrincipalPayload{StableAgentID: a2.AgentID, SessionID: a2.SessionID, Generation: a2.Generation}, now)
	before := ownerStateBytes(t, f.s)
	saves := f.s.p.(*canonicalMemory).saveCalls
	_, skips, selectionErr := SelectDelegationSuccessor(f.s.state, f.s.state.CoordinatorDelegations[id], "asg-r4-y", now)
	if !errors.Is(selectionErr, ErrCoordinatorDelegationExhausted) || len(skips) == 0 || skips[0].To == nil || !skips[0].To.Equal(principalFromAssignment(a2)) || skips[0].Reason != string(SkipBarred) {
		t.Fatalf("live selection did not exclude exact barred A2: skips=%+v error=%v", skips, selectionErr)
	}
	if err := recovery4Handoff(t, f, a, a2, "asg-r4-y", "barred-listed-primary-live"); !errors.Is(err, ErrCoordinatorDelegationExhausted) || !strings.Contains(err.Error(), "no eligible successor") {
		t.Fatalf("live accepted or misclassified barred listed target: %v", err)
	}
	if !bytes.Equal(before, ownerStateBytes(t, f.s)) || saves != f.s.p.(*canonicalMemory).saveCalls {
		t.Fatal("live barred-target rejection changed state")
	}
	bad := recovery6Clone(t, f.s.state)
	forgeRecoveryHandoff(t, &bad, id, "asg-r4-y", a, a2, "vis-r6-listed-a2", now)
	recovery6Reload(t, bad, "HANDOFF target is barred")
	// Barring the old exact triple must not bar a fresh restart of the same
	// stable agent. The new session and generation regain authority only via
	// their own restart and its fresh visibility.
	a3 := assignment.Principal{AgentID: a.AgentID, SessionID: "claude:r6-after-bar", Generation: 3}
	registerSession(t, f, a3.SessionID, a3.AgentID)
	bumpSessionGeneration(t, f, a3.SessionID, a3.Generation)
	confirmPrimaryRestartVisibility(t, f, id, "vis-r6-after-bar", a.AgentID, now)
	if err := f.s.ApplyPrimaryRestart(id, a3, "new-triple-after-bar"); err != nil {
		t.Fatalf("barred old triple blocked new primary restart: %v", err)
	}
	if err := recovery4Handoff(t, f, a, a3, "asg-r4-y", "new-triple-lost-assignment"); err != nil {
		t.Fatalf("unbarred new triple could not receive a covered assignment: %v", err)
	}
	recovery6Reload(t, recovery6Clone(t, f.s.state), "")
}

func TestRecovery6_ReloadRejectsBarredNonPrimarySuccessor(t *testing.T) {
	id := "del-r6-barred-successor"
	f, now, a, b, c := recovery4Setup(t, id, true)
	if err := recovery4Handoff(t, f, a, b, "asg-r4-x", "initial-successor-positive"); err != nil {
		t.Fatal(err)
	}
	recovery6Reload(t, recovery6Clone(t, f.s.state), "")
	confirmVisibility(t, f, id, "vis-r6-b-before-loss", ownercredential.DelegationPrincipalPayload{StableAgentID: b.AgentID, SessionID: b.SessionID, Generation: b.Generation}, now)
	lossConfirm(t, f, id, "loss-r6-b", ownercredential.DelegationPrincipalPayload{StableAgentID: b.AgentID, SessionID: b.SessionID, Generation: b.Generation}, now)
	selected, skips, err := SelectDelegationSuccessor(f.s.state, f.s.state.CoordinatorDelegations[id], "asg-r4-y", now)
	if err != nil || !selected.Equal(principalFromAssignment(c)) || len(skips) == 0 || skips[0].Reason != string(SkipBarred) || skips[0].To == nil || !skips[0].To.Equal(principalFromAssignment(b)) {
		t.Fatalf("selection did not skip barred B for unbarred C: selected=%+v skips=%+v error=%v", selected, skips, err)
	}
	before, saves := ownerStateBytes(t, f.s), f.s.p.(*canonicalMemory).saveCalls
	if err := recovery4Handoff(t, f, a, b, "asg-r4-y", "barred-successor-live"); !errors.Is(err, ErrCoordinatorDelegationInvalid) || !strings.Contains(err.Error(), "must hand off to first eligible successor") {
		t.Fatalf("live barred-successor boundary: %v", err)
	}
	if !bytes.Equal(before, ownerStateBytes(t, f.s)) || saves != f.s.p.(*canonicalMemory).saveCalls {
		t.Fatal("barred successor rejection changed canonical state")
	}
	bad := recovery6Clone(t, f.s.state)
	forgeRecoveryHandoff(t, &bad, id, "asg-r4-y", a, b, "vis-r6-b-before-loss", now)
	recovery6Reload(t, bad, "HANDOFF target is barred")
	if err := recovery4Handoff(t, f, a, c, "asg-r4-y", "unbarred-successor-positive"); err != nil {
		t.Fatalf("unbarred listed successor rejected: %v", err)
	}
	recovery6Reload(t, recovery6Clone(t, f.s.state), "")
}
