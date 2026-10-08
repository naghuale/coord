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

func TestReviewerRejectsExecutorAgentIDAcrossHistory(t *testing.T) {
	for _, executorID := range []string{"agent-former", "agent-old-current", "agent-current"} {
		t.Run(executorID, func(t *testing.T) {
			previous := canonicalIndependenceBase(task.Review)
			next := canonicalRoleTransfer(t, previous, roles.Reviewer, "review-run", executorID, "review-model")
			if err := ValidateCanonicalState(next); !errors.Is(err, ErrNoIndependentReviewer) || !strings.Contains(err.Error(), "appears in prior Executor history") {
				t.Fatalf("startup validation error=%v, want NO_INDEPENDENT_REVIEWER", err)
			}
		})
	}
}

func TestAuditorRejectsExecutorAgentIDAcrossTaskRuns(t *testing.T) {
	previous := canonicalIndependenceBase(task.Audit)
	next := canonicalRoleTransfer(t, previous, roles.Auditor, "audit-run", "agent-former", "audit-model")
	if err := ValidateCanonicalState(next); !errors.Is(err, ErrNoIndependentAuditor) || !strings.Contains(err.Error(), "appears in prior Executor history") {
		t.Fatalf("startup validation error=%v, want NO_INDEPENDENT_AUDITOR", err)
	}
}

func TestIndependentAuditorMayUseSameModel(t *testing.T) {
	previous := canonicalIndependenceBase(task.Audit)
	executor := previous.Runs["executor-current"]
	executor.RoleSelection.Profile.Model = "shared-model"
	executor.RoleHistory[len(executor.RoleHistory)-1].Profile.Model = "shared-model"
	previous.Runs[executor.RunID] = executor
	next := canonicalRoleTransfer(t, previous, roles.Auditor, "audit-run", "agent-auditor", "shared-model")
	if err := ValidateCanonicalState(next); err != nil {
		t.Fatalf("same model with independent AgentID rejected at startup: %v", err)
	}
	if err := ValidateCanonicalTransition(previous, next); err != nil {
		t.Fatalf("same model with independent AgentID rejected at transition: %v", err)
	}
}

func TestNoIndependentReviewerOrAuditorFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name  string
		stage task.Stage
		role  roles.Role
		runID string
		id    string
		want  error
	}{
		{"reviewer", task.Review, roles.Reviewer, "review-run", "agent-former", ErrNoIndependentReviewer},
		{"auditor", task.Audit, roles.Auditor, "audit-run", "agent-former", ErrNoIndependentAuditor},
	} {
		t.Run(tc.name, func(t *testing.T) {
			previous := canonicalIndependenceBase(tc.stage)
			p := &canonicalMemory{state: previous}
			s, err := NewCanonical(p)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close() }()
			candidate := canonicalRoleTransfer(t, previous, tc.role, tc.runID, tc.id, "model")
			before, err := copyCanonicalState(s.state)
			if err != nil {
				t.Fatal(err)
			}
			err = s.UpdateCanonical(func(state *CanonicalState) error {
				*state = candidate
				return nil
			})
			if !errors.Is(err, tc.want) {
				t.Fatalf("assignment error=%v, want typed error %v", err, tc.want)
			}
			if !strings.Contains(err.Error(), "appears in prior Executor history") {
				t.Fatalf("assignment error=%v, want actual AgentID collision", err)
			}
			if p.saveCalls != 0 || !reflect.DeepEqual(s.state, before) || !reflect.DeepEqual(p.state, before) {
				t.Fatal("rejected assignment changed stored Task/Run/Fence or performed a durable Save")
			}
		})
	}
}

