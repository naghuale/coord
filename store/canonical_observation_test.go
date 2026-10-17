package store

import (
	"coord/internal/externalaction"
	"coord/roles"
	"coord/task"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func githubObservationStore(t *testing.T) (*CanonicalStore, *canonicalMemory, time.Time) {
	t.Helper()
	state := resultState(roles.Reviewer)
	p := &canonicalMemory{state: state}
	s, err := NewCanonical(p)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(200, 0).UTC()
	run := state.Runs["run-review"]
	request, _ := json.Marshal(struct {
		Assignment string `json:"assignment"`
	}{"assignment-1"})
	delivery := externalaction.New("delivery-review", externalaction.Deliver, externalaction.Binding{ProjectID: "coord", TaskID: string(resultTaskID), RunID: run.RunID, AssignmentID: "assignment-1", SessionID: run.OwnerSession, SubjectSHA: run.TargetSHA, Generation: run.AgentSessionGeneration, Fence: run.Fence}, "", request, now)
	if err := s.EnqueueAction(delivery); err != nil {
		t.Fatal(err)
	}
	review := resultWithEvidence(task.ReviewResult{Verdict: task.ReviewApproved, Summary: "reviewed", Subject: &task.SubjectBinding{RepositoryID: "naghuale/coord", CommitSHA: run.TargetSHA, TreeHash: strings.Repeat("b", 40)}}, p.state)
	if _, _, err := s.RecordReviewResult("review-subject", resultTaskID, run.RunID, p.state.Tasks[resultTaskID].Revision, review, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, p, now.Add(2 * time.Second)
}

func TestCanonicalObservationIntentBindsExactCanonicalSubject(t *testing.T) {
	s, p, now := githubObservationStore(t)
	run := p.state.Runs["run-review"]
	request := externalaction.GitHubActionPayload{Operation: externalaction.ObservePullRequest, Target: externalaction.GitHubTarget{Repository: "naghuale/coord", PullRequestNumber: 64, BaseBranch: "main", HeadBranch: "feature/issue63", ExpectedHead: run.TargetSHA, ExpectedState: "OPEN"}}
	receipt, replayed, err := s.EnqueueGitHubObservation("observe-pr64", resultTaskID, run.RunID, p.state.Tasks[resultTaskID].Revision, task.OriginReviewProjection, request, now)
	if err != nil || replayed {
		t.Fatalf("receipt=%+v replayed=%v err=%v", receipt, replayed, err)
	}
	state, err := s.CanonicalSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	var action externalaction.Action
	for _, candidate := range state.Orchestration.Actions {
		if candidate.Family == externalaction.GitHub {
			action = candidate
		}
	}
	origin, ok, err := action.CanonicalOrigin()
	if err != nil || !ok || origin.TaskID != resultTaskID.String() || origin.RunID != run.RunID || origin.AssignmentID != "assignment-1" || origin.Subject.CommitSHA != run.TargetSHA || origin.EventID != receipt.EventIDs[0] || origin.CommandID != "observe-pr64" {
		t.Fatalf("canonical origin=%+v ok=%v err=%v", origin, ok, err)
	}
	if action.Status != externalaction.Pending || receipt.CommittedRevision != state.Tasks[resultTaskID].Revision {
		t.Fatalf("intent and receipt did not commit together: action=%s receipt=%+v", action.Status, receipt)
	}
	replayedReceipt, replayed, err := s.EnqueueGitHubObservation("observe-pr64", resultTaskID, run.RunID, receipt.ExpectedRevision, task.OriginReviewProjection, request, now)
	if err != nil || !replayed || replayedReceipt.MessageID != receipt.MessageID {
		t.Fatalf("replay receipt=%+v replayed=%v err=%v", replayedReceipt, replayed, err)
	}
}

func TestObservationRejectsRepositoryOrCommitMismatchWithoutCommit(t *testing.T) {
	for name, mutate := range map[string]func(*externalaction.GitHubActionPayload){
		"repository": func(p *externalaction.GitHubActionPayload) { p.Target.Repository = "other/repo" },
		"commit":     func(p *externalaction.GitHubActionPayload) { p.Target.ExpectedHead = strings.Repeat("c", 40) },
	} {
		t.Run(name, func(t *testing.T) {
			s, p, now := githubObservationStore(t)
			before, _ := s.CanonicalSnapshot()
			run := p.state.Runs["run-review"]
			request := externalaction.GitHubActionPayload{Operation: externalaction.ObservePullRequest, Target: externalaction.GitHubTarget{Repository: "naghuale/coord", PullRequestNumber: 64, ExpectedHead: run.TargetSHA, ExpectedState: "OPEN"}}
			mutate(&request)
			_, _, err := s.EnqueueGitHubObservation("bad-"+name, resultTaskID, run.RunID, p.state.Tasks[resultTaskID].Revision, task.OriginReviewProjection, request, now)
			if err == nil {
				t.Fatal("mismatched subject accepted")
			}
			after, _ := s.CanonicalSnapshot()
			if after.Revision != before.Revision || len(after.Orchestration.Actions) != len(before.Orchestration.Actions) {
				t.Fatal("rejected observation changed canonical state")
			}
		})
	}
}

func TestAmbiguousGitHubObservationRecoveryKeepsOperationIdentity(t *testing.T) {
	s, p, base := githubObservationStore(t)
	run := p.state.Runs["run-review"]
	request := externalaction.GitHubActionPayload{Operation: externalaction.ObservePullRequest, Target: externalaction.GitHubTarget{Repository: "naghuale/coord", PullRequestNumber: 64, BaseBranch: "main", HeadBranch: "feature/issue63", ExpectedHead: run.TargetSHA, ExpectedState: "OPEN"}}
	if _, _, err := s.EnqueueGitHubObservation("restart-pr64", resultTaskID, run.RunID, p.state.Tasks[resultTaskID].Revision, task.OriginReviewProjection, request, base); err != nil {
		t.Fatal(err)
	}
	state, _ := s.CanonicalSnapshot()
	var action externalaction.Action
	for _, candidate := range state.Orchestration.Actions {
		if candidate.Family == externalaction.GitHub {
			action = candidate
		}
	}
	id, operationID := action.ID, action.OperationInstanceID
	action, err := s.ClaimAction(id, "coordd", base.Add(time.Second), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.StartAction(id, "coordd", action.Claim.Epoch, base.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err = s.RecoverActions(base.Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	state, err = s.CanonicalSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	recovered := state.Orchestration.Actions[id]
	if recovered.Status != externalaction.Ambiguous || recovered.ID != id || recovered.OperationInstanceID != operationID {
		t.Fatalf("recovery changed logical operation: %+v", recovered)
	}
}
