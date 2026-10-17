package task

import (
	"coord/taskid"
	"fmt"
	"regexp"
	"strings"
	"time"
)

type CompletionBoundary string

const (
	CompletionNoBoundary       CompletionBoundary = "NONE"
	CompletionOwnerBoundary    CompletionBoundary = "OWNER_REQUIRED"
	CompletionHumanBoundary    CompletionBoundary = "HUMAN_ACTION_REQUIRED"
	CompletionExternalBoundary CompletionBoundary = "BLOCKED_EXTERNAL"
)

type CoordinatorCompletion struct {
	DecisionDigest string             `json:"decision_digest"`
	Transition     string             `json:"transition"`
	Boundary       CompletionBoundary `json:"boundary"`
	Subject        SubjectBinding     `json:"subject"`
}

var completionDigestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (c CoordinatorCompletion) Validate() error {
	if !completionDigestPattern.MatchString(c.DecisionDigest) || c.Transition == "" || len(c.Transition) > 128 {
		return fmt.Errorf("invalid coordinator completion identity")
	}
	switch c.Boundary {
	case CompletionNoBoundary, CompletionOwnerBoundary, CompletionHumanBoundary, CompletionExternalBoundary:
	default:
		return fmt.Errorf("unknown coordinator completion boundary")
	}
	return c.Subject.Validate()
}

type EventType string

const (
	TaskCreated                   EventType = "TASK_CREATED"
	TaskOwnershipTransferred      EventType = "TASK_OWNERSHIP_TRANSFERRED"
	ReviewResultRecorded          EventType = "REVIEW_RESULT_RECORDED"
	AuditResultRecorded           EventType = "AUDIT_RESULT_RECORDED"
	ExecutorResultRecorded        EventType = "EXECUTOR_RESULT_RECORDED"
	DiscussionNodeRecorded        EventType = "DISCUSSION_NODE_RECORDED"
	DiscussionEdgeRecorded        EventType = "DISCUSSION_EDGE_RECORDED"
	ReviewResultSuperseded        EventType = "REVIEW_RESULT_SUPERSEDED"
	AuditResultSuperseded         EventType = "AUDIT_RESULT_SUPERSEDED"
	ExternalActionStateRecorded   EventType = "EXTERNAL_ACTION_STATE_RECORDED"
	CoordinatorCompletionRecorded EventType = "COORDINATOR_COMPLETION_RECORDED"
)