func TestCanonicalStartupRejectsRoleIdentityConflict(t *testing.T) {
	for _, tc := range []struct {
		name  string
		stage task.Stage
		role  roles.Role
		runID string
		id    string
		want  error
	}{
		{"reviewer", task.Review, roles.Reviewer, "review-run", "agent-former", ErrNoIndependentReviewer},
		{"auditor", task.Audit, roles.Auditor, "audit-run", "agent-former", ErrNoIndependentAuditor},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := canonicalRoleTransfer(t, canonicalIndependenceBase(tc.stage), tc.role, tc.runID, tc.id, "model")
			p := &canonicalMemory{state: state}
			_, err := NewCanonical(p)
			if !errors.Is(err, tc.want) {
				t.Fatalf("startup error=%v, want typed error %v", err, tc.want)
			}
			if !strings.Contains(err.Error(), "appears in prior Executor history") {
				t.Fatalf("startup error=%v, want actual AgentID collision", err)
			}
			if p.saveCalls != 0 {
				t.Fatal("invalid snapshot was rewritten")
			}
		})
	}
}

func TestCanonicalTransitionRejectsRoleIdentityConflict(t *testing.T) {
	previous := canonicalIndependenceBase(task.Review)
	next := canonicalRoleTransfer(t, previous, roles.Reviewer, "review-run", "agent-former", "model")
	if err := ValidateCanonicalTransition(previous, next); !errors.Is(err, ErrNoIndependentReviewer) || !strings.Contains(err.Error(), "appears in prior Executor history") {
		t.Fatalf("transition validation error=%v, want NO_INDEPENDENT_REVIEWER", err)
	}
}

func TestCanonicalRoleIndependenceFailsClosedWithoutStableAgentID(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*CanonicalState)
		want   error
	}{
		{"reviewer identity missing", func(state *CanonicalState) {
			run := state.Runs["review-run"]
			run.RoleSelection.AgentID = ""
			run.RoleHistory[len(run.RoleHistory)-1].AgentID = ""
			state.Runs[run.RunID] = run
		}, ErrNoIndependentReviewer},
		{"reviewer selection missing", func(state *CanonicalState) {
			run := state.Runs["review-run"]
			run.RoleSelection = nil
			run.RoleHistory = nil
			state.Runs[run.RunID] = run
		}, ErrNoIndependentReviewer},
		{"reviewer current selection missing despite history", func(state *CanonicalState) {
			run := state.Runs["review-run"]
			run.RoleSelection = nil
			state.Runs[run.RunID] = run
		}, ErrNoIndependentReviewer},
		{"executor history identity missing", func(state *CanonicalState) {
			run := state.Runs["executor-old"]
			run.RoleSelection.AgentID = ""
			for i := range run.RoleHistory {
				run.RoleHistory[i].AgentID = ""
			}
			state.Runs[run.RunID] = run
		}, ErrNoIndependentReviewer},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			state := canonicalRoleTransfer(t, canonicalIndependenceBase(task.Review), roles.Reviewer, "review-run", "independent-reviewer", "model")
			tc.mutate(&state)
			if err := ValidateCanonicalState(state); !errors.Is(err, tc.want) {
				t.Fatalf("validation error=%v, want typed error %v", err, tc.want)
			}
		})
	}
}

