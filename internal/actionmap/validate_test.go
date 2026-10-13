package actionmap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const minimalValidMap = `apiVersion: coord.dev/v1alpha1
kind: ActionMap
events:
  SESSION_STARTED:
    requiredDocuments: [agents]
    allowedActions: [verify_project_id]
    forbiddenActions: [load_historical_by_default]
    expectedResults: [CONTINUE_AUTONOMOUSLY, BLOCKED, OWNER_REQUIRED]
  SESSION_INTERRUPTED:
    requiredDocuments: []
    allowedActions: [preserve_state]
    forbiddenActions: []
    expectedResults: [HUMAN_ACTION_REQUIRED, PRESERVE_AND_STOP]
  SESSION_RESUMED:
    requiredDocuments: [agents]
    allowedActions: [verify_state]
    forbiddenActions: []
    expectedResults: [RECOVER_COORDINATION_STATE, RESUME_ASSIGNMENT]
  SESSION_REPLACED:
    requiredDocuments: []
    allowedActions: []
    forbiddenActions: []
    expectedResults: [PRESERVE_AND_STOP, FREEZE_FOR_REVIEW]
  CONTEXT_LOST:
    requiredDocuments: []
    allowedActions: []
    forbiddenActions: []
    expectedResults: [RECOVER_COORDINATION_STATE, BLOCKED_EXTERNAL]
  WORKTREE_STATE_UNKNOWN:
    requiredDocuments: []
    allowedActions: []
    forbiddenActions: []
    expectedResults: [RECOVER_COORDINATION_STATE, PREPARE_CORRECTION]
  REQUIRED_SOURCE_MISSING:
    requiredDocuments: []
    allowedActions: []
    forbiddenActions: []
    expectedResults: [PREPARE_CORRECTION, BLOCKED_EXTERNAL]
  SUBJECT_DRIFT_DETECTED:
    requiredDocuments: []
    allowedActions: []
    forbiddenActions: []
    expectedResults: [FREEZE_FOR_REVIEW, PRESERVE_AND_STOP]
recoveryTransitions:
  RESUME_ASSIGNMENT:
    when: assignment recovered
    allowedActions: []
    forbiddenActions: []
    resultsIn: []
  FREEZE_FOR_REVIEW:
    when: implementation complete
    allowedActions: []
    forbiddenActions: []
    resultsIn: []
  RECOVER_COORDINATION_STATE:
    when: missing assignment or stale state
    allowedActions: []
    forbiddenActions: []
    resultsIn: []
  PREPARE_CORRECTION:
    when: mandatory source missing
    allowedActions: []
    forbiddenActions: []
    resultsIn: []
  BLOCKED_EXTERNAL:
    when: external resource required
    allowedActions: []
    forbiddenActions: []
    resultsIn: []
  HUMAN_ACTION_REQUIRED:
    when: credential required
    allowedActions: []
    forbiddenActions: []
    resultsIn: []
  OWNER_REQUIRED:
    when: new norm required
    allowedActions: []
    forbiddenActions: []
    resultsIn: []
  PRESERVE_AND_STOP:
    when: invalid binding
    allowedActions: []
    forbiddenActions: []
    resultsIn: []
`

func writeFixture(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "action-map.yaml")
	if err := os.WriteFile(p, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return p
}

func violations(t *testing.T, mapYAML string) []Violation {
	t.Helper()
	p := writeFixture(t, mapYAML)
	vs, err := Validate(p, "")
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	return vs
}

func parseError(t *testing.T, mapYAML string) error {
	t.Helper()
	p := writeFixture(t, mapYAML)
	_, err := Validate(p, "")
	return err
}

func hasCode(vs []Violation, code string) bool {
	for _, v := range vs {
		if v.Code == code {
			return true
		}
	}
	return false
}

func TestValidActionMap(t *testing.T) {
	vs := violations(t, minimalValidMap)
	if len(vs) != 0 {
		t.Errorf("expected no violations, got: %v", vs)
	}
}

func TestUnknownField(t *testing.T) {
	y := minimalValidMap + "\nunknownTopLevel: bad\n"
	err := parseError(t, y)
	if err == nil {
		t.Error("expected parse error for unknown field, got nil")
	}
}

func TestMissingSessionResumed(t *testing.T) {
	y := strings.ReplaceAll(minimalValidMap,
		`  SESSION_RESUMED:
    requiredDocuments: [agents]
    allowedActions: [verify_state]
    forbiddenActions: []
    expectedResults: [RECOVER_COORDINATION_STATE, RESUME_ASSIGNMENT]
`, "")
	vs := violations(t, y)
	if !hasCode(vs, "MISSING_RECOVERY_EVENT") {
		t.Errorf("expected MISSING_RECOVERY_EVENT, got: %v", vs)
	}
}

func TestMissingRecoverCoordinationState(t *testing.T) {
	y := strings.ReplaceAll(minimalValidMap,
		"  RECOVER_COORDINATION_STATE:\n    when: missing assignment or stale state\n    allowedActions: []\n    forbiddenActions: []\n    resultsIn: []\n", "")
	vs := violations(t, y)
	if !hasCode(vs, "MISSING_RECOVERY_TRANSITION") {
		t.Errorf("expected MISSING_RECOVERY_TRANSITION, got: %v", vs)
	}
}

