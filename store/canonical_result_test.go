package store

import (
	"coord/envelope"
	"coord/fsm"
	"coord/roles"
	"coord/task"
	"coord/taskid"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestReviewResultIsStructuredAndTaskScoped(t *testing.T) {
	p := &canonicalMemory{state: ownerEnrolledCanonicalState(t, resultState(roles.Reviewer))}
	s, err := NewCanonical(p)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	now := time.Unix(500, 0).UTC()
	result := resultWithEvidence(task.ReviewResult{Verdict: task.ReviewChangesRequired, Summary: "needs fixes"}, p.state)
	result.Findings[0].Evidence = append(result.Findings[0].Evidence, task.Reference{Kind: task.ReferenceDomainEvent, ID: p.state.Events[0].ID})
	receipt, replayed, err := s.RecordReviewResult("review-message", resultTaskID, "run-review", 3, result, now)
	if err != nil || replayed {
		t.Fatalf("receipt=%+v replayed=%v err=%v", receipt, replayed, err)
	}
	if result.Findings[0].ID != "" || !result.Findings[0].CreatedAt.IsZero() || !result.CreatedAt.IsZero() {
		t.Fatal("Coord-generated IDs or timestamps mutated caller-owned result input")
	}
	assertResultCommit(t, p, receipt, task.ReviewResultRecorded, now)
	event := p.state.Events[len(p.state.Events)-1]
	if event.ActorID != "agent-reviewer" || event.ReviewResult == nil || event.ReviewResult.Findings[0].ID != event.ID+"/finding/1" {
		t.Fatalf("Review result actor/payload/Coord ID incorrect: %+v", event)
	}
	if event.ReviewResult.Findings[0].Evidence[0].Kind != task.ReferenceTask || event.ReviewResult.Findings[0].Evidence[1].Kind != task.ReferenceDomainEvent {
		t.Fatalf("Finding evidence lost its typed Task reference: %+v", event.ReviewResult.Findings[0].Evidence)
	}
}

func TestAuditResultIsStructuredAndTaskScoped(t *testing.T) {
	p := &canonicalMemory{state: ownerEnrolledCanonicalState(t, resultState(roles.Auditor))}
	s, err := NewCanonical(p)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	now := time.Unix(600, 0).UTC()
	result := resultWithEvidence(task.AuditResult{Verdict: task.AuditPass, Summary: "process verified"}, p.state)
	receipt, replayed, err := s.RecordAuditResult("audit-message", resultTaskID, "run-audit", 5, result, now)
	if err != nil || replayed {
		t.Fatalf("receipt=%+v replayed=%v err=%v", receipt, replayed, err)
	}
	assertResultCommit(t, p, receipt, task.AuditResultRecorded, now)
	event := p.state.Events[len(p.state.Events)-1]
	if event.ActorID != "agent-auditor" || event.AuditResult == nil {
		t.Fatalf("Audit result actor/payload incorrect: %+v", event)
	}
}

func TestReviewAndAuditVerdictSetsAreClosed(t *testing.T) {
	if err := (task.ReviewResult{Verdict: "BLOCKED", Summary: "x", Findings: []task.Finding{}, InputArtifactRefs: []string{}, OutputArtifactRefs: []string{}, CreatedAt: time.Now()}).Validate(); err == nil {
		t.Fatal("Review accepted a verdict outside the closed set")
	}
	if err := (task.AuditResult{Verdict: "APPROVED", Summary: "x", Findings: []task.Finding{}, InputArtifactRefs: []string{}, OutputArtifactRefs: []string{}, CreatedAt: time.Now()}).Validate(); err == nil {
		t.Fatal("Audit accepted a verdict outside the closed set")
	}
	if err := (task.ReviewResult{Verdict: task.ReviewApproved, Summary: "x", Findings: []task.Finding{}, InputArtifactRefs: []string{}, OutputArtifactRefs: []string{}, CreatedAt: time.Now()}).Validate(); err != nil {
		t.Fatalf("valid Review verdict rejected: %v", err)
	}
	if err := (task.AuditResult{Verdict: task.AuditChangesRequired, Summary: "x", Findings: []task.Finding{}, InputArtifactRefs: []string{}, OutputArtifactRefs: []string{}, CreatedAt: time.Now()}).Validate(); err != nil {
		t.Fatalf("valid Audit verdict rejected: %v", err)
	}
}

func TestFindingSchemaAndSeverityDoNotDriveTransitions(t *testing.T) {
	for _, severity := range []task.FindingSeverity{task.SeverityCritical, task.SeverityMajor, task.SeverityMinor} {
		p := &canonicalMemory{state: resultState(roles.Reviewer)}
		s, err := NewCanonical(p)
		if err != nil {
			t.Fatal(err)
		}
		result := resultWithEvidence(task.ReviewResult{
			Verdict: task.ReviewApproved, Summary: "reviewed",
			Findings: []task.Finding{{Severity: severity, Summary: "note", Evidence: []task.Reference{{Kind: task.ReferenceTask, ID: string(resultTaskID)}}}},
		}, p.state)
		_, _, err = s.RecordReviewResult("severity-"+string(severity), resultTaskID, "run-review", 3, result, time.Unix(700, 0).UTC())
		if err != nil {
			t.Fatalf("severity %s rejected: %v", severity, err)
		}
		if got := p.state.Tasks[resultTaskID].Status; got != task.Blocked {
			t.Fatalf("severity %s changed workflow transition to %s", severity, got)
		}
		_ = s.Close()
	}
	p := &canonicalMemory{state: resultState(roles.Reviewer)}
	s, err := NewCanonical(p)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	invalid := resultWithEvidence(task.ReviewResult{Verdict: task.ReviewApproved, Summary: "reviewed", Findings: []task.Finding{{Severity: "BLOCKER", Summary: "bad", Evidence: []task.Reference{{Kind: task.ReferenceTask, ID: string(resultTaskID)}}}}}, p.state)
	invalid.Findings[0].Severity = "BLOCKER"
	if _, _, err := s.RecordReviewResult("bad-severity", resultTaskID, "run-review", 3, invalid, time.Unix(701, 0).UTC()); err == nil {
		t.Fatal("unsupported finding severity was accepted")
	}
	callerID := resultWithEvidence(task.ReviewResult{Verdict: task.ReviewApproved, Summary: "reviewed"}, p.state)
	callerID.Findings[0].ID = "caller-chosen"
	if _, _, err := s.RecordReviewResult("caller-finding-id", resultTaskID, "run-review", 3, callerID, time.Unix(702, 0).UTC()); err == nil || !strings.Contains(err.Error(), "coord-generated") {
		t.Fatalf("caller Finding ID error=%v, want rejection", err)
	}
}

func TestReviewResultCommitsWithTransitionEvent(t *testing.T) {
	testResultAtomicCommit(t, roles.Reviewer)
}

func TestAuditResultCommitsWithTransitionEvent(t *testing.T) {
	testResultAtomicCommit(t, roles.Auditor)
}

func TestReviewVerdictWaitsForSupervisorStopProof(t *testing.T) {
	testResultWaitsForStop(t, roles.Reviewer)
}

func TestAuditVerdictWaitsForSupervisorStopProof(t *testing.T) {
	testResultWaitsForStop(t, roles.Auditor)
}

func TestReviewAuditResultStartupValidationRejectsInvalidAssociations(t *testing.T) {
	base := resultState(roles.Reviewer)
	p := &canonicalMemory{state: base}
	s, err := NewCanonical(p)
	if err != nil {
		t.Fatal(err)
	}
	result := resultWithEvidence(task.ReviewResult{Verdict: task.ReviewApproved, Summary: "reviewed"}, p.state)
	_, _, err = s.RecordReviewResult("startup-base", resultTaskID, "run-review", 3, result, time.Unix(800, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	valid := p.state
	cases := []struct {
		name   string
		mutate func(*CanonicalState)
		want   string
	}{
		{"payload event mismatch", func(state *CanonicalState) {
			state.Events[len(state.Events)-1].AuditResult = &task.AuditResult{Verdict: task.AuditPass, Summary: "x", Findings: []task.Finding{}, InputArtifactRefs: []string{}, OutputArtifactRefs: []string{}, CreatedAt: state.Events[len(state.Events)-1].CreatedAt}
		}, "only a ReviewResult payload"},
		{"reviewer done without typed result", func(state *CanonicalState) {
			event := &state.Events[len(state.Events)-1]
			event.Type = "TASK_NOTE"
			event.ReviewResult = nil
		}, "has no canonical result event"},
		{"wrong role", func(state *CanonicalState) {
			selection := *state.Runs["run-review"].RoleSelection
			selection.Profile.Role = roles.Auditor
			selection.Profile.Role = roles.Auditor
			run := state.Runs["run-review"]
			run.RoleSelection = &selection
			run.RequiredRole = roles.Requirement{Role: roles.Auditor}
			run.RoleHistory = []roles.Selection{selection}
			state.Runs["run-review"] = run
		}, "result DomainEvent"},
		{"foreign run", func(state *CanonicalState) {
			state.Events[len(state.Events)-1].RunID = "missing"
			delete(state.Outbox, "run-review")
			state.Inbox = map[string]InboxReceipt{}
		}, "missing or unrelated run"},
		{"invalid revision", func(state *CanonicalState) { state.Events[len(state.Events)-1].TaskRevision++ }, "revision"},
		{"future event reference", func(state *CanonicalState) {
			event := &state.Events[len(state.Events)-1]
			event.ReviewResult.Findings[0].Evidence = []task.Reference{{Kind: task.ReferenceDomainEvent, ID: event.ID}}
		}, "future or unrelated DomainEvent"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state, err := copyCanonicalState(valid)
			if err != nil {
				t.Fatal(err)
			}
			tc.mutate(&state)
			if err := ValidateCanonicalState(state); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("validation error=%v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestReviewResultRejectsInvalidAndOutOfScopeReferences(t *testing.T) {
	foreignState := resultState(roles.Reviewer)
	foreignTaskID := taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAW")
	foreignStage := task.Execution
	foreignNow := time.Unix(99, 0).UTC()
	foreignState.Tasks[foreignTaskID] = task.Task{ID: foreignTaskID, Title: "Other", OwnerID: "owner", Revision: 2, LastEventSeq: 2,
		StagePlan: task.StagePlan{Stages: []task.Stage{task.Execution}, PolicyVersion: task.StagePlanPolicyVersion}, CurrentStage: &foreignStage,
		Status: task.InProgress, Fence: 1, CreatedAt: foreignNow, UpdatedAt: foreignNow.Add(time.Second)}
	foreignSelection := roles.Selection{AgentID: "foreign-agent", Session: "foreign-session", Profile: roles.Profile{Role: roles.Executor}}
	foreignState.Runs["foreign-run"] = fsm.Run{Task: "other", TaskID: foreignTaskID, RunID: "foreign-run", Attempt: 1, Fence: 1,
		TargetSHA: strings.Repeat("b", 40), OwnerSession: foreignSelection.Session, CoordinatorSession: "coord-session", Worktree: "other-worktree",
		Deadline: foreignNow.Add(time.Hour), AssignedAt: foreignNow, LastUpdate: foreignNow, Status: fsm.Started,
		RequiredRole: roles.Requirement{Role: roles.Executor}, RoleSelection: &foreignSelection, RoleHistory: []roles.Selection{foreignSelection}}
	foreignRun := foreignState.Runs["foreign-run"]
	bindCanonicalTestRun(&foreignState, &foreignRun, foreignSelection.AgentID, foreignNow)
	foreignState.Runs[foreignRun.RunID] = foreignRun
	foreignState.Events = append(foreignState.Events,
		task.DomainEvent{ID: task.EventID(foreignTaskID, 1), TaskID: foreignTaskID, EventSeq: 1, TaskRevision: 1, TaskFence: 0, Type: task.TaskCreated, ActorID: "owner", CreatedAt: foreignNow},
		task.DomainEvent{ID: task.EventID(foreignTaskID, 2), TaskID: foreignTaskID, RunID: "foreign-run", EventSeq: 2, TaskRevision: 2, TaskFence: 1, Type: task.TaskOwnershipTransferred, CreatedAt: foreignNow.Add(time.Second)},
	)
	cases := []struct {
		name string
		ref  task.Reference
		want string
	}{
		{"foreign task", task.Reference{Kind: task.ReferenceTask, ID: "task_01ARZ3NDEKTSV4RRFFQ69G5FAW"}, "outside Task scope"},
		{"missing run", task.Reference{Kind: task.ReferenceRun, ID: "run-missing"}, "missing or unrelated Run"},
		{"foreign task run", task.Reference{Kind: task.ReferenceRun, ID: "foreign-run"}, "missing or unrelated Run"},
		{"future event", task.Reference{Kind: task.ReferenceDomainEvent, ID: task.EventID(resultTaskID, 4)}, "future or unrelated DomainEvent"},
		{"foreign task event", task.Reference{Kind: task.ReferenceDomainEvent, ID: task.EventID(foreignTaskID, 1)}, "future or unrelated DomainEvent"},
		{"artifact not modeled", task.Reference{Kind: task.ReferenceArtifact, ID: "artifact-1"}, "artifact metadata not defined"},
		{"unknown kind", task.Reference{Kind: "ADR", ID: "ADR-0001"}, "unsupported reference kind"},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state := resultState(roles.Reviewer)
			if tc.name == "foreign task run" || tc.name == "foreign task event" {
				state = foreignState
			}
			p := &canonicalMemory{state: state}
			s, err := NewCanonical(p)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close() }()
			result := resultWithEvidence(task.ReviewResult{Verdict: task.ReviewApproved, Summary: "reviewed"}, p.state)
			result.Findings[0].Evidence = []task.Reference{tc.ref}
			_, _, err = s.RecordReviewResult("invalid-ref-"+string(rune('a'+i)), resultTaskID, "run-review", 3, result, time.Unix(900+int64(i), 0).UTC())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v, want substring %q", err, tc.want)
			}
			if p.saveCalls != 0 || len(s.state.Events) != len(state.Events) {
				t.Fatal("invalid reference mutated canonical state")
			}
		})
	}
}

func TestResultArtifactReferenceFieldsRemainEmptyUntilMetadataExists(t *testing.T) {
	p := &canonicalMemory{state: resultState(roles.Reviewer)}
	s, err := NewCanonical(p)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	result := resultWithEvidence(task.ReviewResult{Verdict: task.ReviewApproved, Summary: "reviewed"}, p.state)
	result.InputArtifactRefs = []string{"artifact-1"}
	_, _, err = s.RecordReviewResult("artifact-field", resultTaskID, "run-review", 3, result, time.Unix(950, 0).UTC())
	if err == nil || !strings.Contains(err.Error(), "artifact metadata not defined") {
		t.Fatalf("non-empty artifact refs error=%v, want fail-closed rejection", err)
	}
}

func TestReviewResultCommitFailureDoesNotPublish(t *testing.T) {
	p := &canonicalMemory{state: resultState(roles.Reviewer), fail: true}
	s, err := NewCanonical(p)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	before := s.state
	result := resultWithEvidence(task.ReviewResult{Verdict: task.ReviewApproved, Summary: "reviewed"}, p.state)
	_, _, err = s.RecordReviewResult("commit-fail", resultTaskID, "run-review", 3, result, time.Unix(1000, 0).UTC())
	if !errors.Is(err, ErrPersistence) || len(s.state.Events) != len(before.Events) || s.state.Tasks[resultTaskID].Revision != before.Tasks[resultTaskID].Revision {
		t.Fatalf("failed commit published state: err=%v", err)
	}
}

func TestReviewAuditResultRejectsNonWorkingRoleRunsWithoutMutation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status fsm.Status
		setup  func(*fsm.Run)
	}{
		{name: "assigned", status: fsm.Pending},
		{name: "stalled recovery", status: fsm.Stalled, setup: func(run *fsm.Run) {
			run.ResumeStatus = fsm.Started
			run.RecoveryNeeded = true
			run.RecoveryClaim = "recovery-claim"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &canonicalMemory{state: resultState(roles.Reviewer)}
			s, err := NewCanonical(p)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close() }()
			run := s.state.Runs["run-review"]
			run.Status = tc.status
			if tc.setup != nil {
				tc.setup(&run)
			}
			s.state.Runs[run.RunID] = run
			before, err := copyCanonicalState(s.state)
			if err != nil {
				t.Fatal(err)
			}
			result := resultWithEvidence(task.ReviewResult{Verdict: task.ReviewApproved, Summary: "reviewed"}, s.state)
			_, _, err = s.RecordReviewResult("inactive-"+tc.name, resultTaskID, run.RunID, 3, result, time.Unix(200, 0).UTC())
			if err == nil || !strings.Contains(err.Error(), "role Run must be working") {
				t.Fatalf("inactive Run error=%v, want working-state rejection", err)
			}
			if p.saveCalls != 0 || !reflect.DeepEqual(before, s.state) {
				t.Fatal("rejected inactive Run changed canonical state")
			}
		})
	}
}

