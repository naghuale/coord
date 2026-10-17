package actionmap

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
)

var (
	ErrWorkflowInvalid              = errors.New("WORKFLOW_INVALID")
	ErrFactMissing                  = errors.New("WORKFLOW_FACT_MISSING")
	ErrNoMatchingWorkflow           = errors.New("NO_MATCHING_WORKFLOW")
	ErrResponseCorrelation          = errors.New("INVALID_RESPONSE_CORRELATION")
	ErrAutomationPathAvailable      = errors.New("AUTOMATION_PATH_AVAILABLE")
	ErrEligibleReplacementAvailable = errors.New("ELIGIBLE_REPLACEMENT_AVAILABLE")
	ErrHumanActionCommandMissing    = errors.New("HUMAN_ACTION_COMMAND_MISSING")
	ErrHumanActionPostCheckMissing  = errors.New("HUMAN_ACTION_POST_CHECK_MISSING")
	ErrOwnerBoundaryNotProven       = errors.New("OWNER_BOUNDARY_NOT_PROVEN")
	ErrBoundaryNotProven            = errors.New("BOUNDARY_NOT_PROVEN")
)

type factType struct {
	kind, identity string
	enum           []string
}

var workflowFacts = map[FactID]factType{
	"evidence.refs":                {kind: "collection"},
	"assignment.admission":         {kind: "string", enum: []string{"NOT_CONFIRMED", "ADMITTED"}},
	"assignment.delivery":          {kind: "string", enum: []string{"NONE", "DELIVERED", "QUEUED"}},
	"assignment.admissionTimedOut": {kind: "boolean"},
	"assignment.reportedAdmitted":  {kind: "boolean"},
	"replacement.eligible":         {kind: "boolean"}, "replacement.independent": {kind: "boolean"},
	"observedResponse.assignmentId": {kind: "string", identity: "assignment"},
	"currentAssignment.id":          {kind: "string", identity: "assignment"},
	"observedResponse.generation":   {kind: "integer", identity: "generation"},
	"currentAssignment.generation":  {kind: "integer", identity: "generation"},
	"observedResponse.subjectSha":   {kind: "string", identity: "subject"},
	"currentAssignment.subjectSha":  {kind: "string", identity: "subject"},
	"session.heartbeatStale":        {kind: "boolean"}, "session.reconcileAttempted": {kind: "boolean"},
	"session.reconcileFailed": {kind: "boolean"}, "currentGeneration.fenced": {kind: "boolean"},
	"session.executionBlocked": {kind: "boolean"}, "session.reportedRunning": {kind: "boolean"},
	"stopProof.satisfied": {kind: "boolean"}, "fence.accepted": {kind: "boolean"},
	"nextReadyIssue.exists": {kind: "boolean"}, "nextReadyIssue.scopeKnown": {kind: "boolean"},
	"boundaries.none": {kind: "boolean"}, "change.acceptedExactSubject": {kind: "boolean"},
	"change.touchesAgentInstructions": {kind: "boolean"}, "finding.withinAcceptedScope": {kind: "boolean"},
	"failure.technical": {kind: "boolean"}, "failure.withinAcceptedScope": {kind: "boolean"},
	"unknownChanges.staged": {kind: "boolean"}, "readOnlyClassificationSafe": {kind: "boolean"},
	"workspaceBinding.valid": {kind: "boolean"}, "preservationRisk": {kind: "string", enum: []string{"LOW", "HIGH"}},
	"human.boundaryVerified": {kind: "boolean"}, "owner.boundaryVerified": {kind: "boolean"},
	"external.blockerVerified": {kind: "boolean"},
	"workflow.lastOutcome":     {kind: "string"}, "currentAssignment.stateChanged": {kind: "boolean"},
	"currentSession.replaced": {kind: "boolean"}, "completion.requested": {kind: "boolean"},
	"deterministic.available": {kind: "boolean"}, "deterministic.executed": {kind: "boolean"},
	"readyAssignment.exists": {kind: "boolean"}, "readyAssignment.dispatched": {kind: "boolean"},
	"confirmation.requested": {kind: "boolean"}, "futurePromise": {kind: "string"},
	"independentWork.available": {kind: "boolean"}, "independentWork.performed": {kind: "boolean"},
	"human.required": {kind: "boolean"}, "automation.available": {kind: "boolean"},
	"human.commandPrepared": {kind: "boolean"}, "human.postCheckPrepared": {kind: "boolean"},
}

