package shadowpilot_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"coord/internal/shadowpilot"
)

func validEvidence(t *testing.T, id shadowpilot.ShadowRunID) shadowpilot.EvidenceBundle {
	t.Helper()
	b, err := shadowpilot.NewEvidenceBundle(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.AddRef(shadowpilot.EvidenceRef{Kind: shadowpilot.EvidenceExecutorResult, ID: "result-1"}); err != nil {
		t.Fatal(err)
	}
	if err := b.Seal("committed-fixture:shadowpilot"); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestCorrectiveReadinessEnforcesIndependence(t *testing.T) {
	for _, tc := range []struct {
		name      string
		condition shadowpilot.StopCondition
		edit      func(*shadowpilot.ShadowReviewResult, *shadowpilot.ShadowAuditResult)
	}{
		{"reviewer equals executor", shadowpilot.StopReviewerEqualsExecutor, func(r *shadowpilot.ShadowReviewResult, _ *shadowpilot.ShadowAuditResult) { r.ReviewerID = r.ExecutorID }},
		{"auditor equals executor", shadowpilot.StopAuditorEqualsExecutor, func(_ *shadowpilot.ShadowReviewResult, a *shadowpilot.ShadowAuditResult) { a.AuditorID = a.ExecutorID }},
		{"auditor equals reviewer", shadowpilot.StopAuditorEqualsReviewer, func(_ *shadowpilot.ShadowReviewResult, a *shadowpilot.ShadowAuditResult) { a.AuditorID = a.ReviewerID }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, a := validReviewResult(), validAuditResult()
			tc.edit(&r, &a)
			got := shadowpilot.ComputeReadiness(&r, &a, sha1)
			if got.Readiness != shadowpilot.ShadowReadinessBlocked || !strings.Contains(got.Reason, string(tc.condition)) {
				t.Fatalf("independence violation must BLOCK with stop condition: %+v", got)
			}
		})
	}
}

func TestCorrectiveReadinessGuards(t *testing.T) {
	for _, tc := range []struct {
		name string
		want shadowpilot.ShadowReadiness
		edit func(**shadowpilot.ShadowReviewResult, *shadowpilot.ShadowAuditResult)
	}{
		{"audit BLOCKED", shadowpilot.ShadowReadinessBlocked, func(_ **shadowpilot.ShadowReviewResult, a *shadowpilot.ShadowAuditResult) {
			a.Verdict = shadowpilot.ShadowAuditBlocked
		}},
		{"review CHANGES_REQUIRED without findings", shadowpilot.ShadowReadinessChangesRequired, func(r **shadowpilot.ShadowReviewResult, _ *shadowpilot.ShadowAuditResult) {
			(*r).Verdict = shadowpilot.ShadowReviewChangesRequired
		}},
		{"superseded audit", shadowpilot.ShadowReadinessNotEvaluated, func(_ **shadowpilot.ShadowReviewResult, a *shadowpilot.ShadowAuditResult) { a.Superseded = true }},
		{"nil review", shadowpilot.ShadowReadinessNotEvaluated, func(r **shadowpilot.ShadowReviewResult, _ *shadowpilot.ShadowAuditResult) { *r = nil }},
		{"cross-result executor", shadowpilot.ShadowReadinessBlocked, func(_ **shadowpilot.ShadowReviewResult, a *shadowpilot.ShadowAuditResult) {
			a.ExecutorID = "other-executor"
		}},
		{"cross-result reviewer", shadowpilot.ShadowReadinessBlocked, func(_ **shadowpilot.ShadowReviewResult, a *shadowpilot.ShadowAuditResult) {
			a.ReviewerID = "other-reviewer"
		}},
		{"cross-result generation", shadowpilot.ShadowReadinessBlocked, func(_ **shadowpilot.ShadowReviewResult, a *shadowpilot.ShadowAuditResult) { a.Generation++ }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, a := validReviewResult(), validAuditResult()
			if got := shadowpilot.ComputeReadiness(&r, &a, sha1); got.Readiness != shadowpilot.ShadowReadinessReady {
				t.Fatalf("invalid baseline: %+v", got)
			}
			rp := &r
			tc.edit(&rp, &a)
			if got := shadowpilot.ComputeReadiness(rp, &a, sha1); got.Readiness != tc.want {
				t.Fatalf("got %+v, want %s", got, tc.want)
			}
		})
	}
}

func TestCorrectiveNewReportHonorsAggregate(t *testing.T) {
	for _, outcome := range []shadowpilot.ComparisonOutcome{shadowpilot.OutcomeUnsafeDivergence, shadowpilot.OutcomeSafeDivergence, shadowpilot.OutcomeNotEvaluated, "", "UNKNOWN"} {
		t.Run(string(outcome), func(t *testing.T) {
			comparison := allMatchComparison()
			comparison.Outcome = outcome
			r, err := shadowpilot.NewReport("pilot", sha1, "0.1", 1, reportT0, allWPs(), cleanRuntime(), comparison, cleanRecovery(), true, true, validEvidence(t, comparison.ShadowRunID))
			if err != nil {
				t.Fatal(err)
			}
			if r.Recommendation != shadowpilot.CorrectionRequired {
				t.Fatalf("aggregate %q yielded %s", outcome, r.Recommendation)
			}
		})
	}
}

