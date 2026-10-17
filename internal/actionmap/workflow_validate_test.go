package actionmap

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func workflowFixture(t *testing.T) (WorkflowCatalog, CompletionPolicy, ActionMap) {
	t.Helper()
	load := func(name string) []byte {
		b, err := os.ReadFile(filepath.Join("../../docs/operations", name))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	am, err := decodeWorkflow[ActionMap](strings.NewReader(string(load("ACTION-MAP.yaml"))))
	if err != nil {
		t.Fatal(err)
	}
	c, err := LoadWorkflowCatalog(strings.NewReader(string(load("COORDINATOR-WORKFLOWS.yaml"))), am)
	if err != nil {
		t.Fatal(err)
	}
	p, err := LoadCompletionPolicy(strings.NewReader(string(load("COORDINATOR-COMPLETION-POLICY.yaml"))), c, am)
	if err != nil {
		t.Fatal(err)
	}
	return c, p, am
}

func TestWorkflowUsesKnownCanonicalReferences(t *testing.T) {
	c, _, am := workflowFixture(t)
	if len(c.Workflows) != 15 {
		t.Fatal("catalog lost a source scenario")
	}
	for _, w := range c.Workflows {
		if _, ok := am.Events[w.CanonicalEvent]; !ok {
			t.Fatal("unknown event")
		}
		if !canonicalTransition(am, w.CanonicalTransition) {
			t.Fatal("unknown transition")
		}
	}
	if _, ok := am.Events[string(ObservationAgentResponse)]; ok {
		t.Fatal("internal observation became canonical event")
	}
	if canonicalTransition(am, string(OutcomeStaleResponseRejected)) {
		t.Fatal("workflow outcome became canonical transition")
	}
}

func TestObservationCannotBeUsedAsCanonicalEvent(t *testing.T) {
	c, _, am := workflowFixture(t)
	c.Workflows[0].CanonicalEvent = string(ObservationAgentResponse)
	if err := ValidateWorkflowCatalog(c, am); err == nil {
		t.Fatal("observation accepted as canonical event")
	}
}

func TestOutcomeCannotBeUsedAsCanonicalTransition(t *testing.T) {
	c, _, am := workflowFixture(t)
	c.Workflows[0].CanonicalTransition = string(OutcomeStaleResponseRejected)
	if err := ValidateWorkflowCatalog(c, am); err == nil {
		t.Fatal("workflow outcome accepted as canonical transition")
	}
}

func TestWorkflowStructuralFailures(t *testing.T) {
	cases := map[string]func(*WorkflowCatalog){
		"unknown_transition":  func(c *WorkflowCatalog) { c.Workflows[0].CanonicalTransition = "IMPROVISED_TRANSITION" },
		"unknown_observation": func(c *WorkflowCatalog) { c.Workflows[0].Observation = "UNKNOWN_OBSERVATION" },
		"unknown_outcome":     func(c *WorkflowCatalog) { c.Workflows[0].Outcome.Code = "UNKNOWN_OUTCOME" },
		"namespace_event_overlap": func(c *WorkflowCatalog) {
			c.Identifiers.Observations[0].Kind = "CURRENT_ISSUE_CLOSED"
			c.Workflows[0].Observation = "CURRENT_ISSUE_CLOSED"
		},
		"namespace_outcome_overlap": func(c *WorkflowCatalog) {
			c.Identifiers.Outcomes[0].Kind = WorkflowOutcomeCode(c.Identifiers.Observations[0].Kind)
			c.Workflows[0].Outcome.Code = c.Identifiers.Outcomes[0].Kind
		},
		"priority_tie":                func(c *WorkflowCatalog) { c.Workflows[2].Priority = c.Workflows[1].Priority },
		"duplicate_id":                func(c *WorkflowCatalog) { c.Workflows[1].ID = c.Workflows[0].ID },
		"unknown_action":              func(c *WorkflowCatalog) { c.Workflows[0].Actions[0] = "run_shell" },
		"missing_lower_generation":    func(c *WorkflowCatalog) { c.Workflows[2].When.All = c.Workflows[2].When.All[:1] },
		"missing_assignment_mismatch": func(c *WorkflowCatalog) { c.Workflows[2].When.All = c.Workflows[2].When.All[1:] },
		"stale_revokes_current":       func(c *WorkflowCatalog) { c.Workflows[2].Actions[2] = "revoke_current_assignment" },
		"stale_replaces_session":      func(c *WorkflowCatalog) { c.Workflows[2].Actions[3] = "replace_current_session" },
		"replacement_not_independent": func(c *WorkflowCatalog) {
			for i, v := range c.Workflows[1].When.All {
				if v.Fact == "replacement.independent" {
					c.Workflows[1].When.All[i].Value = workflowLiteral(BoolValue(false))
				}
			}
		},
		"replacement_missing_stop_proof": func(c *WorkflowCatalog) {
			for i, v := range c.Workflows[1].When.All {
				if v.Fact == "stopProof.satisfied" {
					c.Workflows[1].When.All = append(c.Workflows[1].When.All[:i], c.Workflows[1].When.All[i+1:]...)
					break
				}
			}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			c, _, am := workflowFixture(t)
			mutate(&c)
			if err := ValidateWorkflowCatalog(c, am); err == nil {
				t.Fatalf("invalid workflow accepted: %s", name)
			}
		})
	}
}