var workflowActions = map[WorkflowAction]bool{
	"record_stale_response_observation": true, "reject_response_for_current_assignment": true,
	"preserve_current_assignment": true, "preserve_current_session": true, "increment_stale_response_metric": true,
	"inspect_current_assignment": true, "verify_replacement_independence": true, "revoke_old_assignment_authority": true,
	"create_durable_replacement_intent": true, "dispatch_replacement": true, "verify_admission": true, "verify_running": true,
	"suspend_mutation_authority": true, "observe_external_session": true, "record_reconcile_obligation": true,
	"continue_independent_work": true, "commit_logical_fence": true, "verify_stop_proof": true,
	"prepare_human_action_card": true, "freeze_subject": true, "request_independent_review": true,
	"request_audit_if_required": true, "record_owner_decision_trace": true, "record_durable_blocker": true,
	"classify_findings": true, "create_correction_assignment": true, "repeat_required_gates": true,
	"capture_failed_ci_job": true, "classify_failure": true, "preserve_unknown_paths": true, "classify_read_only": true,
	"block_mutations": true, "record_snapshot": true, "create_assignment": true, "select_eligible_executor": true,
	"dispatch_assignment": true, "identify_next_ready_issue": true, "verify_issue_scope": true,
	"revoke_current_assignment": true, "replace_current_session": true, "fence_current_generation": true,
	"apply_response_to_current_assignment": true, "mark_current_assignment_failed": true,
	"ask_owner": true, "ask_user_to_choose_agent": true, "passive_wait": true, "blind_redispatch": true,
}

var staleActions = []WorkflowAction{"record_stale_response_observation", "reject_response_for_current_assignment", "preserve_current_assignment", "preserve_current_session", "increment_stale_response_metric"}
var staleForbidden = []WorkflowAction{"apply_response_to_current_assignment", "revoke_current_assignment", "fence_current_generation", "replace_current_session", "mark_current_assignment_failed"}

var workflowIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,95}$`)
var namespaceIDPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,95}$`)

func invalid(detail string) error { return fmt.Errorf("%w: %s", ErrWorkflowInvalid, detail) }

func canonicalTransition(am ActionMap, id string) bool {
	if _, ok := am.RecoveryTransitions[id]; ok {
		return true
	}
	// This existing ActionMap result is the user-confirmed autonomous binding;
	// other expectedResults are NOT silently promoted into transitions.
	if id == "CONTINUE_AUTONOMOUSLY" {
		for _, e := range am.Events {
			if contains(e.ExpectedResults, id) {
				return true
			}
		}
	}
	return false
}

func canonicalIdentifier(am ActionMap, id string) bool {
	if _, ok := am.Events[id]; ok {
		return true
	}
	if canonicalTransition(am, id) {
		return true
	}
	for _, event := range am.Events {
		if contains(event.ExpectedResults, id) || contains(event.AllowedResults, id) {
			return true
		}
	}
	return false
}

func validateMetadata(version, kind, want string, m WorkflowMetadata) error {
	if version != "coord.dev/v1alpha1" || kind != want || m.Status != "Proposed" || (m.NormativeAuthority == nil || *m.NormativeAuthority) || m.CanonicalActionMap != "docs/operations/ACTION-MAP.yaml" {
		return invalid("metadata/authority")
	}
	return nil
}