func TestReviewAndAuditRecordRejectWrongRoleRun(t *testing.T) {
	tests := []struct {
		name     string
		initial  roles.Role
		assigned roles.Role
		result   any
		runID    string
		revision uint64
		want     string
	}{
		{name: "review from auditor", initial: roles.Reviewer, assigned: roles.Auditor,
			result: task.ReviewResult{Verdict: task.ReviewApproved, Summary: "reviewed"},
			runID:  "run-review", revision: 3, want: "ReviewResult requires a REVIEW Task and Reviewer Run"},
		{name: "audit from reviewer", initial: roles.Auditor, assigned: roles.Reviewer,
			result: task.AuditResult{Verdict: task.AuditPass, Summary: "audited"},
			runID:  "run-audit", revision: 5, want: "AuditResult requires an AUDIT Task and Auditor Run"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := &canonicalMemory{state: resultState(tc.initial)}
			s, err := NewCanonical(p)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close() }()
			run := s.state.Runs[tc.runID]
			selection := *run.RoleSelection
			selection.Profile.Role = tc.assigned
			run.RoleSelection = &selection
			run.RequiredRole.Role = tc.assigned
			run.RoleHistory[len(run.RoleHistory)-1] = selection
			s.state.Runs[tc.runID] = run
			before, err := copyCanonicalState(s.state)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Unix(200, 0).UTC()
			var resultErr error
			switch result := tc.result.(type) {
			case task.ReviewResult:
				result = resultWithEvidence(result, s.state)
				_, _, resultErr = s.RecordReviewResult("wrong-role-review", resultTaskID, tc.runID, tc.revision, result, now)
			case task.AuditResult:
				result = resultWithEvidence(result, s.state)
				_, _, resultErr = s.RecordAuditResult("wrong-role-audit", resultTaskID, tc.runID, tc.revision, result, now)
			}
			if resultErr == nil || !strings.Contains(resultErr.Error(), tc.want) {
				t.Fatalf("wrong-role result error=%v, want %q", resultErr, tc.want)
			}
			if p.saveCalls != 0 || !reflect.DeepEqual(before, s.state) {
				t.Fatal("wrong-role result changed canonical state")
			}
		})
	}
}

