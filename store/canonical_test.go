package store

import (
	"coord/envelope"
	"coord/fsm"
	"coord/roles"
	"coord/task"
	"coord/taskid"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
)

type canonicalMemory struct {
	state     CanonicalState
	fail      bool
	lockErr   error
	loadCalls int
	saveCalls int
}

func (p *canonicalMemory) Lock() (func() error, error) {
	if p.lockErr != nil {
		return nil, p.lockErr
	}
	return func() error { return nil }, nil
}
func (p *canonicalMemory) Load() (CanonicalState, error) {
	p.loadCalls++
	return p.state, nil
}
func (p *canonicalMemory) Save(state CanonicalState) error {
	p.saveCalls++
	if p.fail {
		return errors.New("durable write failed")
	}
	p.state = state
	return nil
}

func TestApplyInboxCommandCommitsReceiptWithTaskEventAndDeduplicates(t *testing.T) {
	p := &canonicalMemory{state: taskStateWithRun()}
	s, err := NewCanonical(p)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	const actor, message = "owner-7", "cmd-1"
	id := taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	apply := func(state *CanonicalState) (InboxReceipt, error) {
		value := state.Tasks[id]
		value.Revision++
		value.LastEventSeq++
		value.UpdatedAt = value.UpdatedAt.Add(time.Second)
		state.Tasks[id] = value
		event := task.DomainEvent{ID: task.EventID(id, value.LastEventSeq), TaskID: id, RunID: "run-1", EventSeq: value.LastEventSeq, TaskRevision: value.Revision, TaskFence: value.Fence, Type: "OWNER_DECISION", ActorID: actor, CreatedAt: value.UpdatedAt}
		state.Events = append(state.Events, event)
		return InboxReceipt{ActorID: actor, MessageID: message, TaskID: id, RunID: "run-1", EventIDs: []string{event.ID}, ExpectedRevision: value.Revision - 1, CommittedRevision: value.Revision, RecordedAt: value.UpdatedAt}, nil
	}
	receipt, replayed, err := s.ApplyInboxCommand(actor, message, id, 2, apply)
	if err != nil || replayed {
		t.Fatalf("receipt=%+v replayed=%v err=%v", receipt, replayed, err)
	}
	if p.saveCalls != 1 || len(p.state.Inbox) != 1 {
		t.Fatalf("receipt not durably committed once: saves=%d inbox=%v", p.saveCalls, p.state.Inbox)
	}
	if receipt.ExpectedRevision != 2 || receipt.CommittedRevision != 3 {
		t.Fatalf("receipt revisions = %d -> %d, want 2 -> 3", receipt.ExpectedRevision, receipt.CommittedRevision)
	}
	called := false
	replayedReceipt, replayed, err := s.ApplyInboxCommand(actor, message, id, 2, func(*CanonicalState) (InboxReceipt, error) { called = true; return InboxReceipt{}, nil })
	if err != nil || !replayed || called || !reflect.DeepEqual(receipt, replayedReceipt) || p.saveCalls != 1 {
		t.Fatalf("duplicate command was reapplied: receipt=%+v replayed=%v called=%v saves=%d err=%v", replayedReceipt, replayed, called, p.saveCalls, err)
	}
}

func TestApplyInboxCommandRequiresSameCommitEventAndPreservesStateOnFailure(t *testing.T) {
	p := &canonicalMemory{state: taskStateWithRun()}
	s, err := NewCanonical(p)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	id := taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	oldEvent := p.state.Events[len(p.state.Events)-1]
	_, _, err = s.ApplyInboxCommand("owner-7", "cmd-old-event", id, 2, func(*CanonicalState) (InboxReceipt, error) {
		return InboxReceipt{ActorID: "owner-7", MessageID: "cmd-old-event", TaskID: id, RunID: "run-1", EventIDs: []string{oldEvent.ID}, ExpectedRevision: 2, CommittedRevision: 3, RecordedAt: time.Now()}, nil
	})
	if err == nil {
		t.Fatal("Inbox receipt referencing an older event was accepted")
	}
	if len(s.state.Inbox) != 0 || p.saveCalls != 0 {
		t.Fatal("rejected receipt mutated canonical state")
	}
	p.fail = true
	_, _, err = s.ApplyInboxCommand("owner-7", "cmd-commit-fail", id, 2, func(state *CanonicalState) (InboxReceipt, error) {
		value := state.Tasks[id]
		value.Revision++
		value.LastEventSeq++
		value.UpdatedAt = value.UpdatedAt.Add(time.Second)
		state.Tasks[id] = value
		event := task.DomainEvent{ID: task.EventID(id, value.LastEventSeq), TaskID: id, RunID: "run-1", EventSeq: value.LastEventSeq, TaskRevision: value.Revision, TaskFence: value.Fence, Type: "OWNER_DECISION", CreatedAt: value.UpdatedAt}
		state.Events = append(state.Events, event)
		return InboxReceipt{ActorID: "owner-7", MessageID: "cmd-commit-fail", TaskID: id, RunID: "run-1", EventIDs: []string{event.ID}, ExpectedRevision: value.Revision - 1, CommittedRevision: value.Revision, RecordedAt: value.UpdatedAt}, nil
	})
	if !errors.Is(err, ErrPersistence) {
		t.Fatalf("commit error=%v, want persistence failure", err)
	}
	if len(s.state.Inbox) != 0 {
		t.Fatal("failed durable commit published Inbox receipt")
	}
}

func TestApplyInboxCommandRejectsStaleExpectedRevision(t *testing.T) {
	p := &canonicalMemory{state: taskStateWithRun()}
	s, err := NewCanonical(p)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	id := taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	called := false
	_, _, err = s.ApplyInboxCommand("owner-7", "cmd-stale", id, 1, func(*CanonicalState) (InboxReceipt, error) {
		called = true
		return InboxReceipt{ActorID: "owner-7", MessageID: "cmd-stale", TaskID: id, ExpectedRevision: 1, CommittedRevision: 2, EventIDs: []string{"unused"}, RecordedAt: time.Now()}, nil
	})
	var conflict *RevisionConflictError
	if !errors.As(err, &conflict) || conflict.Expected != 1 || conflict.Actual != 2 {
		t.Fatalf("error = %v, want revision conflict 1 vs 2", err)
	}
	if called || p.saveCalls != 0 || len(s.state.Inbox) != 0 {
		t.Fatalf("stale command was handled or committed: called=%v saves=%d inbox=%d", called, p.saveCalls, len(s.state.Inbox))
	}
}

func TestCanonicalCommitFailureDoesNotPublishOrContinue(t *testing.T) {
	p := &canonicalMemory{state: EmptyCanonicalState(), fail: true}
	s, err := NewCanonical(p)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	run := fsm.Run{RunID: "run-1", Attempt: 1, Fence: 1, OwnerSession: "executor", Worktree: "worktree", Deadline: time.Now().Add(time.Hour), Status: fsm.Pending}
	if err := s.Update(func(runs map[string]fsm.Run) error { runs[run.RunID] = run; return nil }); !errors.Is(err, ErrPersistence) {
		t.Fatalf("first update error = %v, want persistence failure", err)
	}
	if _, ok := s.Get(run.RunID); ok {
		t.Fatal("failed durable commit published the Run in memory")
	}
	invoked := false
	err = s.Update(func(map[string]fsm.Run) error { invoked = true; return nil })
	if !errors.Is(err, ErrPersistence) || invoked {
		t.Fatalf("update after uncertain commit error=%v callback-invoked=%v", err, invoked)
	}
}

func TestCanonicalStateRejectsInvalidInboxOutboxAndSchema(t *testing.T) {
	state := EmptyCanonicalState()
	state.Inbox["receipt"] = InboxReceipt{}
	if err := ValidateCanonicalState(state); err == nil {
		t.Fatal("canonical state accepted an invalid Inbox receipt")
	}
	state = EmptyCanonicalState()
	state.Outbox["missing-run"] = OutboxQueue{}
	if err := ValidateCanonicalState(state); err == nil {
		t.Fatal("canonical state accepted Outbox for a missing Run")
	}
	state = EmptyCanonicalState()
	state.SchemaVersion++
	if err := ValidateCanonicalState(state); err == nil {
		t.Fatal("unsupported canonical schema was accepted")
	}
}