func ValidateWorkflowCatalog(c WorkflowCatalog, am ActionMap) error {
	if err := validateMetadata(c.APIVersion, c.Kind, "CoordinatorWorkflowCatalog", c.Metadata); err != nil {
		return err
	}
	observations, outcomes := map[string]bool{}, map[string]bool{}
	for _, d := range c.Identifiers.Observations {
		id := string(d.Kind)
		if !namespaceIDPattern.MatchString(id) || observations[id] || d.Scope != "internal" || (d.CanonicalEvent == nil || *d.CanonicalEvent) {
			return invalid("observation definition")
		}
		if canonicalIdentifier(am, id) {
			return invalid("namespace overlap")
		}
		observations[id] = true
	}
	for _, d := range c.Identifiers.Outcomes {
		id := string(d.Kind)
		if !namespaceIDPattern.MatchString(id) || outcomes[id] || observations[id] || d.Scope != "workflow" || (d.CanonicalTransition == nil || *d.CanonicalTransition) {
			return invalid("outcome definition/namespace overlap")
		}
		if canonicalIdentifier(am, id) {
			return invalid("namespace overlap")
		}
		outcomes[id] = true
	}
	if len(c.Workflows) == 0 {
		return invalid("empty catalog")
	}
	ids, priorities := map[string]bool{}, map[string]bool{}
	for _, w := range c.Workflows {
		if !workflowIDPattern.MatchString(w.ID) || ids[w.ID] {
			return invalid("workflow identity")
		}
		ids[w.ID] = true
		if _, ok := am.Events[w.CanonicalEvent]; !ok {
			return invalid("unknown canonical event " + w.CanonicalEvent)
		}
		if !canonicalTransition(am, w.CanonicalTransition) {
			return invalid("unknown canonical transition " + w.CanonicalTransition)
		}
		if !observations[string(w.Observation)] || !outcomes[string(w.Outcome.Code)] {
			return invalid("unknown observation/outcome")
		}
		key := fmt.Sprintf("%s/%d", w.CanonicalEvent, w.Priority)
		if w.Priority <= 0 || priorities[key] {
			return invalid("priority tie/nonpositive priority")
		}
		priorities[key] = true
		if err := validateConditions(w.When, c); err != nil {
			return err
		}
		seen := map[WorkflowAction]bool{}
		for _, a := range w.Actions {
			if !workflowActions[a] || seen[a] {
				return invalid("unknown/duplicate workflow action")
			}
			seen[a] = true
		}
		if len(seen) == 0 {
			return invalid("empty actions")
		}
		for _, a := range w.Forbidden {
			if !workflowActions[a] || seen[a] {
				return invalid("unknown/contradictory forbidden action")
			}
		}
		if w.Observation == ObservationAgentResponse || w.Outcome.Code == OutcomeStaleResponseRejected {
			if w.Observation != ObservationAgentResponse || w.Outcome.Code != OutcomeStaleResponseRejected ||
				w.CanonicalEvent != "AGENT_ASSIGNMENT_DISPATCHED" || w.CanonicalTransition != "ADVANCE_DISPATCHED_WORK" ||
				!hasReference(w.When.All, "observedResponse.assignmentId", "NE", "currentAssignment.id") ||
				!hasReference(w.When.All, "observedResponse.generation", "LT", "currentAssignment.generation") ||
				w.Outcome.CurrentAssignmentState != "UNCHANGED" || w.Outcome.CurrentSessionState != "UNCHANGED" {
				return invalid("stale response correlation/preservation contract")
			}
			if len(w.Actions) != len(staleActions) {
				return invalid("stale response actions")
			}
			for i, a := range staleActions {
				if w.Actions[i] != a {
					return invalid("stale response action sequence")
				}
			}
			for _, a := range staleForbidden {
				if !hasAction(w.Forbidden, a) {
					return invalid("stale response mutation prohibition")
				}
			}
		}
		if seen["dispatch_replacement"] || seen["create_durable_replacement_intent"] {
			if !hasBoolean(w.When.All, "replacement.eligible", true) || !hasBoolean(w.When.All, "replacement.independent", true) {
				return invalid("replacement eligibility/independence")
			}
			failed := false
			for _, f := range []FactID{"assignment.admissionTimedOut", "session.heartbeatStale", "session.reconcileFailed", "currentGeneration.fenced", "session.executionBlocked"} {
				failed = failed || hasBoolean(w.When.All, f, true)
			}
			if !failed {
				return invalid("current assignment failure not proven")
			}
			if !hasAction(w.Actions, "verify_admission") || !hasAction(w.Actions, "verify_running") || !hasAction(w.Actions, "verify_replacement_independence") {
				return invalid("replacement admission steps missing")
			}
			// Writer replacement requires proofs bound by the trusted observation
			// adapter; this library does not manufacture a process stop proof.
			if !hasBoolean(w.When.All, "fence.accepted", true) || !hasBoolean(w.When.All, "stopProof.satisfied", true) {
				return invalid("replacement fence/stop proof missing")
			}
		}
	}
	return nil
}

func hasAction(actions []WorkflowAction, a WorkflowAction) bool {
	for _, v := range actions {
		if a == v {
			return true
		}
	}
	return false
}
func hasReference(cs []Condition, f FactID, op Operator, from FactID) bool {
	for _, c := range cs {
		if c.Fact == f && c.Operator == op && c.Value == nil && c.ValueFrom == from {
			return true
		}
	}
	return false
}
func hasBoolean(cs []Condition, f FactID, v bool) bool {
	for _, c := range cs {
		if c.Fact == f && c.Operator == "EQ" && c.ValueFrom == "" && c.Value != nil && c.Value.Kind == "boolean" && c.Value.Boolean == v {
			return true
		}
	}
	return false
}

func validateConditions(g Conditions, c WorkflowCatalog) error {
	if len(g.All)+len(g.Any) == 0 {
		return invalid("empty conditions")
	}
	for _, condition := range append(append([]Condition(nil), g.All...), g.Any...) {
		if err := validateCondition(condition, c); err != nil {
			return err
		}
	}
	return nil
}

func validateCondition(c Condition, catalog WorkflowCatalog) error {
	spec, ok := workflowFacts[c.Fact]
	if !ok {
		return invalid("unknown fact " + string(c.Fact))
	}
	unary := c.Operator == "EMPTY" || c.Operator == "NOT_EMPTY"
	if unary {
		if c.Value != nil || c.ValueFrom != "" || (spec.kind != "string" && spec.kind != "collection") {
			return invalid("unary condition type")
		}
		return nil
	}
	if (c.Value == nil) == (c.ValueFrom == "") {
		return invalid("exactly one value/valueFrom required")
	}
	if c.Operator == "CONTAINS" {
		if spec.kind != "collection" {
			return invalid("contains requires collection")
		}
		if c.Value != nil {
			if c.Value.Kind != "string" {
				return invalid("contains requires string")
			}
		} else {
			right, exists := workflowFacts[c.ValueFrom]
			if !exists || right.kind != "string" {
				return invalid("contains reference requires string")
			}
		}
		return nil
	}
	var rightKind string
	if c.ValueFrom != "" {
		other, exists := workflowFacts[c.ValueFrom]
		if !exists || other.kind != spec.kind || other.identity != spec.identity {
			return invalid("incompatible fact reference")
		}
		rightKind = other.kind
	} else {
		if err := validateFactValue(c.Fact, *c.Value, catalog); err != nil {
			return err
		}
		rightKind = c.Value.Kind
	}
	switch c.Operator {
	case "EQ", "NE":
		if rightKind != spec.kind {
			return invalid("comparison type")
		}
	case "LT", "LTE", "GT", "GTE":
		if spec.kind != "integer" || rightKind != "integer" {
			return invalid("ordering type")
		}
	case "SAME_ASSIGNMENT", "SAME_GENERATION", "SAME_SUBJECT":
		class := map[Operator]string{"SAME_ASSIGNMENT": "assignment", "SAME_GENERATION": "generation", "SAME_SUBJECT": "subject"}[c.Operator]
		if c.ValueFrom == "" || spec.identity != class {
			return invalid("identity comparison type")
		}
	default:
		return invalid("unknown operator")
	}
	return nil
}

