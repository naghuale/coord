package documentregistry

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeExternal struct {
	fetchErr error
	blobs    map[string]struct{ head, oid string }
	err      map[string]error
}

func (f *fakeExternal) Fetch() error { return f.fetchErr }

func (f *fakeExternal) Read(repository, branch, path string) (string, string, error) {
	key := repository + "|" + branch + "|" + path
	if err, ok := f.err[key]; ok {
		return "", "", err
	}
	if v, ok := f.blobs[key]; ok {
		return v.head, v.oid, nil
	}
	return "", "", errors.New("ref unavailable: missing")
}

func TestActiveLocalMissingRejectedEvenWhenOptional(t *testing.T) {
	root := t.TempDir()
	y := `apiVersion: coord.dev/v1alpha1
kind: DocumentRegistry
documents:
  - id: doc
    path: docs/missing.md
    class: process
    status: active
    optional: true
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
		t.Fatalf("expected MISSING_FILE for active optional LOCAL, got %v", vs)
	}
}

func TestProposedMissingRejected(t *testing.T) {
	root := t.TempDir()
	y := `apiVersion: coord.dev/v1alpha1
kind: DocumentRegistry
documents:
  - id: doc
    path: docs/missing.md
    class: process
    status: proposed
    optional: true
    purpose: [x]
    usedBy: [COORDINATOR]
    readWhen: [SESSION_START]
    updateWhen: [PROCESS_NORM_CHANGED]
    currentOperationalAuthority: false
    canonicalProductionAuthority: false
    normativeAuthority: none
    currentNormativeAuthority: false
    ownership: {}
    acceptance: {}
`
	vs := violations(t, y, root)
	if !hasCode(vs, "MISSING_FILE") {
		t.Fatalf("expected MISSING_FILE for proposed LOCAL, got %v", vs)
	}
}

func TestUnknownLocationFailsClosed(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs/a.md"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	y := `apiVersion: coord.dev/v1alpha1
kind: DocumentRegistry
documents:
  - id: doc
    path: docs/a.md
    class: process
    status: active
    location:
      class: GITHUB
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
	if !hasCode(vs, "UNKNOWN_LOCATION") {
		t.Fatalf("expected UNKNOWN_LOCATION, got %v", vs)
	}
}

