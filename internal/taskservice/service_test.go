package taskservice

import (
	"coord/store"
	"coord/task"
	"coord/taskid"
	"errors"
	"strings"
	"testing"
	"time"
)

type memoryPersistence struct{ state store.CanonicalState }

func (p *memoryPersistence) Lock() (func() error, error)           { return func() error { return nil }, nil }
func (p *memoryPersistence) Load() (store.CanonicalState, error)   { return p.state, nil }
func (p *memoryPersistence) Save(state store.CanonicalState) error { p.state = state; return nil }

type sequenceIDs struct {
	ids   []taskid.ID
	index int
}

func (g *sequenceIDs) New() (taskid.ID, error) {
	if g.index >= len(g.ids) {
		return "", errors.New("ID sequence exhausted")
	}
	id := g.ids[g.index]
	g.index++
	return id, nil
}

func TestCreateTaskGeneratesCanonicalTaskID(t *testing.T) {
	state, err := store.NewCanonical(&memoryPersistence{state: store.EmptyCanonicalState()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = state.Close() }()
	ids := &sequenceIDs{ids: []taskid.ID{"task_01ARZ3NDEKTSV4RRFFQ69G5FAV"}}
	service, err := New(state, ids, func() time.Time { return time.Unix(100, 0) })
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.Create(testInput())
	if err != nil {
		t.Fatal(err)
	}
	if created.ID != ids.ids[0] || created.Revision != 1 || created.LastEventSeq != 1 {
		t.Fatalf("unexpected new Task: %+v", created)
	}
	snapshot, err := state.CanonicalSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Tasks) != 1 || len(snapshot.Events) != 1 || snapshot.Events[0].Type != task.TaskCreated {
		t.Fatalf("Task and creation event were not committed together: tasks=%d events=%+v", len(snapshot.Tasks), snapshot.Events)
	}
}

func TestTaskIDCollisionRetriesBeforeCommit(t *testing.T) {
	state, err := store.NewCanonical(&memoryPersistence{state: store.EmptyCanonicalState()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = state.Close() }()
	duplicate := taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	firstIDs := &sequenceIDs{ids: []taskid.ID{duplicate}}
	first, _ := New(state, firstIDs, func() time.Time { return time.Unix(100, 0) })
	if _, err := first.Create(testInput()); err != nil {
		t.Fatal(err)
	}
	ids := &sequenceIDs{ids: []taskid.ID{duplicate, "task_01ARZ3NDEKTSV4RRFFQ69G5FAW"}}
	second, _ := New(state, ids, func() time.Time { return time.Unix(101, 0) })
	created, err := second.Create(testInput())
	if err != nil {
		t.Fatal(err)
	}
	if created.ID != ids.ids[1] || ids.index != 2 {
		t.Fatalf("collision was not retried: task=%s calls=%d", created.ID, ids.index)
	}
	snapshot, err := state.CanonicalSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Tasks) != 2 || len(snapshot.Events) != 2 {
		t.Fatalf("collision retry produced duplicate or partial state: tasks=%d events=%d", len(snapshot.Tasks), len(snapshot.Events))
	}
}

func TestTaskCreateRejectsInvalidIdentityWithoutCommit(t *testing.T) {
	state, err := store.NewCanonical(&memoryPersistence{state: store.EmptyCanonicalState()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = state.Close() }()
	service, _ := New(state, &sequenceIDs{ids: []taskid.ID{"not-a-task-id"}}, time.Now)
	if _, err := service.Create(testInput()); err == nil {
		t.Fatal("invalid generated TaskID was accepted")
	}
	snapshot, err := state.CanonicalSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Tasks) != 0 || len(snapshot.Events) != 0 {
		t.Fatal("invalid TaskID partially changed canonical state")
	}
}

func TestTaskCreateRejectsUnsupportedStageWithoutCommit(t *testing.T) {
	state, err := store.NewCanonical(&memoryPersistence{state: store.EmptyCanonicalState()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = state.Close() }()
	ids := &sequenceIDs{ids: []taskid.ID{
		"task_01ARZ3NDEKTSV4RRFFQ69G5FAV",
		"task_01ARZ3NDEKTSV4RRFFQ69G5FAW",
	}}
	service, err := New(state, ids, func() time.Time { return time.Unix(100, 0) })
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.Create(testInput())
	if err != nil {
		t.Fatal(err)
	}
	unsupported := testInput()
	unsupported.StagePlan.Stages = []task.Stage{task.Execution, task.Review}
	if _, err := service.Create(unsupported); err == nil || !strings.Contains(err.Error(), `runtime does not support stage "REVIEW"`) {
		t.Fatalf("unsupported StagePlan error=%v; want specific REVIEW runtime rejection", err)
	}
	snapshot, err := state.CanonicalSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Tasks) != 1 || len(snapshot.Events) != 1 || snapshot.Tasks[created.ID].Revision != 1 {
		t.Fatalf("unsupported StagePlan changed canonical state: tasks=%d events=%d task=%+v", len(snapshot.Tasks), len(snapshot.Events), snapshot.Tasks[created.ID])
	}
}

func TestTaskCreateRejectsRequiredOwnerDecisionWithoutCommit(t *testing.T) {
	state, err := store.NewCanonical(&memoryPersistence{state: store.EmptyCanonicalState()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = state.Close() }()
	ids := &sequenceIDs{ids: []taskid.ID{
		"task_01ARZ3NDEKTSV4RRFFQ69G5FAV",
		"task_01ARZ3NDEKTSV4RRFFQ69G5FAW",
	}}
	service, err := New(state, ids, func() time.Time { return time.Unix(100, 0) })
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.Create(testInput())
	if err != nil {
		t.Fatal(err)
	}
	unsupported := testInput()
	unsupported.StagePlan.RequireOwnerDecision = true
	if _, err := service.Create(unsupported); err == nil || !strings.Contains(err.Error(), "runtime does not support required Owner decision stage") {
		t.Fatalf("required Owner decision error=%v; want specific runtime rejection", err)
	}
	snapshot, err := state.CanonicalSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Tasks) != 1 || len(snapshot.Events) != 1 || snapshot.Tasks[created.ID].Revision != 1 {
		t.Fatalf("unsupported Owner decision policy changed canonical state: tasks=%d events=%d task=%+v", len(snapshot.Tasks), len(snapshot.Events), snapshot.Tasks[created.ID])
	}
}

func testInput() CreateInput {
	return CreateInput{
		Title: "Task title", OwnerID: "owner-1",
		StagePlan: task.StagePlan{Stages: []task.Stage{task.Execution}, PolicyVersion: task.StagePlanPolicyVersion},
	}
}
