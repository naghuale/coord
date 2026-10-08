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
	ReviewResultRecorded     EventType = "REVIEW_RESULT_RECORDED"
	AuditResultRecorded      EventType = "AUDIT_RESULT_RECORDED"
)

// DomainEvent is an auditable fact in a Task's canonical history.
type DomainEvent struct {
	ID           string        `json:"id"`
	TaskID       taskid.ID     `json:"task_id"`
	RunID        string        `json:"run_id,omitempty"`
	EventSeq     uint64        `json:"event_seq"`
	TaskRevision uint64        `json:"task_revision"`
	TaskFence    uint64        `json:"task_fence"`
	Type         EventType     `json:"type"`
	ActorID      string        `json:"actor_id,omitempty"`
	Summary      string        `json:"summary,omitempty"`
	ReviewResult *ReviewResult `json:"review_result,omitempty"`
	AuditResult  *AuditResult  `json:"audit_result,omitempty"`
	CreatedAt    time.Time     `json:"created_at"`
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
	switch e.Type {
	case ReviewResultRecorded:
		if e.ReviewResult == nil || e.AuditResult != nil {
			return fmt.Errorf("REVIEW_RESULT_RECORDED requires only a ReviewResult payload")
		}
		if err := e.ReviewResult.Validate(); err != nil {
			return fmt.Errorf("invalid ReviewResult payload: %w", err)
		}
	case AuditResultRecorded:
		if e.AuditResult == nil || e.ReviewResult != nil {
			return fmt.Errorf("AUDIT_RESULT_RECORDED requires only an AuditResult payload")
		}
		if err := e.AuditResult.Validate(); err != nil {
			return fmt.Errorf("invalid AuditResult payload: %w", err)
		}
	default:
		if e.ReviewResult != nil || e.AuditResult != nil {
			return fmt.Errorf("event type %q cannot contain a Review/Audit result payload", e.Type)
		}
	}
	return nil
}