func TestSameAgentMayHoldRolesInDifferentTasks(t *testing.T) {
	state := canonicalIndependenceBase(task.Review)
	id := taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAW")
	now := time.Unix(2000, 0).UTC()
	stage := task.Review
	state.Tasks[id] = task.Task{
		ID: id, Title: "other task", OwnerID: "owner", Revision: 6, LastEventSeq: 6,
		StagePlan:    task.StagePlan{Stages: []task.Stage{task.Execution, task.Review, task.Audit}, PolicyVersion: task.StagePlanPolicyVersion},
		CurrentStage: &stage, Status: task.InProgress, Fence: 2, CreatedAt: now, UpdatedAt: now.Add(5 * time.Second),
	}
	state.Events = append(state.Events,
		task.DomainEvent{ID: task.EventID(id, 1), TaskID: id, EventSeq: 1, TaskRevision: 1, Type: task.TaskCreated, ActorID: "owner", CreatedAt: now},
		task.DomainEvent{ID: task.EventID(id, 2), TaskID: id, RunID: "task-b-executor", EventSeq: 2, TaskRevision: 2, TaskFence: 1, Type: task.TaskOwnershipTransferred, CreatedAt: now.Add(time.Second)},
		task.DomainEvent{ID: task.EventID(id, 3), TaskID: id, EventSeq: 3, TaskRevision: 3, TaskFence: 1, Type: "TASK_NOTE", ActorID: "owner", CreatedAt: now.Add(2 * time.Second)},
		task.DomainEvent{ID: task.EventID(id, 4), TaskID: id, EventSeq: 4, TaskRevision: 4, TaskFence: 1, Type: "TASK_NOTE", ActorID: "owner", CreatedAt: now.Add(3 * time.Second)},
		task.DomainEvent{ID: task.EventID(id, 5), TaskID: id, EventSeq: 5, TaskRevision: 5, TaskFence: 1, Type: "TASK_NOTE", ActorID: "owner", CreatedAt: now.Add(4 * time.Second)},
		task.DomainEvent{ID: task.EventID(id, 6), TaskID: id, RunID: "task-b-reviewer", EventSeq: 6, TaskRevision: 6, TaskFence: 2, Type: task.TaskOwnershipTransferred, CreatedAt: now.Add(5 * time.Second)},
	)
	state.Runs["task-b-executor"] = canonicalExecutorRun(id, "task-b-executor", 1, 1, "task-b-executor-session", now, fsm.Failed, []roles.Selection{
		{AgentID: "task-b-executor-agent", Session: "task-b-executor-session", Profile: roles.Profile{Role: roles.Executor, Model: "model"}},
	})
	selection := roles.Selection{AgentID: "agent-current", Session: "task-b-reviewer-session", Profile: roles.Profile{Role: roles.Reviewer, Model: "model"}}
	state.Runs["task-b-reviewer"] = fsm.Run{
		Task: "task-b", TaskID: id, RunID: "task-b-reviewer", Attempt: 1, Fence: 2,
		OwnerSession: selection.Session, Worktree: "worktree", Deadline: now.Add(time.Hour), AssignedAt: now,
		LastUpdate: now, Status: fsm.Pending, RequiredRole: roles.Requirement{Role: roles.Reviewer},
		RoleSelection: &selection, RoleHistory: []roles.Selection{selection},
	}
	if err := ValidateCanonicalState(state); err != nil {
		t.Fatalf("same AgentID across different Tasks was rejected: %v", err)
	}
}

func TestExecutorWithoutStableSelectionFailsClosedBeforeReviewer(t *testing.T) {
	state := canonicalIndependenceBase(task.Review)
	executor := state.Runs["executor-current"]
	executor.RoleSelection = nil
	executor.RoleHistory = nil
	state.Runs[executor.RunID] = executor
	state = canonicalRoleTransfer(t, state, roles.Reviewer, "review-no-executor-identity", "reviewer-independent", "model")
	err := ValidateCanonicalState(state)
	if !errors.Is(err, ErrNoIndependentReviewer) || !strings.Contains(err.Error(), "no verifiable stable AgentID") {
		t.Fatalf("review without verifiable Executor identity error=%v", err)
	}
}

func TestRoleHistoryExtraSelectionRequiresEventAfterTransfer(t *testing.T) {
	state := canonicalIndependenceBase(task.Review)
	run := state.Runs["executor-current"]
	extra := run.RoleHistory[len(run.RoleHistory)-1]
	extra.AgentID = "extra-executor-selection"
	run.RoleHistory = append(run.RoleHistory, extra)
	run.RoleSelection = &extra
	state.Runs[run.RunID] = run
	err := ValidateCanonicalState(state)
	if err == nil || !strings.Contains(err.Error(), "no event anchor for every selection") {
		t.Fatalf("extra selection without post-transfer event error=%v", err)
	}
}

