package coordinator

import (
	"context"
	"errors"
	"fmt"

	"coord/internal/watchdog"
)

// WatchdogRunner is implemented by watchdog.Watchdog.
type WatchdogRunner interface {
	Run(ctx context.Context) error
}

// RunWithWatchdog runs the coordinator state machine and watchdog concurrently
// and returns once both have stopped.
//
// Either side finishing — successfully or with an error — cancels the other.
// A watchdog that only stops on error would keep ticking after the state
// machine completed and would hold the caller open forever, so success cancels
// too. The first non-context error is returned; context cancellation caused by
// our own cancel is not an error.
func RunWithWatchdog(ctx context.Context, runner WatchdogRunner, stateMachine func(context.Context) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	errc := make(chan error, 2)

	go func() {
		errc <- runner.Run(ctx)
	}()
	go func() {
		errc <- stateMachine(ctx)
	}()

	var first error
	for i := 0; i < 2; i++ {
		err := <-errc
		// Whoever finishes first ends the run, whatever the outcome.
		cancel()
		if err != nil && !errors.Is(err, context.Canceled) && first == nil {
			first = err
		}
	}
	return first
}

// ValidateCompletionGate wraps watchdog.ValidateCompletion for use in the
// coordinator turn loop. Returns a descriptive error if the coordinator turn
// would end with a stale session or passive promise.
func ValidateCompletionGate(state watchdog.CoordinatorState) error {
	if err := watchdog.ValidateCompletion(state); err != nil {
		return fmt.Errorf("coordinator completion gate: %w", err)
	}
	return nil
}
