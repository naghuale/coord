package admission

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func makeBootstrap(t *testing.T, projectRoot, repoRoot, worktree string) AgentBootstrap {
	t.Helper()
	actionCtx, _ := json.Marshal(map[string]any{
		"admission": map[string]any{"result": "ADMITTED"},
	})
	return AgentBootstrap{
		APIVersion:          "coord.dev/v1alpha1",
		Kind:                "AgentBootstrap",
		BootstrapValidation: BootstrapResult{Result: "ADMITTED"},
		ActionContext:       json.RawMessage(actionCtx),
		WorkspaceBinding: WorkspaceBinding{
			ProjectRoot:      projectRoot,
			RepositoryRoot:   repoRoot,
			AssignedWorktree: worktree,
		},
		Session:    SessionBinding{Role: "COORDINATOR", AgentID: "test-agent"},
		Assignment: Assignment{AssignmentID: "a1", BaseSHA: "abc123"},
	}
}

func TestValidateAdmitted(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "repo"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "worktree"), 0755); err != nil {
		t.Fatal(err)
	}
	b := makeBootstrap(t, root, filepath.Join(root, "repo"), filepath.Join(root, "worktree"))
	result, err := Validate(b, ValidateOptions{DryRun: true})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if result.Result != StatusAdmitted {
		t.Fatalf("expected ADMITTED, got %s: %v", result.Result, result.Errors)
	}
}

func TestValidateWorktreeEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "repo"), 0755); err != nil {
		t.Fatal(err)
	}
	b := makeBootstrap(t, root, filepath.Join(root, "repo"), outside)
	result, err := Validate(b, ValidateOptions{DryRun: true})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if result.Result != StatusInvalidAdmission {
		t.Fatalf("expected INVALID_ADMISSION for worktree outside projectRoot")
	}
	found := false
	for _, e := range result.Errors {
		if len(e) >= len(ErrWorkspaceBindingInvalid) && e[:len(ErrWorkspaceBindingInvalid)] == ErrWorkspaceBindingInvalid {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected %s in errors, got %v", ErrWorkspaceBindingInvalid, result.Errors)
	}
}

func TestValidateSideEffectsBlocked(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "repo"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "worktree"), 0755); err != nil {
		t.Fatal(err)
	}
	b := makeBootstrap(t, root, filepath.Join(root, "repo"), filepath.Join(root, "worktree"))
	result, err := Validate(b, ValidateOptions{DryRun: true, RealSideEffects: true})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if result.Result != StatusInvalidAdmission {
		t.Fatalf("expected INVALID_ADMISSION when RealSideEffects=true")
	}
	found := false
	for _, e := range result.Errors {
		if len(e) >= len(ErrSideEffectsBlocked) && e[:len(ErrSideEffectsBlocked)] == ErrSideEffectsBlocked {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected %s in errors, got %v", ErrSideEffectsBlocked, result.Errors)
	}
}

func TestValidateBaseSHAMismatch(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	worktree := filepath.Join(root, "worktree")
	if err := os.MkdirAll(repo, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(worktree, 0755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "init", "-q")
	runGit(t, repo, "config", "user.name", "Coord Test")
	runGit(t, repo, "config", "user.email", "coord-test@example.invalid")
	runGit(t, repo, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "tracked.txt")
	runGit(t, repo, "commit", "-q", "-m", "fixture")

	b := makeBootstrap(t, root, repo, worktree)
	b.Assignment.BaseSHA = strings.Repeat("a", 40)
	result, err := Validate(b, ValidateOptions{RequireBaseSHAMatch: true, DryRun: true})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if result.Result != StatusInvalidAdmission {
		t.Fatalf("expected INVALID_ADMISSION for base SHA mismatch, got %s", result.Result)
	}
	if !containsError(result.Errors, ErrBaseSHAMismatch) {
		t.Fatalf("expected %s in errors, got %v", ErrBaseSHAMismatch, result.Errors)
	}
}

func TestValidateExecutableNotAllowed(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	worktree := filepath.Join(root, "worktree")
	if err := os.MkdirAll(repo, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(worktree, 0755); err != nil {
		t.Fatal(err)
	}
	b := makeBootstrap(t, root, repo, worktree)
	result, err := Validate(b, ValidateOptions{
		ExecutablePath:      "/usr/bin/env",
		ExecutableAllowlist: []string{"/usr/bin/grep"},
		DryRun:              true,
	})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if result.Result != StatusInvalidAdmission {
		t.Fatalf("expected INVALID_ADMISSION for executable outside allowlist, got %s", result.Result)
	}
	if !containsError(result.Errors, ErrExecutableNotAllowed) {
		t.Fatalf("expected %s in errors, got %v", ErrExecutableNotAllowed, result.Errors)
	}
}

// TestLauncherDryRun verifies parity with Python test_launcher_dry_run:
// when realSideEffects.allowed is false (DryRun mode), bootstrap validation
// must be ADMITTED and the result must carry DryRun=true (executed=false).
// This covers the build-agent-bootstrap → launch-agent dry-run path in Go.
func TestLauncherDryRun(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "repo"), 0755); err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(root, "worktree")
	if err := os.MkdirAll(worktree, 0755); err != nil {
		t.Fatal(err)
	}
	b := makeBootstrap(t, root, filepath.Join(root, "repo"), worktree)
	// RealSideEffects: false → dry-run; DryRun: true is explicit.
	result, err := Validate(b, ValidateOptions{DryRun: true, RealSideEffects: false})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if result.Result != StatusAdmitted {
		t.Fatalf("expected ADMITTED in dry-run mode, got %s: %v", result.Result, result.Errors)
	}
	if !result.DryRun {
		t.Error("expected DryRun=true (executed=false) in the admission result")
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}

func containsError(errors []string, code string) bool {
	for _, err := range errors {
		if strings.HasPrefix(err, code) {
			return true
		}
	}
	return false
}
