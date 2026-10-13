package recovery

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// buildFixtures creates a minimal valid tree under root and returns opts.
func buildFixtures(t *testing.T, root string) CheckOptions {
	t.Helper()

	docsDir := filepath.Join(root, "docs")
	opsDir := filepath.Join(docsDir, "operations")
	decisionsDir := filepath.Join(docsDir, "decisions")
	for _, d := range []string{opsDir, decisionsDir} {
		if err := os.MkdirAll(d, 0750); err != nil {
			t.Fatal(err)
		}
	}

	regulationPath := filepath.Join(opsDir, "REGULATION.md")
	if err := os.WriteFile(regulationPath, []byte("# Recovery Regulation"), 0600); err != nil {
		t.Fatal(err)
	}

	decisionPath := filepath.Join(decisionsDir, "RECOVERY-REGULATION-R1.yaml")
	if err := os.WriteFile(decisionPath, []byte("status: accepted\nclass: R\n"), 0600); err != nil {
		t.Fatal(err)
	}

	registryContent := `apiVersion: coord.dev/v1alpha1
kind: DocumentRegistry
documents:
  - id: recovery-regulation
    path: docs/operations/REGULATION.md
    class: process
    status: accepted
    purpose: [recovery]
    usedBy: [COORDINATOR]
    readWhen: [SESSION_START]
    updateWhen: [PROCESS_NORM_CHANGED]
    currentOperationalAuthority: false
    canonicalProductionAuthority: false
    normativeAuthority: process
    currentNormativeAuthority: true
    decisionRef: RECOVERY-REGULATION-R1
    ownership: {}
    acceptance: {}
`
	registryPath := filepath.Join(root, "registry.yaml")
	if err := os.WriteFile(registryPath, []byte(registryContent), 0600); err != nil {
		t.Fatal(err)
	}

	actionMapContent := buildMinimalActionMap()
	actionMapPath := filepath.Join(root, "action-map.yaml")
	if err := os.WriteFile(actionMapPath, []byte(actionMapContent), 0600); err != nil {
		t.Fatal(err)
	}

	agentsContent := "RECOVER_SESSION\nPRESERVE_AND_STOP\nRESUME_ASSIGNMENT\nRECOVER_COORDINATION_STATE\n"
	agentsPath := filepath.Join(root, "AGENTS.md")
	if err := os.WriteFile(agentsPath, []byte(agentsContent), 0600); err != nil {
		t.Fatal(err)
	}

	return CheckOptions{
		RegistryPath:   registryPath,
		ActionMapPath:  actionMapPath,
		AgentsPath:     agentsPath,
		RegulationPath: "docs/operations/REGULATION.md",
		DecisionDir:    decisionsDir,
	}
}

func buildMinimalActionMap() string {
	var sb strings.Builder
	sb.WriteString("apiVersion: coord.dev/v1alpha1\nkind: ActionMap\nevents:\n")
	// Each event contributes expected results so all required transitions are reachable.
	events := map[string][]string{
		"SESSION_INTERRUPTED":     {"PRESERVE_AND_STOP", "HUMAN_ACTION_REQUIRED"},
		"SESSION_REPLACED":        {"FREEZE_FOR_REVIEW", "OWNER_REQUIRED"},
		"CONTEXT_LOST":            {"RECOVER_COORDINATION_STATE", "BLOCKED_EXTERNAL"},
		"REQUIRED_SOURCE_MISSING": {"PREPARE_CORRECTION", "BLOCKED_EXTERNAL"},
		"SUBJECT_DRIFT_DETECTED":  {"FREEZE_FOR_REVIEW", "PRESERVE_AND_STOP"},
		"SESSION_RESUMED":         {"RECOVER_COORDINATION_STATE", "RESUME_ASSIGNMENT"},
		"WORKTREE_STATE_UNKNOWN":  {"RECOVER_COORDINATION_STATE", "PREPARE_CORRECTION"},
	}
	for ev, results := range events {
		sb.WriteString("  " + ev + ":\n    requiredDocuments: []\n    allowedActions: []\n    forbiddenActions: []\n    expectedResults: [" + strings.Join(results, ", ") + "]\n")
	}
	sb.WriteString("recoveryTransitions:\n")
	for _, t := range requiredTransitions {
		sb.WriteString("  " + t + ":\n    when: test\n")
	}
	return sb.String()
}

func hasCode(vs []Violation, code string) bool {
	for _, v := range vs {
		if v.Code == code {
			return true
		}
	}
	return false
}

