package shadowpilot_test

import (
	"encoding/json"
	"strings"
	"testing"

	"coord/internal/shadowpilot"
)

func TestReportValidateRejectsZeroComparisonCounts(t *testing.T) {
	r := buildReport(t)
	r.Comparison.Matched = 0
	// DimensionsTotal remains ten and Outcome remains MATCH: all other
	// summary/recommendation guards pass if the counts guard is removed.
	if err := r.Validate(); err == nil || !strings.Contains(err.Error(), "invalid comparison counts") {
		t.Fatalf("zero groups for ten dimensions must fail counts validation: %v", err)
	}
}

func TestComputeReadinessRejectsUnknownAuditVerdict(t *testing.T) {
	for _, verdict := range []shadowpilot.ShadowAuditVerdict{"", "UNKNOWN"} {
		t.Run(string(verdict), func(t *testing.T) {
			r, a := validReviewResult(), validAuditResult()
			if got := shadowpilot.ComputeReadiness(&r, &a, sha1); got.Readiness != shadowpilot.ShadowReadinessReady {
				t.Fatalf("invalid baseline: %+v", got)
			}
			a.Verdict = verdict
			got := shadowpilot.ComputeReadiness(&r, &a, sha1)
			if got.Readiness != shadowpilot.ShadowReadinessBlocked || !strings.Contains(got.Reason, "unknown verdict") {
				t.Fatalf("unknown audit verdict must BLOCK: %+v", got)
			}
		})
	}
}

func TestEvidenceDigestRejectsRelabelledRun(t *testing.T) {
	original := validEvidence(t, "run-A")
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatal(err)
	}
	payload["shadow_run_id"] = "run-B"
	data, err = json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var loaded shadowpilot.EvidenceBundle
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatal(err)
	}
	// Match the relabelled run so the direct identity guard passes. The
	// unchanged sealed digest must still reject re-use for another run.
	if err := loaded.Verify("run-B"); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("relabelled bundle must fail digest validation: %v", err)
	}
}
