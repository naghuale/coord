package orchestration

import (
	"context"
	"strings"
	"testing"
	"time"

	"coord/internal/externalaction"
)

func TestFakeGitHubRequiresIndependentFixtureAndRejectsWrites(t *testing.T) {
	if _, err := NewFakeGitHubAdapter([]GitHubFixture{{Operation: externalaction.ObserveIssue}}); err == nil {
		t.Fatal("invalid fixture accepted")
	}
	fake, err := NewFakeGitHubAdapter(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fake.Execute(context.Background(), externalaction.Action{}); err == nil {
		t.Fatal("fake adapter enabled a write")
	}
}

func TestLegacyFixtureIsNotMarkedMatched(t *testing.T) {
	target := externalaction.GitHubTarget{Repository: "naghuale/coord", PullRequestNumber: 64, BaseBranch: "main", HeadBranch: "feature/issue63", ExpectedHead: strings.Repeat("a", 40), ExpectedState: "OPEN"}
	a, err := externalaction.NewGitHub("legacy-64", externalaction.GitHubActionPayload{Operation: externalaction.ObservePullRequest, ProjectID: "coord", OriginAssignmentID: "assignment", ScopeDigest: strings.Repeat("b", 64), Target: target}, testNow())
	if err != nil {
		t.Fatal(err)
	}
	fake, err := NewFakeGitHubAdapter([]GitHubFixture{{Operation: externalaction.ObservePullRequest, Target: target, ExternalID: "64", State: "OPEN"}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := fake.Observe(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	if result.GitHub.MarkerMatched || result.GitHub.PayloadDigestMatched || result.GitHub.ResultStatus != "NOT_EVALUATED" || result.Evidence != "LEGACY_EXTERNAL_PROVENANCE_INCOMPLETE" {
		t.Fatalf("legacy fixture got positive provenance: %+v", result)
	}
	a.Status = externalaction.Observed
	a.Result = &result
	if err := a.Validate(); err != nil {
		t.Fatalf("typed legacy observation rejected: %v", err)
	}
	a.Status = externalaction.Completed
	if err := a.Validate(); err == nil {
		t.Fatal("legacy observation became completed")
	}
}

func TestFakePipelineUnknownStateRemainsNotEvaluated(t *testing.T) {
	selector := externalaction.PipelineSelector{Repository: "naghuale/coord", Provider: externalaction.ProviderGitHubActions, CommitSHA: strings.Repeat("c", 40), Workflow: "ci.yml"}
	target := externalaction.GitHubTarget{Repository: selector.Repository, ExpectedState: "ANY"}
	a, err := externalaction.NewGitHub("pipeline-1", externalaction.GitHubActionPayload{Operation: externalaction.ObservePipeline, ProjectID: "coord", OriginAssignmentID: "a", ScopeDigest: strings.Repeat("d", 64), Target: target, Pipeline: &selector}, testNow())
	if err != nil {
		t.Fatal(err)
	}
	p, _ := a.GitHubPayload()
	fixture := GitHubFixture{Operation: externalaction.ObservePipeline, Target: target, ExternalID: "run-1", Marker: &p.Marker, PayloadDigest: p.Marker.PayloadDigest, Pipeline: &externalaction.PipelineObservation{Selector: selector, State: "FUTURE_PROVIDER_STATE"}}
	fake, err := NewFakeGitHubAdapter([]GitHubFixture{fixture})
	if err != nil {
		t.Fatal(err)
	}
	result, err := fake.Observe(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	if result.GitHub.Pipeline.State != externalaction.PipelineUnknown || result.GitHub.Pipeline.Evaluation != externalaction.PipelineNotEvaluated {
		t.Fatalf("unknown pipeline promoted: %+v", result.GitHub.Pipeline)
	}
	if !result.GitHub.MarkerMatched || result.GitHub.Marker != p.Marker {
		t.Fatal("fixture marker binding not evaluated independently")
	}
}

func testNow() time.Time { return time.Unix(1760000000, 0).UTC() }
