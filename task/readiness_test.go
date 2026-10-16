package task

import (
	"coord/taskid"
	"testing"
	"time"
)

func makeTaskID(t *testing.T) taskid.ID {
	t.Helper()
	id, err := taskid.New()
	if err != nil {
		t.Fatalf("taskid.New: %v", err)
	}
	return id
}

func reviewEvent(taskID taskid.ID, seq uint64, resultID string, round uint32, verdict ReviewVerdict) DomainEvent {
	now := time.Now()
	rr := &ReviewResult{
		Verdict:            verdict,
		Findings:           []Finding{},
		Summary:            "review",
		InputArtifactRefs:  []string{},
		OutputArtifactRefs: []string{},
		CreatedAt:          now,
		ResultID:           resultID,
		Round:              round,
	}
	return DomainEvent{
		ID:           EventID(taskID, seq),
		TaskID:       taskID,
		EventSeq:     seq,
		TaskRevision: seq,
		TaskFence:    1,
		Type:         ReviewResultRecorded,
		ReviewResult: rr,
		CreatedAt:    now,
	}
}

func auditEvent(taskID taskid.ID, seq uint64, resultID string, round uint32, verdict AuditVerdict) DomainEvent {
	now := time.Now()
	ar := &AuditResult{
		Verdict:            verdict,
		Findings:           []Finding{},
		Summary:            "audit",
		InputArtifactRefs:  []string{},
		OutputArtifactRefs: []string{},
		CreatedAt:          now,
		ResultID:           resultID,
		Round:              round,
	}
	return DomainEvent{
		ID:           EventID(taskID, seq),
		TaskID:       taskID,
		EventSeq:     seq,
		TaskRevision: seq,
		TaskFence:    1,
		Type:         AuditResultRecorded,
		AuditResult:  ar,
		CreatedAt:    now,
	}
}

func supersedReviewEvent(taskID taskid.ID, seq uint64, supersededID string) DomainEvent {
	now := time.Now()
	return DomainEvent{
		ID:                 EventID(taskID, seq),
		TaskID:             taskID,
		EventSeq:           seq,
		TaskRevision:       seq,
		TaskFence:          1,
		Type:               ReviewResultSuperseded,
		SupersededResultID: supersededID,
		CreatedAt:          now,
	}
}

func supersedAuditEvent(taskID taskid.ID, seq uint64, supersededID string) DomainEvent {
	now := time.Now()
	return DomainEvent{
		ID:                 EventID(taskID, seq),
		TaskID:             taskID,
		EventSeq:           seq,
		TaskRevision:       seq,
		TaskFence:          1,
		Type:               AuditResultSuperseded,
		SupersededResultID: supersededID,
		CreatedAt:          now,
	}
}

// NOT_EVALUATED: no events at all.
func TestReadiness_NoEvents(t *testing.T) {
	id := makeTaskID(t)
	eval := EvaluateReadiness(id, nil)
	if eval.Status != ReadinessNotEvaluated {
		t.Errorf("expected NOT_EVALUATED, got %q", eval.Status)
	}
	if eval.ReviewResultID != "" || eval.AuditResultID != "" {
		t.Error("expected empty ResultIDs")
	}
}

// NOT_EVALUATED: only audit exists, no review.
func TestReadiness_AuditOnlyNoReview(t *testing.T) {
	id := makeTaskID(t)
	events := []DomainEvent{
		auditEvent(id, 1, EventID(id, 1), 1, AuditPass),
	}
	eval := EvaluateReadiness(id, events)
	if eval.Status != ReadinessNotEvaluated {
		t.Errorf("expected NOT_EVALUATED (no review), got %q", eval.Status)
	}
}

// CHANGES_REQUIRED: review CHANGES_REQUIRED.
func TestReadiness_ReviewChangesRequired(t *testing.T) {
	id := makeTaskID(t)
	events := []DomainEvent{
		reviewEvent(id, 1, EventID(id, 1), 1, ReviewChangesRequired),
	}
	eval := EvaluateReadiness(id, events)
	if eval.Status != ReadinessChangesRequired {
		t.Errorf("expected CHANGES_REQUIRED, got %q", eval.Status)
	}
	if eval.ReviewResultID != EventID(id, 1) {
		t.Errorf("unexpected ReviewResultID %q", eval.ReviewResultID)
	}
}

// AUDIT_REQUIRED: review APPROVED but no audit.
func TestReadiness_ReviewApprovedNoAudit(t *testing.T) {
	id := makeTaskID(t)
	events := []DomainEvent{
		reviewEvent(id, 1, EventID(id, 1), 1, ReviewApproved),
	}
	eval := EvaluateReadiness(id, events)
	if eval.Status != ReadinessAuditRequired {
		t.Errorf("expected AUDIT_REQUIRED, got %q", eval.Status)
	}
	if eval.AuditResultID != "" {
		t.Errorf("expected empty AuditResultID, got %q", eval.AuditResultID)
	}
}

// READY: review APPROVED + audit PASS.
func TestReadiness_Ready(t *testing.T) {
	id := makeTaskID(t)
	events := []DomainEvent{
		reviewEvent(id, 1, EventID(id, 1), 1, ReviewApproved),
		auditEvent(id, 2, EventID(id, 2), 1, AuditPass),
	}
	eval := EvaluateReadiness(id, events)
	if eval.Status != ReadinessReady {
		t.Errorf("expected READY, got %q", eval.Status)
	}
	if eval.ReviewResultID == "" || eval.AuditResultID == "" {
		t.Error("expected both ResultIDs populated")
	}
}

