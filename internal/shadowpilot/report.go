package shadowpilot

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// AdmissionRecommendation is the final recommendation of the shadow validation
// report regarding admission to P0-3 production workflow.
type AdmissionRecommendation string

const (
	// AdmitP03: all gates pass; the coordinator may prepare the exact P0-3 scope.
	// This does NOT authorize production writes — that requires a separate OWNER
	// decision on the exact scope of issue #7.
	AdmitP03 AdmissionRecommendation = "ADMIT_P0_3"
	// CorrectionRequired: one or more unsafe divergences or unresolved findings.
	CorrectionRequired AdmissionRecommendation = "CORRECTION_REQUIRED"
	// RepeatShadowValidation: evidence is insufficient; NOT_EVALUATED on a
	// blocking dimension.
	RepeatShadowValidation AdmissionRecommendation = "REPEAT_SHADOW_VALIDATION"
	// OwnerDecisionRequired: gates cannot be resolved by the coordinator alone.
	OwnerDecisionRequired AdmissionRecommendation = "OWNER_DECISION_REQUIRED"
)

// WorkPackageStatus records whether a work package was integrated before the
// report was produced.
type WorkPackageStatus string

const (
	WPIntegrated WorkPackageStatus = "INTEGRATED"
	WPPending    WorkPackageStatus = "PENDING"
	WPSkipped    WorkPackageStatus = "SKIPPED"
)

// WorkPackageEntry records the integration status of one shadow pilot WP.
type WorkPackageEntry struct {
	Name   string            `json:"name"`
	SHA    string            `json:"sha,omitempty"`
	Status WorkPackageStatus `json:"status"`
}

// RuntimeEvidence records the execution constraints that were verified during
// the shadow pilot.
type RuntimeEvidence struct {
	RealEntryPointUsed bool   `json:"real_entry_point_used"`
	AdapterKind        string `json:"adapter_kind"`
	FakeOrLive         string `json:"fake_or_live"`
	ProductionWrites   int    `json:"production_writes"`
	GitHubWrites       int    `json:"github_writes"`
	CredentialUse      int    `json:"credential_use"`
}

func (r RuntimeEvidence) Validate() error {
	if r.ProductionWrites != 0 {
		return fmt.Errorf("runtime evidence: production_writes must be 0, got %d", r.ProductionWrites)
	}
	if r.GitHubWrites != 0 {
		return fmt.Errorf("runtime evidence: github_writes must be 0, got %d", r.GitHubWrites)
	}
	if r.CredentialUse != 0 {
		return fmt.Errorf("runtime evidence: credential_use must be 0, got %d", r.CredentialUse)
	}
	return nil
}

// ComparisonSummary summarises the outcome across all 10 comparison dimensions.
type ComparisonSummary struct {
	DimensionsTotal   int `json:"dimensions_total"`
	Matched           int `json:"matched"`
	SafeDivergences   int `json:"safe_divergences"`
	UnsafeDivergences int `json:"unsafe_divergences"`
	NotEvaluated      int `json:"not_evaluated"`
}

func summariseComparison(result ComparisonResult) ComparisonSummary {
	s := ComparisonSummary{DimensionsTotal: len(result.Dimensions)}
	for _, d := range result.Dimensions {
		switch d.Outcome {
		case OutcomeMatch:
			s.Matched++
		case OutcomeSafeDivergence:
			s.SafeDivergences++
		case OutcomeUnsafeDivergence:
			s.UnsafeDivergences++
		case OutcomeNotEvaluated:
			s.NotEvaluated++
		}
	}
	return s
}

// RecoverySummary summarises the crash/restart validation evidence.
type RecoverySummary struct {
	CrashPointsTested     int `json:"crash_points_tested"`
	DuplicateLaunches     int `json:"duplicate_launches"`
	StaleResultsAccepted  int `json:"stale_results_accepted"`
	PartialStatesAccepted int `json:"partial_states_accepted"`
}

func (r RecoverySummary) Validate() error {
	if r.DuplicateLaunches != 0 {
		return fmt.Errorf("recovery summary: duplicate_launches must be 0, got %d", r.DuplicateLaunches)
	}
	if r.StaleResultsAccepted != 0 {
		return fmt.Errorf("recovery summary: stale_results_accepted must be 0, got %d", r.StaleResultsAccepted)
	}
	if r.PartialStatesAccepted != 0 {
		return fmt.Errorf("recovery summary: partial_states_accepted must be 0, got %d", r.PartialStatesAccepted)
	}
	return nil
}

// ShadowValidationReport is the final structured output of the shadow pilot.
// It records all evidence gates and produces a deterministic admission
// recommendation. Only ADMIT_P0_3 allows the coordinator to prepare a P0-3
// scope; it does not authorize any production writes.
type ShadowValidationReport struct {
	PilotID           string             `json:"pilot_id"`
	MainSHA           string             `json:"main_sha"`
	CoordVersion      string             `json:"coord_version"`
	CanonicalRevision uint64             `json:"canonical_revision"`
	ProducedAt        time.Time          `json:"produced_at"`
	WorkPackages      []WorkPackageEntry `json:"work_packages"`
	Runtime           RuntimeEvidence    `json:"runtime"`
	Comparison        ComparisonSummary  `json:"comparison"`
	Recovery          RecoverySummary    `json:"recovery"`
	// ReviewAccepted and AuditAccepted must both be true for ADMIT_P0_3.
	ReviewAccepted bool                    `json:"review_accepted"`
	AuditAccepted  bool                    `json:"audit_accepted"`
	EvidenceRefs   []EvidenceRef           `json:"evidence_refs,omitempty"`
	Recommendation AdmissionRecommendation `json:"recommendation"`
}

