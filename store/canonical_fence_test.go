package store

import (
	"coord/fsm"
	"coord/roles"
	"coord/task"
	"coord/taskid"
	"math"
	"strings"
	"testing"
	"time"
)

func fenceHistoryState() CanonicalState {
	state := taskStateWithRun()
	id := taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	value := state.Tasks[id]
	value.Revision, value.LastEventSeq = 3, 3
	value.UpdatedAt = value.UpdatedAt.Add(time.Second)
	state.Tasks[id] = value
	state.Events = append(state.Events, task.DomainEvent{ID: task.EventID(id, 3), TaskID: id, RunID: "run-1",
		EventSeq: 3, TaskRevision: 3, TaskFence: 1, Type: "WORK_STARTED", CreatedAt: value.UpdatedAt})
	return state
}

func requireStartupRejection(t *testing.T, state CanonicalState, want string) {
	t.Helper()
	if err := ValidateCanonicalState(state); err == nil || (want != "" && !strings.Contains(err.Error(), want)) {
		t.Fatalf("ValidateCanonicalState error=%v, want rejection containing %q", err, want)
	}
}

func TestCanonicalStartupRejectsWrongFenceInEarlyRevision(t *testing.T) {
	state := fenceHistoryState()
	state.Events[1].TaskFence = 0
	requireStartupRejection(t, state, "fence snapshot")
}

func TestCanonicalStartupRejectsWrongFenceInLatestRevision(t *testing.T) {
	state := fenceHistoryState()
	state.Events[2].TaskFence = 2
	requireStartupRejection(t, state, "fence snapshot")
}

func TestCanonicalStartupRejectsMissingFenceBumpOnTransfer(t *testing.T) {
	state := fenceHistoryState()
	id := state.Events[0].TaskID
	value := state.Tasks[id]
	value.Revision++
	value.LastEventSeq++
	value.UpdatedAt = value.UpdatedAt.Add(time.Second)
	state.Tasks[id] = value
	state.Events = append(state.Events, task.DomainEvent{ID: task.EventID(id, value.LastEventSeq), TaskID: id, RunID: "run-1",
		EventSeq: value.LastEventSeq, TaskRevision: value.Revision, TaskFence: value.Fence,
		Type: task.TaskOwnershipTransferred, CreatedAt: value.UpdatedAt})
	requireStartupRejection(t, state, "fence snapshot")
}

func TestCanonicalStartupRejectsExtraFenceBumpWithoutTransfer(t *testing.T) {
	state := fenceHistoryState()
	state.Tasks[state.Events[0].TaskID] = func() task.Task { v := state.Tasks[state.Events[0].TaskID]; v.Fence = 2; return v }()
	run := state.Runs["run-1"]
	run.Status = fsm.Failed
	state.Runs[run.RunID] = run
	state.Events[2].TaskFence = 2
	requireStartupRejection(t, state, "fence snapshot")
}

func TestCanonicalStartupRejectsTwoTransferMarkersInOneRevision(t *testing.T) {
	state := fenceHistoryState()
	state.Events[2].TaskRevision = 2
	state.Events[2].Type = task.TaskOwnershipTransferred
	requireStartupRejection(t, state, "multiple ownership transfer markers")
}

func TestCanonicalStartupRejectsSecondEventInCreationRevision(t *testing.T) {
	state := taskState()
	id := state.Events[0].TaskID
	value := state.Tasks[id]
	value.LastEventSeq = 2
	state.Tasks[id] = value
	state.Events = append(state.Events, task.DomainEvent{ID: task.EventID(id, 2), TaskID: id,
		EventSeq: 2, TaskRevision: 1, TaskFence: 0, Type: "TASK_NOTE", CreatedAt: value.UpdatedAt})
	requireStartupRejection(t, state, "revision 1 must contain exactly one TASK_CREATED")
}

func TestCanonicalStartupRejectsTransferMarkerWithoutRunID(t *testing.T) {
	state := taskState()
	id := state.Events[0].TaskID
	value := state.Tasks[id]
	value.Revision, value.LastEventSeq, value.Fence = 2, 2, 1
	value.UpdatedAt = value.UpdatedAt.Add(time.Second)
	state.Tasks[id] = value
	state.Events = append(state.Events, task.DomainEvent{ID: task.EventID(id, 2), TaskID: id,
		EventSeq: 2, TaskRevision: 2, TaskFence: 1, Type: task.TaskOwnershipTransferred, CreatedAt: value.UpdatedAt})
	requireStartupRejection(t, state, "ownership transfer event has no RunID")
}

func TestCanonicalStartupRejectsFenceOverflow(t *testing.T) {
	if _, err := incrementTaskFence(math.MaxUint64); err != ErrFenceExhausted {
		t.Fatalf("increment at MaxUint64 error=%v, want ErrFenceExhausted", err)
	}
}

func TestCanonicalStartupRejectsFinalFenceDifferentFromTask(t *testing.T) {
	state := fenceHistoryState()
	id := state.Events[0].TaskID
	v := state.Tasks[id]
	v.Fence = 2
	state.Tasks[id] = v
	run := state.Runs["run-1"]
	run.Status = fsm.Failed
	state.Runs[run.RunID] = run
	requireStartupRejection(t, state, "final event fence")
}

