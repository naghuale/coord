package task

import (
	"coord/taskid"
	"fmt"
	"strings"
	"time"
)

type EventType string

const (
	TaskCreated              EventType = "TASK_CREATED"
	TaskOwnershipTransferred EventType = "TASK_OWNERSHIP_TRANSFERRED"
)

// DomainEvent is an auditable fact in a Task's canonical history.
type DomainEvent struct {
	ID           string    `json:"id"`
	TaskID       taskid.ID `json:"task_id"`
	RunID        string    `json:"run_id,omitempty"`
	EventSeq     uint64    `json:"event_seq"`
	TaskRevision uint64    `json:"task_revision"`
	TaskFence    uint64    `json:"task_fence"`
	Type         EventType `json:"type"`
	ActorID      string    `json:"actor_id,omitempty"`
	Summary      string    `json:"summary,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

func EventID(id taskid.ID, seq uint64) string {
	return fmt.Sprintf("%s/event/%d", id, seq)
}

func (e DomainEvent) Validate() error {
	if err := e.TaskID.Validate(); err != nil {
		return fmt.Errorf("invalid event TaskID: %w", err)
	}
	if e.ID != EventID(e.TaskID, e.EventSeq) || e.EventSeq == 0 || e.TaskRevision == 0 {
		return fmt.Errorf("invalid event identity or sequence")
	}
	if e.Type == "" || strings.TrimSpace(string(e.Type)) != string(e.Type) || e.CreatedAt.IsZero() {
		return fmt.Errorf("invalid event type or timestamp")
	}
	return nil
}