func TestReviewResultRejectsTimestampBeforeTaskUpdate(t *testing.T) {
	p := &canonicalMemory{state: resultState(roles.Reviewer)}
	s, err := NewCanonical(p)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	before, err := copyCanonicalState(s.state)
	if err != nil {
		t.Fatal(err)
	}
	result := resultWithEvidence(task.ReviewResult{Verdict: task.ReviewApproved, Summary: "reviewed"}, s.state)
	_, _, err = s.RecordReviewResult("early-result", resultTaskID, "run-review", 3, result, before.Tasks[resultTaskID].UpdatedAt.Add(-time.Nanosecond))
	if err == nil || !strings.Contains(err.Error(), "result timestamp must not precede Task state") {
		t.Fatalf("timestamp error=%v, want pre-Task timestamp rejection", err)
	}
	if p.saveCalls != 0 || !reflect.DeepEqual(before, s.state) {
		t.Fatal("rejected early result changed canonical state")
	}
}

func TestReviewAuditResultStartupValidationRejectsInvalidTerminalProjection(t *testing.T) {
	base := committedResultState(t, roles.Reviewer)
	cases := []struct {
		name   string
		mutate func(*CanonicalState)
		want   string
	}{
		{"task not blocked", func(state *CanonicalState) {
			taskValue := state.Tasks[resultTaskID]
			taskValue.Status = task.InProgress
			state.Tasks[resultTaskID] = taskValue
		}, "latest result event"},
		{"wrong task stage", func(state *CanonicalState) {
			taskValue := state.Tasks[resultTaskID]
			stage := task.Execution
			taskValue.CurrentStage = &stage
			state.Tasks[resultTaskID] = taskValue
		}, "latest result event"},
		{"result run is nonterminal", func(state *CanonicalState) {
			run := state.Runs["run-review"]
			run.Status = fsm.Started
			state.Runs[run.RunID] = run
		}, "nonterminal Run"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state, err := copyCanonicalState(base)
			if err != nil {
				t.Fatal(err)
			}
			tc.mutate(&state)
			if err := ValidateCanonicalState(state); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("startup error=%v, want %q", err, tc.want)
			}
		})
	}
}