func TestCanonicalStateRejectsDanglingInboxEventReference(t *testing.T) {
	state := taskStateWithRun()
	id := taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	receipt := InboxReceipt{ActorID: "owner", MessageID: "cmd-1", TaskID: id, RunID: "run-1", EventIDs: []string{task.EventID(id, 99)}, ExpectedRevision: 2, CommittedRevision: 3, RecordedAt: time.Unix(200, 0).UTC()}
	state.Inbox[InboxKey(receipt.ActorID, receipt.MessageID)] = receipt
	if err := ValidateCanonicalState(state); err == nil {
		t.Fatal("Inbox receipt referencing a missing event was accepted")
	}
}

func TestCanonicalStartupRejectsInboxEventOutsideCommittedRevision(t *testing.T) {
	state := taskStateWithRun()
	id := taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	first := state.Events[1]
	last := task.DomainEvent{ID: task.EventID(id, 3), TaskID: id, RunID: "run-1", EventSeq: 3,
		TaskRevision: 3, TaskFence: 1, Type: "WORK_PROGRESS", CreatedAt: state.Tasks[id].UpdatedAt.Add(time.Second)}
	value := state.Tasks[id]
	value.Revision, value.LastEventSeq = 3, 3
	value.UpdatedAt = last.CreatedAt
	state.Tasks[id] = value
	state.Events = append(state.Events, last)
	receipt := InboxReceipt{ActorID: "owner", MessageID: "cmd-1", TaskID: id, RunID: "run-1",
		EventIDs: []string{first.ID, last.ID}, ExpectedRevision: 2, CommittedRevision: 3, RecordedAt: time.Unix(202, 0).UTC()}
	state.Inbox[InboxKey(receipt.ActorID, receipt.MessageID)] = receipt
	if err := ValidateCanonicalState(state); err == nil || !strings.Contains(err.Error(), "outside committed Task revision") {
		t.Fatalf("ValidateCanonicalState error=%v, want Inbox event revision rejection", err)
	}
}

func TestCanonicalStartupRejectsRoleSelectionNotMatchingLatestHistory(t *testing.T) {
	state := taskStateWithRoleRun(roles.Executor, 1)
	run := state.Runs["run-1"]
	selection := *run.RoleSelection
	selection.AgentID = "different-agent"
	run.RoleHistory = []roles.Selection{selection}
	state.Runs[run.RunID] = run
	if err := ValidateCanonicalState(state); err == nil || !strings.Contains(err.Error(), "selected role must match the latest RoleHistory entry") {
		t.Fatalf("ValidateCanonicalState error=%v, want RoleHistory mismatch rejection", err)
	}
}

func TestCanonicalStateRejectsOutboxReferenceToDifferentRunEvent(t *testing.T) {
	state := taskStateWithRun()
	id := taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	value := state.Tasks[id]
	value.Revision++
	value.LastEventSeq++
	value.UpdatedAt = value.UpdatedAt.Add(time.Second)
	state.Tasks[id] = value
	otherRun := fsm.Run{
		Task: "legacy label", TaskID: id, RunID: "run-2", Attempt: 1, Fence: 1,
		OwnerSession: "reviewer", Worktree: "worktree-review", Deadline: value.UpdatedAt.Add(time.Hour), Status: fsm.Failed,
	}
	state.Runs[otherRun.RunID] = otherRun
	otherRunEvent := task.DomainEvent{
		ID: task.EventID(id, value.LastEventSeq), TaskID: id, RunID: otherRun.RunID,
		EventSeq: value.LastEventSeq, TaskRevision: value.Revision, TaskFence: value.Fence,
		Type: "WORK_FAILED", CreatedAt: value.UpdatedAt,
	}
	state.Events = append(state.Events, otherRunEvent)
	now := value.UpdatedAt.Add(time.Second)
	e := envelope.Envelope{
		SchemaVersion: 1, MessageID: "outbox-run-1", Timestamp: now,
		From: "coord", To: "executor", Kind: envelope.Task, Task: "legacy label",
		Run: "run-1", Attempt: 1, Fence: 1,
		TargetSHA:   "0123456789012345678901234567890123456789",
		TaskPayload: &envelope.TaskPayload{Worktree: "worktree", Deadline: now.Add(time.Hour)},
	}
	state.Outbox["run-1"] = OutboxQueue{Messages: []OutboxMessage{{Envelope: e, EventID: otherRunEvent.ID}}}
	if err := ValidateCanonicalState(state); err == nil || !strings.Contains(err.Error(), "references missing or unrelated DomainEvent") {
		t.Fatalf("Outbox relation error=%v; want rejection of event owned by another Run", err)
	}
}

