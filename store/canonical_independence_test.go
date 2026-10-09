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
	ensureCanonicalTestAgentSession(&state, "task-b-executor-session", "task-b-executor-agent", now)
	run := state.Runs["task-b-reviewer"]
	bindCanonicalTestRun(&state, &run, selection.AgentID, now)
	state.Runs[run.RunID] = run
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
	ensureCanonicalTestAgentSession(&state, run.OwnerSession, extra.AgentID, state.Tasks[run.TaskID].UpdatedAt)
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
	run := state.Runs["reviewer"]
	bindCanonicalTestRun(&state, &run, selection.AgentID, now)
	state.Runs[run.RunID] = run
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
		bindCanonicalTestRun(&state, &executor, selected.AgentID, state.Tasks[id].UpdatedAt)
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
		ensureCanonicalTestAgentSession(&state, reviewer.OwnerSession, selected.AgentID, state.Tasks[id].UpdatedAt)
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
		bindCanonicalTestRun(&state, &executor, "agent-review-extra", state.Tasks[id].UpdatedAt)
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

func TestCanonicalStartupRejectsFormerReviewerOrAuditorAsExecutor(t *testing.T) {
	for _, role := range []roles.Role{roles.Reviewer, roles.Auditor} {
		t.Run(string(role), func(t *testing.T) {
			_, state := canonicalPriorReviewerOrAuditorThenExecutor(t, role, "agent-former-reviewer", false)
			prior := state.Runs["prior-review-role"]
			if !prior.Terminal() {
				t.Fatal("fixture prior Reviewer/Auditor Run must be terminal")
			}
			candidate := state.Runs["executor-after-review"]
			if candidate.RoleSelection.AgentID != "agent-former-reviewer" || candidate.OwnerSession == prior.OwnerSession {
				t.Fatalf("fixture must reuse stable AgentID through a different session: candidate=%+v prior=%+v", candidate, prior)
			}
			p := &canonicalMemory{state: state}
			_, err := NewCanonical(p)
			if !errors.Is(err, ErrExecutorRoleHistoryConflict) || !strings.Contains(err.Error(), "AgentID \"agent-former-reviewer\"") {
				t.Fatalf("startup error=%v, want EXECUTOR_ROLE_HISTORY_CONFLICT for stable AgentID", err)
			}
			if p.saveCalls != 0 {
				t.Fatalf("invalid startup snapshot was saved %d times", p.saveCalls)
			}
		})
	}
}

func TestCanonicalStartupRejectsExecutorMatchingOnlyPriorRoleHistory(t *testing.T) {
	_, state := canonicalPriorReviewerOrAuditorThenExecutor(t, roles.Reviewer, "agent-history-only", true)
	prior := state.Runs["prior-review-role"]
	if prior.RoleSelection.AgentID == "agent-history-only" || prior.RoleHistory[0].AgentID != "agent-history-only" || !prior.Terminal() {
		t.Fatalf("fixture must keep the colliding AgentID only in terminal Run RoleHistory: %+v", prior)
	}
	p := &canonicalMemory{state: state}
	_, err := NewCanonical(p)
	if !errors.Is(err, ErrExecutorRoleHistoryConflict) {
		t.Fatalf("startup error=%v, want EXECUTOR_ROLE_HISTORY_CONFLICT from RoleHistory", err)
	}
	if p.saveCalls != 0 {
		t.Fatalf("invalid startup snapshot was saved %d times", p.saveCalls)
	}
}