func TestRoleIndependenceDoesNotRequireAnExecutorAssignment(t *testing.T) {
	id := taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAW")
	now := time.Unix(3000, 0).UTC()
	stage := task.Review
	state := EmptyCanonicalState()
	state.Tasks[id] = task.Task{
		ID: id, Title: "workflow boundary", OwnerID: "owner", Revision: 2, LastEventSeq: 2,
		StagePlan:    task.StagePlan{Stages: []task.Stage{task.Execution, task.Review}, PolicyVersion: task.StagePlanPolicyVersion},
		CurrentStage: &stage, Status: task.InProgress, Fence: 1, CreatedAt: now, UpdatedAt: now.Add(time.Second),
	}
	selection := roles.Selection{AgentID: "reviewer", Session: "reviewer-session", Profile: roles.Profile{Role: roles.Reviewer, Model: "model"}}
	state.Runs["reviewer"] = fsm.Run{
		Task: "task", TaskID: id, RunID: "reviewer", Attempt: 1, Fence: 1,
		OwnerSession: selection.Session, Worktree: "worktree", Deadline: now.Add(time.Hour), AssignedAt: now,
		LastUpdate: now, Status: fsm.Pending, RequiredRole: roles.Requirement{Role: roles.Reviewer},
		RoleSelection: &selection, RoleHistory: []roles.Selection{selection},
	}
	state.Events = []task.DomainEvent{
		{ID: task.EventID(id, 1), TaskID: id, EventSeq: 1, TaskRevision: 1, Type: task.TaskCreated, ActorID: "owner", CreatedAt: now},
		{ID: task.EventID(id, 2), TaskID: id, RunID: "reviewer", EventSeq: 2, TaskRevision: 2, TaskFence: 1, Type: task.TaskOwnershipTransferred, CreatedAt: now.Add(time.Second)},
	}
	if err := ValidateCanonicalState(state); err != nil {
		t.Fatalf("identity check invented a prior-Executor requirement: %v", err)
	}
}

func TestRoleHistoryWithoutTransferMarkersUsesConservativeEventBounds(t *testing.T) {
	t.Run("Executor uses earliest possible sequence", func(t *testing.T) {
		state := canonicalIndependenceBase(task.Review)
		id := state.Events[0].TaskID
		executor := state.Runs["executor-old"]
		selected := executor.RoleHistory[len(executor.RoleHistory)-1]
		selected.AgentID = "agent-extra-executor"
		executor.RoleHistory = append(executor.RoleHistory, selected)
		executor.RoleSelection = &selected
		state.Runs[executor.RunID] = executor
		appendTaskDomainEvent(&state, id, executor.RunID, "ROLE_HISTORY_UPDATED")                                           // N=5, earliest possible anchor.
		review := canonicalRoleTransfer(t, state, roles.Reviewer, "review-extra-executor", "agent-extra-executor", "model") // N=6.
		appendTaskDomainEvent(&review, id, executor.RunID, "RUN_NOTE")                                                      // N=7, latest possible anchor.
		err := ValidateCanonicalState(review)
		if !errors.Is(err, ErrNoIndependentReviewer) || !strings.Contains(err.Error(), "appears in prior Executor history") {
			t.Fatalf("Executor selection before candidate was not included: %v", err)
		}
	})

	t.Run("Reviewer uses latest possible sequence", func(t *testing.T) {
		state := canonicalIndependenceBase(task.Review)
		id := state.Events[0].TaskID
		state = canonicalRoleTransfer(t, state, roles.Reviewer, "review-history", "review-agent-first", "model") // N=5.
		reviewer := state.Runs["review-history"]
		selected := *reviewer.RoleSelection
		selected.AgentID = "agent-review-extra"
		reviewer.RoleSelection = &selected
		reviewer.RoleHistory = append(reviewer.RoleHistory, selected)
		state.Runs[reviewer.RunID] = reviewer
		appendTaskDomainEvent(&state, id, reviewer.RunID, "ROLE_HISTORY_UPDATED") // N=6.
		reviewer.Status = fsm.Failed
		state.Runs[reviewer.RunID] = reviewer
		taskValue := state.Tasks[id]
		stage := task.Execution
		taskValue.CurrentStage = &stage
		state.Tasks[id] = taskValue
		state = ownershipTransferCandidate(t, state, "executor-rework-history", roles.Executor, 3, taskValue.Fence+1)
		executor := state.Runs["executor-rework-history"]
		executor.RoleSelection.AgentID = "agent-review-extra"
		executor.RoleHistory[0].AgentID = "agent-review-extra"
		state.Runs[executor.RunID] = executor                         // N=7.
		appendTaskDomainEvent(&state, id, reviewer.RunID, "RUN_NOTE") // N=8, latest possible Reviewer anchor.
		err := ValidateCanonicalState(state)
		if !errors.Is(err, ErrNoIndependentReviewer) || !strings.Contains(err.Error(), "appears in prior Executor history") {
			t.Fatalf("latest possible Reviewer selection boundary was not enforced: %v", err)
		}
	})
}

