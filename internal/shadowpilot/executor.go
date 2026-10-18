package shadowpilot

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// ExecutorState is the lifecycle state of a shadow execution.
type ExecutorState string

const (
	ExecutorAdmitted  ExecutorState = "ADMITTED"
	ExecutorRunning   ExecutorState = "RUNNING"
	ExecutorCompleted ExecutorState = "COMPLETED"
	ExecutorFenced    ExecutorState = "FENCED"
)

// ShadowAssignment is the durable assignment intent recorded before execution
// begins. It is immutable after creation and holds no production authority.
// It must be persisted to the shadow store before the executor transitions
// to RUNNING.
type ShadowAssignment struct {
	ShadowRunID  ShadowRunID `json:"shadow_run_id"`
	AssignmentID string      `json:"assignment_id"`
	AgentID      string      `json:"agent_id"`
	SessionID    string      `json:"session_id"`
	Generation   uint64      `json:"generation"`
	// SubjectSHA is the exact 40- or 64-character commit SHA the executor must
	// verify before beginning work. Execution stops with StopExactSubjectMismatch
	// if the executor's working tree does not match this SHA.
	SubjectSHA  string    `json:"subject_sha"`
	InputDigest string    `json:"input_digest"`
	Role        string    `json:"role"`
	CreatedAt   time.Time `json:"created_at"`
}

func (a ShadowAssignment) Validate() error {
	if err := a.ShadowRunID.Validate(); err != nil {
		return fmt.Errorf("shadow assignment: %w", err)
	}
	if strings.TrimSpace(a.AssignmentID) == "" {
		return errors.New("shadow assignment: assignment_id required")
	}
	if strings.TrimSpace(a.AgentID) == "" {
		return errors.New("shadow assignment: agent_id required")
	}
	if strings.TrimSpace(a.SessionID) == "" {
		return errors.New("shadow assignment: session_id required")
	}
	if a.Generation == 0 {
		return errors.New("shadow assignment: generation must be positive")
	}
	if !sha40(a.SubjectSHA) {
		return errors.New("shadow assignment: subject_sha must be a 40 or 64 hex character commit SHA")
	}
	if a.InputDigest == "" {
		return errors.New("shadow assignment: input_digest required")
	}
	if strings.TrimSpace(a.Role) == "" {
		return errors.New("shadow assignment: role required")
	}
	if a.CreatedAt.IsZero() {
		return errors.New("shadow assignment: created_at required")
	}
	return nil
}

// AdmissionResult is the outcome of the shadow executor admission check.
// ADMITTED means the executor may begin work; REJECTED halts execution with
// the given stop condition.
type AdmissionResult struct {
	Outcome   AdmissionOutcome `json:"outcome"`
	Violation *StopViolation   `json:"violation,omitempty"`
}

type AdmissionOutcome string

const (
	AdmissionAdmitted AdmissionOutcome = "ADMITTED"
	AdmissionRejected AdmissionOutcome = "REJECTED"
)

// Admit performs the admission check for a shadow assignment. It verifies
// that the assignment's subject SHA matches the provided actual SHA, and that
// the executing agent is not also assigned as reviewer for the same subject.
// reviewerAgentIDs must contain all agent IDs assigned as reviewers on this
// subject; the executor must not appear in that set.
func Admit(a ShadowAssignment, actualSubjectSHA string, reviewerAgentIDs []string) AdmissionResult {
	if !strings.EqualFold(a.SubjectSHA, actualSubjectSHA) {
		return AdmissionResult{
			Outcome: AdmissionRejected,
			Violation: &StopViolation{
				Condition: StopExactSubjectMismatch,
				Detail: fmt.Sprintf("assignment subject_sha %q != actual %q",
					a.SubjectSHA, actualSubjectSHA),
			},
		}
	}
	if slices.Contains(reviewerAgentIDs, a.AgentID) {
		return AdmissionResult{
			Outcome: AdmissionRejected,
			Violation: &StopViolation{
				Condition: StopReviewerEqualsExecutor,
				Detail:    fmt.Sprintf("executor agent_id %q is also assigned as reviewer", a.AgentID),
			},
		}
	}
	return AdmissionResult{Outcome: AdmissionAdmitted}
}

// ShadowHeartbeat is a single heartbeat tick emitted by the shadow executor.
// Sequence must be monotonically increasing within a ShadowRunID. The executor
// is fenced if no heartbeat arrives within HeartbeatTimeout.
type ShadowHeartbeat struct {
	ShadowRunID ShadowRunID `json:"shadow_run_id"`
	Generation  uint64      `json:"generation"`
	Sequence    uint64      `json:"sequence"`
	At          time.Time   `json:"at"`
}

func (h ShadowHeartbeat) Validate() error {
	if err := h.ShadowRunID.Validate(); err != nil {
		return fmt.Errorf("shadow heartbeat: %w", err)
	}
	if h.Generation == 0 {
		return errors.New("shadow heartbeat: generation must be positive")
	}
	if h.At.IsZero() {
		return errors.New("shadow heartbeat: at required")
	}
	return nil
}

// IsStale returns true if no heartbeat has arrived within timeout.
func (h ShadowHeartbeat) IsStale(now time.Time, timeout time.Duration) bool {
	if h.At.IsZero() {
		return true
	}
	return now.Sub(h.At) >= timeout
}

