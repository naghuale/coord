package shadowpilot

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// ShadowReviewVerdict is the outcome of a shadow review pass.
type ShadowReviewVerdict string

const (
	ShadowReviewApproved        ShadowReviewVerdict = "APPROVED"
	ShadowReviewChangesRequired ShadowReviewVerdict = "CHANGES_REQUIRED"
)

// ShadowAuditVerdict is the outcome of a shadow audit pass.
type ShadowAuditVerdict string

const (
	ShadowAuditPass            ShadowAuditVerdict = "PASS"
	ShadowAuditChangesRequired ShadowAuditVerdict = "CHANGES_REQUIRED"
	ShadowAuditBlocked         ShadowAuditVerdict = "BLOCKED"
	ShadowAuditNotEvaluated    ShadowAuditVerdict = "NOT_EVALUATED"
)

// FindingStatus is the lifecycle state of a shadow finding.
type FindingStatus string

const (
	FindingOpen   FindingStatus = "OPEN"
	FindingClosed FindingStatus = "CLOSED"
)

// FindingSeverity mirrors the production severity enumeration.
type FindingSeverity string

const (
	FindingBlocking    FindingSeverity = "BLOCKING"
	FindingNonBlocking FindingSeverity = "NON_BLOCKING"
)

// ShadowFinding is a single finding within a shadow review or audit result.
// Findings are never silently dropped: closure always requires an evidence
// reference. A BLOCKING finding with Status=OPEN prevents READY readiness.
type ShadowFinding struct {
	ID                 string          `json:"id"`
	Severity           FindingSeverity `json:"severity"`
	Summary            string          `json:"summary"`
	Status             FindingStatus   `json:"status"`
	ClosureEvidenceRef string          `json:"closure_evidence_ref,omitempty"`
	CreatedAt          time.Time       `json:"created_at"`
}

func (f ShadowFinding) Validate() error {
	if strings.TrimSpace(f.ID) == "" {
		return errors.New("shadow finding: id required")
	}
	if strings.TrimSpace(f.Summary) == "" {
		return errors.New("shadow finding: summary required")
	}
	switch f.Severity {
	case FindingBlocking, FindingNonBlocking:
	default:
		return fmt.Errorf("shadow finding: unknown severity %q", f.Severity)
	}
	switch f.Status {
	case FindingOpen, FindingClosed:
	default:
		return fmt.Errorf("shadow finding: unknown status %q", f.Status)
	}
	if f.Status == FindingClosed && strings.TrimSpace(f.ClosureEvidenceRef) == "" {
		return errors.New("shadow finding: closure_evidence_ref required when status is CLOSED")
	}
	if f.CreatedAt.IsZero() {
		return errors.New("shadow finding: created_at required")
	}
	return nil
}

// Close transitions the finding to CLOSED with a mandatory evidence reference.
// It enforces that findings cannot be silently dropped: evidenceRef must be
// non-empty.
func (f *ShadowFinding) Close(evidenceRef string) error {
	if strings.TrimSpace(evidenceRef) == "" {
		return errors.New("shadow finding: cannot close without evidence reference (stop: rationale_or_evidence_lost)")
	}
	if f.Status == FindingClosed {
		return errors.New("shadow finding: already closed")
	}
	f.Status = FindingClosed
	f.ClosureEvidenceRef = evidenceRef
	return nil
}

// ShadowDiscussionNode is a rationale record anchored to a review or audit
// result. It preserves discussion without granting verdict authority.
// Free-text nodes cannot change a verdict, and cannot resolve a finding —
// only ShadowFinding.Close() (with evidence) closes a finding.
type ShadowDiscussionNode struct {
	NodeID          string `json:"node_id"`
	ParentNodeID    string `json:"parent_node_id,omitempty"`
	Author          string `json:"author"`
	Role            string `json:"role"`
	Body            string `json:"body"`
	LinkedFindingID string `json:"linked_finding_id,omitempty"`
	// ResultRef is the ReviewResultID or AuditResultID this node is anchored to.
	ResultRef string    `json:"result_ref"`
	CreatedAt time.Time `json:"created_at"`
}