func TestFreeFormPredicateRejected(t *testing.T) {
	// Retain a valid typed guard alongside the injected expression, so ignoring
	// unknown fields cannot appear safe merely because all guards disappeared.
	data, err := os.ReadFile("../../docs/operations/COORDINATOR-WORKFLOWS.yaml")
	if err != nil {
		t.Fatal(err)
	}
	_, _, am := workflowFixture(t)
	text := strings.Replace(string(data), "  when:\n", "  when:\n    expression: arbitrary_code()\n", 1)
	if text == string(data) {
		t.Fatal("fixture anchor missing")
	}
	if _, err = LoadWorkflowCatalog(strings.NewReader(text), am); err == nil {
		t.Fatal("free-form predicate accepted")
	}
	for _, suffix := range []string{"\n---\nkind: extra\n", "\nkind: duplicate\n"} {
		if _, err = LoadWorkflowCatalog(strings.NewReader(string(data)+suffix), am); err == nil {
			t.Fatal("extra/duplicate document accepted")
		}
	}
}

func TestTypedConditionValidated(t *testing.T) {
	c, _, _ := workflowFixture(t)
	groups := []struct {
		condition Condition
		facts     Facts
		want      bool
	}{
		{Condition{Fact: "replacement.eligible", Operator: "EQ", Value: workflowLiteral(BoolValue(true))}, Facts{"replacement.eligible": BoolValue(true)}, true},
		{Condition{Fact: "observedResponse.generation", Operator: "LT", ValueFrom: "currentAssignment.generation"}, Facts{"observedResponse.generation": NumberValue(1), "currentAssignment.generation": NumberValue(2)}, true},
		{Condition{Fact: "observedResponse.assignmentId", Operator: "SAME_ASSIGNMENT", ValueFrom: "currentAssignment.id"}, Facts{"observedResponse.assignmentId": TextValue("a"), "currentAssignment.id": TextValue("a")}, true},
		{Condition{Fact: "evidence.refs", Operator: "CONTAINS", Value: workflowLiteral(TextValue("e1"))}, Facts{"evidence.refs": ListValue("e1", "e2")}, true},
		{Condition{Fact: "futurePromise", Operator: "EMPTY"}, Facts{"futurePromise": TextValue("later")}, false},
	}
	for _, tc := range groups {
		got, err := EvaluateConditions(Conditions{All: []Condition{tc.condition}}, tc.facts, c)
		if err != nil || got != tc.want {
			t.Fatalf("typed condition result=%v error=%v", got, err)
		}
	}
	if _, err := EvaluateConditions(Conditions{All: []Condition{groups[0].condition}}, Facts{}, c); !errors.Is(err, ErrFactMissing) {
		t.Fatalf("missing fact accepted: %v", err)
	}
	for _, v := range []Value{TextValue("true"), NumberValue(1)} {
		if err := validateCondition(Condition{Fact: "replacement.eligible", Operator: "EQ", Value: workflowLiteral(v)}, c); err == nil {
			t.Fatal("wrong fact type accepted")
		}
	}
	fixtures, err := filepath.Glob("../../docs/operations/testdata/coordinator-workflows/*.yaml")
	if err != nil || len(fixtures) == 0 {
		t.Fatal("negative fixtures missing")
	}
	for _, path := range fixtures {
		t.Run(filepath.Base(path), func(t *testing.T) {
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			g, err := decodeWorkflow[Conditions](strings.NewReader(string(b)))
			if err == nil {
				err = validateConditions(g, c)
			}
			if err == nil {
				t.Fatal("negative condition fixture accepted")
			}
		})
	}
}

