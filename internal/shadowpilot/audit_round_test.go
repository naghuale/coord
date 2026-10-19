package shadowpilot_test

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"coord/internal/shadowpilot"
)

func auditCheckpoint() shadowpilot.ShadowCheckpoint {
	a := crashAssignment()
	r, audit := validReviewResult(), validAuditResult()
	r.Generation, audit.Generation = a.Generation, a.Generation
	return shadowpilot.ShadowCheckpoint{Assignment: &a, Execution: completedExecution(), ReviewResult: &r, AuditResult: &audit}
}

func TestAuditRoundOpenAuditFindingsBlockReadiness(t *testing.T) {
	r, a := validReviewResult(), validAuditResult()
	a.Findings = []shadowpilot.ShadowFinding{{ID: "audit-f1", Severity: shadowpilot.FindingBlocking, Status: shadowpilot.FindingOpen, Summary: "unresolved audit defect", CreatedAt: t0}}
	if !a.HasOpenBlockingFindings() {
		t.Fatal("audit must expose open blocking findings")
	}
	got := shadowpilot.ComputeReadiness(&r, &a, readinessAssignment(sha1))
	if got.Readiness != shadowpilot.ShadowReadinessBlocked || !strings.Contains(got.Reason, "audit has open blocking") {
		t.Fatalf("audit PASS must not erase blocking finding: %+v", got)
	}
	a.Findings[0].Severity = shadowpilot.FindingNonBlocking
	if a.HasOpenBlockingFindings() {
		t.Fatal("nonblocking finding classified as blocking")
	}
	if got := shadowpilot.ComputeReadiness(&r, &a, readinessAssignment(sha1)); got.Readiness != shadowpilot.ShadowReadinessReady {
		t.Fatalf("nonblocking finding: %+v", got)
	}
}

func TestAuditRoundConstructorEnforcesAdmission(t *testing.T) {
	a := validAssignment()
	for _, tc := range []struct {
		name, actual string
		reviewers    []string
	}{
		{"reviewer equals executor", a.SubjectSHA, []string{a.AgentID}},
		{"foreign subject", sha2, []string{"agent-reviewer"}},
		{"missing reviewers", a.SubjectSHA, nil},
		{"empty reviewer", a.SubjectSHA, []string{""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := shadowpilot.NewShadowExecution(a, tc.actual, tc.reviewers)
			if err == nil {
				t.Fatal("constructor admitted a rejected assignment")
			}
			if e.State == shadowpilot.ExecutorAdmitted || e.State == shadowpilot.ExecutorRunning {
				t.Fatalf("rejected constructor returned admitted execution: %+v", e)
			}
			if err := e.Start(t0); err == nil {
				t.Fatal("rejected assignment reached RUNNING")
			}
		})
	}
	e, err := shadowpilot.NewShadowExecution(a, a.SubjectSHA, []string{"agent-reviewer"})
	if err != nil {
		t.Fatal(err)
	}
	if e.Admission.Outcome != shadowpilot.AdmissionAdmitted || e.Admission.Violation != nil {
		t.Fatalf("missing persisted admission: %+v", e.Admission)
	}
	if err := e.Start(t0); err != nil {
		t.Fatal(err)
	}
}

func TestAuditRoundStartRequiresRecordedAdmission(t *testing.T) {
	e := shadowpilot.ShadowExecution{Assignment: validAssignment(), State: shadowpilot.ExecutorAdmitted}
	before := e
	if err := e.Start(t0); err == nil {
		t.Fatal("state label without admission proof reached RUNNING")
	}
	if !reflect.DeepEqual(before, e) {
		t.Fatal("rejected start mutated execution")
	}
}