func TestCanonicalRunStorePersistsSingleTopLevelOutbox(t *testing.T) {
	p := &canonicalMemory{state: EmptyCanonicalState()}
	s, err := NewCanonical(p)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	now := time.Now().UTC()
	run := fsm.Run{RunID: "run-legacy", Attempt: 1, Fence: 1, OwnerSession: "executor", Worktree: "worktree", Deadline: now.Add(time.Hour), Status: fsm.Pending}
	if err := s.Update(func(runs map[string]fsm.Run) error { runs[run.RunID] = run; return nil }); err != nil {
		t.Fatal(err)
	}
	e := envelope.Envelope{SchemaVersion: 1, MessageID: "msg-1", Timestamp: now, From: "coord", To: "executor", Kind: envelope.Task, Task: "legacy task", Run: run.RunID, Attempt: 1, Fence: 1, TargetSHA: "0123456789012345678901234567890123456789", TaskPayload: &envelope.TaskPayload{Worktree: "worktree", Deadline: now.Add(time.Hour)}}
	if err := s.Update(func(runs map[string]fsm.Run) error {
		r := runs[run.RunID]
		r.Outbox = []envelope.Envelope{e}
		r.SendRetries = 2
		r.SendRetryAt = now.Add(time.Minute)
		runs[run.RunID] = r
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(p.state.Runs[run.RunID].Outbox) != 0 || !p.state.Runs[run.RunID].SendRetryAt.IsZero() || p.state.Runs[run.RunID].SendRetries != 0 {
		t.Fatal("Run duplicated durable Outbox fields")
	}
	queue := p.state.Outbox[run.RunID]
	if len(queue.Messages) != 1 || queue.Messages[0].Envelope.MessageID != "msg-1" || queue.Retries != 2 || !queue.RetryAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("top-level queue did not preserve envelope/retry state: %+v", queue)
	}
	loaded, ok := s.Get(run.RunID)
	if !ok || len(loaded.Outbox) != 1 || loaded.Outbox[0].MessageID != "msg-1" || loaded.SendRetries != 2 || !loaded.SendRetryAt.Equal(queue.RetryAt) {
		t.Fatalf("Run compatibility view did not hydrate Outbox: %+v, ok=%v", loaded, ok)
	}
}

func TestCanonicalStateRejectsInvalidRunFSMState(t *testing.T) {
	state := EmptyCanonicalState()
	state.Runs["run-1"] = fsm.Run{
		RunID: "run-1", Attempt: 1, Fence: 1, OwnerSession: "executor",
		Worktree: "worktree", Deadline: time.Now().Add(time.Hour), Status: "UNKNOWN",
	}
	if err := ValidateCanonicalState(state); err == nil {
		t.Fatal("canonical snapshot accepted an unknown Run status")
	}
}

func TestCanonicalStoreRefusesWithoutOwnershipBeforeLoad(t *testing.T) {
	want := errors.New("ownership unavailable")
	p := &canonicalMemory{lockErr: want}
	if state, err := NewCanonical(p); !errors.Is(err, want) || state != nil {
		t.Fatalf("store=%v error=%v; want ownership refusal", state, err)
	}
	if p.loadCalls != 0 {
		t.Fatalf("loaded canonical state %d times without ownership", p.loadCalls)
	}
}

func TestCanonicalStateValidatesTaskEventAndRunRelations(t *testing.T) {
	state := taskState()
	run := fsm.Run{Task: "legacy label stays distinct", TaskID: taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV"), RunID: "run-1", Attempt: 1, Fence: 1, OwnerSession: "executor", Worktree: "worktree", Deadline: time.Now().Add(time.Hour), Status: fsm.Started}
	state.Runs[run.RunID] = run
	value := state.Tasks[run.TaskID]
	value.Revision, value.LastEventSeq = 2, 2
	value.Fence = 1
	value.UpdatedAt = value.UpdatedAt.Add(time.Second)
	state.Tasks[run.TaskID] = value
	state.Events = append(state.Events, task.DomainEvent{
		ID: task.EventID(run.TaskID, 2), TaskID: run.TaskID, RunID: run.RunID,
		EventSeq: 2, TaskRevision: 2, TaskFence: 1, Type: task.TaskOwnershipTransferred, CreatedAt: value.UpdatedAt,
	})
	if err := ValidateCanonicalState(state); err != nil {
		t.Fatalf("valid explicit TaskID relation rejected: %v", err)
	}
	if state.Runs[run.RunID].Task != "legacy label stays distinct" {
		t.Fatal("legacy Run.Task was rewritten")
	}

	invalid := taskStateWithRun()
	run.TaskID = "task_01ARZ3NDEKTSV4RRFFQ69G5FAW"
	invalid.Runs[run.RunID] = run
	if err := ValidateCanonicalState(invalid); err == nil || !strings.Contains(err.Error(), "references missing or invalid TaskID") {
		t.Fatalf("Run missing-TaskID error=%v; want missing or invalid TaskID", err)
	}
}

func TestCanonicalStateRejectsMultipleActiveRunsPerTask(t *testing.T) {
	state := taskStateWithRun()
	id := taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	value := state.Tasks[id]
	value.Revision, value.LastEventSeq = 3, 3
	value.UpdatedAt = value.UpdatedAt.Add(time.Second)
	state.Tasks[id] = value
	state.Runs["run-2"] = fsm.Run{RunID: "run-2", TaskID: id, Attempt: 1, Fence: 1, OwnerSession: "executor-2", Worktree: "worktree-2", Deadline: value.UpdatedAt.Add(time.Hour), Status: fsm.Started}
	state.Events = append(state.Events, task.DomainEvent{
		ID: task.EventID(id, 3), TaskID: id, RunID: "run-2", EventSeq: 3, TaskRevision: 3, TaskFence: value.Fence,
		Type: "TASK_ASSIGNED", CreatedAt: value.UpdatedAt,
	})
	if err := ValidateCanonicalState(state); err == nil || !strings.Contains(err.Error(), "multiple active runs") {
		t.Fatalf("multiple active Run error=%v; want multiple active runs", err)
	}
}

func TestCanonicalStateRejectsEventSequenceGapAfterFirstEvent(t *testing.T) {
	state := taskStateWithRun()
	id := taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	state.Events[1].EventSeq = 3
	state.Events[1].ID = task.EventID(id, 3)
	if err := ValidateCanonicalState(state); err == nil || !strings.Contains(err.Error(), "event sequence has a gap or duplicate") {
		t.Fatalf("event gap error=%v; want event sequence gap", err)
	}
}

func TestCanonicalStateRejectsTerminalRunFenceAboveTaskFence(t *testing.T) {
	state := taskStateWithRun()
	run := state.Runs["run-1"]
	run.Status = fsm.Failed
	run.Fence = 2
	state.Runs[run.RunID] = run
	if err := ValidateCanonicalState(state); err == nil || !strings.Contains(err.Error(), "fence exceeds Task fence") {
		t.Fatalf("terminal Run fence error=%v; want fence exceeds Task fence", err)
	}
}

func TestCanonicalStateRejectsTaskRevisionGapInEventHistory(t *testing.T) {
	state := taskStateWithRun()
	id := taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	value := state.Tasks[id]
	value.Revision = 4
	state.Tasks[id] = value
	state.Events[1].TaskRevision = 4
	if err := ValidateCanonicalState(state); err == nil {
		t.Fatal("canonical state accepted a Task revision gap in event history")
	}
}

func TestCanonicalStateRejectsTaskRevisionAheadOfEventHistory(t *testing.T) {
	state := taskStateWithRun()
	id := taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	value := state.Tasks[id]
	value.Revision++
	state.Tasks[id] = value
	if err := ValidateCanonicalState(state); err == nil || !strings.Contains(err.Error(), "latest event revision differs from task revision") {
		t.Fatalf("Task/event revision error=%v; want latest event revision mismatch", err)
	}
}

func TestCanonicalStateRequiresLastEventSequenceToMatchHistoryLength(t *testing.T) {
	state := taskStateWithRun()
	id := taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	value := state.Tasks[id]
	value.LastEventSeq++
	state.Tasks[id] = value
	if err := ValidateCanonicalState(state); err == nil || !strings.Contains(err.Error(), "event history does not reach last event sequence") {
		t.Fatalf("LastEventSeq error=%v; want history length mismatch", err)
	}
}

func TestCanonicalTransitionRequiresRevisionEventAndMonotonicFence(t *testing.T) {
	previous := taskStateWithRun()
	next, err := copyCanonicalState(previous)
	if err != nil {
		t.Fatal(err)
	}
	id := taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	value := next.Tasks[id]
	value.Revision++
	value.LastEventSeq++
	value.Fence = 0
	value.UpdatedAt = value.UpdatedAt.Add(time.Second)
	next.Tasks[id] = value
	next.Events = append(next.Events, task.DomainEvent{
		ID: task.EventID(id, 2), TaskID: id, EventSeq: 2, TaskRevision: 2, TaskFence: value.Fence,
		Type: "OWNER_DECISION", CreatedAt: value.UpdatedAt,
	})
	if err := ValidateCanonicalTransition(previous, next); err == nil {
		t.Fatal("Task Fence decrease was accepted")
	}

	next, err = copyCanonicalState(previous)
	if err != nil {
		t.Fatal(err)
	}
	next.Tasks[id] = value
	if err := ValidateCanonicalTransition(previous, next); err == nil {
		t.Fatal("Task mutation without same-commit event was accepted")
	}
}

func TestCanonicalTransitionRequiresEventForRecoveryClaimChange(t *testing.T) {
	previous := taskStateWithRun()
	run := previous.Runs["run-1"]
	run.Status = fsm.Stalled
	run.ResumeStatus = fsm.Started
	run.RecoveryNeeded = true
	previous.Runs[run.RunID] = run
	if err := ValidateCanonicalState(previous); err != nil {
		t.Fatalf("valid stalled recovery state rejected: %v", err)
	}
	next, err := copyCanonicalState(previous)
	if err != nil {
		t.Fatal(err)
	}
	run = next.Runs["run-1"]
	run.RecoveryClaim = "claim-1"
	next.Runs[run.RunID] = run
	if err := ValidateCanonicalState(next); err != nil {
		t.Fatalf("valid claimed recovery state rejected: %v", err)
	}
	if err := ValidateCanonicalTransition(previous, next); err == nil || !strings.Contains(err.Error(), "domain mutation requires a Task revision increment") {
		t.Fatalf("RecoveryClaim-only transition error=%v; want Task revision and DomainEvent requirement", err)
	}
}

func TestCanonicalTransitionRejectsTaskMutationWithoutEvent(t *testing.T) {
	previous := taskStateWithRun()
	next, err := copyCanonicalState(previous)
	if err != nil {
		t.Fatal(err)
	}
	id := taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	value := next.Tasks[id]
	value.Title = "Changed without an event"
	value.Revision++
	next.Tasks[id] = value
	if err := ValidateCanonicalTransition(previous, next); err == nil || !strings.Contains(err.Error(), "mutation requires a same-commit event at its new revision") {
		t.Fatalf("Task transition error=%v; want same-commit event requirement", err)
	}
}

func TestCanonicalTransitionRejectsTaskMutationWithoutRevisionIncrement(t *testing.T) {
	previous := taskStateWithRun()
	next, err := copyCanonicalState(previous)
	if err != nil {
		t.Fatal(err)
	}
	id := taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	value := next.Tasks[id]
	value.Title = "Changed without a revision increment"
	value.LastEventSeq++
	value.UpdatedAt = value.UpdatedAt.Add(time.Second)
	next.Tasks[id] = value
	next.Events = append(next.Events, task.DomainEvent{
		ID: task.EventID(id, value.LastEventSeq), TaskID: id,
		EventSeq: value.LastEventSeq, TaskRevision: value.Revision, TaskFence: value.Fence,
		Type: "TASK_UPDATED", CreatedAt: value.UpdatedAt,
	})
	if err := ValidateCanonicalState(next); err != nil {
		t.Fatalf("candidate state should be structurally valid: %v", err)
	}
	if err := ValidateCanonicalTransition(previous, next); err == nil || !strings.Contains(err.Error(), "revision must increase by one per mutation") {
		t.Fatalf("Task transition error=%v; want missing revision increment", err)
	}
}

func TestCanonicalTransitionRejectsSameCommitEventAtPreviousRevision(t *testing.T) {
	previous := taskStateWithRun()
	next, err := copyCanonicalState(previous)
	if err != nil {
		t.Fatal(err)
	}
	id := taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	value := next.Tasks[id]
	value.Title = "Updated task"
	value.Revision++
	value.LastEventSeq += 2
	value.UpdatedAt = value.UpdatedAt.Add(time.Second)
	next.Tasks[id] = value
	next.Events = append(next.Events,
		task.DomainEvent{
			ID: task.EventID(id, value.LastEventSeq-1), TaskID: id,
			EventSeq: value.LastEventSeq - 1, TaskRevision: value.Revision - 1, TaskFence: value.Fence,
			Type: "STALE_REVISION_EVENT", CreatedAt: value.UpdatedAt,
		},
		task.DomainEvent{
			ID: task.EventID(id, value.LastEventSeq), TaskID: id,
			EventSeq: value.LastEventSeq, TaskRevision: value.Revision, TaskFence: value.Fence,
			Type: "TASK_UPDATED", CreatedAt: value.UpdatedAt,
		},
	)
	if err := ValidateCanonicalState(next); err != nil {
		t.Fatalf("candidate should pass snapshot-level continuity checks: %v", err)
	}
	if err := ValidateCanonicalTransition(previous, next); err == nil || !strings.Contains(err.Error(), "same-commit events must all reference new revision") {
		t.Fatalf("transition error=%v; want stale same-commit event rejection", err)
	}
}

func TestCanonicalStoreRejectsTaskMutationWithoutEvent(t *testing.T) {
	p := &canonicalMemory{state: taskStateWithRun()}
	s, err := NewCanonical(p)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	id := taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	err = s.UpdateCanonical(func(state *CanonicalState) error {
		value := state.Tasks[id]
		value.Title = "Changed without an event"
		value.Revision++
		state.Tasks[id] = value
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "latest event revision differs from task revision") {
		t.Fatalf("Task mutation error=%v; want snapshot event revision guard", err)
	}
	if p.saveCalls != 0 || s.state.Tasks[id].Title != "Task" {
		t.Fatal("Task mutation without event reached persistence or changed published state")
	}
}

func TestCanonicalTransitionRequiresEventForTaskRunMutation(t *testing.T) {
	previous := taskStateWithRun()
	next, err := copyCanonicalState(previous)
	if err != nil {
		t.Fatal(err)
	}
	run := next.Runs["run-1"]
	run.Status = fsm.Blocked
	next.Runs[run.RunID] = run
	if err := ValidateCanonicalTransition(previous, next); err == nil {
		t.Fatal("Task-scoped Run mutation without same-commit DomainEvent was accepted")
	}
}

func TestCanonicalTaskOutboxRequiresEventFromSameCommit(t *testing.T) {
	previous := taskStateWithRun()
	next, err := copyCanonicalState(previous)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(200, 0).UTC()
	e := envelope.Envelope{SchemaVersion: 1, MessageID: "task-msg", Timestamp: now, From: "coord", To: "executor", Kind: envelope.Task, Task: "legacy task", Run: "run-1", Attempt: 1, Fence: 1, TargetSHA: "0123456789012345678901234567890123456789", TaskPayload: &envelope.TaskPayload{Worktree: "worktree", Deadline: now.Add(time.Hour)}}
	priorEvent := previous.Events[len(previous.Events)-1]
	next.Outbox["run-1"] = OutboxQueue{Messages: []OutboxMessage{{Envelope: e, EventID: priorEvent.ID}}}
	if err := ValidateCanonicalState(next); err != nil {
		t.Fatalf("valid reference rejected: %v", err)
	}
	if err := ValidateCanonicalTransition(previous, next); err == nil {
		t.Fatal("Task-scoped Outbox using an older event was accepted")
	}
	value := next.Tasks[priorEvent.TaskID]
	value.Revision++
	value.LastEventSeq++
	value.UpdatedAt = value.UpdatedAt.Add(time.Second)
	next.Tasks[priorEvent.TaskID] = value
	event := task.DomainEvent{ID: task.EventID(value.ID, value.LastEventSeq), TaskID: value.ID, RunID: "run-1", EventSeq: value.LastEventSeq, TaskRevision: value.Revision, TaskFence: value.Fence, Type: "WORK_STARTED", CreatedAt: value.UpdatedAt}
	next.Events = append(next.Events, event)
	next.Outbox["run-1"] = OutboxQueue{Messages: []OutboxMessage{{Envelope: e, EventID: event.ID}}}
	if err := ValidateCanonicalState(next); err != nil {
		t.Fatalf("same-commit state invalid: %v", err)
	}
	if err := ValidateCanonicalTransition(previous, next); err != nil {
		t.Fatalf("same-commit Outbox/Event transition rejected: %v", err)
	}
}

func TestCanonicalOutboxExistingMessageIdentityAndOrderAreImmutable(t *testing.T) {
	now := time.Unix(300, 0).UTC()
	run := fsm.Run{RunID: "run-old", Attempt: 1, Fence: 1, OwnerSession: "agent", Worktree: "worktree", Deadline: now.Add(time.Hour), Status: fsm.Pending}
	makeEnvelope := func(id string, at time.Time) envelope.Envelope {
		return envelope.Envelope{SchemaVersion: 1, MessageID: id, Timestamp: at, From: "coord", To: "agent", Kind: envelope.Task, Task: "legacy", Run: run.RunID, Attempt: 1, Fence: 1, TargetSHA: "0123456789012345678901234567890123456789", TaskPayload: &envelope.TaskPayload{Worktree: "worktree", Deadline: now.Add(time.Hour)}}
	}
	previous := EmptyCanonicalState()
	previous.Runs[run.RunID] = run
	previous.Outbox[run.RunID] = OutboxQueue{Messages: []OutboxMessage{{Envelope: makeEnvelope("m1", now)}, {Envelope: makeEnvelope("m2", now.Add(time.Second))}}}
	if err := ValidateCanonicalState(previous); err != nil {
		t.Fatal(err)
	}
	next, err := copyCanonicalState(previous)
	if err != nil {
		t.Fatal(err)
	}
	next.Outbox[run.RunID].Messages[0], next.Outbox[run.RunID].Messages[1] = next.Outbox[run.RunID].Messages[1], next.Outbox[run.RunID].Messages[0]
	if err := ValidateCanonicalTransition(previous, next); err == nil {
		t.Fatal("outbox reordering was accepted")
	}
	next, err = copyCanonicalState(previous)
	if err != nil {
		t.Fatal(err)
	}
	next.Outbox[run.RunID].Messages[0].Envelope.Task = "changed"
	if err := ValidateCanonicalTransition(previous, next); err == nil {
		t.Fatal("existing message mutation was accepted")
	}
}

func TestCanonicalTransitionAllowsTransportOnlyRunUpdateWithoutDomainEvent(t *testing.T) {
	previous := taskStateWithRun()
	next, err := copyCanonicalState(previous)
	if err != nil {
		t.Fatal(err)
	}
	run := next.Runs["run-1"]
	run.LastHeartbeatAt = run.LastHeartbeatAt.Add(time.Second)
	next.Runs[run.RunID] = run
	if err := ValidateCanonicalState(next); err != nil {
		t.Fatalf("transport-only update made state invalid: %v", err)
	}
	if err := ValidateCanonicalTransition(previous, next); err != nil {
		t.Fatalf("transport-only update incorrectly required DomainEvent: %v", err)
	}
}

func TestCanonicalTransitionRequiresTaskRevisionAndEventForRunDomainChange(t *testing.T) {
	previous := taskStateWithRun()
	next, err := copyCanonicalState(previous)
	if err != nil {
		t.Fatal(err)
	}
	id := taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	value := next.Tasks[id]
	value.Revision++
	value.LastEventSeq++
	value.Status = task.Blocked
	value.UpdatedAt = value.UpdatedAt.Add(time.Second)
	next.Tasks[id] = value
	run := next.Runs["run-1"]
	run.Status = fsm.Blocked
	run.Blocker = "waiting for input"
	next.Runs[run.RunID] = run
	next.Events = append(next.Events, task.DomainEvent{
		ID: task.EventID(id, value.LastEventSeq), TaskID: id, RunID: run.RunID,
		EventSeq: value.LastEventSeq, TaskRevision: value.Revision, TaskFence: value.Fence, Type: "WORK_BLOCKED",
		CreatedAt: value.UpdatedAt,
	})
	if err := ValidateCanonicalState(next); err != nil {
		t.Fatalf("atomic Task/Run/Event candidate rejected: %v", err)
	}
	if err := ValidateCanonicalTransition(previous, next); err != nil {
		t.Fatalf("atomic Task/Run/Event transition rejected: %v", err)
	}
}

func TestCanonicalTransitionRejectsOwnerChangeWithoutTaskFenceBump(t *testing.T) {
	previous := claimedRecoveryState()
	next := ownershipTransferCandidate(t, previous, "run-1", roles.Executor, 1, 1)
	completeRecoveryRun(&next, "executor-new", 1)
	if err := ValidateCanonicalTransition(previous, next); err == nil || !strings.Contains(err.Error(), "increment Task.Fence by exactly one") {
		t.Fatalf("owner transfer error=%v; want required Task.Fence increment", err)
	}
}

func TestCanonicalTransitionRejectsRunFenceChangeWithoutRecovery(t *testing.T) {
	previous := taskStateWithRoleRun(roles.Executor, 1)
	next := ownershipTransferCandidate(t, previous, "run-1", roles.Executor, 1, 2)
	run := next.Runs["run-1"]
	run.Fence = 2
	next.Runs[run.RunID] = run
	rewriteLastEventType(&next, "TASK_UPDATED")
	if err := ValidateCanonicalTransition(previous, next); err == nil || !strings.Contains(err.Error(), "fence can change only during recovery ownership transfer") {
		t.Fatalf("Fence-only change error=%v; want ownership transfer requirement", err)
	}
}

func TestCanonicalTransitionRejectsFenceBumpWithoutOwnershipTransfer(t *testing.T) {
	previous := taskStateWithRoleRun(roles.Executor, 1)
	run := failedRun(previous.Runs["run-1"])
	previous.Runs[run.RunID] = run

	next, err := copyCanonicalState(previous)
	if err != nil {
		t.Fatal(err)
	}
	id := taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	taskValue := next.Tasks[id]
	taskValue.Fence++
	next.Tasks[id] = taskValue
	appendTaskDomainEvent(&next, id, run.RunID, "TASK_UPDATED")

	if err := ValidateCanonicalTransition(previous, next); err == nil {
		t.Fatalf("Task-only Fence bump error=%v; want ownership transfer rejection", err)
	}
}

func TestCanonicalTransitionRejectsFenceJump(t *testing.T) {
	previous := claimedRecoveryState()
	next := ownershipTransferCandidate(t, previous, "run-1", roles.Executor, 1, 3)
	completeRecoveryRun(&next, "executor-new", 3)
	if err := ValidateCanonicalTransition(previous, next); err == nil || !strings.Contains(err.Error(), "increment Task.Fence by exactly one") {
		t.Fatalf("Fence jump error=%v; want exactly-one increment", err)
	}
}

func TestCanonicalTransitionRequiresOwnershipTransferEvent(t *testing.T) {
	previous := claimedRecoveryState()
	next := ownershipTransferCandidate(t, previous, "run-1", roles.Executor, 1, 2)
	completeRecoveryRun(&next, "executor-new", 2)
	rewriteLastEventType(&next, "WORK_PROGRESS")
	if err := ValidateCanonicalTransition(previous, next); err == nil || !strings.Contains(err.Error(), "same-commit TASK_OWNERSHIP_TRANSFERRED event") {
		t.Fatalf("transfer without marker error=%v; want transfer event requirement", err)
	}
}

func TestCanonicalTransitionRequiresRunFenceToMatchTaskFence(t *testing.T) {
	previous := claimedRecoveryState()
	next := ownershipTransferCandidate(t, previous, "run-1", roles.Executor, 1, 2)
	completeRecoveryRun(&next, "executor-new", 1)
	if err := ValidateCanonicalTransition(previous, next); err == nil || !strings.Contains(err.Error(), "fence does not match Task") {
		t.Fatalf("Run fence mismatch error=%v; want Task fence comparison", err)
	}
}

func TestCanonicalTransitionRejectsSecondActiveWriter(t *testing.T) {
	previous := taskStateWithRoleRun(roles.Executor, 1)
	next := ownershipTransferCandidate(t, previous, "run-2", roles.Executor, 2, 2)
	run := next.Runs["run-2"]
	run.OwnerSession = "executor-2"
	next.Runs[run.RunID] = run
	if err := ValidateCanonicalTransition(previous, next); err == nil || !strings.Contains(err.Error(), "multiple active writer runs") {
		t.Fatalf("multiple writers error=%v; want single-writer rejection", err)
	}

	// Terminalizing the old Run in the same commit cannot stand in for stop
	// proof; the previous committed state must already show it as terminal.
	next.Runs["run-1"] = failedRun(previous.Runs["run-1"])
	if err := ValidateCanonicalTransition(previous, next); err == nil || !strings.Contains(err.Error(), "previous writer \"run-1\" to be terminal before this commit") {
		t.Fatalf("same-commit terminalization error=%v; want previous-snapshot terminal requirement", err)
	}
}

func TestCanonicalTransitionRejectsRecoveryWithoutStalledRecoveryClaim(t *testing.T) {
	previous := taskStateWithRoleRun(roles.Executor, 1)
	run := previous.Runs["run-1"]
	run.Status = fsm.Stalled
	run.ResumeStatus = fsm.Started
	run.RecoveryNeeded = true
	previous.Runs[run.RunID] = run
	next := ownershipTransferCandidate(t, previous, "run-1", roles.Executor, 1, 2)
	run = next.Runs["run-1"]
	setRunOwner(&run, "executor-new")
	run.Fence = 2
	run.Status = fsm.Pending
	run.ResumeStatus = ""
	run.RecoveryNeeded = false
	next.Runs[run.RunID] = run
	if err := ValidateCanonicalTransition(previous, next); err == nil || !strings.Contains(err.Error(), "recovery transfer requires stalled Run") {
		t.Fatalf("unclaimed recovery error=%v; want recovery claim requirement", err)
	}
}

func TestCanonicalTransitionRejectsRecoveryWithUnclearedState(t *testing.T) {
	for _, tc := range []struct {
		name           string
		recoveryNeeded bool
		recoveryClaim  string
	}{
		{name: "recovery needed remains", recoveryNeeded: true},
		{name: "claim remains", recoveryNeeded: true, recoveryClaim: "claim-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			previous := claimedRecoveryState()
			next := ownershipTransferCandidate(t, previous, "run-1", roles.Executor, 1, 2)
			run := next.Runs["run-1"]
			setRunOwner(&run, "executor-new")
			run.Fence = 2
			run.Status = fsm.Stalled
			run.ResumeStatus = fsm.Started
			run.RecoveryNeeded = tc.recoveryNeeded
			run.RecoveryClaim = tc.recoveryClaim
			next.Runs[run.RunID] = run
			if err := ValidateCanonicalState(next); err != nil {
				t.Fatalf("candidate state invalid: %v", err)
			}
			if err := ValidateCanonicalTransition(previous, next); err == nil || !strings.Contains(err.Error(), "recovery transfer requires stalled Run") {
				t.Fatalf("incomplete recovery error=%v; want recovery cleanup rejection", err)
			}
		})
	}
}

func TestCanonicalTransitionRejectsOwnerChangeWhenRunBecomesTerminal(t *testing.T) {
	previous := taskStateWithRoleRun(roles.Executor, 1)
	next, err := copyCanonicalState(previous)
	if err != nil {
		t.Fatal(err)
	}
	run := failedRun(next.Runs["run-1"])
	setRunOwner(&run, "executor-new")
	next.Runs[run.RunID] = run
	appendTaskDomainEvent(&next, run.TaskID, run.RunID, "WORK_FAILED")
	if err := ValidateCanonicalState(next); err != nil {
		t.Fatalf("candidate state invalid: %v", err)
	}
	if err := ValidateCanonicalTransition(previous, next); err == nil || !strings.Contains(err.Error(), "terminal Run") {
		t.Fatalf("terminal owner change error=%v; want rejection", err)
	}
}

func TestCanonicalTransitionRejectsTerminalRunReactivation(t *testing.T) {
	for _, changeOwner := range []bool{false, true} {
		name := "same owner"
		if changeOwner {
			name = "changed owner"
		}
		t.Run(name, func(t *testing.T) {
			previous := taskStateWithRoleRun(roles.Executor, 1)
			run := failedRun(previous.Runs["run-1"])
			previous.Runs[run.RunID] = run
			next, err := copyCanonicalState(previous)
			if err != nil {
				t.Fatal(err)
			}
			run = next.Runs[run.RunID]
			run.Status = fsm.Started
			if changeOwner {
				setRunOwner(&run, "executor-new")
			}
			next.Runs[run.RunID] = run
			appendTaskDomainEvent(&next, run.TaskID, run.RunID, "WORK_STARTED")
			if err := ValidateCanonicalState(next); err != nil {
				t.Fatalf("candidate state invalid: %v", err)
			}
			if err := ValidateCanonicalTransition(previous, next); err == nil || !strings.Contains(err.Error(), "terminal Run") {
				t.Fatalf("terminal Run reactivation error=%v; want rejection", err)
			}
		})
	}
}

func TestCanonicalTransitionRejectsTerminalRunFenceChangeWithoutRecovery(t *testing.T) {
	previous := taskStateWithRoleRun(roles.Executor, 1)
	priorExecutor := failedRun(previous.Runs["run-1"])
	previous.Runs[priorExecutor.RunID] = priorExecutor
	previous = ownershipTransferCandidate(t, previous, "run-review", roles.Reviewer, 1, 2)
	if err := ValidateCanonicalState(previous); err != nil {
		t.Fatalf("previous state invalid: %v", err)
	}

	next, err := copyCanonicalState(previous)
	if err != nil {
		t.Fatal(err)
	}
	priorExecutor = next.Runs["run-1"]
	priorExecutor.Fence = 2
	next.Runs[priorExecutor.RunID] = priorExecutor
	appendTaskDomainEvent(&next, priorExecutor.TaskID, priorExecutor.RunID, "FENCE_REWRITTEN")
	if err := ValidateCanonicalTransition(previous, next); err == nil {
		t.Fatalf("terminal Run fence change error=%v; want recovery-transfer-only rejection", err)
	}
}

func TestCanonicalTransitionRejectsActiveRunFenceChangeWithoutRecovery(t *testing.T) {
	previous := taskStateWithRoleRun(roles.Executor, 1)
	next, err := copyCanonicalState(previous)
	if err != nil {
		t.Fatal(err)
	}
	id := taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	taskValue := next.Tasks[id]
	taskValue.Fence = 2
	next.Tasks[id] = taskValue
	run := next.Runs["run-1"]
	run.Fence = 2
	next.Runs[run.RunID] = run
	appendTaskDomainEvent(&next, run.TaskID, run.RunID, "FENCE_REWRITTEN")
	if err := ValidateCanonicalTransition(previous, next); err == nil {
		t.Fatalf("active Run fence change error=%v; want recovery-transfer-only rejection", err)
	}
}

func TestCanonicalStateRejectsMismatchedSelectedRole(t *testing.T) {
	state := taskStateWithRoleRun(roles.Executor, 1)
	run := state.Runs["run-1"]
	run.RoleSelection.Profile.Role = roles.Reviewer
	state.Runs[run.RunID] = run
	if err := ValidateCanonicalState(state); err == nil || !strings.Contains(err.Error(), "selected profile role must match required role") {
		t.Fatalf("mismatched selection error=%v; want role consistency rejection", err)
	}
}

func TestCanonicalStateRejectsSelectedRoleSessionMismatch(t *testing.T) {
	state := taskStateWithRoleRun(roles.Executor, 1)
	run := state.Runs["run-1"]
	run.RoleSelection.Session = "other-session"
	state.Runs[run.RunID] = run
	if err := ValidateCanonicalState(state); err == nil || !strings.Contains(err.Error(), "selected role session must match Run owner session") {
		t.Fatalf("mismatched selection session error=%v; want owner consistency rejection", err)
	}
}

func TestCanonicalTransitionRejectsTaskRunRoleMutation(t *testing.T) {
	previous := taskStateWithRoleRun(roles.Executor, 1)
	next, err := copyCanonicalState(previous)
	if err != nil {
		t.Fatal(err)
	}
	run := next.Runs["run-1"]
	run.RequiredRole.Role = roles.Reviewer
	run.RoleSelection.Profile.Role = roles.Reviewer
	next.Runs[run.RunID] = run
	appendTaskDomainEvent(&next, run.TaskID, run.RunID, "ROLE_CHANGED")
	if err := ValidateCanonicalTransition(previous, next); err == nil || !strings.Contains(err.Error(), "role is immutable") {
		t.Fatalf("Run role mutation error=%v; want rejection", err)
	}
}

func TestCanonicalTransitionRequiresRoleSelectionHistory(t *testing.T) {
	for _, tc := range []struct {
		name           string
		appendNext     bool
		appendTwice    bool
		appendMismatch bool
		remove         bool
		wantErr        string
	}{
		{name: "changed agent without history append", wantErr: "RoleSelection change requires the same selection appended to RoleHistory"},
		{name: "changed agent with matching history append", appendNext: true},
		{name: "changed agent with two history entries", appendNext: true, appendTwice: true, wantErr: "RoleSelection change requires the same selection appended to RoleHistory"},
		{name: "changed agent with mismatching history entry", appendNext: true, appendMismatch: true, wantErr: "RoleSelection change requires the same selection appended to RoleHistory"},
		{name: "remove selection", remove: true, wantErr: "selected role is immutable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			previous := taskStateWithRoleRun(roles.Executor, 1)
			run := previous.Runs["run-1"]
			original := *run.RoleSelection
			run.RoleHistory = []roles.Selection{original}
			previous.Runs[run.RunID] = run

			next, err := copyCanonicalState(previous)
			if err != nil {
				t.Fatal(err)
			}
			run = next.Runs[run.RunID]
			switch {
			case tc.remove:
				run.RoleSelection = nil
			default:
				changed := original
				changed.AgentID = "agent-replacement"
				run.RoleSelection = &changed
				if tc.appendNext {
					appended := changed
					if tc.appendMismatch {
						appended.AgentID = "different-agent"
					}
					run.RoleHistory = append(run.RoleHistory, appended)
					if tc.appendTwice {
						run.RoleHistory = append(run.RoleHistory, changed)
					}
				}
			}
			next.Runs[run.RunID] = run
			appendTaskDomainEvent(&next, run.TaskID, run.RunID, "ROLE_SELECTION_CHANGED")
			if tc.wantErr == "" {
				if err := ValidateCanonicalState(next); err != nil {
					t.Fatalf("candidate state invalid: %v", err)
				}
			}
			transitionErr := ValidateCanonicalTransition(previous, next)
			if tc.wantErr != "" {
				if transitionErr == nil || !strings.Contains(transitionErr.Error(), tc.wantErr) {
					t.Fatalf("RoleSelection transition error=%v; want %q", transitionErr, tc.wantErr)
				}
				return
			}
			if transitionErr != nil {
				t.Fatalf("RoleSelection change with matching history append rejected: %v", transitionErr)
			}
		})
	}
}

func TestCanonicalTransitionRejectsTransferMarkerWithoutTransfer(t *testing.T) {
	previous := taskStateWithRoleRun(roles.Executor, 1)
	next, err := copyCanonicalState(previous)
	if err != nil {
		t.Fatal(err)
	}
	run := next.Runs["run-1"]
	run.Status = fsm.Blocked
	run.Blocker = "waiting"
	next.Runs[run.RunID] = run
	appendTaskDomainEvent(&next, run.TaskID, run.RunID, task.TaskOwnershipTransferred)
	if err := ValidateCanonicalTransition(previous, next); err == nil {
		t.Fatalf("unpaired transfer marker error=%v; want rejection", err)
	}
}

func TestCanonicalTransitionRejectsTransferMarkerForWrongRun(t *testing.T) {
	previous := taskStateWithRoleRun(roles.Executor, 1)
	prior := previous.Runs["run-1"]
	prior.Status = fsm.Failed
	previous.Runs[prior.RunID] = prior
	next := ownershipTransferCandidate(t, previous, "run-review", roles.Reviewer, 1, 2)
	next.Events[len(next.Events)-1].RunID = "run-1"
	taskValue := next.Tasks[prior.TaskID]
	taskValue.LastEventSeq++
	taskValue.UpdatedAt = taskValue.UpdatedAt.Add(time.Second)
	next.Tasks[taskValue.ID] = taskValue
	next.Events = append(next.Events, task.DomainEvent{
		ID: task.EventID(taskValue.ID, taskValue.LastEventSeq), TaskID: taskValue.ID,
		RunID: "run-review", EventSeq: taskValue.LastEventSeq, TaskRevision: taskValue.Revision, TaskFence: taskValue.Fence,
		Type: "TASK_ASSIGNED", CreatedAt: taskValue.UpdatedAt,
	})
	if err := ValidateCanonicalTransition(previous, next); err == nil {
		t.Fatalf("wrong-run transfer marker error=%v; want rejection", err)
	}
}

func TestCanonicalTransitionRejectsExtraTransferMarker(t *testing.T) {
	previous := taskStateWithRoleRun(roles.Executor, 1)
	prior := previous.Runs["run-1"]
	prior.Status = fsm.Failed
	previous.Runs[prior.RunID] = prior
	next := ownershipTransferCandidate(t, previous, "run-review", roles.Reviewer, 1, 2)
	taskValue := next.Tasks[prior.TaskID]
	taskValue.LastEventSeq++
	taskValue.UpdatedAt = taskValue.UpdatedAt.Add(time.Second)
	next.Tasks[taskValue.ID] = taskValue
	next.Events = append(next.Events, task.DomainEvent{
		ID: task.EventID(taskValue.ID, taskValue.LastEventSeq), TaskID: taskValue.ID,
		RunID: "run-review", EventSeq: taskValue.LastEventSeq, TaskRevision: taskValue.Revision, TaskFence: taskValue.Fence,
		Type: task.TaskOwnershipTransferred, CreatedAt: taskValue.UpdatedAt,
	})
	if err := ValidateCanonicalTransition(previous, next); err == nil {
		t.Fatalf("extra transfer marker error=%v; want exactly-one rejection", err)
	}
}

func TestCanonicalTransitionRoleHistoryIsAppendOnly(t *testing.T) {
	selection := func(agent string, role roles.Role) roles.Selection {
		return roles.Selection{AgentID: agent, Session: "executor", Profile: roles.Profile{Role: role}}
	}
	for _, tc := range []struct {
		name   string
		change func([]roles.Selection) []roles.Selection
	}{
		{name: "rewrite prefix entry", change: func(history []roles.Selection) []roles.Selection { history[0].AgentID = "rewritten"; return history }},
		{name: "rewrite prefix role", change: func(history []roles.Selection) []roles.Selection {
			history[0].Profile.Role = roles.Reviewer
			return history
		}},
		{name: "remove entry", change: func(history []roles.Selection) []roles.Selection {
			return []roles.Selection{history[0], history[2], selection("agent-d", roles.Executor)}
		}},
		{name: "shorten history", change: func(history []roles.Selection) []roles.Selection { return history[:2] }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			previous := taskStateWithRoleRun(roles.Executor, 1)
			run := previous.Runs["run-1"]
			run.RoleHistory = []roles.Selection{selection("agent-a", roles.Executor), selection("agent-b", roles.Executor), selection("agent-c", roles.Executor)}
			selected := selection("agent-c", roles.Executor)
			run.RoleSelection = &selected
			previous.Runs[run.RunID] = run
			next, err := copyCanonicalState(previous)
			if err != nil {
				t.Fatal(err)
			}
			run = next.Runs[run.RunID]
			run.RoleHistory = tc.change(run.RoleHistory)
			next.Runs[run.RunID] = run
			appendTaskDomainEvent(&next, run.TaskID, run.RunID, "ROLE_HISTORY_UPDATED")
			if err := ValidateCanonicalTransition(previous, next); err == nil || !strings.Contains(err.Error(), "RoleHistory is append-only") {
				t.Fatalf("RoleHistory mutation error=%v; want append-only rejection", err)
			}
		})
	}

	t.Run("append at end", func(t *testing.T) {
		previous := taskStateWithRoleRun(roles.Executor, 1)
		run := previous.Runs["run-1"]
		selected := selection("agent-a", roles.Executor)
		run.RoleSelection = &selected
		run.RoleHistory = []roles.Selection{selected}
		previous.Runs[run.RunID] = run
		next, err := copyCanonicalState(previous)
		if err != nil {
			t.Fatal(err)
		}
		run = next.Runs[run.RunID]
		selected = selection("agent-b", roles.Executor)
		run.RoleSelection = &selected
		run.RoleHistory = append(run.RoleHistory, selected)
		next.Runs[run.RunID] = run
		appendTaskDomainEvent(&next, run.TaskID, run.RunID, "ROLE_HISTORY_UPDATED")
		if err := ValidateCanonicalState(next); err != nil {
			t.Fatalf("candidate state invalid: %v", err)
		}
		if err := ValidateCanonicalTransition(previous, next); err != nil {
			t.Fatalf("append-only RoleHistory update rejected: %v", err)
		}
	})
}

