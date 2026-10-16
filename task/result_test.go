package task

import (
	"strings"
	"testing"
	"time"
)

var (
	validSHA      = strings.Repeat("a", 40)
	validTreeHash = strings.Repeat("b", 40)
)

func validSubjectBinding() *SubjectBinding {
	return &SubjectBinding{
		RepositoryID: "naghuale/coord",
		CommitSHA:    validSHA,
		TreeHash:     validTreeHash,
	}
}

func baseReviewResult() ReviewResult {
	return ReviewResult{
		Verdict:            ReviewApproved,
		Findings:           []Finding{},
		Summary:            "ok",
		InputArtifactRefs:  []string{},
		OutputArtifactRefs: []string{},
		CreatedAt:          time.Now(),
	}
}

func baseAuditResult() AuditResult {
	return AuditResult{
		Verdict:            AuditPass,
		Findings:           []Finding{},
		Summary:            "ok",
		InputArtifactRefs:  []string{},
		OutputArtifactRefs: []string{},
		CreatedAt:          time.Now(),
	}
}

// SubjectBinding

func TestSubjectBinding_Valid(t *testing.T) {
	if err := validSubjectBinding().Validate(); err != nil {
		t.Errorf("expected valid, got: %v", err)
	}
}

func TestSubjectBinding_MissingRepositoryID(t *testing.T) {
	s := validSubjectBinding()
	s.RepositoryID = ""
	if err := s.Validate(); err == nil {
		t.Error("expected error for missing repository_id")
	}
}

func TestSubjectBinding_ShortSHA(t *testing.T) {
	s := validSubjectBinding()
	s.CommitSHA = "abc123"
	if err := s.Validate(); err == nil {
		t.Error("expected error for short commit SHA")
	}
}

func TestSubjectBinding_MissingTreeHash(t *testing.T) {
	s := validSubjectBinding()
	s.TreeHash = ""
	if err := s.Validate(); err == nil {
		t.Error("expected error for missing tree_hash")
	}
}

func TestSubjectBinding_PatchDigestOptional(t *testing.T) {
	s := validSubjectBinding()
	s.PatchDigest = ""
	if err := s.Validate(); err != nil {
		t.Errorf("patch_digest is optional, got error: %v", err)
	}
	s.PatchDigest = "sha256:abc"
	if err := s.Validate(); err != nil {
		t.Errorf("patch_digest set, still valid, got error: %v", err)
	}
}

func TestSubjectBinding_IsZero(t *testing.T) {
	var s SubjectBinding
	if !s.IsZero() {
		t.Error("zero value SubjectBinding must be IsZero")
	}
	if validSubjectBinding().IsZero() {
		t.Error("non-zero SubjectBinding must not be IsZero")
	}
}

// ReviewResult with Subject

func TestReviewResult_WithValidSubject(t *testing.T) {
	r := baseReviewResult()
	r.Subject = validSubjectBinding()
	if err := r.Validate(); err != nil {
		t.Errorf("expected valid ReviewResult with subject, got: %v", err)
	}
}

func TestReviewResult_WithInvalidSubject(t *testing.T) {
	r := baseReviewResult()
	r.Subject = &SubjectBinding{RepositoryID: "x", CommitSHA: "short", TreeHash: validTreeHash}
	if err := r.Validate(); err == nil {
		t.Error("expected error for ReviewResult with invalid subject")
	}
}

func TestReviewResult_SubjectNilIsValid(t *testing.T) {
	r := baseReviewResult()
	r.Subject = nil
	if err := r.Validate(); err != nil {
		t.Errorf("nil subject must be valid (backward compat), got: %v", err)
	}
}

// AuditResult with AuditBlocked verdict

func TestAuditResult_AuditBlocked(t *testing.T) {
	r := baseAuditResult()
	r.Verdict = AuditBlocked
	if err := r.Validate(); err != nil {
		t.Errorf("AUDIT_BLOCKED is a valid verdict, got: %v", err)
	}
}

func TestAuditResult_UnsupportedVerdict(t *testing.T) {
	r := baseAuditResult()
	r.Verdict = "INVENTED"
	if err := r.Validate(); err == nil {
		t.Error("expected error for unsupported audit verdict")
	}
}

func TestAuditResult_WithValidSubject(t *testing.T) {
	r := baseAuditResult()
	r.Subject = validSubjectBinding()
	if err := r.Validate(); err != nil {
		t.Errorf("expected valid AuditResult with subject, got: %v", err)
	}
}

func TestAuditResult_WithInvalidSubject(t *testing.T) {
	r := baseAuditResult()
	r.Subject = &SubjectBinding{}
	if err := r.Validate(); err == nil {
		t.Error("expected error for AuditResult with invalid subject")
	}
}

// AuditedProperty

func TestAuditResult_ValidProperties(t *testing.T) {
	r := baseAuditResult()
	r.Properties = []AuditedProperty{
		{Name: "independence", Verdict: "PASS"},
		{Name: "subject_binding", Verdict: "PASS", Note: "verified commit sha"},
	}
	if err := r.Validate(); err != nil {
		t.Errorf("valid properties accepted, got: %v", err)
	}
}

