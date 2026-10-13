package actioncontext

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

const testRegistry = `kind: DocumentRegistry
documents:
  - id: agents
    path: AGENTS.md
    class: process
    status: active
    readWhen: [SESSION_STARTED]
    currentOperationalAuthority: true
  - id: current
    path: operations/project/CURRENT.yaml
    class: active_context
    status: active
    readWhen: [SESSION_STARTED]
    currentOperationalAuthority: true
  - id: history-doc
    path: docs/HISTORY.md
    class: reference
    status: historical
    readWhen: [HISTORICAL_INVESTIGATION, SESSION_STARTED]
    currentOperationalAuthority: false
`

const testActionMap = `kind: ActionMap
events:
  SESSION_STARTED:
    requiredDocuments: [agents, current]
    allowedActions: [verify_head]
    forbiddenActions: []
    expectedResults: [CONTINUE]
`

const testBudgets = `kind: AgentContextBudgets
budgets:
  COORDINATOR: {maxDocuments: 7, maxHistoricalDocuments: 0, maxInputTokens: 24000}
  EXECUTOR: {maxDocuments: 6, maxHistoricalDocuments: 0, maxInputTokens: 20000}
  REVIEWER: {maxDocuments: 8, maxHistoricalDocuments: 0, maxInputTokens: 28000}
  AUDITOR: {maxDocuments: 10, maxHistoricalDocuments: 1, maxInputTokens: 32000}
`

