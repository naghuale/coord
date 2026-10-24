package store

import (
	"coord/assignment"
	"coord/fsm"
	"coord/roles"
	"coord/task"
	"coord/taskid"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type ledgerFixture struct {
	s            *CanonicalStore
	p            *canonicalMemory
	now          time.Time
	coord, agent assignment.Principal
	id           string
}

func newLedgerFixture(t *testing.T) *ledgerFixture {
	t.Helper()
	f := &ledgerFixture{p: &canonicalMemory{state: EmptyCanonicalState()}, now: time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC), coord: assignment.Principal{SessionID: "claude:coord-1", AgentID: "coordinator", Generation: 1}, agent: assignment.Principal{SessionID: "opencode:oe4-session", AgentID: "oe4", Generation: 1}, id: "assignment-oe4-new"}
	var err error
	f.s, err = NewCanonicalWithClock(f.p, func() time.Time { return f.now })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.s.Close() })
	for i, p := range []assignment.Principal{f.coord, f.agent} {
		if err := f.s.RegisterAgentSession(p.SessionID, p.AgentID, ed25519.PublicKey(strings.Repeat(string(rune('a'+i)), 32))); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.s.OfferAssignment(f.coord, f.offer(), "owner-approved-offer"); err != nil {
		t.Fatal(err)
	}
	return f
}
func (f *ledgerFixture) offer() assignment.Record {
	return assignment.Record{AssignmentID: f.id, State: assignment.Offered, Issuer: f.coord.SessionID, IssuerAgentID: f.coord.AgentID, IssuerGeneration: f.coord.Generation, CurrentCoordinator: f.coord.SessionID, CoordinatorAgentID: f.coord.AgentID, CoordinatorGeneration: f.coord.Generation, HandoffEvidence: []assignment.Handoff{}, Assignee: f.agent.SessionID, StableAgentID: f.agent.AgentID, SessionID: f.agent.SessionID, AssigneeGeneration: 1, Role: "AUDITOR", SubjectSHA: strings.Repeat("a", 40), IssuedAt: f.now, ValidUntil: f.now.Add(time.Hour), AdmissionBasis: assignment.AdmissionBasis{Kind: "POOL", Reference: "exact-owner-target", TargetSessionID: f.agent.SessionID, TargetAgentID: f.agent.AgentID, Role: "AUDITOR", SubjectSHA: strings.Repeat("a", 40)}, VisibilityEvidence: assignment.VisibilityEvidence{Reference: "operator-visible-session-receipt", SessionID: f.agent.SessionID, ObservedAt: f.now}, Revision: 1, UpdatedAt: f.now}
}
func (f *ledgerFixture) record(t *testing.T) assignment.Record {
	t.Helper()
	state, err := f.s.CanonicalSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	return state.AssignmentLedger.Records[f.id]
}
func (f *ledgerFixture) change(t *testing.T, kind string) {
	t.Helper()
	actor := f.coord
	if kind == "ACK" {
		actor = f.agent
	}
	if err := f.s.ChangeAssignment(actor, f.id, f.record(t).Revision, kind, "verified-"+kind); err != nil {
		t.Fatal(err)
	}
}
func (f *ledgerFixture) activate(t *testing.T) {
	t.Helper()
	f.change(t, "ACK")
	f.change(t, "ACTIVATE")
}
func (f *ledgerFixture) preflight(t *testing.T) error {
	t.Helper()
	r := f.record(t)
	return f.s.CheckAssignmentWork(f.agent, f.id, r.SubjectSHA, r.Role, r.Issuer, f.coord.SessionID, f.coord.Generation)
}
func (f *ledgerFixture) result(t *testing.T) assignment.Result {
	t.Helper()
	r := f.record(t)
	return assignment.Result{AssignmentID: r.AssignmentID, SubjectSHA: r.SubjectSHA, Assignee: r.Assignee, SessionID: r.SessionID, StableAgentID: r.StableAgentID, Role: r.Role, CoordinatorGeneration: r.CoordinatorGeneration, ResultID: "result-1", Digest: strings.Repeat("d", 64), EvidenceRef: "immutable-result-artifact"}
}

