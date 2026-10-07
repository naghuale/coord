package coord

import (
	"context"
	"coord/fsm"
	"fmt"
)

// ClaimRecovery persists an exclusive claim; it never expires while a writer may live.
func (c *Coordinator) ClaimRecovery(id string, fence uint64, claim string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if claim == "" {
		return fmt.Errorf("empty recovery claim")
	}
	return c.store.Update(func(runs map[string]fsm.Run) error {
		r, ok := runs[id]
		if !ok || r.Fence != fence || !r.RecoveryNeeded || r.Status != fsm.Stalled || r.RecoveryClaim != "" || !c.cfg.Now().Before(r.Deadline) {
			return fmt.Errorf("recovery already claimed or stale")
		}
		r.RecoveryClaim = claim
		runs[id] = r
		return nil
	})
}
func (c *Coordinator) ReleaseRecovery(id string, fence uint64, claim string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.store.Update(func(runs map[string]fsm.Run) error {
		r, ok := runs[id]
		if !ok {
			return fmt.Errorf("unknown run")
		}
		if r.Fence != fence {
			return nil
		}
		if r.RecoveryClaim != claim {
			return fmt.Errorf("not claim owner")
		}
		r.RecoveryClaim = ""
		runs[id] = r
		return nil
	})
}

// RecoverClaimed transfers ownership after the watchdog has fenced the old writer.
func (c *Coordinator) RecoverClaimed(ctx context.Context, id, owner string, fence uint64, claim string) error {
	return c.recover(ctx, id, owner, fence, claim)
}
func (c *Coordinator) FailRecovery(id string, fence uint64, claim, reason string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.store.Update(func(runs map[string]fsm.Run) error {
		r, ok := runs[id]
		if !ok || r.Fence != fence || r.RecoveryClaim != claim || claim == "" {
			return fmt.Errorf("stale recovery failure")
		}
		// The claim authorizes this transition only; the terminal run owns no
		// recovery state afterwards.
		c.failDeadline(&r, c.cfg.Now())
		r.Blocker = reason
		runs[id] = r
		return nil
	})
}
