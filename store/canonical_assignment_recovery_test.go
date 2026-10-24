package store

import (
	"bytes"
	"coord/assignment"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func registerAssignmentLossObserver(t *testing.T, f *ledgerFixture) assignment.Principal {
	t.Helper()
	observer := assignment.Principal{SessionID: "codex:loss-observer", AgentID: "loss-observer", Generation: 1}
	if err := f.s.RegisterAgentSession(observer.SessionID, observer.AgentID, ed25519.PublicKey(strings.Repeat("d", 32))); err != nil {
		t.Fatal(err)
	}
	return observer
}

func TestAssignmentLossRejectsCoordinatorAndTarget(t *testing.T) {
	for _, who := range []string{"coordinator", "assignee"} {
		t.Run(who, func(t *testing.T) {
			f := newLedgerFixture(t)
			f.activate(t)
			observer := f.coord
			if who == "assignee" {
				observer = f.agent
			}
			before := assignmentStateBytes(t, f.s)
			if err := f.s.ObserveAssignmentCoordinatorLoss(observer, f.coord, "self-loss"); !errors.Is(err, assignment.ErrInvalid) {
				t.Fatalf("self-loss err=%v", err)
			}
			if !bytes.Equal(before, assignmentStateBytes(t, f.s)) {
				t.Fatal("rejected self-loss changed canonical bytes")
			}
		})
	}
}

func TestAssignmentLossBatchRejectsAnyAffectedTarget(t *testing.T) {
	f := newLedgerFixture(t)
	f.activate(t)
	observer := registerAssignmentLossObserver(t, f)
	offer := f.offer()
	offer.AssignmentID = "other-target-in-same-batch"
	offer.Assignee, offer.SessionID, offer.StableAgentID = observer.SessionID, observer.SessionID, observer.AgentID
	offer.AdmissionBasis.TargetSessionID, offer.AdmissionBasis.TargetAgentID = observer.SessionID, observer.AgentID
	offer.VisibilityEvidence.SessionID = observer.SessionID
	if err := f.s.OfferAssignment(f.coord, offer, "second-offer"); err != nil {
		t.Fatal(err)
	}
	before := assignmentStateBytes(t, f.s)
	if err := f.s.ObserveAssignmentCoordinatorLoss(observer, f.coord, "batch-observation"); !errors.Is(err, assignment.ErrInvalid) {
		t.Fatalf("target in batch err=%v", err)
	}
	if !bytes.Equal(before, assignmentStateBytes(t, f.s)) {
		t.Fatal("loss batch partially committed before target rejection")
	}
}

func TestAssignmentRecoveryRejectsTargetAndOpenLossObserver(t *testing.T) {
	for _, who := range []string{"assignee-self-succession", "observer-self-succession", "third-party-observer-self-succession"} {
		t.Run(who, func(t *testing.T) {
			f := newLedgerFixture(t)
			f.activate(t)
			observer := registerAssignmentLossObserver(t, f)
			if who == "observer-self-succession" {
				observer = registerAssignmentSuccessor(t, f)
			}
			if err := f.s.ObserveAssignmentCoordinatorLoss(observer, f.coord, "verified-loss"); err != nil {
				t.Fatal(err)
			}
			next := observer
			if who == "assignee-self-succession" {
				next = f.agent
			}
			before := assignmentStateBytes(t, f.s)
			if err := f.s.HandoffAssignment(f.coord, next, f.id, f.record(t).Revision, "self-succession"); !errors.Is(err, assignment.ErrInvalid) {
				t.Fatalf("recovery err=%v", err)
			}
			if !bytes.Equal(before, assignmentStateBytes(t, f.s)) {
				t.Fatal("rejected recovery changed canonical bytes")
			}
		})
	}
}

func TestAssignmentRecoveryUsesOnlyOpenLossObserver(t *testing.T) {
	f := newLedgerFixture(t)
	f.activate(t)
	observerA := registerAssignmentLossObserver(t, f)
	successorB := registerAssignmentSuccessor(t, f)
	if err := f.s.ObserveAssignmentCoordinatorLoss(observerA, f.coord, "first-loss"); err != nil {
		t.Fatal(err)
	}
	if err := f.s.HandoffAssignment(f.coord, successorB, f.id, f.record(t).Revision, "distinct-successor-B"); err != nil {
		t.Fatal(err)
	}
	f.coord = successorB
	if err := f.preflight(t); err != nil {
		t.Fatal(err)
	}
	state, err := f.s.CanonicalSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if _, open := state.AssignmentLedger.OpenCoordinatorLossObserver(f.id); open {
		t.Fatal("handoff did not close first loss interval")
	}
	// The original coordinator can observe B's later loss; A's old interval is closed.
	observerC := assignment.Principal{SessionID: "claude:coord-1", AgentID: "coordinator", Generation: 1}
	if err := f.s.ObserveAssignmentCoordinatorLoss(observerC, successorB, "second-loss"); err != nil {
		t.Fatal(err)
	}
	if err := f.s.HandoffAssignment(successorB, observerA, f.id, f.record(t).Revision, "A-distinct-from-current-observer-C"); err != nil {
		t.Fatal(err)
	}
	f.coord = observerA
	if err := f.preflight(t); err != nil {
		t.Fatal(err)
	}
	if err := f.s.AcceptAssignmentResult(observerA, f.agent, f.result(t)); err != nil {
		t.Fatal(err)
	}
}

func forgedLossEvent(f *ledgerFixture, r assignment.Record, actor assignment.Principal) assignment.Event {
	r.Revision++
	r.UpdatedAt = f.now
	r.CoordinatorLost = true
	return assignment.Event{AssignmentID: r.AssignmentID, Revision: r.Revision, Kind: "COORDINATOR_LOST", Actor: actor, EvidenceRef: "forged-loss", At: f.now, Record: r}
}
func forgedHandoffEvent(f *ledgerFixture, r assignment.Record, next assignment.Principal) assignment.Event {
	previous := r.Coordinator()
	r.Revision++
	r.UpdatedAt = f.now
	r.CoordinatorLost = false
	r.CurrentCoordinator, r.CoordinatorAgentID, r.CoordinatorGeneration = next.SessionID, next.AgentID, next.Generation
	r.HandoffEvidence = append(append([]assignment.Handoff{}, r.HandoffEvidence...), assignment.Handoff{From: previous, To: next, EvidenceRef: "forged-handoff", At: f.now})
	return assignment.Event{AssignmentID: r.AssignmentID, Revision: r.Revision, Kind: "HANDOFF", Actor: next, EvidenceRef: "forged-handoff", At: f.now, Record: r}
}
func injectAssignmentEvent(state *CanonicalState, event assignment.Event) {
	// Deliberately bypass Append to model a forged persisted projection/history.
	state.AssignmentLedger.Records[event.AssignmentID] = event.Record
	state.AssignmentLedger.Events = append(state.AssignmentLedger.Events, event)
	state.Revision++
}

func TestAssignmentReplayRejectsSelfInterestedRecovery(t *testing.T) {
	for _, kind := range []string{"loss-by-coordinator", "loss-by-target", "loss-conflict-in-another-record", "handoff-to-target", "handoff-to-observer", "replace-open-loss-observer"} {
		t.Run(kind, func(t *testing.T) {
			f := newLedgerFixture(t)
			f.activate(t)
			observer := registerAssignmentLossObserver(t, f)
			successor := registerAssignmentSuccessor(t, f)
			if kind == "loss-conflict-in-another-record" {
				offer := f.offer()
				offer.AssignmentID = "other-record-targets-observer"
				offer.Assignee, offer.SessionID, offer.StableAgentID = observer.SessionID, observer.SessionID, observer.AgentID
				offer.AdmissionBasis.TargetSessionID, offer.AdmissionBasis.TargetAgentID = observer.SessionID, observer.AgentID
				offer.VisibilityEvidence.SessionID = observer.SessionID
				if err := f.s.OfferAssignment(f.coord, offer, "before-loss"); err != nil {
					t.Fatal(err)
				}
			}
			if strings.HasPrefix(kind, "handoff") || kind == "replace-open-loss-observer" {
				if err := f.s.ObserveAssignmentCoordinatorLoss(observer, f.coord, "legitimate-loss"); err != nil {
					t.Fatal(err)
				}
			}
			state, err := f.s.CanonicalSnapshot()
			if err != nil {
				t.Fatal(err)
			}
			r := state.AssignmentLedger.Records[f.id]
			var event assignment.Event
			switch kind {
			case "loss-by-coordinator":
				event = forgedLossEvent(f, r, f.coord)
			case "loss-by-target":
				event = forgedLossEvent(f, r, f.agent)
			case "loss-conflict-in-another-record":
				event = forgedLossEvent(f, r, observer)
			case "handoff-to-target":
				event = forgedHandoffEvent(f, r, f.agent)
			case "handoff-to-observer":
				event = forgedHandoffEvent(f, r, observer)
			case "replace-open-loss-observer":
				event = forgedLossEvent(f, r, successor)
			}
			before, err := json.Marshal(state.AssignmentLedger)
			if err != nil {
				t.Fatal(err)
			}
			var direct assignment.Ledger
			if err := json.Unmarshal(before, &direct); err != nil {
				t.Fatal(err)
			}
			if err := direct.Append(event); !errors.Is(err, assignment.ErrInvalid) {
				t.Errorf("direct invalid event err=%v", err)
			}
			after, err := json.Marshal(&direct)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Error("rejected replay event mutated ledger")
			}
			injectAssignmentEvent(&state, event)
			if kind == "replace-open-loss-observer" {
				injectAssignmentEvent(&state, forgedHandoffEvent(f, event.Record, observer))
			}
			raw, err := json.Marshal(state)
			if err != nil {
				t.Fatal(err)
			}
			var decoded CanonicalState
			if err := json.Unmarshal(raw, &decoded); err != nil {
				t.Fatal(err)
			}
			persistence := &canonicalMemory{state: decoded}
			opened, err := NewCanonical(persistence)
			if err == nil {
				if closeErr := opened.Close(); closeErr != nil {
					t.Error(closeErr)
				}
				t.Fatal("unsafe recovery history accepted on snapshot load")
			}
			if !errors.Is(err, assignment.ErrInvalid) {
				t.Fatalf("unrelated load failure: %v", err)
			}
			unchanged, err := json.Marshal(persistence.state)
			if err != nil {
				t.Fatal(err)
			}
			if persistence.saveCalls != 0 || !bytes.Equal(raw, unchanged) {
				t.Fatal("invalid snapshot was repaired or rewritten")
			}
		})
	}
}
