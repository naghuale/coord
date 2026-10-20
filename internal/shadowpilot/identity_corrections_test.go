package shadowpilot_test

import (
	"strings"
	"testing"

	"coord/internal/shadowpilot"
)

func identityAliases(id string) []string {
	return []string{" " + id, id + " ", "\u00a0" + id + "\u00a0", "\t" + id, id + "\n", strings.ToUpper(id)}
}

func TestIdentityCorrectionResultBoundaries(t *testing.T) {
	for _, field := range []string{"review reviewer", "review executor", "audit auditor", "audit executor", "audit reviewer"} {
		for _, alias := range identityAliases("agent-independent") {
			t.Run(field+"/"+alias, func(t *testing.T) {
				review, audit := validReviewResult(), validAuditResult()
				var err error
				switch field {
				case "review reviewer":
					review.ReviewerID = alias
					err = review.Validate()
				case "review executor":
					review.ExecutorID = alias
					err = review.Validate()
				case "audit auditor":
					audit.AuditorID = alias
					err = audit.Validate()
				case "audit executor":
					audit.ExecutorID = alias
					err = audit.Validate()
				case "audit reviewer":
					audit.ReviewerID = alias
					err = audit.Validate()
				}
				if err == nil || !strings.Contains(err.Error(), "agent identity must be lowercase") {
					t.Fatalf("noncanonical %s accepted or wrong guard: %v", field, err)
				}
			})
		}
	}
}

func TestIdentityCorrectionReadinessRejectsIndependenceAliases(t *testing.T) {
	for _, condition := range []string{"reviewer equals executor", "auditor equals executor", "auditor equals reviewer"} {
		target := "agent-executor"
		if condition == "auditor equals reviewer" {
			target = "agent-reviewer"
		}
		for _, alias := range identityAliases(target) {
			t.Run(condition+"/"+alias, func(t *testing.T) {
				review, audit := validReviewResult(), validAuditResult()
				switch condition {
				case "reviewer equals executor":
					review.ReviewerID, audit.ReviewerID = alias, alias
				case "auditor equals executor":
					audit.AuditorID = alias
				case "auditor equals reviewer":
					audit.AuditorID = alias
				}
				got := shadowpilot.ComputeReadiness(&review, &audit, readinessAssignment(sha1))
				if got.Readiness != shadowpilot.ShadowReadinessBlocked || !strings.Contains(got.Reason, "agent identity must be lowercase") {
					t.Fatalf("readiness = %+v, want canonical identity rejection", got)
				}
			})
		}
	}
}

func TestIdentityCorrectionAdmissionRejectsAliases(t *testing.T) {
	a := validAssignment()
	for _, id := range []string{a.AgentID, "agent-reviewer"} {
		for _, alias := range identityAliases(id) {
			t.Run(alias, func(t *testing.T) {
				got := shadowpilot.Admit(a, a.SubjectSHA, []string{alias})
				if got.Outcome != shadowpilot.AdmissionRejected || got.Violation == nil || !strings.Contains(got.Violation.Detail, "agent identity must be lowercase") {
					t.Fatalf("admission = %+v, want canonical reviewer identity rejection", got)
				}
			})
		}
	}
	for _, alias := range identityAliases(a.AgentID) {
		bad := a
		bad.AgentID = alias
		if err := bad.Validate(); err == nil {
			t.Fatalf("assignment accepted alias %q", alias)
		}
		if got := shadowpilot.Admit(bad, bad.SubjectSHA, []string{"agent-reviewer"}); got.Outcome != shadowpilot.AdmissionRejected {
			t.Fatalf("admission accepted executor alias %q", alias)
		}
	}
}

func TestIdentityCorrectionCheckpointRejectsReviewerAliases(t *testing.T) {
	for _, alias := range identityAliases("agent-reviewer") {
		a := readinessAssignment(sha1)
		review, audit := validReviewResult(), validAuditResult()
		audit.ReviewerID = alias
		cp := shadowpilot.ShadowCheckpoint{Assignment: &a, ReviewResult: &review, AuditResult: &audit}
		if err := shadowpilot.ValidateCheckpointIntegrity(cp); err == nil || !strings.Contains(err.Error(), "agent identity must be lowercase") {
			t.Fatalf("checkpoint alias %q: %v; want rejection at identity boundary", alias, err)
		}
		if plan := shadowpilot.PlanRecovery(cp, nil); !plan.Is(shadowpilot.RecoveryPlanBlocked) {
			t.Fatalf("checkpoint alias %q: recovery = %+v", alias, plan)
		}
	}
}

func TestIdentityCorrectionCheckpointValidatesDiscussionNodes(t *testing.T) {
	node := shadowpilot.ShadowDiscussionNode{NodeID: "node-1", Author: "agent-reviewer", Role: "reviewer", Body: "rationale", ResultRef: "review-result-001", CreatedAt: t0}
	a := validAssignment()
	for _, assignment := range []*shadowpilot.ShadowAssignment{nil, &a} {
		cp := shadowpilot.ShadowCheckpoint{Assignment: assignment, DiscussionNodes: []shadowpilot.ShadowDiscussionNode{node, node}}
		if err := shadowpilot.ValidateCheckpointIntegrity(cp); err != nil {
			t.Fatalf("valid nodes rejected: %v", err)
		}
		cp.DiscussionNodes[1].NodeID = ""
		if err := shadowpilot.ValidateCheckpointIntegrity(cp); err == nil || !strings.Contains(err.Error(), "discussion node[1]: discussion node: node_id required") {
			t.Fatalf("invalid second node: %v", err)
		}
		if plan := shadowpilot.PlanRecovery(cp, nil); !plan.Is(shadowpilot.RecoveryPlanBlocked) {
			t.Fatalf("invalid discussion node recovery = %+v", plan)
		}
	}
}
