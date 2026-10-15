package watchdog

import "errors"

var (
	ErrUnsupportedConfirmationRequest     = errors.New("coordinator turn ended with a confirmation request for a deterministic transition")
	ErrDeterministicTransitionNotExecuted = errors.New("a deterministic next transition was identified but not executed")
	ErrReadyAssignmentNotDispatched       = errors.New("an available assignment was not dispatched")
	ErrConflictingBoundary                = errors.New("coordinator result declares more than one boundary: OwnerRequired, HumanActionRequired and BlockedExternal are mutually exclusive")
)

// AvailableAssignmentRef is a non-nil sentinel that records the identity of an
// assignment that was available at turn end but not dispatched.
type AvailableAssignmentRef struct {
	IssueID    string
	AssigneeID string
}

// CoordinatorResult captures what the coordinator decided to do (or not do)
// during its turn. It is separate from CoordinatorState, which captures
// per-session observations. A result is invalid when it carries a deterministic
// next transition that was not executed, or an available assignment that was
// not dispatched, or when it ends with a confirmation request.
type CoordinatorResult struct {
	// Boundary declarations — at most one should be true.
	OwnerRequired       bool
	HumanActionRequired bool
	BlockedExternal     bool

	// NextTransition is set when the coordinator identified the next
	// deterministic State Machine transition.
	NextTransition string
	// TransitionExecuted is true when the coordinator actually performed
	// the transition within the turn.
	TransitionExecuted bool

	// ConfirmationRequested is true when the coordinator's output ends with
	// a question that delegates a deterministic choice back to the user:
	// "Начинаем?", "Продолжать?", "Назначить агента?" and equivalents.
	ConfirmationRequested bool

	// AvailableAssignment is non-nil when a ready assignment exists at turn
	// end.
	AvailableAssignment *AvailableAssignmentRef
	// AssignmentDispatched is true when the coordinator dispatched that
	// assignment during the turn.
	AssignmentDispatched bool

	// FuturePromise is non-empty when the coordinator's output is a passive
	// promise ("I will do X next time") rather than verified current state.
	FuturePromise string
}

// validateDeclaredBoundary checks that at most one boundary flag is set on
// result. It does NOT verify that the declaration is justified; the action
// map layer is responsible for requiring evidence reason codes. A boundary
// declaration short-circuits all completion-gate checks, so a caller that
// sets one of these flags bypasses the confirmation-request and
// assignment-dispatch rules — intentionally, because a boundary is a
// legitimate turn-ending state.
func validateDeclaredBoundary(result CoordinatorResult) error {
	count := 0
	if result.OwnerRequired {
		count++
	}
	if result.HumanActionRequired {
		count++
	}
	if result.BlockedExternal {
		count++
	}
	if count > 1 {
		return ErrConflictingBoundary
	}
	return nil
}

// ValidateCoordinatorCompletion checks that a coordinator turn result is
// consistent with the Completion Gate rules. It returns nil when the result is
// valid, or the first rule violation encountered.
//
// Boundary declarations (OwnerRequired, HumanActionRequired, BlockedExternal)
// are mutually exclusive. When one is set, only validateDeclaredBoundary is
// applied; no further checks run.
//
// Without a declared boundary the following are enforced:
//   - A deterministic next transition that was identified must be executed.
//   - The turn must not end with a confirmation request.
//   - An available assignment must be dispatched.
//   - The turn must not end with a passive future promise.
func ValidateCoordinatorCompletion(state CoordinatorState, result CoordinatorResult) error {
	if result.OwnerRequired || result.HumanActionRequired || result.BlockedExternal {
		return validateDeclaredBoundary(result)
	}

	if result.NextTransition != "" && !result.TransitionExecuted {
		return ErrDeterministicTransitionNotExecuted
	}

	if result.ConfirmationRequested {
		return ErrUnsupportedConfirmationRequest
	}

	if result.AvailableAssignment != nil && !result.AssignmentDispatched {
		return ErrReadyAssignmentNotDispatched
	}

	if result.FuturePromise != "" {
		return ErrPassiveFuturePromise
	}

	// Delegate existing session-level checks to ValidateCompletion.
	return ValidateCompletion(state)
}
