package documentregistry

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFixture writes content to a temp file and returns its path.
func writeFixture(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return p
}

// minimalValid returns a valid registry YAML with one document.
// The doc path is written to root so file-existence checks pass.
func minimalValid(t *testing.T, root string) string {
	t.Helper()
	docPath := "docs/test-doc.md"
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, docPath), []byte("# test"), 0600); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf(`apiVersion: coord.dev/v1alpha1
kind: DocumentRegistry
documents:
  - id: test-doc
    path: %s
    class: process
    status: active
    purpose: [testing]
    usedBy: [COORDINATOR]
    readWhen: [SESSION_START]
    updateWhen: [PROCESS_NORM_CHANGED]
    currentOperationalAuthority: false
    canonicalProductionAuthority: false
    normativeAuthority: none
    ownership: {}
    acceptance: {}
`, docPath)
}

func violations(t *testing.T, yaml, root string) []Violation {
	t.Helper()
	p := writeFixture(t, t.TempDir(), "registry.yaml", yaml)
	vs, err := Validate(p, root)
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	return vs
}

func parseError(t *testing.T, yaml string) error {
	t.Helper()
	p := writeFixture(t, t.TempDir(), "registry.yaml", yaml)
	_, err := Validate(p, t.TempDir())
	return err
}

func hasCode(vs []Violation, code string) bool {
	for _, v := range vs {
		if v.Code == code {
			return true
		}
	}
	return false
}

func TestValidRegistry(t *testing.T) {
	root := t.TempDir()
	vs := violations(t, minimalValid(t, root), root)
	if len(vs) != 0 {
		t.Errorf("expected no violations, got: %v", vs)
	}
}

func TestDuplicateID(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs/a.md"), []byte(""), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs/b.md"), []byte(""), 0600); err != nil {
		t.Fatal(err)
	}
	y := `apiVersion: coord.dev/v1alpha1
kind: DocumentRegistry
documents:
  - id: same-id
    path: docs/a.md
    class: process
    status: active
    purpose: [x]
    usedBy: [COORDINATOR]
    readWhen: [SESSION_START]
    updateWhen: [PROCESS_NORM_CHANGED]
    currentOperationalAuthority: false
    canonicalProductionAuthority: false
    normativeAuthority: none
    ownership: {}
    acceptance: {}
  - id: same-id
    path: docs/b.md
    class: process
    status: active
    purpose: [x]
    usedBy: [COORDINATOR]
    readWhen: [SESSION_START]
    updateWhen: [PROCESS_NORM_CHANGED]
    currentOperationalAuthority: false
    canonicalProductionAuthority: false
    normativeAuthority: none
    ownership: {}
    acceptance: {}
`
	vs := violations(t, y, root)
	if !hasCode(vs, "DUPLICATE_ID") {
		t.Errorf("expected DUPLICATE_ID, got: %v", vs)
	}
}

func TestDuplicatePath(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs/a.md"), []byte(""), 0600); err != nil {
		t.Fatal(err)
	}
	y := `apiVersion: coord.dev/v1alpha1
kind: DocumentRegistry
documents:
  - id: doc-a
    path: docs/a.md
    class: process
    status: active
    purpose: [x]
    usedBy: [COORDINATOR]
    readWhen: [SESSION_START]
    updateWhen: [PROCESS_NORM_CHANGED]
    currentOperationalAuthority: false
    canonicalProductionAuthority: false
    normativeAuthority: none
    ownership: {}
    acceptance: {}
  - id: doc-b
    path: docs/a.md
    class: process
    status: active
    purpose: [x]
    usedBy: [COORDINATOR]
    readWhen: [SESSION_START]
    updateWhen: [PROCESS_NORM_CHANGED]
    currentOperationalAuthority: false
    canonicalProductionAuthority: false
    normativeAuthority: none
    ownership: {}
    acceptance: {}
`
	vs := violations(t, y, root)
	if !hasCode(vs, "DUPLICATE_PATH") {
		t.Errorf("expected DUPLICATE_PATH, got: %v", vs)
	}
}

