package store

import (
	"bytes"
	"coord/assignment"
	"coord/ownercredential"
	"errors"
	"strings"
	"testing"
	"time"
)

func recovery7Fixture(t *testing.T, id string, fresh bool) (*ownerCommandFixture, time.Time, assignment.Principal, assignment.Principal, assignment.Principal) {
	t.Helper()
	f, now, a, b, _ := recovery4Setup(t, id, false)
	for _, name := range []string{"asg-r7-x", "asg-r7-y"} {
		recovery5AddGrantBoundAssignment(t, f, id, name, b, now)
	}
	_, a2, _, _ := recovery5RestartPrimary(t, f, id, now)
	lossConfirm(t, f, id, "loss-r7-post-restart", ownercredential.DelegationPrincipalPayload{StableAgentID: b.AgentID, SessionID: b.SessionID, Generation: b.Generation}, now)
	if fresh {
		confirmVisibility(t, f, id, "vis-r7-fresh", ownercredential.DelegationPrincipalPayload{StableAgentID: a2.AgentID, SessionID: a2.SessionID, Generation: a2.Generation}, now)
	}
	return f, now, a, a2, b
}

func recovery7RejectUnchanged(t *testing.T, f *ownerCommandFixture, call func() error, reason string) {
	t.Helper()
	before, saves := ownerStateBytes(t, f.s), f.s.p.(*canonicalMemory).saveCalls
	if err := call(); !errors.Is(err, ErrCoordinatorDelegationInvalid) || !strings.Contains(err.Error(), reason) {
		t.Fatalf("rejection = %v, want delegation error containing %q", err, reason)
	}
	if !bytes.Equal(before, ownerStateBytes(t, f.s)) || saves != f.s.p.(*canonicalMemory).saveCalls {
		t.Fatal("rejected transition changed canonical bytes or called Save")
	}
}

func TestRecovery7_A3LiveAndReload(t *testing.T) {
	t.Run("pre_restart_loss_without_new_visibility", func(t *testing.T) {
		f, _, a, a2, _ := recovery7Fixture(t, "del-r7-pre", false)
		if err := recovery4Handoff(t, f, a, a2, "asg-r4-x", "pre-restart-loss"); err != nil {
			t.Fatalf("A-2 pre-restart positive control: %v", err)
		}
		d := f.s.state.CoordinatorDelegations["del-r7-pre"]
		if d.History[len(d.History)-1].ReceiptDig != "vis-r5-primary-restart" {
			t.Fatal("pre-restart loss did not retain the restart visibility")
		}
		recovery6Reload(t, recovery6Clone(t, f.s.state), "")
	})
	t.Run("post_restart_loss_without_fresh_visibility", func(t *testing.T) {
		f, _, _, a2, b := recovery7Fixture(t, "del-r7-no-fresh", false)
		copy := recovery6Clone(t, f.s.state)
		before := mustMarshalRecovery6(t, copy)
		if err := enforceDelegatedHandoff(&copy, b, a2, "asg-r7-x", f.s.now().UTC()); !errors.Is(err, ErrCoordinatorDelegationInvalid) || !strings.Contains(err.Error(), "post-restart loss requires fresh visibility") {
			t.Fatalf("isolated live guard reused restart visibility: %v", err)
		}
		if !bytes.Equal(before, mustMarshalRecovery6(t, copy)) {
			t.Fatal("isolated rejected live guard changed state")
		}
		recovery7RejectUnchanged(t, f, func() error {
			return recovery4Handoff(t, f, b, a2, "asg-r7-x", "post-restart-no-fresh")
		}, "post-restart loss requires fresh visibility")
	})
	t.Run("fresh_capture_and_sibling_keep_one_visibility", func(t *testing.T) {
		id := "del-r7-fresh"
		f, _, _, a2, b := recovery7Fixture(t, id, true)
		origin, ok := currentPrimaryRestart(f.s.state.CoordinatorDelegations[id])
		if !ok {
			t.Fatal("positive fixture lacks active restart")
		}
		for _, name := range []string{"asg-r7-x", "asg-r7-y"} {
			if err := recovery4Handoff(t, f, b, a2, name, "post-restart-fresh-capture"); err != nil {
				t.Fatalf("fresh capture/sibling %s: %v", name, err)
			}
			d := f.s.state.CoordinatorDelegations[id]
			h := d.History[len(d.History)-1]
			if h.ReceiptDig != "vis-r7-fresh" || !d.CurrentActing().Equal(principalFromAssignment(a2)) {
				t.Fatalf("fresh/sibling route used the wrong authority or receipt: %+v", h)
			}
			kept, ok := currentPrimaryRestart(d)
			if !ok || kept.ReceiptID != origin.ReceiptID || kept.CanonicalRevision != origin.CanonicalRevision {
				t.Fatal("fresh same-primary capture lost the original restart binding")
			}
			recovery6Reload(t, recovery6Clone(t, f.s.state), "")
		}
		d := f.s.state.CoordinatorDelegations[id]
		for _, vis := range d.VisibilityReceipts {
			if vis.MessageID == "vis-r7-fresh" && (vis.ConsumedAt.IsZero() || vis.ConsumedBy != "asg-r7-x") {
				t.Fatalf("sibling consumed visibility again: %+v", vis)
			}
		}
	})
	t.Run("another_successor_cannot_replace_acting_primary", func(t *testing.T) {
		id := "del-r7-other-successor"
		f, now, _, _, b := recovery7Fixture(t, id, true)
		_, _, c := recovery4Principals()
		confirmVisibility(t, f, id, "vis-r7-other", ownercredential.DelegationPrincipalPayload{StableAgentID: c.AgentID, SessionID: c.SessionID, Generation: c.Generation}, now)
		copy := recovery6Clone(t, f.s.state)
		if err := enforceDelegatedHandoff(&copy, b, c, "asg-r7-x", now); !errors.Is(err, ErrCoordinatorDelegationInvalid) || !strings.Contains(err.Error(), "another unbarred coordinator is acting") {
			t.Fatalf("isolated live guard let another successor replace A2: %v", err)
		}
		recovery7RejectUnchanged(t, f, func() error {
			return recovery4Handoff(t, f, b, c, "asg-r7-x", "post-loss-other-successor")
		}, "another unbarred coordinator is acting")
	})
}