func TestAuditRoundPlannerWiresIntegrity(t *testing.T) {
	for _, mode := range []string{"foreign review", "superseded review", "foreign and superseded review", "superseded audit"} {
		t.Run(mode, func(t *testing.T) {
			cp := auditCheckpoint()
			if got := shadowpilot.PlanRecovery(cp, nil); !got.Is(shadowpilot.RecoveryReadinessOnly) {
				t.Fatalf("invalid baseline: %+v", got)
			}
			switch mode {
			case "foreign review":
				cp.ReviewResult.SubjectSHA = sha2
			case "superseded review":
				cp.ReviewResult.Superseded = true
			case "foreign and superseded review":
				cp.ReviewResult.SubjectSHA = sha2
				cp.ReviewResult.Superseded = true
			case "superseded audit":
				cp.AuditResult.Superseded = true
			}
			if err := shadowpilot.ValidateCheckpointIntegrity(cp); err == nil {
				t.Fatal("invalid checkpoint accepted")
			}
			if got := shadowpilot.PlanRecovery(cp, nil); !got.Is(shadowpilot.RecoveryPlanBlocked) {
				t.Fatalf("planner resumed invalid graph: %+v", got)
			}
		})
	}
}

func TestAuditRoundReadinessBindsAssignment(t *testing.T) {
	for _, mode := range []string{"both agents", "review agent", "audit agent", "both generations", "review generation", "audit generation"} {
		t.Run(mode, func(t *testing.T) {
			r, a := validReviewResult(), validAuditResult()
			switch mode {
			case "both agents":
				r.ExecutorID = "someone-else"
				a.ExecutorID = "someone-else"
			case "review agent":
				r.ExecutorID = "someone-else"
			case "audit agent":
				a.ExecutorID = "someone-else"
			case "both generations":
				r.Generation++
				a.Generation++
			case "review generation":
				r.Generation++
			case "audit generation":
				a.Generation++
			}
			got := shadowpilot.ComputeReadiness(&r, &a, readinessAssignment(sha1))
			if got.Readiness != shadowpilot.ShadowReadinessBlocked || !strings.Contains(got.Reason, "match assignment") {
				t.Fatalf("unbound readiness pair: %+v", got)
			}
		})
	}
}

func TestAuditRoundCheckpointBindsReviewAudit(t *testing.T) {
	for _, field := range []string{"review agent", "review generation", "audit agent", "audit generation"} {
		t.Run(field, func(t *testing.T) {
			cp := auditCheckpoint()
			switch field {
			case "review agent":
				cp.ReviewResult.ExecutorID = "someone-else"
			case "review generation":
				cp.ReviewResult.Generation++
			case "audit agent":
				cp.AuditResult.ExecutorID = "someone-else"
			case "audit generation":
				cp.AuditResult.Generation++
			}
			if err := shadowpilot.ValidateCheckpointIntegrity(cp); err == nil {
				t.Fatal("foreign review/audit accepted")
			}
			if got := shadowpilot.PlanRecovery(cp, nil); !got.Is(shadowpilot.RecoveryPlanBlocked) {
				t.Fatalf("planner accepted foreign pair: %+v", got)
			}
		})
	}
}

func TestAuditRoundCheckpointBindsExecution(t *testing.T) {
	for _, source := range []string{"execution assignment", "result"} {
		for _, field := range []string{"assignment", "agent", "session", "input", "subject", "generation"} {
			t.Run(source+"/"+field, func(t *testing.T) {
				cp := auditCheckpoint()
				if source == "result" {
					r := cp.Execution.Result
					switch field {
					case "assignment":
						r.AssignmentID = "other"
					case "agent":
						r.AgentID = "other"
					case "session":
						r.SessionID = "other"
					case "input":
						r.InputDigest = "other"
					case "subject":
						r.SubjectSHA = sha2
					case "generation":
						r.Generation++
					}
				} else {
					a := &cp.Execution.Assignment
					switch field {
					case "assignment":
						a.AssignmentID = "other"
					case "agent":
						a.AgentID = "other"
					case "session":
						a.SessionID = "other"
					case "input":
						a.InputDigest = "other"
					case "subject":
						a.SubjectSHA = sha2
					case "generation":
						a.Generation++
					}
				}
				if err := shadowpilot.ValidateCheckpointIntegrity(cp); err == nil {
					t.Fatal("execution identity mismatch accepted")
				}
				if got := shadowpilot.PlanRecovery(cp, nil); !got.Is(shadowpilot.RecoveryPlanBlocked) {
					t.Fatalf("planner resumed foreign result: %+v", got)
				}
			})
		}
	}
}