func TestMissingRegisteredFile(t *testing.T) {
	root := t.TempDir()
	y := `apiVersion: coord.dev/v1alpha1
kind: DocumentRegistry
documents:
  - id: ghost
    path: docs/ghost.md
    class: process
    status: active
    purpose: [x]
    usedBy: [COORDINATOR]
    readWhen: [SESSION_START]
    updateWhen: [PROCESS_NORM_CHANGED]
    currentOperationalAuthority: false
    canonicalProductionAuthority: false
    normativeAuthority: none
    ownership: {}
    acceptance: {}
`
	vs := violations(t, y, root)
	if !hasCode(vs, "MISSING_FILE") {
		t.Errorf("expected MISSING_FILE, got: %v", vs)
	}
}

func TestUnknownField(t *testing.T) {
	err := parseError(t, `apiVersion: coord.dev/v1alpha1
kind: DocumentRegistry
documents:
  - id: doc
    path: x.md
    unknownField: bad
`)
	if err == nil {
		t.Error("expected parse error for unknown field, got nil")
	}
}

func TestProposedMandatoryAuthority(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs/a.md"), []byte(""), 0600); err != nil {
		t.Fatal(err)
	}
	boolTrue := true
	_ = boolTrue
	y := `apiVersion: coord.dev/v1alpha1
kind: DocumentRegistry
documents:
  - id: proposed-norm
    path: docs/a.md
    class: process
    status: proposed
    purpose: [x]
    usedBy: [COORDINATOR]
    readWhen: [SESSION_START]
    updateWhen: [PROCESS_NORM_CHANGED]
    currentOperationalAuthority: false
    canonicalProductionAuthority: false
    normativeAuthority: process
    currentNormativeAuthority: true
    ownership: {}
    acceptance: {}
`
	vs := violations(t, y, root)
	if !hasCode(vs, "PROPOSED_MANDATORY_AUTHORITY") {
		t.Errorf("expected PROPOSED_MANDATORY_AUTHORITY, got: %v", vs)
	}
}

func TestHistoricalCurrentAuthority(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs/a.md"), []byte(""), 0600); err != nil {
		t.Fatal(err)
	}
	y := `apiVersion: coord.dev/v1alpha1
kind: DocumentRegistry
documents:
  - id: hist-doc
    path: docs/a.md
    class: historical
    status: historical
    purpose: [x]
    usedBy: [COORDINATOR]
    readWhen: [SESSION_START]
    updateWhen: [PROCESS_NORM_CHANGED]
    currentOperationalAuthority: true
    canonicalProductionAuthority: false
    normativeAuthority: none
    ownership: {}
    acceptance: {}
`
	vs := violations(t, y, root)
	if !hasCode(vs, "HISTORICAL_CURRENT_AUTHORITY") {
		t.Errorf("expected HISTORICAL_CURRENT_AUTHORITY, got: %v", vs)
	}
}

func TestLegacyCanonicalAuthority(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs/a.md"), []byte(""), 0600); err != nil {
		t.Fatal(err)
	}
	y := `apiVersion: coord.dev/v1alpha1
kind: DocumentRegistry
documents:
  - id: legacy-doc
    path: docs/a.md
    class: historical
    status: legacy
    purpose: [x]
    usedBy: [COORDINATOR]
    readWhen: [SESSION_START]
    updateWhen: [PROCESS_NORM_CHANGED]
    currentOperationalAuthority: false
    canonicalProductionAuthority: true
    normativeAuthority: none
    ownership: {}
    acceptance: {}
`
	vs := violations(t, y, root)
	if !hasCode(vs, "LEGACY_CANONICAL_AUTHORITY") {
		t.Errorf("expected LEGACY_CANONICAL_AUTHORITY, got: %v", vs)
	}
}