func TestAssignmentNoWorkBeforeActiveAndAckInsufficient(t *testing.T) {
	f := newLedgerFixture(t)
	for _, phase := range []string{"OFFERED", "ACKED"} {
		if phase == "ACKED" {
			f.change(t, "ACK")
		}
		before, _ := f.s.CanonicalSnapshot()
		if err := f.preflight(t); err == nil {
			t.Fatalf("work authorized in %s", phase)
		}
		r := f.record(t)
		if err := f.s.RecordAssignmentWorkStarted(f.agent, f.id, r.Revision, r.SubjectSHA, r.Role, r.Issuer, f.coord.SessionID, 1, "work-start"); err == nil {
			t.Fatalf("workStarted accepted in %s", phase)
		}
		if err := f.s.AcceptAssignmentResult(f.coord, f.agent, f.result(t)); err == nil {
			t.Fatalf("result accepted in %s", phase)
		}
		after, _ := f.s.CanonicalSnapshot()
		if !reflect.DeepEqual(before, after) {
			t.Fatal("invalid work changed canonical state")
		}
	}
	f.change(t, "ACTIVATE")
	if err := f.preflight(t); err != nil {
		t.Fatal(err)
	}
	r := f.record(t)
	if err := f.s.RecordAssignmentWorkStarted(f.agent, f.id, r.Revision, r.SubjectSHA, r.Role, r.Issuer, f.coord.SessionID, 1, "actual-start"); err != nil {
		t.Fatal(err)
	}
	if f.record(t).WorkStartedAt.IsZero() {
		t.Fatal("workStarted observation not recorded")
	}
}

func TestAssignmentSuspensionCannotInventAck(t *testing.T) {
	f := newLedgerFixture(t)
	f.change(t, "SUSPEND")
	f.change(t, "RECONCILE")
	if f.record(t).State != assignment.Offered || !f.record(t).AcknowledgedAt.IsZero() {
		t.Fatal("reconciliation invented ACK")
	}
	if err := f.s.ChangeAssignment(f.coord, f.id, f.record(t).Revision, "ACTIVATE", "skip-ACK"); err == nil {
		t.Fatal("activation bypassed exact target ACK")
	}
	f.activate(t)
	f.change(t, "SUSPEND")
	f.change(t, "RECONCILE")
	if f.record(t).State != assignment.Acked {
		t.Fatal("prior verified ACK was lost")
	}
	if err := f.preflight(t); err == nil {
		t.Fatal("reconciliation resumed without explicit activation")
	}
	f.change(t, "ACTIVATE")
	if err := f.preflight(t); err != nil {
		t.Fatal(err)
	}
}

func TestAssignmentOfferPreservesIssueTimeBeforeCommit(t *testing.T) {
	f := newLedgerFixture(t)
	r := f.offer()
	r.AssignmentID = "prepared-earlier"
	issued, deadline := r.IssuedAt, r.ValidUntil
	f.now = f.now.Add(time.Second)
	if err := f.s.OfferAssignment(f.coord, r, "new-explicit-coordinator-offer"); err != nil {
		t.Fatalf("normal delay between preparation and commit rejected: %v", err)
	}
	state, _ := f.s.CanonicalSnapshot()
	saved := state.AssignmentLedger.Records[r.AssignmentID]
	if !saved.IssuedAt.Equal(issued) || !saved.ValidUntil.Equal(deadline) || !saved.UpdatedAt.Equal(f.now) {
		t.Fatal("issuance/deadline reset to commit time")
	}
	r.AssignmentID = "future-offer"
	r.IssuedAt = f.now.Add(time.Minute)
	if err := f.s.OfferAssignment(f.coord, r, "future"); err == nil {
		t.Fatal("future issuance accepted")
	}
}