func TestStaleResponsePreservesCurrentAssignmentAndSession(t *testing.T) {
	c, _, am := workflowFixture(t)
	a := CurrentAssignment{ID: "current", SessionID: "s2", Generation: 2, SubjectSHA: strings.Repeat("a", 40), State: "RUNNING"}
	s := CurrentSession{ID: "s2", Generation: 2, State: "RUNNING"}
	o := AgentResponseObservation{ObservationID: "obs-1", AssignmentID: "old", SessionID: "s1", Generation: 1, SubjectSHA: strings.Repeat("b", 40), ResultType: "AgentResult", ResultDigest: strings.Repeat("c", 64), ObservedAt: time.Unix(100, 0)}
	got, err := ExecuteStaleResponse(c, am, o, a, s)
	if err != nil || got.Outcome.Code != OutcomeStaleResponseRejected {
		t.Fatalf("stale lower-generation response not rejected: %v", err)
	}
	if got.Assignment != a {
		t.Fatal("stale response mutated current assignment")
	}
	if got.Session != s {
		t.Fatal("stale response replaced current session")
	}
	if got.Outcome.AssignmentID != o.AssignmentID || got.Outcome.Generation != o.Generation || got.Outcome.SessionID != o.SessionID {
		t.Fatal("historical outcome attached to current binding")
	}
	for _, generation := range []uint64{2, 3} {
		t.Run(fmtGeneration(generation), func(t *testing.T) {
			bad := o
			bad.Generation = generation
			result, err := ExecuteStaleResponse(c, am, bad, a, s)
			if !errors.Is(err, ErrResponseCorrelation) {
				t.Fatalf("same/newer generation accepted as stale: %v", err)
			}
			if result.Assignment != a || result.Session != s {
				t.Fatal("invalid observation changed current state")
			}
		})
	}
}

func fmtGeneration(v uint64) string {
	if v == 2 {
		return "same_generation"
	}
	return "newer_generation"
}

func TestCurrentAssignmentFailureMayTriggerReplacement(t *testing.T) {
	c, _, am := workflowFixture(t)
	facts := Facts{"assignment.admission": TextValue("NOT_CONFIRMED"), "assignment.admissionTimedOut": BoolValue(true), "replacement.eligible": BoolValue(true), "replacement.independent": BoolValue(true), "fence.accepted": BoolValue(true), "stopProof.satisfied": BoolValue(true)}
	w, err := SelectWorkflow(c, am, "AGENT_ASSIGNMENT_DISPATCHED", "ASSIGNMENT_DELIVERY_OBSERVED", facts)
	if err != nil || w.ID != "replace-non-admitted-session" {
		t.Fatalf("replacement not selected: %v", err)
	}
	for _, f := range []FactID{"assignment.admissionTimedOut", "replacement.independent", "stopProof.satisfied"} {
		original := facts[f]
		facts[f] = BoolValue(false)
		if _, err := SelectWorkflow(c, am, "AGENT_ASSIGNMENT_DISPATCHED", "ASSIGNMENT_DELIVERY_OBSERVED", facts); !errors.Is(err, ErrNoMatchingWorkflow) {
			t.Fatalf("replacement allowed without %s: %v", f, err)
		}
		facts[f] = original
	}
}

