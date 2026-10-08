package store

import (
	"fmt"

	"coord/fsm"
)

// validateCanonicalAgentSessionBindings checks the persisted binding snapshot
// for Task-scoped Runs. It deliberately permits an older generation and a
// revoked but still recorded binding: commands will enforce freshness in the
// later command boundary. Missing history cannot be repaired during startup.
func validateCanonicalAgentSessionBindings(state CanonicalState) error {
	for _, run := range state.Runs {
		if run.TaskID == "" {
			continue
		}
		if run.AgentSessionGeneration == 0 {
			if !run.Terminal() {
				return fmt.Errorf("active Task-scoped Run %q has no AgentSession generation", run.RunID)
			}
			continue
		}
		if err := validateRunAgentSessionBinding(state, run, false); err != nil {
			return err
		}
	}
	return nil
}

func validateRunAgentSessionBinding(state CanonicalState, run fsm.Run, requireCurrent bool) error {
	session, exists := state.AgentSessions[run.OwnerSession]
	if !exists {
		return fmt.Errorf("task-scoped Run %q references missing AgentSession %q", run.RunID, run.OwnerSession)
	}
	if run.RoleSelection == nil || run.RoleSelection.AgentID == "" {
		return fmt.Errorf("task-scoped Run %q has no stable AgentID selection", run.RunID)
	}
	if run.RoleSelection.AgentID != session.AgentID {
		return fmt.Errorf("task-scoped Run %q AgentID does not match AgentSession %q", run.RunID, run.OwnerSession)
	}
	if run.AgentSessionGeneration > session.Generation {
		return fmt.Errorf("task-scoped Run %q AgentSession generation exceeds current binding generation", run.RunID)
	}
	if requireCurrent {
		if !session.Active() {
			return fmt.Errorf("new Task-scoped Run %q requires an active AgentSession", run.RunID)
		}
		if run.AgentSessionGeneration != session.Generation {
			return fmt.Errorf("new Task-scoped Run %q must record the current AgentSession generation", run.RunID)
		}
	}
	return nil
}

// validateCanonicalAgentSessionAssignments constrains assignment and recovery
// transitions. A newly assigned Run must bind to the current active session;
// an existing Run may change its binding only as part of ownership transfer.
func validateCanonicalAgentSessionAssignments(previous, next CanonicalState) error {
	for runID, run := range next.Runs {
		if run.TaskID == "" {
			continue
		}
		prior, existed := previous.Runs[runID]
		if !existed {
			if err := validateRunAgentSessionBinding(next, run, true); err != nil {
				return err
			}
			continue
		}
		priorAgentID := ""
		if prior.RoleSelection != nil {
			priorAgentID = prior.RoleSelection.AgentID
		}
		nextAgentID := ""
		if run.RoleSelection != nil {
			nextAgentID = run.RoleSelection.AgentID
		}
		bindingChanged := prior.OwnerSession != run.OwnerSession ||
			prior.AgentSessionGeneration != run.AgentSessionGeneration || priorAgentID != nextAgentID
		if !bindingChanged {
			continue
		}
		if prior.OwnerSession == run.OwnerSession {
			return fmt.Errorf("task-scoped Run %q AgentID or generation can change only with ownership transfer", runID)
		}
		if err := validateRunAgentSessionBinding(next, run, true); err != nil {
			return err
		}
	}
	return nil
}