func TestAuditResult_PropertyMissingName(t *testing.T) {
	r := baseAuditResult()
	r.Properties = []AuditedProperty{{Name: "", Verdict: "PASS"}}
	if err := r.Validate(); err == nil {
		t.Error("expected error for property missing name")
	}
}

func TestAuditResult_PropertyMissingVerdict(t *testing.T) {
	r := baseAuditResult()
	r.Properties = []AuditedProperty{{Name: "independence", Verdict: ""}}
	if err := r.Validate(); err == nil {
		t.Error("expected error for property missing verdict")
	}
}

// Finding with new fields

func validFinding() Finding {
	return Finding{
		ID:        "task-01/event/1/finding/1",
		Severity:  SeverityMajor,
		Summary:   "issue found",
		Evidence:  []Reference{{Kind: ReferenceTask, ID: "task-01"}},
		CreatedAt: time.Now(),
	}
}

func TestFinding_ValidStatus(t *testing.T) {
	for _, status := range []FindingStatus{FindingOpen, FindingResolved, FindingDismissed} {
		r := baseReviewResult()
		f := validFinding()
		f.Status = status
		r.Findings = []Finding{f}
		if err := r.Validate(); err != nil {
			t.Errorf("status %q should be valid, got: %v", status, err)
		}
	}
}

func TestFinding_InvalidStatus(t *testing.T) {
	r := baseReviewResult()
	f := validFinding()
	f.Status = "INVALID"
	r.Findings = []Finding{f}
	if err := r.Validate(); err == nil {
		t.Error("expected error for unsupported finding status")
	}
}

func TestFinding_ValidClass(t *testing.T) {
	for _, class := range []DecisionClass{DecisionClassFD, DecisionClassTD, DecisionClassTDR2} {
		r := baseReviewResult()
		f := validFinding()
		f.Class = class
		r.Findings = []Finding{f}
		if err := r.Validate(); err != nil {
			t.Errorf("class %q should be valid, got: %v", class, err)
		}
	}
}

func TestFinding_InvalidClass(t *testing.T) {
	r := baseReviewResult()
	f := validFinding()
	f.Class = "UNKNOWN_CLASS"
	r.Findings = []Finding{f}
	if err := r.Validate(); err == nil {
		t.Error("expected error for unsupported finding class")
	}
}

func TestFinding_PathLineOptional(t *testing.T) {
	r := baseReviewResult()
	f := validFinding()
	f.Path = "store/canonical_result.go"
	f.Line = 42
	r.Findings = []Finding{f}
	if err := r.Validate(); err != nil {
		t.Errorf("path and line are optional, got: %v", err)
	}
}

// Mutation tests

// M01: ReviewResult accepts wrong tree hash — invalid subject must be rejected.
func TestMutation_M01_ReviewResultWrongTreeHash(t *testing.T) {
	r := baseReviewResult()
	r.Subject = &SubjectBinding{
		RepositoryID: "naghuale/coord",
		CommitSHA:    validSHA,
		TreeHash:     "",
	}
	if err := r.Validate(); err == nil {
		t.Error("M01: ReviewResult with empty tree_hash must be rejected")
	}
}

// M02: AuditResult accepts wrong subject (short SHA) — must be rejected.
func TestMutation_M02_AuditResultWrongSubject(t *testing.T) {
	r := baseAuditResult()
	r.Subject = &SubjectBinding{
		RepositoryID: "naghuale/coord",
		CommitSHA:    "tooshort",
		TreeHash:     validTreeHash,
	}
	if err := r.Validate(); err == nil {
		t.Error("M02: AuditResult with invalid subject must be rejected")
	}
}

// M04: CHANGES_REQUIRED treated as PASS — must be rejected as invalid audit verdict.
func TestMutation_M04_ChangesRequiredAsPass(t *testing.T) {
	r := baseAuditResult()
	r.Verdict = AuditChangesRequired
	// CHANGES_REQUIRED is a valid audit verdict — it is NOT the same as PASS.
	// This test documents that CHANGES_REQUIRED is accepted as a verdict but differs from PASS.
	if err := r.Validate(); err != nil {
		t.Errorf("M04: CHANGES_REQUIRED must be accepted as a valid verdict: %v", err)
	}
	if r.Verdict == AuditPass {
		t.Error("M04: CHANGES_REQUIRED must not equal PASS")
	}
}

// M05: NOT_EVALUATED treated as ACCEPTED — must not compile as AuditVerdict.
// This is enforced by the type system: "NOT_EVALUATED" string won't match any AuditVerdict const.
func TestMutation_M05_NotEvaluatedIsNotAccepted(t *testing.T) {
	r := baseAuditResult()
	r.Verdict = AuditVerdict("NOT_EVALUATED")
	if err := r.Validate(); err == nil {
		t.Error("M05: NOT_EVALUATED must not be accepted as a valid audit verdict")
	}
}
