// Package watchdog is intended to run in a separate supervisor process.
package watchdog

import (
	"context"
	"coord/fsm"
	"fmt"
	"time"
)

// Backend must serialize recovery claims across watchdog processes.
// FenceOld must stop the old process and verify its worktree lock is released.
// Spawn must create an executor that waits for TASK before writing.
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
	runs, err := w.Backend.Candidates(ctx)
	if err != nil {
		return err
	}
	for _, r := range runs {
		if !r.RecoveryNeeded || r.Status != fsm.Stalled {
			continue
		}
		if err = w.recover(ctx, r); err != nil {
			return err
		}
	}
	return nil
}
func (w Watchdog) recover(ctx context.Context, r fsm.Run) (err error) {
	ok, err := w.Backend.Claim(ctx, r.RunID, r.Fence)
	if err != nil || !ok {
		return err
	}
	defer func() {
		releaseErr := w.Backend.Release(ctx, r.RunID, r.Fence)
		if err == nil {
			err = releaseErr
		}
	}()
	if err = w.Backend.FenceOld(ctx, r); err != nil {
		return fmt.Errorf("fence old owner: %w", err)
	}
	if err = w.Backend.VerifyWorktree(ctx, r); err != nil {
		return w.Backend.Fail(ctx, r.RunID, r.Fence, "worktree verification failed")
	}
	session, err := w.Backend.Spawn(ctx, r)
	if err != nil {
		return err
	}
	if err = w.Backend.Transfer(ctx, r.RunID, session, r.Fence); err != nil {
		stopErr := w.Backend.Stop(ctx, session)
		if stopErr != nil {
			return fmt.Errorf("transfer: %v; stop replacement: %w", err, stopErr)
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