func TestAssignmentTerminalNeverRevives(t *testing.T) {
	for _, terminal := range []assignment.State{assignment.Void, assignment.Expired, assignment.Completed} {
		t.Run(string(terminal), func(t *testing.T) {
			f := newLedgerFixture(t)
			f.activate(t)
			switch terminal {
			case assignment.Void:
				f.change(t, "VOID")
			case assignment.Expired:
				f.now = f.record(t).ValidUntil
				f.change(t, "EXPIRE")
			case assignment.Completed:
				if err := f.s.AcceptAssignmentResult(f.coord, f.agent, f.result(t)); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := f.s.CanonicalSnapshot()
			for _, kind := range []string{"ACTIVATE", "ACK", "RECONCILE", "SUSPEND"} {
				if err := f.s.ChangeAssignment(f.coord, f.id, f.record(t).Revision, kind, "new-ACK-or-ALIVE"); err == nil {
					t.Fatalf("%s revived by %s", terminal, kind)
				}
			}
			if err := f.preflight(t); err == nil {
				t.Fatalf("terminal %s granted work", terminal)
			}
			if err := f.s.AcceptAssignmentResult(f.coord, f.agent, f.result(t)); err == nil {
				t.Fatalf("terminal %s accepted another result", terminal)
			}
			after, _ := f.s.CanonicalSnapshot()
			if !reflect.DeepEqual(before, after) {
				t.Fatal("terminal refusal changed state")
			}
		})
	}
}

func TestAssignmentOe4StaleLateDeliveryAfterRestartDoesNoWork(t *testing.T) {
	for _, terminal := range []string{"VOID", "EXPIRE"} {
		t.Run(terminal, func(t *testing.T) {
			f := newLedgerFixture(t)
			f.activate(t)
			if terminal == "EXPIRE" {
				f.now = f.record(t).ValidUntil
			}
			f.change(t, terminal)
			for receiver := 1; receiver <= 2; receiver++ {
				_ = f.s.Close()
				var err error
				f.s, err = NewCanonicalWithClock(f.p, func() time.Time { return f.now })
				if err != nil {
					t.Fatal(err)
				}
				before, _ := f.s.CanonicalSnapshot()
				workCalls := 0
				// Stale preflight after restart in two receiver generations; no transport
				// frame is injected here (authenticated callback is tested separately).
				if err := f.preflight(t); err == nil {
					workCalls++
				}
				if workCalls != 0 {
					t.Fatalf("stale oe4 prompt caused work on receiver generation %d", receiver)
				}
				after, _ := f.s.CanonicalSnapshot()
				if !reflect.DeepEqual(before, after) {
					t.Fatal("delivery revived terminal state")
				}
			}
		})
	}
}

func TestAssignmentExpiredValidUntilDeniedBeforeTimer(t *testing.T) {
	f := newLedgerFixture(t)
	f.activate(t)
	f.now = f.record(t).ValidUntil
	if f.record(t).State != assignment.Active {
		t.Fatal("fixture must retain stored ACTIVE")
	}
	if err := f.preflight(t); err == nil {
		t.Fatal("deadline equality allowed work")
	}
	if err := f.s.AcceptAssignmentResult(f.coord, f.agent, f.result(t)); err == nil {
		t.Fatal("deadline equality allowed result")
	}
	snapshot, _ := f.s.CanonicalSnapshot()
	status, err := InspectAssignment(snapshot, f.id, f.now)
	if err != nil || status.WorkMayStart {
		t.Fatalf("status=%+v err=%v", status, err)
	}
}

func TestAssignmentUnknownResultAndIdReuseRejected(t *testing.T) {
	f := newLedgerFixture(t)
	f.activate(t)
	claim := f.result(t)
	claim.AssignmentID = "unknown-id"
	if err := f.s.AcceptAssignmentResult(f.coord, f.agent, claim); !errors.Is(err, ErrAssignmentUnknown) {
		t.Fatalf("unknown result error=%v", err)
	}
	for _, terminal := range []bool{false, true} {
		if terminal {
			f.change(t, "VOID")
		}
		if err := f.s.OfferAssignment(f.coord, f.offer(), "reused-id"); err == nil {
			t.Fatal("assignmentId reused")
		}
	}
}

func TestAssignmentDuplicateResultAcceptedOnlyOnceAtomically(t *testing.T) {
	f := newLedgerFixture(t)
	f.activate(t)
	claim := f.result(t)
	before, _ := f.s.CanonicalSnapshot()
	var successes atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if f.s.AcceptAssignmentResult(f.coord, f.agent, claim) == nil {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatalf("accepted %d duplicate results", successes.Load())
	}
	after, _ := f.s.CanonicalSnapshot()
	if after.Revision != before.Revision+1 || len(after.AssignmentLedger.Events) != len(before.AssignmentLedger.Events)+1 {
		t.Fatal("result did not commit once")
	}
	if after.AssignmentLedger.Records[f.id].State != assignment.Completed {
		t.Fatal("missing completion")
	}
}

func TestAssignmentCoordinatorLossAndVerifiedHandoff(t *testing.T) {
	f := newLedgerFixture(t)
	f.activate(t)
	original := f.record(t)
	observer := registerAssignmentLossObserver(t, f)
	if err := f.s.ObserveAssignmentCoordinatorLoss(observer, f.coord, "supervisor-observed-loss"); err != nil {
		t.Fatal(err)
	}
	if err := f.preflight(t); err == nil {
		t.Fatal("dead coordinator without handoff allowed work")
	}
	if err := f.s.AcceptAssignmentResult(f.coord, f.agent, f.result(t)); err == nil {
		t.Fatal("dead coordinator allowed result")
	}
	next := assignment.Principal{SessionID: "claude:coord-2", AgentID: "coordinator", Generation: 1}
	if err := f.s.RegisterAgentSession(next.SessionID, next.AgentID, ed25519.PublicKey(strings.Repeat("c", 32))); err != nil {
		t.Fatal(err)
	}
	before, _ := f.s.CanonicalSnapshot()
	if err := f.s.HandoffAssignment(f.coord, next, f.id, f.record(t).Revision, ""); err == nil {
		t.Fatal("handoff without evidence accepted")
	}
	after, _ := f.s.CanonicalSnapshot()
	if !reflect.DeepEqual(before, after) {
		t.Fatal("unverified handoff changed state")
	}
	if err := f.s.HandoffAssignment(f.coord, next, f.id, f.record(t).Revision, "owner-verified-recovery-handoff"); err != nil {
		t.Fatal(err)
	}
	r := f.record(t)
	if r.Issuer != original.Issuer || !r.ValidUntil.Equal(original.ValidUntil) || r.CurrentCoordinator != next.SessionID {
		t.Fatal("handoff changed issuer/clock or failed to replace coordinator")
	}
	if err := f.preflight(t); err == nil {
		t.Fatal("old currentCoordinator accepted after handoff")
	}
	f.coord = next
	if err := f.preflight(t); err != nil {
		t.Fatal(err)
	}
	if err := f.s.AcceptAssignmentResult(next, f.agent, f.result(t)); err != nil {
		t.Fatal(err)
	}
}

func TestAssignmentExactIdentityGenerationAndSubject(t *testing.T) {
	for _, field := range []string{"subject", "assignee", "session", "agent", "role", "generation", "sender", "coordinator"} {
		t.Run(field, func(t *testing.T) {
			f := newLedgerFixture(t)
			f.activate(t)
			claim := f.result(t)
			sender, coord := f.agent, f.coord
			switch field {
			case "subject":
				claim.SubjectSHA = strings.Repeat("b", 40)
			case "assignee":
				claim.Assignee = "wrong-target"
			case "session":
				claim.SessionID = "wrong-session"
			case "agent":
				claim.StableAgentID = "other-agent"
			case "role":
				claim.Role = "REVIEWER"
			case "generation":
				claim.CoordinatorGeneration++
			case "sender":
				sender.Generation++
			case "coordinator":
				coord.SessionID = "dead-original-issuer"
			}
			before, _ := f.s.CanonicalSnapshot()
			if err := f.s.AcceptAssignmentResult(coord, sender, claim); err == nil {
				t.Fatalf("result with wrong %s accepted", field)
			}
			after, _ := f.s.CanonicalSnapshot()
			if !reflect.DeepEqual(before, after) {
				t.Fatal("rejected result changed state")
			}
		})
	}
}

func TestAssignmentSessionRotationBlocksWorkAndResult(t *testing.T) {
	f := newLedgerFixture(t)
	f.activate(t)
	if err := f.s.RotateAgentSessionKey(f.coord.SessionID, ed25519.PublicKey(strings.Repeat("z", 32))); err != nil {
		t.Fatal(err)
	}
	if err := f.preflight(t); err == nil {
		t.Fatal("stale coordinator generation allowed work")
	}
	if err := f.s.AcceptAssignmentResult(f.coord, f.agent, f.result(t)); err == nil {
		t.Fatal("stale coordinator generation accepted result")
	}
}

func TestAssignmentFailedCommitPublishesNeitherResultNorCompletion(t *testing.T) {
	f := newLedgerFixture(t)
	f.activate(t)
	before, _ := f.s.CanonicalSnapshot()
	f.p.fail = true
	if err := f.s.AcceptAssignmentResult(f.coord, f.agent, f.result(t)); !errors.Is(err, ErrPersistence) {
		t.Fatalf("err=%v", err)
	}
	after, _ := f.s.CanonicalSnapshot()
	if !reflect.DeepEqual(before, after) {
		t.Fatal("failed save published assignment result")
	}
	if err := f.s.AcceptAssignmentResult(f.coord, f.agent, f.result(t)); err == nil {
		t.Fatal("failed store permitted further writes")
	}
}

func TestAssignmentOldSnapshotCompatibleAndHistoryAppendOnly(t *testing.T) {
	old := EmptyCanonicalState()
	raw, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "assignment_ledger") {
		t.Fatal("legacy state unexpectedly has ledger")
	}
	var loaded CanonicalState
	if err := json.Unmarshal(raw, &loaded); err != nil {
		t.Fatal(err)
	}
	if err := ValidateCanonicalState(loaded); err != nil {
		t.Fatal(err)
	}
	f := newLedgerFixture(t)
	before, _ := f.s.CanonicalSnapshot()
	for _, mutate := range []func(*CanonicalState){func(s *CanonicalState) { s.AssignmentLedger = nil }, func(s *CanonicalState) { delete(s.AssignmentLedger.Records, f.id) }, func(s *CanonicalState) { s.AssignmentLedger.Events[0].Record.SubjectSHA = strings.Repeat("b", 40) }} {
		if err := f.s.UpdateCanonical(func(s *CanonicalState) error { mutate(s); return nil }); err == nil {
			t.Fatal("ledger removal/history rewrite accepted")
		}
	}
	after, _ := f.s.CanonicalSnapshot()
	if !reflect.DeepEqual(before, after) {
		t.Fatal("invalid history changed snapshot")
	}
}

