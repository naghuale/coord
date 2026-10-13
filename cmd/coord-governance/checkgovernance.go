package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"coord/internal/actionmap"
	"coord/internal/defectpatterns"
	"coord/internal/documentregistry"
	"coord/internal/recovery"
)

// runCheckDocumentRegistry implements `coord-governance check-document-registry`.
func runCheckDocumentRegistry(args []string) error {
	fs := flag.NewFlagSet("check-document-registry", flag.ContinueOnError)
	registryFlag := fs.String("registry", "docs/DOCUMENT-REGISTRY.yaml", "path to DOCUMENT-REGISTRY.yaml")
	rootFlag := fs.String("root", ".", "project root for file-existence checks")
	if err := fs.Parse(args); err != nil {
		return err
	}

	vs, err := documentregistry.Validate(*registryFlag, *rootFlag)
	if err != nil {
		return fmt.Errorf("check-document-registry: %w", err)
	}
	if len(vs) == 0 {
		fmt.Println("DOCUMENT_REGISTRY_VALID")
		return nil
	}
	for _, v := range vs {
		fmt.Fprintf(os.Stderr, "- [%s] %s\n", v.Code, v.Detail)
	}
	fmt.Fprintln(os.Stderr, "DOCUMENT_REGISTRY_INVALID")
	return fmt.Errorf("document registry has %d violation(s)", len(vs))
}

// runCheckActionMap implements `coord-governance check-action-map`.
func runCheckActionMap(args []string) error {
	fs := flag.NewFlagSet("check-action-map", flag.ContinueOnError)
	actionMapFlag := fs.String("action-map", "docs/operations/ACTION-MAP.yaml", "path to ACTION-MAP.yaml")
	registryFlag := fs.String("registry", "docs/DOCUMENT-REGISTRY.yaml", "path to DOCUMENT-REGISTRY.yaml")
	if err := fs.Parse(args); err != nil {
		return err
	}

	vs, err := actionmap.Validate(*actionMapFlag, *registryFlag)
	if err != nil {
		return fmt.Errorf("check-action-map: %w", err)
	}
	if len(vs) == 0 {
		fmt.Println("ACTION_MAP_VALID")
		return nil
	}
	for _, v := range vs {
		fmt.Fprintf(os.Stderr, "- [%s] %s\n", v.Code, v.Detail)
	}
	fmt.Fprintln(os.Stderr, "ACTION_MAP_INVALID")
	return fmt.Errorf("action map has %d violation(s)", len(vs))
}

// runCheckDefectPatterns implements `coord-governance check-defect-patterns`.
func runCheckDefectPatterns(args []string) error {
	fs := flag.NewFlagSet("check-defect-patterns", flag.ContinueOnError)
	patternsFlag := fs.String("patterns", "docs/operations/DEFECT-PATTERNS.yaml", "path to DEFECT-PATTERNS.yaml")
	rootFlag := fs.String("root", ".", "project root for owningNorm file-existence checks")
	if err := fs.Parse(args); err != nil {
		return err
	}

	vs, err := defectpatterns.Validate(*patternsFlag, *rootFlag)
	if err != nil {
		return fmt.Errorf("check-defect-patterns: %w", err)
	}
	if len(vs) == 0 {
		fmt.Println("DEFECT_PATTERNS_VALID")
		return nil
	}
	for _, v := range vs {
		fmt.Fprintf(os.Stderr, "- [%s] %s\n", v.Code, v.Detail)
	}
	fmt.Fprintln(os.Stderr, "DEFECT_PATTERNS_INVALID")
	return fmt.Errorf("defect patterns has %d violation(s)", len(vs))
}

// runRecoveryGovernanceCheck implements `coord-governance recovery governance-check`.
func runRecoveryGovernanceCheck(args []string) error {
	fs := flag.NewFlagSet("governance-check", flag.ContinueOnError)
	registryFlag := fs.String("registry", "docs/DOCUMENT-REGISTRY.yaml", "path to DOCUMENT-REGISTRY.yaml")
	actionMapFlag := fs.String("action-map", "docs/operations/ACTION-MAP.yaml", "path to ACTION-MAP.yaml")
	agentsFlag := fs.String("agents", "AGENTS.md", "path to AGENTS.md")
	regulationFlag := fs.String("regulation", "docs/operations/AGENT-SESSION-RECOVERY-REGULATION.md", "regulation path as registered in the registry")
	repoRootFlag := fs.String("repo-root", "", "canonical repository root; when set, verifies git top-level matches (rejects ancestor repos)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	repoRoot := *repoRootFlag
	if repoRoot == "" {
		// Derive from git rev-parse when not explicitly provided.
		if out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output(); err == nil {
			repoRoot = strings.TrimSpace(string(out))
		}
	}

	opts := recovery.CheckOptions{
		RegistryPath:   *registryFlag,
		ActionMapPath:  *actionMapFlag,
		AgentsPath:     *agentsFlag,
		RegulationPath: *regulationFlag,
		RepoRoot:       repoRoot,
	}
	vs, err := recovery.Check(opts)
	if err != nil {
		return fmt.Errorf("recovery governance-check: %w", err)
	}
	if len(vs) == 0 {
		fmt.Println("RECOVERY_GOVERNANCE_VALID")
		return nil
	}
	for _, v := range vs {
		fmt.Fprintf(os.Stderr, "- [%s] %s\n", v.Code, v.Detail)
	}
	fmt.Fprintln(os.Stderr, "RECOVERY_GOVERNANCE_INVALID")
	return fmt.Errorf("recovery governance has %d violation(s)", len(vs))
}
