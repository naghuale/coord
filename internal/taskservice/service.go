// Package taskservice retains an internal compatibility surface for the former
// unauthenticated Task writer. Create now fails closed; production Task writes
// must use the authenticated Owner command boundary in CanonicalStore.
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
	_ = input
	return task.Task{}, store.ErrOwnerCommandRequired
}