func TestAssignmentTransportCommandCannotChangeAuthority(t *testing.T) {
	p := &canonicalMemory{state: taskStateWithRun()}
	clock := &canonicalTestClock{now: time.Unix(5000, 0).UTC()}
	s, err := NewCanonicalWithClock(p, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	}()
	before, _ := s.CanonicalSnapshot()
	frame := validAgentCommandFrame(before, "transport-message")
	_, _, err = s.ApplyAgentCommand(frame, taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV"), 2, func(state *CanonicalState, run fsm.Run) (InboxReceipt, error) {
		state.AssignmentLedger = assignment.Empty() // even a valid empty authority cutover is forbidden from transport
		return applyTestAgentCommand(clock, "transport-message")(state, run)
	})
	if !errors.Is(err, ErrAgentCommandTransitionForbidden) {
		t.Fatalf("transport changed assignment authority: %v", err)
	}
	after, _ := s.CanonicalSnapshot()
	if !reflect.DeepEqual(before, after) {
		t.Fatal("transport callback changed canonical state")
	}
}

func TestAssignmentTypedResultGateAtomicWithTask(t *testing.T) {
	p := &canonicalMemory{state: resultState(roles.Reviewer)}
	now := time.Unix(500, 0).UTC()
	s, err := NewCanonicalWithClock(p, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	}()
	coord := assignment.Principal{SessionID: "coord:authority", AgentID: "coordinator", Generation: 1}
	if err := s.RegisterAgentSession(coord.SessionID, coord.AgentID, ed25519.PublicKey(strings.Repeat("c", 32))); err != nil {
		t.Fatal(err)
	}
	state, _ := s.CanonicalSnapshot()
	run := state.Runs["run-review"]
	r := assignment.Record{AssignmentID: "typed-review-assignment", State: assignment.Offered, Issuer: coord.SessionID, IssuerAgentID: coord.AgentID, IssuerGeneration: 1, CurrentCoordinator: coord.SessionID, CoordinatorAgentID: coord.AgentID, CoordinatorGeneration: 1, HandoffEvidence: []assignment.Handoff{}, Assignee: run.OwnerSession, SessionID: run.OwnerSession, StableAgentID: run.RoleSelection.AgentID, AssigneeGeneration: run.AgentSessionGeneration, Role: "REVIEWER", SubjectSHA: run.TargetSHA, IssuedAt: now, ValidUntil: now.Add(time.Hour), AdmissionBasis: assignment.AdmissionBasis{Kind: "POOL", Reference: "admission", TargetSessionID: run.OwnerSession, TargetAgentID: run.RoleSelection.AgentID, Role: "REVIEWER", SubjectSHA: run.TargetSHA}, VisibilityEvidence: assignment.VisibilityEvidence{Reference: "visible", SessionID: run.OwnerSession, ObservedAt: now}, Revision: 1, UpdatedAt: now, RunBinding: &assignment.RunBinding{TaskID: string(resultTaskID), RunID: run.RunID, Fence: run.Fence, Attempt: run.Attempt}}
	if err := s.OfferAssignment(coord, r, "offer"); err != nil {
		t.Fatal(err)
	}
	result := resultWithEvidence(task.ReviewResult{Verdict: task.ReviewApproved, Summary: "reviewed"}, p.state)
	if _, _, err := s.RecordReviewResult("early", resultTaskID, run.RunID, 3, result, now, coord); err == nil {
		t.Fatal("typed result accepted before ACTIVE")
	}
	agent := r.Target()
	if err := s.ChangeAssignment(agent, r.AssignmentID, 1, "ACK", "ack"); err != nil {
		t.Fatal(err)
	}
	if err := s.ChangeAssignment(coord, r.AssignmentID, 2, "ACTIVATE", "active"); err != nil {
		t.Fatal(err)
	}
	before, _ := s.CanonicalSnapshot()
	if _, _, err := s.RecordReviewResult("typed-result", resultTaskID, run.RunID, 3, result, now, coord); err != nil {
		t.Fatal(err)
	}
	after, _ := s.CanonicalSnapshot()
	if after.Revision != before.Revision+1 || after.AssignmentLedger.Records[r.AssignmentID].State != assignment.Completed || after.Tasks[resultTaskID].Revision != 4 {
		t.Fatal("typed result and ledger not atomic")
	}
	if _, _, err := s.RecordReviewResult("typed-result", resultTaskID, run.RunID, 3, result, now, coord); err == nil {
		t.Fatal("typed duplicate bypassed assignment gate through Inbox replay")
	}
}

