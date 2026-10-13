package recovery

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

var requiredEvents = []string{
	"SESSION_INTERRUPTED", "SESSION_RESUMED", "SESSION_REPLACED",
	"CONTEXT_LOST", "WORKTREE_STATE_UNKNOWN",
	"REQUIRED_SOURCE_MISSING", "SUBJECT_DRIFT_DETECTED",
}

var requiredTransitions = []string{
	"RESUME_ASSIGNMENT", "FREEZE_FOR_REVIEW", "RECOVER_COORDINATION_STATE",
	"PREPARE_CORRECTION", "BLOCKED_EXTERNAL",
	"HUMAN_ACTION_REQUIRED", "OWNER_REQUIRED", "PRESERVE_AND_STOP",
}

// normativeAuthorityAllowlist holds the values that grant current normative authority.
var normativeAuthorityAllowlist = map[string]bool{
	"process": true, "product": true, "canonical": true,
}

// agentsRequiredPhrases are the recovery phrases that must appear in AGENTS.md.
var agentsRequiredPhrases = []string{
	"RECOVER_SESSION", "PRESERVE_AND_STOP",
	"RESUME_ASSIGNMENT", "RECOVER_COORDINATION_STATE",
}

// Violation is a single failing check with a stable reason code and detail.
type Violation struct {
	Code   string
	Detail string
}

// CheckOptions holds the paths required for the recovery governance check.
type CheckOptions struct {
	RegistryPath   string
	ActionMapPath  string
	AgentsPath     string
	RegulationPath string
	DecisionDir    string // defaults to <RegulationPath dir>/../decisions

	// RepoRoot is the canonical absolute path of the repository root. When set,
	// readDecisionCommitted verifies that git rev-parse --show-toplevel equals
	// this value before reading from HEAD, preventing an ancestor repository
	// from being accepted as the project's git context.
	RepoRoot string
}

// Check runs all recovery governance validations and returns violations.
func Check(opts CheckOptions) ([]Violation, error) {
	var vs []Violation

	registryVs, err := checkRegistry(opts)
	if err != nil {
		return nil, fmt.Errorf("registry check: %w", err)
	}
	vs = append(vs, registryVs...)

	amVs, err := checkActionMap(opts.ActionMapPath)
	if err != nil {
		return nil, fmt.Errorf("action map check: %w", err)
	}
	vs = append(vs, amVs...)

	agVs, err := checkAgents(opts.AgentsPath)
	if err != nil {
		return nil, fmt.Errorf("agents check: %w", err)
	}
	vs = append(vs, agVs...)

	return vs, nil
}