func TestGeneratedProjectionMatchesCatalog(t *testing.T) {
	c, _, _ := workflowFixture(t)
	data, err := os.ReadFile("../../docs/operations/COORDINATOR-WORKFLOWS.md")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != RenderWorkflowMarkdown(c) {
		t.Fatal("generated projection does not match YAML")
	}
}

func validCompletionFacts() Facts {
	return Facts{
		"deterministic.available": BoolValue(false), "deterministic.executed": BoolValue(false),
		"readyAssignment.exists": BoolValue(false), "readyAssignment.dispatched": BoolValue(false),
		"assignment.delivery": TextValue("NONE"), "assignment.reportedAdmitted": BoolValue(false), "assignment.admission": TextValue("NOT_CONFIRMED"),
		"session.reportedRunning": BoolValue(false), "session.heartbeatStale": BoolValue(false), "session.reconcileAttempted": BoolValue(false),
		"human.required": BoolValue(false), "automation.available": BoolValue(false), "replacement.eligible": BoolValue(false),
		"human.commandPrepared": BoolValue(false), "human.postCheckPrepared": BoolValue(false),
		"completion.requested": BoolValue(true), "confirmation.requested": BoolValue(false), "futurePromise": TextValue(""),
		"independentWork.available": BoolValue(false), "independentWork.performed": BoolValue(false),
		"workflow.lastOutcome": TextValue("NONE"), "currentAssignment.stateChanged": BoolValue(false), "currentSession.replaced": BoolValue(false),
	}
}

func TestCompletionPolicyEnforcesTypedGuards(t *testing.T) {
	c, p, am := workflowFixture(t)
	if err := EvaluateCompletionPolicy(p, c, am, validCompletionFacts()); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		reason  string
		changes Facts
	}{
		{"DETERMINISTIC_TRANSITION_NOT_EXECUTED", Facts{"deterministic.available": BoolValue(true)}},
		{"READY_ASSIGNMENT_NOT_DISPATCHED", Facts{"readyAssignment.exists": BoolValue(true)}},
		{"DELIVERED_NOT_ADMITTED", Facts{"assignment.delivery": TextValue("DELIVERED"), "assignment.reportedAdmitted": BoolValue(true)}},
		{"QUEUED_NOT_RUNNING", Facts{"assignment.delivery": TextValue("QUEUED"), "session.reportedRunning": BoolValue(true)}},
		{"SESSION_RECONCILE_REQUIRED", Facts{"session.heartbeatStale": BoolValue(true)}},
		{"AUTOMATION_PATH_AVAILABLE", Facts{"human.required": BoolValue(true), "automation.available": BoolValue(true)}},
		{"ELIGIBLE_REPLACEMENT_AVAILABLE", Facts{"human.required": BoolValue(true), "replacement.eligible": BoolValue(true)}},
		{"HUMAN_ACTION_COMMAND_MISSING", Facts{"human.required": BoolValue(true)}},
		{"HUMAN_ACTION_POST_CHECK_MISSING", Facts{"human.required": BoolValue(true), "human.commandPrepared": BoolValue(true)}},
		{"UNSUPPORTED_CONFIRMATION_REQUEST", Facts{"confirmation.requested": BoolValue(true)}},
		{"PASSIVE_FUTURE_PROMISE", Facts{"futurePromise": TextValue("later")}},
		{"INDEPENDENT_WORK_NOT_PERFORMED", Facts{"independentWork.available": BoolValue(true)}},
		{"STALE_RESPONSE_MUTATED_CURRENT_ASSIGNMENT", Facts{"workflow.lastOutcome": TextValue("STALE_RESPONSE_REJECTED"), "currentAssignment.stateChanged": BoolValue(true)}},
	}
	for _, tc := range cases {
		t.Run(tc.reason, func(t *testing.T) {
			facts := validCompletionFacts()
			for k, v := range tc.changes {
				facts[k] = v
			}
			err := EvaluateCompletionPolicy(p, c, am, facts)
			if err == nil || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("missing completion guard %s: %v", tc.reason, err)
			}
		})
	}
	changed := p
	changed.Guards = append([]CompletionGuard(nil), p.Guards[1:]...)
	if err := ValidateCompletionPolicy(changed, c, am); err == nil {
		t.Fatal("critical guard removed")
	}
}

