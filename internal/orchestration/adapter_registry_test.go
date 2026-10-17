package orchestration

import (
	"context"
	"strings"
	"testing"

	"coord/internal/externalaction"
)

func TestRegistryRejectsUnknownFamilyWithoutSessionFallback(t *testing.T) {
	s, _, _, _ := fixture(t)
	defer func() { _ = s.Close() }()
	fake := fake()
	registry, err := NewAdapterRegistry(s, AdapterRegistration{Family: externalaction.AgentSession, Adapter: fake})
	if err != nil {
		t.Fatal(err)
	}
	a, _, _, _ := fixture(t)
	defer func() { _ = a.Close() }()
	state, _ := a.CanonicalSnapshot()
	var session externalaction.Action
	for _, action := range state.Orchestration.Actions {
		session = action
	}
	session.Family = "UNKNOWN"
	if _, err := registry.Execute(context.Background(), session); err == nil {
		t.Fatal("unknown family reached session fallback")
	}
}

func TestRegistryRoutesGitHubObservationOnlyToGitHubFamily(t *testing.T) {
	s, _, _, now := fixture(t)
	defer func() { _ = s.Close() }()
	selector := externalaction.GitHubTarget{Repository: "naghuale/coord", IssueNumber: 64, ExpectedState: "OPEN"}
	p := externalaction.GitHubActionPayload{Operation: externalaction.ObserveIssue, ProjectID: "coord", OriginAssignmentID: "assignment-1", ScopeDigest: strings.Repeat("a", 64), Target: selector}
	a, err := externalaction.NewGitHub("observe-64", p, *now)
	if err != nil {
		t.Fatal(err)
	}
	p, _ = a.GitHubPayload()
	gh, err := NewFakeGitHubAdapter([]GitHubFixture{{Operation: externalaction.ObserveIssue, Target: selector, ExternalID: "64", State: "OPEN", Marker: &p.Marker, PayloadDigest: p.Marker.PayloadDigest}})
	if err != nil {
		t.Fatal(err)
	}
	session := fake()
	registry, err := NewAdapterRegistry(s, AdapterRegistration{Family: externalaction.AgentSession, Adapter: session}, AdapterRegistration{Family: externalaction.GitHub, Adapter: gh})
	if err != nil {
		t.Fatal(err)
	}
	result, err := registry.Observe(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	if result.GitHub == nil || result.GitHub.ExternalID != "64" || result.GitHub.State != "OPEN" {
		t.Fatalf("unexpected routed result: %+v", result)
	}
	if len(session.calls) != 0 {
		t.Fatalf("GitHub action reached session adapter: %v", session.calls)
	}
}

func TestRegistryRejectsWriteOperation(t *testing.T) {
	s, _, _, now := fixture(t)
	defer func() { _ = s.Close() }()
	p := externalaction.GitHubActionPayload{Operation: externalaction.CreateIssue, ProjectID: "coord", OriginAssignmentID: "a", ScopeDigest: strings.Repeat("a", 64), Target: externalaction.GitHubTarget{Repository: "naghuale/coord", ExpectedState: "OPEN"}, Request: externalaction.GitHubRequest{Title: "write"}}
	a, err := externalaction.NewGitHub("write", p, *now)
	if err != nil {
		t.Fatal(err)
	}
	gh, _ := NewFakeGitHubAdapter(nil)
	registry, _ := NewAdapterRegistry(s, AdapterRegistration{Family: externalaction.GitHub, Adapter: gh})
	if _, err := registry.Execute(context.Background(), a); err == nil {
		t.Fatal("write action was routed")
	}
}
