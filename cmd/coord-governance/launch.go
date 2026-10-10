package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"coord/internal/actioncontext"
	"coord/internal/governanceio"
)

// bootstrapFile is the minimal schema used by launcher admission.
type bootstrapFile struct {
	Kind             string `json:"kind"`
	WorkspaceBinding struct {
		ProjectRoot      string `json:"projectRoot"`
		RepositoryRoot   string `json:"repositoryRoot"`
		AssignedWorktree string `json:"assignedWorktree"`
	} `json:"workspaceBinding"`
	Assignment struct {
		AssignmentID string `json:"assignmentId"`
		BaseSHA      string `json:"baseSha"`
	} `json:"assignment"`
	Admission struct {
		Result string `json:"result"`
	} `json:"admission"`
}

func runAgentLaunch(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("agent launch", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	bootstrapPath := fs.String("bootstrap", "", "path to bootstrap JSON file")
	dryRun := fs.Bool("dry-run", false, "validate bootstrap without executing")
	execute := fs.Bool("execute", false, "launch the agent after admission")

	flagArgs, cmdArgs := splitCommand(args)
	if err := fs.Parse(flagArgs); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected arguments before --")
	}
	if *bootstrapPath == "" {
		return fmt.Errorf("--bootstrap is required")
	}
	if *dryRun == *execute {
		return fmt.Errorf("exactly one of --dry-run or --execute is required")
	}
	if *execute && len(cmdArgs) == 0 {
		return fmt.Errorf("--execute requires a command after --")
	}
	if *dryRun && len(cmdArgs) != 0 {
		return fmt.Errorf("--dry-run does not accept a command")
	}

	f, err := os.Open(*bootstrapPath)
	if err != nil {
		return fmt.Errorf("open bootstrap: %w", err)
	}
	bootstrap, decodeErr := governanceio.DecodeStrict[bootstrapFile](f)
	closeErr := f.Close()
	if decodeErr != nil {
		return fmt.Errorf("decode bootstrap: %w", decodeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close bootstrap: %w", closeErr)
	}

	errs := validateLaunchBootstrap(bootstrap)
	if *execute {
		errs = append(errs, validateExecutable(cmdArgs[0])...)
	}

	receipt := map[string]any{
		"kind":         "LaunchReceipt",
		"assignmentId": bootstrap.Assignment.AssignmentID,
		"baseSha":      bootstrap.Assignment.BaseSHA,
		"dryRun":       *dryRun,
	}
	if len(errs) > 0 {
		receipt["result"] = actioncontext.StatusInvalidAdmission
		receipt["errors"] = errs
		if err := writeLaunchReceipt(os.Stdout, receipt); err != nil {
			return fmt.Errorf("write launch receipt: %w", err)
		}
		return fmt.Errorf("admission failed: %s", strings.Join(errs, ", "))
	}
	receipt["result"] = actioncontext.StatusAdmitted
	if *dryRun {
		return writeLaunchReceipt(os.Stdout, receipt)
	}

	// Execute directly with argv; no shell is involved.
	proc := exec.CommandContext(ctx, cmdArgs[0], cmdArgs[1:]...)
	proc.Dir = bootstrap.WorkspaceBinding.ProjectRoot
	proc.Stdin = os.Stdin
	proc.Stdout = os.Stdout
	proc.Stderr = os.Stderr
	return proc.Run()
}

func splitCommand(args []string) ([]string, []string) {
	for i, arg := range args {
		if arg == "--" {
			return args[:i], args[i+1:]
		}
	}
	return args, nil
}

func validateLaunchBootstrap(bootstrap bootstrapFile) []string {
	var errs []string
	if bootstrap.Kind != "AgentBootstrap" {
		errs = append(errs, "BOOTSTRAP_KIND_INVALID")
	}
	if bootstrap.Admission.Result != string(actioncontext.StatusAdmitted) {
		errs = append(errs, "ACTION_CONTEXT_NOT_ADMITTED")
	}
	if bootstrap.Assignment.AssignmentID == "" || bootstrap.Assignment.BaseSHA == "" {
		errs = append(errs, "ASSIGNMENT_INVALID")
	}
	projectRoot := bootstrap.WorkspaceBinding.ProjectRoot
	if !isInside(bootstrap.WorkspaceBinding.RepositoryRoot, projectRoot) ||
		!isInside(bootstrap.WorkspaceBinding.AssignedWorktree, projectRoot) {
		errs = append(errs, "WORKSPACE_BINDING_INVALID")
	}
	return errs
}

func validateExecutable(path string) []string {
	if !filepath.IsAbs(path) {
		return []string{"EXECUTABLE_NOT_ABSOLUTE"}
	}
	resolved, err := exec.LookPath(path)
	if err != nil {
		return []string{"EXECUTABLE_NOT_FOUND"}
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return []string{"EXECUTABLE_NOT_FOUND"}
	}
	return nil
}

func writeLaunchReceipt(w io.Writer, receipt map[string]any) error {
	return governanceio.Encode(w, receipt)
}

func isInside(child, parent string) bool {
	if !filepath.IsAbs(child) || !filepath.IsAbs(parent) {
		return false
	}
	c, err := filepath.EvalSymlinks(child)
	if err != nil {
		return false
	}
	p, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return false
	}
	cInfo, err := os.Stat(c)
	if err != nil || !cInfo.IsDir() {
		return false
	}
	pInfo, err := os.Stat(p)
	if err != nil || !pInfo.IsDir() {
		return false
	}
	rel, err := filepath.Rel(p, c)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}
