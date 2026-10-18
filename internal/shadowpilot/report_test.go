package shadowpilot_test

import (
	"testing"
	"time"

	"coord/internal/shadowpilot"
)

var reportT0 = time.Date(2026, 10, 17, 18, 0, 0, 0, time.UTC)

var allDims = []shadowpilot.ComparisonDimension{
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

// allMatchComparison returns a ComparisonResult with all 10 dimensions MATCH.
func allMatchComparison() shadowpilot.ComparisonResult {
	dims := make([]shadowpilot.DimensionResult, 0, 10)
	for _, d := range allDims {
		dims = append(dims, shadowpilot.DimensionResult{
			Dimension: d,
			Outcome:   shadowpilot.OutcomeMatch,
		})
	}
	cr, _ := shadowpilot.NewComparisonResult("sv-run-01", dims)
	return cr
}

func cleanRuntime() shadowpilot.RuntimeEvidence {
	return shadowpilot.RuntimeEvidence{
		RealEntryPointUsed: true,
		AdapterKind:        "shadow-fake",
		FakeOrLive:         "fake",
		ProductionWrites:   0,
		GitHubWrites:       0,
		CredentialUse:      0,
	}
}

func cleanRecovery() shadowpilot.RecoverySummary {
	return shadowpilot.RecoverySummary{
		CrashPointsTested:     10,
		DuplicateLaunches:     0,
		StaleResultsAccepted:  0,
		PartialStatesAccepted: 0,
	}
}

func allWPs() []shadowpilot.WorkPackageEntry {
	return []shadowpilot.WorkPackageEntry{
		{Name: "WP-SV-01", SHA: sha1, Status: shadowpilot.WPIntegrated},
		{Name: "WP-SV-02", SHA: sha1, Status: shadowpilot.WPIntegrated},
		{Name: "WP-SV-03", SHA: sha1, Status: shadowpilot.WPIntegrated},
		{Name: "WP-SV-04", SHA: sha1, Status: shadowpilot.WPIntegrated},
		{Name: "WP-SV-05", SHA: sha1, Status: shadowpilot.WPIntegrated},
		{Name: "WP-SV-06", SHA: sha1, Status: shadowpilot.WPIntegrated},
	}
}

func buildReport(t *testing.T) shadowpilot.ShadowValidationReport {
	t.Helper()
	r, err := shadowpilot.NewReport(
		"SHADOW-VALIDATION-PILOT-R1",
		sha1,
		"0.1.0",
		42,
		reportT0,
		allWPs(),
		cleanRuntime(),
		allMatchComparison(),
		cleanRecovery(),
		true,
		true,
		validEvidence(t, "sv-run-01"),
	)
	if err != nil {
		t.Fatalf("NewReport failed: %v", err)
	}
	return r
}

func TestNewReportAllGatesPassGivesAdmitP03(t *testing.T) {
	r := buildReport(t)
	if r.Recommendation != shadowpilot.AdmitP03 {
		t.Fatalf("expected ADMIT_P0_3, got %q", r.Recommendation)
	}
}

func TestNewReportUnsafeDivergenceGivesCorrectionRequired(t *testing.T) {
	dims := make([]shadowpilot.DimensionResult, 0, 10)
	for i, d := range allDims {
		outcome := shadowpilot.OutcomeMatch
		if i == 0 {
			outcome = shadowpilot.OutcomeUnsafeDivergence
		}
		dims = append(dims, shadowpilot.DimensionResult{Dimension: d, Outcome: outcome})
	}
	cr, _ := shadowpilot.NewComparisonResult("sv-run-02", dims)
	r, err := shadowpilot.NewReport(
		"SHADOW-VALIDATION-PILOT-R1", sha1, "0.1.0", 42, reportT0,
		allWPs(), cleanRuntime(), cr, cleanRecovery(), true, true, validEvidence(t, cr.ShadowRunID),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.Recommendation != shadowpilot.CorrectionRequired {
		t.Fatalf("unsafe divergence must give CORRECTION_REQUIRED, got %q", r.Recommendation)
	}
}

func TestNewReportNotEvaluatedDimensionGivesRepeat(t *testing.T) {
	dims := make([]shadowpilot.DimensionResult, 0, 10)
	for i, d := range allDims {
		outcome := shadowpilot.OutcomeMatch
		if i == 3 {
			outcome = shadowpilot.OutcomeNotEvaluated
		}
		dims = append(dims, shadowpilot.DimensionResult{Dimension: d, Outcome: outcome})
	}
	cr, _ := shadowpilot.NewComparisonResult("sv-run-03", dims)
	r, err := shadowpilot.NewReport(
		"SHADOW-VALIDATION-PILOT-R1", sha1, "0.1.0", 42, reportT0,
		allWPs(), cleanRuntime(), cr, cleanRecovery(), true, true, validEvidence(t, cr.ShadowRunID),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The legacy per-dimension REPEAT rule remains; NewReport's aggregate
	// boundary deliberately applies the stricter non-MATCH -> CORRECTION rule.
	if got := shadowpilot.DeriveRecommendation(shadowpilot.P03AdmissionGates{
		AllWPsIntegrated: true, NotEvaluatedBlockingProperties: r.Comparison.NotEvaluated,
		ReviewAccepted: true, AuditAccepted: true,
	}); got != shadowpilot.RepeatShadowValidation {
		t.Fatalf("per-dimension gate must give REPEAT, got %q", got)
	}
	if r.Recommendation != shadowpilot.CorrectionRequired {
		t.Fatalf("non-MATCH report aggregate must give CORRECTION_REQUIRED, got %q", r.Recommendation)
	}
}

func TestNewReportNoReviewGivesOwnerDecision(t *testing.T) {
	r, err := shadowpilot.NewReport(
		"SHADOW-VALIDATION-PILOT-R1", sha1, "0.1.0", 42, reportT0,
		allWPs(), cleanRuntime(), allMatchComparison(), cleanRecovery(),
		false, // reviewAccepted = false
		true, validEvidence(t, "sv-run-01"),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.Recommendation != shadowpilot.OwnerDecisionRequired {
		t.Fatalf("no review must give OWNER_DECISION_REQUIRED, got %q", r.Recommendation)
	}
}

func TestNewReportPendingWPGivesRepeat(t *testing.T) {
	wps := allWPs()
	wps[5].Status = shadowpilot.WPPending // WP-SV-06 pending
	r, err := shadowpilot.NewReport(
		"SHADOW-VALIDATION-PILOT-R1", sha1, "0.1.0", 42, reportT0,
		wps, cleanRuntime(), allMatchComparison(), cleanRecovery(), true, true, validEvidence(t, "sv-run-01"),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.Recommendation != shadowpilot.RepeatShadowValidation {
		t.Fatalf("pending WP must give REPEAT_SHADOW_VALIDATION, got %q", r.Recommendation)
	}
}

func TestNewReportProductionWritesGivesCorrectionRequired(t *testing.T) {
	rt := cleanRuntime()
	rt.ProductionWrites = 1
	_, err := shadowpilot.NewReport(
		"SHADOW-VALIDATION-PILOT-R1", sha1, "0.1.0", 42, reportT0,
		allWPs(), rt, allMatchComparison(), cleanRecovery(), true, true, validEvidence(t, "sv-run-01"),
	)
	if err == nil {
		t.Fatal("production writes must fail report validation")
	}
}

func TestNewReportDuplicateLaunchGivesCorrectionRequired(t *testing.T) {
	rec := cleanRecovery()
	rec.DuplicateLaunches = 1
	_, err := shadowpilot.NewReport(
		"SHADOW-VALIDATION-PILOT-R1", sha1, "0.1.0", 42, reportT0,
		allWPs(), cleanRuntime(), allMatchComparison(), rec, true, true, validEvidence(t, "sv-run-01"),
	)
	if err == nil {
		t.Fatal("duplicate launches must fail report validation")
	}
}

func TestReportValidateRejectsMissingPilotID(t *testing.T) {
	_, err := shadowpilot.NewReport(
		"", sha1, "0.1.0", 42, reportT0,
		allWPs(), cleanRuntime(), allMatchComparison(), cleanRecovery(), true, true, validEvidence(t, "sv-run-01"),
	)
	if err == nil {
		t.Fatal("empty pilot_id must be rejected")
	}
}

func TestReportValidateRejectsShortMainSHA(t *testing.T) {
	_, err := shadowpilot.NewReport(
		"SHADOW-VALIDATION-PILOT-R1", "shortsha", "0.1.0", 42, reportT0,
		allWPs(), cleanRuntime(), allMatchComparison(), cleanRecovery(), true, true, validEvidence(t, "sv-run-01"),
	)
	if err == nil {
		t.Fatal("short main_sha must be rejected")
	}
}

func TestDeriveRecommendationPrecedenceUnsafeBeatsNotEvaluated(t *testing.T) {
	gates := shadowpilot.P03AdmissionGates{
		AllWPsIntegrated:               true,
		UnsafeDivergences:              1,
		NotEvaluatedBlockingProperties: 2,
		ReviewAccepted:                 true,
		AuditAccepted:                  true,
	}
	rec := shadowpilot.DeriveRecommendation(gates)
	if rec != shadowpilot.CorrectionRequired {
		t.Fatalf("unsafe must take precedence over NOT_EVALUATED, got %q", rec)
	}
}

func TestDeriveRecommendationGitHubWritesGiveCorrectionRequired(t *testing.T) {
	gates := shadowpilot.P03AdmissionGates{
		AllWPsIntegrated: true,
		GitHubWrites:     1,
		ReviewAccepted:   true,
		AuditAccepted:    true,
	}
	rec := shadowpilot.DeriveRecommendation(gates)
	if rec != shadowpilot.CorrectionRequired {
		t.Fatalf("github writes must give CORRECTION_REQUIRED, got %q", rec)
	}
}

func TestReportComparisonSummaryCountsAreCorrect(t *testing.T) {
	r := buildReport(t)
	if r.Comparison.DimensionsTotal != 10 {
		t.Fatalf("expected 10 dimensions, got %d", r.Comparison.DimensionsTotal)
	}
	if r.Comparison.Matched != 10 {
		t.Fatalf("expected 10 matched, got %d", r.Comparison.Matched)
	}
	if r.Comparison.UnsafeDivergences != 0 {
		t.Fatalf("expected 0 unsafe, got %d", r.Comparison.UnsafeDivergences)
	}
}

func TestReportValidateRejectsZeroProducedAt(t *testing.T) {
	_, err := shadowpilot.NewReport(
		"SHADOW-VALIDATION-PILOT-R1", sha1, "0.1.0", 42,
		time.Time{}, // zero time
		allWPs(), cleanRuntime(), allMatchComparison(), cleanRecovery(), true, true, validEvidence(t, "sv-run-01"),
	)
	if err == nil {
		t.Fatal("zero produced_at must be rejected")
	}
}
