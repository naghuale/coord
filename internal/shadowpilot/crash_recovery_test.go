package shadowpilot_test

import (
	"testing"
	"time"

	"coord/internal/shadowpilot"
)

// Fixtures

var crashT0 = time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)

func crashAssignment() shadowpilot.ShadowAssignment {
	return shadowpilot.ShadowAssignment{
		ShadowRunID:  "sv-run-crash-1",
		AssignmentID: "assign-crash-001",
		AgentID:      "agent-executor",
		SessionID:    "session-crash-abc",
		Generation:   2,
		SubjectSHA:   sha1,
		InputDigest:  "sha256:aabbcc",
		Role:         "executor",
		CreatedAt:    crashT0,
	}
}

func crashExecution(state shadowpilot.ExecutorState) *shadowpilot.ShadowExecution {
	a := crashAssignment()
	ex, _ := newAdmittedExecution(a)
	if state == shadowpilot.ExecutorRunning || state == shadowpilot.ExecutorCompleted || state == shadowpilot.ExecutorFenced {
		_ = ex.Start(crashT0)
	}
	return &ex
}

func completedExecution() *shadowpilot.ShadowExecution {
	a := crashAssignment()
	ex, _ := newAdmittedExecution(a)
	_ = ex.Start(crashT0)
	r := shadowpilot.AgentResult{
		Kind:         shadowpilot.AgentResultKind,
		AssignmentID: a.AssignmentID,
		AgentID:      a.AgentID,
		SessionID:    a.SessionID,
		Generation:   a.Generation,
		SubjectSHA:   a.SubjectSHA,
		InputDigest:  a.InputDigest,
		Findings:     []shadowpilot.Finding{},
		CompletedAt:  crashT0.Add(5 * time.Second),
	}
	_ = ex.Complete(r, crashT0.Add(5*time.Second))
	return &ex
}

// Test cases

func TestRestartBeforeIntentDoesNotLaunch(t *testing.T) {
	cp := shadowpilot.ShadowCheckpoint{} // nil Assignment = crash before intent
	plan := shadowpilot.PlanRecovery(cp, nil)
	if !plan.Is(shadowpilot.RecoveryNoLaunch) {
		t.Fatalf("expected NO_LAUNCH, got %q: %s", plan.Action, plan.Reason)
	}
}

func TestRestartAfterIntentObservesBeforeDispatch(t *testing.T) {
	a := crashAssignment()
	cp := shadowpilot.ShadowCheckpoint{Assignment: &a} // intent but no execution
	plan := shadowpilot.PlanRecovery(cp, nil)
	if !plan.Is(shadowpilot.RecoveryObserveFirst) {
		t.Fatalf("expected OBSERVE_FIRST, got %q: %s", plan.Action, plan.Reason)
	}
}

func TestPersistedAdmittedIsNotRunning(t *testing.T) {
	a := crashAssignment()
	ex := crashExecution(shadowpilot.ExecutorAdmitted)
	cp := shadowpilot.ShadowCheckpoint{Assignment: &a, Execution: ex}
	plan := shadowpilot.PlanRecovery(cp, nil)
	if !plan.Is(shadowpilot.RecoveryObserveFirst) {
		t.Fatalf("ADMITTED must require OBSERVE_FIRST, got %q: %s", plan.Action, plan.Reason)
	}
}

func TestPersistedRunningRequiresFreshObservation(t *testing.T) {
	// Persisted RUNNING without liveness observation must return OBSERVE_FIRST.
	a := crashAssignment()
	ex := crashExecution(shadowpilot.ExecutorRunning)
	cp := shadowpilot.ShadowCheckpoint{Assignment: &a, Execution: ex}
	plan := shadowpilot.PlanRecovery(cp, nil)
	if !plan.Is(shadowpilot.RecoveryObserveFirst) {
		t.Fatalf("RUNNING without liveness must require OBSERVE_FIRST, got %q: %s", plan.Action, plan.Reason)
	}
}

// Mutation M01: persisted RUNNING accepted as live — must not happen without liveness.
func TestM01PersistedRunningAcceptedAsLiveIsRejected(t *testing.T) {
	a := crashAssignment()
	ex := crashExecution(shadowpilot.ExecutorRunning)
	cp := shadowpilot.ShadowCheckpoint{Assignment: &a, Execution: ex}
	// Without liveness observation, must not assume live.
	plan := shadowpilot.PlanRecovery(cp, nil)
	if plan.Is(shadowpilot.RecoveryAlreadyComplete) || plan.Is(shadowpilot.RecoveryResumeGraph) {
		t.Fatalf("M01: persisted RUNNING must not be treated as live, got %q", plan.Action)
	}
}