func checkRegistry(opts CheckOptions) ([]Violation, error) {
	type minDoc struct {
		ID                        string `yaml:"id"`
		Path                      string `yaml:"path"`
		Status                    string `yaml:"status"`
		NormativeAuthority        string `yaml:"normativeAuthority"`
		CurrentNormativeAuthority *bool  `yaml:"currentNormativeAuthority"`
		DecisionRef               string `yaml:"decisionRef"`
	}
	type minRegistry struct {
		Documents []minDoc `yaml:"documents"`
	}

	f, err := os.Open(opts.RegistryPath)
	if err != nil {
		return nil, fmt.Errorf("open registry: %w", err)
	}
	defer func() { _ = f.Close() }()

	var reg minRegistry
	if err := yaml.NewDecoder(f).Decode(&reg); err != nil {
		return nil, fmt.Errorf("parse registry: %w", err)
	}

	paths := map[string]minDoc{}
	for _, d := range reg.Documents {
		paths[d.Path] = d
	}

	var vs []Violation

	doc, ok := paths[opts.RegulationPath]
	if !ok {
		vs = append(vs, Violation{
			Code:   "REGULATION_NOT_REGISTERED",
			Detail: fmt.Sprintf("recovery regulation %q is not registered", opts.RegulationPath),
		})
		return vs, nil
	}

	if doc.Status != "accepted" {
		vs = append(vs, Violation{
			Code:   "REGULATION_NOT_ACCEPTED",
			Detail: fmt.Sprintf("recovery regulation status must be 'accepted', got %q", doc.Status),
		})
	}
	if !normativeAuthorityAllowlist[doc.NormativeAuthority] {
		vs = append(vs, Violation{
			Code:   "REGULATION_NOT_NORMATIVE",
			Detail: fmt.Sprintf("recovery regulation normativeAuthority must be in {process,product,canonical}, got %q", doc.NormativeAuthority),
		})
	}
	if doc.CurrentNormativeAuthority == nil || !*doc.CurrentNormativeAuthority {
		vs = append(vs, Violation{
			Code:   "REGULATION_CURRENT_NORMATIVE_MISSING",
			Detail: "recovery regulation must have currentNormativeAuthority=true",
		})
	}
	if doc.DecisionRef == "" {
		vs = append(vs, Violation{
			Code:   "REGULATION_MISSING_DECISION_REF",
			Detail: "recovery regulation must have a decisionRef",
		})
		return vs, nil
	}

	// decisionRef must be a bare ID (no path separators).
	if strings.ContainsAny(doc.DecisionRef, "/\\") || strings.HasPrefix(doc.DecisionRef, ".") {
		vs = append(vs, Violation{
			Code:   "INVALID_DECISION_REF",
			Detail: fmt.Sprintf("decisionRef must be a bare ID (no path separators), got %q", doc.DecisionRef),
		})
		return vs, nil
	}

	decisionDir := opts.DecisionDir
	if decisionDir == "" {
		// Derive decisions/ as a sibling of the directory containing the registry.
		decisionDir = filepath.Join(filepath.Dir(opts.RegistryPath), "decisions")
	}
	decisionDir = filepath.Clean(decisionDir)
	decisionPath := filepath.Join(decisionDir, doc.DecisionRef+".yaml")

	// Verify no path escape from decisions dir.
	resolvedDecision, err := filepath.Abs(decisionPath)
	if err != nil {
		vs = append(vs, Violation{Code: "DECISION_PATH_ERROR", Detail: fmt.Sprintf("cannot resolve decision path: %v", err)})
		return vs, nil
	}
	resolvedDir, err := filepath.Abs(decisionDir)
	if err != nil {
		vs = append(vs, Violation{Code: "DECISION_PATH_ERROR", Detail: fmt.Sprintf("cannot resolve decisions dir: %v", err)})
		return vs, nil
	}
	if !strings.HasPrefix(resolvedDecision+string(filepath.Separator), resolvedDir+string(filepath.Separator)) {
		vs = append(vs, Violation{Code: "DECISION_PATH_ESCAPE", Detail: "decision artifact path escapes docs/decisions/"})
		return vs, nil
	}

	// Verify the decision is committed in HEAD.
	decisionBytes, gitErr := readDecisionCommitted(resolvedDecision, opts.RepoRoot)
	if gitErr != nil {
		vs = append(vs, Violation{Code: "DECISION_NOT_COMMITTED", Detail: gitErr.Error()})
		return vs, nil
	}

	// Parse and validate the decision artifact.
	type decision struct {
		Status string `yaml:"status"`
		Class  string `yaml:"class"`
	}
	var dec decision
	if err := yaml.Unmarshal(decisionBytes, &dec); err != nil {
		vs = append(vs, Violation{Code: "DECISION_PARSE_ERROR", Detail: fmt.Sprintf("parse decision %q: %v", doc.DecisionRef, err)})
		return vs, nil
	}
	if dec.Status != "accepted" {
		vs = append(vs, Violation{Code: "DECISION_NOT_ACCEPTED", Detail: fmt.Sprintf("decision %q status must be 'accepted', got %q", doc.DecisionRef, dec.Status)})
	}
	if dec.Class != "R" {
		vs = append(vs, Violation{Code: "DECISION_NOT_CLASS_R", Detail: fmt.Sprintf("decision %q must be class R, got %q", doc.DecisionRef, dec.Class)})
	}

	return vs, nil
}

// readDecisionCommitted reads absPath from the git HEAD of its repository and
// returns the committed bytes. It fails closed:
//   - if the file is not inside a git repository, it returns an error;
//   - if repoRoot is non-empty, it verifies that git rev-parse --show-toplevel
//     equals repoRoot; a mismatch (ancestor repo) is rejected.
//
// No fallback to disk read: untracked files are not accepted as committed.
func readDecisionCommitted(absPath, repoRoot string) ([]byte, error) {
	dir := filepath.Dir(absPath)

	// Determine the canonical repository top-level from git.
	tlCmd := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel")
	tlOut, tlErr := tlCmd.Output()
	if tlErr != nil {
		return nil, fmt.Errorf("decision file %q is not inside a git repository: cannot verify committed state (fail closed)", absPath)
	}
	detectedRoot := filepath.Clean(strings.TrimSpace(string(tlOut)))

	// When an explicit boundary was provided, verify we are in the correct repo.
	// Resolve symlinks on both sides to handle platforms where os.TempDir() and
	// filepath.EvalSymlinks differ (e.g. macOS /var → /private/var).
	if repoRoot != "" {
		canonRoot := filepath.Clean(repoRoot)
		if resolved, err := filepath.EvalSymlinks(canonRoot); err == nil {
			canonRoot = resolved
		}
		resolvedDetected := detectedRoot
		if resolved, err := filepath.EvalSymlinks(detectedRoot); err == nil {
			resolvedDetected = resolved
		}
		if resolvedDetected != canonRoot {
			return nil, fmt.Errorf("decision file %q belongs to repository at %q, not the required %q (ancestor repository rejected)", absPath, detectedRoot, repoRoot)
		}
	}

	// Resolve symlinks on absPath so filepath.Rel works correctly on platforms
	// where os.TempDir() differs from the canonical path (e.g. macOS /var → /private/var).
	resolvedAbsPath := absPath
	if rp, err := filepath.EvalSymlinks(absPath); err == nil {
		resolvedAbsPath = rp
	}
	rel, err := filepath.Rel(detectedRoot, resolvedAbsPath)
	if err != nil {
		return nil, fmt.Errorf("cannot relativize decision path to repo root: %w", err)
	}
	rel = filepath.ToSlash(rel)

	cmd := exec.Command("git", "-C", detectedRoot, "show", "HEAD:"+rel)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("decision artifact %q exists on disk but is not committed in HEAD", rel)
	}
	return out, nil
}

