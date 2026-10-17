package shadowpilot_test

import (
	"errors"
	"testing"
	"time"

	"coord/internal/shadowpilot"
)

var t0 = time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)

func validAssignment() shadowpilot.ShadowAssignment {
	return shadowpilot.ShadowAssignment{
		ShadowRunID:  "sv-run-exec-1",
		AssignmentID: "assign-exec-001",
		AgentID:      "agent-executor",
		SessionID:    "session-exec-abc",
		Generation:   1,
		SubjectSHA:   "b4334cd2438837140bc112b97cba49eb1a478f08",
		InputDigest:  "sha256:abc123",
		Role:         "executor",
		CreatedAt:    t0,
	}
}

func TestShadowAssignmentValidateAcceptsValid(t *testing.T) {
	if err := validAssignment().Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestShadowAssignmentValidateRejectsEmptyRole(t *testing.T) {
	a := validAssignment()
	a.Role = ""
	if err := a.Validate(); err == nil {
		t.Fatal("expected error for empty role")
	}
}

func TestShadowAssignmentValidateRejectsZeroGeneration(t *testing.T) {
	a := validAssignment()
	a.Generation = 0
	if err := a.Validate(); err == nil {
		t.Fatal("expected error for generation=0")
	}
}

// Admission tests

func TestAdmitAcceptsMatchingSubjectNoReviewerConflict(t *testing.T) {
	a := validAssignment()
	r := shadowpilot.Admit(a, a.SubjectSHA, []string{"agent-reviewer-1"})
	if r.Outcome != shadowpilot.AdmissionAdmitted {
		t.Fatalf("expected ADMITTED, got %q: %v", r.Outcome, r.Violation)
	}
}

func TestAdmitRejectsSubjectMismatch(t *testing.T) {
	a := validAssignment()
	r := shadowpilot.Admit(a, "aacc4ee0d713846a537b623db2ee2dba7114e9e4", nil)
	if r.Outcome != shadowpilot.AdmissionRejected {
		t.Fatal("expected REJECTED for subject mismatch")
	}
	if r.Violation == nil || r.Violation.Condition != shadowpilot.StopExactSubjectMismatch {
		t.Fatalf("expected StopExactSubjectMismatch, got %v", r.Violation)
	}
}

func TestAdmitRejectsExecutorWhoIsAlsoReviewer(t *testing.T) {
	a := validAssignment()
	r := shadowpilot.Admit(a, a.SubjectSHA, []string{"agent-executor"})
	if r.Outcome != shadowpilot.AdmissionRejected {
		t.Fatal("expected REJECTED for reviewer_equals_executor")
	}
	if r.Violation == nil || r.Violation.Condition != shadowpilot.StopReviewerEqualsExecutor {
		t.Fatalf("expected StopReviewerEqualsExecutor, got %v", r.Violation)
	}
}

// ShadowExecution lifecycle tests

func TestNewShadowExecutionIsAdmitted(t *testing.T) {
	ex, err := shadowpilot.NewShadowExecution(validAssignment())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ex.State != shadowpilot.ExecutorAdmitted {
		t.Fatalf("expected ADMITTED, got %q", ex.State)
	}
}

func TestStartTransitionsToRunning(t *testing.T) {
	ex, _ := shadowpilot.NewShadowExecution(validAssignment())
	if err := ex.Start(t0); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if ex.State != shadowpilot.ExecutorRunning {
		t.Fatalf("expected RUNNING, got %q", ex.State)
	}
}

func TestStartRejectsDoubleStart(t *testing.T) {
	ex, _ := shadowpilot.NewShadowExecution(validAssignment())
	_ = ex.Start(t0)
	if err := ex.Start(t0); err == nil {
		t.Fatal("expected error for double Start")
	}
}

func TestRecordHeartbeatIncrements(t *testing.T) {
	ex, _ := shadowpilot.NewShadowExecution(validAssignment())
	_ = ex.Start(t0)
	hb := shadowpilot.ShadowHeartbeat{
		ShadowRunID: "sv-run-exec-1",
		Generation:  1,
		Sequence:    1,
		At:          t0.Add(time.Second),
	}
	if err := ex.RecordHeartbeat(hb); err != nil {
		t.Fatalf("RecordHeartbeat: %v", err)
	}
	if len(ex.Heartbeats) != 1 {
		t.Fatalf("expected 1 heartbeat, got %d", len(ex.Heartbeats))
	}
}

func TestRecordHeartbeatRejectsOutOfOrder(t *testing.T) {
	ex, _ := shadowpilot.NewShadowExecution(validAssignment())
	_ = ex.Start(t0)
	hb := shadowpilot.ShadowHeartbeat{ShadowRunID: "sv-run-exec-1", Generation: 1, Sequence: 5, At: t0.Add(time.Second)}
	_ = ex.RecordHeartbeat(hb)
	hb2 := shadowpilot.ShadowHeartbeat{ShadowRunID: "sv-run-exec-1", Generation: 1, Sequence: 3, At: t0.Add(2 * time.Second)}
	if err := ex.RecordHeartbeat(hb2); err == nil {
		t.Fatal("expected error for out-of-order heartbeat sequence")
	}
}

func TestRecordHeartbeatRejectsWrongGeneration(t *testing.T) {
	ex, _ := shadowpilot.NewShadowExecution(validAssignment())
	_ = ex.Start(t0)
	hb := shadowpilot.ShadowHeartbeat{ShadowRunID: "sv-run-exec-1", Generation: 99, Sequence: 1, At: t0.Add(time.Second)}
	if err := ex.RecordHeartbeat(hb); err == nil {
		t.Fatal("expected error for mismatched generation in heartbeat")
	}
}

func TestRecordHeartbeatRejectsIfNotRunning(t *testing.T) {
	ex, _ := shadowpilot.NewShadowExecution(validAssignment())
	hb := shadowpilot.ShadowHeartbeat{ShadowRunID: "sv-run-exec-1", Generation: 1, Sequence: 1, At: t0.Add(time.Second)}
	if err := ex.RecordHeartbeat(hb); err == nil {
		t.Fatal("expected error: heartbeat before Start")
	}
}

func validAgentResult(a shadowpilot.ShadowAssignment) shadowpilot.AgentResult {
	return shadowpilot.AgentResult{
		Kind:         shadowpilot.AgentResultKind,
		AssignmentID: a.AssignmentID,
		AgentID:      a.AgentID,
		SessionID:    a.SessionID,
		Generation:   a.Generation,
		SubjectSHA:   a.SubjectSHA,
		InputDigest:  a.InputDigest,
		Findings:     []shadowpilot.Finding{},
		CompletedAt:  t0.Add(5 * time.Second),
	}
}

func TestCompleteTransitionsToCompleted(t *testing.T) {
	a := validAssignment()
	ex, _ := shadowpilot.NewShadowExecution(a)
	_ = ex.Start(t0)
	if err := ex.Complete(validAgentResult(a), t0.Add(5*time.Second)); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if ex.State != shadowpilot.ExecutorCompleted {
		t.Fatalf("expected COMPLETED, got %q", ex.State)
	}
	if ex.Result == nil {
		t.Fatal("result must be set after Complete")
	}
}

func TestCompleteRejectsStaleGeneration(t *testing.T) {
	a := validAssignment()
	ex, _ := shadowpilot.NewShadowExecution(a)
	_ = ex.Start(t0)
	r := validAgentResult(a)
	r.Generation = 99
	err := ex.Complete(r, t0.Add(5*time.Second))
	if err == nil {
		t.Fatal("expected StaleGenerationError")
	}
	var sg *shadowpilot.StaleGenerationError
	if !errors.As(err, &sg) {
		t.Fatalf("expected StaleGenerationError, got %T: %v", err, err)
	}
}

func TestCompleteRejectsSubjectMismatch(t *testing.T) {
	a := validAssignment()
	ex, _ := shadowpilot.NewShadowExecution(a)
	_ = ex.Start(t0)
	r := validAgentResult(a)
	r.SubjectSHA = "aacc4ee0d713846a537b623db2ee2dba7114e9e4"
	if err := ex.Complete(r, t0.Add(5*time.Second)); err == nil {
		t.Fatal("expected error for subject SHA mismatch in result")
	}
}

func TestFencedExecutionCannotBeCompleted(t *testing.T) {
	a := validAssignment()
	ex, _ := shadowpilot.NewShadowExecution(a)
	_ = ex.Start(t0)
	_ = ex.Fence(shadowpilot.StopViolation{
		Condition: shadowpilot.StopWriteOutsideShadowStore,
		Detail:    "attempt to write to production path",
	}, t0.Add(time.Second))
	if ex.State != shadowpilot.ExecutorFenced {
		t.Fatalf("expected FENCED, got %q", ex.State)
	}
	if err := ex.Complete(validAgentResult(a), t0.Add(2*time.Second)); err == nil {
		t.Fatal("expected error: fenced execution cannot be completed")
	}
}

func TestFenceCannotOccurAfterComplete(t *testing.T) {
	a := validAssignment()
	ex, _ := shadowpilot.NewShadowExecution(a)
	_ = ex.Start(t0)
	_ = ex.Complete(validAgentResult(a), t0.Add(time.Second))
	if err := ex.Fence(shadowpilot.StopViolation{
		Condition: shadowpilot.StopWriteOutsideShadowStore,
		Detail:    "too late",
	}, t0.Add(2*time.Second)); err == nil {
		t.Fatal("expected error: cannot fence a completed execution")
	}
}

func TestCheckHeartbeatTimeoutFencesStaleExecution(t *testing.T) {
	a := validAssignment()
	ex, _ := shadowpilot.NewShadowExecution(a)
	_ = ex.Start(t0)
	hb := shadowpilot.ShadowHeartbeat{
		ShadowRunID: "sv-run-exec-1",
		Generation:  1,
		Sequence:    1,
		At:          t0.Add(time.Second),
	}
	_ = ex.RecordHeartbeat(hb)
	// advance past timeout
	if err := ex.CheckHeartbeatTimeout(t0.Add(time.Minute), 30*time.Second); err != nil {
		t.Fatalf("CheckHeartbeatTimeout: %v", err)
	}
	if ex.State != shadowpilot.ExecutorFenced {
		t.Fatalf("expected FENCED after stale heartbeat, got %q", ex.State)
	}
}

func TestCheckHeartbeatTimeoutNoOpIfNotRunning(t *testing.T) {
	a := validAssignment()
	ex, _ := shadowpilot.NewShadowExecution(a)
	// still ADMITTED
	if err := ex.CheckHeartbeatTimeout(t0.Add(time.Hour), time.Second); err != nil {
		t.Fatalf("unexpected error on non-running execution: %v", err)
	}
	if ex.State != shadowpilot.ExecutorAdmitted {
		t.Fatalf("state should remain ADMITTED, got %q", ex.State)
	}
}
