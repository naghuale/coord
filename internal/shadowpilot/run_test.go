package shadowpilot_test

import (
	"testing"
	"time"

	"coord/internal/shadowpilot"
)

func TestShadowRunIDValidateRejectsEmpty(t *testing.T) {
	if err := shadowpilot.ShadowRunID("").Validate(); err == nil {
		t.Fatal("expected error for empty shadow run ID")
	}
}

func TestShadowRunIDValidateRejectsTooLong(t *testing.T) {
	long := shadowpilot.ShadowRunID(string(make([]byte, 129)))
	if err := long.Validate(); err == nil {
		t.Fatal("expected error for ID > 128 characters")
	}
}

func TestShadowRunIDValidateAcceptsValid(t *testing.T) {
	if err := shadowpilot.ShadowRunID("sv-run-abc123").Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func validShadowRun() shadowpilot.ShadowRun {
	return shadowpilot.ShadowRun{
		ID:           "sv-run-1",
		AssignmentID: "assign-001",
		AgentID:      "agent-executor",
		SessionID:    "session-abc",
		Generation:   1,
		SubjectSHA:   "b4334cd2438837140bc112b97cba49eb1a478f08",
		InputDigest:  "sha256:abc123",
		CreatedAt:    time.Now().UTC(),
	}
}

func TestShadowRunValidateAcceptsValid(t *testing.T) {
	r := validShadowRun()
	if err := r.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestShadowRunValidateRejectsEmptyAssignmentID(t *testing.T) {
	r := validShadowRun()
	r.AssignmentID = ""
	if err := r.Validate(); err == nil {
		t.Fatal("expected error for empty assignment_id")
	}
}

func TestShadowRunValidateRejectsEmptyAgentID(t *testing.T) {
	r := validShadowRun()
	r.AgentID = ""
	if err := r.Validate(); err == nil {
		t.Fatal("expected error for empty agent_id")
	}
}

func TestShadowRunValidateRejectsZeroGeneration(t *testing.T) {
	r := validShadowRun()
	r.Generation = 0
	if err := r.Validate(); err == nil {
		t.Fatal("expected error for generation=0")
	}
}

func TestShadowRunValidateRejectsShortSubjectSHA(t *testing.T) {
	r := validShadowRun()
	r.SubjectSHA = "short"
	if err := r.Validate(); err == nil {
		t.Fatal("expected error for short subject SHA")
	}
}

func TestAgentResultValidateAcceptsValid(t *testing.T) {
	r := shadowpilot.AgentResult{
		Kind:         shadowpilot.AgentResultKind,
		AssignmentID: "assign-001",
		AgentID:      "agent-executor",
		SessionID:    "session-abc",
		Generation:   1,
		SubjectSHA:   "b4334cd2438837140bc112b97cba49eb1a478f08",
		InputDigest:  "sha256:abc123",
		Findings:     []shadowpilot.Finding{},
		CompletedAt:  time.Now().UTC(),
	}
	if err := r.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAgentResultValidateRejectsWrongKind(t *testing.T) {
	r := shadowpilot.AgentResult{
		Kind:         "WrongKind",
		AssignmentID: "assign-001",
		AgentID:      "agent-executor",
		SessionID:    "session-abc",
		Generation:   1,
		SubjectSHA:   "b4334cd2438837140bc112b97cba49eb1a478f08",
		InputDigest:  "sha256:abc123",
		Findings:     []shadowpilot.Finding{},
		CompletedAt:  time.Now().UTC(),
	}
	if err := r.Validate(); err == nil {
		t.Fatal("expected error for wrong Kind")
	}
}

func TestAgentResultDigestIsStable(t *testing.T) {
	r := shadowpilot.AgentResult{
		Kind:         shadowpilot.AgentResultKind,
		AssignmentID: "assign-001",
		AgentID:      "agent-executor",
		SessionID:    "session-abc",
		Generation:   1,
		SubjectSHA:   "b4334cd2438837140bc112b97cba49eb1a478f08",
		InputDigest:  "sha256:abc123",
		Findings:     []shadowpilot.Finding{},
		CompletedAt:  time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC),
	}
	d1, err := shadowpilot.AgentResultDigest(r)
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	d2, err := shadowpilot.AgentResultDigest(r)
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	if d1 != d2 {
		t.Fatalf("digest not stable: %q != %q", d1, d2)
	}
}

func TestAgentResultDigestChangesOnDifferentSubject(t *testing.T) {
	base := shadowpilot.AgentResult{
		Kind:         shadowpilot.AgentResultKind,
		AssignmentID: "assign-001",
		AgentID:      "agent-executor",
		SessionID:    "session-abc",
		Generation:   1,
		SubjectSHA:   "b4334cd2438837140bc112b97cba49eb1a478f08",
		InputDigest:  "sha256:abc123",
		Findings:     []shadowpilot.Finding{},
		CompletedAt:  time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC),
	}
	other := base
	other.SubjectSHA = "aacc4ee0d713846a537b623db2ee2dba7114e9e4"
	d1, _ := shadowpilot.AgentResultDigest(base)
	d2, _ := shadowpilot.AgentResultDigest(other)
	if d1 == d2 {
		t.Fatal("digest must differ for different subject SHA")
	}
}

func TestNewBaselineResultSetsDigest(t *testing.T) {
	b, err := shadowpilot.NewBaselineResult(
		"b4334cd2438837140bc112b97cba49eb1a478f08",
		"sha256:abc123",
		[]shadowpilot.Finding{},
		time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if b.Digest == "" {
		t.Fatal("baseline digest must be set")
	}
}

func TestNewBaselineResultRejectsShortSHA(t *testing.T) {
	_, err := shadowpilot.NewBaselineResult("short", "sha256:x", nil, time.Now().UTC())
	if err == nil {
		t.Fatal("expected error for short SHA")
	}
}
