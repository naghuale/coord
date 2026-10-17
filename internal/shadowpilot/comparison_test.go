package shadowpilot_test

import (
	"testing"

	"coord/internal/shadowpilot"
)

func allMatchDimensions() []shadowpilot.DimensionResult {
	dims := []shadowpilot.ComparisonDimension{
		shadowpilot.DimAssignmentIdentity,
		shadowpilot.DimExactSubject,
		shadowpilot.DimRoleIndependence,
		shadowpilot.DimResultSchema,
		shadowpilot.DimFindingIdentity,
		shadowpilot.DimRationalePreserved,
		shadowpilot.DimEvidenceBinding,
		shadowpilot.DimReadinessState,
		shadowpilot.DimRestartBehavior,
		shadowpilot.DimDuplicateSideEffect,
	}
	results := make([]shadowpilot.DimensionResult, len(dims))
	for i, d := range dims {
		results[i] = shadowpilot.DimensionResult{Dimension: d, Outcome: shadowpilot.OutcomeMatch}
	}
	return results
}

func TestNewComparisonResultAllMatchIsMatch(t *testing.T) {
	cr, err := shadowpilot.NewComparisonResult("sv-run-1", allMatchDimensions())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cr.Outcome != shadowpilot.OutcomeMatch {
		t.Fatalf("expected MATCH, got %q", cr.Outcome)
	}
}

func TestNewComparisonResultUnsafeDivergenceEscalates(t *testing.T) {
	dims := allMatchDimensions()
	dims[0].Outcome = shadowpilot.OutcomeUnsafeDivergence
	cr, err := shadowpilot.NewComparisonResult("sv-run-1", dims)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cr.Outcome != shadowpilot.OutcomeUnsafeDivergence {
		t.Fatalf("expected UNSAFE_DIVERGENCE, got %q", cr.Outcome)
	}
}

func TestNewComparisonResultNotEvaluatedEscalates(t *testing.T) {
	dims := allMatchDimensions()
	dims[3].Outcome = shadowpilot.OutcomeNotEvaluated
	cr, err := shadowpilot.NewComparisonResult("sv-run-1", dims)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cr.Outcome != shadowpilot.OutcomeNotEvaluated {
		t.Fatalf("expected NOT_EVALUATED, got %q", cr.Outcome)
	}
}

func TestNewComparisonResultUnsafeTrumpsNotEvaluated(t *testing.T) {
	dims := allMatchDimensions()
	dims[0].Outcome = shadowpilot.OutcomeUnsafeDivergence
	dims[1].Outcome = shadowpilot.OutcomeNotEvaluated
	cr, err := shadowpilot.NewComparisonResult("sv-run-1", dims)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cr.Outcome != shadowpilot.OutcomeUnsafeDivergence {
		t.Fatalf("UNSAFE must trump NOT_EVALUATED, got %q", cr.Outcome)
	}
}

func TestNewComparisonResultRejectsMissingDimension(t *testing.T) {
	dims := allMatchDimensions()
	dims = dims[:9] // drop last
	_, err := shadowpilot.NewComparisonResult("sv-run-1", dims)
	if err == nil {
		t.Fatal("expected error for missing dimension")
	}
}

func TestNewComparisonResultRejectsDuplicateDimension(t *testing.T) {
	dims := allMatchDimensions()
	dims[1] = dims[0] // duplicate DimAssignmentIdentity
	_, err := shadowpilot.NewComparisonResult("sv-run-1", dims)
	if err == nil {
		t.Fatal("expected error for duplicate dimension")
	}
}

func TestNewComparisonResultRejectsUnknownDimension(t *testing.T) {
	dims := allMatchDimensions()
	dims[0].Dimension = "unknown_dimension"
	_, err := shadowpilot.NewComparisonResult("sv-run-1", dims)
	if err == nil {
		t.Fatal("expected error for unknown dimension")
	}
}

func TestStopViolationValidateRequiresDetail(t *testing.T) {
	v := shadowpilot.StopViolation{Condition: shadowpilot.StopExactSubjectMismatch, Detail: ""}
	if err := v.Validate(); err == nil {
		t.Fatal("expected error for empty detail")
	}
}

func TestStopViolationValidateAcceptsValid(t *testing.T) {
	v := shadowpilot.StopViolation{
		Condition: shadowpilot.StopReviewerEqualsExecutor,
		Detail:    "reviewer agent-abc equals executor agent-abc",
	}
	if err := v.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