func TestAuditRoundRuntimeEvidence(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*shadowpilot.RuntimeEvidence)
	}{
		{"no entry point", func(r *shadowpilot.RuntimeEvidence) { r.RealEntryPointUsed = false }},
		{"no adapter", func(r *shadowpilot.RuntimeEvidence) { r.AdapterKind = "" }},
		{"no mode", func(r *shadowpilot.RuntimeEvidence) { r.FakeOrLive = "" }},
		{"unknown mode", func(r *shadowpilot.RuntimeEvidence) { r.FakeOrLive = "UNKNOWN" }},
		{"unknown mode without adapter-mode implication", func(r *shadowpilot.RuntimeEvidence) { r.AdapterKind = "custom-adapter"; r.FakeOrLive = "UNKNOWN" }},
		{"fake marked live", func(r *shadowpilot.RuntimeEvidence) { r.FakeOrLive = "live" }},
		{"live marked fake", func(r *shadowpilot.RuntimeEvidence) { r.AdapterKind = "shadow-live" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := cleanRuntime()
			tc.edit(&r)
			if err := r.Validate(); err == nil {
				t.Fatal("runtime evidence omission/contradiction accepted")
			}
			report := buildReport(t)
			report.Runtime = r
			if err := report.Validate(); err == nil {
				t.Fatal("report ignored invalid runtime evidence")
			}
		})
	}
}

func TestAuditRoundRecoveryAndWorkPackageEvidence(t *testing.T) {
	for _, count := range []int{0, -1} {
		r := cleanRecovery()
		r.CrashPointsTested = count
		if err := r.Validate(); err == nil {
			t.Fatal("missing crash evidence accepted")
		}
		report := buildReport(t)
		report.Recovery = r
		if err := report.Validate(); err == nil {
			t.Fatal("report ignored missing crash evidence")
		}
	}
	for _, mode := range []string{"name", "SHA", "nonhex SHA", "status", "version", "revision"} {
		t.Run(mode, func(t *testing.T) {
			r := buildReport(t)
			switch mode {
			case "name":
				r.WorkPackages[0].Name = ""
			case "SHA":
				r.WorkPackages[0].SHA = ""
			case "nonhex SHA":
				r.WorkPackages[0].SHA = strings.Repeat("z", 40)
			case "status":
				r.WorkPackages[0].Status = "UNKNOWN"
			case "version":
				r.CoordVersion = ""
			case "revision":
				r.CanonicalRevision = 0
			}
			if err := r.Validate(); err == nil {
				t.Fatal("incomplete report accepted")
			}
		})
	}
	wp := shadowpilot.WorkPackageEntry{Name: "pending", Status: shadowpilot.WPPending}
	if err := wp.Validate(); err != nil {
		t.Fatalf("pending WP cannot yet have an integration SHA: %v", err)
	}
}

