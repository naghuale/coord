package actionmap

import (
	"fmt"
	"os"

	"coord/internal/governanceio"
	"gopkg.in/yaml.v3"
)

// Violation is a single failing check with a stable reason code and detail.
type Violation struct {
	Code   string
	Detail string
}

// Validate parses actionMapPath and registryPath, runs all structural
// governance checks, and returns any violations.
func Validate(actionMapPath, registryPath string) ([]Violation, error) {
	f, err := os.Open(actionMapPath)
	if err != nil {
		return nil, fmt.Errorf("open action map: %w", err)
	}
	defer func() { _ = f.Close() }()

	am, err := governanceio.DecodeStrictYAML[ActionMap](f)
	if err != nil {
		return nil, fmt.Errorf("parse action map: %w", err)
	}

	registeredIDs, err := loadRegisteredIDs(registryPath)
	if err != nil {
		return nil, fmt.Errorf("load registry: %w", err)
	}

	return validate(am, registeredIDs), nil
}

func validate(am ActionMap, registeredIDs map[string]bool) []Violation {
	var vs []Violation

	if am.APIVersion != "coord.dev/v1alpha1" {
		vs = append(vs, Violation{Code: "INVALID_API_VERSION", Detail: fmt.Sprintf("got %q", am.APIVersion)})
	}
	if am.Kind != "ActionMap" {
		vs = append(vs, Violation{Code: "INVALID_KIND", Detail: fmt.Sprintf("got %q", am.Kind)})
	}

	// Required recovery events present.
	for _, ev := range requiredRecoveryEvents {
		if _, ok := am.Events[ev]; !ok {
			vs = append(vs, Violation{Code: "MISSING_RECOVERY_EVENT", Detail: fmt.Sprintf("required event %s absent", ev)})
		}
	}

	// Required recovery transitions present in recoveryTransitions map.
	for _, t := range requiredRecoveryTransitions {
		if _, ok := am.RecoveryTransitions[t]; !ok {
			vs = append(vs, Violation{Code: "MISSING_RECOVERY_TRANSITION", Detail: fmt.Sprintf("required transition %s absent", t)})
		}
	}

	// Collect all expectedResults across events.
	allExpected := map[string]bool{}

	for evName, ev := range am.Events {
		loc := fmt.Sprintf("event %s", evName)

		// No wildcard or catch-all event IDs.
		if evName == "*" || evName == "DEFAULT" || evName == "_" {
			vs = append(vs, Violation{Code: "PERMISSIVE_FALLBACK_EVENT", Detail: fmt.Sprintf("%s: wildcard event is forbidden", loc)})
		}

		// expectedResults must be from the known closed set.
		for _, r := range ev.ExpectedResults {
			if !knownExpectedResults[r] {
				vs = append(vs, Violation{Code: "UNKNOWN_EXPECTED_RESULT", Detail: fmt.Sprintf("%s: unknown expectedResult %q", loc, r)})
			}
			allExpected[r] = true
		}

		// requiredDocuments must be registered.
		for _, docID := range ev.RequiredDocuments {
			if len(registeredIDs) > 0 && !registeredIDs[docID] {
				vs = append(vs, Violation{Code: "UNREGISTERED_DOCUMENT", Detail: fmt.Sprintf("%s: requiredDocument %q not in registry", loc, docID)})
			}
		}
		if ev.Transition != "" {
			if _, ok := am.RecoveryTransitions[ev.Transition]; !ok {
				vs = append(vs, Violation{Code: "UNDEFINED_EVENT_TRANSITION", Detail: fmt.Sprintf("%s: transition %q is not defined", loc, ev.Transition)})
			}
		}
		for _, action := range ev.RequiredActions {
			if !contains(ev.AllowedActions, action) {
				vs = append(vs, Violation{Code: "REQUIRED_ACTION_NOT_ALLOWED", Detail: fmt.Sprintf("%s: required action %q is not allowed", loc, action)})
			}
			if contains(ev.ForbiddenActions, action) {
				vs = append(vs, Violation{Code: "REQUIRED_ACTION_FORBIDDEN", Detail: fmt.Sprintf("%s: required action %q is forbidden", loc, action)})
			}
		}
		for _, result := range ev.AllowedResults {
			if !knownExpectedResults[result] {
				vs = append(vs, Violation{Code: "UNKNOWN_ALLOWED_RESULT", Detail: fmt.Sprintf("%s: unknown allowedResult %q", loc, result)})
			}
			if len(ev.ExpectedResults) > 0 && !contains(ev.ExpectedResults, result) {
				vs = append(vs, Violation{Code: "ALLOWED_RESULT_NOT_EXPECTED", Detail: fmt.Sprintf("%s: allowedResult %q is absent from expectedResults", loc, result)})
			}
		}
	}

	// Dispatch is a mandatory transition contract. Delivery, queueing and idle
	// session state cannot be collapsed into admission or execution.
	if ev, ok := am.Events["AGENT_ASSIGNMENT_DISPATCHED"]; ok {
		if ev.Transition != "ADVANCE_DISPATCHED_WORK" {
			vs = append(vs, Violation{Code: "INVALID_DISPATCH_TRANSITION", Detail: "AGENT_ASSIGNMENT_DISPATCHED must select ADVANCE_DISPATCHED_WORK"})
		}
		for _, action := range []string{
			"inspect_session_state", "verify_assignment_binding", "verify_subject_sha",
			"establish_admission_state", "start_agent_if_authorized",
			"process_available_results", "perform_independent_coordination_work",
		} {
			if !contains(ev.RequiredActions, action) {
				vs = append(vs, Violation{Code: "MISSING_DISPATCH_ACTION", Detail: fmt.Sprintf("AGENT_ASSIGNMENT_DISPATCHED lacks required action %q", action)})
			}
		}
		for _, action := range []string{
			"passively_wait_for_notification", "report_future_delivery",
			"treat_idle_as_running", "treat_queued_as_admitted", "ask_owner_what_to_do_next",
		} {
			if !contains(ev.ForbiddenActions, action) {
				vs = append(vs, Violation{Code: "MISSING_DISPATCH_PROHIBITION", Detail: fmt.Sprintf("AGENT_ASSIGNMENT_DISPATCHED lacks prohibition %q", action)})
			}
		}
		if ev.OwnerRequired == nil || *ev.OwnerRequired {
			vs = append(vs, Violation{Code: "INVALID_DISPATCH_OWNER_POLICY", Detail: "AGENT_ASSIGNMENT_DISPATCHED must set ownerRequired: false"})
		}
		if _, ok := am.RecoveryTransitions["ADVANCE_DISPATCHED_WORK"]; !ok {
			vs = append(vs, Violation{Code: "MISSING_DISPATCH_TRANSITION", Detail: "ADVANCE_DISPATCHED_WORK is not defined"})
		}
	}

	// Recovery transitions must be reachable from at least one event's expectedResults.
	for _, t := range requiredRecoveryTransitions {
		if _, ok := am.RecoveryTransitions[t]; !ok {
			continue // already reported above
		}
		if !allExpected[t] {
			vs = append(vs, Violation{Code: "UNREACHABLE_TRANSITION", Detail: fmt.Sprintf("transition %s not in any event's expectedResults", t)})
		}
	}

	// RECOVER_COORDINATION_STATE must be reachable from WORKTREE_STATE_UNKNOWN.
	if ev, ok := am.Events["WORKTREE_STATE_UNKNOWN"]; ok {
		if !contains(ev.ExpectedResults, "RECOVER_COORDINATION_STATE") {
			vs = append(vs, Violation{Code: "MISSING_RECOVER_COORDINATION_STATE_EDGE", Detail: "RECOVER_COORDINATION_STATE not reachable from WORKTREE_STATE_UNKNOWN"})
		}
	}

	// RECOVER_COORDINATION_STATE must be reachable from SESSION_RESUMED.
	if ev, ok := am.Events["SESSION_RESUMED"]; ok {
		if !contains(ev.ExpectedResults, "RECOVER_COORDINATION_STATE") {
			vs = append(vs, Violation{Code: "MISSING_RECOVER_COORDINATION_STATE_EDGE", Detail: "RECOVER_COORDINATION_STATE not reachable from SESSION_RESUMED"})
		}
	}

	// OWNER_REQUIRED must carry at least one evidence reason; an empty list is
	// fail-open because any OWNER_REQUIRED invocation would be evidenceless.
	if t, ok := am.RecoveryTransitions["OWNER_REQUIRED"]; ok {
		if len(t.ReasonCodes) == 0 {
			vs = append(vs, Violation{Code: "OWNER_REQUIRED_REASON_CODE_MISSING", Detail: "OWNER_REQUIRED transition must have at least one reason code"})
		}
		for _, rc := range t.ReasonCodes {
			if !ownerRequiredReasonCodes[rc] {
				vs = append(vs, Violation{Code: "INVALID_OWNER_REASON_CODE", Detail: fmt.Sprintf("OWNER_REQUIRED transition has invalid reason code %q", rc)})
			}
		}
	}

	// HUMAN_ACTION_REQUIRED reason codes must be from the human-action allowlist.
	if t, ok := am.RecoveryTransitions["HUMAN_ACTION_REQUIRED"]; ok {
		for _, rc := range t.ReasonCodes {
			if !humanActionRequiredReasonCodes[rc] {
				vs = append(vs, Violation{Code: "INVALID_HUMAN_ACTION_REASON_CODE", Detail: fmt.Sprintf("HUMAN_ACTION_REQUIRED transition has invalid reason code %q", rc)})
			}
		}
	}

	return vs
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// loadRegisteredIDs reads document IDs from the registry YAML (lenient parse;
// the registry checker validates it separately).
// Returns an empty map (not an error) when registryPath is empty.
func loadRegisteredIDs(registryPath string) (map[string]bool, error) {
	if registryPath == "" {
		return map[string]bool{}, nil
	}
	type minDoc struct {
		ID string `yaml:"id"`
	}
	type minRegistry struct {
		Documents []minDoc `yaml:"documents"`
	}
	f, err := os.Open(registryPath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	var reg minRegistry
	d := yaml.NewDecoder(f)
	if err := d.Decode(&reg); err != nil {
		return nil, fmt.Errorf("parse registry: %w", err)
	}
	ids := make(map[string]bool, len(reg.Documents))
	for _, doc := range reg.Documents {
		ids[doc.ID] = true
	}
	return ids, nil
}
