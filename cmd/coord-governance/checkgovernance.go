package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"coord/internal/actionmap"
	"coord/internal/architecturecheck"
	"coord/internal/defectpatterns"
	"coord/internal/documentregistry"
	"coord/internal/recovery"
)

// runCheckDocumentRegistry implements `coord-governance check-document-registry`.
// Default mode is LOCAL so developer make check does not need network.
// CI sets --mode CI_STRICT and an evidence path outside the work tree.
func runCheckDocumentRegistry(args []string) error {
	fs := flag.NewFlagSet("check-document-registry", flag.ContinueOnError)
	registryFlag := fs.String("registry", "docs/DOCUMENT-REGISTRY.yaml", "path to DOCUMENT-REGISTRY.yaml")
	rootFlag := fs.String("root", ".", "project root for file-existence checks")
	modeFlag := fs.String("mode", string(documentregistry.ModeLocal), "LOCAL (default) or CI_STRICT")
	evidenceFlag := fs.String("evidence", "", "coord.registry-evidence/v1 path; required for CI_STRICT; must be outside the repository")
	if err := fs.Parse(args); err != nil {
		return err
	}

	res, err := documentregistry.ValidateWithOptions(*registryFlag, documentregistry.Options{
		Root:         *rootFlag,
		Mode:         documentregistry.Mode(*modeFlag),
		EvidencePath: *evidenceFlag,
	})
	if err != nil {
		return fmt.Errorf("check-document-registry: %w", err)
	}
	for _, w := range res.Warnings {
		fmt.Fprintf(os.Stderr, "- [%s] %s\n", w.Code, w.Detail)
	}
	if len(res.Violations) == 0 {
		if len(res.Warnings) > 0 {
			fmt.Println("DOCUMENT_REGISTRY_NOT_EVALUATED")
			return nil
		}
		fmt.Println("DOCUMENT_REGISTRY_VALID")
		return nil
	}
	for _, v := range res.Violations {
		fmt.Fprintf(os.Stderr, "- [%s] %s\n", v.Code, v.Detail)
	}
	fmt.Fprintln(os.Stderr, "DOCUMENT_REGISTRY_INVALID")
	return fmt.Errorf("document registry has %d violation(s)", len(res.Violations))
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

// runCheckArchitectureSync implements `coord-governance check-architecture-sync`.
// Go is the blocking authority for #97; the Python script remains as evidence only.
func runCheckArchitectureSync(args []string) error {
	fs := flag.NewFlagSet("check-architecture-sync", flag.ContinueOnError)
	rootFlag := fs.String("root", ".", "project root")
	registryFlag := fs.String("registry", "docs/architecture/COMPONENT-STATUS.yaml", "path to COMPONENT-STATUS.yaml")
	docRegistryFlag := fs.String("document-registry", "docs/DOCUMENT-REGISTRY.yaml", "path to DOCUMENT-REGISTRY.yaml")
	if err := fs.Parse(args); err != nil {
		return err
	}

	res, err := architecturecheck.Check(architecturecheck.Options{
		Root:            *rootFlag,
		RegistryPath:    *registryFlag,
		DocRegistryPath: *docRegistryFlag,
	})
	if err != nil {
		return fmt.Errorf("check-architecture-sync: %w", err)
	}
	if len(res.Violations) == 0 {
		fmt.Printf("Architecture sync check PASS: %d components\n", res.Components)
		return nil
	}
	fmt.Fprintln(os.Stderr, "Architecture sync check FAILED")
	for _, v := range res.Violations {
		fmt.Fprintf(os.Stderr, "- %s: %s\n", v.Code, v.Detail)
	}
	return fmt.Errorf("architecture sync has %d violation(s)", len(res.Violations))
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

	repoRoot := strings.TrimSpace(*repoRootFlag)
	if repoRoot == "" {
		return fmt.Errorf("flag --repo-root is required: " +
			"omitting it makes the git boundary check tautological; " +
			"pass the canonical project repository root (e.g. $(git rev-parse --show-toplevel))")
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