func TestCanonicalStartupRejectsExecutorWithoutStableAgentIDAfterReviewOrAudit(t *testing.T) {
	for _, role := range []roles.Role{roles.Reviewer, roles.Auditor} {
		t.Run(string(role), func(t *testing.T) {
			_, state := canonicalPriorReviewerOrAuditorThenExecutor(t, role, "agent-former-reviewer", false)
			executor := state.Runs["executor-after-review"]
			executor.RoleSelection = nil
			executor.RoleHistory = nil
			state.Runs[executor.RunID] = executor

			p := &canonicalMemory{state: state}
			_, err := NewCanonical(p)
			if !errors.Is(err, ErrExecutorRoleHistoryConflict) || !strings.Contains(err.Error(), "Executor assignment has no verifiable stable AgentID") {
				t.Fatalf("startup error=%v, want EXECUTOR_ROLE_HISTORY_CONFLICT for unverifiable Executor identity after %s", err, role)
			}
			if p.saveCalls != 0 {
				t.Fatalf("invalid startup snapshot was saved %d times", p.saveCalls)
			}
		})
	}
}

func TestCanonicalStartupAllowsExecutorWithoutStableAgentIDBeforeReviewOrAudit(t *testing.T) {
	state := canonicalIndependenceBase(task.Execution)
	executor := state.Runs["executor-current"]
	executor.RoleSelection = nil
	executor.RoleHistory = nil
	state.Runs[executor.RunID] = executor

	p := &canonicalMemory{state: state}
	if _, err := NewCanonical(p); err != nil {
		t.Fatalf("Executor without stable AgentID before any Reviewer/Auditor assignment was rejected: %v", err)
	}
	if p.saveCalls != 0 {
		t.Fatalf("valid legacy snapshot was rewritten %d times", p.saveCalls)
	}
}

func TestCanonicalTransitionRejectsExecutorWithoutStableAgentIDAfterReviewOrAudit(t *testing.T) {
	for _, role := range []roles.Role{roles.Reviewer, roles.Auditor} {
		t.Run(string(role), func(t *testing.T) {
			previous, next := canonicalPriorReviewerOrAuditorThenExecutor(t, role, "agent-former-reviewer", false)
			executor := next.Runs["executor-after-review"]
			executor.RoleSelection = nil
			executor.RoleHistory = nil
			next.Runs[executor.RunID] = executor

			if err := ValidateCanonicalTransition(previous, next); !errors.Is(err, ErrExecutorRoleHistoryConflict) || !strings.Contains(err.Error(), "Executor assignment has no verifiable stable AgentID") {
				t.Fatalf("transition validation error=%v, want EXECUTOR_ROLE_HISTORY_CONFLICT for unverifiable Executor identity after %s", err, role)
			}
			p := &canonicalMemory{state: previous}
			s, err := NewCanonical(p)
			if err != nil {
				t.Fatalf("open pre-assignment state: %v", err)
			}
			defer func() { _ = s.Close() }()
			before, err := copyCanonicalState(s.state)
			if err != nil {
				t.Fatal(err)
			}
			err = s.UpdateCanonical(func(state *CanonicalState) error {
				*state = next
				return nil
			})
			if !errors.Is(err, ErrExecutorRoleHistoryConflict) {
				t.Fatalf("UpdateCanonical error=%v, want EXECUTOR_ROLE_HISTORY_CONFLICT", err)
			}
			if p.saveCalls != 0 || !reflect.DeepEqual(s.state, before) || !reflect.DeepEqual(p.state, before) {
				t.Fatal("rejected Executor assignment changed state or performed durable Save")
			}
		})
	}
}

func TestCanonicalTransitionRejectsFormerReviewerOrAuditorAsExecutorWithoutSave(t *testing.T) {
	for _, role := range []roles.Role{roles.Reviewer, roles.Auditor} {
		t.Run(string(role), func(t *testing.T) {
			previous, next := canonicalPriorReviewerOrAuditorThenExecutor(t, role, "agent-former-reviewer", false)
			if err := ValidateCanonicalTransition(previous, next); !errors.Is(err, ErrExecutorRoleHistoryConflict) {
				t.Fatalf("transition validation error=%v, want EXECUTOR_ROLE_HISTORY_CONFLICT", err)
			}

			p := &canonicalMemory{state: previous}
			s, err := NewCanonical(p)
			if err != nil {
				t.Fatalf("open valid pre-assignment state: %v", err)
			}
			defer func() { _ = s.Close() }()
			before, err := copyCanonicalState(s.state)
			if err != nil {
				t.Fatal(err)
			}
			err = s.UpdateCanonical(func(state *CanonicalState) error {
				*state = next
				return nil
			})
			if !errors.Is(err, ErrExecutorRoleHistoryConflict) {
				t.Fatalf("UpdateCanonical error=%v, want EXECUTOR_ROLE_HISTORY_CONFLICT", err)
			}
			if p.saveCalls != 0 || !reflect.DeepEqual(s.state, before) || !reflect.DeepEqual(p.state, before) {
				t.Fatal("rejected Executor assignment changed state or performed durable Save")
			}
		})
	}
}