func TestReviewAuditResultStartupValidationRejectsEventMetadataMismatch(t *testing.T) {
	base := committedResultState(t, roles.Reviewer)
	for _, field := range []string{"summary", "created_at"} {
		t.Run(field, func(t *testing.T) {
			state, err := copyCanonicalState(base)
			if err != nil {
				t.Fatal(err)
			}
			event := &state.Events[len(state.Events)-1]
			if field == "summary" {
				event.Summary = "different"
			} else {
				event.CreatedAt = event.CreatedAt.Add(time.Second)
			}
			if err := ValidateCanonicalState(state); err == nil || !strings.Contains(err.Error(), "metadata differs") {
				t.Fatalf("startup error=%v, want payload metadata mismatch", err)
			}
		})
	}
}

func TestReviewAuditResultsRequireFindingsAndEvidence(t *testing.T) {
	for _, tc := range []struct {
		name string
		make func() error
		want string
	}{
		{"nil findings", func() error {
			return (task.ReviewResult{Verdict: task.ReviewApproved, Summary: "ok", CreatedAt: time.Now(), InputArtifactRefs: []string{}, OutputArtifactRefs: []string{}}).Validate()
		}, "findings must be present"},
		{"finding without evidence", func() error {
			return (task.AuditResult{Verdict: task.AuditPass, Summary: "ok", CreatedAt: time.Now(), Findings: []task.Finding{{ID: "finding", Severity: task.SeverityMinor, Summary: "note", CreatedAt: time.Now()}}, InputArtifactRefs: []string{}, OutputArtifactRefs: []string{}}).Validate()
		}, "requires evidence references"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.make(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("validation error=%v, want %q", err, tc.want)
			}
		})
	}
}

