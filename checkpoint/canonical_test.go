package checkpoint

import (
	"bytes"
	"coord/envelope"
	"coord/fsm"
	"coord/store"
	"coord/task"
	"coord/taskid"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type countingCanonicalFile struct {
	CanonicalFile
	saves int
}

func (f *countingCanonicalFile) Save(state store.CanonicalState) error {
	f.saves++
	return f.CanonicalFile.Save(state)
}

type uncertainCanonicalFile struct {
	CanonicalFile
	failAfterWrite bool
}

func (f uncertainCanonicalFile) Load() (store.CanonicalState, error) {
	data, err := os.ReadFile(f.Path)
	if os.IsNotExist(err) {
		return store.EmptyCanonicalState(), nil
	}
	if err != nil {
		return store.CanonicalState{}, err
	}
	var state store.CanonicalState
	if err := json.Unmarshal(data, &state); err != nil {
		return store.CanonicalState{}, err
	}
	return state, nil
}

func (f *uncertainCanonicalFile) Save(state store.CanonicalState) error {
	if err := f.CanonicalFile.Save(state); err != nil {
		return err
	}
	if f.failAfterWrite {
		f.failAfterWrite = false
		return errors.New("commit outcome uncertain after durable write")
	}
	return nil
}

func TestCanonicalCommitFailureRequiresReopenAndRevalidatesDurableState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "canonical.json")
	file := CanonicalFile{Path: path}
	id := taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	now := time.Unix(100, 0).UTC()
	stage := task.Execution
	state := store.EmptyCanonicalState()
	state.Tasks[id] = task.Task{ID: id, Title: "before", OwnerID: "owner", Revision: 1, LastEventSeq: 1,
		StagePlan:    task.StagePlan{Stages: []task.Stage{task.Execution}, PolicyVersion: task.StagePlanPolicyVersion},
		CurrentStage: &stage, Status: task.Pending, CreatedAt: now, UpdatedAt: now}
	state.Events = []task.DomainEvent{{ID: task.EventID(id, 1), TaskID: id, EventSeq: 1, TaskRevision: 1,
		Type: task.TaskCreated, ActorID: "owner", CreatedAt: now}}
	if err := file.Save(state); err != nil {
		t.Fatal(err)
	}
	persistence := &uncertainCanonicalFile{CanonicalFile: file, failAfterWrite: true}
	s, err := store.NewCanonical(persistence)
	if err != nil {
		t.Fatal(err)
	}
	err = s.UpdateCanonical(func(next *store.CanonicalState) error {
		value := next.Tasks[id]
		value.Title = "possibly committed"
		value.Revision++
		value.LastEventSeq++
		value.UpdatedAt = now.Add(time.Second)
		next.Tasks[id] = value
		next.Events = append(next.Events, task.DomainEvent{ID: task.EventID(id, value.LastEventSeq), TaskID: id,
			EventSeq: value.LastEventSeq, TaskRevision: value.Revision, TaskFence: value.Fence,
			Type: "TASK_UPDATED", ActorID: "owner", CreatedAt: value.UpdatedAt})
		return nil
	})
	if !errors.Is(err, store.ErrPersistence) {
		t.Fatalf("uncertain commit error=%v, want ErrPersistence", err)
	}
	called := false
	if err := s.UpdateCanonical(func(*store.CanonicalState) error { called = true; return nil }); !errors.Is(err, store.ErrPersistence) || called {
		t.Fatalf("failed handle accepted UpdateCanonical: err=%v callback=%v", err, called)
	}
	if err := s.Update(func(map[string]fsm.Run) error { called = true; return nil }); !errors.Is(err, store.ErrPersistence) || called {
		t.Fatalf("failed handle accepted RunStore.Update: err=%v callback=%v", err, called)
	}
	if _, _, err := s.ApplyInboxCommand("owner", "cmd", id, 2, func(*store.CanonicalState) (store.InboxReceipt, error) {
		called = true
		return store.InboxReceipt{}, nil
	}); !errors.Is(err, store.ErrPersistence) || called {
		t.Fatalf("failed handle accepted ApplyInboxCommand: err=%v callback=%v", err, called)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	persisted, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(persisted, &document); err != nil {
		t.Fatal(err)
	}
	document["tasks"].(map[string]any)[string(id)].(map[string]any)["last_event_seq"] = float64(1)
	corrupt, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, corrupt, 0600); err != nil {
		t.Fatal(err)
	}
	if reopened, err := store.NewCanonical(persistence); err == nil {
		_ = reopened.Close()
		t.Fatal("reopen accepted durable canonical state with broken Event reference")
	}
	if err := os.WriteFile(path, persisted, 0600); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.NewCanonical(persistence)
	if err != nil {
		t.Fatalf("reopen rejected the durably committed canonical state: %v", err)
	}
	defer func() { _ = reopened.Close() }()
	loaded, err := reopened.CanonicalSnapshot()
	if err != nil || loaded.Tasks[id].Title != "possibly committed" || loaded.Tasks[id].Revision != 2 {
		t.Fatalf("reopen did not load the uncertain durable commit: task=%+v err=%v", loaded.Tasks[id], err)
	}
	called = false
	if err := s.UpdateCanonical(func(*store.CanonicalState) error { called = true; return nil }); err == nil || called {
		t.Fatalf("old handle became writable after close/reopen: err=%v callback=%v", err, called)
	}
	if err := reopened.UpdateCanonical(func(next *store.CanonicalState) error {
		value := next.Tasks[id]
		value.Title = "updated after reopen"
		value.Revision++
		value.LastEventSeq++
		value.UpdatedAt = now.Add(2 * time.Second)
		next.Tasks[id] = value
		next.Events = append(next.Events, task.DomainEvent{ID: task.EventID(id, value.LastEventSeq), TaskID: id,
			EventSeq: value.LastEventSeq, TaskRevision: value.Revision, TaskFence: value.Fence,
			Type: "TASK_UPDATED", ActorID: "owner", CreatedAt: value.UpdatedAt})
		return nil
	}); err != nil {
		t.Fatalf("mutation through reopened store failed: %v", err)
	}
}

