package watchdog

import (
	"errors"
	"testing"
)

func TestValidateCoordinatorCompletion_NoBoundary_Valid(t *testing.T) {
	result := CoordinatorResult{
		NextTransition:     "START_ISSUE_51",
		TransitionExecuted: true,
	}
	if err := ValidateCoordinatorCompletion(CoordinatorState{}, result); err != nil {
		t.Errorf("expected nil, got: %v", err)
	}
}

func TestValidateCoordinatorCompletion_DeterministicTransitionNotExecuted(t *testing.T) {
	result := CoordinatorResult{
		NextTransition:     "START_ISSUE_51",
		TransitionExecuted: false,
	}
	err := ValidateCoordinatorCompletion(CoordinatorState{}, result)
	if !errors.Is(err, ErrDeterministicTransitionNotExecuted) {
		t.Errorf("expected ErrDeterministicTransitionNotExecuted, got: %v", err)
	}
}

func TestValidateCoordinatorCompletion_ConfirmationRequested(t *testing.T) {
	result := CoordinatorResult{
		ConfirmationRequested: true,
	}
	err := ValidateCoordinatorCompletion(CoordinatorState{}, result)
	if !errors.Is(err, ErrUnsupportedConfirmationRequest) {
		t.Errorf("expected ErrUnsupportedConfirmationRequest, got: %v", err)
	}
}

func TestValidateCoordinatorCompletion_ReadyAssignmentNotDispatched(t *testing.T) {
	result := CoordinatorResult{
		AvailableAssignment:  &AvailableAssignmentRef{IssueID: "51", AssigneeID: "o42"},
		AssignmentDispatched: false,
	}
	err := ValidateCoordinatorCompletion(CoordinatorState{}, result)
	if !errors.Is(err, ErrReadyAssignmentNotDispatched) {
		t.Errorf("expected ErrReadyAssignmentNotDispatched, got: %v", err)
	}
}

func TestValidateCoordinatorCompletion_AvailableAssignmentDispatched(t *testing.T) {
	result := CoordinatorResult{
		AvailableAssignment:  &AvailableAssignmentRef{IssueID: "51", AssigneeID: "o42"},
		AssignmentDispatched: true,
	}
	if err := ValidateCoordinatorCompletion(CoordinatorState{}, result); err != nil {
		t.Errorf("expected nil for dispatched assignment, got: %v", err)
	}
}

func TestValidateCoordinatorCompletion_FuturePromise(t *testing.T) {
	result := CoordinatorResult{
		FuturePromise: "I will do X next time",
	}
	err := ValidateCoordinatorCompletion(CoordinatorState{}, result)
	if !errors.Is(err, ErrPassiveFuturePromise) {
		t.Errorf("expected ErrPassiveFuturePromise, got: %v", err)
	}
}

func TestValidateCoordinatorCompletion_OwnerRequiredSingleBoundary(t *testing.T) {
	result := CoordinatorResult{OwnerRequired: true}
	if err := ValidateCoordinatorCompletion(CoordinatorState{}, result); err != nil {
		t.Errorf("single OwnerRequired boundary is valid, got: %v", err)
	}
}

func TestValidateCoordinatorCompletion_HumanActionRequiredSingleBoundary(t *testing.T) {
	result := CoordinatorResult{HumanActionRequired: true}
	if err := ValidateCoordinatorCompletion(CoordinatorState{}, result); err != nil {
		t.Errorf("single HumanActionRequired boundary is valid, got: %v", err)
	}
}

func TestValidateCoordinatorCompletion_BlockedExternalSingleBoundary(t *testing.T) {
	result := CoordinatorResult{BlockedExternal: true}
	if err := ValidateCoordinatorCompletion(CoordinatorState{}, result); err != nil {
		t.Errorf("single BlockedExternal boundary is valid, got: %v", err)
	}
}

func TestValidateCoordinatorCompletion_MultipleBoundariesRejected(t *testing.T) {
	result := CoordinatorResult{OwnerRequired: true, HumanActionRequired: true}
	err := ValidateCoordinatorCompletion(CoordinatorState{}, result)
	if !errors.Is(err, ErrConflictingBoundary) {
		t.Errorf("expected ErrConflictingBoundary for multiple boundaries, got: %v", err)
	}
}

// Mutation target: removing the ConfirmationRequested check must kill this test.
func TestMutation_ConfirmationRequestedCheckRequired(t *testing.T) {
	result := CoordinatorResult{
		ConfirmationRequested: true,
		NextTransition:        "",
	}
	err := ValidateCoordinatorCompletion(CoordinatorState{}, result)
	if err == nil {
		t.Error("ConfirmationRequested=true must be rejected; removing this check is a gate violation")
	}
}

// Mutation target: removing the DeterministicTransition check must kill this test.
func TestMutation_DeterministicTransitionCheckRequired(t *testing.T) {
	result := CoordinatorResult{
		NextTransition:     "START_ISSUE_51",
		TransitionExecuted: false,
	}
	err := ValidateCoordinatorCompletion(CoordinatorState{}, result)
	if err == nil {
		t.Error("unexecuted deterministic transition must be rejected; removing this check is a gate violation")
	}
}

// Mutation target: removing the AvailableAssignment check must kill this test.
func TestMutation_ReadyAssignmentCheckRequired(t *testing.T) {
	result := CoordinatorResult{
		AvailableAssignment:  &AvailableAssignmentRef{IssueID: "51", AssigneeID: "o42"},
		AssignmentDispatched: false,
	}
	err := ValidateCoordinatorCompletion(CoordinatorState{}, result)
	if err == nil {
		t.Error("undispatched available assignment must be rejected; removing this check is a gate violation")
	}
}

// Boundary isolation: OwnerRequired must not skip other checks when not set.
func TestValidateCoordinatorCompletion_BoundaryIsolation(t *testing.T) {
	// No boundary but confirmation requested — must fail.
	result := CoordinatorResult{
		OwnerRequired:         false,
		ConfirmationRequested: true,
	}
	err := ValidateCoordinatorCompletion(CoordinatorState{}, result)
	if !errors.Is(err, ErrUnsupportedConfirmationRequest) {
		t.Errorf("expected ErrUnsupportedConfirmationRequest without boundary, got: %v", err)
	}
}

// DelegatesToValidateCompletion: session-level passive wait is also caught.
func TestValidateCoordinatorCompletion_DelegatesToValidateCompletion(t *testing.T) {
	state := CoordinatorState{
		PendingAssignments: []PendingAssignment{
			{SessionState: SessionDelivered},
		},
	}
	result := CoordinatorResult{} // no coordinator-level violation
	err := ValidateCoordinatorCompletion(state, result)
	if !errors.Is(err, ErrDeliveredNotAdmitted) {
		t.Errorf("expected delegation to catch ErrDeliveredNotAdmitted, got: %v", err)
	}
}
