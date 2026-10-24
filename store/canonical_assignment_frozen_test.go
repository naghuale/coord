package store

import (
	"bytes"
	"coord/assignment"
	"errors"
	"testing"
)

// Freeze is an admission/schema restriction, not automatic cancellation of
// already-issued assignments. This table records actual existing transitions.
func TestAssignmentFrozenStateTable(t *testing.T) {
	for _, phase := range []string{"ACTIVE", "ACKED", "OFFERED", "NEW_OFFER", "COMPLETED", "VOID", "EXPIRED", "SUSPENDED_WITH_ACK", "SUSPENDED_WITHOUT_ACK"} {
		t.Run(phase, func(t *testing.T) {
			f := newLedgerFixture(t)
			switch phase {
			case "ACTIVE", "COMPLETED":
				f.activate(t)
			case "ACKED", "SUSPENDED_WITH_ACK":
				f.change(t, "ACK")
			}
			switch phase {
			case "COMPLETED":
				if err := f.s.AcceptAssignmentResult(f.coord, f.agent, f.result(t)); err != nil {
					t.Fatal(err)
				}
			case "VOID":
				f.change(t, "VOID")
			case "EXPIRED":
				f.now = f.record(t).ValidUntil
				f.change(t, "EXPIRE")
			case "SUSPENDED_WITH_ACK", "SUSPENDED_WITHOUT_ACK":
				f.change(t, "SUSPEND")
			}
			before := assignmentStateBytes(t, f.s)
			f.s.FreezeAssignmentLedger()
			if !bytes.Equal(before, assignmentStateBytes(t, f.s)) {
				t.Fatal("freeze automatically changed an existing assignment")
			}
			switch phase {
			case "NEW_OFFER":
				offer := f.offer()
				offer.AssignmentID = "new-id-while-frozen"
				if err := f.s.OfferAssignment(f.coord, offer, "new-offer"); !errors.Is(err, ErrLedgerFrozen) {
					t.Fatalf("new offer err=%v", err)
				}
				if !bytes.Equal(before, assignmentStateBytes(t, f.s)) {
					t.Fatal("rejected new offer changed canonical bytes")
				}
				return
			case "COMPLETED", "VOID", "EXPIRED":
				r := f.record(t)
				if err := f.s.ChangeAssignment(f.coord, f.id, r.Revision, "ACTIVATE", "revive"); err == nil {
					t.Fatal("frozen terminal revived")
				}
				if err := f.s.RecordAssignmentWorkStarted(f.agent, f.id, r.Revision, r.SubjectSHA, r.Role, r.Issuer, f.coord.SessionID, f.coord.Generation, "late-start"); err == nil {
					t.Fatal("frozen terminal allowed work")
				}
				if err := f.s.AcceptAssignmentResult(f.coord, f.agent, f.result(t)); err == nil {
					t.Fatal("frozen terminal accepted result")
				}
				if !bytes.Equal(before, assignmentStateBytes(t, f.s)) {
					t.Fatal("frozen terminal changed canonical bytes")
				}
				return
			case "SUSPENDED_WITH_ACK", "SUSPENDED_WITHOUT_ACK":
				if err := f.preflight(t); err == nil {
					t.Fatal("frozen suspended assignment allowed work")
				}
				f.change(t, "RECONCILE")
				expected := assignment.Acked
				if phase == "SUSPENDED_WITHOUT_ACK" {
					expected = assignment.Offered
				}
				if f.record(t).State != expected {
					t.Fatal("frozen reconciliation invented/lost ACK")
				}
			}
			if f.record(t).State == assignment.Offered {
				r := f.record(t)
				unchanged := assignmentStateBytes(t, f.s)
				if err := f.s.ChangeAssignment(f.coord, f.id, r.Revision, "ACTIVATE", "skip-ACK"); err == nil {
					t.Fatal("frozen OFFERED bypassed ACK")
				}
				if !bytes.Equal(unchanged, assignmentStateBytes(t, f.s)) {
					t.Fatal("rejected activation changed canonical bytes")
				}
				f.change(t, "ACK")
			}
			if f.record(t).State == assignment.Acked {
				f.change(t, "ACTIVATE")
			}
			if err := f.preflight(t); err != nil {
				t.Fatal(err)
			}
			r := f.record(t)
			if err := f.s.RecordAssignmentWorkStarted(f.agent, f.id, r.Revision, r.SubjectSHA, r.Role, r.Issuer, f.coord.SessionID, f.coord.Generation, "frozen-existing-work"); err != nil {
				t.Fatal(err)
			}
			if err := f.s.AcceptAssignmentResult(f.coord, f.agent, f.result(t)); err != nil {
				t.Fatal(err)
			}
			if f.record(t).State != assignment.Completed {
				t.Fatal("frozen existing result not completed")
			}
		})
	}
	for _, transition := range []string{"VOID", "EXPIRE"} {
		t.Run("ACTIVE_"+transition, func(t *testing.T) {
			f := newLedgerFixture(t)
			f.activate(t)
			f.s.FreezeAssignmentLedger()
			expected := assignment.Void
			if transition == "EXPIRE" {
				f.now = f.record(t).ValidUntil
				expected = assignment.Expired
			}
			f.change(t, transition)
			if f.record(t).State != expected {
				t.Fatal("frozen active terminal transition failed")
			}
		})
	}
}