// LEDGER-ROLLBACK-01: restart the ledger-aware service in frozen mode. Existing
// results and non-ledger service functions remain available; no new offer.
func TestAssignmentLedgerRollbackFrozenMode(t *testing.T) {
	f := newLedgerFixture(t)
	f.activate(t)
	terminalID := "terminal-assignment"
	offered := f.offer()
	offered.AssignmentID = terminalID
	if err := f.s.OfferAssignment(f.coord, offered, "terminal-offer"); err != nil {
		t.Fatal(err)
	}
	if err := f.s.ChangeAssignment(f.coord, terminalID, 1, "VOID", "cancelled"); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	f.s, err = NewCanonicalWithClock(f.p, func() time.Time { return f.now })
	if err != nil {
		t.Fatal(err)
	}
	f.s.FreezeAssignmentLedger()
	state, _ := f.s.CanonicalSnapshot()
	status, err := InspectAssignment(state, f.id, f.now)
	if err != nil || !status.WorkMayStart {
		t.Fatalf("frozen existing status=%+v err=%v", status, err)
	}
	terminal, err := InspectAssignment(state, terminalID, f.now)
	if err != nil || terminal.State != assignment.Void || terminal.WorkMayStart {
		t.Fatal("frozen launch revived terminal assignment")
	}
	newOffer := f.offer()
	newOffer.AssignmentID = "new-after-freeze"
	before, _ := f.s.CanonicalSnapshot()
	if err := f.s.OfferAssignment(f.coord, newOffer, "new-offer"); !errors.Is(err, ErrLedgerFrozen) {
		t.Fatalf("frozen new OFFER error=%v", err)
	}
	if err := f.s.UpdateCanonical(func(s *CanonicalState) error {
		return s.AssignmentLedger.Append(assignment.Event{AssignmentID: newOffer.AssignmentID, Revision: 1, Kind: "OFFER", Actor: f.coord, EvidenceRef: "bypass-offer", At: f.now, Record: newOffer})
	}); !errors.Is(err, ErrLedgerFrozen) {
		t.Fatalf("generic commit bypassed freeze: %v", err)
	}
	after, _ := f.s.CanonicalSnapshot()
	if !reflect.DeepEqual(before, after) {
		t.Fatal("frozen rejection changed ledger data")
	}
	if err := f.preflight(t); err != nil {
		t.Fatal(err)
	}
	if err := f.s.AcceptAssignmentResult(f.coord, f.agent, f.result(t)); err != nil {
		t.Fatalf("frozen existing result unavailable: %v", err)
	}
	if f.record(t).State != assignment.Completed {
		t.Fatal("frozen result not completed")
	}
	if err := f.s.ChangeAssignment(f.coord, terminalID, 2, "ACTIVATE", "revive"); err == nil {
		t.Fatal("frozen terminal safety disabled")
	}
	// A non-ledger existing function still works in the same service/store.
	if err := f.s.RegisterAgentSession("codex:diagnostic-session", "diagnostic", ed25519.PublicKey(strings.Repeat("z", 32))); err != nil {
		t.Fatalf("non-ledger service unavailable: %v", err)
	}
}
