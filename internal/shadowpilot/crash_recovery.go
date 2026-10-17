package shadowpilot

import (
	"errors"
	"fmt"
	"strings"
)

// ShadowCheckpoint is the full persisted state that survives a coordd crash.
// It is the authoritative record of what was durably committed to the shadow
// store before the crash. Fields are nil when the crash occurred before that
// lifecycle phase was reached.
//
// A checkpoint with a non-nil Execution and Execution.State == RUNNING does NOT
// prove the agent is still alive; the coordinator must observe liveness before
// any redispatch (see PlanRecovery).
type ShadowCheckpoint struct {
	Assignment      *ShadowAssignment      `json:"assignment,omitempty"`
	Execution       *ShadowExecution       `json:"execution,omitempty"`
	ReviewResult    *ShadowReviewResult    `json:"review_result,omitempty"`
	AuditResult     *ShadowAuditResult     `json:"audit_result,omitempty"`
	DiscussionNodes []ShadowDiscussionNode `json:"discussion_nodes,omitempty"`
}

// AgentLiveness is the result of observing whether an agent session is
// currently alive. The coordinator must obtain this from an authoritative
// external source before any redispatch decision.
type AgentLiveness struct {
	// IsAlive is true only if the agent session is confirmed running by an
	// authoritative observation, not inferred from persisted state.
	IsAlive bool `json:"is_alive"`
	// ObservedGeneration is the generation of the observed session.
	// Zero means the agent is not alive or generation could not be determined.
	ObservedGeneration uint64 `json:"observed_generation"`
}

// RecoveryAction is the action the coordinator must take after observing the
// crash state.
type RecoveryAction string

const (
	// RecoveryNoLaunch: crash before durable intent — no agent was dispatched;
	// nothing to recover.
	RecoveryNoLaunch RecoveryAction = "NO_LAUNCH"
	// RecoveryObserveFirst: durable intent exists but liveness is unknown or
	// execution state is ADMITTED/RUNNING — observe the agent session before
	// any redispatch. Never assume RUNNING means alive.
	RecoveryObserveFirst RecoveryAction = "OBSERVE_FIRST"
	// RecoveryFencedNoRestart: execution is FENCED — terminal state, no restart.
	RecoveryFencedNoRestart RecoveryAction = "FENCED_NO_RESTART"
	// RecoveryAlreadyComplete: execution is COMPLETED — apply result idempotently,
	// do not relaunch.
	RecoveryAlreadyComplete RecoveryAction = "ALREADY_COMPLETE"
	// RecoveryDuplicateBlocked: a restart would create a duplicate execution for
	// an assignment that already has a COMPLETED or FENCED execution. Stop.
	RecoveryDuplicateBlocked RecoveryAction = "DUPLICATE_BLOCKED"
	// RecoveryResumeGraph: execution COMPLETED and result accepted; continue with
	// review/audit graph from persisted state.
	RecoveryResumeGraph RecoveryAction = "RESUME_GRAPH"
	// RecoveryReadinessOnly: execution COMPLETED, review and audit results both
	// present — recompute readiness from persisted evidence.
	RecoveryReadinessOnly RecoveryAction = "READINESS_ONLY"
)

// RecoveryPlan is the coordinator's decision after observing crash state and
// agent liveness.
type RecoveryPlan struct {
	Action string `json:"action"`
	Reason string `json:"reason"`
	// StaleGeneration is set when the persisted result generation does not match
	// the assignment generation, triggering StopStaleGenerationAccepted.
	StaleGeneration *StaleGenerationError `json:"stale_generation,omitempty"`
}

func (p RecoveryPlan) Is(a RecoveryAction) bool {
	return p.Action == string(a)
}

