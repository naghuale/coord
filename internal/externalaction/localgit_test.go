package externalaction_test

import (
	"encoding/json"
	"testing"
	"time"

	"coord/internal/externalaction"
)

// Invariant matrix (P0-3-LOCAL-GIT-AND-RUN-RECOVERY-R1):
//   I1: unknown family rejected fail-closed
//   I2: LOCAL_GIT payload validator rejects missing required fields per operation
//   I3: protected branch (main/master) rejected for mutation operations
//   I4: system tmp prohibited in repository_root
//   I5: marker identity must match OperationInstanceID, Key, and payload digest
//   I6: Completed/Observed requires positive LocalGitObservation (not nil)
//   I7: external family result rejected in session action
//   I8: idempotency key stable across timestamps (same inputs → same key)
//   I9: absolute / traversal paths in allowed_paths rejected

var (
	localGitRoot = "/Users/test/workspace/project"
	localGitAt   = time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
)

func makeCommitPayload(branch string) externalaction.LocalGitActionPayload {
	return externalaction.LocalGitActionPayload{
		Operation:          externalaction.LocalGitCreateCommit,
		RepositoryID:       "repo-abc",
		RepositoryRoot:     localGitRoot,
		BaseSHA:            "aabbccddeeff00112233445566778899aabbccdd",
		Branch:             branch,
		ExpectedOutputTree: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
		ChangeSetDigest:    "cafebabecafebabecafebabecafebabecafebabecafebabecafebabecafebabe",
		CommitMessage:      "feat: write fixture file",
	}
}

// remarshalPayload re-encodes a mutated LocalGitActionPayload into action.Request
// and recalculates RequestDigest (but NOT the Key, so marker mismatches survive).
func remarshalPayload(t *testing.T, a *externalaction.Action, payload externalaction.LocalGitActionPayload) {
	t.Helper()
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	a.Request = b
	a.RequestDigest = externalaction.Digest(b)
}

// --- Scenario tests (I1-I9) ---