func TestValidFixture(t *testing.T) {
	root := t.TempDir()
	opts := buildFixtures(t, root)
	vs, err := Check(opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vs) != 0 {
		t.Errorf("expected no violations, got: %v", vs)
	}
}

func TestRegulationProposed(t *testing.T) {
	root := t.TempDir()
	opts := buildFixtures(t, root)
	// Mutate: change status to proposed.
	reg, err := os.ReadFile(opts.RegistryPath)
	if err != nil {
		t.Fatal(err)
	}
	reg2 := strings.ReplaceAll(string(reg), "status: accepted", "status: proposed")
	if err := os.WriteFile(opts.RegistryPath, []byte(reg2), 0600); err != nil {
		t.Fatal(err)
	}
	vs, err := Check(opts)
	if err != nil {
		t.Fatal(err)
	}
	if !hasCode(vs, "REGULATION_NOT_ACCEPTED") {
		t.Errorf("expected REGULATION_NOT_ACCEPTED, got: %v", vs)
	}
}

func TestRegulationNormativeAuthorityFalse(t *testing.T) {
	root := t.TempDir()
	opts := buildFixtures(t, root)
	reg, err := os.ReadFile(opts.RegistryPath)
	if err != nil {
		t.Fatal(err)
	}
	reg2 := strings.ReplaceAll(string(reg), "normativeAuthority: process", "normativeAuthority: none")
	if err := os.WriteFile(opts.RegistryPath, []byte(reg2), 0600); err != nil {
		t.Fatal(err)
	}
	vs, err := Check(opts)
	if err != nil {
		t.Fatal(err)
	}
	if !hasCode(vs, "REGULATION_NOT_NORMATIVE") {
		t.Errorf("expected REGULATION_NOT_NORMATIVE, got: %v", vs)
	}
}

func TestRegulationDecisionMissing(t *testing.T) {
	root := t.TempDir()
	opts := buildFixtures(t, root)
	// Remove the decision file.
	decPath := filepath.Join(opts.DecisionDir, "RECOVERY-REGULATION-R1.yaml")
	if err := os.Remove(decPath); err != nil {
		t.Fatal(err)
	}
	vs, err := Check(opts)
	if err != nil {
		t.Fatal(err)
	}
	if !hasCode(vs, "DECISION_NOT_COMMITTED") {
		t.Errorf("expected DECISION_NOT_COMMITTED, got: %v", vs)
	}
}

func TestDecisionClassInvalid(t *testing.T) {
	root := t.TempDir()
	opts := buildFixtures(t, root)
	decPath := filepath.Join(opts.DecisionDir, "RECOVERY-REGULATION-R1.yaml")
	if err := os.WriteFile(decPath, []byte("status: accepted\nclass: TD\n"), 0600); err != nil {
		t.Fatal(err)
	}
	vs, err := Check(opts)
	if err != nil {
		t.Fatal(err)
	}
	if !hasCode(vs, "DECISION_NOT_CLASS_R") {
		t.Errorf("expected DECISION_NOT_CLASS_R, got: %v", vs)
	}
}

func TestDecisionStatusInvalid(t *testing.T) {
	root := t.TempDir()
	opts := buildFixtures(t, root)
	decPath := filepath.Join(opts.DecisionDir, "RECOVERY-REGULATION-R1.yaml")
	if err := os.WriteFile(decPath, []byte("status: proposed\nclass: R\n"), 0600); err != nil {
		t.Fatal(err)
	}
	vs, err := Check(opts)
	if err != nil {
		t.Fatal(err)
	}
	if !hasCode(vs, "DECISION_NOT_ACCEPTED") {
		t.Errorf("expected DECISION_NOT_ACCEPTED, got: %v", vs)
	}
}

func TestRegulationNotRegistered(t *testing.T) {
	root := t.TempDir()
	opts := buildFixtures(t, root)
	// Point at a path not in the registry.
	opts.RegulationPath = "docs/operations/NONEXISTENT.md"
	vs, err := Check(opts)
	if err != nil {
		t.Fatal(err)
	}
	if !hasCode(vs, "REGULATION_NOT_REGISTERED") {
		t.Errorf("expected REGULATION_NOT_REGISTERED, got: %v", vs)
	}
}