func TestAcceptedNormMissingDecisionRef(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs/a.md"), []byte(""), 0600); err != nil {
		t.Fatal(err)
	}
	y := `apiVersion: coord.dev/v1alpha1
kind: DocumentRegistry
documents:
  - id: process-norm
    path: docs/a.md
    class: process
    status: accepted
    purpose: [x]
    usedBy: [COORDINATOR]
    readWhen: [SESSION_START]
    updateWhen: [PROCESS_NORM_CHANGED]
    currentOperationalAuthority: false
    canonicalProductionAuthority: false
    normativeAuthority: process
    currentNormativeAuthority: true
    ownership: {}
    acceptance: {}
`
	vs := violations(t, y, root)
	if !hasCode(vs, "ACCEPTED_NORM_MISSING_DECISION_REF") {
		t.Errorf("expected ACCEPTED_NORM_MISSING_DECISION_REF, got: %v", vs)
	}
}

func TestPathEscapeParent(t *testing.T) {
	root := t.TempDir()
	y := `apiVersion: coord.dev/v1alpha1
kind: DocumentRegistry
documents:
  - id: escape
    path: ../etc/passwd
    class: process
    status: active
    purpose: [x]
    usedBy: [COORDINATOR]
    readWhen: [SESSION_START]
    updateWhen: [PROCESS_NORM_CHANGED]
    currentOperationalAuthority: false
    canonicalProductionAuthority: false
    normativeAuthority: none
    ownership: {}
    acceptance: {}
`
	vs := violations(t, y, root)
	if !hasCode(vs, "PATH_ESCAPE") {
		t.Errorf("expected PATH_ESCAPE, got: %v", vs)
	}
}

func TestSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "secret.md")
	if err := os.WriteFile(target, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.md")
	if err := os.Symlink(target, link); err != nil {
		t.Skip("symlinks not supported:", err)
	}
	y := `apiVersion: coord.dev/v1alpha1
kind: DocumentRegistry
documents:
  - id: symlinked
    path: link.md
    class: process
    status: active
    purpose: [x]
    usedBy: [COORDINATOR]
    readWhen: [SESSION_START]
    updateWhen: [PROCESS_NORM_CHANGED]
    currentOperationalAuthority: false
    canonicalProductionAuthority: false
    normativeAuthority: none
    ownership: {}
    acceptance: {}
`
	vs := violations(t, y, root)
	if !hasCode(vs, "PATH_ESCAPE") {
		t.Errorf("expected PATH_ESCAPE for symlink escape, got: %v", vs)
	}
}

// MutationTest: removing the currentOperationalAuthority=true restriction for
// historical docs must be detected.
func TestMutation_HistoricalCannotBeCurrentAuthority(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs/a.md"), []byte(""), 0600); err != nil {
		t.Fatal(err)
	}

	y := `apiVersion: coord.dev/v1alpha1
kind: DocumentRegistry
documents:
  - id: hist
    path: docs/a.md
    class: historical
    status: historical
    purpose: [x]
    usedBy: [COORDINATOR]
    readWhen: [SESSION_START]
    updateWhen: [PROCESS_NORM_CHANGED]
    currentOperationalAuthority: true
    canonicalProductionAuthority: false
    normativeAuthority: none
    ownership: {}
    acceptance: {}
`
	vs := violations(t, y, root)
	found := false
	for _, v := range vs {
		if strings.Contains(v.Detail, "historical") {
			found = true
		}
	}
	if !found {
		t.Errorf("mutation not detected: historical doc with currentOperationalAuthority=true should be rejected")
	}
}

func TestRealRegistry(t *testing.T) {
	const registryPath = "../../docs/DOCUMENT-REGISTRY.yaml"
	const root = "../.."
	if _, err := os.Stat(registryPath); err != nil {
		t.Skip("real registry not available:", err)
	}
	vs, err := Validate(registryPath, root)
	if err != nil {
		t.Fatalf("parse error on real registry: %v", err)
	}
	if len(vs) != 0 {
		for _, v := range vs {
			t.Errorf("violation %s: %s", v.Code, v.Detail)
		}
	}
}