func TestRecovery7_A3ContextSurvivesOlderLossRoutingButNotNewLoss(t *testing.T) {
	id := "del-r7-context-boundary"
	f, now, a, a2, b := recovery7Fixture(t, id, true)
	if err := recovery4Handoff(t, f, b, a2, "asg-r7-x", "first-post-restart-capture"); err != nil {
		t.Fatal(err)
	}
	if err := recovery4Handoff(t, f, a, a2, "asg-r4-x", "interleaved-pre-restart-loss"); err != nil {
		t.Fatal(err)
	}
	if err := recovery4Handoff(t, f, b, a2, "asg-r7-y", "same-post-loss-sibling"); err != nil {
		t.Fatalf("older pre-restart routing destroyed the fresh sibling context: %v", err)
	}
	recovery6Reload(t, recovery6Clone(t, f.s.state), "")
	d := assignment.Principal{AgentID: "agent-r7-d", SessionID: "deepseek:r7-d", Generation: 1}
	recovery5AddGrantBoundAssignment(t, f, id, "asg-r7-new-loss", d, now)
	lossConfirm(t, f, id, "loss-r7-new-context", ownercredential.DelegationPrincipalPayload{StableAgentID: d.AgentID, SessionID: d.SessionID, Generation: d.Generation}, now)
	recovery7RejectUnchanged(t, f, func() error {
		return recovery4Handoff(t, f, d, a2, "asg-r7-new-loss", "cannot-reuse-previous-fresh-context")
	}, "post-restart loss requires fresh visibility")
	confirmVisibility(t, f, id, "vis-r7-second-fresh", ownercredential.DelegationPrincipalPayload{StableAgentID: a2.AgentID, SessionID: a2.SessionID, Generation: a2.Generation}, now)
	if err := recovery4Handoff(t, f, d, a2, "asg-r7-new-loss", "second-fresh-context"); err != nil {
		t.Fatalf("a later loss was stuck despite fresh visibility: %v", err)
	}
	recovery6Reload(t, recovery6Clone(t, f.s.state), "")
}

func TestRecovery7_A3AnyLaterLossEndsFreshSiblingContext(t *testing.T) {
	id := "del-r7-another-loss"
	f, now, _, a2, b := recovery7Fixture(t, id, true)
	if err := recovery4Handoff(t, f, b, a2, "asg-r7-x", "initial-fresh-post-loss-capture"); err != nil {
		t.Fatal(err)
	}
	d := assignment.Principal{AgentID: "agent-r7-later", SessionID: "deepseek:r7-later", Generation: 1}
	recovery5AddGrantBoundAssignment(t, f, id, "asg-r7-later", d, now)
	lossConfirm(t, f, id, "loss-r7-later", ownercredential.DelegationPrincipalPayload{StableAgentID: d.AgentID, SessionID: d.SessionID, Generation: d.Generation}, now)
	copy := recovery6Clone(t, f.s.state)
	if err := enforceDelegatedHandoff(&copy, b, a2, "asg-r7-y", now); !errors.Is(err, ErrCoordinatorDelegationInvalid) || !strings.Contains(err.Error(), "post-restart loss requires fresh visibility") {
		t.Fatalf("isolated live guard reused a context ended by a later loss: %v", err)
	}
	recovery7RejectUnchanged(t, f, func() error {
		return recovery4Handoff(t, f, b, a2, "asg-r7-y", "old-sibling-after-another-loss")
	}, "post-restart loss requires fresh visibility")
	bad := recovery6Clone(t, f.s.state)
	forgeRecoverySiblingWithoutReconsuming(t, &bad, id, "asg-r7-y", b, a2, "vis-r7-fresh", now)
	recovery6Reload(t, bad, "visibility receipt consumed more than once")
	confirmVisibility(t, f, id, "vis-r7-recapture", ownercredential.DelegationPrincipalPayload{StableAgentID: a2.AgentID, SessionID: a2.SessionID, Generation: a2.Generation}, now)
	if err := recovery4Handoff(t, f, b, a2, "asg-r7-y", "fresh-recapture-after-another-loss"); err != nil {
		t.Fatalf("new context made an old post-restart loss permanently stuck: %v", err)
	}
	recovery6Reload(t, recovery6Clone(t, f.s.state), "")
}