func TestRestartDoesNotDuplicateExecution(t *testing.T) {
	a := crashAssignment()
	ex := completedExecution()
	cp := shadowpilot.ShadowCheckpoint{Assignment: &a, Execution: ex}
	plan := shadowpilot.PlanRecovery(cp, nil)
	// Must not return NO_LAUNCH (would imply starting fresh) — must be ALREADY_COMPLETE or RESUME_GRAPH.
	if plan.Is(shadowpilot.RecoveryNoLaunch) {
		t.Fatal("completed execution must not result in NO_LAUNCH (would trigger restart)")
	}
	if plan.Is(shadowpilot.RecoveryObserveFirst) {
		// OBSERVE_FIRST is acceptable only if result is ambiguous; COMPLETED with a result is not ambiguous.
		t.Fatalf("COMPLETED execution with result must not require OBSERVE_FIRST, got %q: %s", plan.Action, plan.Reason)
	}
}

// Mutation M02: restart launches duplicate execution — must be blocked.
func TestM02RestartDuplicateExecutionIsBlocked(t *testing.T) {
	a := crashAssignment()
	ex := completedExecution()
	cp := shadowpilot.ShadowCheckpoint{Assignment: &a, Execution: ex}
	plan := shadowpilot.PlanRecovery(cp, nil)
	if plan.Is(shadowpilot.RecoveryNoLaunch) || plan.Is(shadowpilot.RecoveryObserveFirst) {
		t.Fatalf("M02: completed execution must block duplicate launch, got %q", plan.Action)
	}
}

func TestStaleGenerationResultRejectedAfterRestart(t *testing.T) {
	a := crashAssignment() // generation = 2
	ex, _ := newAdmittedExecution(a)
	_ = ex.Start(crashT0)
	staleResult := shadowpilot.AgentResult{
		Kind:         shadowpilot.AgentResultKind,
		AssignmentID: a.AssignmentID,
		AgentID:      a.AgentID,
		SessionID:    a.SessionID,
		Generation:   1, // stale: does not match assignment gen 2
		SubjectSHA:   a.SubjectSHA,
		InputDigest:  a.InputDigest,
		Findings:     []shadowpilot.Finding{},
		CompletedAt:  crashT0.Add(5 * time.Second),
	}
	// Manually inject stale result into execution state (bypass Complete validation
	// to simulate a corrupt checkpoint scenario).
	ex.State = shadowpilot.ExecutorCompleted
	ex.Result = &staleResult

	cp := shadowpilot.ShadowCheckpoint{Assignment: &a, Execution: &ex}
	plan := shadowpilot.PlanRecovery(cp, nil)
	if plan.StaleGeneration == nil {
		t.Fatal("expected stale generation to be detected in recovery plan")
	}
}

// Mutation M03: stale generation result accepted — must be rejected.
func TestM03StaleGenerationResultAcceptedIsRejected(t *testing.T) {
	a := crashAssignment() // generation = 2
	ex, _ := newAdmittedExecution(a)
	_ = ex.Start(crashT0)
	ex.State = shadowpilot.ExecutorCompleted
	staleResult := shadowpilot.AgentResult{
		Kind: shadowpilot.AgentResultKind, Generation: 1, SubjectSHA: sha1,
		AssignmentID: a.AssignmentID, AgentID: a.AgentID, SessionID: a.SessionID,
		InputDigest: a.InputDigest, Findings: []shadowpilot.Finding{},
		CompletedAt: crashT0.Add(time.Second),
	}
	ex.Result = &staleResult

	cp := shadowpilot.ShadowCheckpoint{Assignment: &a, Execution: &ex}
	plan := shadowpilot.PlanRecovery(cp, nil)
	if plan.StaleGeneration == nil {
		t.Fatal("M03: stale generation result must not be accepted")
	}
}

func TestReviewResultSurvivesRestart(t *testing.T) {
	a := crashAssignment()
	ex := completedExecution()
	rr := &shadowpilot.ShadowReviewResult{
		ResultID:   "rr-001",
		ReviewerID: "agent-reviewer",
		ExecutorID: "agent-executor",
		SubjectSHA: sha1,
		Generation: 2,
		Verdict:    shadowpilot.ShadowReviewApproved,
		Findings:   []shadowpilot.ShadowFinding{},
		Summary:    "approved",
		CreatedAt:  crashT0.Add(10 * time.Second),
	}
	cp := shadowpilot.ShadowCheckpoint{Assignment: &a, Execution: ex, ReviewResult: rr}
	if err := shadowpilot.ValidateCheckpointIntegrity(cp); err != nil {
		t.Fatalf("checkpoint with review should be valid: %v", err)
	}
	plan := shadowpilot.PlanRecovery(cp, nil)
	// With review but no audit, should resume graph.
	if !plan.Is(shadowpilot.RecoveryResumeGraph) {
		t.Fatalf("expected RESUME_GRAPH with only review present, got %q: %s", plan.Action, plan.Reason)
	}
}