func TestTrackedOutsideAllowlistRejected(t *testing.T) {
	root := t.TempDir()
	y := `apiVersion: coord.dev/v1alpha1
kind: DocumentRegistry
documents:
  - id: doc
    path: docs/secret.md
    class: active_context
    status: active
    optional: true
    location:
      class: TRACKED_IN_OTHER_BRANCH
      repository: naghuale/coord
      branch: ops/coord/project
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
	res, err := ValidateWithOptions(writeFixture(t, t.TempDir(), "registry.yaml", y), Options{
		Root: root, Mode: ModeLocal, External: &fakeExternal{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !hasCode(res.Violations, "PATH_OUTSIDE_ALLOWLIST") {
		t.Fatalf("expected PATH_OUTSIDE_ALLOWLIST, got %v", res.Violations)
	}
}

func TestExternalRefUnavailableCIStrictFails(t *testing.T) {
	root := t.TempDir()
	evidence := filepath.Join(t.TempDir(), "evidence.json")
	y := trackedYAML()
	res, err := ValidateWithOptions(writeFixture(t, t.TempDir(), "registry.yaml", y), Options{
		Root: root, Mode: ModeCIStrict, EvidencePath: evidence,
		External: &fakeExternal{err: map[string]error{
			"naghuale/coord|ops/coord/project|operations/project/CURRENT.yaml": errors.New("ref unavailable: missing"),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !hasCode(res.Violations, "EXTERNAL_REF_UNAVAILABLE") {
		t.Fatalf("expected EXTERNAL_REF_UNAVAILABLE, got %v", res.Violations)
	}
}

func TestExternalRefUnavailableLocalWarns(t *testing.T) {
	root := t.TempDir()
	y := trackedYAML()
	res, err := ValidateWithOptions(writeFixture(t, t.TempDir(), "registry.yaml", y), Options{
		Root: root, Mode: ModeLocal,
		External: &fakeExternal{err: map[string]error{
			"naghuale/coord|ops/coord/project|operations/project/CURRENT.yaml": errors.New("ref unavailable: missing"),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Violations) != 0 {
		t.Fatalf("LOCAL must not fail on unavailable ref, got %v", res.Violations)
	}
	if !hasCode(res.Warnings, "NOT_EVALUATED") {
		t.Fatalf("expected NOT_EVALUATED warning, got %v", res.Warnings)
	}
}

func TestTrackedExternalAcceptedWithEvidence(t *testing.T) {
	root := t.TempDir()
	evidence := filepath.Join(t.TempDir(), "evidence.json")
	y := trackedYAML()
	res, err := ValidateWithOptions(writeFixture(t, t.TempDir(), "registry.yaml", y), Options{
		Root: root, Mode: ModeCIStrict, EvidencePath: evidence, RepositoryHead: "abc",
		External: &fakeExternal{blobs: map[string]struct{ head, oid string }{
			"naghuale/coord|ops/coord/project|operations/project/CURRENT.yaml": {head: "branchhead", oid: "bloboid"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Violations) != 0 {
		t.Fatalf("expected valid tracked ref, got %v", res.Violations)
	}
	raw, err := os.ReadFile(evidence)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, part := range []string{"coord.registry-evidence/v1", "branchhead", "bloboid", "PASS"} {
		if !strings.Contains(body, part) {
			t.Fatalf("evidence artifact missing %q: %s", part, body)
		}
	}
}

func TestEvidencePathInsideRepositoryRejected(t *testing.T) {
	root := t.TempDir()
	evidence := filepath.Join(root, "inside.json")
	y := trackedYAML()
	_, err := ValidateWithOptions(writeFixture(t, t.TempDir(), "registry.yaml", y), Options{
		Root: root, Mode: ModeCIStrict, EvidencePath: evidence,
		External: &fakeExternal{blobs: map[string]struct{ head, oid string }{
			"naghuale/coord|ops/coord/project|operations/project/CURRENT.yaml": {head: "h", oid: "o"},
		}},
	})
	if err == nil {
		t.Fatal("expected evidence-inside-repo error")
	}
}

func TestSupersededRequiresSupersededBy(t *testing.T) {
	root := t.TempDir()
	y := `apiVersion: coord.dev/v1alpha1
kind: DocumentRegistry
documents:
  - id: old
    path: docs/old.md
    class: process
    status: superseded
    purpose: [x]
    usedBy: [COORDINATOR]
    readWhen: [HISTORICAL_INVESTIGATION]
    updateWhen: [PROCESS_NORM_CHANGED]
    currentOperationalAuthority: false
    canonicalProductionAuthority: false
    normativeAuthority: none
    ownership: {}
    acceptance: {}
`
	vs := violations(t, y, root)
	if !hasCode(vs, "SUPERSEDED_BY_REQUIRED") {
		t.Fatalf("expected SUPERSEDED_BY_REQUIRED, got %v", vs)
	}
}

func TestHistoricalWithoutProvenanceRejected(t *testing.T) {
	root := t.TempDir()
	y := `apiVersion: coord.dev/v1alpha1
kind: DocumentRegistry
documents:
  - id: hist
    path: docs/hist.md
    class: historical
    status: historical
    purpose: [no digest here]
    usedBy: [COORDINATOR]
    readWhen: [HISTORICAL_INVESTIGATION]
    updateWhen: [RESOLUTION_REFERENCE_ADDED]
    currentOperationalAuthority: false
    canonicalProductionAuthority: false
    normativeAuthority: none
    ownership: {}
    acceptance: {}
`
	vs := violations(t, y, root)
	if !hasCode(vs, "HISTORICAL_PROVENANCE_MISSING") {
		t.Fatalf("expected HISTORICAL_PROVENANCE_MISSING, got %v", vs)
	}
}

func TestHistoricalPurposeProvenanceAccepted(t *testing.T) {
	root := t.TempDir()
	y := `apiVersion: coord.dev/v1alpha1
kind: DocumentRegistry
documents:
  - id: hist
    path: docs/hist.md
    class: historical
    status: historical
    purpose:
      - "historical record; legacy sha256 19f1c49875c2b96cc55ed3eb039e23f7c88a0967a13921867e38f61ef3460029; actionmap-recon-grok-a17"
    usedBy: [COORDINATOR]
    readWhen: [HISTORICAL_INVESTIGATION]
    updateWhen: [RESOLUTION_REFERENCE_ADDED]
    currentOperationalAuthority: false
    canonicalProductionAuthority: false
    normativeAuthority: none
    ownership: {}
    acceptance: {}
`
	vs := violations(t, y, root)
	if hasCode(vs, "HISTORICAL_PROVENANCE_MISSING") {
		t.Fatalf("purpose provenance should satisfy historical rule, got %v", vs)
	}
}

func TestHistoricalStructuredProvenanceAccepted(t *testing.T) {
	root := t.TempDir()
	y := `apiVersion: coord.dev/v1alpha1
kind: DocumentRegistry
documents:
  - id: hist
    path: docs/hist.md
    class: historical
    status: historical
    purpose: [x]
    provenance:
      sourceDigest: sha256:19f1c49875c2b96cc55ed3eb039e23f7c88a0967a13921867e38f61ef3460029
      reconciliationRef: issue97-impl-grok-a38
    usedBy: [COORDINATOR]
    readWhen: [HISTORICAL_INVESTIGATION]
    updateWhen: [RESOLUTION_REFERENCE_ADDED]
    currentOperationalAuthority: false
    canonicalProductionAuthority: false
    normativeAuthority: none
    ownership: {}
    acceptance: {}
`
	vs := violations(t, y, root)
	if hasCode(vs, "HISTORICAL_PROVENANCE_MISSING") {
		t.Fatalf("structured provenance should pass, got %v", vs)
	}
}

func trackedYAML() string {
	return `apiVersion: coord.dev/v1alpha1
kind: DocumentRegistry
documents:
  - id: project-current
    path: operations/project/CURRENT.yaml
    class: active_context
    status: active
    optional: true
    location:
      class: TRACKED_IN_OTHER_BRANCH
      repository: naghuale/coord
      branch: ops/coord/project
    purpose: [current]
    usedBy: [COORDINATOR]
    readWhen: [SESSION_START]
    updateWhen: [ISSUE_STATE_CHANGED]
    currentOperationalAuthority: true
    canonicalProductionAuthority: false
    normativeAuthority: none
    ownership: {}
    acceptance: {}
`
}
