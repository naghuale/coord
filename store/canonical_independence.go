package store

import (
	"errors"
	"fmt"
	"reflect"

	"coord/fsm"
	"coord/roles"
	"coord/task"
	"coord/taskid"
)

type roleAssignmentIdentity struct {
	taskID taskid.ID
	seq    uint64
	role   roles.Role
	agent  string
}

var (
	ErrNoIndependentReviewer       = errors.New("no independent reviewer: NO_INDEPENDENT_REVIEWER")
	ErrNoIndependentAuditor        = errors.New("no independent auditor: NO_INDEPENDENT_AUDITOR")
	ErrExecutorRoleHistoryConflict = errors.New("executor role history conflict: EXECUTOR_ROLE_HISTORY_CONFLICT")
)

// validateCanonicalRoleIndependence checks stable AgentID identities across
// every Task-scoped Run and its append-only role history. Model and session
// address are deliberately not identity keys.
func validateCanonicalRoleIndependence(state CanonicalState) error {
	transfers := make(map[taskid.ID]map[string][]uint64)
	runEventSeqs := make(map[taskid.ID]map[string][]uint64)
	for _, event := range state.Events {
		if event.RunID != "" {
			if runEventSeqs[event.TaskID] == nil {
				runEventSeqs[event.TaskID] = make(map[string][]uint64)
			}
			runEventSeqs[event.TaskID][event.RunID] = append(runEventSeqs[event.TaskID][event.RunID], event.EventSeq)
		}
		if event.Type != task.TaskOwnershipTransferred {
			continue
		}
		if transfers[event.TaskID] == nil {
			transfers[event.TaskID] = make(map[string][]uint64)
		}
		transfers[event.TaskID][event.RunID] = append(transfers[event.TaskID][event.RunID], event.EventSeq)
	}
	assignments := make([]roleAssignmentIdentity, 0)

	for _, run := range state.Runs {
		if run.TaskID == "" {
			continue
		}
		selections := runRoleSelections(run)
		role := runRole(run)
		if role != roles.Executor && role != roles.Reviewer && role != roles.Auditor {
			continue
		}
		markerSeqs := transfers[run.TaskID][run.RunID]
		eventSeqs := runEventSeqs[run.TaskID][run.RunID]
		if role == roles.Executor && len(selections) == 0 {
			for _, seq := range markerSeqs {
				assignments = append(assignments, roleAssignmentIdentity{taskID: run.TaskID, seq: seq, role: role})
			}
			continue
		}
		if role != roles.Executor && run.RoleSelection == nil {
			return roleIndependenceError(role, run.TaskID, "Review/Audit Run has no selected stable AgentID")
		}
		extraSelectionSeqs, ok := selectionEventAnchors(eventSeqs, markerSeqs, len(selections)-len(markerSeqs), role)
		if !ok {
			return fmt.Errorf("task-scoped %s Run %q role history has no event anchor for every selection", role, run.RunID)
		}
		for i, selection := range selections {
			if selection.Profile.Role != role {
				return fmt.Errorf("task-scoped Run %q role history changes role from %s", run.RunID, role)
			}
			var seq uint64
			if i < len(markerSeqs) {
				seq = markerSeqs[i]
			} else {
				seq = extraSelectionSeqs[i-len(markerSeqs)]
			}
			assignments = append(assignments, roleAssignmentIdentity{
				taskID: run.TaskID, seq: seq, role: role, agent: selection.AgentID,
			})
		}
	}

	for _, candidate := range assignments {
		if candidate.role != roles.Reviewer && candidate.role != roles.Auditor {
			continue
		}
		if candidate.agent == "" {
			return roleIndependenceError(candidate.role, candidate.taskID, "selected role has no stable AgentID")
		}
		for _, executor := range assignments {
			if executor.taskID != candidate.taskID || executor.role != roles.Executor || !executorAssignmentAtOrBefore(executor.seq, candidate.seq) {
				continue
			}
			if executor.agent == "" {
				return roleIndependenceError(candidate.role, candidate.taskID, "prior Executor assignment has no verifiable stable AgentID")
			}
			if executor.agent == candidate.agent {
				return roleIndependenceError(candidate.role, candidate.taskID, fmt.Sprintf("AgentID %q appears in prior Executor history", candidate.agent))
			}
		}
	}

	for _, candidate := range assignments {
		if candidate.role != roles.Executor {
			continue
		}
		for _, prior := range assignments {
			if prior.taskID != candidate.taskID ||
				(prior.role != roles.Reviewer && prior.role != roles.Auditor) ||
				prior.seq > candidate.seq {
				continue
			}
			if candidate.agent == "" {
				return fmt.Errorf("%w for Task %q: Executor assignment has no verifiable stable AgentID after prior %s assignment", ErrExecutorRoleHistoryConflict, candidate.taskID, prior.role)
			}
			if prior.agent == candidate.agent {
				return fmt.Errorf("%w for Task %q: AgentID %q previously held %s role", ErrExecutorRoleHistoryConflict, candidate.taskID, candidate.agent, prior.role)
			}
		}
	}
	return nil
}

func selectionEventAnchors(eventSeqs, markerSeqs []uint64, extraCount int, role roles.Role) ([]uint64, bool) {
	if extraCount <= 0 {
		return nil, true
	}
	if len(markerSeqs) == 0 {
		return nil, false
	}
	var eligible []uint64
	for _, seq := range eventSeqs {
		if seq > markerSeqs[len(markerSeqs)-1] {
			eligible = append(eligible, seq)
		}
	}
	if len(eligible) < extraCount {
		return nil, false
	}
	result := make([]uint64, extraCount)
	if role == roles.Executor {
		copy(result, eligible[:extraCount])
		return result, true
	}
	copy(result, eligible[len(eligible)-extraCount:])
	return result, true
}

// Executor assignment at the same or an earlier Task event sequence is part
// of the history that a Review/Audit assignment must be independent from.
func executorAssignmentAtOrBefore(executorSeq, candidateSeq uint64) bool {
	return executorSeq <= candidateSeq
}

func runRoleSelections(run fsm.Run) []roles.Selection {
	selections := append([]roles.Selection(nil), run.RoleHistory...)
	if run.RoleSelection != nil && (len(selections) == 0 || !reflect.DeepEqual(selections[len(selections)-1], *run.RoleSelection)) {
		selections = append(selections, *run.RoleSelection)
	}
	return selections
}

func roleIndependenceError(role roles.Role, taskID taskid.ID, detail string) error {
	switch role {
	case roles.Reviewer:
		return fmt.Errorf("%w for Task %q: %s", ErrNoIndependentReviewer, taskID, detail)
	case roles.Auditor:
		return fmt.Errorf("%w for Task %q: %s", ErrNoIndependentAuditor, taskID, detail)
	default:
		return fmt.Errorf("unknown independent role %q for Task %q", role, taskID)
	}
}