func TestCanonicalTransitionCommitsStateEventOutboxAtomically(t *testing.T) {
	path := filepath.Join(t.TempDir(), "canonical.json")
	file := CanonicalFile{Path: path}
	id := taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	now := time.Unix(100, 0).UTC()
	stage := task.Execution
	state := store.EmptyCanonicalState()
	state.Tasks[id] = task.Task{ID: id, Title: "task", OwnerID: "owner", Revision: 2, LastEventSeq: 2, Fence: 1,
		StagePlan:    task.StagePlan{Stages: []task.Stage{task.Execution}, PolicyVersion: task.StagePlanPolicyVersion},
		CurrentStage: &stage, Status: task.InProgress, CreatedAt: now, UpdatedAt: now.Add(time.Second)}
	state.Runs["run-1"] = fsm.Run{Task: "legacy label", TaskID: id, RunID: "run-1", Attempt: 1, Fence: 1,
		OwnerSession: "executor", Worktree: "worktree", Deadline: now.Add(time.Hour), Status: fsm.Started}
	state.Events = []task.DomainEvent{
		{ID: task.EventID(id, 1), TaskID: id, EventSeq: 1, TaskRevision: 1, TaskFence: 0, Type: task.TaskCreated, ActorID: "owner", CreatedAt: now},
		{ID: task.EventID(id, 2), TaskID: id, RunID: "run-1", EventSeq: 2, TaskRevision: 2, TaskFence: 1, Type: task.TaskOwnershipTransferred, CreatedAt: now.Add(time.Second)},
	}
	if err := file.Save(state); err != nil {
		t.Fatal(err)
	}
	persistence := &countingCanonicalFile{CanonicalFile: file}
	s, err := store.NewCanonical(persistence)
	if err != nil {
		t.Fatal(err)
	}

	const actor, messageID = "owner-7", "cmd-atomic"
	receipt, replayed, err := s.ApplyInboxCommand(actor, messageID, id, 2, func(next *store.CanonicalState) (store.InboxReceipt, error) {
		value := next.Tasks[id]
		value.Revision = 3
		value.LastEventSeq = 3
		value.UpdatedAt = now.Add(2 * time.Second)
		next.Tasks[id] = value
		run := next.Runs["run-1"]
		run.ProgressPct = 25
		next.Runs[run.RunID] = run
		event := task.DomainEvent{ID: task.EventID(id, 3), TaskID: id, RunID: run.RunID,
			EventSeq: 3, TaskRevision: 3, TaskFence: value.Fence, Type: "WORK_PROGRESS", ActorID: actor, CreatedAt: value.UpdatedAt}
		next.Events = append(next.Events, event)
		env := envelope.Envelope{SchemaVersion: 1, MessageID: "outbox-atomic", Timestamp: value.UpdatedAt,
			From: "coord", To: run.OwnerSession, Kind: envelope.Task, Task: run.Task, Run: run.RunID,
			Attempt: run.Attempt, Fence: run.Fence, TargetSHA: "0123456789012345678901234567890123456789",
			TaskPayload: &envelope.TaskPayload{Worktree: run.Worktree, Deadline: run.Deadline}}
		next.Outbox[run.RunID] = store.OutboxQueue{Messages: []store.OutboxMessage{{Envelope: env, EventID: event.ID}}}
		return store.InboxReceipt{ActorID: actor, MessageID: messageID, TaskID: id, RunID: run.RunID,
			EventIDs: []string{event.ID}, ExpectedRevision: 2, CommittedRevision: 3, RecordedAt: value.UpdatedAt}, nil
	})
	if err != nil || replayed {
		t.Fatalf("ApplyInboxCommand receipt=%+v replayed=%v err=%v", receipt, replayed, err)
	}
	if persistence.saves != 1 {
		t.Fatalf("durable Save count=%d, want exactly one", persistence.saves)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	reloaded, err := file.Load()
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Tasks[id].Revision != 3 || reloaded.Tasks[id].LastEventSeq != 3 || reloaded.Runs["run-1"].ProgressPct != 25 {
		t.Fatalf("Task/Run not committed together: task=%+v run=%+v", reloaded.Tasks[id], reloaded.Runs["run-1"])
	}
	if len(reloaded.Events) != 3 || reloaded.Events[2].ID != receipt.EventIDs[0] {
		t.Fatalf("DomainEvent not committed with state: %+v", reloaded.Events)
	}
	if got := reloaded.Outbox["run-1"].Messages; len(got) != 1 || got[0].EventID != receipt.EventIDs[0] || got[0].Envelope.MessageID != "outbox-atomic" {
		t.Fatalf("Outbox not committed with DomainEvent: %+v", got)
	}
	if got := reloaded.Inbox[store.InboxKey(actor, messageID)]; got.CommittedRevision != 3 || got.EventIDs[0] != receipt.EventIDs[0] {
		t.Fatalf("Inbox receipt not committed with state: %+v", got)
	}
}

func TestCanonicalFileRoundTripsTaskEventAndExplicitRunRelation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "canonical.json")
	file := CanonicalFile{Path: path}
	id := taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	stage := task.Execution
	now := time.Unix(100, 0).UTC()
	state := store.EmptyCanonicalState()
	state.Tasks[id] = task.Task{
		ID: id, Title: "task", OwnerID: "owner", Revision: 2, LastEventSeq: 2, Fence: 1,
		StagePlan:    task.StagePlan{Stages: []task.Stage{task.Execution}, PolicyVersion: task.StagePlanPolicyVersion},
		CurrentStage: &stage, Status: task.InProgress, CreatedAt: now, UpdatedAt: now.Add(time.Second),
	}
	state.Runs["run-1"] = fsm.Run{Task: "legacy label", TaskID: id, RunID: "run-1", Attempt: 1, Fence: 1, OwnerSession: "executor", Worktree: "worktree", Deadline: now.Add(time.Hour), Status: fsm.Started}
	state.Events = append(state.Events,
		task.DomainEvent{ID: task.EventID(id, 1), TaskID: id, EventSeq: 1, TaskRevision: 1, TaskFence: 0, Type: task.TaskCreated, ActorID: "owner", CreatedAt: now},
		task.DomainEvent{ID: task.EventID(id, 2), TaskID: id, RunID: "run-1", EventSeq: 2, TaskRevision: 2, TaskFence: 1, Type: task.TaskOwnershipTransferred, CreatedAt: now.Add(time.Second)},
	)
	if err := file.Save(state); err != nil {
		t.Fatal(err)
	}
	loaded, err := file.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Tasks[id].ID != id || loaded.Runs["run-1"].TaskID != id || loaded.Runs["run-1"].Task != "legacy label" || len(loaded.Events) != 2 {
		t.Fatalf("canonical state round trip changed identities: %+v", loaded)
	}
}