func TestAuditRoundHexSubjects(t *testing.T) {
	validators := []struct {
		name     string
		validate func(string) error
	}{
		{"run", func(s string) error { r := validShadowRun(); r.SubjectSHA = s; return r.Validate() }},
		{"assignment", func(s string) error { a := validAssignment(); a.SubjectSHA = s; return a.Validate() }},
		{"agent result", func(s string) error { r := *completedExecution().Result; r.SubjectSHA = s; return r.Validate() }},
		{"baseline", func(s string) error { _, err := shadowpilot.NewBaselineResult(s, "input", nil, t0); return err }},
		{"review", func(s string) error { r := validReviewResult(); r.SubjectSHA = s; return r.Validate() }},
		{"audit", func(s string) error { a := validAuditResult(); a.SubjectSHA = s; return a.Validate() }},
		{"report", func(s string) error { r := buildReport(t); r.MainSHA = s; return r.Validate() }},
		{"work package", func(s string) error { w := allWPs()[0]; w.SHA = s; return w.Validate() }},
	}
	for _, v := range validators {
		t.Run(v.name, func(t *testing.T) {
			for _, s := range []string{strings.Repeat("z", 40), strings.Repeat("Z", 64)} {
				if err := v.validate(s); err == nil {
					t.Fatal("nonhex subject accepted")
				}
			}
			for _, s := range []string{strings.Repeat("aB", 20), strings.Repeat("CD", 32)} {
				if err := v.validate(s); err != nil {
					t.Fatalf("valid mixed-case hex rejected: %v", err)
				}
			}
		})
	}
}

func TestAuditRoundResultDigestOrdering(t *testing.T) {
	r := *completedExecution().Result
	r.Findings = []shadowpilot.Finding{{ID: "f2", Summary: "second"}, {ID: "f1", Summary: "first"}}
	before := append([]shadowpilot.Finding(nil), r.Findings...)
	a, err := shadowpilot.AgentResultDigest(r)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(r.Findings, before) {
		t.Fatal("digest mutated caller findings")
	}
	r.Findings = []shadowpilot.Finding{before[1], before[0]}
	b, err := shadowpilot.AgentResultDigest(r)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatal("finding insertion order changed digest")
	}
}

func TestAuditRoundRecommendationHasOneDecisionPath(t *testing.T) {
	for _, outcome := range []shadowpilot.ComparisonOutcome{shadowpilot.OutcomeMatch, shadowpilot.OutcomeSafeDivergence, shadowpilot.OutcomeUnsafeDivergence, shadowpilot.OutcomeNotEvaluated} {
		for _, review := range []bool{false, true} {
			for _, audit := range []bool{false, true} {
				for _, integrated := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/review=%v/audit=%v/integrated=%v", outcome, review, audit, integrated), func(t *testing.T) {
						c := allMatchComparison()
						c.Dimensions[0].Outcome = outcome
						c, err := shadowpilot.NewComparisonResult(c.ShadowRunID, c.Dimensions)
						if err != nil {
							t.Fatal(err)
						}
						wps := allWPs()
						if !integrated {
							wps[0].Status = shadowpilot.WPPending
						}
						r, err := shadowpilot.NewReport("pilot", sha1, "0.1", 1, reportT0, wps, cleanRuntime(), c, cleanRecovery(), review, audit, validEvidence(t, c.ShadowRunID))
						if err != nil {
							t.Fatal(err)
						}
						g := shadowpilot.P03AdmissionGates{ComparisonOutcome: c.Outcome, SafeDivergences: r.Comparison.SafeDivergences, UnsafeDivergences: r.Comparison.UnsafeDivergences, NotEvaluatedBlockingProperties: r.Comparison.NotEvaluated, AllWPsIntegrated: integrated, ReviewAccepted: review, AuditAccepted: audit}
						if got := shadowpilot.DeriveRecommendation(g); got != r.Recommendation {
							t.Fatalf("derivation paths disagree: exported=%s report=%s", got, r.Recommendation)
						}
						if outcome != shadowpilot.OutcomeMatch && r.Recommendation != shadowpilot.CorrectionRequired {
							t.Fatal("non-MATCH was not correction required")
						}
					})
				}
			}
		}
	}
	g := shadowpilot.P03AdmissionGates{AllWPsIntegrated: true, ReviewAccepted: true, AuditAccepted: true, SafeDivergences: 1}
	if got := shadowpilot.DeriveRecommendation(g); got != shadowpilot.CorrectionRequired {
		t.Fatalf("counter-only safe divergence admitted: %s", got)
	}
}
