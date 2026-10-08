package store

import (
	"coord/envelope"
	"coord/fsm"
	"coord/task"
	"coord/taskid"
	"errors"
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
		event := task.DomainEvent{ID: task.EventID(id, value.LastEventSeq), TaskID: id, RunID: "run-1", EventSeq: value.LastEventSeq, TaskRevision: value.Revision, Type: "OWNER_DECISION", ActorID: actor, CreatedAt: value.UpdatedAt}
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
		event := task.DomainEvent{ID: task.EventID(id, value.LastEventSeq), TaskID: id, RunID: "run-1", EventSeq: value.LastEventSeq, TaskRevision: value.Revision, Type: "OWNER_DECISION", CreatedAt: value.UpdatedAt}
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
		EventSeq: value.LastEventSeq, TaskRevision: value.Revision,
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
	value.UpdatedAt = value.UpdatedAt.Add(time.Second)
	state.Tasks[run.TaskID] = value
	state.Events = append(state.Events, task.DomainEvent{
		ID: task.EventID(run.TaskID, 2), TaskID: run.TaskID, RunID: run.RunID,
		EventSeq: 2, TaskRevision: 2, Type: "TASK_ASSIGNED", CreatedAt: value.UpdatedAt,
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
		ID: task.EventID(id, 3), TaskID: id, RunID: "run-2", EventSeq: 3, TaskRevision: 3,
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
	previous := taskState()
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
		ID: task.EventID(id, 2), TaskID: id, EventSeq: 2, TaskRevision: 2,
		Type: "OWNER_DECISION", CreatedAt: value.UpdatedAt,
	})
	if err := ValidateCanonicalState(next); err != nil {
		t.Fatalf("candidate state should be structurally valid: %v", err)
	}
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
		EventSeq: value.LastEventSeq, TaskRevision: value.Revision,
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
			EventSeq: value.LastEventSeq - 1, TaskRevision: value.Revision - 1,
			Type: "STALE_REVISION_EVENT", CreatedAt: value.UpdatedAt,
		},
		task.DomainEvent{
			ID: task.EventID(id, value.LastEventSeq), TaskID: id,
			EventSeq: value.LastEventSeq, TaskRevision: value.Revision,
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
	event := task.DomainEvent{ID: task.EventID(value.ID, value.LastEventSeq), TaskID: value.ID, RunID: "run-1", EventSeq: value.LastEventSeq, TaskRevision: value.Revision, Type: "WORK_STARTED", CreatedAt: value.UpdatedAt}
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
		EventSeq: value.LastEventSeq, TaskRevision: value.Revision, Type: "WORK_BLOCKED",
		CreatedAt: value.UpdatedAt,
	})
	if err := ValidateCanonicalState(next); err != nil {
		t.Fatalf("atomic Task/Run/Event candidate rejected: %v", err)
	}
	if err := ValidateCanonicalTransition(previous, next); err != nil {
		t.Fatalf("atomic Task/Run/Event transition rejected: %v", err)
	}
}

func taskState() CanonicalState {
	state := EmptyCanonicalState()
	id := taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	stage := task.Execution
	now := time.Unix(100, 0).UTC()
	state.Tasks[id] = task.Task{
		ID: id, Title: "Task", OwnerID: "owner", Revision: 1, LastEventSeq: 1,
		StagePlan:    task.StagePlan{Stages: []task.Stage{task.Execution}, PolicyVersion: task.StagePlanPolicyVersion},
		CurrentStage: &stage, Status: task.InProgress, Fence: 1, CreatedAt: now, UpdatedAt: now,
	}
	state.Events = append(state.Events, task.DomainEvent{
		ID: task.EventID(id, 1), TaskID: id, EventSeq: 1, TaskRevision: 1,
		Type: task.TaskCreated, ActorID: "owner", CreatedAt: now,
	})
	return state
}

func taskStateWithRun() CanonicalState {
	state := taskState()
	id := taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	value := state.Tasks[id]
	value.Revision, value.LastEventSeq = 2, 2
	value.UpdatedAt = value.UpdatedAt.Add(time.Second)
	state.Tasks[id] = value
	run := fsm.Run{
		Task: "legacy label", TaskID: id, RunID: "run-1", Attempt: 1, Fence: 1,
		OwnerSession: "executor", Worktree: "worktree", Deadline: value.UpdatedAt.Add(time.Hour), Status: fsm.Started,
	}
	state.Runs[run.RunID] = run
	state.Events = append(state.Events, task.DomainEvent{
		ID: task.EventID(id, 2), TaskID: id, RunID: run.RunID,
		EventSeq: 2, TaskRevision: 2, Type: "TASK_ASSIGNED", CreatedAt: value.UpdatedAt,
	})
	return state
}