func TestCorrectiveReportRejectsForgedRecommendation(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*shadowpilot.ShadowValidationReport)
	}{
		{"unsafe counts", func(r *shadowpilot.ShadowValidationReport) { r.Comparison.Matched--; r.Comparison.UnsafeDivergences++ }},
		{"not evaluated counts", func(r *shadowpilot.ShadowValidationReport) { r.Comparison.Matched--; r.Comparison.NotEvaluated++ }},
		{"unsafe aggregate", func(r *shadowpilot.ShadowValidationReport) {
			r.Comparison.Outcome = shadowpilot.OutcomeUnsafeDivergence
		}},
		{"pending WP", func(r *shadowpilot.ShadowValidationReport) { r.WorkPackages[0].Status = shadowpilot.WPPending }},
		{"no review", func(r *shadowpilot.ShadowValidationReport) { r.ReviewAccepted = false }},
		{"no audit", func(r *shadowpilot.ShadowValidationReport) { r.AuditAccepted = false }},
		{"unknown recommendation", func(r *shadowpilot.ShadowValidationReport) { r.Recommendation = "UNKNOWN" }},
		{"empty recommendation", func(r *shadowpilot.ShadowValidationReport) { r.Recommendation = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := buildReport(t)
			tc.edit(&r)
			// Exercise the loaded-artifact validation boundary, not only NewReport.
			data, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			var loaded shadowpilot.ShadowValidationReport
			if err := json.Unmarshal(data, &loaded); err != nil {
				t.Fatal(err)
			}
			if err := loaded.Validate(); err == nil || !strings.Contains(err.Error(), "recommendation") {
				t.Fatalf("forged recommendation accepted or masked: %v", err)
			}
		})
	}
}

func TestCorrectiveReportDimensionCount(t *testing.T) {
	r := buildReport(t)
	r.Comparison.DimensionsTotal = 9
	r.Comparison.Matched = 9 // counts remain internally consistent
	if err := r.Validate(); err == nil {
		t.Fatal("report accepted fewer than ten dimensions")
	}
}

func TestCorrectiveCompleteBindsEveryIdentity(t *testing.T) {
	for _, field := range []string{"assignment", "agent", "session", "input"} {
		t.Run(field, func(t *testing.T) {
			a := validAssignment()
			e, err := shadowpilot.NewShadowExecution(a)
			if err != nil {
				t.Fatal(err)
			}
			if err := e.Start(t0); err != nil {
				t.Fatal(err)
			}
			r := shadowpilot.AgentResult{Kind: shadowpilot.AgentResultKind, AssignmentID: a.AssignmentID, AgentID: a.AgentID, SessionID: a.SessionID, Generation: a.Generation, SubjectSHA: a.SubjectSHA, InputDigest: a.InputDigest, CompletedAt: t0}
			switch field {
			case "assignment":
				r.AssignmentID = "other-assignment"
			case "agent":
				r.AgentID = "other-agent"
			case "session":
				r.SessionID = "other-session"
			case "input":
				r.InputDigest = "other-digest"
			}
			before := e
			if err := e.Complete(r, t0); err == nil {
				t.Fatal("foreign result accepted")
			}
			if !reflect.DeepEqual(before, e) {
				t.Fatal("rejected result changed execution")
			}
		})
	}
}

func TestCorrectiveRecoveryLiveGeneration(t *testing.T) {
	a := crashAssignment()
	cp := shadowpilot.ShadowCheckpoint{Assignment: &a, Execution: crashExecution(shadowpilot.ExecutorRunning)}
	plan := shadowpilot.PlanRecovery(cp, &shadowpilot.AgentLiveness{IsAlive: true, ObservedGeneration: a.Generation + 1})
	if !plan.Is(shadowpilot.RecoveryDuplicateBlocked) {
		t.Fatalf("stale live generation: %+v", plan)
	}
}

func TestCorrectiveRecoveryFenced(t *testing.T) {
	a := crashAssignment()
	e := crashExecution(shadowpilot.ExecutorRunning)
	if err := e.Fence(shadowpilot.StopViolation{Condition: shadowpilot.StopStaleGenerationAccepted, Detail: "stale session"}, crashT0); err != nil {
		t.Fatal(err)
	}
	plan := shadowpilot.PlanRecovery(shadowpilot.ShadowCheckpoint{Assignment: &a, Execution: e}, nil)
	if !plan.Is(shadowpilot.RecoveryFencedNoRestart) {
		t.Fatalf("fenced execution must remain terminal: %+v", plan)
	}
}

