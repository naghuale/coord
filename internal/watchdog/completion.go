package watchdog

import "errors"

var (
	ErrDeliveredNotAdmitted    = errors.New("assignment is DELIVERED but not ADMITTED: passive wait prohibited")
	ErrQueuedNotRunning        = errors.New("assignment is QUEUED but not RUNNING: passive wait prohibited")
	ErrHeartbeatStale          = errors.New("heartbeat is stale and reconcile was not attempted")
	ErrCoordinatorStoppedEarly = errors.New("independent work was available but not performed")
	ErrPassiveFuturePromise    = errors.New("coordinator turn ended with a future promise instead of verified state")
)

type PendingAssignment struct {
	SessionState             SessionState
	HeartbeatStale           bool
	ReconcileAttempted       bool
	IndependentWorkAvailable bool
	IndependentWorkPerformed bool
}

type CoordinatorState struct {
	PendingAssignments []PendingAssignment
	FuturePromise      string
}

func ValidateCompletion(state CoordinatorState) error {
	for _, assignment := range state.PendingAssignments {
		switch {
		case assignment.SessionState == SessionDelivered:
			return ErrDeliveredNotAdmitted
		case assignment.SessionState == SessionQueued:
			return ErrQueuedNotRunning
		case assignment.HeartbeatStale && !assignment.ReconcileAttempted:
			return ErrHeartbeatStale
		case assignment.IndependentWorkAvailable && !assignment.IndependentWorkPerformed:
			return ErrCoordinatorStoppedEarly
		}
	}
	if state.FuturePromise != "" {
		return ErrPassiveFuturePromise
	}
	return nil
}
