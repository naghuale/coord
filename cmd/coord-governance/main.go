package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"coord/internal/actioncontext"
)

func main() {
	resolveCmd := flag.NewFlagSet("resolve", flag.ExitOnError)
	projectRoot := resolveCmd.String("project-root", "", "absolute path to project root")
	role := resolveCmd.String("role", "", "agent role (COORDINATOR, EXECUTOR, REVIEWER, AUDITOR)")
	event := resolveCmd.String("event", "", "process event name from ACTION-MAP.yaml")
	assignmentID := resolveCmd.String("assignment-id", "", "assignment identifier")
	baseSHA := resolveCmd.String("base-sha", "", "base git commit SHA")
	output := resolveCmd.String("output", "", "relative path inside project root to write JSON result (atomic)")

	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: coord-governance <command> [flags]")
		fmt.Fprintln(os.Stderr, "commands: resolve, agent launch, run-scenarios, baseline, check-document-registry, check-action-map, check-defect-patterns, recovery governance-check")
		os.Exit(2)
	}

	switch os.Args[1] {
	case "resolve":
		if err := resolveCmd.Parse(os.Args[2:]); err != nil {
			os.Exit(2)
		}
		if *projectRoot == "" || *role == "" || *event == "" || *assignmentID == "" || *baseSHA == "" {
			fmt.Fprintln(os.Stderr, "resolve requires -project-root, -role, -event, -assignment-id, -base-sha")
			os.Exit(2)
		}
		result, err := (&actioncontext.FileResolver{ProjectRoot: *projectRoot}).Resolve(context.Background(), actioncontext.ResolveRequest{
			ProjectRoot:  *projectRoot,
			Role:         actioncontext.Role(*role),
			Event:        actioncontext.Event(*event),
			AssignmentID: *assignmentID,
			BaseSHA:      *baseSHA,
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, "resolve:", err)
			os.Exit(1)
		}
		if err := writeResult(*projectRoot, *output, result); err != nil {
			fmt.Fprintln(os.Stderr, "write result:", err)
			os.Exit(1)
		}
		if result.Admission.Status != actioncontext.StatusAdmitted {
			os.Exit(1)
		}
	case "agent":
		if len(os.Args) < 3 || os.Args[2] != "launch" {
			fmt.Fprintln(os.Stderr, "usage: coord-governance agent launch [flags] [-- executable args...]")
			os.Exit(2)
		}
		if err := runAgentLaunch(context.Background(), os.Args[3:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "run-scenarios":
		if err := runRunScenarios(context.Background(), os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "baseline":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: coord-governance baseline <store-run|compare|promote> [flags]")
			os.Exit(2)
		}
		var err error
		switch os.Args[2] {
		case "store-run":
			err = runBaselineStoreRun(context.Background(), os.Args[3:])
		case "compare":
			err = runBaselineCompare(context.Background(), os.Args[3:])
		case "promote":
			err = runBaselinePromote(context.Background(), os.Args[3:])
		default:
			fmt.Fprintf(os.Stderr, "unknown baseline subcommand: %s\n", os.Args[2])
			os.Exit(2)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "check-document-registry":
		if err := runCheckDocumentRegistry(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "check-action-map":
		if err := runCheckActionMap(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "check-defect-patterns":
		if err := runCheckDefectPatterns(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "recovery":
		if len(os.Args) < 3 || os.Args[2] != "governance-check" {
			fmt.Fprintln(os.Stderr, "usage: coord-governance recovery governance-check [flags]")
			os.Exit(2)
		}
		if err := runRecoveryGovernanceCheck(os.Args[3:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		os.Exit(2)
	}
}

func writeResult(projectRoot, output string, result actioncontext.ActionContext) error {
	if output == "" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(result)
	}
	if filepath.IsAbs(output) {
		return fmt.Errorf("output path must be relative to project root")
	}

	root, err := filepath.Abs(projectRoot)
	if err != nil {
		return err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("resolve project root: %w", err)
	}
	root = filepath.Clean(root)
	target := filepath.Clean(filepath.Join(root, output))
	if !pathWithinRoot(target, root) {
		return fmt.Errorf("output path is outside project root")
	}

	tmpDir := filepath.Join(root, ".scratch", "tmp")
	tmpDir, err = ensureContainedDirectory(root, tmpDir)
	if err != nil {
		return fmt.Errorf("prepare project-local temp directory: %w", err)
	}
	parent, err := ensureContainedDirectory(root, filepath.Dir(target))
	if err != nil {
		return fmt.Errorf("prepare output directory: %w", err)
	}
	target = filepath.Join(parent, filepath.Base(target))

	f, err := os.CreateTemp(tmpDir, "ac-*.json")
	if err != nil {
		return err
	}
	tmpPath := f.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(result); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, target); err != nil {
		return err
	}
	return nil
}

func pathWithinRoot(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func ensureContainedDirectory(root, path string) (string, error) {
	path = filepath.Clean(path)
	if !pathWithinRoot(path, root) {
		return "", fmt.Errorf("directory path is outside project root")
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return "", err
	}
	current := root
	if rel == "." {
		return current, nil
	}
	for _, component := range strings.Split(rel, string(filepath.Separator)) {
		if component == "" || component == "." {
			continue
		}
		next := filepath.Join(current, component)
		info, err := os.Lstat(next)
		if os.IsNotExist(err) {
			if err := os.Mkdir(next, 0700); err != nil && !os.IsExist(err) {
				return "", err
			}
			info, err = os.Lstat(next)
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			next, err = filepath.EvalSymlinks(next)
			if err != nil {
				return "", err
			}
			if !pathWithinRoot(next, root) {
				return "", fmt.Errorf("directory path resolves outside project root")
			}
			info, err = os.Stat(next)
			if err != nil {
				return "", err
			}
		}
		if !info.IsDir() {
			return "", fmt.Errorf("path component %q is not a directory", component)
		}
		current = next
	}
	return current, nil
}