func humanBoundaryFixture(t *testing.T) (CompletionContext, CompletionDecision) {
	now := time.Unix(100, 0)
	o := BoundaryObservation{ID: "proof", Source: "CANONICAL_OBSERVATION", Digest: strings.Repeat("a", 64), ActionID: "action", AssignmentID: "assignment", SubjectSHA: strings.Repeat("b", 40), ObservedAt: now.Add(-time.Second), ValidUntil: now.Add(time.Minute), ReasonCode: "SYSTEM_DIALOG_REQUIRED", EvidenceRefs: []string{"canonical-event-1"}, DecisionAlreadyAccepted: true, OwnerBoundaryAbsent: true, Automation: AvailabilityUnavailable, Replacement: AvailabilityUnavailable, InternalRemedy: AvailabilityUnavailable, PhysicalActionRequired: true}
	ctx := CompletionContext{Now: now, Observations: map[string]BoundaryObservation{"proof": o}, Facts: validCompletionFacts(), Evidence: map[string]BoundaryEvidence{"canonical-event-1": {ID: "canonical-event-1", Source: "CANONICAL_EVENT", Digest: strings.Repeat("e", 64), ActionID: o.ActionID, AssignmentID: o.AssignmentID, SubjectSHA: o.SubjectSHA}}}
	d := CompletionDecision{HumanActionRequired: true, ObservationID: "proof", PendingActionID: "action", PendingAssignmentID: "assignment", SubjectSHA: o.SubjectSHA, ReasonCode: o.ReasonCode, HumanAction: HumanActionCard{Command: "approved-client confirm action", WorkingDirectory: "/project", ExpectedResult: "confirmed", PostChecks: []string{"observe canonical receipt"}, FailureTransition: "BLOCKED_EXTERNAL", Continuation: "reconcile receipt"}}
	digest, err := HumanActionDigest(d)
	if err != nil {
		t.Fatal(err)
	}
	o.RequestDigest = digest
	ctx.Observations["proof"] = o
	return ctx, d
}

func TestHumanBoundaryRequiresNoAutomationOrReplacement(t *testing.T) {
	c, p, am := workflowFixture(t)
	ctx, d := humanBoundaryFixture(t)
	if err := ValidateCompletionDecision(p, c, am, ctx, d); err != nil {
		t.Fatal(err)
	}
	for _, which := range []string{"automation", "replacement", "unknown", "missing_command", "missing_postcheck", "stale_proof", "wrong_subject", "unsupported_reason", "owner_boundary_not_absent", "dangling_evidence", "changed_command", "changed_post_check"} {
		t.Run(which, func(t *testing.T) {
			ctx, d := humanBoundaryFixture(t)
			o := ctx.Observations["proof"]
			var want error
			switch which {
			case "automation":
				o.Automation = AvailabilityAvailable
				want = ErrAutomationPathAvailable
			case "replacement":
				o.Replacement = AvailabilityAvailable
				want = ErrEligibleReplacementAvailable
			case "unknown":
				o.Replacement = AvailabilityUnknown
				want = ErrBoundaryNotProven
			case "missing_command":
				d.HumanAction.Command = ""
				digest, err := HumanActionDigest(d)
				if err != nil {
					t.Fatal(err)
				}
				o.RequestDigest = digest
				want = ErrHumanActionCommandMissing
			case "missing_postcheck":
				d.HumanAction.PostChecks = nil
				want = ErrHumanActionPostCheckMissing
			case "stale_proof":
				ctx.Now = o.ValidUntil
				want = ErrBoundaryNotProven
			case "wrong_subject":
				d.SubjectSHA = strings.Repeat("c", 40)
				want = ErrBoundaryNotProven
			case "unsupported_reason":
				o.ReasonCode = "EXTERNAL_SYSTEM_WRITE_REQUIRED"
				d.ReasonCode = o.ReasonCode
				want = ErrBoundaryNotProven
			case "owner_boundary_not_absent":
				o.OwnerBoundaryAbsent = false
				want = ErrBoundaryNotProven
			case "dangling_evidence":
				ctx.Evidence = nil
				want = ErrBoundaryNotProven
			case "changed_command":
				d.HumanAction.Command = "different action"
				want = ErrBoundaryNotProven
			case "changed_post_check":
				d.HumanAction.PostChecks = []string{"different post-check"}
				want = ErrBoundaryNotProven
			}
			ctx.Observations["proof"] = o
			if err := ValidateHumanBoundary(ctx, d, am); !errors.Is(err, want) {
				t.Fatalf("human admission bypassed %s: %v", which, err)
			}
		})
	}
	ctx.Facts["futurePromise"] = TextValue("later")
	if err := ValidateCompletionDecision(p, c, am, ctx, d); err == nil || !strings.Contains(err.Error(), "PASSIVE_FUTURE_PROMISE") {
		t.Fatalf("valid boundary bypassed completion: %v", err)
	}
}