func TestRecordReviewResultRejectsNilArtifactReferenceLists(t *testing.T) {
	p := &canonicalMemory{state: resultState(roles.Reviewer)}
	s, err := NewCanonical(p)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	result := task.ReviewResult{Verdict: task.ReviewApproved, Summary: "reviewed", Findings: []task.Finding{}}
	_, _, err = s.RecordReviewResult("nil-artifact-lists", resultTaskID, "run-review", 3, result, time.Unix(200, 0).UTC())
	if err == nil || !strings.Contains(err.Error(), "input_artifact_refs and output_artifact_refs must be present") {
		t.Fatalf("nil artifact lists error=%v, want presence validation", err)
	}
	if p.saveCalls != 0 {
		t.Fatal("result with absent artifact lists was committed")
	}
}

func TestRecordReviewResultRejectsReviewerWhoAlsoExecuted(t *testing.T) {
	p := &canonicalMemory{state: resultState(roles.Reviewer)}
	s, err := NewCanonical(p)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	// After opening with a valid state, change the executor run's AgentID to match
	// the reviewer's in-memory state. This simulates a reviewer who also executed
	// the task, triggering the reviewer≠executor independence check in RecordReviewResult
	// without failing startup validation (which validates the committed state).
	run := s.state.Runs["run-executor"]
	sel := *run.RoleSelection
	sel.AgentID = "agent-reviewer"
	run.RoleSelection = &sel
	run.RoleHistory = []roles.Selection{sel}
	s.state.Runs[run.RunID] = run

	before, err := copyCanonicalState(s.state)
	if err != nil {
		t.Fatal(err)
	}
	result := resultWithEvidence(task.ReviewResult{Verdict: task.ReviewApproved, Summary: "reviewed"}, s.state)
	_, _, err = s.RecordReviewResult("independence-review", resultTaskID, "run-review", 3, result, time.Unix(500, 0).UTC())
	if err == nil || !strings.Contains(err.Error(), "independence violation") {
		t.Fatalf("reviewer≠executor check: error=%v, want independence violation", err)
	}
	if p.saveCalls != 0 || !reflect.DeepEqual(before, s.state) {
		t.Fatal("independence violation changed canonical state")
	}
}

func TestRecordAuditResultRejectsAuditorWhoAlsoExecuted(t *testing.T) {
	p := &canonicalMemory{state: resultState(roles.Auditor)}
	s, err := NewCanonical(p)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	// Rebind the executor run to the same AgentID as the auditor to trigger the
	// auditor≠executor independence check in RecordAuditResult.
	execRun := s.state.Runs["run-executor"]
	execSel := *execRun.RoleSelection
	execSel.AgentID = "agent-auditor"
	execRun.RoleSelection = &execSel
	execRun.RoleHistory = []roles.Selection{execSel}
	s.state.Runs[execRun.RunID] = execRun

	before, err := copyCanonicalState(s.state)
	if err != nil {
		t.Fatal(err)
	}
	result := resultWithEvidence(task.AuditResult{Verdict: task.AuditPass, Summary: "audited"}, s.state)
	_, _, err = s.RecordAuditResult("independence-audit-exec", resultTaskID, "run-audit", 5, result, time.Unix(500, 0).UTC())
	if err == nil || !strings.Contains(err.Error(), "independence violation") {
		t.Fatalf("auditor≠executor check: error=%v, want independence violation", err)
	}
	if p.saveCalls != 0 || !reflect.DeepEqual(before, s.state) {
		t.Fatal("independence violation changed canonical state")
	}
}

func TestRecordAuditResultRejectsAuditorWhoAlsoReviewed(t *testing.T) {
	p := &canonicalMemory{state: resultState(roles.Auditor)}
	s, err := NewCanonical(p)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	// Rebind the auditor run to the same AgentID as the reviewer to trigger the
	// auditor≠reviewer independence check in RecordAuditResult.
	// resultState(Auditor) includes run-review (agent-reviewer), so changing
	// run-audit's AgentID to "agent-reviewer" creates the conflict.
	auditRun := s.state.Runs["run-audit"]
	auditSel := *auditRun.RoleSelection
	auditSel.AgentID = "agent-reviewer"
	auditRun.RoleSelection = &auditSel
	auditRun.RoleHistory = []roles.Selection{auditSel}
	s.state.Runs[auditRun.RunID] = auditRun

	before, err := copyCanonicalState(s.state)
	if err != nil {
		t.Fatal(err)
	}
	result := resultWithEvidence(task.AuditResult{Verdict: task.AuditPass, Summary: "audited"}, s.state)
	_, _, err = s.RecordAuditResult("independence-audit-review", resultTaskID, "run-audit", 5, result, time.Unix(500, 0).UTC())
	if err == nil || !strings.Contains(err.Error(), "independence violation") {
		t.Fatalf("auditor≠reviewer check: error=%v, want independence violation", err)
	}
	if p.saveCalls != 0 || !reflect.DeepEqual(before, s.state) {
		t.Fatal("independence violation changed canonical state")
	}
}

