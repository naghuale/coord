package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeBootstrap(t *testing.T, root, kind, admissionResult string) string {
	t.Helper()
	b := bootstrapFile{}
	b.Kind = kind
	b.WorkspaceBinding.ProjectRoot = root
	b.WorkspaceBinding.RepositoryRoot = root
	b.WorkspaceBinding.AssignedWorktree = filepath.Join(root, "worktree")
	b.Assignment.AssignmentID = "a1"
	b.Assignment.BaseSHA = "273c2c2b5e4f1a9d0c3b8e7f6a5d4c3b2a1e0f9e"
	b.Admission.Result = admissionResult
	if err := os.MkdirAll(filepath.Join(root, "worktree"), 0755); err != nil {
		t.Fatalf("create worktree fixture: %v", err)
	}
	data, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("marshal bootstrap: %v", err)
	}
	path := filepath.Join(root, "bootstrap.json")
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write bootstrap: %v", err)
	}
	return path
}

func TestLauncherDryRun(t *testing.T) {
	root := t.TempDir()
	bp := writeBootstrap(t, root, "AgentBootstrap", "ADMITTED")
	executorCalled := false
	executor := func(context.Context, []string, string) error {
		executorCalled = true
		return nil
	}
	if err := runAgentLaunchWithExecutor(context.Background(), []string{"--bootstrap", bp, "--dry-run"}, executor); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if executorCalled {
		t.Fatal("dry-run invoked the process executor")
	}
}

func TestLauncherExecuteInvokesExecutorWithBoundProjectRoot(t *testing.T) {
	root := t.TempDir()
	bp := writeBootstrap(t, root, "AgentBootstrap", "ADMITTED")
	wantArgs := []string{"/bin/echo", "launch", "a1"}
	wantErr := errors.New("executor sentinel")
	calls := 0
	var gotArgs []string
	var gotProjectRoot string
	executor := func(_ context.Context, args []string, projectRoot string) error {
		calls++
		gotArgs = append([]string(nil), args...)
		gotProjectRoot = projectRoot
		return wantErr
	}

	err := runAgentLaunchWithExecutor(context.Background(), []string{
		"--bootstrap", bp, "--execute", "--", wantArgs[0], wantArgs[1], wantArgs[2],
	}, executor)
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected executor error %v to propagate, got %v", wantErr, err)
	}
	if calls != 1 {
		t.Fatalf("executor calls = %d, want exactly one", calls)
	}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("executor args = %#v, want %#v", gotArgs, wantArgs)
	}
	if gotProjectRoot != root {
		t.Fatalf("executor ProjectRoot = %q, want bootstrap ProjectRoot %q", gotProjectRoot, root)
	}
}

func TestAgentLaunchWrongKind(t *testing.T) {
	root := t.TempDir()
	bp := writeBootstrap(t, root, "WrongKind", "ADMITTED")
	err := runAgentLaunch(context.Background(), []string{"--bootstrap", bp, "--dry-run"})
	if err == nil || !strings.Contains(err.Error(), "BOOTSTRAP_KIND_INVALID") {
		t.Fatalf("expected BOOTSTRAP_KIND_INVALID, got %v", err)
	}
}

func TestAgentLaunchNotAdmitted(t *testing.T) {
	root := t.TempDir()
	bp := writeBootstrap(t, root, "AgentBootstrap", "INVALID_ADMISSION")
	err := runAgentLaunch(context.Background(), []string{"--bootstrap", bp, "--dry-run"})
	if err == nil || !strings.Contains(err.Error(), "ACTION_CONTEXT_NOT_ADMITTED") {
		t.Fatalf("expected ACTION_CONTEXT_NOT_ADMITTED, got %v", err)
	}
}

func TestAgentLaunchExecuteRelativePathRejected(t *testing.T) {
	root := t.TempDir()
	bp := writeBootstrap(t, root, "AgentBootstrap", "ADMITTED")
	err := runAgentLaunch(context.Background(), []string{"--bootstrap", bp, "--execute", "--", "relative/path"})
	if err == nil || !strings.Contains(err.Error(), "EXECUTABLE_NOT_ABSOLUTE") {
		t.Fatalf("expected EXECUTABLE_NOT_ABSOLUTE, got %v", err)
	}
}