func TestOwnerBoundaryRequiresAcceptedReasonAndEvidence(t *testing.T) {
	c, p, am := workflowFixture(t)
	ctx, d := humanBoundaryFixture(t)
	d.HumanActionRequired = false
	d.OwnerRequired = true
	d.ReasonCode = "FAIL_CLOSED_WEAKENING"
	o := ctx.Observations["proof"]
	o.ReasonCode = d.ReasonCode
	o.NormRef = "R142"
	o.NormDigest = strings.Repeat("d", 64)
	o.NormStatus = "ACCEPTED"
	o.EvidenceRefs = append(o.EvidenceRefs, "norm-proof")
	ctx.Evidence["norm-proof"] = BoundaryEvidence{ID: "norm-proof", Source: "ACCEPTED_NORM", Digest: o.NormDigest, ActionID: o.ActionID, AssignmentID: o.AssignmentID, SubjectSHA: o.SubjectSHA, NormRef: o.NormRef, NormStatus: "ACCEPTED"}
	ctx.Observations["proof"] = o
	if err := ValidateCompletionDecision(p, c, am, ctx, d); err != nil {
		t.Fatal(err)
	}
	for _, which := range []string{"missing_reason", "missing_norm", "proposed_norm", "empty_evidence"} {
		t.Run(which, func(t *testing.T) {
			bad := o
			decision := d
			switch which {
			case "missing_reason":
				bad.ReasonCode = ""
				decision.ReasonCode = ""
			case "missing_norm":
				bad.NormRef = ""
			case "proposed_norm":
				bad.NormStatus = "PROPOSED"
			case "empty_evidence":
				bad.EvidenceRefs = nil
			}
			ctx.Observations["proof"] = bad
			if err := ValidateCompletionDecision(p, c, am, ctx, decision); !errors.Is(err, ErrOwnerBoundaryNotProven) {
				t.Fatalf("owner accepted without proven reason: %v", err)
			}
		})
	}
}

func TestExternalBlockerRequiresObservedFailure(t *testing.T) {
	c, p, am := workflowFixture(t)
	ctx, d := humanBoundaryFixture(t)
	d.HumanActionRequired = false
	d.BlockedExternal = true
	o := ctx.Observations["proof"]
	o.ExternalUnavailable = true
	o.PhysicalActionRequired = false
	o.ReasonCode = "REGISTRY_UNAVAILABLE"
	d.ReasonCode = o.ReasonCode
	ctx.Observations["proof"] = o
	if err := ValidateCompletionDecision(p, c, am, ctx, d); err != nil {
		t.Fatal(err)
	}
	o.ExternalUnavailable = false
	ctx.Observations["proof"] = o
	if err := ValidateCompletionDecision(p, c, am, ctx, d); !errors.Is(err, ErrBoundaryNotProven) {
		t.Fatalf("external blocker accepted without observation: %v", err)
	}
}