func (n ShadowDiscussionNode) Validate() error {
	if strings.TrimSpace(n.NodeID) == "" {
		return errors.New("discussion node: node_id required")
	}
	if strings.TrimSpace(n.Author) == "" {
		return errors.New("discussion node: author required")
	}
	if strings.TrimSpace(n.Role) == "" {
		return errors.New("discussion node: role required")
	}
	if strings.TrimSpace(n.Body) == "" {
		return errors.New("discussion node: body required")
	}
	if strings.TrimSpace(n.ResultRef) == "" {
		return errors.New("discussion node: result_ref required")
	}
	if n.CreatedAt.IsZero() {
		return errors.New("discussion node: created_at required")
	}
	return nil
}

// ShadowReviewResult is the canonical review verdict for a shadow run.
// Coord assigns ResultID, ReviewerID, SubjectSHA and CreatedAt; callers
// supply Verdict, Findings and Summary.
// Superseded marks results from a prior subject that must not contribute to
// readiness.
type ShadowReviewResult struct {
	ResultID   string              `json:"result_id"`
	ReviewerID string              `json:"reviewer_id"`
	ExecutorID string              `json:"executor_id"`
	SubjectSHA string              `json:"subject_sha"`
	Generation uint64              `json:"generation"`
	Verdict    ShadowReviewVerdict `json:"verdict"`
	Findings   []ShadowFinding     `json:"findings"`
	Summary    string              `json:"summary"`
	Superseded bool                `json:"superseded"`
	CreatedAt  time.Time           `json:"created_at"`
}

func (r ShadowReviewResult) Validate() error {
	if strings.TrimSpace(r.ResultID) == "" {
		return errors.New("shadow review result: result_id required")
	}
	if strings.TrimSpace(r.ReviewerID) == "" {
		return errors.New("shadow review result: reviewer_id required")
	}
	if strings.TrimSpace(r.ExecutorID) == "" {
		return errors.New("shadow review result: executor_id required")
	}
	if r.ReviewerID == r.ExecutorID {
		return &IndependenceViolation{
			Condition: StopReviewerEqualsExecutor,
			AgentID:   r.ReviewerID,
			Detail:    "reviewer_id equals executor_id",
		}
	}
	if !sha40(r.SubjectSHA) {
		return errors.New("shadow review result: subject_sha must be a 40 or 64 hex character commit SHA")
	}
	if r.Generation == 0 {
		return errors.New("shadow review result: generation must be positive")
	}
	switch r.Verdict {
	case ShadowReviewApproved, ShadowReviewChangesRequired:
	default:
		return fmt.Errorf("shadow review result: unknown verdict %q", r.Verdict)
	}
	if strings.TrimSpace(r.Summary) == "" {
		return errors.New("shadow review result: summary required")
	}
	if r.CreatedAt.IsZero() {
		return errors.New("shadow review result: created_at required")
	}
	for i, f := range r.Findings {
		if err := f.Validate(); err != nil {
			return fmt.Errorf("shadow review result: finding[%d]: %w", i, err)
		}
	}
	return nil
}

// HasOpenBlockingFindings returns true if any BLOCKING finding is still OPEN.
func (r ShadowReviewResult) HasOpenBlockingFindings() bool {
	for _, f := range r.Findings {
		if f.Severity == FindingBlocking && f.Status == FindingOpen {
			return true
		}
	}
	return false
}