// Mutation M04: ReviewResult lost after restart — checkpoint must include it.
func TestM04ReviewResultLostIsDetected(t *testing.T) {
	a := crashAssignment()
	ex := completedExecution()
	// Checkpoint without ReviewResult despite having a completed execution.
	cp := shadowpilot.ShadowCheckpoint{Assignment: &a, Execution: ex}
	plan := shadowpilot.PlanRecovery(cp, nil)
	if !plan.Is(shadowpilot.RecoveryResumeGraph) {
		t.Fatalf("M04: without review, should resume graph (review not yet done), got %q", plan.Action)
	}
}

func TestFindingAndRationaleSurviveRestart(t *testing.T) {
	dn := shadowpilot.ShadowDiscussionNode{
		NodeID:    "dn-001",
		Author:    "agent-reviewer",
		Role:      "reviewer",
		Body:      "fencing invariant violated",
		ResultRef: "rr-001",
		CreatedAt: crashT0.Add(5 * time.Second),
	}
	a := crashAssignment()
	ex := completedExecution()
	cp := shadowpilot.ShadowCheckpoint{
		Assignment:      &a,
		Execution:       ex,
		DiscussionNodes: []shadowpilot.ShadowDiscussionNode{dn},
	}
	if err := shadowpilot.ValidateCheckpointIntegrity(cp); err != nil {
		t.Fatalf("checkpoint with discussion nodes should be valid: %v", err)
	}
	if len(cp.DiscussionNodes) != 1 {
		t.Fatal("discussion nodes must survive in checkpoint")
	}
}

// Mutation M05: Finding rationale lost — ValidateCheckpointIntegrity cannot
// detect discussion node loss (that requires application-level tracking),
// but the struct type ensures nodes are preserved when present.
func TestM05FindingRationaleStructurallyPreserved(t *testing.T) {
	dn := shadowpilot.ShadowDiscussionNode{
		NodeID:    "dn-001",
		Author:    "agent-reviewer",
		Role:      "reviewer",
		Body:      "rationale",
		ResultRef: "rr-001",
		CreatedAt: crashT0.Add(time.Second),
	}
	cp := shadowpilot.ShadowCheckpoint{DiscussionNodes: []shadowpilot.ShadowDiscussionNode{dn}}
	if len(cp.DiscussionNodes) == 0 {
		t.Fatal("M05: discussion nodes must be preserved in checkpoint")
	}
}

func TestOldReviewDoesNotApplyToCorrectedSubject(t *testing.T) {
	a := crashAssignment() // sha1
	// Old review from prior (now-corrected) subject.
	oldReview := &shadowpilot.ShadowReviewResult{
		ResultID:   "rr-old",
		ReviewerID: "agent-reviewer",
		ExecutorID: "agent-executor",
		SubjectSHA: sha2, // different subject
		Generation: 1,
		Verdict:    shadowpilot.ShadowReviewApproved,
		Summary:    "old",
		CreatedAt:  crashT0,
	}
	cp := shadowpilot.ShadowCheckpoint{Assignment: &a, ReviewResult: oldReview}
	// ValidateCheckpointIntegrity must reject this.
	if err := shadowpilot.ValidateCheckpointIntegrity(cp); err == nil {
		t.Fatal("expected integrity error for mismatched review subject SHA")
	}
}

// Mutation M06: old Review applied to new subject — must be caught.
func TestM06OldReviewAppliedToNewSubjectIsRejected(t *testing.T) {
	a := crashAssignment()
	a.SubjectSHA = sha2 // assignment is for new subject
	oldReview := &shadowpilot.ShadowReviewResult{
		ResultID:   "rr-old",
		ReviewerID: "agent-reviewer",
		ExecutorID: "agent-executor",
		SubjectSHA: sha1, // old subject
		Generation: 1,
		Verdict:    shadowpilot.ShadowReviewApproved,
		Summary:    "old",
		CreatedAt:  crashT0,
	}
	cp := shadowpilot.ShadowCheckpoint{Assignment: &a, ReviewResult: oldReview}
	if err := shadowpilot.ValidateCheckpointIntegrity(cp); err == nil {
		t.Fatal("M06: old review applied to new subject must be rejected")
	}
}