func TestCorrectiveCheckpointSupersededReview(t *testing.T) {
	a, r := crashAssignment(), validReviewResult()
	r.Superseded = true
	if err := shadowpilot.ValidateCheckpointIntegrity(shadowpilot.ShadowCheckpoint{Assignment: &a, ReviewResult: &r}); err == nil {
		t.Fatal("superseded review treated as current")
	}
}

func TestCorrectiveEvidenceDuplicateRefs(t *testing.T) {
	b := validEvidence(t, "sv-run-1")
	b.Refs = append(b.Refs, b.Refs[0])
	b.Manifest = append(b.Manifest, b.Manifest[0])
	b.BundleDigest, _ = shadowpilot.EvidenceBundleDigest(b.ShadowRunID, b.Manifest, b.Provenance)
	if err := b.Validate(); err == nil || !strings.Contains(err.Error(), "duplicate ref") {
		t.Fatalf("duplicate refs accepted or masked: %v", err)
	}
}

func TestCorrectiveEvidenceIntegrity(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*shadowpilot.EvidenceBundle)
	}{
		{"empty manifest", func(b *shadowpilot.EvidenceBundle) {
			b.Manifest = nil
			b.Refs = nil
			b.BundleDigest, _ = shadowpilot.EvidenceBundleDigest(b.ShadowRunID, b.Manifest, b.Provenance)
		}},
		{"missing provenance", func(b *shadowpilot.EvidenceBundle) {
			b.Provenance = ""
			b.BundleDigest, _ = shadowpilot.EvidenceBundleDigest(b.ShadowRunID, b.Manifest, b.Provenance)
		}},
		{"digest mismatch", func(b *shadowpilot.EvidenceBundle) { b.BundleDigest = "forged" }},
		{"ref omitted from manifest", func(b *shadowpilot.EvidenceBundle) { b.Refs[0].ID = "other-result" }},
		{"tampered manifest", func(b *shadowpilot.EvidenceBundle) { b.Manifest[0].ID = "other-result"; b.Refs[0].ID = "other-result" }},
		{"invalid ref", func(b *shadowpilot.EvidenceBundle) {
			b.Refs[0].Kind = ""
			b.Manifest[0].Kind = ""
			b.BundleDigest, _ = shadowpilot.EvidenceBundleDigest(b.ShadowRunID, b.Manifest, b.Provenance)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := validEvidence(t, "sv-run-1")
			tc.edit(&b)
			if err := b.Validate(); err == nil {
				t.Fatal("invalid evidence accepted")
			}
		})
	}
	b := validEvidence(t, "sv-run-1")
	if err := b.Verify("sv-run-2"); err == nil {
		t.Fatal("cross-run evidence accepted")
	}
	draft, _ := shadowpilot.NewEvidenceBundle("sv-run-1")
	if err := draft.Validate(); err == nil {
		t.Fatal("empty draft accepted as evidence")
	}
}

func TestCorrectiveEvidenceDigestDeterministic(t *testing.T) {
	b := validEvidence(t, "sv-run-1")
	if err := b.AddRef(shadowpilot.EvidenceRef{Kind: shadowpilot.EvidenceAuditResult, ID: "audit-1"}); err != nil {
		t.Fatal(err)
	}
	manifest := []shadowpilot.EvidenceRef{b.Manifest[1], b.Manifest[0]}
	digest, err := shadowpilot.EvidenceBundleDigest(b.ShadowRunID, manifest, b.Provenance)
	if err != nil || digest != b.BundleDigest {
		t.Fatalf("unstable digest: %s %v", digest, err)
	}
	if err := b.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestCorrectiveReportRequiresEvidence(t *testing.T) {
	for _, mode := range []string{"missing", "foreign run", "tampered digest"} {
		t.Run(mode, func(t *testing.T) {
			r := buildReport(t)
			switch mode {
			case "missing":
				r.Evidence = shadowpilot.EvidenceBundle{}
			case "foreign run":
				r.Evidence = validEvidence(t, "other-run")
			case "tampered digest":
				r.Evidence.BundleDigest = "forged"
			}
			if err := r.Validate(); err == nil {
				t.Fatal("invalid report evidence accepted")
			}
		})
	}
}

func TestCorrectiveBaselineDigest(t *testing.T) {
	for _, field := range []string{"digest", "finding", "input", "subject"} {
		t.Run(field, func(t *testing.T) {
			b, err := shadowpilot.NewBaselineResult(sha1, "input-1", []shadowpilot.Finding{{ID: "f1", Summary: "baseline"}}, t0)
			if err != nil {
				t.Fatal(err)
			}
			if err := b.Validate(); err != nil {
				t.Fatal(err)
			}
			switch field {
			case "digest":
				b.Digest = "forged"
			case "finding":
				b.Findings[0].Summary = "planted finding"
			case "input":
				b.InputDigest = "other-input"
			case "subject":
				b.SubjectSHA = sha2
			}
			if err := b.Validate(); err == nil {
				t.Fatal("tampered baseline accepted")
			}
		})
	}
}
