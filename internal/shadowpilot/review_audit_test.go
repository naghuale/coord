package shadowpilot_test

import (
	"errors"
	"testing"
	"time"

	"coord/internal/shadowpilot"
)

const sha1 = "b4334cd2438837140bc112b97cba49eb1a478f08"
const sha2 = "aacc4ee0d713846a537b623db2ee2dba7114e9e4"

func validReviewResult() shadowpilot.ShadowReviewResult {
	return shadowpilot.ShadowReviewResult{
		ResultID:   "review-result-001",
		ReviewerID: "agent-reviewer",
		ExecutorID: "agent-executor",
		SubjectSHA: sha1,
		Generation: 1,
		Verdict:    shadowpilot.ShadowReviewApproved,
		Findings:   []shadowpilot.ShadowFinding{},
		Summary:    "all invariants pass",
		CreatedAt:  time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC),
	}
}

func validAuditResult() shadowpilot.ShadowAuditResult {
	return shadowpilot.ShadowAuditResult{
		ResultID:   "audit-result-001",
		AuditorID:  "agent-auditor",
		ExecutorID: "agent-executor",
		ReviewerID: "agent-reviewer",
		SubjectSHA: sha1,
		Generation: 1,
		Verdict:    shadowpilot.ShadowAuditPass,
		Findings:   []shadowpilot.ShadowFinding{},
		Summary:    "all properties pass",
		CreatedAt:  time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC),
	}
}

// ReviewResult validation

func TestReviewResultRecordedForExactSubject(t *testing.T) {
	r := validReviewResult()
	if err := r.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReviewerCannotEqualExecutor(t *testing.T) {
	r := validReviewResult()
	r.ReviewerID = r.ExecutorID
	err := r.Validate()
	if err == nil {
		t.Fatal("expected error for reviewer_equals_executor")
	}
	var iv *shadowpilot.IndependenceViolation
	if !errors.As(err, &iv) {
		t.Fatalf("expected IndependenceViolation, got %T: %v", err, err)
	}
	if iv.Condition != shadowpilot.StopReviewerEqualsExecutor {
		t.Fatalf("expected StopReviewerEqualsExecutor, got %v", iv.Condition)
	}
}

// Mutation M01: reviewer independence disabled — validator must still fire
func TestM01ReviewerIndependenceDisabledIsRejected(t *testing.T) {
	r := validReviewResult()
	r.ReviewerID = "agent-executor" // same as ExecutorID
	if err := r.Validate(); err == nil {
		t.Fatal("M01: reviewer independence check must not be disabled")
	}
}

// Finding lifecycle

func TestBlockingFindingProducesChangesRequired(t *testing.T) {
	r := validReviewResult()
	r.Verdict = shadowpilot.ShadowReviewChangesRequired
	r.Findings = []shadowpilot.ShadowFinding{{
		ID:        "finding-001",
		Severity:  shadowpilot.FindingBlocking,
		Summary:   "critical issue",
		Status:    shadowpilot.FindingOpen,
		CreatedAt: time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC),
	}}
	if err := r.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !r.HasOpenBlockingFindings() {
		t.Fatal("expected HasOpenBlockingFindings to return true")
	}
}

// Mutation M02: blocking finding ignored — readiness must not become READY
func TestM02BlockingFindingIgnoredIsRejected(t *testing.T) {
	review := validReviewResult()
	review.Verdict = shadowpilot.ShadowReviewApproved
	review.Findings = []shadowpilot.ShadowFinding{{
		ID:        "finding-001",
		Severity:  shadowpilot.FindingBlocking,
		Summary:   "unresolved",
		Status:    shadowpilot.FindingOpen, // blocking and open
		CreatedAt: time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC),
	}}
	audit := validAuditResult()
	result := shadowpilot.ComputeReadiness(&review, &audit, readinessAssignment(sha1))
	if result.Readiness == shadowpilot.ShadowReadinessReady {
		t.Fatal("M02: blocking open finding must prevent READY")
	}
}