// DomainEvent is an auditable fact in a Task's canonical history.
type DomainEvent struct {
	ID             string          `json:"id"`
	TaskID         taskid.ID       `json:"task_id"`
	RunID          string          `json:"run_id,omitempty"`
	EventSeq       uint64          `json:"event_seq"`
	TaskRevision   uint64          `json:"task_revision"`
	TaskFence      uint64          `json:"task_fence"`
	Type           EventType       `json:"type"`
	ActorID        string          `json:"actor_id,omitempty"`
	Summary        string          `json:"summary,omitempty"`
	ReviewResult   *ReviewResult   `json:"review_result,omitempty"`
	AuditResult    *AuditResult    `json:"audit_result,omitempty"`
	ExecutorResult *ExecutorResult `json:"executor_result,omitempty"`
	DiscussionNode *DiscussionNode `json:"discussion_node,omitempty"`
	DiscussionEdge *DiscussionEdge `json:"discussion_edge,omitempty"`
	// SupersededResultID is set only for ReviewResultSuperseded and
	// AuditResultSuperseded events. It names the ResultID of the result
	// that is no longer eligible for readiness evaluation.
	SupersededResultID    string                 `json:"superseded_result_id,omitempty"`
	ExternalActionID      string                 `json:"external_action_id,omitempty"`
	ExternalActionStatus  string                 `json:"external_action_status,omitempty"`
	ExternalObservationID string                 `json:"external_observation_id,omitempty"`
	CoordinatorCompletion *CoordinatorCompletion `json:"coordinator_completion,omitempty"`
	CreatedAt             time.Time              `json:"created_at"`
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
	if e.Type != ExternalActionStateRecorded && (e.ExternalActionID != "" || e.ExternalActionStatus != "" || e.ExternalObservationID != "") {
		return fmt.Errorf("external action metadata is only valid on EXTERNAL_ACTION_STATE_RECORDED")
	}
	if e.Type != CoordinatorCompletionRecorded && e.CoordinatorCompletion != nil {
		return fmt.Errorf("coordinator completion payload is only valid on its typed event")
	}
	switch e.Type {
	case DiscussionNodeRecorded:
		if e.DiscussionNode == nil || e.DiscussionEdge != nil || e.ReviewResult != nil || e.AuditResult != nil || e.ExecutorResult != nil {
			return fmt.Errorf("DISCUSSION_NODE_RECORDED requires only a DiscussionNode payload")
		}
		if err := e.DiscussionNode.Validate(); err != nil {
			return fmt.Errorf("invalid DiscussionNode: %w", err)
		}
		if e.DiscussionNode.TaskID != e.TaskID || !e.DiscussionNode.CreatedAt.Equal(e.CreatedAt) || e.DiscussionNode.Actor != e.ActorID || e.DiscussionNode.Summary != e.Summary {
			return fmt.Errorf("DiscussionNode metadata differs from DomainEvent")
		}
	case DiscussionEdgeRecorded:
		if e.DiscussionEdge == nil || e.DiscussionNode != nil || e.ReviewResult != nil || e.AuditResult != nil || e.ExecutorResult != nil {
			return fmt.Errorf("DISCUSSION_EDGE_RECORDED requires only a DiscussionEdge payload")
		}
		if err := e.DiscussionEdge.Validate(); err != nil {
			return fmt.Errorf("invalid DiscussionEdge: %w", err)
		}
		if e.DiscussionEdge.TaskID != e.TaskID || !e.DiscussionEdge.CreatedAt.Equal(e.CreatedAt) || e.DiscussionEdge.Actor != e.ActorID {
			return fmt.Errorf("DiscussionEdge metadata differs from DomainEvent")
		}
	case ReviewResultRecorded:
		if e.ReviewResult == nil || e.AuditResult != nil || e.ExecutorResult != nil {
			return fmt.Errorf("REVIEW_RESULT_RECORDED requires only a ReviewResult payload")
		}
		if err := e.ReviewResult.Validate(); err != nil {
			return fmt.Errorf("invalid ReviewResult payload: %w", err)
		}
	case AuditResultRecorded:
		if e.AuditResult == nil || e.ReviewResult != nil || e.ExecutorResult != nil {
			return fmt.Errorf("AUDIT_RESULT_RECORDED requires only an AuditResult payload")
		}
		if err := e.AuditResult.Validate(); err != nil {
			return fmt.Errorf("invalid AuditResult payload: %w", err)
		}
	case ExecutorResultRecorded:
		if e.ExecutorResult == nil || e.ReviewResult != nil || e.AuditResult != nil || e.DiscussionNode != nil || e.DiscussionEdge != nil {
			return fmt.Errorf("EXECUTOR_RESULT_RECORDED requires only an ExecutorResult payload")
		}
		if err := e.ExecutorResult.Validate(); err != nil {
			return fmt.Errorf("invalid ExecutorResult payload: %w", err)
		}
	case ReviewResultSuperseded:
		if e.SupersededResultID == "" {
			return fmt.Errorf("REVIEW_RESULT_SUPERSEDED requires superseded_result_id")
		}
		if e.ReviewResult != nil || e.AuditResult != nil || e.ExecutorResult != nil || e.DiscussionNode != nil || e.DiscussionEdge != nil {
			return fmt.Errorf("REVIEW_RESULT_SUPERSEDED must not carry a typed result payload")
		}
	case AuditResultSuperseded:
		if e.SupersededResultID == "" {
			return fmt.Errorf("AUDIT_RESULT_SUPERSEDED requires superseded_result_id")
		}
		if e.ReviewResult != nil || e.AuditResult != nil || e.ExecutorResult != nil || e.DiscussionNode != nil || e.DiscussionEdge != nil {
			return fmt.Errorf("AUDIT_RESULT_SUPERSEDED must not carry a typed result payload")
		}
	default:
		if e.Type == CoordinatorCompletionRecorded {
			if e.CoordinatorCompletion == nil || e.ExternalActionID != "" || e.Summary != e.CoordinatorCompletion.DecisionDigest || e.ReviewResult != nil || e.AuditResult != nil || e.ExecutorResult != nil || e.DiscussionNode != nil || e.DiscussionEdge != nil || e.SupersededResultID != "" {
				return fmt.Errorf("COORDINATOR_COMPLETION_RECORDED requires typed completion payload only")
			}
			if err := e.CoordinatorCompletion.Validate(); err != nil {
				return err
			}
		} else if e.Type == ExternalActionStateRecorded {
			if e.ExternalActionID == "" || e.ExternalActionStatus == "" || e.Summary != e.ExternalActionID || e.ReviewResult != nil || e.AuditResult != nil || e.ExecutorResult != nil || e.DiscussionNode != nil || e.DiscussionEdge != nil || e.SupersededResultID != "" {
				return fmt.Errorf("EXTERNAL_ACTION_STATE_RECORDED requires action identity and status only")
			}
			switch e.ExternalActionStatus {
			case "PENDING", "CLAIMED", "DISPATCHING", "DISPATCHED", "AMBIGUOUS", "OBSERVED", "COMPLETED", "FAILED", "BLOCKED", "FENCED":
			default:
				return fmt.Errorf("unknown external action status")
			}
			if (e.ExternalActionStatus == "OBSERVED" || e.ExternalActionStatus == "COMPLETED") && e.ExternalObservationID == "" {
				return fmt.Errorf("observed external action requires observation identity")
			}
			if e.ExternalActionStatus != "OBSERVED" && e.ExternalActionStatus != "COMPLETED" && e.ExternalObservationID != "" {
				return fmt.Errorf("observation identity on unobserved action")
			}
		} else if e.ReviewResult != nil || e.AuditResult != nil || e.ExecutorResult != nil || e.DiscussionNode != nil || e.DiscussionEdge != nil || e.ExternalActionID != "" || e.ExternalActionStatus != "" || e.ExternalObservationID != "" {
			return fmt.Errorf("event type %q cannot contain a typed payload", e.Type)
		}
	}
	return nil
}
