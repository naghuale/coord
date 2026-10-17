package task

import (
	"coord/taskid"
	"fmt"
	"strings"
)

// OriginKind identifies a canonical source for an ExternalAction. It is a
// closed set: display text and caller supplied labels are not authority.
type OriginKind string

const (
	OriginTrackingProjection OriginKind = "TRACKING_PROJECTION"
	OriginReviewProjection   OriginKind = "REVIEW_PROJECTION"
	OriginAuditProjection    OriginKind = "AUDIT_PROJECTION"
	OriginCIPipeline         OriginKind = "CI_PIPELINE"
	OriginIntegrationGate    OriginKind = "INTEGRATION_GATE"
)

type ExternalActionOrigin struct {
	Kind         OriginKind     `json:"kind"`
	ProjectID    string         `json:"project_id"`
	TaskID       string         `json:"task_id,omitempty"`
	RunID        string         `json:"run_id,omitempty"`
	AssignmentID string         `json:"assignment_id,omitempty"`
	Subject      SubjectBinding `json:"subject"`
	CommandID    string         `json:"command_id"`
	EventID      string         `json:"event_id"`
}

func (o ExternalActionOrigin) Validate() error {
	switch o.Kind {
	case OriginTrackingProjection, OriginReviewProjection, OriginAuditProjection, OriginCIPipeline, OriginIntegrationGate:
	default:
		return fmt.Errorf("unknown external action origin kind %q", o.Kind)
	}
	if strings.TrimSpace(o.ProjectID) == "" || strings.TrimSpace(o.CommandID) == "" || strings.TrimSpace(o.EventID) == "" {
		return fmt.Errorf("external action origin requires project, command and event identity")
	}
	if (o.TaskID == "") != (o.RunID == "") || (o.TaskID == "") != (o.AssignmentID == "") {
		return fmt.Errorf("external action task, run and assignment identity must be supplied together")
	}
	if o.TaskID != "" {
		id := taskid.ID(o.TaskID)
		if err := id.Validate(); err != nil {
			return fmt.Errorf("external action origin TaskID: %w", err)
		}
	}
	if err := o.Subject.Validate(); err != nil {
		return fmt.Errorf("external action origin subject: %w", err)
	}
	return nil
}
