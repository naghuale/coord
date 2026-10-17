package orchestration

import (
	"context"
	"fmt"
	"sync"

	"coord/internal/externalaction"
)

type GitHubFixture = externalaction.GitHubFixture

type FakeGitHubAdapter struct {
	mu           sync.RWMutex
	fixtures     []GitHubFixture
	acknowledged map[string]bool
}

func NewFakeGitHubAdapter(fixtures []GitHubFixture) (*FakeGitHubAdapter, error) {
	seen := map[string]bool{}
	copyFixtures := append([]GitHubFixture(nil), fixtures...)
	for i := range copyFixtures {
		f := &copyFixtures[i]
		if err := f.Validate(); err != nil {
			return nil, fmt.Errorf("invalid fake GitHub fixture: %w", err)
		}
		key := fmt.Sprintf("%s/%s/%d", f.Target.Repository, f.Operation, max(f.Target.IssueNumber, f.Target.PullRequestNumber))
		if seen[key] {
			return nil, fmt.Errorf("duplicate fake GitHub fixture")
		}
		seen[key] = true
		if f.Marker != nil {
			marker := *f.Marker
			f.Marker = &marker
		}
		if f.Pipeline != nil {
			pipeline := *f.Pipeline
			f.Pipeline = &pipeline
		}
	}
	return &FakeGitHubAdapter{fixtures: copyFixtures, acknowledged: map[string]bool{}}, nil
}

func (a *FakeGitHubAdapter) SourceID() string { return "fixture:github:in-memory" }
func (a *FakeGitHubAdapter) Execute(ctx context.Context, action externalaction.Action) (externalaction.Result, error) {
	payload, err := action.GitHubPayload()
	if err != nil {
		return externalaction.Result{}, err
	}
	if payload.Operation != externalaction.ObserveIssue && payload.Operation != externalaction.ObservePullRequest && payload.Operation != externalaction.ObservePipeline {
		return externalaction.Result{}, fmt.Errorf("GitHub writes are disabled in fixture adapter")
	}
	return a.Observe(ctx, action)
}
func (a *FakeGitHubAdapter) Observe(_ context.Context, action externalaction.Action) (externalaction.Result, error) {
	p, err := action.GitHubPayload()
	if err != nil {
		return externalaction.Result{}, err
	}
	if p.Operation != externalaction.ObserveIssue && p.Operation != externalaction.ObservePullRequest && p.Operation != externalaction.ObservePipeline {
		return externalaction.Result{}, fmt.Errorf("GitHub operation disabled")
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	for _, f := range a.fixtures {
		if f.Operation != p.Operation || f.Target.Repository != p.Target.Repository {
			continue
		}
		if p.Operation == externalaction.ObserveIssue && f.Target.IssueNumber != p.Target.IssueNumber {
			continue
		}
		if p.Operation == externalaction.ObservePullRequest && f.Target.PullRequestNumber != p.Target.PullRequestNumber {
			continue
		}
		if p.Operation == externalaction.ObservePipeline && (f.Pipeline == nil || p.Pipeline == nil || f.Pipeline.Selector.Digest() != p.Pipeline.Digest()) {
			continue
		}
		responseDigest := externalaction.Digest(f)
		o := &externalaction.GitHubObservation{Target: f.Target, ExternalID: f.ExternalID, State: f.State, ResponseDigest: responseDigest}
		if p.Operation == externalaction.ObservePipeline {
			state, evaluation := externalaction.MapPipelineState(string(f.Pipeline.State))
			pipeline := *f.Pipeline
			pipeline.State, pipeline.Evaluation = state, evaluation
			o.Pipeline = &pipeline
			o.Target = p.Target
		}
		if f.Marker == nil {
			o.ResultStatus = "NOT_EVALUATED"
			return externalaction.Result{GitHub: o, Outcome: "OBSERVED", Evidence: "LEGACY_EXTERNAL_PROVENANCE_INCOMPLETE", EvidenceClass: string(externalaction.EvidenceFixtureNotLive)}, nil
		}
		o.Marker = *f.Marker
		o.MarkerMatched = *f.Marker == p.Marker
		o.PayloadDigestMatched = f.PayloadDigest == p.Marker.PayloadDigest
		o.ResultStatus = "PASS"
		return externalaction.Result{GitHub: o, Outcome: "OBSERVED", Evidence: a.SourceID(), EvidenceClass: string(externalaction.EvidenceFixtureNotLive)}, nil
	}
	return externalaction.Result{}, fmt.Errorf("fixture observation not found")
}
func (a *FakeGitHubAdapter) Acknowledge(_ context.Context, receipt externalaction.Receipt) error {
	if receipt.ActionID == "" || receipt.ObservationID == "" {
		return fmt.Errorf("invalid fixture receipt")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.acknowledged[receipt.ObservationID] = true
	return nil
}