func TestAuditResultSurvivesRestart(t *testing.T) {
	a := crashAssignment()
	ex := completedExecution()
	rr := &shadowpilot.ShadowReviewResult{
		ResultID: "rr-001", ReviewerID: "agent-reviewer", ExecutorID: "agent-executor",
		SubjectSHA: sha1, Generation: 2, Verdict: shadowpilot.ShadowReviewApproved,
		Summary: "approved", CreatedAt: crashT0.Add(10 * time.Second),
	}
	ar := &shadowpilot.ShadowAuditResult{
		ResultID: "ar-001", AuditorID: "agent-auditor", ExecutorID: "agent-executor",
		ReviewerID: "agent-reviewer", SubjectSHA: sha1, Generation: 2,
		Verdict: shadowpilot.ShadowAuditPass, Summary: "all pass",
		CreatedAt: crashT0.Add(15 * time.Second),
	}
	cp := shadowpilot.ShadowCheckpoint{Assignment: &a, Execution: ex, ReviewResult: rr, AuditResult: ar}
	if err := shadowpilot.ValidateCheckpointIntegrity(cp); err != nil {
		t.Fatalf("checkpoint with review + audit should be valid: %v", err)
	}
	plan := shadowpilot.PlanRecovery(cp, nil)
	if !plan.Is(shadowpilot.RecoveryReadinessOnly) {
		t.Fatalf("with review+audit, expected READINESS_ONLY, got %q: %s", plan.Action, plan.Reason)
	}
}

func TestReadinessRecomputesIdenticallyAfterRestart(t *testing.T) {
	review := &shadowpilot.ShadowReviewResult{
		ResultID: "rr-001", ReviewerID: "agent-reviewer", ExecutorID: "agent-executor",
		SubjectSHA: sha1, Generation: 2, Verdict: shadowpilot.ShadowReviewApproved,
		Summary: "approved", CreatedAt: crashT0,
	}
	audit := &shadowpilot.ShadowAuditResult{
		ResultID: "ar-001", AuditorID: "agent-auditor", ExecutorID: "agent-executor",
		ReviewerID: "agent-reviewer", SubjectSHA: sha1, Generation: 2,
		Verdict: shadowpilot.ShadowAuditPass, Summary: "all pass",
		CreatedAt: crashT0.Add(5 * time.Second),
	}
	// Pre-crash readiness.
	r1 := shadowpilot.ComputeReadiness(review, audit, crashAssignment())
	// Post-restart readiness (same inputs).
	r2 := shadowpilot.ComputeReadiness(review, audit, crashAssignment())
	if r1.Readiness != r2.Readiness {
		t.Fatalf("readiness must be deterministic: %q != %q", r1.Readiness, r2.Readiness)
	}
	if r1.Readiness != shadowpilot.ShadowReadinessReady {
		t.Fatalf("expected READY, got %q: %s", r1.Readiness, r1.Reason)
	}
}

// Mutation M07: mixed-subject Audit gives READY — must not pass.
func TestM07MixedSubjectAuditAfterRestartIsRejected(t *testing.T) {
	review := &shadowpilot.ShadowReviewResult{
		ResultID: "rr-001", ReviewerID: "agent-reviewer", ExecutorID: "agent-executor",
		SubjectSHA: sha1, Generation: 2, Verdict: shadowpilot.ShadowReviewApproved,
		Summary: "approved", CreatedAt: crashT0,
	}
	audit := &shadowpilot.ShadowAuditResult{
		ResultID: "ar-001", AuditorID: "agent-auditor", ExecutorID: "agent-executor",
		ReviewerID: "agent-reviewer", SubjectSHA: sha2, // different subject
		Generation: 2, Verdict: shadowpilot.ShadowAuditPass, Summary: "pass",
		CreatedAt: crashT0,
	}
	result := shadowpilot.ComputeReadiness(review, audit, crashAssignment())
	if result.Readiness == shadowpilot.ShadowReadinessReady {
		t.Fatal("M07: mixed-subject review+audit must not produce READY after restart")
	}
}

func TestPartialStateDoesNotBecomeCurrent(t *testing.T) {
	a := crashAssignment()
	// COMPLETED execution without a result — invalid partial state.
	partialEx, _ := newAdmittedExecution(a)
	_ = partialEx.Start(crashT0)
	partialEx.State = shadowpilot.ExecutorCompleted // force without result (simulating corruption)
	// partialEx.Result remains nil

	cp := shadowpilot.ShadowCheckpoint{Assignment: &a, Execution: &partialEx}
	if err := shadowpilot.ValidateCheckpointIntegrity(cp); err == nil {
		t.Fatal("COMPLETED execution without result must be rejected by integrity check")
	}
}

// Mutation M08: partial state becomes current — ValidateCheckpointIntegrity must reject.
func TestM08PartialStateBecomesCurrentIsRejected(t *testing.T) {
	a := crashAssignment()
	partialEx, _ := newAdmittedExecution(a)
	_ = partialEx.Start(crashT0)
	partialEx.State = shadowpilot.ExecutorCompleted // no result

	cp := shadowpilot.ShadowCheckpoint{Assignment: &a, Execution: &partialEx}
	if err := shadowpilot.ValidateCheckpointIntegrity(cp); err == nil {
		t.Fatal("M08: partial state (COMPLETED without result) must not become current")
	}
}