func validateFactValue(f FactID, v Value, c WorkflowCatalog) error {
	s, ok := workflowFacts[f]
	if !ok || v.Kind != s.kind {
		return invalid("fact value type")
	}
	if len(s.enum) > 0 && !contains(s.enum, v.Text) {
		return invalid("fact enum")
	}
	if s.identity == "generation" && v.Number == 0 {
		return invalid("generation must be positive")
	}
	if s.identity == "assignment" && strings.TrimSpace(v.Text) == "" {
		return invalid("assignment identity empty")
	}
	if s.identity == "subject" && !validHex(v.Text, 40) {
		return invalid("subject requires full SHA")
	}
	if f == "workflow.lastOutcome" && v.Text != "NONE" {
		found := false
		for _, d := range c.Identifiers.Outcomes {
			found = found || v.Text == string(d.Kind)
		}
		if !found {
			return invalid("unknown outcome fact")
		}
	}
	return nil
}

func EvaluateConditions(g Conditions, facts Facts, c WorkflowCatalog) (bool, error) {
	if err := validateConditions(g, c); err != nil {
		return false, err
	}
	// Validate and resolve every referenced fact before choosing a branch;
	// short-circuiting must not turn an unknown observation into evidence.
	all, any := true, len(g.Any) == 0
	for _, group := range []struct {
		items []Condition
		isAll bool
	}{{g.All, true}, {g.Any, false}} {
		for _, condition := range group.items {
			value, ok := facts[condition.Fact]
			if !ok {
				return false, fmt.Errorf("%w: %s", ErrFactMissing, condition.Fact)
			}
			if err := validateFactValue(condition.Fact, value, c); err != nil {
				return false, err
			}
			var right Value
			if condition.Value != nil {
				right = *condition.Value
			}
			if condition.ValueFrom != "" {
				var exists bool
				right, exists = facts[condition.ValueFrom]
				if !exists {
					return false, ErrFactMissing
				}
				if err := validateFactValue(condition.ValueFrom, right, c); err != nil {
					return false, err
				}
			}
			match := compareCondition(condition.Operator, value, right)
			if group.isAll {
				all = all && match
			} else {
				any = any || match
			}
		}
	}
	return all && any, nil
}

func compareCondition(op Operator, left, right Value) bool {
	equal := left.Kind == right.Kind && left.Boolean == right.Boolean && left.Text == right.Text && left.Number == right.Number && slices.Equal(left.Items, right.Items)
	switch op {
	case "EQ", "SAME_ASSIGNMENT", "SAME_GENERATION", "SAME_SUBJECT":
		return equal
	case "NE":
		return !equal
	case "LT":
		return left.Number < right.Number
	case "LTE":
		return left.Number <= right.Number
	case "GT":
		return left.Number > right.Number
	case "GTE":
		return left.Number >= right.Number
	case "CONTAINS":
		return slices.Contains(left.Items, right.Text)
	case "EMPTY":
		return left.Text == "" && len(left.Items) == 0
	case "NOT_EMPTY":
		return left.Text != "" || len(left.Items) > 0
	}
	return false
}

// SelectWorkflow returns a copied plan, not side-effect authority. An executor
// must use existing canonical commands/outbox; this slice has no such adapter.
func SelectWorkflow(c WorkflowCatalog, am ActionMap, event string, observation ObservationKind, facts Facts) (Workflow, error) {
	if err := ValidateWorkflowCatalog(c, am); err != nil {
		return Workflow{}, err
	}
	var candidates []Workflow
	for _, w := range c.Workflows {
		if w.CanonicalEvent != event || w.Observation != observation {
			continue
		}
		match, err := EvaluateConditions(w.When, facts, c)
		if err != nil {
			return Workflow{}, err
		}
		if match {
			candidates = append(candidates, w)
		}
	}
	if len(candidates) == 0 {
		return Workflow{}, ErrNoMatchingWorkflow
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Priority > candidates[j].Priority })
	w := candidates[0]
	w.Actions = append([]WorkflowAction(nil), w.Actions...)
	w.Forbidden = append([]WorkflowAction(nil), w.Forbidden...)
	return w, nil
}

