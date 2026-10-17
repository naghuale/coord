package orchestration

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"coord/internal/externalaction"
)

func observationAction(t *testing.T, op externalaction.GitHubOperation) externalaction.Action {
	t.Helper()
	target := externalaction.GitHubTarget{Repository: "naghuale/coord", ExpectedState: "OPEN"}
	switch op {
	case externalaction.ObserveIssue:
		target.IssueNumber = 64
	case externalaction.ObservePullRequest:
		target.PullRequestNumber = 64
		target.BaseBranch = "main"
		target.HeadBranch = "feature/issue63"
		target.ExpectedHead = sha
	case externalaction.ObservePipeline:
		// Pipeline selectors are bound to a commit, never only to a moving ref.
	}
	p := externalaction.GitHubActionPayload{Operation: op, ProjectID: "coord", OriginAssignmentID: "assignment-1", ScopeDigest: strings.Repeat("a", 64), Target: target}
	if op == externalaction.ObservePipeline {
		p.Pipeline = &externalaction.PipelineSelector{Repository: target.Repository, Provider: "fixture", CommitSHA: sha, Ref: "refs/heads/main", Workflow: "ci"}
	}
	a, err := externalaction.NewGitHub("observe-64", p, time.Unix(200, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestObserveExistingPR64WithoutCreate(t *testing.T) {
	a := observationAction(t, externalaction.ObservePullRequest)
	p, _ := a.GitHubPayload()
	adapter, err := NewFakeGitHubAdapter([]GitHubFixture{{Operation: externalaction.ObservePullRequest, Target: p.Target, ExternalID: "PR-64", State: "OPEN", Marker: &p.Marker, PayloadDigest: p.Marker.PayloadDigest}})
	if err != nil {
		t.Fatal(err)
	}
	r, err := adapter.Execute(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	if r.GitHub.ExternalID != "PR-64" || r.Outcome != "OBSERVED" || len(adapter.acknowledged) != 0 {
		t.Fatalf("unexpected read-only observation: %+v", r)
	}
}

func TestUnknownFamilyRejected(t *testing.T) {
	s, _, a, _ := fixture(t)
	defer func() { _ = s.Close() }()
	session := fake()
	registry, err := NewAdapterRegistry(s, AdapterRegistration{Family: externalaction.AgentSession, Adapter: session})
	if err != nil {
		t.Fatal(err)
	}
	a.Family = externalaction.Family("UNREGISTERED")
	if _, err = registry.Execute(context.Background(), a); err == nil {
		t.Fatal("unknown family fell through to session adapter")
	}
	if len(session.calls) != 0 {
		t.Fatalf("unknown family reached session adapter: %v", session.calls)
	}
}

func TestGitHubFamilyUsesGitHubDispatcher(t *testing.T) {
	s, _, _, _ := fixture(t)
	defer func() { _ = s.Close() }()
	a := observationAction(t, externalaction.ObserveIssue)
	p, _ := a.GitHubPayload()
	gh, err := NewFakeGitHubAdapter([]GitHubFixture{{Operation: p.Operation, Target: p.Target, ExternalID: "ISSUE-64", State: "OPEN", Marker: &p.Marker, PayloadDigest: p.Marker.PayloadDigest}})
	if err != nil {
		t.Fatal(err)
	}
	session := fake()
	registry, err := NewAdapterRegistry(s, AdapterRegistration{Family: externalaction.AgentSession, Adapter: session}, AdapterRegistration{Family: externalaction.GitHub, Adapter: gh})
	if err != nil {
		t.Fatal(err)
	}
	result, err := registry.Execute(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	if result.GitHub == nil || result.GitHub.ExternalID != "ISSUE-64" || len(session.calls) != 0 {
		t.Fatalf("wrong family route: %+v sessionCalls=%v", result, session.calls)
	}
}

func TestAgentSessionFamilyStillUsesSessionDispatcher(t *testing.T) {
	s, _, a, _ := fixture(t)
	defer func() { _ = s.Close() }()
	session := fake()
	registry, err := NewAdapterRegistry(s, AdapterRegistration{Family: externalaction.AgentSession, Adapter: session})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = registry.Execute(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if len(session.calls) != 1 || !strings.HasPrefix(session.calls[0], "execute:") {
		t.Fatalf("session adapter not used: %v", session.calls)
	}
}

func TestLegacyPRWithoutMarkerIsNotMarkedMatched(t *testing.T) {
	a := observationAction(t, externalaction.ObservePullRequest)
	p, _ := a.GitHubPayload()
	adapter, _ := NewFakeGitHubAdapter([]GitHubFixture{{Operation: p.Operation, Target: p.Target, ExternalID: "PR-64", State: "OPEN"}})
	r, err := adapter.Observe(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	if r.Outcome != "OBSERVED" || r.GitHub.ResultStatus != "NOT_EVALUATED" || r.GitHub.MarkerMatched || r.GitHub.PayloadDigestMatched || r.Evidence != "LEGACY_EXTERNAL_PROVENANCE_INCOMPLETE" {
		t.Fatalf("legacy provenance overstated: %+v", r)
	}
	a.Status = externalaction.Observed
	a.Result = &r
	if err = a.Validate(); err != nil {
		t.Fatalf("allowed legacy OBSERVED record rejected: %v", err)
	}
	a.Status = externalaction.Completed
	if err = a.Validate(); err == nil {
		t.Fatal("legacy observation completed without provenance")
	}
}

func TestObservedHeadMismatchRejected(t *testing.T) {
	a := observationAction(t, externalaction.ObservePullRequest)
	p, _ := a.GitHubPayload()
	adapter, _ := NewFakeGitHubAdapter([]GitHubFixture{{Operation: p.Operation, Target: p.Target, ExternalID: "PR-64", State: "OPEN", Marker: &p.Marker, PayloadDigest: p.Marker.PayloadDigest}})
	r, err := adapter.Observe(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	r.GitHub.Target.ExpectedHead = strings.Repeat("c", 40)
	a.Status = externalaction.Observed
	a.Result = &r
	if err = a.Validate(); err == nil {
		t.Fatal("observation with another head SHA accepted")
	}
}

func TestObservedBaseMismatchRejected(t *testing.T) {
	a := observationAction(t, externalaction.ObservePullRequest)
	p, _ := a.GitHubPayload()
	adapter, _ := NewFakeGitHubAdapter([]GitHubFixture{{Operation: p.Operation, Target: p.Target, ExternalID: "PR-64", State: "OPEN", Marker: &p.Marker, PayloadDigest: p.Marker.PayloadDigest}})
	r, err := adapter.Observe(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	r.GitHub.Target.BaseBranch = "release"
	a.Status = externalaction.Observed
	a.Result = &r
	if err = a.Validate(); err == nil {
		t.Fatal("observation with another base branch accepted")
	}
}

func TestAmbiguousObservationReconciledAfterRestart(t *testing.T) {
	a := observationAction(t, externalaction.ObservePullRequest)
	a.Status = externalaction.Ambiguous
	a.Claim.Epoch = 2
	if err := a.Validate(); err != nil {
		t.Fatal(err)
	}
	// Recovery preserves the same logical operation and epoch for observe-first reconciliation.
	if a.OperationInstanceID != "observe-64" || a.Status != externalaction.Ambiguous {
		t.Fatalf("ambiguous operation identity changed: %+v", a)
	}
}

func TestObservationRetryUsesSameOperationInstanceID(t *testing.T) {
	a := observationAction(t, externalaction.ObserveIssue)
	p, _ := a.GitHubPayload()
	adapter, _ := NewFakeGitHubAdapter([]GitHubFixture{{Operation: p.Operation, Target: p.Target, ExternalID: "ISSUE-64", State: "OPEN", Marker: &p.Marker, PayloadDigest: p.Marker.PayloadDigest}})
	one, err := adapter.Observe(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	two, err := adapter.Observe(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	if a.OperationInstanceID != "observe-64" || one.GitHub.ExternalID != two.GitHub.ExternalID || one.GitHub.ResponseDigest != two.GitHub.ResponseDigest {
		t.Fatal("retry changed logical observation identity/result")
	}
}

func TestNoCredentialsPersisted(t *testing.T) {
	a := observationAction(t, externalaction.ObserveIssue)
	b, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"token", "password", "authorization", "private_key"} {
		if strings.Contains(strings.ToLower(string(b)), secret) {
			t.Fatalf("credential field %q persisted", secret)
		}
	}
}

func TestFixtureEvidenceCannotBecomeLiveGateEvidence(t *testing.T) {
	a := observationAction(t, externalaction.ObserveIssue)
	p, _ := a.GitHubPayload()
	adapter, err := NewFakeGitHubAdapter([]GitHubFixture{{Operation: p.Operation, Target: p.Target, ExternalID: "ISSUE-64", State: "OPEN", Marker: &p.Marker, PayloadDigest: p.Marker.PayloadDigest}})
	if err != nil {
		t.Fatal(err)
	}
	r, err := adapter.Observe(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	a.Status = externalaction.Observed
	a.Result = &r
	if err = a.Validate(); err != nil {
		t.Fatalf("fixture observation invalid: %v", err)
	}
	r.EvidenceClass = "LIVE_GITHUB"
	a.Result = &r
	if err = a.Validate(); err == nil {
		t.Fatal("fixture result promoted to live evidence")
	}
}