func TestUnknownExpectedResult(t *testing.T) {
	y := strings.ReplaceAll(minimalValidMap,
		"expectedResults: [CONTINUE_AUTONOMOUSLY, BLOCKED, OWNER_REQUIRED]",
		"expectedResults: [CONTINUE_AUTONOMOUSLY, LINUX_RUNNER_UNAVAILABLE]")
	vs := violations(t, y)
	if !hasCode(vs, "UNKNOWN_EXPECTED_RESULT") {
		t.Errorf("expected UNKNOWN_EXPECTED_RESULT, got: %v", vs)
	}
}

func TestOwnerRequiredWithBadReasonCode(t *testing.T) {
	// LINUX_RUNNER_UNAVAILABLE is a BLOCKED_EXTERNAL reason code, not OWNER_REQUIRED.
	y := strings.ReplaceAll(minimalValidMap,
		"  OWNER_REQUIRED:\n    when: new norm required\n    allowedActions: []\n    forbiddenActions: []\n    resultsIn: []",
		"  OWNER_REQUIRED:\n    when: new norm required\n    allowedActions: []\n    forbiddenActions: []\n    resultsIn: []\n    reasonCodes: [LINUX_RUNNER_UNAVAILABLE]")
	vs := violations(t, y)
	if !hasCode(vs, "INVALID_OWNER_REASON_CODE") {
		t.Errorf("expected INVALID_OWNER_REASON_CODE for LINUX_RUNNER_UNAVAILABLE, got: %v", vs)
	}
}

func TestHumanActionWithOwnerReasonCode(t *testing.T) {
	// NEW_NORM_REQUIRED is an OWNER_REQUIRED reason code, not HUMAN_ACTION_REQUIRED.
	y := strings.ReplaceAll(minimalValidMap,
		"  HUMAN_ACTION_REQUIRED:\n    when: credential required\n    allowedActions: []\n    forbiddenActions: []\n    resultsIn: []",
		"  HUMAN_ACTION_REQUIRED:\n    when: credential required\n    allowedActions: []\n    forbiddenActions: []\n    resultsIn: []\n    reasonCodes: [NEW_NORM_REQUIRED]")
	vs := violations(t, y)
	if !hasCode(vs, "INVALID_HUMAN_ACTION_REASON_CODE") {
		t.Errorf("expected INVALID_HUMAN_ACTION_REASON_CODE, got: %v", vs)
	}
}

func TestPermissiveFallbackEvent(t *testing.T) {
	// Insert the wildcard event into the events section (before recoveryTransitions).
	y := strings.ReplaceAll(minimalValidMap, "recoveryTransitions:",
		`  "*":
    requiredDocuments: []
    allowedActions: [anything]
    forbiddenActions: []
    expectedResults: [CONTINUE_AUTONOMOUSLY]
recoveryTransitions:`)
	vs := violations(t, y)
	if !hasCode(vs, "PERMISSIVE_FALLBACK_EVENT") {
		t.Errorf("expected PERMISSIVE_FALLBACK_EVENT, got: %v", vs)
	}
}

func TestMissingRecoverCoordinationStateEdge(t *testing.T) {
	// Remove RECOVER_COORDINATION_STATE from SESSION_RESUMED expectedResults.
	y := strings.ReplaceAll(minimalValidMap,
		"expectedResults: [RECOVER_COORDINATION_STATE, RESUME_ASSIGNMENT]",
		"expectedResults: [RESUME_ASSIGNMENT, BLOCKED]")
	vs := violations(t, y)
	if !hasCode(vs, "MISSING_RECOVER_COORDINATION_STATE_EDGE") {
		t.Errorf("expected MISSING_RECOVER_COORDINATION_STATE_EDGE, got: %v", vs)
	}
}

// MutationTest: removing SESSION_RESUMED must be detected.
func TestMutation_RequiredEventRemoval(t *testing.T) {
	y := strings.ReplaceAll(minimalValidMap,
		`  SESSION_RESUMED:
    requiredDocuments: [agents]
    allowedActions: [verify_state]
    forbiddenActions: []
    expectedResults: [RECOVER_COORDINATION_STATE, RESUME_ASSIGNMENT]
`, "")
	vs := violations(t, y)
	found := false
	for _, v := range vs {
		if strings.Contains(v.Detail, "SESSION_RESUMED") {
			found = true
		}
	}
	if !found {
		t.Error("mutation not detected: removal of SESSION_RESUMED should produce a violation mentioning it")
	}
}

func TestRealActionMap(t *testing.T) {
	const amPath = "../../docs/operations/ACTION-MAP.yaml"
	const regPath = "../../docs/DOCUMENT-REGISTRY.yaml"
	if _, err := os.Stat(amPath); err != nil {
		t.Skip("real action map not available:", err)
	}
	vs, err := Validate(amPath, regPath)
	if err != nil {
		t.Fatalf("parse error on real action map: %v", err)
	}
	if len(vs) != 0 {
		for _, v := range vs {
			t.Errorf("violation %s: %s", v.Code, v.Detail)
		}
	}
}