// ShadowAuditResult is the canonical audit verdict for a shadow run.
// Auditor must be independent of both Executor and Reviewer.
type ShadowAuditResult struct {
	ResultID   string             `json:"result_id"`
	AuditorID  string             `json:"auditor_id"`
	ExecutorID string             `json:"executor_id"`
	ReviewerID string             `json:"reviewer_id"`
	SubjectSHA string             `json:"subject_sha"`
	Generation uint64             `json:"generation"`
	Verdict    ShadowAuditVerdict `json:"verdict"`
	Findings   []ShadowFinding    `json:"findings"`
	Summary    string             `json:"summary"`
	Superseded bool               `json:"superseded"`
	CreatedAt  time.Time          `json:"created_at"`
}

func (r ShadowAuditResult) Validate() error {
	if strings.TrimSpace(r.ResultID) == "" {
		return errors.New("shadow audit result: result_id required")
	}
	if strings.TrimSpace(r.AuditorID) == "" {
		return errors.New("shadow audit result: auditor_id required")
	}
	if strings.TrimSpace(r.ExecutorID) == "" {
		return errors.New("shadow audit result: executor_id required")
	}
	if strings.TrimSpace(r.ReviewerID) == "" {
		return errors.New("shadow audit result: reviewer_id required")
	}
	if r.AuditorID == r.ExecutorID {
		return &IndependenceViolation{
			Condition: StopAuditorEqualsExecutor,
			AgentID:   r.AuditorID,
			Detail:    "auditor_id equals executor_id",
		}
	}
	if r.AuditorID == r.ReviewerID {
		return &IndependenceViolation{
			Condition: StopAuditorEqualsReviewer,
			AgentID:   r.AuditorID,
			Detail:    "auditor_id equals reviewer_id",
		}
	}
	if !sha40(r.SubjectSHA) {
		return errors.New("shadow audit result: subject_sha must be a 40 or 64 hex character commit SHA")
	}
	if r.Generation == 0 {
		return errors.New("shadow audit result: generation must be positive")
	}
	switch r.Verdict {
	case ShadowAuditPass, ShadowAuditChangesRequired, ShadowAuditBlocked, ShadowAuditNotEvaluated:
	default:
		return fmt.Errorf("shadow audit result: unknown verdict %q", r.Verdict)
	}
	if strings.TrimSpace(r.Summary) == "" {
		return errors.New("shadow audit result: summary required")
	}
	if r.CreatedAt.IsZero() {
		return errors.New("shadow audit result: created_at required")
	}
	for i, f := range r.Findings {
		if err := f.Validate(); err != nil {
			return fmt.Errorf("shadow audit result: finding[%d]: %w", i, err)
		}
	}
	return nil
}

// HasOpenBlockingFindings reports unresolved audit findings independently of verdict.
func (r ShadowAuditResult) HasOpenBlockingFindings() bool {
	for _, f := range r.Findings {
		if f.Severity == FindingBlocking && f.Status == FindingOpen {
			return true
		}
	}
	return false
}

// ShadowReadiness is the deterministic readiness computed from a review and
// audit result pair anchored to the same exact subject SHA.
type ShadowReadiness string

const (
	ShadowReadinessReady           ShadowReadiness = "READY"
	ShadowReadinessChangesRequired ShadowReadiness = "CHANGES_REQUIRED"
	ShadowReadinessNotEvaluated    ShadowReadiness = "NOT_EVALUATED"
	ShadowReadinessBlocked         ShadowReadiness = "BLOCKED"
)

// ReadinessResult is the outcome of ComputeReadiness, including the reason.
type ReadinessResult struct {
	Readiness ShadowReadiness `json:"readiness"`
	Reason    string          `json:"reason"`
}

