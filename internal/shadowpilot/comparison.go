package shadowpilot

import (
	"errors"
	"fmt"
)

// ComparisonOutcome is the overall outcome of a shadow comparison pass.
type ComparisonOutcome string

const (
	OutcomeMatch            ComparisonOutcome = "MATCH"
	OutcomeSafeDivergence   ComparisonOutcome = "SAFE_DIVERGENCE"
	OutcomeUnsafeDivergence ComparisonOutcome = "UNSAFE_DIVERGENCE"
	OutcomeNotEvaluated     ComparisonOutcome = "NOT_EVALUATED"
)

// ComparisonDimension is one of the 10 named comparison axes.
type ComparisonDimension string

const (
	DimAssignmentIdentity  ComparisonDimension = "assignment_identity"
	DimExactSubject        ComparisonDimension = "exact_subject"
	DimRoleIndependence    ComparisonDimension = "role_independence"
	DimResultSchema        ComparisonDimension = "result_schema"
	DimFindingIdentity     ComparisonDimension = "finding_identity"
	DimRationalePreserved  ComparisonDimension = "rationale_preservation"
	DimEvidenceBinding     ComparisonDimension = "evidence_binding"
	DimReadinessState      ComparisonDimension = "readiness_state"
	DimRestartBehavior     ComparisonDimension = "restart_behavior"
	DimDuplicateSideEffect ComparisonDimension = "duplicate_side_effects"
)

// allDimensions is the complete ordered set of dimensions. The order is
// authoritative; a ComparisonResult must cover all 10.
var allDimensions = []ComparisonDimension{
	DimAssignmentIdentity,
	DimExactSubject,
	DimRoleIndependence,
	DimResultSchema,
	DimFindingIdentity,
	DimRationalePreserved,
	DimEvidenceBinding,
	DimReadinessState,
	DimRestartBehavior,
	DimDuplicateSideEffect,
}

// DimensionResult is the outcome for a single comparison dimension.
type DimensionResult struct {
	Dimension ComparisonDimension `json:"dimension"`
	Outcome   ComparisonOutcome   `json:"outcome"`
	Note      string              `json:"note,omitempty"`
}

func (d DimensionResult) Validate() error {
	found := false
	for _, dim := range allDimensions {
		if d.Dimension == dim {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("dimension result: unknown dimension %q", d.Dimension)
	}
	switch d.Outcome {
	case OutcomeMatch, OutcomeSafeDivergence, OutcomeUnsafeDivergence, OutcomeNotEvaluated:
	default:
		return fmt.Errorf("dimension result: unknown outcome %q", d.Outcome)
	}
	return nil
}

// ComparisonResult is the full outcome of comparing a shadow run against its
// baseline. Outcome is derived from the dimension results; it is never set
// directly. A ComparisonResult with any UNSAFE_DIVERGENCE dimension always
// reports OutcomeUnsafeDivergence overall. A result with all MATCH dimensions
// reports OutcomeMatch. NOT_EVALUATED in any dimension escalates to
// OutcomeNotEvaluated unless an UNSAFE_DIVERGENCE is also present.
type ComparisonResult struct {
	ShadowRunID ShadowRunID       `json:"shadow_run_id"`
	Dimensions  []DimensionResult `json:"dimensions"`
	Outcome     ComparisonOutcome `json:"outcome"`
}

// NewComparisonResult constructs a ComparisonResult from the given dimensions
// and derives the overall Outcome. It requires exactly one result per dimension.
func NewComparisonResult(id ShadowRunID, dims []DimensionResult) (ComparisonResult, error) {
	if err := id.Validate(); err != nil {
		return ComparisonResult{}, err
	}
	if len(dims) != len(allDimensions) {
		return ComparisonResult{}, fmt.Errorf("comparison result: must cover all %d dimensions, got %d", len(allDimensions), len(dims))
	}
	seen := make(map[ComparisonDimension]bool, len(allDimensions))
	for _, d := range dims {
		if err := d.Validate(); err != nil {
			return ComparisonResult{}, fmt.Errorf("comparison result: %w", err)
		}
		if seen[d.Dimension] {
			return ComparisonResult{}, fmt.Errorf("comparison result: duplicate dimension %q", d.Dimension)
		}
		seen[d.Dimension] = true
	}
	for _, dim := range allDimensions {
		if !seen[dim] {
			return ComparisonResult{}, fmt.Errorf("comparison result: missing dimension %q", dim)
		}
	}
	outcome := deriveOutcome(dims)
	return ComparisonResult{ShadowRunID: id, Dimensions: dims, Outcome: outcome}, nil
}

func deriveOutcome(dims []DimensionResult) ComparisonOutcome {
	hasUnsafe := false
	hasNotEval := false
	hasSafe := false
	for _, d := range dims {
		switch d.Outcome {
		case OutcomeUnsafeDivergence:
			hasUnsafe = true
		case OutcomeNotEvaluated:
			hasNotEval = true
		case OutcomeSafeDivergence:
			hasSafe = true
		}
	}
	if hasUnsafe {
		return OutcomeUnsafeDivergence
	}
	if hasNotEval {
		return OutcomeNotEvaluated
	}
	if hasSafe {
		return OutcomeSafeDivergence
	}
	return OutcomeMatch
}

// StopCondition identifies a pilot stop-condition violation. When any stop
// condition is triggered, the pilot halts and records a typed finding before
// any correction flow.
type StopCondition string

const (
	StopExactSubjectMismatch           StopCondition = "exact_subject_mismatch"
	StopAssignmentOrGenerationMismatch StopCondition = "assignment_or_generation_mismatch"
	StopReviewerEqualsExecutor         StopCondition = "reviewer_equals_executor"
	StopAuditorEqualsExecutor          StopCondition = "auditor_equals_executor"
	StopAuditorEqualsReviewer          StopCondition = "auditor_equals_reviewer"
	StopWriteOutsideShadowStore        StopCondition = "write_outside_shadow_store"
	StopCredentialRequested            StopCondition = "credential_requested"
	StopExternalGitHubMutation         StopCondition = "external_github_mutation"
	StopDuplicateLaunchAfterRestart    StopCondition = "duplicate_agent_launch_after_restart"
	StopStaleGenerationAccepted        StopCondition = "stale_generation_result_accepted"
	StopRationaleOrEvidenceLost        StopCondition = "rationale_or_evidence_lost"
	StopNotEvaluatedAsPass             StopCondition = "not_evaluated_interpreted_as_pass"
	StopProductionStateMutation        StopCondition = "canonical_production_state_mutation"
)

// StopViolation records a triggered stop condition.
type StopViolation struct {
	Condition StopCondition `json:"condition"`
	Detail    string        `json:"detail"`
}

func (v StopViolation) Validate() error {
	if v.Condition == "" {
		return errors.New("stop violation: condition required")
	}
	if v.Detail == "" {
		return errors.New("stop violation: detail required")
	}
	return nil
}