func writeFixture(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func makeRoot(t *testing.T) (string, ResolveRequest) {
	t.Helper()
	root := t.TempDir()
	writeFixture(t, root, "docs/DOCUMENT-REGISTRY.yaml", testRegistry)
	writeFixture(t, root, "docs/operations/ACTION-MAP.yaml", testActionMap)
	writeFixture(t, root, "config/CONTEXT-BUDGETS.yaml", testBudgets)
	writeFixture(t, root, "AGENTS.md", "# agents")
	writeFixture(t, root, "operations/project/CURRENT.yaml", "current: true")
	return root, ResolveRequest{
		ProjectRoot:  root,
		Role:         RoleCoordinator,
		Event:        "SESSION_STARTED",
		AssignmentID: "a1",
		BaseSHA:      "273c2c2b5e4f1a9d0c3b8e7f6a5d4c3b2a1e0f9e",
	}
}

func TestResolveAdmitted(t *testing.T) {
	_, req := makeRoot(t)
	result, err := Resolve(req)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if result.Admission.Status != StatusAdmitted {
		t.Fatalf("expected ADMITTED, got %s: %+v", result.Admission.Status, result.Admission.Rejections)
	}
	if len(result.Admission.Documents) != 2 {
		t.Fatalf("expected 2 documents, got %d", len(result.Admission.Documents))
	}
}

func TestResolveUnknownEventFailsClosed(t *testing.T) {
	_, req := makeRoot(t)
	req.Event = "NONEXISTENT_EVENT"
	result, err := Resolve(req)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if result.Admission.Status != StatusInvalidAdmission {
		t.Fatalf("expected INVALID_ADMISSION for unknown event")
	}
	if len(result.Admission.Rejections) == 0 {
		t.Fatal("expected at least one rejection")
	}
	if result.Admission.Rejections[0].Reason != ErrUnknownEvent {
		t.Fatalf("expected %s, got %s", ErrUnknownEvent, result.Admission.Rejections[0].Reason)
	}
}

func TestResolveHistoricalRejected(t *testing.T) {
	_, req := makeRoot(t)
	req.AdditionalDocumentIDs = []string{"history-doc"}
	result, err := Resolve(req)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if result.Admission.Status != StatusInvalidAdmission {
		t.Fatalf("expected INVALID_ADMISSION for historical doc outside HISTORICAL_INVESTIGATION")
	}
	found := false
	for _, r := range result.Admission.Rejections {
		if r.Reason == ErrHistoricalNotCurrent {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected %s rejection, got %+v", ErrHistoricalNotCurrent, result.Admission.Rejections)
	}
}

func TestResolveRequiredDocumentMissing(t *testing.T) {
	root, req := makeRoot(t)
	// Remove one required doc so it triggers missing
	_ = os.Remove(filepath.Join(root, "AGENTS.md"))
	result, err := Resolve(req)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if result.Admission.Status != StatusInvalidAdmission {
		t.Fatalf("expected INVALID_ADMISSION when required doc is missing")
	}
}

func TestResolveProposedRejected(t *testing.T) {
	root, req := makeRoot(t)
	writeFixture(t, root, "docs/DOCUMENT-REGISTRY.yaml", testRegistry+`  - id: proposed-doc
    path: docs/PROPOSED.md
    class: design
    status: proposed
    readWhen: [SESSION_STARTED]
    currentOperationalAuthority: false
`)
	writeFixture(t, root, "docs/PROPOSED.md", "# proposed")
	req.AdditionalDocumentIDs = []string{"proposed-doc"}
	req.AllowProposedDirection = false

	result, err := Resolve(req)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if result.Admission.Status != StatusInvalidAdmission {
		t.Fatalf("expected INVALID_ADMISSION for proposed document, got %s", result.Admission.Status)
	}
	for _, rejection := range result.Admission.Rejections {
		if rejection.Reason == ErrProposedNotAccepted {
			return
		}
	}
	t.Fatalf("expected %s rejection, got %+v", ErrProposedNotAccepted, result.Admission.Rejections)
}

// TestDigestDriftDetected verifies parity with Python test_digest_drift_detected:
// after resolving context, modifying a tracked file causes SHA256File to return
// a different digest, which the caller must detect as drift (ErrDigestDrift exists
// as a named error code and SHA256File reflects the changed content).
func TestDigestDriftDetected(t *testing.T) {
	root, req := makeRoot(t)
	result, err := Resolve(req)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if result.Admission.Status != StatusAdmitted {
		t.Fatalf("pre-condition: expected ADMITTED, got %s", result.Admission.Status)
	}
	// Find the document binding for AGENTS.md.
	var agentsBinding *DocumentBinding
	for i := range result.Admission.Documents {
		if result.Admission.Documents[i].Path == "AGENTS.md" {
			agentsBinding = &result.Admission.Documents[i]
			break
		}
	}
	if agentsBinding == nil {
		t.Fatal("AGENTS.md not found in resolved documents")
	}
	capturedDigest := agentsBinding.Digest

	// Simulate the file being modified after context was resolved.
	agentsPath := filepath.Join(root, "AGENTS.md")
	if err := os.WriteFile(agentsPath, []byte("# agents — modified"), 0644); err != nil {
		t.Fatal(err)
	}

	// SHA256File must now return a different digest.
	newDigest, err := SHA256File(agentsPath)
	if err != nil {
		t.Fatalf("SHA256File: %v", err)
	}
	if newDigest == capturedDigest {
		t.Errorf("digest unchanged after file modification — SHA256File does not detect content changes")
	}
	// Confirm ErrDigestDrift error code is defined for callers to use.
	if ErrDigestDrift == "" {
		t.Error("ErrDigestDrift constant must be non-empty")
	}
}

func TestFileResolverImplementsInterface(t *testing.T) {
	root, req := makeRoot(t)
	var _ Resolver = &FileResolver{ProjectRoot: root}
	r := &FileResolver{ProjectRoot: root}
	ctx := context.Background()
	ac, err := r.Resolve(ctx, ResolveRequest{
		Role:         req.Role,
		Event:        req.Event,
		AssignmentID: req.AssignmentID,
		BaseSHA:      req.BaseSHA,
	})
	if err != nil {
		t.Fatalf("FileResolver.Resolve: %v", err)
	}
	if ac.Admission.Status != StatusAdmitted {
		t.Fatalf("expected ADMITTED, got %s: %+v", ac.Admission.Status, ac.Admission.Rejections)
	}
	if ac.Event != req.Event {
		t.Fatalf("expected Event=%s, got %s", req.Event, ac.Event)
	}
	if ac.Role != req.Role {
		t.Fatalf("expected Role=%s, got %s", req.Role, ac.Role)
	}
	if len(ac.Transition.AllowedActions) == 0 {
		t.Fatal("expected non-empty Transition.AllowedActions")
	}
	if ac.APIVersion == "" || ac.Kind != "ActionContext" {
		t.Fatalf("expected APIVersion/Kind set, got %q/%q", ac.APIVersion, ac.Kind)
	}
}