// ComputeReadiness derives the readiness for a given subject SHA from a review
// and audit result. Both results must be anchored to subjectSHA; superseded
// results, subject mismatches and independence violations produce NOT_EVALUATED
// or BLOCKED rather than passing.
//
// Precedence: BLOCKED > CHANGES_REQUIRED > NOT_EVALUATED > READY.
func ComputeReadiness(review *ShadowReviewResult, audit *ShadowAuditResult, assignment ShadowAssignment) ReadinessResult {
	if review == nil {
		return ReadinessResult{ShadowReadinessNotEvaluated, "no review result"}
	}
	if audit == nil {
		return ReadinessResult{ShadowReadinessNotEvaluated, "no audit result"}
	}
	if err := review.Validate(); err != nil {
		return ReadinessResult{ShadowReadinessBlocked, err.Error()}
	}
	if err := audit.Validate(); err != nil {
		return ReadinessResult{ShadowReadinessBlocked, err.Error()}
	}
	if err := assignment.Validate(); err != nil {
		return ReadinessResult{ShadowReadinessBlocked, err.Error()}
	}
	if review.ExecutorID != assignment.AgentID || audit.ExecutorID != assignment.AgentID ||
		review.Generation != assignment.Generation || audit.Generation != assignment.Generation {
		return ReadinessResult{ShadowReadinessBlocked, "review/audit executor or generation does not match assignment"}
	}
	subjectSHA := assignment.SubjectSHA
	if review.ExecutorID != audit.ExecutorID || review.ReviewerID != audit.ReviewerID || review.Generation != audit.Generation {
		return ReadinessResult{ShadowReadinessBlocked, "review and audit role/generation bindings differ"}
	}

	// Superseded results cannot contribute to readiness.
	if review.Superseded {
		return ReadinessResult{ShadowReadinessNotEvaluated, "review result is superseded"}
	}
	if audit.Superseded {
		return ReadinessResult{ShadowReadinessNotEvaluated, "audit result is superseded"}
	}

	// Both results must reference the same exact subject SHA.
	if !strings.EqualFold(review.SubjectSHA, subjectSHA) {
		return ReadinessResult{ShadowReadinessNotEvaluated,
			fmt.Sprintf("review subject_sha %q does not match target %q", review.SubjectSHA, subjectSHA)}
	}
	if !strings.EqualFold(audit.SubjectSHA, subjectSHA) {
		return ReadinessResult{ShadowReadinessNotEvaluated,
			fmt.Sprintf("audit subject_sha %q does not match target %q", audit.SubjectSHA, subjectSHA)}
	}
	if !strings.EqualFold(review.SubjectSHA, audit.SubjectSHA) {
		return ReadinessResult{ShadowReadinessNotEvaluated,
			"review and audit reference different subject SHAs"}
	}

	if audit.HasOpenBlockingFindings() {
		return ReadinessResult{ShadowReadinessBlocked, "audit has open blocking finding(s)"}
	}
	// NOT_EVALUATED verdict is never treated as PASS.
	if audit.Verdict == ShadowAuditNotEvaluated {
		return ReadinessResult{ShadowReadinessNotEvaluated, "audit verdict is NOT_EVALUATED"}
	}
	if audit.Verdict == ShadowAuditBlocked {
		return ReadinessResult{ShadowReadinessBlocked, "audit verdict is BLOCKED"}
	}

	// Unresolved blocking findings from review prevent READY.
	if review.HasOpenBlockingFindings() {
		return ReadinessResult{ShadowReadinessChangesRequired,
			"review has open blocking finding(s)"}
	}
	if review.Verdict == ShadowReviewChangesRequired {
		return ReadinessResult{ShadowReadinessChangesRequired,
			"review verdict is CHANGES_REQUIRED"}
	}
	if audit.Verdict == ShadowAuditChangesRequired {
		return ReadinessResult{ShadowReadinessChangesRequired,
			"audit verdict is CHANGES_REQUIRED"}
	}

	return ReadinessResult{ShadowReadinessReady, "review APPROVED and audit PASS on same subject"}
}

// IndependenceViolation is returned when a reviewer or auditor is not
// independent of another party. It carries the triggered StopCondition.
type IndependenceViolation struct {
	Condition StopCondition
	AgentID   string
	Detail    string
}

func (e *IndependenceViolation) Error() string {
	return fmt.Sprintf("independence violation (%s): %s", e.Condition, e.Detail)
}
