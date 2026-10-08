package checkpoint

import (
	"bytes"
	"coord/fsm"
	"coord/store"
	"coord/task"
	"coord/taskid"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

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
