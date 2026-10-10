package admission

import (
	"encoding/json"
	"os"
	"path/filepath"
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