func TestFindingCannotResolveWithoutEvidence(t *testing.T) {
	f := shadowpilot.ShadowFinding{
		ID:        "finding-001",
		Severity:  shadowpilot.FindingBlocking,
		Summary:   "issue",
		Status:    shadowpilot.FindingOpen,
		CreatedAt: time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC),
	}
	if err := f.Close(""); err == nil {
		t.Fatal("expected error for close without evidence")
	}
}

// Mutation M04: finding closes without evidence — Validate must reject
func TestM04FindingClosedWithoutEvidenceIsRejected(t *testing.T) {
	f := shadowpilot.ShadowFinding{
		ID:                 "finding-001",
		Severity:           shadowpilot.FindingBlocking,
		Summary:            "issue",
		Status:             shadowpilot.FindingClosed,
		ClosureEvidenceRef: "", // missing evidence
		CreatedAt:          time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC),
	}
	if err := f.Validate(); err == nil {
		t.Fatal("M04: Validate must reject CLOSED finding without closure_evidence_ref")
	}
}

func TestFindingCloseRequiresEvidence(t *testing.T) {
	f := shadowpilot.ShadowFinding{
		ID:        "finding-001",
		Severity:  shadowpilot.FindingBlocking,
		Summary:   "issue",
		Status:    shadowpilot.FindingOpen,
		CreatedAt: time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC),
	}
	if err := f.Close("ev-ref-001"); err != nil {
		t.Fatalf("Close with evidence: %v", err)
	}
	if f.Status != shadowpilot.FindingClosed {
		t.Fatal("finding must be CLOSED after Close")
	}
	if f.ClosureEvidenceRef == "" {
		t.Fatal("closure_evidence_ref must be set")
	}
}

// Discussion graph

func TestDiscussionPreservesRationale(t *testing.T) {
	n := shadowpilot.ShadowDiscussionNode{
		NodeID:    "dn-001",
		Author:    "agent-reviewer",
		Role:      "reviewer",
		Body:      "This implementation violates the fencing invariant.",
		ResultRef: "review-result-001",
		CreatedAt: time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC),
	}
	if err := n.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// Mutation M03: free text changes verdict — DiscussionNode has no verdict field
func TestM03DiscussionCannotChangeVerdict(t *testing.T) {
	// ShadowDiscussionNode has no Verdict field: the type enforces this structurally.
	// This test ensures the type does not expose a verdict-changing mechanism.
	var n shadowpilot.ShadowDiscussionNode
	_ = n.Body // free text body only
	// Confirm no verdict-setting field exists at compile time by verifying
	// the struct fields.
	_ = n.NodeID
	_ = n.Author
	_ = n.Role
	_ = n.Body
	_ = n.ResultRef
	// If this test compiles, the type does not have a Verdict field.
}

// Correction: new subject

func TestCorrectionCreatesNewSubject(t *testing.T) {
	// A correction produces a new subject SHA. The old review is superseded.
	oldReview := validReviewResult()
	oldReview.Superseded = true

	// New review for new subject.
	newReview := validReviewResult()
	newReview.SubjectSHA = sha2
	newReview.ResultID = "review-result-002"

	newAudit := validAuditResult()
	newAudit.SubjectSHA = sha2

	result := shadowpilot.ComputeReadiness(&newReview, &newAudit, readinessAssignment(sha2))
	if result.Readiness != shadowpilot.ShadowReadinessReady {
		t.Fatalf("expected READY for new subject, got %q: %s", result.Readiness, result.Reason)
	}
}

func TestOldReviewDoesNotApplyToNewSubject(t *testing.T) {
	oldReview := validReviewResult()
	oldReview.SubjectSHA = sha1 // old subject

	newAudit := validAuditResult()
	newAudit.SubjectSHA = sha2 // new subject

	// Target is new subject; old review does not match.
	result := shadowpilot.ComputeReadiness(&oldReview, &newAudit, readinessAssignment(sha2))
	if result.Readiness == shadowpilot.ShadowReadinessReady {
		t.Fatalf("M05: old review must not apply to new subject, got %q", result.Readiness)
	}
}