// ExecuteStaleResponse is a pure local execution of the sole non-mutating
// response workflow. The returned outcome still needs canonical recording.
func ExecuteStaleResponse(c WorkflowCatalog, am ActionMap, o AgentResponseObservation, a CurrentAssignment, s CurrentSession) (StaleResponseExecution, error) {
	result := StaleResponseExecution{Assignment: a, Session: s}
	if o.ObservationID == "" || o.AssignmentID == "" || o.SessionID == "" || o.Generation == 0 || o.ObservedAt.IsZero() || o.ResultType == "" || !validHex(o.ResultDigest, 64) || !validHex(o.SubjectSHA, 40) ||
		a.ID == "" || a.SessionID != s.ID || a.Generation == 0 || a.Generation != s.Generation || !validHex(a.SubjectSHA, 40) {
		return result, ErrResponseCorrelation
	}
	if o.AssignmentID == a.ID || o.Generation >= a.Generation {
		return result, ErrResponseCorrelation
	}
	facts := Facts{"observedResponse.assignmentId": TextValue(o.AssignmentID), "currentAssignment.id": TextValue(a.ID), "observedResponse.generation": NumberValue(o.Generation), "currentAssignment.generation": NumberValue(a.Generation)}
	w, err := SelectWorkflow(c, am, "AGENT_ASSIGNMENT_DISPATCHED", ObservationAgentResponse, facts)
	if err != nil {
		return result, err
	}
	if w.Outcome.Code != OutcomeStaleResponseRejected {
		return result, ErrResponseCorrelation
	}
	result.Outcome = WorkflowOutcome{WorkflowID: w.ID, Code: w.Outcome.Code, ObservationID: o.ObservationID, AssignmentID: o.AssignmentID, SessionID: o.SessionID, Generation: o.Generation, ReasonCodes: nil}
	result.Actions = append([]WorkflowAction(nil), w.Actions...)
	return result, nil
}

var completionReasons = map[string]bool{
	"AUTOMATION_PATH_AVAILABLE": true, "ELIGIBLE_REPLACEMENT_AVAILABLE": true, "HUMAN_ACTION_COMMAND_MISSING": true,
	"HUMAN_ACTION_POST_CHECK_MISSING": true, "DETERMINISTIC_TRANSITION_NOT_EXECUTED": true, "UNSUPPORTED_CONFIRMATION_REQUEST": true,
	"READY_ASSIGNMENT_NOT_DISPATCHED": true, "PASSIVE_FUTURE_PROMISE": true, "DELIVERED_NOT_ADMITTED": true, "QUEUED_NOT_RUNNING": true,
	"SESSION_RECONCILE_REQUIRED": true, "INDEPENDENT_WORK_NOT_PERFORMED": true, "STALE_RESPONSE_MUTATED_CURRENT_ASSIGNMENT": true,
}

func workflowLiteral(v Value) *Value { return &v }