func TestRecovery7_A3EqualRevisionDoesNotProveEarlierLoss(t *testing.T) {
	id := "del-r7-strict-order"
	f, now, a, a2, _ := recovery7Fixture(t, id, false)
	before := recovery6Clone(t, f.s.state)
	d := before.CoordinatorDelegations[id]
	var lossRevision uint64
	for _, h := range d.History {
		if h.Kind == DelegationHistoryLossRecorded && h.ReceiptID == "loss-r4-a" {
			lossRevision = h.CanonicalRevision
		}
	}
	for i := range d.History {
		if d.History[i].Kind == DelegationHistoryPrimaryRestart {
			d.History[i].CanonicalRevision = lossRevision
		}
	}
	before.CoordinatorDelegations[id] = d
	copy := recovery6Clone(t, before)
	if err := enforceDelegatedHandoff(&copy, a, a2, "asg-r4-x", now); !errors.Is(err, ErrCoordinatorDelegationInvalid) || !strings.Contains(err.Error(), "post-restart loss requires fresh visibility") {
		t.Fatalf("live treated equal commit revisions as an earlier loss: %v", err)
	}
	bad := recovery6Clone(t, before)
	forgeRecoverySiblingWithoutReconsuming(t, &bad, id, "asg-r4-x", a, a2, "vis-r5-primary-restart", now)
	if err := validateHandoffGrantDelta(before, bad, d, bad.CoordinatorDelegations[id]); !errors.Is(err, ErrCoordinatorDelegationInvalid) || !strings.Contains(err.Error(), "visibility receipt required") {
		t.Fatalf("seam treated equal commit revisions as an earlier loss: %v", err)
	}
	recovery6Reload(t, bad, "visibility receipt consumed more than once")
}

func TestRecovery7_A3PromotedRestartRevisionCannotReorderLossHistory(t *testing.T) {
	id := "del-r7-history-prefix"
	f, now, _, a2, b := recovery7Fixture(t, id, false)
	registerSession(t, f, "codex:r7-unrelated", "agent-r7-unrelated")
	before := recovery6Clone(t, f.s.state)
	d := before.CoordinatorDelegations[id]
	for i := range d.History {
		if d.History[i].Kind == DelegationHistoryPrimaryRestart {
			d.History[i].CanonicalRevision = before.Revision
		}
	}
	before.CoordinatorDelegations[id] = d
	copy := recovery6Clone(t, before)
	if err := enforceDelegatedHandoff(&copy, b, a2, "asg-r7-x", now); !errors.Is(err, ErrCoordinatorDelegationInvalid) || !strings.Contains(err.Error(), "post-restart loss requires fresh visibility") {
		t.Fatalf("numeric revision promotion reclassified a later history entry as pre-restart: %v", err)
	}
	bad := recovery6Clone(t, before)
	forgeRecoverySiblingWithoutReconsuming(t, &bad, id, "asg-r7-x", b, a2, "vis-r5-primary-restart", now)
	if err := validateHandoffGrantDelta(before, bad, d, bad.CoordinatorDelegations[id]); !errors.Is(err, ErrCoordinatorDelegationInvalid) || !strings.Contains(err.Error(), "visibility receipt required") {
		t.Fatalf("seam trusted promoted revision over grant history order: %v", err)
	}
	recovery6Reload(t, bad, "visibility receipt consumed more than once")
}

