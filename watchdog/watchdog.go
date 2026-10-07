// Package watchdog is intended to run in a separate supervisor process.
package watchdog

import (
	"context"
	"coord/fsm"
	"errors"
	"fmt"
	"time"
)

// Backend must serialize recovery claims across watchdog processes.
// FenceOld must stop the old process and verify its worktree lock is released.
// Spawn must create an executor that waits for TASK before writing.
// Stop must confirm replacement termination and writer-lock release, even when
// Transfer returned an error after committing ownership.
type Backend interface {
	Candidates(context.Context) ([]fsm.Run, error)
	Claim(context.Context, string, uint64) (bool, error)
	Release(context.Context, string, uint64) error
	FenceOld(context.Context, fsm.Run) error
	VerifyWorktree(context.Context, fsm.Run) error
	Spawn(context.Context, fsm.Run) (string, error)
	Transfer(context.Context, string, string, uint64) error
	Stop(context.Context, string) error
	Fail(context.Context, string, uint64, string) error
}
type Watchdog struct {
	Backend  Backend
	Interval time.Duration
	OnError  func(error)
}

func (w Watchdog) Sweep(ctx context.Context) error {
	if w.Backend == nil {
		return fmt.Errorf("watchdog backend required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	runs, err := w.Backend.Candidates(ctx)
	if err != nil {
		return err
	}
	var failures []error
	for _, r := range runs {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}
		if !r.RecoveryNeeded || r.Status != fsm.Stalled {
			continue
		}
		if err = w.recover(ctx, r); err != nil {
			failures = append(failures, fmt.Errorf("recover run %s: %w", r.RunID, err))
		}
	}
	return errors.Join(failures...)
}
func (w Watchdog) recover(ctx context.Context, r fsm.Run) (err error) {
	ok, err := w.Backend.Claim(ctx, r.RunID, r.Fence)
	if err != nil || !ok {
		return err
	}
	releaseSafe := true
	defer func() {
		if !releaseSafe {
			return
		}
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if releaseErr := w.Backend.Release(cleanup, r.RunID, r.Fence); releaseErr != nil {
			err = errors.Join(err, fmt.Errorf("release recovery claim: %w", releaseErr))
		}
	}()
	if err = w.Backend.FenceOld(ctx, r); err != nil {
		return fmt.Errorf("fence old owner: %w", err)
	}
	if err = w.Backend.VerifyWorktree(ctx, r); err != nil {
		return errors.Join(fmt.Errorf("verify worktree: %w", err), w.Backend.Fail(ctx, r.RunID, r.Fence, "worktree verification failed"))
	}
	session, err := w.Backend.Spawn(ctx, r)
	if err != nil {
		if session == "" {
			// A failed spawn without an address cannot prove partial-process cleanup.
			releaseSafe = false
			return fmt.Errorf("spawn replacement without confirmed cleanup: %w", err)
		}
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if stopErr := w.Backend.Stop(cleanup, session); stopErr != nil {
			releaseSafe = false
			return errors.Join(fmt.Errorf("spawn replacement: %w", err), fmt.Errorf("stop partial replacement: %w", stopErr))
		}
		return fmt.Errorf("spawn replacement: %w", err)
	}
	if session == "" || session == r.OwnerSession {
		releaseSafe = false
		return fmt.Errorf("invalid replacement identity; cleanup requires operator verification")
	}
	if err = w.Backend.Transfer(ctx, r.RunID, session, r.Fence); err != nil {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		stopErr := w.Backend.Stop(cleanup, session)
		if stopErr != nil {
			// Keep the claim until an operator confirms the replacement is stopped.
			releaseSafe = false
			return errors.Join(fmt.Errorf("transfer: %w", err), fmt.Errorf("stop replacement: %w", stopErr))
		}
		return err
	}
	return nil
}
func (w Watchdog) Loop(ctx context.Context) error {
	if w.Backend == nil || w.Interval <= 0 {
		return fmt.Errorf("invalid watchdog configuration")
	}
	tick := time.NewTicker(w.Interval)
	defer tick.Stop()
	for {
		if err := w.Sweep(ctx); err != nil {
			if w.OnError == nil {
				return err
			}
			w.OnError(err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}