func TestCanonicalTransitionRejectsFenceOverflow(t *testing.T) {
	previous := taskStateWithRoleRun(roles.Executor, 1)
	id := taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	taskValue := previous.Tasks[id]
	taskValue.Fence = math.MaxUint64
	previous.Tasks[id] = taskValue
	run := previous.Runs["run-1"]
	run.Fence = math.MaxUint64
	run.Status = fsm.Stalled
	run.ResumeStatus = fsm.Started
	run.RecoveryNeeded = true
	run.RecoveryClaim = "claim"
	previous.Runs[run.RunID] = run
	next := ownershipTransferCandidate(t, previous, "run-1", roles.Executor, 1, math.MaxUint64)
	run = next.Runs["run-1"]
	setRunOwner(&run, "executor-new")
	run.Status = fsm.Pending
	run.ResumeStatus = ""
	run.RecoveryNeeded = false
	run.RecoveryClaim = ""
	next.Runs[run.RunID] = run
	if err := ValidateCanonicalTransition(previous, next); !errors.Is(err, ErrFenceExhausted) {
		t.Fatalf("Fence overflow error=%v; want ErrFenceExhausted", err)
	}
}

func TestCanonicalTransitionFirstAssignmentIncrementsTaskFence(t *testing.T) {
	previous := taskState()
	id := taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	taskValue := previous.Tasks[id]
	taskValue.Fence = 0
	previous.Tasks[id] = taskValue
	next := ownershipTransferCandidate(t, previous, "run-1", roles.Executor, 1, 1)
	if err := ValidateCanonicalState(next); err != nil {
		t.Fatalf("first assignment candidate invalid: %v", err)
	}
	if err := ValidateCanonicalTransition(previous, next); err != nil {
		t.Fatalf("first assignment rejected: %v", err)
	}
}