// I2: OBSERVE_REPOSITORY needs only identity fields.
func TestLocalGitObserveRepositoryMinimalFields(t *testing.T) {
	p := externalaction.LocalGitActionPayload{
		Operation:      externalaction.LocalGitObserveRepository,
		RepositoryID:   "repo-1",
		RepositoryRoot: localGitRoot,
	}
	if err := p.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// I2: CREATE_ISOLATED_BRANCH requires base_sha.
func TestLocalGitCreateBranchRequiresBaseSHA(t *testing.T) {
	p := externalaction.LocalGitActionPayload{
		Operation:      externalaction.LocalGitCreateIsolatedBranch,
		RepositoryID:   "repo-1",
		RepositoryRoot: localGitRoot,
		Branch:         "feature/p03-fixture",
	}
	if err := p.Validate(); err == nil {
		t.Fatal("expected error for missing base_sha")
	}
}

// I2: WRITE_ALLOWLISTED_FILES requires allowed_paths, trees and digest.
func TestLocalGitWriteFilesRequiredFields(t *testing.T) {
	base := externalaction.LocalGitActionPayload{
		Operation:          externalaction.LocalGitWriteAllowlistedFiles,
		RepositoryID:       "repo-1",
		RepositoryRoot:     localGitRoot,
		Branch:             "feature/p03-fixture",
		AllowedPaths:       []string{"testdata/p0-3/write-fixture.yaml"},
		ExpectedInputTree:  "1111111111111111111111111111111111111111111111111111111111111111",
		ExpectedOutputTree: "2222222222222222222222222222222222222222222222222222222222222222",
		ChangeSetDigest:    "3333333333333333333333333333333333333333333333333333333333333333",
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("base payload invalid: %v", err)
	}
	for _, tc := range []struct {
		field string
		apply func(*externalaction.LocalGitActionPayload)
	}{
		{"allowed_paths", func(p *externalaction.LocalGitActionPayload) { p.AllowedPaths = nil }},
		{"expected_input_tree", func(p *externalaction.LocalGitActionPayload) { p.ExpectedInputTree = "" }},
		{"expected_output_tree", func(p *externalaction.LocalGitActionPayload) { p.ExpectedOutputTree = "" }},
		{"change_set_digest", func(p *externalaction.LocalGitActionPayload) { p.ChangeSetDigest = "" }},
	} {
		p := base
		tc.apply(&p)
		if err := p.Validate(); err == nil {
			t.Errorf("expected error when %s is missing", tc.field)
		}
	}
}

func TestLocalGitUnknownOperationRejected(t *testing.T) {
	p := externalaction.LocalGitActionPayload{
		Operation:      "UNKNOWN_OP",
		RepositoryID:   "repo-1",
		RepositoryRoot: localGitRoot,
	}
	if err := p.Validate(); err == nil {
		t.Fatal("expected error for unknown operation")
	}
}

// I3: Protected branches rejected for mutation operations.
func TestLocalGitProtectedBranchRejected(t *testing.T) {
	for _, branch := range []string{"main", "master", "MAIN", "Main", "refs/heads/main", "refs/heads/master", "REFS/HEADS/MASTER"} {
		p := makeCommitPayload(branch)
		if err := p.Validate(); err == nil {
			t.Errorf("expected rejection for protected branch %q", branch)
		}
	}
}

// I3: Feature branches accepted.
func TestLocalGitFeatureBranchAccepted(t *testing.T) {
	if err := makeCommitPayload("feature/p03-test").Validate(); err != nil {
		t.Fatalf("unexpected error for feature branch: %v", err)
	}
}

// I4: System tmp roots rejected.
func TestLocalGitSystemTmpRejected(t *testing.T) {
	for _, root := range []string{"/tmp/repo", "/private/tmp/repo", "/var/tmp/repo", "/tmp/../tmp/repo", "/private/tmp//repo", "/workspace/../tmp/repo"} {
		p := externalaction.LocalGitActionPayload{
			Operation:      externalaction.LocalGitObserveRepository,
			RepositoryID:   "repo-1",
			RepositoryRoot: root,
		}
		if err := p.Validate(); err == nil {
			t.Errorf("expected rejection for system tmp root %q", root)
		}
	}
}

// I9: Absolute path in allowed_paths rejected.
func TestLocalGitAbsoluteAllowedPathRejected(t *testing.T) {
	p := externalaction.LocalGitActionPayload{
		Operation:          externalaction.LocalGitWriteAllowlistedFiles,
		RepositoryID:       "repo-1",
		RepositoryRoot:     localGitRoot,
		Branch:             "feature/p03-fixture",
		AllowedPaths:       []string{"/absolute/path.yaml"},
		ExpectedInputTree:  "1111111111111111111111111111111111111111111111111111111111111111",
		ExpectedOutputTree: "2222222222222222222222222222222222222222222222222222222222222222",
		ChangeSetDigest:    "3333333333333333333333333333333333333333333333333333333333333333",
	}
	if err := p.Validate(); err == nil {
		t.Fatal("expected error for absolute path in allowed_paths")
	}
}

// I9: Path traversal in allowed_paths rejected.
func TestLocalGitPathTraversalRejected(t *testing.T) {
	p := externalaction.LocalGitActionPayload{
		Operation:          externalaction.LocalGitWriteAllowlistedFiles,
		RepositoryID:       "repo-1",
		RepositoryRoot:     localGitRoot,
		Branch:             "feature/p03-fixture",
		AllowedPaths:       []string{"../outside.yaml"},
		ExpectedInputTree:  "1111111111111111111111111111111111111111111111111111111111111111",
		ExpectedOutputTree: "2222222222222222222222222222222222222222222222222222222222222222",
		ChangeSetDigest:    "3333333333333333333333333333333333333333333333333333333333333333",
	}
	if err := p.Validate(); err == nil {
		t.Fatal("expected error for path traversal in allowed_paths")
	}
}

// I5: NewLocalGit sets marker fields that survive round-trip validation.
func TestLocalGitNewActionMarkerRoundTrip(t *testing.T) {
	p := makeCommitPayload("feature/p03-test")
	a, err := externalaction.NewLocalGit("action-1", p, localGitAt)
	if err != nil {
		t.Fatalf("NewLocalGit: %v", err)
	}
	if err := a.Validate(); err != nil {
		t.Fatalf("Validate after NewLocalGit: %v", err)
	}
	payload, err := a.LocalGitPayload()
	if err != nil {
		t.Fatalf("LocalGitPayload: %v", err)
	}
	if payload.Marker.OperationInstanceID == "" || payload.Marker.IdempotencyKey == "" || payload.Marker.PayloadDigest == "" {
		t.Fatal("marker fields empty after NewLocalGit")
	}
}

// I8: Idempotency key stable across different timestamps (same logical inputs).
func TestLocalGitIdempotencyKeyStableAcrossTimestamps(t *testing.T) {
	p := makeCommitPayload("feature/p03-test")
	a1, err := externalaction.NewLocalGit("action-stable", p, localGitAt)
	if err != nil {
		t.Fatalf("first NewLocalGit: %v", err)
	}
	a2, err := externalaction.NewLocalGit("action-stable", p, localGitAt.Add(1000))
	if err != nil {
		t.Fatalf("second NewLocalGit: %v", err)
	}
	if a1.Key != a2.Key {
		t.Fatalf("idempotency key changed across timestamps: %q vs %q", a1.Key, a2.Key)
	}
}

// I6: Completed action without LocalGitObservation is invalid.
func TestLocalGitCompletedRequiresObservation(t *testing.T) {
	a, err := externalaction.NewLocalGit("action-cmp", makeCommitPayload("feature/p03-test"), localGitAt)
	if err != nil {
		t.Fatalf("NewLocalGit: %v", err)
	}
	a.Status = externalaction.Completed
	a.Result = &externalaction.Result{Outcome: "OBSERVED", Evidence: "fixture"}
	if err := a.Validate(); err == nil {
		t.Fatal("expected error for Completed without LocalGitObservation")
	}
}

// I7: AGENT_SESSION action must not carry a LocalGit result.
func TestLocalGitResultRejectedInSessionAction(t *testing.T) {
	req, _ := json.Marshal(map[string]any{"assignment": "a"})
	a := externalaction.New("s1", externalaction.Deliver, externalaction.Binding{
		ProjectID: "p", TaskID: "t", RunID: "r", AssignmentID: "a",
		SessionID: "s", SubjectSHA: "aabbccddeeff00112233445566778899aabbccdd", Generation: 1,
	}, "", req, localGitAt)
	a.Result = &externalaction.Result{LocalGit: &externalaction.LocalGitObservation{RepositoryID: "repo-1"}}
	if err := a.Validate(); err == nil {
		t.Fatal("expected error for LocalGit result in session action")
	}
}

// I1: Unknown family rejected fail-closed.
func TestLocalGitUnknownFamilyRejected(t *testing.T) {
	a, err := externalaction.NewLocalGit("action-x", makeCommitPayload("feature/p03-test"), localGitAt)
	if err != nil {
		t.Fatalf("NewLocalGit: %v", err)
	}
	a.Family = "UNKNOWN_FUTURE_FAMILY"
	if err := a.Validate(); err == nil {
		t.Fatal("expected error for unknown family")
	}
}

// --- Mutation tests ---

// M01: Tampered OperationInstanceID in marker detected.
func TestM01LocalGitMarkerOperationInstanceIDMismatch(t *testing.T) {
	a, err := externalaction.NewLocalGit("action-m01", makeCommitPayload("feature/p03-test"), localGitAt)
	if err != nil {
		t.Fatalf("NewLocalGit: %v", err)
	}
	payload, err := a.LocalGitPayload()
	if err != nil {
		t.Fatalf("LocalGitPayload: %v", err)
	}
	payload.Marker.OperationInstanceID = "tampered-id"
	remarshalPayload(t, &a, payload)
	if err := a.Validate(); err == nil {
		t.Fatal("M01: tampered marker OperationInstanceID accepted")
	}
}

// M02: Tampered IdempotencyKey in marker detected.
func TestM02LocalGitMarkerIdempotencyKeyMismatch(t *testing.T) {
	a, err := externalaction.NewLocalGit("action-m02", makeCommitPayload("feature/p03-test"), localGitAt)
	if err != nil {
		t.Fatalf("NewLocalGit: %v", err)
	}
	payload, err := a.LocalGitPayload()
	if err != nil {
		t.Fatalf("LocalGitPayload: %v", err)
	}
	payload.Marker.IdempotencyKey = "tampered-key"
	remarshalPayload(t, &a, payload)
	if err := a.Validate(); err == nil {
		t.Fatal("M02: tampered marker IdempotencyKey accepted")
	}
}

// M03: Tampered PayloadDigest in marker detected.
func TestM03LocalGitMarkerPayloadDigestMismatch(t *testing.T) {
	a, err := externalaction.NewLocalGit("action-m03", makeCommitPayload("feature/p03-test"), localGitAt)
	if err != nil {
		t.Fatalf("NewLocalGit: %v", err)
	}
	payload, err := a.LocalGitPayload()
	if err != nil {
		t.Fatalf("LocalGitPayload: %v", err)
	}
	payload.Marker.PayloadDigest = "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	remarshalPayload(t, &a, payload)
	if err := a.Validate(); err == nil {
		t.Fatal("M03: tampered marker PayloadDigest accepted")
	}
}

// M04: Completed action with nil Result.LocalGit rejected.
func TestM04LocalGitCompletedNilObservationRejected(t *testing.T) {
	a, err := externalaction.NewLocalGit("action-m04", makeCommitPayload("feature/p03-test"), localGitAt)
	if err != nil {
		t.Fatalf("NewLocalGit: %v", err)
	}
	a.Status = externalaction.Completed
	a.Result = &externalaction.Result{Outcome: "OBSERVED", Evidence: "e"}
	if err := a.Validate(); err == nil {
		t.Fatal("M04: completed LocalGit action with nil observation accepted")
	}
}

// M05: MarkerMatched=false in observation rejected for Completed.
func TestM05LocalGitObservationMarkerNotMatchedRejected(t *testing.T) {
	a, err := externalaction.NewLocalGit("action-m05", makeCommitPayload("feature/p03-test"), localGitAt)
	if err != nil {
		t.Fatalf("NewLocalGit: %v", err)
	}
	payload, _ := a.LocalGitPayload()
	a.Status = externalaction.Completed
	a.Result = &externalaction.Result{
		Outcome:  "OBSERVED",
		Evidence: "e",
		LocalGit: &externalaction.LocalGitObservation{
			Marker:               payload.Marker,
			MarkerMatched:        false,
			PayloadDigestMatched: true,
			RepositoryID:         payload.RepositoryID,
			ResultStatus:         "PASS",
			ResponseDigest:       "cafebabecafebabecafebabecafebabecafebabecafebabecafebabecafebabe",
		},
	}
	if err := a.Validate(); err == nil {
		t.Fatal("M05: MarkerMatched=false accepted for Completed")
	}
}

// M06: PayloadDigestMatched=false in observation rejected for Completed.
func TestM06LocalGitObservationPayloadDigestNotMatchedRejected(t *testing.T) {
	a, err := externalaction.NewLocalGit("action-m06", makeCommitPayload("feature/p03-test"), localGitAt)
	if err != nil {
		t.Fatalf("NewLocalGit: %v", err)
	}
	payload, _ := a.LocalGitPayload()
	a.Status = externalaction.Completed
	a.Result = &externalaction.Result{
		Outcome:  "OBSERVED",
		Evidence: "e",
		LocalGit: &externalaction.LocalGitObservation{
			Marker:               payload.Marker,
			MarkerMatched:        true,
			PayloadDigestMatched: false,
			RepositoryID:         payload.RepositoryID,
			ResultStatus:         "PASS",
			ResponseDigest:       "cafebabecafebabecafebabecafebabecafebabecafebabecafebabecafebabe",
		},
	}
	if err := a.Validate(); err == nil {
		t.Fatal("M06: PayloadDigestMatched=false accepted for Completed")
	}
}

// M07: RepositoryID mismatch in observation rejected for Completed.
func TestM07LocalGitObservationRepositoryMismatchRejected(t *testing.T) {
	a, err := externalaction.NewLocalGit("action-m07", makeCommitPayload("feature/p03-test"), localGitAt)
	if err != nil {
		t.Fatalf("NewLocalGit: %v", err)
	}
	payload, _ := a.LocalGitPayload()
	a.Status = externalaction.Completed
	a.Result = &externalaction.Result{
		Outcome:  "OBSERVED",
		Evidence: "e",
		LocalGit: &externalaction.LocalGitObservation{
			Marker:               payload.Marker,
			MarkerMatched:        true,
			PayloadDigestMatched: true,
			RepositoryID:         "different-repo",
			ResultStatus:         "PASS",
			ResponseDigest:       "cafebabecafebabecafebabecafebabecafebabecafebabecafebabecafebabe",
		},
	}
	if err := a.Validate(); err == nil {
		t.Fatal("M07: RepositoryID mismatch in observation accepted")
	}
}

// M08: Completed action without StopMethod=="" rejected if StopMethod is set.
func TestM08LocalGitResultEnvelopeRejectsStopMethod(t *testing.T) {
	a, err := externalaction.NewLocalGit("action-m08", makeCommitPayload("feature/p03-test"), localGitAt)
	if err != nil {
		t.Fatalf("NewLocalGit: %v", err)
	}
	payload, _ := a.LocalGitPayload()
	a.Status = externalaction.Completed
	a.Result = &externalaction.Result{
		Outcome:    "OBSERVED",
		Evidence:   "e",
		StopMethod: "SOME_STOP", // must be empty for LOCAL_GIT
		LocalGit: &externalaction.LocalGitObservation{
			Marker:               payload.Marker,
			MarkerMatched:        true,
			PayloadDigestMatched: true,
			RepositoryID:         payload.RepositoryID,
			ResultStatus:         "PASS",
			ResponseDigest:       "cafebabecafebabecafebabecafebabecafebabecafebabecafebabecafebabe",
		},
	}
	if err := a.Validate(); err == nil {
		t.Fatal("M08: StopMethod set in LocalGit result accepted")
	}
}