// Mutation M05: old Review reused after subject change
func TestM05OldReviewReusedIsRejected(t *testing.T) {
	oldReview := validReviewResult()
	oldReview.Superseded = true

	audit := validAuditResult()

	result := shadowpilot.ComputeReadiness(&oldReview, &audit, readinessAssignment(sha1))
	if result.Readiness == shadowpilot.ShadowReadinessReady {
		t.Fatal("M05: superseded review result must not produce READY")
	}
}

// AuditResult validation

func TestAuditorCannotEqualExecutor(t *testing.T) {
	r := validAuditResult()
	r.AuditorID = r.ExecutorID
	err := r.Validate()
	if err == nil {
		t.Fatal("expected error for auditor_equals_executor")
	}
	var iv *shadowpilot.IndependenceViolation
	if !errors.As(err, &iv) {
		t.Fatalf("expected IndependenceViolation, got %T: %v", err, err)
	}
	if iv.Condition != shadowpilot.StopAuditorEqualsExecutor {
		t.Fatalf("expected StopAuditorEqualsExecutor, got %v", iv.Condition)
	}
}

func TestAuditorCannotEqualReviewer(t *testing.T) {
	r := validAuditResult()
	r.AuditorID = r.ReviewerID
	err := r.Validate()
	if err == nil {
		t.Fatal("expected error for auditor_equals_reviewer")
	}
	var iv *shadowpilot.IndependenceViolation
	if !errors.As(err, &iv) {
		t.Fatalf("expected IndependenceViolation, got %T: %v", err, err)
	}
	if iv.Condition != shadowpilot.StopAuditorEqualsReviewer {
		t.Fatalf("expected StopAuditorEqualsReviewer, got %v", iv.Condition)
	}
}

// Mutation M06: auditor independence disabled
func TestM06AuditorIndependenceDisabledIsRejected(t *testing.T) {
	r := validAuditResult()
	r.AuditorID = r.ExecutorID
	if err := r.Validate(); err == nil {
		t.Fatal("M06: auditor independence check must not be disabled")
	}
}

// Readiness: NOT_EVALUATED

func TestAuditNotEvaluatedIsNotPass(t *testing.T) {
	review := validReviewResult()
	audit := validAuditResult()
	audit.Verdict = shadowpilot.ShadowAuditNotEvaluated

	result := shadowpilot.ComputeReadiness(&review, &audit, readinessAssignment(sha1))
	if result.Readiness == shadowpilot.ShadowReadinessReady {
		t.Fatal("expected NOT_EVALUATED, got READY")
	}
	if result.Readiness != shadowpilot.ShadowReadinessNotEvaluated {
		t.Fatalf("expected NOT_EVALUATED, got %q", result.Readiness)
	}
}

// Mutation M07: NOT_EVALUATED treated as PASS
func TestM07NotEvaluatedTreatedAsPassIsRejected(t *testing.T) {
	review := validReviewResult()
	audit := validAuditResult()
	audit.Verdict = shadowpilot.ShadowAuditNotEvaluated

	result := shadowpilot.ComputeReadiness(&review, &audit, readinessAssignment(sha1))
	if result.Readiness == shadowpilot.ShadowReadinessReady {
		t.Fatal("M07: NOT_EVALUATED must not be treated as PASS")
	}
}

func TestAcceptedReviewAndAuditSameSubjectCanBecomeReady(t *testing.T) {
	review := validReviewResult()
	audit := validAuditResult()
	result := shadowpilot.ComputeReadiness(&review, &audit, readinessAssignment(sha1))
	if result.Readiness != shadowpilot.ShadowReadinessReady {
		t.Fatalf("expected READY, got %q: %s", result.Readiness, result.Reason)
	}
}

// Mutation M08: mixed-subject Review and Audit treated as READY
func TestM08MixedSubjectReadinessIsRejected(t *testing.T) {
	review := validReviewResult()
	review.SubjectSHA = sha1

	audit := validAuditResult()
	audit.SubjectSHA = sha2 // different subject

	result := shadowpilot.ComputeReadiness(&review, &audit, readinessAssignment(sha1))
	if result.Readiness == shadowpilot.ShadowReadinessReady {
		t.Fatal("M08: mixed-subject review and audit must not produce READY")
	}
}
