package store

import "errors"

// CoordinatorDelegationEnabled is the compile-time production gate for #93b.
// Production stays disabled until a separate OWNER cutover. Tests may enable a
// harness helper that lives only in _test.go; gates never read env/flag/config.
const CoordinatorDelegationEnabled = false

// ErrCoordinatorDelegationDisabled is returned when a delegation command or
// grant-aware path is invoked while the production const (and harness) are off.
var ErrCoordinatorDelegationDisabled = errors.New("COORDINATOR_DELEGATION_DISABLED")

// ErrCoordinatorDelegationInvalid rejects malformed or forbidden delegation
// commands before any durable write.
var ErrCoordinatorDelegationInvalid = errors.New("COORDINATOR_DELEGATION_INVALID")

// ErrCoordinatorDelegationExhausted blocks work when no eligible successor
// remains under an active grant (ADR §11).
var ErrCoordinatorDelegationExhausted = errors.New("COORDINATOR_DELEGATION_EXHAUSTED")

// delegationHarnessEnabled is false in production. Only _test.go may set it.
var delegationHarnessEnabled bool

func coordinatorDelegationActive() bool {
	return CoordinatorDelegationEnabled || delegationHarnessEnabled
}
