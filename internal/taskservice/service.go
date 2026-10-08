// Package taskservice contains an isolated canonical Task persistence helper.
// It is currently used only by tests: it does not authenticate OwnerID and is
// not a daemon command boundary. Production composition must add a verifier for
// Task-scoped Owner capability and session generation before wiring this code.
package taskservice

import (
	"coord/store"
	"coord/task"
	"coord/taskid"
	"errors"
	"fmt"
	"time"
)

var ErrTaskIDCollision = errors.New("could not allocate a unique TaskID")

const taskIDAttempts = 16

type IDGenerator interface {
	New() (taskid.ID, error)
}

type realIDGenerator struct{}

func (realIDGenerator) New() (taskid.ID, error) { return taskid.New() }

type Service struct {
	state *store.CanonicalStore
	ids   IDGenerator
	now   func() time.Time
}

func New(state *store.CanonicalStore, ids IDGenerator, now func() time.Time) (*Service, error) {
	if state == nil {
		return nil, fmt.Errorf("canonical state store required")
	}
	if ids == nil {
		ids = realIDGenerator{}
	}
	if now == nil {
		now = time.Now
	}
	return &Service{state: state, ids: ids, now: now}, nil
}

type CreateInput struct {
	Title       string
	Description string
	OwnerID     string
	StagePlan   task.StagePlan
}

func (s *Service) Create(input CreateInput) (created task.Task, err error) {
	if err := input.StagePlan.Validate(); err != nil {
		return task.Task{}, fmt.Errorf("validate StagePlan: %w", err)
	}
	if err := input.StagePlan.ValidateRuntimeSupport(); err != nil {
		return task.Task{}, fmt.Errorf("StagePlan is not executable by the current runtime: %w", err)
	}
	now := s.now().UTC()
	err = s.state.UpdateCanonical(func(state *store.CanonicalState) error {
		var id taskid.ID
		for attempt := 0; attempt < taskIDAttempts; attempt++ {
			candidate, genErr := s.ids.New()
			if genErr != nil {
				return fmt.Errorf("generate TaskID: %w", genErr)
			}
			if parseErr := candidate.Validate(); parseErr != nil {
				return fmt.Errorf("generator returned invalid TaskID: %w", parseErr)
			}
			if _, exists := state.Tasks[candidate]; exists {
				continue
			}
			id = candidate
			break
		}
		if id == "" {
			return ErrTaskIDCollision
		}
		stage := task.Execution
		created = task.Task{
			ID: id, Title: input.Title, Description: input.Description, OwnerID: input.OwnerID,
			Revision: 1, LastEventSeq: 1, StagePlan: input.StagePlan,
			CurrentStage: &stage, Status: task.Pending, CreatedAt: now, UpdatedAt: now,
		}
		if err := created.Validate(); err != nil {
			return fmt.Errorf("validate new Task: %w", err)
		}
		state.Tasks[id] = created
		state.Events = append(state.Events, task.DomainEvent{
			ID: task.EventID(id, 1), TaskID: id, EventSeq: 1, TaskRevision: 1,
			Type: task.TaskCreated, ActorID: input.OwnerID, Summary: "Task created", CreatedAt: now,
		})
		return nil
	})
	if err != nil {
		return task.Task{}, err
	}
	return created, nil
}
