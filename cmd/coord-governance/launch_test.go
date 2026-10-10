package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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

func TestAgentLaunchDryRunAdmitted(t *testing.T) {
	root := t.TempDir()
	bp := writeBootstrap(t, root, "AgentBootstrap", "ADMITTED")
	if err := runAgentLaunch(context.Background(), []string{"--bootstrap", bp, "--dry-run"}); err != nil {
		t.Fatalf("expected nil, got %v", err)
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
