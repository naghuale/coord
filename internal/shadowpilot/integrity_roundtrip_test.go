package shadowpilot_test

import (
	"encoding/json"
	"strings"
	"testing"

	"coord/internal/shadowpilot"
)

func TestCorrectiveNewReportRejectsAggregateMismatch(t *testing.T) {
	for _, outcome := range []shadowpilot.ComparisonOutcome{shadowpilot.OutcomeUnsafeDivergence, shadowpilot.OutcomeSafeDivergence, shadowpilot.OutcomeNotEvaluated, "", "UNKNOWN"} {
		t.Run(string(outcome), func(t *testing.T) {
			comparison := allMatchComparison()
			comparison.Outcome = outcome
			_, err := shadowpilot.NewReport("pilot", sha1, "0.1", 1, reportT0, allWPs(), cleanRuntime(), comparison, cleanRecovery(), true, true, validEvidence(t, comparison.ShadowRunID))
			if err == nil || !strings.Contains(err.Error(), "aggregate") {
				t.Fatalf("aggregate mismatch accepted or masked: %v", err)
			}
		})
	}
	comparison := allMatchComparison()
	comparison.Dimensions[0].Outcome = shadowpilot.OutcomeUnsafeDivergence
	if _, err := shadowpilot.NewReport("pilot", sha1, "0.1", 1, reportT0, allWPs(), cleanRuntime(), comparison, cleanRecovery(), true, true, validEvidence(t, comparison.ShadowRunID)); err == nil {
		t.Fatal("optimistic supplied MATCH accepted against unsafe dimensions")
	}
}

func TestCorrectiveReportAggregateConsistency(t *testing.T) {
	for _, mode := range []string{"unsafe outcome with matching counts", "MATCH outcome with safe divergences"} {
		t.Run(mode, func(t *testing.T) {
			r := buildReport(t)
			if mode == "unsafe outcome with matching counts" {
				r.Comparison.Outcome = shadowpilot.OutcomeUnsafeDivergence
			} else {
				r.Comparison.Matched--
				r.Comparison.SafeDivergences++
			}
			if err := r.Validate(); err == nil || !strings.Contains(err.Error(), "aggregate") {
				t.Fatalf("aggregate inconsistent with counters: %v", err)
			}
		})
	}
}

func TestCorrectiveEvidenceJSONTampering(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(map[string]any)
	}{
		{"swap ref", func(p map[string]any) {
			p["refs"].([]any)[0].(map[string]any)["id"] = "foreign-result"
			p["manifest"].([]any)[0].(map[string]any)["id"] = "foreign-result"
		}},
		{"drop ref", func(p map[string]any) { p["refs"] = []any{}; p["manifest"] = []any{} }},
		{"duplicate ref", func(p map[string]any) {
			refs := p["refs"].([]any)
			p["refs"] = append(refs, refs[0])
			manifest := p["manifest"].([]any)
			p["manifest"] = append(manifest, manifest[0])
		}},
		{"change provenance", func(p map[string]any) { p["provenance"] = "foreign-source" }},
		{"change run", func(p map[string]any) { p["shadow_run_id"] = "foreign-run" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := validEvidence(t, "run-A")
			data, err := json.Marshal(original)
			if err != nil {
				t.Fatal(err)
			}
			var payload map[string]any
			if err := json.Unmarshal(data, &payload); err != nil {
				t.Fatal(err)
			}
			tc.edit(payload)
			data, err = json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			var loaded shadowpilot.EvidenceBundle
			if err := json.Unmarshal(data, &loaded); err != nil {
				t.Fatal(err)
			}
			if err := loaded.Verify("run-A"); err == nil {
				t.Fatal("tampered JSON evidence accepted")
			}
		})
	}
	data, err := json.Marshal(validEvidence(t, "run-A"))
	if err != nil {
		t.Fatal(err)
	}
	var loaded shadowpilot.EvidenceBundle
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatal(err)
	}
	if err := loaded.Verify("run-A"); err != nil {
		t.Fatal(err)
	}
	if err := loaded.Verify("run-B"); err == nil {
		t.Fatal("cross-run sealed JSON evidence accepted")
	}
}

func TestCorrectiveEvidenceSealEnforcement(t *testing.T) {
	for _, mode := range []string{"empty provenance", "empty refs", "duplicate refs"} {
		t.Run(mode, func(t *testing.T) {
			b, err := shadowpilot.NewEvidenceBundle("run-A")
			if err != nil {
				t.Fatal(err)
			}
			provenance := "fixture:run-A"
			if mode != "empty refs" {
				if err := b.AddRef(shadowpilot.EvidenceRef{Kind: shadowpilot.EvidenceExecutorResult, ID: "result-A"}); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "empty provenance" {
				provenance = ""
			}
			if mode == "duplicate refs" {
				b.Refs = append(b.Refs, b.Refs[0])
			}
			if err := b.Seal(provenance); err == nil {
				t.Fatal("invalid constructor input sealed")
			}
		})
	}
}

func TestCorrectiveEvidenceZeroValueRejected(t *testing.T) {
	var b shadowpilot.EvidenceBundle
	if err := b.Verify("run-A"); err == nil {
		t.Fatal("zero-value evidence verified")
	}
	comparison := allMatchComparison()
	if _, err := shadowpilot.NewReport("pilot", sha1, "0.1", 1, reportT0, allWPs(), cleanRuntime(), comparison, cleanRecovery(), true, true, b); err == nil {
		t.Fatal("report accepted zero-value evidence")
	}
}
