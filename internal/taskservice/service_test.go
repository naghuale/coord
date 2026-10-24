package taskservice

import (
	"coord/store"
	"coord/task"
	"coord/taskid"
	"errors"
	"testing"
	"time"
)

type memoryPersistence struct{ state store.CanonicalState }

func (p *memoryPersistence) Lock() (func() error, error)           { return func() error { return nil }, nil }
func (p *memoryPersistence) Load() (store.CanonicalState, error)   { return p.state, nil }
func (p *memoryPersistence) Save(state store.CanonicalState) error { p.state = state; return nil }

type unusedIDs struct{}

func (unusedIDs) New() (taskid.ID, error) { return "", errors.New("must not be called") }

func TestTaskServiceCannotBypassAuthenticatedOwnerCommand(t *testing.T) {
	p := &memoryPersistence{state: store.EmptyCanonicalState()}
	state, err := store.NewCanonical(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := state.Close(); err != nil {
			t.Errorf("close canonical store: %v", err)
		}
	})
	service, err := New(state, unusedIDs{}, func() time.Time { return time.Unix(100, 0) })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Create(CreateInput{Title: "Task", OwnerID: "owner", StagePlan: task.StagePlan{Stages: []task.Stage{task.Execution}, PolicyVersion: task.StagePlanPolicyVersion}}); !errors.Is(err, store.ErrOwnerCommandRequired) {
		t.Fatalf("unauthenticated Task service error=%v", err)
	}
	snapshot, err := state.CanonicalSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Tasks) != 0 || len(snapshot.Events) != 0 || p.state.Revision != 0 {
		t.Fatal("unauthenticated Task service changed canonical state")
	}
}