func TestCanonicalTransitionRecoveryPreservesAttemptAndIncrementsTaskFence(t *testing.T) {
	previous := claimedRecoveryState()
	next := ownershipTransferCandidate(t, previous, "run-1", roles.Executor, 1, 2)
	completeRecoveryRun(&next, "executor-new", 2)
	if err := ValidateCanonicalTransition(previous, next); err != nil {
		t.Fatalf("valid recovery transfer rejected: %v", err)
	}
}

func TestCanonicalTransitionRoleHandoffPreservesWorkflowAttempt(t *testing.T) {
	previous := taskStateWithRoleRun(roles.Executor, 1)
	prior := previous.Runs["run-1"]
	prior.Status = fsm.Failed
	previous.Runs[prior.RunID] = prior
	next := ownershipTransferCandidate(t, previous, "run-review", roles.Reviewer, 1, 2)
	if err := ValidateCanonicalState(next); err != nil {
		t.Fatalf("review handoff candidate invalid: %v", err)
	}
	if err := ValidateCanonicalTransition(previous, next); err != nil {
		t.Fatalf("role handoff with same Attempt rejected: %v", err)
	}
	wrongAttempt := ownershipTransferCandidate(t, previous, "run-review-wrong-attempt", roles.Reviewer, 2, 2)
	if err := ValidateCanonicalTransition(previous, wrongAttempt); err == nil || !strings.Contains(err.Error(), "must preserve the current Executor Attempt") {
		t.Fatalf("review handoff with a new Attempt error=%v; want same-pass Attempt enforcement", err)
	}
}