func TestSameAgentMayBeExecutorInAnotherTaskAfterReview(t *testing.T) {
	for _, priorRole := range []roles.Role{roles.Reviewer, roles.Auditor} {
		t.Run(string(priorRole), func(t *testing.T) {
			state, _ := canonicalPriorReviewerOrAuditorThenExecutor(t, priorRole, "agent-cross-task", false)
			var priorAssignmentSeq uint64
			for _, event := range state.Events {
				if event.RunID == "prior-review-role" && event.Type == task.TaskOwnershipTransferred {
					priorAssignmentSeq = event.EventSeq
				}
			}
			if priorAssignmentSeq == 0 {
				t.Fatal("fixture has no prior Reviewer/Auditor assignment event")
			}

			otherTaskID := taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAW")
			executorRunID := "other-task-executor"
			addIndependentTaskExecutor(t, &state, otherTaskID, executorRunID, "agent-cross-task", priorAssignmentSeq+1)
			var executorAssignmentSeq uint64
			for _, event := range state.Events {
				if event.TaskID == otherTaskID && event.RunID == executorRunID && event.Type == task.TaskOwnershipTransferred {
					executorAssignmentSeq = event.EventSeq
				}
			}
			if executorAssignmentSeq <= priorAssignmentSeq {
				t.Fatalf("fixture must compare a larger Task B EventSeq (%d) with prior %s EventSeq (%d)", executorAssignmentSeq, priorRole, priorAssignmentSeq)
			}
			if err := ValidateCanonicalState(state); err != nil {
				t.Fatalf("same AgentID as %s in Task A was rejected as Executor in Task B: %v", priorRole, err)
			}
		})
	}
}

func TestSameAgentMayReviewAgainInSameTask(t *testing.T) {
	state := canonicalIndependenceBase(task.Review)
	state = canonicalRoleTransfer(t, state, roles.Reviewer, "review-first", "agent-repeat-reviewer", "review-model")
	first := state.Runs["review-first"]
	first.Status = fsm.Failed
	state.Runs[first.RunID] = first
	state = canonicalRoleTransfer(t, state, roles.Reviewer, "review-again", "agent-repeat-reviewer", "review-model")
	if err := ValidateCanonicalState(state); err != nil {
		t.Fatalf("repeating Reviewer with the same AgentID in one Task was rejected: %v", err)
	}
}

func canonicalPriorReviewerOrAuditorThenExecutor(t *testing.T, priorRole roles.Role, priorAgentID string, historyOnly bool) (CanonicalState, CanonicalState) {
	t.Helper()
	priorStage := task.Review
	if priorRole == roles.Auditor {
		priorStage = task.Audit
	}
	state := canonicalRoleTransfer(t, canonicalIndependenceBase(priorStage), priorRole, "prior-review-role", priorAgentID, "prior-role-model")
	prior := state.Runs["prior-review-role"]
	if historyOnly {
		current := *prior.RoleSelection
		current.AgentID = "agent-current-review-role"
		prior.RoleHistory = append(prior.RoleHistory, current)
		prior.RoleSelection = &current
		bindCanonicalTestRun(&state, &prior, current.AgentID, state.Tasks[prior.TaskID].UpdatedAt)
		appendTaskDomainEvent(&state, prior.TaskID, prior.RunID, "ROLE_HISTORY_UPDATED")
	}
	prior.Status = fsm.Failed
	state.Runs[prior.RunID] = prior
	taskValue := state.Tasks[prior.TaskID]
	execution := task.Execution
	taskValue.CurrentStage = &execution
	state.Tasks[prior.TaskID] = taskValue

	next := ownershipTransferCandidate(t, state, "executor-after-review", roles.Executor, 3, taskValue.Fence+1)
	executor := next.Runs["executor-after-review"]
	bindCanonicalTestRun(&next, &executor, priorAgentID, next.Tasks[executor.TaskID].UpdatedAt)
	next.Runs[executor.RunID] = executor
	return state, next
}

