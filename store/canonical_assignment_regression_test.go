package store

import (
	"bytes"
	"coord/assignment"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// A90-1: identity is stale even if the assignment still names the same tuple.
// EXPIRE is tested at its legal deadline, so a time guard cannot mask identity.
func TestAssignmentChangeRejectsStaleCoordinatorFrames(t *testing.T) {
	for _, action := range []string{"SUSPEND", "VOID", "EXPIRE"} {
		for _, rotated := range []bool{false, true} {
			label := "current"
			if rotated {
				label = "rotated"
			}
			t.Run(action+"/"+label, func(t *testing.T) {
				f := newLedgerFixture(t)
				f.activate(t)
				if action == "EXPIRE" {
					f.now = f.record(t).ValidUntil
				}
				if rotated {
					if err := f.s.RotateAgentSessionKey(f.coord.SessionID, ed25519.PublicKey(strings.Repeat("q", 32))); err != nil {
						t.Fatal(err)
					}
				}
				r := f.record(t)
				if r.Coordinator() != f.coord {
					t.Fatal("fixture no longer names the tested coordinator frame")
				}
				before := assignmentStateBytes(t, f.s)
				saves := f.p.saveCalls
				err := f.s.ChangeAssignment(f.coord, f.id, r.Revision, action, "verified-frame")
				if rotated {
					if !errors.Is(err, assignment.ErrInvalid) {
						t.Errorf("stale coordinator performed %s: %v", action, err)
					}
					if !bytes.Equal(before, assignmentStateBytes(t, f.s)) || f.p.saveCalls != saves {
						t.Error("stale coordinator changed canonical state or persisted a mutation")
					}
				} else {
					if err != nil {
						t.Fatal(err)
					}
					expected := assignment.Suspended
					if action == "VOID" {
						expected = assignment.Void
					}
					if action == "EXPIRE" {
						expected = assignment.Expired
					}
					if f.record(t).State != expected {
						t.Fatal("current coordinator positive control did not transition")
					}
				}
			})
		}
	}
}

func reloadAssignmentRegressionState(t *testing.T, state CanonicalState, now time.Time) *CanonicalStore {
	t.Helper()
	before, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	var decoded CanonicalState
	if err := json.Unmarshal(before, &decoded); err != nil {
		t.Fatal(err)
	}
	disk := &canonicalMemory{state: decoded}
	restored, err := NewCanonicalWithClock(disk, func() time.Time { return now })
	if err != nil {
		t.Fatalf("valid event-time history did not reload: %v", err)
	}
	t.Cleanup(func() {
		if err := restored.Close(); err != nil {
			t.Error(err)
		}
	})
	after := assignmentStateBytes(t, restored)
	if !bytes.Equal(before, after) || disk.saveCalls != 0 {
		t.Fatal("valid snapshot reload rewrote canonical history")
	}
	return restored
}

// A90-2, availability direction: a later target cannot invalidate an earlier
// observation that was valid before that target assignment existed.
func TestAssignmentReplayAllowsLaterObserverTarget(t *testing.T) {
	f := newLedgerFixture(t)
	f.activate(t)
	observer := registerAssignmentLossObserver(t, f)
	successor := registerAssignmentSuccessor(t, f)
	if err := f.s.ObserveAssignmentCoordinatorLoss(observer, f.coord, "early-loss"); err != nil {
		t.Fatal(err)
	}
	if err := f.s.HandoffAssignment(f.coord, successor, f.id, f.record(t).Revision, "early-recovery"); err != nil {
		t.Fatal(err)
	}
	later := *f
	later.id = "observer-target-created-later"
	later.agent = observer
	f.now = f.now.Add(time.Second)
	later.now = f.now
	if err := f.s.OfferAssignment(later.coord, later.offer(), "later-target"); err != nil {
		t.Fatalf("later valid offer was judged against earlier loss: %v", err)
	}
	state, err := f.s.CanonicalSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if state.AssignmentLedger.Records[later.id].Target() != observer {
		t.Fatal("later record does not expose the final-state conflict")
	}
	restored := reloadAssignmentRegressionState(t, state, f.now)
	replayed, err := restored.CanonicalSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if replayed.AssignmentLedger.Records[f.id].Coordinator() != successor {
		t.Fatal("reload lost the earlier valid recovery")
	}
}

// A90-2, safety direction: final completion of another assignment must not hide
// that its target was the observer at the time of a forged loss event.
func TestAssignmentReplayRejectsEarlierConflictDespiteFinalCompletion(t *testing.T) {
	f := newLedgerFixture(t)
	f.activate(t)
	observer := registerAssignmentLossObserver(t, f)
	other := *f
	other.id = "observer-target-before-loss"
	other.agent = observer
	if err := f.s.OfferAssignment(other.coord, other.offer(), "earlier-target"); err != nil {
		t.Fatal(err)
	}
	other.activate(t)
	state, err := f.s.CanonicalSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	injectAssignmentEvent(&state, forgedLossEvent(f, state.AssignmentLedger.Records[f.id], observer))
	completed := state.AssignmentLedger.Records[other.id]
	claim := other.result(t)
	completed.State = assignment.Completed
	completed.AcceptedResult = &claim
	completed.Revision++
	completed.UpdatedAt = f.now
	injectAssignmentEvent(&state, assignment.Event{AssignmentID: other.id, Revision: completed.Revision, Kind: "RESULT", Actor: other.coord, EvidenceRef: claim.EvidenceRef, At: f.now, Record: completed})
	if state.AssignmentLedger.Records[other.id].State != assignment.Completed {
		t.Fatal("fixture final state must mask the earlier target conflict")
	}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	var decoded CanonicalState
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	disk := &canonicalMemory{state: decoded}
	restored, err := NewCanonicalWithClock(disk, func() time.Time { return f.now })
	if err == nil {
		if closeErr := restored.Close(); closeErr != nil {
			t.Error(closeErr)
		}
		t.Fatal("final completion hid an earlier invalid loss during replay")
	}
	if !errors.Is(err, assignment.ErrInvalid) {
		t.Fatalf("unexpected snapshot error: %v", err)
	}
	after, err := json.Marshal(disk.state)
	if err != nil {
		t.Fatal(err)
	}
	if disk.saveCalls != 0 || !bytes.Equal(raw, after) {
		t.Fatal("rejected forged snapshot was repaired or saved")
	}
}

// A90-3: a failed first offer may initialize only its disposable working copy.
func TestAssignmentFailedFirstOfferDoesNotPublish(t *testing.T) {
	disk := &canonicalMemory{state: EmptyCanonicalState()}
	now := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	s, err := NewCanonicalWithClock(disk, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	f := &ledgerFixture{s: s, p: disk, now: now, id: "first-offer-retry", coord: assignment.Principal{SessionID: "claude:first-offer-coordinator", AgentID: "coordinator", Generation: 1}, agent: assignment.Principal{SessionID: "codex:first-offer-target", AgentID: "target", Generation: 1}}
	for i, p := range []assignment.Principal{f.coord, f.agent} {
		if err := s.RegisterAgentSession(p.SessionID, p.AgentID, ed25519.PublicKey(strings.Repeat(string(rune('m'+i)), 32))); err != nil {
			t.Fatal(err)
		}
	}
	initial, err := s.CanonicalSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if initial.AssignmentLedger != nil {
		t.Fatal("fixture must start without a ledger")
	}
	before := assignmentStateBytes(t, s)
	saves := disk.saveCalls
	invalid := f.offer()
	invalid.ValidUntil = now.Add(-time.Second)
	if err := s.OfferAssignment(f.coord, invalid, "rejected-first-offer"); !errors.Is(err, assignment.ErrInvalid) {
		t.Errorf("invalid initial offer err=%v", err)
	}
	after, err := s.CanonicalSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, assignmentStateBytes(t, s)) || after.AssignmentLedger != nil {
		t.Error("failed first offer changed published snapshot")
	}
	persisted, err := json.Marshal(disk.state)
	if err != nil {
		t.Fatal(err)
	}
	if disk.saveCalls != saves || !bytes.Equal(before, persisted) {
		t.Fatal("failed first offer changed durable state")
	}
	// A valid retry proves the failed ID was not consumed and initial creation works.
	if err := s.OfferAssignment(f.coord, f.offer(), "valid-first-offer-retry"); err != nil {
		t.Fatal(err)
	}
	final, err := s.CanonicalSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if final.AssignmentLedger == nil || len(final.AssignmentLedger.Records) != 1 || len(final.AssignmentLedger.Events) != 1 || disk.saveCalls != saves+1 {
		t.Fatal("valid retry did not create exactly one offer")
	}
}

// A90-4: another assignment's global history must not terminate this interval.
func TestAssignmentOpenLossSurvivesUnrelatedOffer(t *testing.T) {
	f := newLedgerFixture(t)
	f.activate(t)
	observer := registerAssignmentLossObserver(t, f)
	successor := registerAssignmentSuccessor(t, f)
	if err := f.s.ObserveAssignmentCoordinatorLoss(observer, f.coord, "original-loss"); err != nil {
		t.Fatal(err)
	}
	unrelated := *f
	unrelated.id = "unrelated-between-loss-and-handoff"
	unrelated.coord = successor
	f.now = f.now.Add(2 * time.Second)
	unrelated.now = f.now
	if err := f.s.OfferAssignment(successor, unrelated.offer(), "unrelated-offer"); err != nil {
		t.Fatal(err)
	}
	state, err := f.s.CanonicalSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if actual, open := state.AssignmentLedger.OpenCoordinatorLossObserver(f.id); !open || actual != observer {
		t.Fatal("unrelated assignment hid open loss")
	}
	if err := f.s.HandoffAssignment(f.coord, successor, f.id, f.record(t).Revision, "distinct-recovery-after-unrelated-offer"); err != nil {
		t.Fatal(err)
	}
	state, err = f.s.CanonicalSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	restored := reloadAssignmentRegressionState(t, state, f.now)
	r, err := restored.CanonicalSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if r.AssignmentLedger.Records[f.id].Coordinator() != successor || r.AssignmentLedger.Records[f.id].CoordinatorLost {
		t.Fatal("unrelated offer prevented valid recovery")
	}
	if _, open := r.AssignmentLedger.OpenCoordinatorLossObserver(f.id); open {
		t.Fatal("successful recovery left original loss open")
	}
}