// Version-1 core guards cannot be removed or weakened by editing policy YAML.
var requiredGuardContracts = []CompletionGuard{
	{ID: "deterministic-transition-executed", When: Conditions{All: []Condition{{Fact: "deterministic.available", Operator: "EQ", Value: workflowLiteral(BoolValue(true))}}}, Require: Conditions{All: []Condition{{Fact: "deterministic.executed", Operator: "EQ", Value: workflowLiteral(BoolValue(true))}}}, Violation: struct {
		ReasonCode string `yaml:"reasonCode"`
	}{ReasonCode: "DETERMINISTIC_TRANSITION_NOT_EXECUTED"}},
	{ID: "ready-assignment-dispatched", When: Conditions{All: []Condition{{Fact: "readyAssignment.exists", Operator: "EQ", Value: workflowLiteral(BoolValue(true))}}}, Require: Conditions{All: []Condition{{Fact: "readyAssignment.dispatched", Operator: "EQ", Value: workflowLiteral(BoolValue(true))}}}, Violation: struct {
		ReasonCode string `yaml:"reasonCode"`
	}{ReasonCode: "READY_ASSIGNMENT_NOT_DISPATCHED"}},
	{ID: "delivered-is-not-admitted", When: Conditions{All: []Condition{{Fact: "assignment.delivery", Operator: "EQ", Value: workflowLiteral(TextValue("DELIVERED"))}, {Fact: "assignment.reportedAdmitted", Operator: "EQ", Value: workflowLiteral(BoolValue(true))}}}, Require: Conditions{All: []Condition{{Fact: "assignment.admission", Operator: "EQ", Value: workflowLiteral(TextValue("ADMITTED"))}}}, Violation: struct {
		ReasonCode string `yaml:"reasonCode"`
	}{ReasonCode: "DELIVERED_NOT_ADMITTED"}},
	{ID: "queued-is-not-running", When: Conditions{All: []Condition{{Fact: "assignment.delivery", Operator: "EQ", Value: workflowLiteral(TextValue("QUEUED"))}}}, Require: Conditions{All: []Condition{{Fact: "session.reportedRunning", Operator: "EQ", Value: workflowLiteral(BoolValue(false))}}}, Violation: struct {
		ReasonCode string `yaml:"reasonCode"`
	}{ReasonCode: "QUEUED_NOT_RUNNING"}},
	{ID: "stale-heartbeat-reconciled", When: Conditions{All: []Condition{{Fact: "session.heartbeatStale", Operator: "EQ", Value: workflowLiteral(BoolValue(true))}}}, Require: Conditions{All: []Condition{{Fact: "session.reconcileAttempted", Operator: "EQ", Value: workflowLiteral(BoolValue(true))}}}, Violation: struct {
		ReasonCode string `yaml:"reasonCode"`
	}{ReasonCode: "SESSION_RECONCILE_REQUIRED"}},
	{ID: "human-has-no-automation", When: Conditions{All: []Condition{{Fact: "human.required", Operator: "EQ", Value: workflowLiteral(BoolValue(true))}}}, Require: Conditions{All: []Condition{{Fact: "automation.available", Operator: "EQ", Value: workflowLiteral(BoolValue(false))}}}, Violation: struct {
		ReasonCode string `yaml:"reasonCode"`
	}{ReasonCode: "AUTOMATION_PATH_AVAILABLE"}},
	{ID: "human-has-no-replacement", When: Conditions{All: []Condition{{Fact: "human.required", Operator: "EQ", Value: workflowLiteral(BoolValue(true))}}}, Require: Conditions{All: []Condition{{Fact: "replacement.eligible", Operator: "EQ", Value: workflowLiteral(BoolValue(false))}}}, Violation: struct {
		ReasonCode string `yaml:"reasonCode"`
	}{ReasonCode: "ELIGIBLE_REPLACEMENT_AVAILABLE"}},
	{ID: "human-has-command", When: Conditions{All: []Condition{{Fact: "human.required", Operator: "EQ", Value: workflowLiteral(BoolValue(true))}}}, Require: Conditions{All: []Condition{{Fact: "human.commandPrepared", Operator: "EQ", Value: workflowLiteral(BoolValue(true))}}}, Violation: struct {
		ReasonCode string `yaml:"reasonCode"`
	}{ReasonCode: "HUMAN_ACTION_COMMAND_MISSING"}},
	{ID: "human-has-post-check", When: Conditions{All: []Condition{{Fact: "human.required", Operator: "EQ", Value: workflowLiteral(BoolValue(true))}}}, Require: Conditions{All: []Condition{{Fact: "human.postCheckPrepared", Operator: "EQ", Value: workflowLiteral(BoolValue(true))}}}, Violation: struct {
		ReasonCode string `yaml:"reasonCode"`
	}{ReasonCode: "HUMAN_ACTION_POST_CHECK_MISSING"}},
	{ID: "no-confirmation-question", When: Conditions{All: []Condition{{Fact: "completion.requested", Operator: "EQ", Value: workflowLiteral(BoolValue(true))}}}, Require: Conditions{All: []Condition{{Fact: "confirmation.requested", Operator: "EQ", Value: workflowLiteral(BoolValue(false))}}}, Violation: struct {
		ReasonCode string `yaml:"reasonCode"`
	}{ReasonCode: "UNSUPPORTED_CONFIRMATION_REQUEST"}},
	{ID: "no-future-promise", When: Conditions{All: []Condition{{Fact: "completion.requested", Operator: "EQ", Value: workflowLiteral(BoolValue(true))}}}, Require: Conditions{All: []Condition{{Fact: "futurePromise", Operator: "EMPTY"}}}, Violation: struct {
		ReasonCode string `yaml:"reasonCode"`
	}{ReasonCode: "PASSIVE_FUTURE_PROMISE"}},
	{ID: "independent-work-performed", When: Conditions{All: []Condition{{Fact: "independentWork.available", Operator: "EQ", Value: workflowLiteral(BoolValue(true))}}}, Require: Conditions{All: []Condition{{Fact: "independentWork.performed", Operator: "EQ", Value: workflowLiteral(BoolValue(true))}}}, Violation: struct {
		ReasonCode string `yaml:"reasonCode"`
	}{ReasonCode: "INDEPENDENT_WORK_NOT_PERFORMED"}},
	{ID: "current-assignment-preserved-after-stale-response", When: Conditions{All: []Condition{{Fact: "workflow.lastOutcome", Operator: "EQ", Value: workflowLiteral(TextValue("STALE_RESPONSE_REJECTED"))}}}, Require: Conditions{All: []Condition{{Fact: "currentAssignment.stateChanged", Operator: "EQ", Value: workflowLiteral(BoolValue(false))}, {Fact: "currentSession.replaced", Operator: "EQ", Value: workflowLiteral(BoolValue(false))}}}, Violation: struct {
		ReasonCode string `yaml:"reasonCode"`
	}{ReasonCode: "STALE_RESPONSE_MUTATED_CURRENT_ASSIGNMENT"}},
}