func TestRecovery7_A3SeamAndReload(t *testing.T) {
	t.Run("another_successor_cannot_replace_acting_primary", func(t *testing.T) {
		id := "del-r7-seam-other"
		f, now, _, _, b := recovery7Fixture(t, id, true)
		_, _, c := recovery4Principals()
		confirmVisibility(t, f, id, "vis-r7-seam-other", ownercredential.DelegationPrincipalPayload{StableAgentID: c.AgentID, SessionID: c.SessionID, Generation: c.Generation}, now)
		before := recovery6Clone(t, f.s.state)
		bad := recovery6Clone(t, before)
		forgeRecoveryHandoff(t, &bad, id, "asg-r7-x", b, c, "vis-r7-seam-other", now)
		if err := validateHandoffGrantDelta(before, bad, before.CoordinatorDelegations[id], bad.CoordinatorDelegations[id]); !errors.Is(err, ErrCoordinatorDelegationInvalid) || !strings.Contains(err.Error(), "another unbarred coordinator is acting") {
			t.Fatalf("typed seam allowed a different successor while A2 acts: %v", err)
		}
		recovery7RejectUnchanged(t, f, func() error {
			return f.s.commitCanonical(delegationCommitHandoff, func(next *CanonicalState) error { *next = recovery6Clone(t, bad); return nil })
		}, "another unbarred coordinator is acting")
		recovery6Reload(t, bad, "another unbarred coordinator is acting")
	})
	t.Run("restart_receipt_cannot_cover_post_restart_loss", func(t *testing.T) {
		id := "del-r7-seam-stale"
		f, now, _, a2, b := recovery7Fixture(t, id, false)
		before := recovery6Clone(t, f.s.state)
		bad := recovery6Clone(t, before)
		forgeRecoverySiblingWithoutReconsuming(t, &bad, id, "asg-r7-x", b, a2, "vis-r5-primary-restart", now)
		if err := validateHandoffGrantDelta(before, bad, before.CoordinatorDelegations[id], bad.CoordinatorDelegations[id]); !errors.Is(err, ErrCoordinatorDelegationInvalid) || !strings.Contains(err.Error(), "visibility receipt required") {
			t.Fatalf("seam reused restart receipt for a later loss: %v", err)
		}
		recovery7RejectUnchanged(t, f, func() error {
			return f.s.commitCanonical(delegationCommitHandoff, func(next *CanonicalState) error { *next = recovery6Clone(t, bad); return nil })
		}, "visibility receipt required")
		recovery6Reload(t, bad, "visibility receipt consumed more than once")
	})
	t.Run("fresh_capture_and_sibling_callbacks", func(t *testing.T) {
		id := "del-r7-seam-fresh"
		f, now, _, a2, b := recovery7Fixture(t, id, true)
		for i, name := range []string{"asg-r7-x", "asg-r7-y"} {
			before := recovery6Clone(t, f.s.state)
			next := recovery6Clone(t, before)
			if i == 0 {
				forgeRecoveryHandoff(t, &next, id, name, b, a2, "vis-r7-fresh", now)
			} else {
				forgeRecoverySiblingWithoutReconsuming(t, &next, id, name, b, a2, "vis-r7-fresh", now)
			}
			if err := validateHandoffGrantDelta(before, next, before.CoordinatorDelegations[id], next.CoordinatorDelegations[id]); err != nil {
				t.Fatalf("fresh/sibling seam positive %s: %v", name, err)
			}
			recovery6Reload(t, next, "")
			if err := f.s.commitCanonical(delegationCommitHandoff, func(state *CanonicalState) error { *state = recovery6Clone(t, next); return nil }); err != nil {
				t.Fatalf("fresh/sibling callback %s: %v", name, err)
			}
		}
	})
	t.Run("consuming_fresh_receipt_cannot_mask_restart_digest", func(t *testing.T) {
		id := "del-r7-seam-digest"
		f, now, _, a2, b := recovery7Fixture(t, id, true)
		before := recovery6Clone(t, f.s.state)
		bad := recovery6Clone(t, before)
		forgeRecoveryHandoff(t, &bad, id, "asg-r7-x", b, a2, "vis-r7-fresh", now)
		d := bad.CoordinatorDelegations[id]
		d.History[len(d.History)-1].ReceiptDig = "vis-r5-primary-restart"
		bad.CoordinatorDelegations[id] = d
		if err := validateHandoffGrantDelta(before, bad, before.CoordinatorDelegations[id], d); !errors.Is(err, ErrCoordinatorDelegationInvalid) || !strings.Contains(err.Error(), "visibility receipt does not match handoff") {
			t.Fatalf("fresh consumption masked the wrong handoff receipt: %v", err)
		}
		recovery7RejectUnchanged(t, f, func() error {
			return f.s.commitCanonical(delegationCommitHandoff, func(next *CanonicalState) error { *next = recovery6Clone(t, bad); return nil })
		}, "visibility receipt does not match handoff")
		recovery6Reload(t, bad, "visibility receipt consumed more than once")
	})
}