func TestCanonicalTransitionReworkIncrementsAttemptAndFence(t *testing.T) {
	previous := taskStateWithRoleRun(roles.Executor, 1)
	prior := previous.Runs["run-1"]
	prior.Status = fsm.Failed
	previous.Runs[prior.RunID] = prior
	next := ownershipTransferCandidate(t, previous, "run-rework", roles.Executor, 2, 2)
	if err := ValidateCanonicalState(next); err != nil {
		t.Fatalf("rework candidate invalid: %v", err)
	}
	if err := ValidateCanonicalTransition(previous, next); err != nil {
		t.Fatalf("rework with next Attempt rejected: %v", err)
	}
	wrongAttempt := ownershipTransferCandidate(t, previous, "run-rework-wrong-attempt", roles.Executor, 1, 2)
	if err := ValidateCanonicalTransition(previous, wrongAttempt); err == nil || !strings.Contains(err.Error(), "must use the next Attempt") {
		t.Fatalf("rework without increment error=%v; want next Attempt enforcement", err)
	}
}

func TestCanonicalTransitionRetryPreservesAttemptAndTaskFence(t *testing.T) {
	previous := taskStateWithRoleRun(roles.Executor, 1)
	next, err := copyCanonicalState(previous)
	if err != nil {
		t.Fatal(err)
	}
	run := next.Runs["run-1"]
	run.Retries++
	run.RetryAt = run.RetryAt.Add(time.Second)
	next.Runs[run.RunID] = run
	if err := ValidateCanonicalTransition(previous, next); err != nil {
		t.Fatalf("retry without Attempt or Task.Fence change rejected: %v", err)
	}
	changedAttempt := next.Runs["run-1"]
	changedAttempt.Attempt++
	next.Runs[changedAttempt.RunID] = changedAttempt
	if err := ValidateCanonicalTransition(previous, next); err == nil || !strings.Contains(err.Error(), "Attempt is immutable") {
		t.Fatalf("Attempt mutation error=%v; want immutable Run Attempt", err)
	}
}