const resultTaskID taskid.ID = "task_01ARZ3NDEKTSV4RRFFQ69G5FAV"

func committedResultState(t *testing.T, role roles.Role) CanonicalState {
	t.Helper()
	p := &canonicalMemory{state: resultState(role)}
	s, err := NewCanonical(p)
	if err != nil {
		t.Fatal(err)
	}
	if role == roles.Reviewer {
		result := resultWithEvidence(task.ReviewResult{Verdict: task.ReviewApproved, Summary: "reviewed"}, p.state)
		_, _, err = s.RecordReviewResult("startup-result", resultTaskID, "run-review", 3, result, time.Unix(2000, 0).UTC())
	} else {
		result := resultWithEvidence(task.AuditResult{Verdict: task.AuditPass, Summary: "audited"}, p.state)
		_, _, err = s.RecordAuditResult("startup-result", resultTaskID, "run-audit", 5, result, time.Unix(3000, 0).UTC())
	}
	if err != nil {
		_ = s.Close()
		t.Fatalf("record result fixture: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	state, err := copyCanonicalState(p.state)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func testResultAtomicCommit(t *testing.T, role roles.Role) {
	t.Helper()
	p := &canonicalMemory{state: resultState(role)}
	s, err := NewCanonical(p)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	beforeSaves := p.saveCalls
	now := time.Unix(1100, 0).UTC()
	if role == roles.Reviewer {
		result := resultWithEvidence(task.ReviewResult{Verdict: task.ReviewApproved, Summary: "reviewed"}, p.state)
		_, _, err = s.RecordReviewResult("atomic-review", resultTaskID, "run-review", 3, result, now)
	} else {
		result := resultWithEvidence(task.AuditResult{Verdict: task.AuditChangesRequired, Summary: "rework"}, p.state)
		_, _, err = s.RecordAuditResult("atomic-audit", resultTaskID, "run-audit", 5, result, now)
	}
	if err != nil {
		t.Fatal(err)
	}
	if p.saveCalls != beforeSaves+1 {
		t.Fatalf("durable saves=%d, want one commit", p.saveCalls-beforeSaves)
	}
	reopened, err := NewCanonical(p)
	if err != nil {
		t.Fatalf("committed result did not survive startup validation: %v", err)
	}
	defer func() { _ = reopened.Close() }()
	if err := ValidateCanonicalState(p.state); err != nil {
		t.Fatalf("saved snapshot invalid: %v", err)
	}
}

func testResultWaitsForStop(t *testing.T, role roles.Role) {
	t.Helper()
	p := &canonicalMemory{state: resultState(role)}
	s, err := NewCanonical(p)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	now := time.Unix(1200, 0).UTC()
	if role == roles.Reviewer {
		result := resultWithEvidence(task.ReviewResult{Verdict: task.ReviewApproved, Summary: "reviewed"}, p.state)
		_, _, err = s.RecordReviewResult("stop-review", resultTaskID, "run-review", 3, result, now)
	} else {
		result := resultWithEvidence(task.AuditResult{Verdict: task.AuditPass, Summary: "audited"}, p.state)
		_, _, err = s.RecordAuditResult("stop-audit", resultTaskID, "run-audit", 5, result, now)
	}
	if err != nil {
		t.Fatal(err)
	}
	taskValue := s.state.Tasks[resultTaskID]
	wantStage, resultRunID := task.Review, "run-review"
	if role == roles.Auditor {
		wantStage, resultRunID = task.Audit, "run-audit"
	}
	if taskValue.Status != task.Blocked || taskValue.CurrentStage == nil || *taskValue.CurrentStage != wantStage || s.state.Runs[resultRunID].Status != fsm.Done {
		if role == roles.Reviewer {
			t.Fatalf("result did not block Task while completing role Run: task=%+v run=%+v", taskValue, s.state.Runs["run-review"])
		}
	}
	if role == roles.Auditor && (taskValue.Status != task.Blocked || s.state.Runs["run-audit"].Status != fsm.Done) {
		t.Fatalf("Audit result did not await stop proof: task=%+v run=%+v", taskValue, s.state.Runs["run-audit"])
	}
	if taskValue.Fence != 2 && role == roles.Reviewer || taskValue.Fence != 3 && role == roles.Auditor {
		t.Fatalf("result changed Task Fence before stop proof: %d", taskValue.Fence)
	}
	if len(s.state.Runs) != 2 && role == roles.Reviewer || len(s.state.Runs) != 3 && role == roles.Auditor {
		t.Fatalf("result created an unexpected writer Run: %d runs", len(s.state.Runs))
	}
}

func assertResultCommit(t *testing.T, p *canonicalMemory, receipt InboxReceipt, eventType task.EventType, now time.Time) {
	t.Helper()
	if p.saveCalls != 1 || len(p.state.Events) == 0 || receipt.CommittedRevision != p.state.Tasks[resultTaskID].Revision {
		t.Fatalf("state, event and receipt were not committed once: saves=%d receipt=%+v", p.saveCalls, receipt)
	}
	event := p.state.Events[len(p.state.Events)-1]
	if event.Type != eventType || event.TaskRevision != receipt.CommittedRevision || !event.CreatedAt.Equal(now) {
		t.Fatalf("event does not match result commit: %+v", event)
	}
	if p.state.Tasks[resultTaskID].Status != task.Blocked {
		t.Fatalf("Task status=%s, want BLOCKED", p.state.Tasks[resultTaskID].Status)
	}
	if receipt.EventIDs[0] != event.ID || receipt.RunID != event.RunID {
		t.Fatalf("Inbox receipt does not reference committed result event: %+v", receipt)
	}
	queue := p.state.Outbox[event.RunID]
	if len(queue.Messages) != 1 || queue.Messages[0].EventID != event.ID || queue.Messages[0].Envelope.Kind != envelope.ResultAck {
		t.Fatalf("result acknowledgement is not in the same commit: %+v", queue)
	}
}

func resultWithEvidence[T task.ReviewResult | task.AuditResult](result T, _ CanonicalState) T {
	switch value := any(result).(type) {
	case task.ReviewResult:
		if value.Findings == nil {
			value.Findings = []task.Finding{{Severity: task.SeverityMajor, Summary: "finding", Evidence: []task.Reference{{Kind: task.ReferenceTask, ID: string(resultTaskID)}}}}
		}
		if value.InputArtifactRefs == nil {
			value.InputArtifactRefs = []string{}
		}
		if value.OutputArtifactRefs == nil {
			value.OutputArtifactRefs = []string{}
		}
		return any(value).(T)
	case task.AuditResult:
		if value.Findings == nil {
			value.Findings = []task.Finding{{Severity: task.SeverityMinor, Summary: "finding", Evidence: []task.Reference{{Kind: task.ReferenceTask, ID: string(resultTaskID)}}}}
		}
		if value.InputArtifactRefs == nil {
			value.InputArtifactRefs = []string{}
		}
		if value.OutputArtifactRefs == nil {
			value.OutputArtifactRefs = []string{}
		}
		return any(value).(T)
	default:
		panic("unsupported result type")
	}
}

func resultState(resultRole roles.Role) CanonicalState {
	state := EmptyCanonicalState()
	id := resultTaskID
	now := time.Unix(100, 0).UTC()
	stagePlan := []task.Stage{task.Execution, task.Review}
	currentStage := task.Review
	if resultRole == roles.Auditor {
		stagePlan = []task.Stage{task.Execution, task.Review, task.Audit}
		currentStage = task.Audit
	}
	state.Tasks[id] = task.Task{ID: id, Title: "Task", OwnerID: "owner", Revision: 3, LastEventSeq: 3,
		StagePlan: task.StagePlan{Stages: stagePlan, PolicyVersion: task.StagePlanPolicyVersion}, CurrentStage: &currentStage,
		Status: task.InProgress, Fence: 2, CreatedAt: now, UpdatedAt: now.Add(2 * time.Second)}
	state.Events = append(state.Events,
		task.DomainEvent{ID: task.EventID(id, 1), TaskID: id, EventSeq: 1, TaskRevision: 1, TaskFence: 0, Type: task.TaskCreated, ActorID: "owner", CreatedAt: now},
		task.DomainEvent{ID: task.EventID(id, 2), TaskID: id, RunID: "run-executor", EventSeq: 2, TaskRevision: 2, TaskFence: 1, Type: task.TaskOwnershipTransferred, CreatedAt: now.Add(time.Second)},
		task.DomainEvent{ID: task.EventID(id, 3), TaskID: id, RunID: "run-review", EventSeq: 3, TaskRevision: 3, TaskFence: 2, Type: task.TaskOwnershipTransferred, CreatedAt: now.Add(2 * time.Second)},
	)
	sha := strings.Repeat("a", 40)
	executorSelection := roles.Selection{AgentID: "agent-executor", Session: "executor-session", Profile: roles.Profile{Role: roles.Executor}}
	state.Runs["run-executor"] = fsm.Run{Task: "task-label", TaskID: id, RunID: "run-executor", Attempt: 1, Fence: 1,
		TargetSHA: sha, OwnerSession: executorSelection.Session, CoordinatorSession: "coord-session", Worktree: "worktree",
		Deadline: now.Add(time.Hour), AssignedAt: now, LastUpdate: now, Status: fsm.Done,
		Result:       &envelope.ResultPayload{Outcome: "SUCCESS", Summary: "executor result"},
		RequiredRole: roles.Requirement{Role: roles.Executor}, RoleSelection: &executorSelection, RoleHistory: []roles.Selection{executorSelection}}
	run := state.Runs["run-executor"]
	bindCanonicalTestRun(&state, &run, executorSelection.AgentID, now)
	state.Runs[run.RunID] = run
	role := resultRole
	runID := "run-review"
	if resultRole == roles.Auditor {
		runID = "run-audit"
		state.Tasks[id] = func() task.Task {
			v := state.Tasks[id]
			v.Revision = 5
			v.LastEventSeq = 5
			v.Fence = 3
			v.UpdatedAt = now.Add(4 * time.Second)
			return v
		}()
		state.Events = append(state.Events,
			task.DomainEvent{ID: task.EventID(id, 4), TaskID: id, RunID: "run-review", EventSeq: 4, TaskRevision: 4, TaskFence: 2, Type: task.ReviewResultRecorded, ActorID: "agent-reviewer", Summary: "review complete", CreatedAt: now.Add(3 * time.Second),
				ReviewResult: &task.ReviewResult{Verdict: task.ReviewApproved, Findings: []task.Finding{}, Summary: "review complete", InputArtifactRefs: []string{}, OutputArtifactRefs: []string{}, CreatedAt: now.Add(3 * time.Second)}},
			task.DomainEvent{ID: task.EventID(id, 5), TaskID: id, RunID: "run-audit", EventSeq: 5, TaskRevision: 5, TaskFence: 3, Type: task.TaskOwnershipTransferred, CreatedAt: now.Add(4 * time.Second)},
		)
		reviewerSelection := roles.Selection{AgentID: "agent-reviewer", Session: "review-session", Profile: roles.Profile{Role: roles.Reviewer}}
		state.Runs["run-review"] = fsm.Run{Task: "task-label", TaskID: id, RunID: "run-review", Attempt: 1, Fence: 2,
			TargetSHA: sha, OwnerSession: reviewerSelection.Session, CoordinatorSession: "coord-session", Worktree: "worktree",
			Deadline: now.Add(time.Hour), AssignedAt: now, LastUpdate: now, Status: fsm.Done,
			RequiredRole: roles.Requirement{Role: roles.Reviewer}, RoleSelection: &reviewerSelection, RoleHistory: []roles.Selection{reviewerSelection}}
		run := state.Runs["run-review"]
		bindCanonicalTestRun(&state, &run, reviewerSelection.AgentID, now.Add(2*time.Second))
		state.Runs[run.RunID] = run
		role = roles.Auditor
	}
	selection := roles.Selection{AgentID: "agent-" + strings.ToLower(string(role)), Session: strings.ToLower(string(role)) + "-session", Profile: roles.Profile{Role: role}}
	state.Runs[runID] = fsm.Run{Task: "task-label", TaskID: id, RunID: runID, Attempt: 1, Fence: state.Tasks[id].Fence,
		TargetSHA: sha, OwnerSession: selection.Session, CoordinatorSession: "coord-session", Worktree: "worktree",
		Deadline: now.Add(time.Hour), AssignedAt: now, LastUpdate: now, Status: fsm.Started,
		RequiredRole: roles.Requirement{Role: role}, RoleSelection: &selection, RoleHistory: []roles.Selection{selection}}
	run = state.Runs[runID]
	bindCanonicalTestRun(&state, &run, selection.AgentID, now.Add(time.Duration(state.Tasks[id].LastEventSeq)*time.Second))
	state.Runs[runID] = run
	return state
}

// executorState returns a CanonicalState with a Task at Execution stage and an
// active Executor Run, ready to receive RecordExecutorResult.
func executorState() CanonicalState {
	state := EmptyCanonicalState()
	id := resultTaskID
	now := time.Unix(100, 0).UTC()
	currentStage := task.Execution
	state.Tasks[id] = task.Task{
		ID: id, Title: "Task", OwnerID: "owner", Revision: 2, LastEventSeq: 2,
		StagePlan:    task.StagePlan{Stages: []task.Stage{task.Execution, task.Review}, PolicyVersion: task.StagePlanPolicyVersion},
		CurrentStage: &currentStage, Status: task.InProgress, Fence: 1,
		CreatedAt: now, UpdatedAt: now.Add(time.Second),
	}
	state.Events = append(state.Events,
		task.DomainEvent{ID: task.EventID(id, 1), TaskID: id, EventSeq: 1, TaskRevision: 1, TaskFence: 0, Type: task.TaskCreated, ActorID: "owner", CreatedAt: now},
		task.DomainEvent{ID: task.EventID(id, 2), TaskID: id, RunID: "run-executor", EventSeq: 2, TaskRevision: 2, TaskFence: 1, Type: task.TaskOwnershipTransferred, CreatedAt: now.Add(time.Second)},
	)
	sha := strings.Repeat("a", 40)
	executorSelection := roles.Selection{AgentID: "agent-executor", Session: "executor-session", Profile: roles.Profile{Role: roles.Executor}}
	state.Runs["run-executor"] = fsm.Run{
		Task: "task-label", TaskID: id, RunID: "run-executor", Attempt: 1, Fence: 1,
		TargetSHA: sha, CoordinatorSession: "coord-session", Worktree: "worktree",
		Deadline: now.Add(time.Hour), AssignedAt: now, LastUpdate: now, Status: fsm.Started,
		RequiredRole:  roles.Requirement{Role: roles.Executor},
		RoleSelection: &executorSelection, RoleHistory: []roles.Selection{executorSelection},
	}
	run := state.Runs["run-executor"]
	bindCanonicalTestRun(&state, &run, executorSelection.AgentID, now)
	state.Runs[run.RunID] = run
	return state
}

func TestExecutorResultIsStructuredAndTaskScoped(t *testing.T) {
	p := &canonicalMemory{state: ownerEnrolledCanonicalState(t, executorState())}
	s, err := NewCanonical(p)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	now := time.Unix(500, 0).UTC()
	result := task.ExecutorResult{Outcome: task.ExecutorSuccess, Summary: "executor finished", OutputArtifactRefs: []string{}}
	receipt, replayed, err := s.RecordExecutorResult("executor-message", resultTaskID, "run-executor", 2, result, now)
	if err != nil || replayed {
		t.Fatalf("receipt=%+v replayed=%v err=%v", receipt, replayed, err)
	}
	assertResultCommit(t, p, receipt, task.ExecutorResultRecorded, now)
	event := p.state.Events[len(p.state.Events)-1]
	if event.ActorID != "agent-executor" || event.ExecutorResult == nil || event.ExecutorResult.Outcome != task.ExecutorSuccess {
		t.Fatalf("Executor result actor/payload incorrect: %+v", event)
	}
	if p.state.Runs["run-executor"].Status != fsm.Done {
		t.Fatal("Executor Run status was not set to Done after result")
	}
}

func TestExecutorResultRejectsNonExecutorRun(t *testing.T) {
	p := &canonicalMemory{state: resultState(roles.Reviewer)}
	s, err := NewCanonical(p)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	result := task.ExecutorResult{Outcome: task.ExecutorSuccess, Summary: "done", OutputArtifactRefs: []string{}}
	_, _, err = s.RecordExecutorResult("wrong-role", resultTaskID, "run-review", 3, result, time.Unix(500, 0).UTC())
	if err == nil || !strings.Contains(err.Error(), "Executor Run") {
		t.Fatalf("non-Executor Run accepted: err=%v", err)
	}
	if p.saveCalls != 0 {
		t.Fatal("non-Executor result was committed")
	}
}