func TestCanonicalStartupRejectsSecondTaskCreated(t *testing.T) {
	state := fenceHistoryState()
	value := state.Tasks[state.Events[0].TaskID]
	value.LastEventSeq++
	state.Tasks[value.ID] = value
	e := task.DomainEvent{ID: task.EventID(value.ID, value.LastEventSeq), TaskID: value.ID, EventSeq: value.LastEventSeq,
		TaskRevision: value.Revision, TaskFence: value.Fence, Type: task.TaskCreated, CreatedAt: value.UpdatedAt}
	state.Events = append(state.Events, e)
	requireStartupRejection(t, state, "TASK_CREATED after")
}

func TestCanonicalStartupRejectsLateTaskCreated(t *testing.T) {
	state := fenceHistoryState()
	state.Events[1].Type = task.TaskCreated
	requireStartupRejection(t, state, "TASK_CREATED after")
}

func TestCanonicalStartupRejectsEventRunIDOwnedByAnotherTask(t *testing.T) {
	state := fenceHistoryState()
	otherID := taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAW")
	now := time.Unix(200, 0).UTC()
	stage := task.Execution
	state.Tasks[otherID] = task.Task{ID: otherID, Title: "other", OwnerID: "owner", Revision: 2, LastEventSeq: 2, Fence: 1,
		StagePlan: task.StagePlan{Stages: []task.Stage{task.Execution}, PolicyVersion: task.StagePlanPolicyVersion}, CurrentStage: &stage,
		Status: task.InProgress, CreatedAt: now, UpdatedAt: now.Add(time.Second)}
	state.Events = append(state.Events, task.DomainEvent{ID: task.EventID(otherID, 1), TaskID: otherID, EventSeq: 1,
		TaskRevision: 1, TaskFence: 0, Type: task.TaskCreated, CreatedAt: now}, task.DomainEvent{ID: task.EventID(otherID, 2), TaskID: otherID,
		RunID: "foreign-run", EventSeq: 2, TaskRevision: 2, TaskFence: 1, Type: task.TaskOwnershipTransferred, CreatedAt: now.Add(time.Second)})
	state.Runs["foreign-run"] = fsm.Run{TaskID: otherID, RunID: "foreign-run", Attempt: 1, Fence: 1,
		OwnerSession: "other", Worktree: "other", Deadline: now.Add(time.Hour), Status: fsm.Failed}
	state.Events[2].RunID = "foreign-run"
	requireStartupRejection(t, state, "missing or unrelated run")
}

func TestCanonicalStartupAcceptsFenceSnapshotsAcrossTransferAndRecovery(t *testing.T) {
	state := fenceHistoryState()
	id := state.Events[0].TaskID
	value := state.Tasks[id]
	value.Revision++
	value.LastEventSeq++
	value.Fence = 2
	value.UpdatedAt = value.UpdatedAt.Add(time.Second)
	state.Tasks[id] = value
	run := state.Runs["run-1"]
	run.Fence = 2
	run.OwnerSession = "executor-recovered"
	state.Runs[run.RunID] = run
	state.Events = append(state.Events, task.DomainEvent{ID: task.EventID(id, value.LastEventSeq), TaskID: id, RunID: run.RunID,
		EventSeq: value.LastEventSeq, TaskRevision: value.Revision, TaskFence: 2,
		Type: task.TaskOwnershipTransferred, CreatedAt: value.UpdatedAt})
	if err := ValidateCanonicalState(state); err != nil {
		t.Fatalf("valid startup history rejected: %v", err)
	}
}

func TestCanonicalStartupRejectsRunFenceDifferentFromLatestTransfer(t *testing.T) {
	previous := taskStateWithRoleRun(roles.Executor, 1)
	old := previous.Runs["run-1"]
	old.Status = fsm.Failed
	previous.Runs[old.RunID] = old
	state := ownershipTransferCandidate(t, previous, "run-review", roles.Reviewer, 1, 2)
	old = state.Runs["run-1"]
	old.Fence = 2 // valid relative to Task.Fence, wrong for this Run's own marker
	state.Runs[old.RunID] = old
	requireStartupRejection(t, state, "differs from latest transfer fence")
}

func TestCanonicalStartupRejectsTaskScopedRunWithoutTransferMarker(t *testing.T) {
	state := fenceHistoryState()
	id := state.Events[0].TaskID
	run := fsm.Run{TaskID: id, RunID: "run-unmarked", Attempt: 1, Fence: 1,
		OwnerSession: "agent", Worktree: "worktree", Deadline: time.Now().Add(time.Hour), Status: fsm.Failed}
	state.Runs[run.RunID] = run
	value := state.Tasks[id]
	value.Revision++
	value.LastEventSeq++
	value.UpdatedAt = value.UpdatedAt.Add(time.Second)
	state.Tasks[id] = value
	state.Events = append(state.Events, task.DomainEvent{ID: task.EventID(id, value.LastEventSeq), TaskID: id, RunID: run.RunID,
		EventSeq: value.LastEventSeq, TaskRevision: value.Revision, TaskFence: value.Fence, Type: "RUN_RETIRED", CreatedAt: value.UpdatedAt})
	requireStartupRejection(t, state, "has no ownership transfer event")
}