func failedRun(run fsm.Run) fsm.Run {
	run.Status = fsm.Failed
	run.ResumeStatus = ""
	run.RecoveryNeeded = false
	run.RecoveryClaim = ""
	return run
}

func taskStateWithRoleRun(role roles.Role, attempt uint64) CanonicalState {
	state := taskStateWithRun()
	run := state.Runs["run-1"]
	run.Attempt = attempt
	run.RequiredRole = roles.Requirement{Role: role}
	run.RoleSelection = &roles.Selection{AgentID: "agent-" + strings.ToLower(string(role)), Session: run.OwnerSession, Profile: roles.Profile{Role: role}}
	run.RoleHistory = []roles.Selection{*run.RoleSelection}
	state.Runs[run.RunID] = run
	return state
}

func claimedRecoveryState() CanonicalState {
	state := taskStateWithRoleRun(roles.Executor, 1)
	run := state.Runs["run-1"]
	run.Status = fsm.Stalled
	run.ResumeStatus = fsm.Started
	run.RecoveryNeeded = true
	run.RecoveryClaim = "claim-1"
	state.Runs[run.RunID] = run
	return state
}

func completeRecoveryRun(state *CanonicalState, owner string, fence uint64) {
	run := state.Runs["run-1"]
	setRunOwner(&run, owner)
	run.Fence = fence
	run.Status = fsm.Pending
	run.ResumeStatus = ""
	run.RecoveryNeeded = false
	run.RecoveryClaim = ""
	state.Runs[run.RunID] = run
}