// ShadowExecution is the full mutable state of a running shadow execution.
// State transitions: ADMITTED → RUNNING → COMPLETED | FENCED.
// A fenced execution can never transition to COMPLETED.
type ShadowExecution struct {
	Assignment     ShadowAssignment  `json:"assignment"`
	State          ExecutorState     `json:"state"`
	Heartbeats     []ShadowHeartbeat `json:"heartbeats"`
	Result         *AgentResult      `json:"result,omitempty"`
	FenceViolation *StopViolation    `json:"fence_violation,omitempty"`
	StartedAt      time.Time         `json:"started_at"`
	CompletedAt    time.Time         `json:"completed_at,omitempty"`
}

// NewShadowExecution creates a shadow execution in ADMITTED state.
func NewShadowExecution(a ShadowAssignment) (ShadowExecution, error) {
	if err := a.Validate(); err != nil {
		return ShadowExecution{}, err
	}
	return ShadowExecution{Assignment: a, State: ExecutorAdmitted}, nil
}

// Start transitions the execution from ADMITTED to RUNNING.
func (e *ShadowExecution) Start(now time.Time) error {
	if e.State != ExecutorAdmitted {
		return fmt.Errorf("shadow execution: Start requires ADMITTED state, got %q", e.State)
	}
	e.State = ExecutorRunning
	e.StartedAt = now
	return nil
}

// RecordHeartbeat appends a heartbeat. Sequence must be strictly greater than
// the previous sequence. Returns an error if out of order or if not RUNNING.
func (e *ShadowExecution) RecordHeartbeat(h ShadowHeartbeat) error {
	if e.State != ExecutorRunning {
		return fmt.Errorf("shadow execution: heartbeat requires RUNNING state, got %q", e.State)
	}
	if err := h.Validate(); err != nil {
		return err
	}
	if h.Generation != e.Assignment.Generation {
		return fmt.Errorf("shadow execution: heartbeat generation %d != assignment generation %d",
			h.Generation, e.Assignment.Generation)
	}
	if len(e.Heartbeats) > 0 {
		last := e.Heartbeats[len(e.Heartbeats)-1]
		if h.Sequence <= last.Sequence {
			return fmt.Errorf("shadow execution: heartbeat sequence %d must be > last %d",
				h.Sequence, last.Sequence)
		}
	}
	e.Heartbeats = append(e.Heartbeats, h)
	return nil
}

// Complete transitions the execution to COMPLETED with the given result.
// All result identity fields must match the assignment before any state change.
func (e *ShadowExecution) Complete(r AgentResult, now time.Time) error {
	if e.State == ExecutorFenced {
		return errors.New("shadow execution: fenced execution cannot be completed")
	}
	if e.State != ExecutorRunning {
		return fmt.Errorf("shadow execution: Complete requires RUNNING state, got %q", e.State)
	}
	if err := r.Validate(); err != nil {
		return fmt.Errorf("shadow execution: result invalid: %w", err)
	}
	if r.Generation != e.Assignment.Generation {
		return &StaleGenerationError{
			ResultGeneration:     r.Generation,
			AssignmentGeneration: e.Assignment.Generation,
		}
	}
	if !strings.EqualFold(r.SubjectSHA, e.Assignment.SubjectSHA) {
		return fmt.Errorf("shadow execution: result subject_sha %q != assignment %q",
			r.SubjectSHA, e.Assignment.SubjectSHA)
	}
	if r.AssignmentID != e.Assignment.AssignmentID || r.AgentID != e.Assignment.AgentID ||
		r.SessionID != e.Assignment.SessionID || r.InputDigest != e.Assignment.InputDigest {
		return errors.New("shadow execution: result assignment, agent, session or input digest does not match assignment")
	}
	e.State = ExecutorCompleted
	e.Result = &r
	e.CompletedAt = now
	return nil
}

// Fence halts the execution with a stop violation. A fenced execution is
// terminal and cannot be transitioned to COMPLETED.
func (e *ShadowExecution) Fence(v StopViolation, now time.Time) error {
	if err := v.Validate(); err != nil {
		return err
	}
	if e.State == ExecutorCompleted {
		return errors.New("shadow execution: cannot fence a completed execution")
	}
	e.State = ExecutorFenced
	e.FenceViolation = &v
	e.CompletedAt = now
	return nil
}

// CheckHeartbeatTimeout fences the execution if the last heartbeat is stale.
// It is a no-op if not RUNNING.
func (e *ShadowExecution) CheckHeartbeatTimeout(now time.Time, timeout time.Duration) error {
	if e.State != ExecutorRunning {
		return nil
	}
	if len(e.Heartbeats) == 0 {
		if now.Sub(e.StartedAt) >= timeout {
			return e.Fence(StopViolation{
				Condition: StopAssignmentOrGenerationMismatch,
				Detail:    "no heartbeat received within timeout since start",
			}, now)
		}
		return nil
	}
	last := e.Heartbeats[len(e.Heartbeats)-1]
	if last.IsStale(now, timeout) {
		return e.Fence(StopViolation{
			Condition: StopAssignmentOrGenerationMismatch,
			Detail: fmt.Sprintf("heartbeat stale: last at %s, timeout %s",
				last.At.Format(time.RFC3339), timeout),
		}, now)
	}
	return nil
}

// StaleGenerationError is returned when an AgentResult carries a generation
// that does not match the current assignment. It triggers StopStaleGenerationAccepted.
type StaleGenerationError struct {
	ResultGeneration     uint64
	AssignmentGeneration uint64
}

func (e *StaleGenerationError) Error() string {
	return fmt.Sprintf("shadow execution: stale generation: result %d != assignment %d "+
		"(stop condition: %s)", e.ResultGeneration, e.AssignmentGeneration, StopStaleGenerationAccepted)
}