// AUDIT_BLOCKED: review APPROVED + audit AUDIT_BLOCKED.
func TestReadiness_AuditBlocked(t *testing.T) {
	id := makeTaskID(t)
	events := []DomainEvent{
		reviewEvent(id, 1, EventID(id, 1), 1, ReviewApproved),
		auditEvent(id, 2, EventID(id, 2), 1, AuditBlocked),
	}
	eval := EvaluateReadiness(id, events)
	if eval.Status != ReadinessAuditBlocked {
		t.Errorf("expected AUDIT_BLOCKED, got %q", eval.Status)
	}
}

// CHANGES_REQUIRED via audit.
func TestReadiness_AuditChangesRequired(t *testing.T) {
	id := makeTaskID(t)
	events := []DomainEvent{
		reviewEvent(id, 1, EventID(id, 1), 1, ReviewApproved),
		auditEvent(id, 2, EventID(id, 2), 1, AuditChangesRequired),
	}
	eval := EvaluateReadiness(id, events)
	if eval.Status != ReadinessChangesRequired {
		t.Errorf("expected CHANGES_REQUIRED (via audit), got %q", eval.Status)
	}
}

// Supersession: READY after review superseded, leaving no valid review → NOT_EVALUATED.
func TestReadiness_ReviewSuperseded_NotEvaluated(t *testing.T) {
	id := makeTaskID(t)
	r1ID := EventID(id, 1)
	events := []DomainEvent{
		reviewEvent(id, 1, r1ID, 1, ReviewApproved),
		auditEvent(id, 2, EventID(id, 2), 1, AuditPass),
		supersedReviewEvent(id, 3, r1ID),
	}
	eval := EvaluateReadiness(id, events)
	if eval.Status != ReadinessNotEvaluated {
		t.Errorf("expected NOT_EVALUATED after review superseded, got %q", eval.Status)
	}
	if len(eval.SupersededIDs) != 1 || eval.SupersededIDs[0] != r1ID {
		t.Errorf("unexpected SupersededIDs %v", eval.SupersededIDs)
	}
}

// Supersession: second review (round 2) after supersession of round 1 → uses round 2.
func TestReadiness_SecondReviewAfterSupersession(t *testing.T) {
	id := makeTaskID(t)
	r1ID := EventID(id, 1)
	r2ID := EventID(id, 3)
	events := []DomainEvent{
		reviewEvent(id, 1, r1ID, 1, ReviewChangesRequired),
		supersedReviewEvent(id, 2, r1ID),
		reviewEvent(id, 3, r2ID, 2, ReviewApproved),
		auditEvent(id, 4, EventID(id, 4), 1, AuditPass),
	}
	eval := EvaluateReadiness(id, events)
	if eval.Status != ReadinessReady {
		t.Errorf("expected READY after round-2 review, got %q", eval.Status)
	}
	if eval.ReviewResultID != r2ID {
		t.Errorf("expected ReviewResultID=%q, got %q", r2ID, eval.ReviewResultID)
	}
}

// Audit superseded → AUDIT_REQUIRED.
func TestReadiness_AuditSuperseded_AuditRequired(t *testing.T) {
	id := makeTaskID(t)
	a1ID := EventID(id, 2)
	events := []DomainEvent{
		reviewEvent(id, 1, EventID(id, 1), 1, ReviewApproved),
		auditEvent(id, 2, a1ID, 1, AuditPass),
		supersedAuditEvent(id, 3, a1ID),
	}
	eval := EvaluateReadiness(id, events)
	if eval.Status != ReadinessAuditRequired {
		t.Errorf("expected AUDIT_REQUIRED after audit superseded, got %q", eval.Status)
	}
}

// Higher-round result wins regardless of event order.
func TestReadiness_HigherRoundWins(t *testing.T) {
	id := makeTaskID(t)
	r1ID := EventID(id, 1)
	r2ID := EventID(id, 2)
	// Round 2 (APPROVED) appears before round 1 (CHANGES_REQUIRED) in event slice.
	events := []DomainEvent{
		reviewEvent(id, 2, r2ID, 2, ReviewApproved),
		reviewEvent(id, 1, r1ID, 1, ReviewChangesRequired),
		auditEvent(id, 3, EventID(id, 3), 1, AuditPass),
	}
	eval := EvaluateReadiness(id, events)
	if eval.Status != ReadinessReady {
		t.Errorf("expected READY (higher round wins), got %q", eval.Status)
	}
	if eval.ReviewResultID != r2ID {
		t.Errorf("expected ReviewResultID=%q, got %q", r2ID, eval.ReviewResultID)
	}
}

// M06 mutation: SUPERSEDED result counted as active → ReadinessReady must not occur.
func TestMutation_M06_SupersededNotCountedAsActive(t *testing.T) {
	id := makeTaskID(t)
	r1ID := EventID(id, 1)
	events := []DomainEvent{
		reviewEvent(id, 1, r1ID, 1, ReviewApproved),
		supersedReviewEvent(id, 2, r1ID),
		// No second review. No audit.
	}
	eval := EvaluateReadiness(id, events)
	if eval.Status == ReadinessReady {
		t.Error("M06: superseded ReviewResult must not count as active for READY")
	}
	if eval.Status != ReadinessNotEvaluated {
		t.Errorf("M06: expected NOT_EVALUATED, got %q", eval.Status)
	}
}

// M07 mutation: NOT_EVALUATED treated as READY — must be rejected.
func TestMutation_M07_NotEvaluatedIsNotReady(t *testing.T) {
	id := makeTaskID(t)
	eval := EvaluateReadiness(id, nil)
	if eval.Status == ReadinessReady {
		t.Error("M07: NOT_EVALUATED must not be treated as READY")
	}
}
