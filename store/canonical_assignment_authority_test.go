package store

import (
	"bytes"
	"coord/assignment"
	"crypto/ed25519"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func assignmentStateBytes(t *testing.T, s *CanonicalStore) []byte {
	t.Helper()
	state, err := s.CanonicalSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func registerAssignmentSuccessor(t *testing.T, f *ledgerFixture) assignment.Principal {
	t.Helper()
	next := assignment.Principal{SessionID: "claude:coord-2", AgentID: "coordinator", Generation: 1}
	if err := f.s.RegisterAgentSession(next.SessionID, next.AgentID, ed25519.PublicKey(strings.Repeat("c", 32))); err != nil {
		t.Fatal(err)
	}
	return next
}

func invalidateAssignmentPrincipal(t *testing.T, f *ledgerFixture, p assignment.Principal, kind string) {
	t.Helper()
	key := ed25519.PublicKey(strings.Repeat("z", 32))
	var err error
	if kind == "rotated" {
		err = f.s.RotateAgentSessionKey(p.SessionID, key)
	} else {
		err = f.s.ReplaceAgentSessionAddress(p.SessionID, p.SessionID+"-replacement", key, "verified-session-replacement")
	}
	if err != nil {
		t.Fatal(err)
	}
}

func TestAssignmentHandoffRequiresCurrentPredecessor(t *testing.T) {
	for _, kind := range []string{"rotated", "revoked"} {
		t.Run(kind, func(t *testing.T) {
			f := newLedgerFixture(t)
			f.activate(t)
			next := registerAssignmentSuccessor(t, f)
			invalidateAssignmentPrincipal(t, f, f.coord, kind)
			before := assignmentStateBytes(t, f.s)
			if err := f.s.HandoffAssignment(f.coord, next, f.id, f.record(t).Revision, "stale-handoff-frame"); err == nil {
				t.Fatal("stale predecessor transferred authority before recorded loss")
			}
			if !bytes.Equal(before, assignmentStateBytes(t, f.s)) {
				t.Fatal("rejected stale handoff changed canonical bytes")
			}
		})
	}
	t.Run("current", func(t *testing.T) {
		f := newLedgerFixture(t)
		f.activate(t)
		next := registerAssignmentSuccessor(t, f)
		if err := f.s.HandoffAssignment(f.coord, next, f.id, f.record(t).Revision, "verified-live-handoff"); err != nil {
			t.Fatal(err)
		}
		state, err := f.s.CanonicalSnapshot()
		if err != nil {
			t.Fatal(err)
		}
		event := state.AssignmentLedger.Events[len(state.AssignmentLedger.Events)-1]
		if event.Actor != f.coord || event.Record.Coordinator() != next {
			t.Fatal("normal handoff did not preserve active predecessor actor")
		}
		f.coord = next
		if err := f.preflight(t); err != nil {
			t.Fatal(err)
		}
	})
}

func TestAssignmentLossRequiresCurrentObserver(t *testing.T) {
	for _, kind := range []string{"rotated", "revoked"} {
		t.Run(kind, func(t *testing.T) {
			f := newLedgerFixture(t)
			f.activate(t)
			observer := registerAssignmentLossObserver(t, f)
			invalidateAssignmentPrincipal(t, f, observer, kind)
			before := assignmentStateBytes(t, f.s)
			if err := f.s.ObserveAssignmentCoordinatorLoss(observer, f.coord, "old-generation-loss-frame"); err == nil {
				t.Fatal("stale observer recorded coordinator loss")
			}
			if !bytes.Equal(before, assignmentStateBytes(t, f.s)) {
				t.Fatal("rejected stale loss observation changed canonical bytes")
			}
		})
	}
}

func TestAssignmentLossRecoveryWithRotatedCoordinator(t *testing.T) {
	f := newLedgerFixture(t)
	f.activate(t)
	original := f.record(t)
	next := registerAssignmentSuccessor(t, f)
	observer := registerAssignmentLossObserver(t, f)
	invalidateAssignmentPrincipal(t, f, f.coord, "rotated")
	// The active observer is distinct from the stale lost-coordinator subject.
	if err := f.s.ObserveAssignmentCoordinatorLoss(observer, f.coord, "verified-supervisor-loss"); err != nil {
		t.Fatal(err)
	}
	state, err := f.s.CanonicalSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	event := state.AssignmentLedger.Events[len(state.AssignmentLedger.Events)-1]
	if event.Actor != observer || event.Record.Coordinator() != f.coord || !event.Record.CoordinatorLost {
		t.Fatal("loss actor was conflated with lost coordinator subject")
	}
	before := assignmentStateBytes(t, f.s)
	if err := f.preflight(t); err == nil {
		t.Fatal("recorded loss permitted work")
	}
	for _, candidate := range []struct {
		name      string
		principal assignment.Principal
		evidence  string
	}{
		{"no-evidence", next, ""},
		{"stale-successor", f.coord, "verified-recovery"},
	} {
		if err := f.s.HandoffAssignment(f.coord, candidate.principal, f.id, f.record(t).Revision, candidate.evidence); err == nil {
			t.Fatalf("invalid recovery accepted: %s", candidate.name)
		}
		if !bytes.Equal(before, assignmentStateBytes(t, f.s)) {
			t.Fatalf("invalid recovery changed canonical bytes: %s", candidate.name)
		}
	}
	if err := f.s.HandoffAssignment(f.coord, next, f.id, f.record(t).Revision, "verified-owner-recovery"); err != nil {
		t.Fatal(err)
	}
	state, err = f.s.CanonicalSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	event = state.AssignmentLedger.Events[len(state.AssignmentLedger.Events)-1]
	r := event.Record
	if event.Actor != next || r.CoordinatorLost || r.Coordinator() != next || r.Issuer != original.Issuer || !r.ValidUntil.Equal(original.ValidUntil) {
		t.Fatal("recovery actor/lineage/issuer/deadline invalid")
	}
	if err := f.s.Close(); err != nil {
		t.Fatal(err)
	}
	f.s, err = NewCanonicalWithClock(f.p, func() time.Time { return f.now })
	if err != nil {
		t.Fatal(err)
	}
	f.coord = next
	if err := f.preflight(t); err != nil {
		t.Fatal(err)
	}
	r = f.record(t)
	if err := f.s.RecordAssignmentWorkStarted(f.agent, f.id, r.Revision, r.SubjectSHA, r.Role, original.Issuer, next.SessionID, next.Generation, "verified-work-start"); err != nil {
		t.Fatal(err)
	}
	if err := f.s.AcceptAssignmentResult(next, f.agent, f.result(t)); err != nil {
		t.Fatal(err)
	}
}

func TestAssignmentPreflightAndWorkStartedRequireIssuer(t *testing.T) {
	f := newLedgerFixture(t)
	f.activate(t)
	r := f.record(t)
	before := assignmentStateBytes(t, f.s)
	for _, issuer := range []string{"", "claude:wrong-issuer"} {
		if err := f.s.CheckAssignmentWork(f.agent, f.id, r.SubjectSHA, r.Role, issuer, f.coord.SessionID, f.coord.Generation); err == nil {
			t.Fatal("preflight ignored expected issuer")
		}
		if err := f.s.RecordAssignmentWorkStarted(f.agent, f.id, r.Revision, r.SubjectSHA, r.Role, issuer, f.coord.SessionID, f.coord.Generation, "start"); err == nil {
			t.Fatal("workStarted ignored expected issuer")
		}
		if !bytes.Equal(before, assignmentStateBytes(t, f.s)) {
			t.Fatal("issuer mismatch changed canonical bytes")
		}
	}
	if err := f.s.CheckAssignmentWork(f.agent, f.id, r.SubjectSHA, r.Role, r.Issuer, f.coord.SessionID, f.coord.Generation); err != nil {
		t.Fatal(err)
	}
	if err := f.s.RecordAssignmentWorkStarted(f.agent, f.id, r.Revision, r.SubjectSHA, r.Role, r.Issuer, f.coord.SessionID, f.coord.Generation, "verified-start"); err != nil {
		t.Fatal(err)
	}
}
