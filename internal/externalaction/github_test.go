package externalaction

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func githubFixture(t *testing.T) Action {
	t.Helper()
	a, err := NewGitHub("operation-1", GitHubActionPayload{Operation: CreateIssue, ProjectID: "coord", OriginAssignmentID: "assignment-1", ScopeDigest: strings.Repeat("a", 64), Target: GitHubTarget{Repository: "naghuale/coord", ExpectedState: "OPEN"}, Request: GitHubRequest{Title: "Technical tracking", Body: "accepted scope"}}, time.Unix(100, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	return a
}
func completeFixture(t *testing.T) Action {
	t.Helper()
	a := githubFixture(t)
	p, err := a.GitHubPayload()
	if err != nil {
		t.Fatal(err)
	}
	a.Status = Completed
	a.Result = &Result{Outcome: "OBSERVED", Evidence: "fixture-record-1", GitHub: &GitHubObservation{Marker: p.Marker, MarkerMatched: true, PayloadDigestMatched: true, Target: p.Target, ExternalID: "1", State: "OPEN", ResponseDigest: strings.Repeat("b", 64), ResultStatus: "PASS"}}
	if err := a.Validate(); err != nil {
		t.Fatal(err)
	}
	return a
}
func rewritePayload(t *testing.T, a *Action, change func(*GitHubActionPayload)) {
	t.Helper()
	p, err := a.GitHubPayload()
	if err != nil {
		t.Fatal(err)
	}
	change(&p)
	a.Request, err = json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	a.RequestDigest = Digest(a.Request)
}
func TestUnknownFamilyRejected(t *testing.T) {
	a := New("session", Deliver, Binding{ProjectID: "coord", TaskID: "task", RunID: "run", AssignmentID: "assignment", SessionID: "session", SubjectSHA: strings.Repeat("a", 40), Generation: 1}, "", json.RawMessage(`{"assignment":"assignment"}`), time.Unix(100, 0).UTC())
	if err := a.Validate(); err != nil {
		t.Fatal(err)
	}
	if a.Key != "85b2d0cc833fc5038fc50b4a3dc1b700d8a0b1f8cd54f255d9b2232aa19a74f7" || Digest(a) != "5a961ffec6f1b06548c36b57454d59b53c53b38aec324feabab8bd4af86bb8ec" {
		t.Fatal("schema-2 golden session key/digest changed")
	}
	a.Family = "UNKNOWN"
	if err := a.Validate(); err == nil {
		t.Fatal("unknown family accepted")
	}
	a.Family = AgentSession
	a.SchemaVersion = 2
	if err := a.Validate(); err == nil {
		t.Fatal("new schema2 action accepted")
	}
}
func TestGitHubPayloadRejectedAsAgentSessionPayload(t *testing.T) {
	a := New("session", Deliver, Binding{ProjectID: "coord", TaskID: "task", RunID: "run", AssignmentID: "assignment", SessionID: "session", SubjectSHA: strings.Repeat("a", 40), Generation: 1}, "", json.RawMessage(`{"assignment":"assignment"}`), time.Unix(100, 0))
	// Mixed-family request detects a permissive decoder even if legacy required
	// assignment is present. No local copy of the production decoder is used.
	a.Request = json.RawMessage(`{"assignment":"assignment","operation":"CREATE_ISSUE"}`)
	a.RequestDigest = Digest(a.Request)
	a.Key = Key(a)
	if err := a.Validate(); err == nil {
		t.Fatal("GitHub operation accepted by session decoder")
	}
}
func TestAgentSessionPayloadRejectedAsGitHubPayload(t *testing.T) {
	a := githubFixture(t)
	a.Request = json.RawMessage(`{"assignment":"assignment"}`)
	a.RequestDigest = Digest(a.Request)
	a.Key = Key(a)
	if err := a.Validate(); err == nil {
		t.Fatal("session payload accepted by GitHub decoder")
	}
}
func TestCompletedRequiresPositiveObservation(t *testing.T) {
	a := completeFixture(t)
	a.Result.GitHub = nil
	if err := a.Validate(); err == nil {
		t.Fatal("completed without observation")
	}
	for _, field := range []string{"status", "externalId", "response", "state"} {
		a = completeFixture(t)
		switch field {
		case "status":
			a.Result.GitHub.ResultStatus = "NOT_EVALUATED"
		case "externalId":
			a.Result.GitHub.ExternalID = ""
		case "response":
			a.Result.GitHub.ResponseDigest = ""
		case "state":
			a.Result.GitHub.State = "CLOSED"
		}
		if err := a.Validate(); err == nil {
			t.Fatalf("completed with invalid %s", field)
		}
	}
}
func TestCompletedRejectsMarkerMismatch(t *testing.T) {
	a := completeFixture(t)
	a.Result.GitHub.MarkerMatched = false
	if err := a.Validate(); err == nil {
		t.Fatal("false marker match accepted")
	}
	for _, field := range []string{"instance", "key", "digest"} {
		a = githubFixture(t)
		rewritePayload(t, &a, func(p *GitHubActionPayload) {
			switch field {
			case "instance":
				p.Marker.OperationInstanceID = "other"
			case "key":
				p.Marker.IdempotencyKey = "other"
			case "digest":
				p.Marker.PayloadDigest = strings.Repeat("c", 64)
			}
		})
		if err := a.Validate(); err == nil {
			t.Fatalf("marker %s mismatch accepted", field)
		}
	}
}
func TestCompletedRejectsPayloadDigestMismatch(t *testing.T) {
	a := completeFixture(t)
	a.Result.GitHub.PayloadDigestMatched = false
	if err := a.Validate(); err == nil {
		t.Fatal("false payload match accepted")
	}
	a = completeFixture(t)
	a.Result.GitHub.Marker.PayloadDigest = strings.Repeat("c", 64)
	if err := a.Validate(); err == nil {
		t.Fatal("observed digest mismatch accepted")
	}
}
func TestCompletedRejectsAmbiguousWithoutObservation(t *testing.T) {
	before := githubFixture(t)
	before.Status = Ambiguous
	prev := Empty()
	prev.Actions[before.ID] = before
	prev.Events = append(prev.Events, Event{ActionID: before.ID, Revision: 1, Digest: Digest(before), At: before.CreatedAt})
	after := completeFixture(t)
	next := Empty()
	next.Actions[after.ID] = after
	next.Events = append(next.Events, prev.Events...)
	next.Events = append(next.Events, Event{ActionID: after.ID, Revision: 2, Digest: Digest(after), At: after.CreatedAt})
	if err := ValidateTransition(prev, next, 2); err == nil {
		t.Fatal("direct AMBIGUOUS to COMPLETED accepted")
	}
	fresh := Empty()
	fresh.Actions[after.ID] = after
	fresh.Events = next.Events[1:]
	if err := ValidateTransition(Empty(), fresh, 2); err == nil {
		t.Fatal("new action bypassed durable PENDING intent")
	}
	// A receipt alone must not replace a bound observation in a snapshot.
	next.Receipts["observation"] = Receipt{ObservationID: "observation", ActionID: after.ID, Digest: Digest(*after.Result), Revision: 2}
	if err := Validate(next, 2); err == nil {
		t.Fatal("receipt without observation accepted")
	}
	observation := Observation{ID: "observation", ActionID: after.ID, Source: "trusted-fixture", ClaimEpoch: after.Claim.Epoch, RequestDigest: after.RequestDigest, Result: *after.Result, Digest: Digest(*after.Result), At: after.CreatedAt}
	next.Observations[observation.ID] = observation
	if err := Validate(next, 2); err != nil {
		t.Fatalf("positive bound snapshot rejected: %v", err)
	}
	observation.ClaimEpoch++
	next.Observations[observation.ID] = observation
	if err := Validate(next, 2); err == nil {
		t.Fatal("completed GitHub accepted stale epoch")
	}
}
func mergeFixture() GitHubActionPayload {
	return GitHubActionPayload{Operation: MergePullRequest, ProjectID: "coord", OriginAssignmentID: "assignment", ScopeDigest: strings.Repeat("a", 64), Target: GitHubTarget{Repository: "naghuale/coord", PullRequestNumber: 1, ExpectedHead: strings.Repeat("a", 40), ExpectedState: "MERGED"}, Request: GitHubRequest{MergeMethod: "SQUASH"}, MergeGateRef: "gate:1"}
}
func TestEmptyMergeGateRefRejected(t *testing.T) {
	p := mergeFixture()
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	p.MergeGateRef = ""
	if err := p.Validate(); err == nil || !strings.Contains(err.Error(), "MERGE_GATE_MISSING") {
		t.Fatalf("empty gate: %v", err)
	}
}
func TestMissingMergeGateRejected(t *testing.T) {
	if err := ValidateMergeGate(mergeFixture(), nil); err == nil {
		t.Fatal("unavailable lookup accepted")
	}
	if err := ValidateMergeGate(mergeFixture(), func(string) (MergeGate, bool) { return MergeGate{}, false }); err == nil {
		t.Fatal("missing record accepted")
	}
}
func TestMergeGateMustMatchExpectedHead(t *testing.T) {
	p := mergeFixture()
	gate := MergeGate{Repository: p.Target.Repository, PullRequestNumber: 1, ExpectedHead: p.Target.ExpectedHead, State: "ACCEPTED"}
	lookup := func(string) (MergeGate, bool) { return gate, true }
	if err := ValidateMergeGate(p, lookup); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"head", "repository", "pr", "status", "superseded"} {
		g := gate
		switch field {
		case "head":
			g.ExpectedHead = strings.Repeat("b", 40)
		case "repository":
			g.Repository = "other/repo"
		case "pr":
			g.PullRequestNumber = 2
		case "status":
			g.State = "NOT_EVALUATED"
		case "superseded":
			g.Superseded = true
		}
		if err := ValidateMergeGate(p, func(string) (MergeGate, bool) { return g, true }); err == nil {
			t.Fatalf("invalid gate %s accepted", field)
		}
	}
}
