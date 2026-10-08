package checkpoint

import (
	"bytes"
	"coord/fsm"
	"coord/store"
	"coord/task"
	"coord/taskid"
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
		task.DomainEvent{ID: task.EventID(id, 1), TaskID: id, EventSeq: 1, TaskRevision: 1, Type: task.TaskCreated, ActorID: "owner", CreatedAt: now},
		task.DomainEvent{ID: task.EventID(id, 2), TaskID: id, RunID: "run-1", EventSeq: 2, TaskRevision: 2, Type: "TASK_ASSIGNED", CreatedAt: now.Add(time.Second)},
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