func TestCanonicalFileReadsAndRewritesCreationOnlyV1WithoutFenceField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "canonical.json")
	file := CanonicalFile{Path: path}
	state := store.EmptyCanonicalState()
	id := taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	now := time.Unix(100, 0).UTC()
	stage := task.Execution
	state.Tasks[id] = task.Task{ID: id, Title: "old", OwnerID: "owner", Revision: 1, LastEventSeq: 1,
		StagePlan:    task.StagePlan{Stages: []task.Stage{task.Execution}, PolicyVersion: task.StagePlanPolicyVersion},
		CurrentStage: &stage, Status: task.Pending, CreatedAt: now, UpdatedAt: now}
	state.Events = []task.DomainEvent{{ID: task.EventID(id, 1), TaskID: id, EventSeq: 1, TaskRevision: 1,
		Type: task.TaskCreated, ActorID: "owner", CreatedAt: now}}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	events := document["events"].([]any)
	delete(events[0].(map[string]any), "task_fence")
	raw, err = json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}

	s, err := store.NewCanonical(file)
	if err != nil {
		t.Fatalf("load old creation-only schema v1: %v", err)
	}
	err = s.UpdateCanonical(func(next *store.CanonicalState) error {
		value := next.Tasks[id]
		value.Title = "updated"
		value.Revision++
		value.LastEventSeq++
		value.UpdatedAt = now.Add(time.Second)
		next.Tasks[id] = value
		next.Events = append(next.Events, task.DomainEvent{ID: task.EventID(id, value.LastEventSeq), TaskID: id,
			EventSeq: value.LastEventSeq, TaskRevision: value.Revision, TaskFence: value.Fence,
			Type: "TASK_UPDATED", CreatedAt: value.UpdatedAt})
		return nil
	})
	if err != nil {
		t.Fatalf("rewrite schema v1: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.NewCanonical(file)
	if err != nil {
		t.Fatalf("reopen rewritten schema v1: %v", err)
	}
	defer func() {
		if err := reopened.Close(); err != nil {
			t.Errorf("close reopened canonical store: %v", err)
		}
	}()
	reopenedState, err := reopened.CanonicalSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if reopenedState.SchemaVersion != store.CanonicalSchemaVersion {
		t.Fatalf("schema version = %d", reopenedState.SchemaVersion)
	}
	final, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var rewritten map[string]any
	if err := json.Unmarshal(final, &rewritten); err != nil {
		t.Fatal(err)
	}
	for i, event := range rewritten["events"].([]any) {
		if _, ok := event.(map[string]any)["task_fence"]; !ok {
			t.Fatalf("event %d missing task_fence after rewrite", i)
		}
	}
}

func TestCanonicalFileRejectsTransferredV1WithoutFenceAndPreservesSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "canonical.json")
	file := CanonicalFile{Path: path}
	state := store.EmptyCanonicalState()
	id := taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	now := time.Unix(100, 0).UTC()
	stage := task.Execution
	state.Tasks[id] = task.Task{ID: id, Title: "task", OwnerID: "owner", Revision: 2, LastEventSeq: 2, Fence: 1,
		StagePlan:    task.StagePlan{Stages: []task.Stage{task.Execution}, PolicyVersion: task.StagePlanPolicyVersion},
		CurrentStage: &stage, Status: task.InProgress, CreatedAt: now, UpdatedAt: now.Add(time.Second)}
	state.Runs["run-1"] = fsm.Run{TaskID: id, RunID: "run-1", Attempt: 1, Fence: 1, OwnerSession: "agent", Worktree: "worktree", Deadline: now.Add(time.Hour), Status: fsm.Started}
	state.Events = []task.DomainEvent{
		{ID: task.EventID(id, 1), TaskID: id, EventSeq: 1, TaskRevision: 1, TaskFence: 0, Type: task.TaskCreated, CreatedAt: now},
		{ID: task.EventID(id, 2), TaskID: id, RunID: "run-1", EventSeq: 2, TaskRevision: 2, TaskFence: 1, Type: task.TaskOwnershipTransferred, CreatedAt: now.Add(time.Second)},
	}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	delete(document["events"].([]any)[1].(map[string]any), "task_fence")
	raw, err = json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.NewCanonical(file); err == nil {
		t.Fatal("transferred history without task_fence was accepted")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("rejected snapshot was modified: err=%v", err)
	}
}

func TestCanonicalFileRejectsUnknownSchemaWithoutModifyingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "canonical.json")
	data := []byte(`{"schema_version":3,"tasks":{},"runs":{},"events":[],"inbox":{},"outbox":{}}`)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := (CanonicalFile{Path: path}).Load(); err == nil {
		t.Fatal("canonical loader accepted a non-current schema")
	}
	current, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(current, data) {
		t.Fatalf("rejected schema modified source: err=%v unchanged=%v", err, bytes.Equal(current, data))
	}
}