// Validate checks that all required fields are present and consistent.
func (r ShadowValidationReport) Validate() error {
	if strings.TrimSpace(r.PilotID) == "" {
		return errors.New("report: pilot_id required")
	}
	if !sha40(r.MainSHA) {
		return errors.New("report: main_sha must be a 40 or 64 hex character commit SHA")
	}
	if r.ProducedAt.IsZero() {
		return errors.New("report: produced_at required")
	}
	if len(r.WorkPackages) == 0 {
		return errors.New("report: work_packages required")
	}
	if err := r.Runtime.Validate(); err != nil {
		return err
	}
	if err := r.Recovery.Validate(); err != nil {
		return err
	}
	if r.Comparison.DimensionsTotal != len(allDimensions) {
		return fmt.Errorf("report: comparison dimensions_total must be %d, got %d",
			len(allDimensions), r.Comparison.DimensionsTotal)
	}
	switch r.Recommendation {
	case AdmitP03, CorrectionRequired, RepeatShadowValidation, OwnerDecisionRequired:
	default:
		return fmt.Errorf("report: unknown recommendation %q", r.Recommendation)
	}
	return nil
}

// P03AdmissionGates records each gate that must pass for ADMIT_P0_3.
type P03AdmissionGates struct {
	AllWPsIntegrated               bool `json:"all_wps_integrated"`
	UnsafeDivergences              int  `json:"unsafe_divergences"`
	NotEvaluatedBlockingProperties int  `json:"not_evaluated_blocking_properties"`
	DuplicateLaunches              int  `json:"duplicate_launches"`
	StaleResultsAccepted           int  `json:"stale_results_accepted"`
	ProductionWrites               int  `json:"production_writes"`
	GitHubWrites                   int  `json:"github_writes"`
	ReviewAccepted                 bool `json:"review_accepted"`
	AuditAccepted                  bool `json:"audit_accepted"`
}

// DeriveRecommendation computes the admission recommendation from the evidence
// gates. The coordinator uses this to derive the ShadowValidationReport
// Recommendation field deterministically.
//
// Precedence: CORRECTION_REQUIRED (unsafe) > REPEAT (not_evaluated) > ADMIT/OWNER.
func DeriveRecommendation(gates P03AdmissionGates) AdmissionRecommendation {
	if gates.UnsafeDivergences > 0 ||
		gates.DuplicateLaunches > 0 ||
		gates.StaleResultsAccepted > 0 ||
		gates.ProductionWrites > 0 ||
		gates.GitHubWrites > 0 {
		return CorrectionRequired
	}
	if gates.NotEvaluatedBlockingProperties > 0 {
		return RepeatShadowValidation
	}
	if !gates.ReviewAccepted || !gates.AuditAccepted {
		return OwnerDecisionRequired
	}
	if !gates.AllWPsIntegrated {
		return RepeatShadowValidation
	}
	return AdmitP03
}

// NewReport constructs a ShadowValidationReport from a comparison result and
// recovery summary and derives the recommendation from the evidence gates.
func NewReport(
	pilotID string,
	mainSHA string,
	coordVersion string,
	canonicalRevision uint64,
	producedAt time.Time,
	workPackages []WorkPackageEntry,
	runtime RuntimeEvidence,
	comparison ComparisonResult,
	recovery RecoverySummary,
	reviewAccepted bool,
	auditAccepted bool,
	evidenceRefs []EvidenceRef,
) (ShadowValidationReport, error) {
	if strings.TrimSpace(pilotID) == "" {
		return ShadowValidationReport{}, errors.New("new report: pilot_id required")
	}
	if !sha40(mainSHA) {
		return ShadowValidationReport{}, errors.New("new report: main_sha must be a commit SHA")
	}

	compSummary := summariseComparison(comparison)

	allIntegrated := true
	for _, wp := range workPackages {
		if wp.Status != WPIntegrated {
			allIntegrated = false
			break
		}
	}

	gates := P03AdmissionGates{
		AllWPsIntegrated:               allIntegrated,
		UnsafeDivergences:              compSummary.UnsafeDivergences,
		NotEvaluatedBlockingProperties: compSummary.NotEvaluated,
		DuplicateLaunches:              recovery.DuplicateLaunches,
		StaleResultsAccepted:           recovery.StaleResultsAccepted,
		ProductionWrites:               runtime.ProductionWrites,
		GitHubWrites:                   runtime.GitHubWrites,
		ReviewAccepted:                 reviewAccepted,
		AuditAccepted:                  auditAccepted,
	}
	recommendation := DeriveRecommendation(gates)

	report := ShadowValidationReport{
		PilotID:           pilotID,
		MainSHA:           mainSHA,
		CoordVersion:      coordVersion,
		CanonicalRevision: canonicalRevision,
		ProducedAt:        producedAt,
		WorkPackages:      workPackages,
		Runtime:           runtime,
		Comparison:        compSummary,
		Recovery:          recovery,
		ReviewAccepted:    reviewAccepted,
		AuditAccepted:     auditAccepted,
		EvidenceRefs:      evidenceRefs,
		Recommendation:    recommendation,
	}
	if err := report.Validate(); err != nil {
		return ShadowValidationReport{}, err
	}
	return report, nil
}