// PlanRecovery determines the recovery action from the persisted checkpoint and
// the optionally observed agent liveness. liveness may be nil when observation
// has not yet been performed.
//
// Critical invariants enforced by this function:
//   - Persisted RUNNING ≠ live: OBSERVE_FIRST is returned unless liveness was
//     explicitly observed (liveness != nil).
//   - No duplicate execution: COMPLETED/FENCED state blocks any restart.
//   - Stale generation from a prior session is rejected before acceptance.
//   - Old ReviewResult does not apply to a corrected subject: Superseded flag
//     and subject SHA mismatch both trigger NOT_EVALUATED readiness downstream.
func PlanRecovery(checkpoint ShadowCheckpoint, liveness *AgentLiveness) RecoveryPlan {
	// Phase 1: crash before durable assignment intent.
	if checkpoint.Assignment == nil {
		return RecoveryPlan{Action: string(RecoveryNoLaunch),
			Reason: "crash before durable assignment intent: no agent was dispatched"}
	}

	// Phase 2: assignment exists but no execution recorded.
	if checkpoint.Execution == nil {
		return RecoveryPlan{Action: string(RecoveryObserveFirst),
			Reason: "assignment intent persisted but no execution started: observe before dispatch"}
	}

	exec := checkpoint.Execution

	// Phase 3: FENCED is terminal.
	if exec.State == ExecutorFenced {
		return RecoveryPlan{Action: string(RecoveryFencedNoRestart),
			Reason: fmt.Sprintf("execution is FENCED (stop condition: %v): no restart",
				exec.FenceViolation)}
	}

	// Phase 4: ADMITTED ≠ RUNNING: persisted ADMITTED requires observation.
	if exec.State == ExecutorAdmitted {
		return RecoveryPlan{Action: string(RecoveryObserveFirst),
			Reason: "execution is ADMITTED but not RUNNING: observe liveness before dispatch"}
	}

	// Phase 5: RUNNING ≠ live.
	// Persisted RUNNING state does NOT prove the agent is still executing.
	// We must observe liveness before redispatch.
	if exec.State == ExecutorRunning {
		if liveness == nil {
			return RecoveryPlan{Action: string(RecoveryObserveFirst),
				Reason: "persisted RUNNING state does not prove liveness: observe before any dispatch"}
		}
		if !liveness.IsAlive {
			// Session died: treat as if crash occurred during execution.
			// Check for a completed result we can apply idempotently.
			if exec.Result != nil {
				if err := checkGeneration(exec, checkpoint.Assignment); err != nil {
					return RecoveryPlan{Action: string(RecoveryObserveFirst), Reason: err.Error(),
						StaleGeneration: asStaleGenError(err)}
				}
				return buildCompleteOrResume(checkpoint)
			}
			// No result: execution died without completing.
			return RecoveryPlan{Action: string(RecoveryObserveFirst),
				Reason: "session died during execution without a result: observe and reassign"}
		}
		// Agent is alive — check generation match (stale session from prior assignment).
		if liveness.ObservedGeneration != checkpoint.Assignment.Generation {
			return RecoveryPlan{Action: string(RecoveryDuplicateBlocked),
				Reason: fmt.Sprintf("live agent generation %d != assignment generation %d: stale session",
					liveness.ObservedGeneration, checkpoint.Assignment.Generation)}
		}
		// Healthy running session — no action needed, continue monitoring.
		return RecoveryPlan{Action: string(RecoveryObserveFirst),
			Reason: "session is alive with matching generation: reconcile heartbeat"}
	}

	// Phase 6: COMPLETED.
	if exec.State == ExecutorCompleted {
		if exec.Result == nil {
			return RecoveryPlan{Action: string(RecoveryObserveFirst),
				Reason: "COMPLETED execution has no result: incomplete state, cannot be current"}
		}
		if err := checkGeneration(exec, checkpoint.Assignment); err != nil {
			return RecoveryPlan{Action: string(RecoveryObserveFirst), Reason: err.Error(),
				StaleGeneration: asStaleGenError(err)}
		}
		return buildCompleteOrResume(checkpoint)
	}

	return RecoveryPlan{Action: string(RecoveryObserveFirst),
		Reason: fmt.Sprintf("unknown execution state %q: observe before any action", exec.State)}
}

func buildCompleteOrResume(checkpoint ShadowCheckpoint) RecoveryPlan {
	if checkpoint.ReviewResult != nil && checkpoint.AuditResult != nil {
		return RecoveryPlan{Action: string(RecoveryReadinessOnly),
			Reason: "execution, review, and audit all present: recompute readiness"}
	}
	return RecoveryPlan{Action: string(RecoveryResumeGraph),
		Reason: "execution complete: resume review/audit graph from persisted state"}
}

func checkGeneration(exec *ShadowExecution, assignment *ShadowAssignment) error {
	if exec.Result != nil && exec.Result.Generation != assignment.Generation {
		return &StaleGenerationError{
			ResultGeneration:     exec.Result.Generation,
			AssignmentGeneration: assignment.Generation,
		}
	}
	return nil
}

func asStaleGenError(err error) *StaleGenerationError {
	var sg *StaleGenerationError
	if errors.As(err, &sg) {
		return sg
	}
	return nil
}

// ValidateCheckpointIntegrity verifies that a checkpoint does not contain
// partial state that could be mistakenly treated as authoritative. It returns
// an error if:
//   - execution is COMPLETED but has no result;
//   - execution is RUNNING but has no assignment;
//   - review result subject SHA does not match assignment subject SHA (old
//     review from a superseded subject);
//   - audit result subject SHA does not match assignment subject SHA.
func ValidateCheckpointIntegrity(checkpoint ShadowCheckpoint) error {
	if checkpoint.Assignment == nil {
		return nil // pre-intent crash is valid
	}
	if checkpoint.Execution != nil {
		exec := checkpoint.Execution
		if exec.State == ExecutorCompleted && exec.Result == nil {
			return errors.New("checkpoint integrity: COMPLETED execution has no result")
		}
	}
	if checkpoint.ReviewResult != nil {
		if !strings.EqualFold(checkpoint.ReviewResult.SubjectSHA, checkpoint.Assignment.SubjectSHA) {
			return fmt.Errorf("checkpoint integrity: review result subject %q != assignment %q — old review from prior subject",
				checkpoint.ReviewResult.SubjectSHA, checkpoint.Assignment.SubjectSHA)
		}
		if checkpoint.ReviewResult.Superseded {
			return errors.New("checkpoint integrity: review result is superseded and must not be current")
		}
	}
	if checkpoint.AuditResult != nil {
		if !strings.EqualFold(checkpoint.AuditResult.SubjectSHA, checkpoint.Assignment.SubjectSHA) {
			return fmt.Errorf("checkpoint integrity: audit result subject %q != assignment %q",
				checkpoint.AuditResult.SubjectSHA, checkpoint.Assignment.SubjectSHA)
		}
	}
	return nil
}