func ValidateCompletionPolicy(p CompletionPolicy, c WorkflowCatalog, am ActionMap) error {
	if err := ValidateWorkflowCatalog(c, am); err != nil {
		return err
	}
	if err := validateMetadata(p.APIVersion, p.Kind, "CoordinatorCompletionPolicy", p.Metadata); err != nil {
		return err
	}
	reasons, ids := map[string]bool{}, map[string]bool{}
	for _, r := range p.ReasonCodes {
		if !completionReasons[r] || reasons[r] {
			return invalid("completion reason taxonomy")
		}
		reasons[r] = true
	}
	for _, g := range p.Guards {
		if !workflowIDPattern.MatchString(g.ID) || ids[g.ID] || !reasons[g.Violation.ReasonCode] {
			return invalid("completion guard identity/reason")
		}
		ids[g.ID] = true
		if err := validateConditions(g.When, c); err != nil {
			return err
		}
		if err := validateConditions(g.Require, c); err != nil {
			return err
		}
	}
	for _, required := range requiredGuardContracts {
		found := false
		for _, g := range p.Guards {
			if g.ID == required.ID {
				found = true
				if !reflect.DeepEqual(g, required) {
					return invalid("core completion guard changed")
				}
			}
		}
		if !found {
			return invalid("core completion guard missing")
		}
	}
	if len(p.Guards) == 0 {
		return invalid("empty completion policy")
	}
	return nil
}

func EvaluateCompletionPolicy(p CompletionPolicy, c WorkflowCatalog, am ActionMap, facts Facts) error {
	if err := ValidateCompletionPolicy(p, c, am); err != nil {
		return err
	}
	for _, g := range p.Guards {
		applies, err := EvaluateConditions(g.When, facts, c)
		if err != nil {
			return err
		}
		if !applies {
			continue
		}
		ok, err := EvaluateConditions(g.Require, facts, c)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("%s: guard %s", g.Violation.ReasonCode, g.ID)
		}
	}
	return nil
}

func validHex(s string, n int) bool {
	data, err := hex.DecodeString(s)
	return err == nil && len(s) == n && hex.EncodeToString(data) == s
}

func boundObservation(ctx CompletionContext, d CompletionDecision) (BoundaryObservation, error) {
	o, ok := ctx.Observations[d.ObservationID]
	if !ok || o.ID != d.ObservationID || o.ID == "" || ctx.Now.IsZero() || o.ObservedAt.IsZero() || o.ObservedAt.After(ctx.Now) || !ctx.Now.Before(o.ValidUntil) ||
		(o.Source != "CANONICAL_OBSERVATION" && o.Source != "APPROVED_ADAPTER") || !validHex(o.Digest, 64) || !validHex(o.SubjectSHA, 40) ||
		o.ActionID == "" || o.ActionID != d.PendingActionID || o.AssignmentID != d.PendingAssignmentID || o.SubjectSHA != d.SubjectSHA || o.ReasonCode != d.ReasonCode || len(o.EvidenceRefs) == 0 {
		return o, ErrBoundaryNotProven
	}
	for _, ref := range o.EvidenceRefs {
		evidence, exists := ctx.Evidence[ref]
		if !exists || ref == "" || evidence.ID != ref || !validHex(evidence.Digest, 64) || evidence.ActionID != o.ActionID || evidence.AssignmentID != o.AssignmentID || evidence.SubjectSHA != o.SubjectSHA ||
			(evidence.Source != "CANONICAL_EVENT" && evidence.Source != "ADAPTER_OBSERVATION" && evidence.Source != "ACCEPTED_NORM") {
			return o, ErrBoundaryNotProven
		}
	}
	return o, nil
}

func ValidateOwnerBoundary(ctx CompletionContext, d CompletionDecision) error {
	if !d.OwnerRequired {
		return nil
	}
	o, err := boundObservation(ctx, d)
	if err != nil || !ownerRequiredReasonCodes[d.ReasonCode] || o.NormStatus != "ACCEPTED" || strings.TrimSpace(o.NormRef) == "" || !validHex(o.NormDigest, 64) {
		return ErrOwnerBoundaryNotProven
	}
	foundNorm := false
	for _, ref := range o.EvidenceRefs {
		evidence := ctx.Evidence[ref]
		if evidence.Source == "ACCEPTED_NORM" && evidence.NormStatus == "ACCEPTED" && evidence.NormRef == o.NormRef && evidence.Digest == o.NormDigest {
			foundNorm = true
		}
	}
	if !foundNorm {
		return ErrOwnerBoundaryNotProven
	}
	return nil
}