func TestRoleHistoryRejectsRoleChange(t *testing.T) {
	state := canonicalIndependenceBase(task.Review)
	run := state.Runs["executor-old"]
	run.RoleHistory[0].Profile.Role = roles.Reviewer
	state.Runs[run.RunID] = run
	err := ValidateCanonicalState(state)
	if err == nil || !strings.Contains(err.Error(), "role history changes role") {
		t.Fatalf("role mutation error=%v, want role-history rejection", err)
	}
}

func canonicalRoleTransfer(t *testing.T, previous CanonicalState, role roles.Role, runID, agentID, model string) CanonicalState {
	t.Helper()
	maxAttempt := uint64(0)
	for _, run := range previous.Runs {
		if run.TaskID == taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV") && runRole(run) == roles.Executor && run.Attempt > maxAttempt {
			maxAttempt = run.Attempt
		}
	}
	next := ownershipTransferCandidate(t, previous, runID, role, maxAttempt, previous.Tasks[taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV")].Fence+1)
	run := next.Runs[runID]
	selection := *run.RoleSelection
	selection.AgentID = agentID
	selection.Profile.Model = model
	run.RoleSelection = &selection
	run.RoleHistory = []roles.Selection{selection}
	next.Runs[runID] = run
	return next
}

func canonicalIndependenceBase(stage task.Stage) CanonicalState {
	id := taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	now := time.Unix(1000, 0).UTC()
	state := EmptyCanonicalState()
	state.Tasks[id] = task.Task{
		ID: id, Title: "independence", OwnerID: "owner", Revision: 4, LastEventSeq: 4,
		StagePlan:    task.StagePlan{Stages: []task.Stage{task.Execution, task.Review, task.Audit}, PolicyVersion: task.StagePlanPolicyVersion},
		CurrentStage: &stage, Status: task.InProgress, Fence: 3, CreatedAt: now, UpdatedAt: now.Add(3 * time.Second),
	}
	state.Events = append(state.Events,
		task.DomainEvent{ID: task.EventID(id, 1), TaskID: id, EventSeq: 1, TaskRevision: 1, TaskFence: 0, Type: task.TaskCreated, ActorID: "owner", CreatedAt: now},
		task.DomainEvent{ID: task.EventID(id, 2), TaskID: id, RunID: "executor-old", EventSeq: 2, TaskRevision: 2, TaskFence: 1, Type: task.TaskOwnershipTransferred, CreatedAt: now.Add(time.Second)},
		task.DomainEvent{ID: task.EventID(id, 3), TaskID: id, RunID: "executor-old", EventSeq: 3, TaskRevision: 3, TaskFence: 2, Type: task.TaskOwnershipTransferred, CreatedAt: now.Add(2 * time.Second)},
		task.DomainEvent{ID: task.EventID(id, 4), TaskID: id, RunID: "executor-current", EventSeq: 4, TaskRevision: 4, TaskFence: 3, Type: task.TaskOwnershipTransferred, CreatedAt: now.Add(3 * time.Second)},
	)
	state.Runs["executor-old"] = canonicalExecutorRun(id, "executor-old", 1, 2, "executor-old-session", now, fsm.Failed, []roles.Selection{
		{AgentID: "agent-former", Session: "executor-old-session-1", Profile: roles.Profile{Role: roles.Executor, Model: "executor-model"}},
		{AgentID: "agent-old-current", Session: "executor-old-session", Profile: roles.Profile{Role: roles.Executor, Model: "executor-model"}},
	})
	state.Runs["executor-current"] = canonicalExecutorRun(id, "executor-current", 2, 3, "executor-current-session", now, fsm.Done, []roles.Selection{
		{AgentID: "agent-current", Session: "executor-current-session", Profile: roles.Profile{Role: roles.Executor, Model: "executor-model"}},
	})
	return state
}

func TestReviewerOrAuditorMayOnlyUseAnAgentThatBecameExecutorLaterThanItsPastAssignment(t *testing.T) {
	for _, reviewerCandidate := range []bool{true, false} {
		name, role := "auditor", roles.Auditor
		if reviewerCandidate {
			name, role = "reviewer", roles.Reviewer
		}
		t.Run(name, func(t *testing.T) {
			state := canonicalIndependenceBase(task.Review)
			// R is independent when assigned to review the then-current Executor.
			state = canonicalRoleTransfer(t, state, roles.Reviewer, "review-past", "agent-r", "review-model")
			pastReviewer := state.Runs["review-past"]
			pastReviewer.Status = fsm.Failed
			state.Runs[pastReviewer.RunID] = pastReviewer
			// Later, R becomes the Executor for a rework attempt. The historical
			// review remains valid, while a subsequent review/audit by R must fail.
			state.Tasks[pastReviewer.TaskID] = func() task.Task {
				taskValue := state.Tasks[pastReviewer.TaskID]
				stage := task.Execution
				taskValue.CurrentStage = &stage
				return taskValue
			}()
			state = ownershipTransferCandidate(t, state, "executor-rework", roles.Executor, 3, state.Tasks[pastReviewer.TaskID].Fence+1)
			rework := state.Runs["executor-rework"]
			rework.RoleSelection.AgentID = "agent-r"
			rework.RoleHistory[0].AgentID = "agent-r"
			state.Runs[rework.RunID] = rework
			if err := ValidateCanonicalState(state); err != nil {
				t.Fatalf("future Executor identity retroactively invalidated prior Reviewer assignment: %v", err)
			}
			// End the rework Run without inventing a ReviewResult, then record a
			// later Review/Audit transfer for the same identity.
			rework.Status = fsm.Failed
			state.Runs[rework.RunID] = rework
			taskValue := state.Tasks[pastReviewer.TaskID]
			stage := task.Review
			if role == roles.Auditor {
				stage = task.Audit
			}
			taskValue.CurrentStage = &stage
			state.Tasks[pastReviewer.TaskID] = taskValue
			state = canonicalRoleTransfer(t, state, role, "review-later", "agent-r", "role-model")
			want := ErrNoIndependentReviewer
			if role == roles.Auditor {
				want = ErrNoIndependentAuditor
			}
			if err := ValidateCanonicalState(state); !errors.Is(err, want) {
				t.Fatalf("later assignment error=%v, want %v", err, want)
			}
		})
	}
}

func TestExecutorAtPreviousAdjacentEventSequenceIsNotIndependent(t *testing.T) {
	for _, role := range []roles.Role{roles.Reviewer, roles.Auditor} {
		t.Run(string(role), func(t *testing.T) {
			stage := task.Review
			if role == roles.Auditor {
				stage = task.Audit
			}
			state := canonicalIndependenceBase(stage)
			// The latest Executor transfer is immediately before the candidate's
			// transfer (N-1 then N), so it must be included in the identity check.
			state = canonicalRoleTransfer(t, state, role, "candidate", "agent-current", "model")
			want := ErrNoIndependentReviewer
			if role == roles.Auditor {
				want = ErrNoIndependentAuditor
			}
			if err := ValidateCanonicalState(state); !errors.Is(err, want) {
				t.Fatalf("adjacent prior Executor assignment error=%v, want %v", err, want)
			}
		})
	}
}

func canonicalExecutorRun(id taskid.ID, runID string, attempt, fence uint64, owner string, now time.Time, status fsm.Status, history []roles.Selection) fsm.Run {
	selection := history[len(history)-1]
	run := fsm.Run{
		Task: "task-label", TaskID: id, RunID: runID, Attempt: attempt, Fence: fence,
		OwnerSession: owner, Worktree: "worktree", Deadline: now.Add(time.Hour), AssignedAt: now,
		LastUpdate: now, Status: status, RequiredRole: roles.Requirement{Role: roles.Executor},
		RoleSelection: &selection, RoleHistory: history,
	}
	if status == fsm.Done {
		run.Result = &envelope.ResultPayload{Outcome: "SUCCESS", Summary: "completed"}
	}
	return run
}
