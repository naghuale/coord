package defectpatterns

import (
	"os"
	"path/filepath"
	"testing"
)

const minimalValidPatterns = `apiVersion: coord.dev/v1alpha1
kind: DefectPatterns
patterns:
  - patternId: DP-001
    title: "Test pattern"
    automationStatus: PARTIAL
    owningNorm: AGENTS.md
    selfTestRef: "coord governance check-defect-patterns"
`

func writeFixture(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "defect-patterns.yaml")
	if err := os.WriteFile(p, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return p
}

func writeAgentsMD(t *testing.T, root string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("# AGENTS"), 0600); err != nil {
		t.Fatal(err)
	}
}

func violations(t *testing.T, yaml, root string) []Violation {
	t.Helper()
	p := writeFixture(t, yaml)
	vs, err := Validate(p, root)
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	return vs
}

func parseError(t *testing.T, yaml string) error {
	t.Helper()
	p := writeFixture(t, yaml)
	_, err := Validate(p, t.TempDir())
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

func TestValidPatterns(t *testing.T) {
	root := t.TempDir()
	writeAgentsMD(t, root)
	vs := violations(t, minimalValidPatterns, root)
	if len(vs) != 0 {
		t.Errorf("expected no violations, got: %v", vs)
	}
}

func TestDuplicatePatternID(t *testing.T) {
	root := t.TempDir()
	writeAgentsMD(t, root)
	y := `apiVersion: coord.dev/v1alpha1
kind: DefectPatterns
patterns:
  - patternId: DP-001
    title: "First"
    automationStatus: PARTIAL
    owningNorm: AGENTS.md
  - patternId: DP-001
    title: "Duplicate"
    automationStatus: PARTIAL
    owningNorm: AGENTS.md
`
	vs := violations(t, y, root)
	if !hasCode(vs, "DUPLICATE_PATTERN_ID") {
		t.Errorf("expected DUPLICATE_PATTERN_ID, got: %v", vs)
	}
}

func TestInvalidAutomationStatus(t *testing.T) {
	root := t.TempDir()
	writeAgentsMD(t, root)
	y := `apiVersion: coord.dev/v1alpha1
kind: DefectPatterns
patterns:
  - patternId: DP-001
    title: "Test"
    automationStatus: UNKNOWN
    owningNorm: AGENTS.md
`
	vs := violations(t, y, root)
	if !hasCode(vs, "INVALID_AUTOMATION_STATUS") {
		t.Errorf("expected INVALID_AUTOMATION_STATUS, got: %v", vs)
	}
}

func TestMissingOwningNorm(t *testing.T) {
	root := t.TempDir()
	y := `apiVersion: coord.dev/v1alpha1
kind: DefectPatterns
patterns:
  - patternId: DP-001
    title: "Test"
    automationStatus: PARTIAL
    owningNorm: ""
`
	vs := violations(t, y, root)
	if !hasCode(vs, "MISSING_OWNING_NORM") {
		t.Errorf("expected MISSING_OWNING_NORM, got: %v", vs)
	}
}

func TestFullWithoutSelfTest(t *testing.T) {
	root := t.TempDir()
	writeAgentsMD(t, root)
	y := `apiVersion: coord.dev/v1alpha1
kind: DefectPatterns
patterns:
  - patternId: DP-001
    title: "Test"
    automationStatus: FULL
    owningNorm: AGENTS.md
`
	vs := violations(t, y, root)
	if !hasCode(vs, "FULL_MISSING_SELF_TEST") {
		t.Errorf("expected FULL_MISSING_SELF_TEST, got: %v", vs)
	}
}

func TestUnknownField(t *testing.T) {
	err := parseError(t, `apiVersion: coord.dev/v1alpha1
kind: DefectPatterns
patterns:
  - patternId: DP-001
    unknownField: bad
`)
	if err == nil {
		t.Error("expected parse error for unknown field, got nil")
	}
}

func TestOwningNormNotFound(t *testing.T) {
	root := t.TempDir()
	// Don't create AGENTS.md — should produce OWNING_NORM_NOT_FOUND.
	vs := violations(t, minimalValidPatterns, root)
	if !hasCode(vs, "OWNING_NORM_NOT_FOUND") {
		t.Errorf("expected OWNING_NORM_NOT_FOUND, got: %v", vs)
	}
}

// MutationTest: removing the FULL self-test requirement must be detected.
func TestMutation_FullRequiresSelfTest(t *testing.T) {
	root := t.TempDir()
	writeAgentsMD(t, root)
	y := `apiVersion: coord.dev/v1alpha1
kind: DefectPatterns
patterns:
  - patternId: DP-001
    title: "Full automation no selftest"
    automationStatus: FULL
    owningNorm: AGENTS.md
`
	vs := violations(t, y, root)
	found := false
	for _, v := range vs {
		if v.Code == "FULL_MISSING_SELF_TEST" {
			found = true
		}
	}
	if !found {
		t.Error("mutation not detected: FULL automation without selfTestRef must be rejected")
	}
}

func TestRealDefectPatterns(t *testing.T) {
	const patternsPath = "../../docs/operations/DEFECT-PATTERNS.yaml"
	const root = "../.."
	if _, err := os.Stat(patternsPath); err != nil {
		t.Skip("real defect patterns not available:", err)
	}
	vs, err := Validate(patternsPath, root)
	if err != nil {
		t.Fatalf("parse error on real defect patterns: %v", err)
	}
	if len(vs) != 0 {
		for _, v := range vs {
			t.Errorf("violation %s: %s", v.Code, v.Detail)
		}
	}
}