func checkActionMap(actionMapPath string) ([]Violation, error) {
	type eventBody struct {
		ExpectedResults []string `yaml:"expectedResults"`
	}
	type minActionMap struct {
		Events              map[string]eventBody `yaml:"events"`
		RecoveryTransitions map[string]struct{}  `yaml:"recoveryTransitions"`
	}

	f, err := os.Open(actionMapPath)
	if err != nil {
		return nil, fmt.Errorf("open action map: %w", err)
	}
	defer func() { _ = f.Close() }()

	var am minActionMap
	if err := yaml.NewDecoder(f).Decode(&am); err != nil {
		return nil, fmt.Errorf("parse action map: %w", err)
	}

	var vs []Violation

	for _, ev := range requiredEvents {
		if _, ok := am.Events[ev]; !ok {
			vs = append(vs, Violation{Code: "MISSING_RECOVERY_EVENT", Detail: fmt.Sprintf("required recovery event %s absent", ev)})
		}
	}

	// Collect all expectedResults.
	allExpected := map[string]bool{}
	for _, body := range am.Events {
		for _, r := range body.ExpectedResults {
			allExpected[r] = true
		}
	}

	for _, t := range requiredTransitions {
		if _, ok := am.RecoveryTransitions[t]; !ok {
			vs = append(vs, Violation{Code: "MISSING_RECOVERY_TRANSITION", Detail: fmt.Sprintf("required recovery transition %s absent from recoveryTransitions", t)})
		}
		if _, ok := am.RecoveryTransitions[t]; ok && !allExpected[t] {
			vs = append(vs, Violation{Code: "UNREACHABLE_TRANSITION", Detail: fmt.Sprintf("transition %s is defined but not reachable from any event's expectedResults", t)})
		}
	}

	// RECOVER_COORDINATION_STATE must not appear as an event key; it is a
	// recovery transition only. Adding it as an event would allow bypass of the
	// transition-only access control.
	if _, ok := am.Events["RECOVER_COORDINATION_STATE"]; ok {
		vs = append(vs, Violation{
			Code:   "RECOVER_COORDINATION_STATE_AS_EVENT",
			Detail: "RECOVER_COORDINATION_STATE must not appear in events; it is a recoveryTransitions entry only",
		})
	}

	// RECOVER_COORDINATION_STATE edge checks.
	if wtu, ok := am.Events["WORKTREE_STATE_UNKNOWN"]; ok {
		if !containsStr(wtu.ExpectedResults, "RECOVER_COORDINATION_STATE") {
			vs = append(vs, Violation{Code: "MISSING_RECOVER_COORDINATION_STATE_EDGE", Detail: "RECOVER_COORDINATION_STATE not reachable from WORKTREE_STATE_UNKNOWN"})
		}
	}
	if sr, ok := am.Events["SESSION_RESUMED"]; ok {
		if !containsStr(sr.ExpectedResults, "RECOVER_COORDINATION_STATE") {
			vs = append(vs, Violation{Code: "MISSING_RECOVER_COORDINATION_STATE_EDGE", Detail: "RECOVER_COORDINATION_STATE not reachable from SESSION_RESUMED"})
		}
	}

	return vs, nil
}

func checkAgents(agentsPath string) ([]Violation, error) {
	content, err := os.ReadFile(agentsPath)
	if err != nil {
		return nil, fmt.Errorf("read AGENTS.md: %w", err)
	}
	var vs []Violation
	text := string(content)
	for _, phrase := range agentsRequiredPhrases {
		if !strings.Contains(text, phrase) {
			vs = append(vs, Violation{Code: "AGENTS_MISSING_PHRASE", Detail: fmt.Sprintf("AGENTS.md missing required recovery phrase %q", phrase)})
		}
	}
	return vs, nil
}

func containsStr(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
