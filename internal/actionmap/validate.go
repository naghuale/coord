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

	// OWNER_REQUIRED reason codes must be from the owner-boundary allowlist.
	if t, ok := am.RecoveryTransitions["OWNER_REQUIRED"]; ok {
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