func TestMissingRecoveryEvent(t *testing.T) {
	root := t.TempDir()
	opts := buildFixtures(t, root)
	am, _ := os.ReadFile(opts.ActionMapPath)
	// Remove SESSION_RESUMED event.
	lines := strings.Split(string(am), "\n")
	var filtered []string
	skip := false
	for _, l := range lines {
		if strings.TrimSpace(l) == "SESSION_RESUMED:" {
			skip = true
		}
		if skip && strings.HasPrefix(l, "  ") && !strings.HasPrefix(l, "  SESSION_RESUMED") {
			if strings.TrimSpace(l) != "" && !strings.HasPrefix(strings.TrimSpace(l), "required") &&
				!strings.HasPrefix(strings.TrimSpace(l), "allowed") &&
				!strings.HasPrefix(strings.TrimSpace(l), "forbidden") &&
				!strings.HasPrefix(strings.TrimSpace(l), "expected") {
				skip = false
			}
		}
		if !skip {
			filtered = append(filtered, l)
		}
		if skip && strings.HasPrefix(l, "    expectedResults") {
			skip = false
		}
	}
	if err := os.WriteFile(opts.ActionMapPath, []byte(strings.Join(filtered, "\n")), 0600); err != nil {
		t.Fatal(err)
	}
	vs, err := Check(opts)
	if err != nil {
		t.Fatal(err)
	}
	if !hasCode(vs, "MISSING_RECOVERY_EVENT") {
		t.Errorf("expected MISSING_RECOVERY_EVENT, got: %v", vs)
	}
}

func TestAgentsMissingPhrase(t *testing.T) {
	root := t.TempDir()
	opts := buildFixtures(t, root)
	// Remove RECOVER_SESSION from AGENTS.md.
	content, err := os.ReadFile(opts.AgentsPath)
	if err != nil {
		t.Fatal(err)
	}
	content2 := strings.ReplaceAll(string(content), "RECOVER_SESSION\n", "")
	if err := os.WriteFile(opts.AgentsPath, []byte(content2), 0600); err != nil {
		t.Fatal(err)
	}
	vs, err := Check(opts)
	if err != nil {
		t.Fatal(err)
	}
	if !hasCode(vs, "AGENTS_MISSING_PHRASE") {
		t.Errorf("expected AGENTS_MISSING_PHRASE, got: %v", vs)
	}
}

func TestDecisionRefWithPathSeparator(t *testing.T) {
	root := t.TempDir()
	opts := buildFixtures(t, root)
	reg, err := os.ReadFile(opts.RegistryPath)
	if err != nil {
		t.Fatal(err)
	}
	reg2 := strings.ReplaceAll(string(reg), "decisionRef: RECOVERY-REGULATION-R1", "decisionRef: path/RECOVERY-REGULATION-R1")
	if err := os.WriteFile(opts.RegistryPath, []byte(reg2), 0600); err != nil {
		t.Fatal(err)
	}
	vs, err := Check(opts)
	if err != nil {
		t.Fatal(err)
	}
	if !hasCode(vs, "INVALID_DECISION_REF") {
		t.Errorf("expected INVALID_DECISION_REF for path separator in decisionRef, got: %v", vs)
	}
}

// MutationTest: reverting status to proposed must be detected.
func TestMutation_RegulationProposed(t *testing.T) {
	root := t.TempDir()
	opts := buildFixtures(t, root)
	reg, err := os.ReadFile(opts.RegistryPath)
	if err != nil {
		t.Fatal(err)
	}
	mutated := strings.ReplaceAll(string(reg), "status: accepted", "status: proposed")
	if err := os.WriteFile(opts.RegistryPath, []byte(mutated), 0600); err != nil {
		t.Fatal(err)
	}
	vs, err := Check(opts)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, v := range vs {
		if strings.Contains(v.Detail, "proposed") || v.Code == "REGULATION_NOT_ACCEPTED" {
			found = true
		}
	}
	if !found {
		t.Error("mutation not detected: proposed status should produce a violation")
	}
}

func TestRealGovenance(t *testing.T) {
	const base = "../.."
	opts := CheckOptions{
		RegistryPath:   base + "/docs/DOCUMENT-REGISTRY.yaml",
		ActionMapPath:  base + "/docs/operations/ACTION-MAP.yaml",
		AgentsPath:     base + "/AGENTS.md",
		RegulationPath: "docs/operations/AGENT-SESSION-RECOVERY-REGULATION.md",
	}
	for _, p := range []string{opts.RegistryPath, opts.ActionMapPath, opts.AgentsPath} {
		if _, err := os.Stat(p); err != nil {
			t.Skip("real files not available:", p)
		}
	}
	vs, err := Check(opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vs) != 0 {
		for _, v := range vs {
			t.Errorf("violation %s: %s", v.Code, v.Detail)
		}
	}
}