func addIndependentTaskExecutor(t *testing.T, state *CanonicalState, id taskid.ID, runID, agentID string, transferSeq uint64) {
	t.Helper()
	if transferSeq < 2 {
		t.Fatalf("transfer EventSeq must follow Task creation, got %d", transferSeq)
	}
	now := time.Unix(4000, 0).UTC()
	execution := task.Execution
	updatedAt := now.Add(time.Duration(transferSeq-1) * time.Second)
	state.Tasks[id] = task.Task{
		ID: id, Title: "other task", OwnerID: "owner", Revision: transferSeq, LastEventSeq: transferSeq,
		StagePlan:    task.StagePlan{Stages: []task.Stage{task.Execution, task.Review}, PolicyVersion: task.StagePlanPolicyVersion},
		CurrentStage: &execution, Status: task.InProgress, Fence: 1, CreatedAt: now, UpdatedAt: updatedAt,
	}
	state.Events = append(state.Events, task.DomainEvent{ID: task.EventID(id, 1), TaskID: id, EventSeq: 1, TaskRevision: 1, Type: task.TaskCreated, ActorID: "owner", CreatedAt: now})
	for seq := uint64(2); seq <= transferSeq; seq++ {
		event := task.DomainEvent{
			ID: task.EventID(id, seq), TaskID: id, EventSeq: seq, TaskRevision: seq,
			TaskFence: 0, Type: "TASK_NOTE", ActorID: "owner", CreatedAt: now.Add(time.Duration(seq-1) * time.Second),
		}
		if seq == transferSeq {
			event.RunID = runID
			event.Type = task.TaskOwnershipTransferred
			event.TaskFence = 1
		}
		state.Events = append(state.Events, event)
	}
	selection := roles.Selection{AgentID: agentID, Session: "other-task-session", Profile: roles.Profile{Role: roles.Executor}}
	run := fsm.Run{
		Task: "task B", TaskID: id, RunID: runID, Attempt: 1, Fence: 1,
		OwnerSession: selection.Session, Worktree: "worktree", Deadline: updatedAt.Add(time.Hour), AssignedAt: updatedAt,
		LastUpdate: updatedAt, Status: fsm.Pending, RequiredRole: roles.Requirement{Role: roles.Executor},
		RoleSelection: &selection, RoleHistory: []roles.Selection{selection},
	}
	bindCanonicalTestRun(state, &run, agentID, updatedAt)
	state.Runs[run.RunID] = run
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
	bindCanonicalTestRun(&next, &run, agentID, next.Tasks[run.TaskID].UpdatedAt)
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
	ensureCanonicalTestAgentSession(&state, "executor-old-session", "agent-old-current", now)
	ensureCanonicalTestAgentSession(&state, "executor-current-session", "agent-current", now)
	return state
}

func TestFormerReviewerOrAuditorCannotLaterBecomeExecutor(t *testing.T) {
	for _, priorRole := range []roles.Role{roles.Reviewer, roles.Auditor} {
		t.Run(string(priorRole), func(t *testing.T) {
			_, candidate := canonicalPriorReviewerOrAuditorThenExecutor(t, priorRole, "agent-r", false)
			if err := ValidateCanonicalState(candidate); !errors.Is(err, ErrExecutorRoleHistoryConflict) {
				t.Fatalf("Executor assignment after prior %s selection error=%v, want EXECUTOR_ROLE_HISTORY_CONFLICT", priorRole, err)
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
