// Package task defines the canonical Task aggregate and its validation rules.
package task

import (
	"coord/taskid"
	"fmt"
	"strings"
	"time"
)

type Stage string

const (
	Execution     Stage = "EXECUTION"
	Review        Stage = "REVIEW"
	Audit         Stage = "AUDIT"
	OwnerDecision Stage = "OWNER_DECISION"
)

type Status string

const (
	Pending    Status = "PENDING"
	InProgress Status = "IN_PROGRESS"
	Blocked    Status = "BLOCKED"
	Failed     Status = "FAILED"
	Completed  Status = "COMPLETED"
)

const StagePlanPolicyVersion uint32 = 1

// StagePlan is the validated sequence of agent stages. OwnerDecision is a
// conditional interruption and cannot be inserted into Stages.
type StagePlan struct {
	Stages               []Stage `json:"stages"`
	RequireOwnerDecision bool    `json:"require_owner_decision"`
	PolicyVersion        uint32  `json:"policy_version"`
}

func (p StagePlan) Validate() error {
	if p.PolicyVersion != StagePlanPolicyVersion {
		return fmt.Errorf("unsupported stage plan policy version %d", p.PolicyVersion)
	}
	if len(p.Stages) == 0 || p.Stages[0] != Execution {
		return fmt.Errorf("stage plan must begin with EXECUTION")
	}
	seen := make(map[Stage]bool, len(p.Stages))
	last := -1
	for _, stage := range p.Stages {
		if seen[stage] {
			return fmt.Errorf("duplicate stage %q", stage)
		}
		seen[stage] = true
		var order int
		switch stage {
		case Execution:
			order = 0
		case Review:
			order = 1
		case Audit:
			order = 2
		default:
			return fmt.Errorf("stage %q is not allowed in StagePlan", stage)
		}
		if order <= last {
			return fmt.Errorf("stages are not in workflow order")
		}
		last = order
	}
	return nil
}

type Task struct {
	ID           taskid.ID `json:"id"`
	Title        string    `json:"title"`
	Description  string    `json:"description"`
	OwnerID      string    `json:"owner_id"`
	Revision     uint64    `json:"revision"`
	LastEventSeq uint64    `json:"last_event_seq"`
	StagePlan    StagePlan `json:"stage_plan"`
	CurrentStage *Stage    `json:"current_stage,omitempty"`
	Status       Status    `json:"status"`
	Fence        uint64    `json:"fence"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func (t Task) Validate() error {
	if err := t.ID.Validate(); err != nil {
		return fmt.Errorf("invalid TaskID: %w", err)
	}
	if strings.TrimSpace(t.Title) == "" || strings.TrimSpace(t.OwnerID) == "" {
		return fmt.Errorf("task title and owner are required")
	}
	if t.Revision == 0 || t.LastEventSeq == 0 {
		return fmt.Errorf("task revision and last event sequence must be positive")
	}
	if err := t.StagePlan.Validate(); err != nil {
		return err
	}
	if t.CreatedAt.IsZero() || t.UpdatedAt.IsZero() || t.UpdatedAt.Before(t.CreatedAt) {
		return fmt.Errorf("invalid task timestamps")
	}
	terminal := t.Status == Failed || t.Status == Completed
	if terminal {
		if t.CurrentStage != nil {
			return fmt.Errorf("terminal task cannot have a current stage")
		}
	} else {
		if t.Status != Pending && t.Status != InProgress && t.Status != Blocked {
			return fmt.Errorf("invalid task status %q", t.Status)
		}
		if t.CurrentStage == nil {
			return fmt.Errorf("nonterminal task requires a current stage")
		}
		if *t.CurrentStage == OwnerDecision {
			if t.Status != Blocked {
				return fmt.Errorf("OWNER_DECISION stage requires BLOCKED status")
			}
		} else if !contains(t.StagePlan.Stages, *t.CurrentStage) {
			return fmt.Errorf("current stage %q is absent from StagePlan", *t.CurrentStage)
		}
	}
	if t.Status == Pending && (t.CurrentStage == nil || *t.CurrentStage != Execution) {
		return fmt.Errorf("PENDING task must be at EXECUTION")
	}
	return nil
}

// ValidateRuntimeSupport rejects plans whose stages the current runtime cannot
// execute. It is applied at the task creation boundary, separately from the
// complete StagePlan grammar.
func (p StagePlan) ValidateRuntimeSupport() error {
	for _, stage := range p.Stages {
		if stage != Execution {
			return fmt.Errorf("runtime does not support stage %q", stage)
		}
	}
	if p.RequireOwnerDecision {
		return fmt.Errorf("runtime does not support required Owner decision stage")
	}
	return nil
}

func contains(stages []Stage, wanted Stage) bool {
	for _, stage := range stages {
		if stage == wanted {
			return true
		}
	}
	return false
}