func setRunOwner(run *fsm.Run, owner string) {
	run.OwnerSession = owner
	if run.RoleSelection != nil {
		run.RoleSelection.Session = owner
		run.RoleHistory = append(run.RoleHistory, *run.RoleSelection)
	}
}

func ownershipTransferCandidate(t *testing.T, previous CanonicalState, runID string, role roles.Role, attempt, fence uint64) CanonicalState {
	t.Helper()
	next, err := copyCanonicalState(previous)
	if err != nil {
		t.Fatal(err)
	}
	id := taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	taskValue := next.Tasks[id]
	taskValue.Revision++
	taskValue.LastEventSeq++
	taskValue.Fence = fence
	taskValue.UpdatedAt = taskValue.UpdatedAt.Add(time.Second)
	next.Tasks[id] = taskValue
	if _, exists := next.Runs[runID]; !exists {
		owner := "agent-" + strings.ToLower(string(role))
		selection := roles.Selection{AgentID: owner, Session: owner, Profile: roles.Profile{Role: role}}
		next.Runs[runID] = fsm.Run{
			Task: "legacy label", TaskID: id, RunID: runID, Attempt: attempt, Fence: fence,
			OwnerSession: owner, Worktree: "worktree", Deadline: taskValue.UpdatedAt.Add(time.Hour), Status: fsm.Pending,
			RequiredRole: roles.Requirement{Role: role}, RoleSelection: &selection, RoleHistory: []roles.Selection{selection},
		}
	}
	next.Events = append(next.Events, task.DomainEvent{
		ID: task.EventID(id, taskValue.LastEventSeq), TaskID: id, RunID: runID,
		EventSeq: taskValue.LastEventSeq, TaskRevision: taskValue.Revision, TaskFence: taskValue.Fence,
		Type: task.TaskOwnershipTransferred, CreatedAt: taskValue.UpdatedAt,
	})
	return next
}

func rewriteLastEventType(state *CanonicalState, eventType task.EventType) {
	state.Events[len(state.Events)-1].Type = eventType
}

func appendTaskDomainEvent(state *CanonicalState, taskID taskid.ID, runID string, eventType task.EventType) {
	taskValue := state.Tasks[taskID]
	taskValue.Revision++
	taskValue.LastEventSeq++
	taskValue.UpdatedAt = taskValue.UpdatedAt.Add(time.Second)
	state.Tasks[taskID] = taskValue
	state.Events = append(state.Events, task.DomainEvent{
		ID: task.EventID(taskID, taskValue.LastEventSeq), TaskID: taskID, RunID: runID,
		EventSeq: taskValue.LastEventSeq, TaskRevision: taskValue.Revision, TaskFence: taskValue.Fence,
		Type: eventType, CreatedAt: taskValue.UpdatedAt,
	})
}

func taskState() CanonicalState {
	state := EmptyCanonicalState()
	id := taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	stage := task.Execution
	now := time.Unix(100, 0).UTC()
	state.Tasks[id] = task.Task{
		ID: id, Title: "Task", OwnerID: "owner", Revision: 1, LastEventSeq: 1,
		StagePlan:    task.StagePlan{Stages: []task.Stage{task.Execution}, PolicyVersion: task.StagePlanPolicyVersion},
		CurrentStage: &stage, Status: task.InProgress, Fence: 0, CreatedAt: now, UpdatedAt: now,
	}
	state.Events = append(state.Events, task.DomainEvent{
		ID: task.EventID(id, 1), TaskID: id, EventSeq: 1, TaskRevision: 1, TaskFence: 0,
		Type: task.TaskCreated, ActorID: "owner", CreatedAt: now,
	})
	return state
}

func taskStateWithRun() CanonicalState {
	state := taskState()
	id := taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	value := state.Tasks[id]
	value.Revision, value.LastEventSeq = 2, 2
	value.Fence = 1
	value.UpdatedAt = value.UpdatedAt.Add(time.Second)
	state.Tasks[id] = value
	run := fsm.Run{
		Task: "legacy label", TaskID: id, RunID: "run-1", Attempt: 1, Fence: 1,
		OwnerSession: "executor", Worktree: "worktree", Deadline: value.UpdatedAt.Add(time.Hour), Status: fsm.Started,
	}
	state.Runs[run.RunID] = run
	state.Events = append(state.Events, task.DomainEvent{
		ID: task.EventID(id, 2), TaskID: id, RunID: run.RunID,
		EventSeq: 2, TaskRevision: 2, TaskFence: 1, Type: task.TaskOwnershipTransferred, CreatedAt: value.UpdatedAt,
	})
	return state
}