// HumanActionDigest binds the exact prepared card to the accepted action.
// New observations/retries retain the same digest; observation ID is excluded.
func HumanActionDigest(d CompletionDecision) (string, error) {
	data, err := json.Marshal(struct {
		Version                            uint32
		ActionID, AssignmentID, SubjectSHA string
		Card                               HumanActionCard
	}{1, d.PendingActionID, d.PendingAssignmentID, d.SubjectSHA, d.HumanAction})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func ValidateHumanBoundary(ctx CompletionContext, d CompletionDecision, am ActionMap) error {
	if !d.HumanActionRequired {
		return nil
	}
	o, err := boundObservation(ctx, d)
	if err != nil {
		return err
	}
	for _, id := range []FactID{"automation.available", "replacement.eligible"} {
		fact, exists := ctx.Facts[id]
		if !exists || fact.Kind != "boolean" {
			return ErrFactMissing
		}
		if fact.Boolean {
			if id == "automation.available" {
				return ErrAutomationPathAvailable
			}
			return ErrEligibleReplacementAvailable
		}
	}
	if o.Automation == AvailabilityAvailable {
		return ErrAutomationPathAvailable
	}
	if o.Replacement == AvailabilityAvailable {
		return ErrEligibleReplacementAvailable
	}
	if !humanActionRequiredReasonCodes[d.ReasonCode] || !o.DecisionAlreadyAccepted || !o.OwnerBoundaryAbsent || !o.PhysicalActionRequired ||
		o.Automation != AvailabilityUnavailable || o.Replacement != AvailabilityUnavailable || o.InternalRemedy != AvailabilityUnavailable {
		return ErrBoundaryNotProven
	}
	h := d.HumanAction
	if strings.TrimSpace(h.Command) == "" || !filepath.IsAbs(h.WorkingDirectory) || strings.TrimSpace(h.ExpectedResult) == "" {
		return ErrHumanActionCommandMissing
	}
	if len(h.PostChecks) == 0 {
		return ErrHumanActionPostCheckMissing
	}
	for _, check := range h.PostChecks {
		if strings.TrimSpace(check) == "" {
			return ErrHumanActionPostCheckMissing
		}
	}
	if !canonicalTransition(am, h.FailureTransition) || strings.TrimSpace(h.Continuation) == "" {
		return ErrBoundaryNotProven
	}
	digest, err := HumanActionDigest(d)
	if err != nil || !validHex(o.RequestDigest, 64) || digest != o.RequestDigest {
		return ErrBoundaryNotProven
	}
	return nil
}

func ValidateExternalBlocker(ctx CompletionContext, d CompletionDecision) error {
	if !d.BlockedExternal {
		return nil
	}
	o, err := boundObservation(ctx, d)
	if err != nil || !o.ExternalUnavailable || o.PhysicalActionRequired || o.ReasonCode == "" || ownerRequiredReasonCodes[o.ReasonCode] || humanActionRequiredReasonCodes[o.ReasonCode] {
		return ErrBoundaryNotProven
	}
	return nil
}

// ValidateCompletionDecision has no boolean-boundary short circuit. Callers
// must supply trusted observations and typed turn facts, including independent
// work and future promises, even when a legitimate boundary exists.
func ValidateCompletionDecision(p CompletionPolicy, c WorkflowCatalog, am ActionMap, ctx CompletionContext, d CompletionDecision) error {
	count := 0
	for _, b := range []bool{d.OwnerRequired, d.HumanActionRequired, d.BlockedExternal} {
		if b {
			count++
		}
	}
	if count > 1 {
		return ErrBoundaryNotProven
	}
	if err := ValidateOwnerBoundary(ctx, d); err != nil {
		return err
	}
	if err := ValidateHumanBoundary(ctx, d, am); err != nil {
		return err
	}
	if err := ValidateExternalBlocker(ctx, d); err != nil {
		return err
	}
	facts := make(Facts, len(ctx.Facts)+3)
	for k, v := range ctx.Facts {
		facts[k] = v
	}
	facts["human.required"] = BoolValue(d.HumanActionRequired)
	facts["human.commandPrepared"] = BoolValue(strings.TrimSpace(d.HumanAction.Command) != "" && filepath.IsAbs(d.HumanAction.WorkingDirectory) && strings.TrimSpace(d.HumanAction.ExpectedResult) != "")
	facts["human.postCheckPrepared"] = BoolValue(len(d.HumanAction.PostChecks) > 0)
	return EvaluateCompletionPolicy(p, c, am, facts)
}

// RenderWorkflowMarkdown is a deterministic, read-only projection.
func RenderWorkflowMarkdown(c WorkflowCatalog) string {
	var b strings.Builder
	b.WriteString("# Coordinator workflows\n\nGenerated from COORDINATOR-WORKFLOWS.yaml. Proposed; no production activation.\n\n| Workflow | Canonical event | Canonical transition | Observation | Priority | Workflow outcome |\n|---|---|---|---|---:|---|\n")
	for _, w := range c.Workflows {
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %d | %s |\n", w.ID, w.CanonicalEvent, w.CanonicalTransition, w.Observation, w.Priority, w.Outcome.Code)
	}
	b.WriteString("\nObservations and outcomes are local namespaces. Plans do not grant mutation authority. Only stale-response rejection has a pure local executor; external actions and daemon wiring are not implemented by this catalog.\n")
	b.WriteString("\n## Completion policy boundary\n\nCOORDINATOR-COMPLETION-POLICY.yaml is a separate typed policy. ValidateCompletionDecision checks evidence binding, freshness, existing OWNER/HUMAN reason allowlists and all core turn guards. A Human Action requires observed absence of automation, replacement and internal remedies plus a complete command/post-check/failure card whose digest matches the accepted action. A boundary flag never skips completion checks.\n\nObservation records and Facts must be supplied by a trusted canonical/approved-adapter projection, never parsed from model text. This library checks structured provenance; it does not authenticate an Inbox sender, establish the truth of a normative argument, or implement that production projection. Existing watchdog completion and coordd callers are unchanged.\n")
	return b.String()
}
