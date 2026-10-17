// Package orchestration connects durable external actions to the existing
// coordd loop. Adapters are explicitly injected trusted process boundaries;
// there is deliberately no live launch adapter or unauthenticated ingress.
package orchestration

import (
	"context"
	"coord/internal/externalaction"
	"coord/internal/projection"
	"coord/store"
	"errors"
	"fmt"
	"sort"
	"time"
)

type Adapter interface {
	SourceID() string
	Execute(context.Context, externalaction.Action) (externalaction.Result, error)
	// Observe resolves THIS operation instance/key, not a fresh liveness query.
	Observe(context.Context, externalaction.Action) (externalaction.Result, error)
	Acknowledge(context.Context, externalaction.Receipt) error
}
type Runtime struct {
	Store        *store.CanonicalStore
	Adapter      Adapter
	DispatcherID string
	Now          func() time.Time
	Lease        time.Duration
	CallTimeout  time.Duration
	Projector    *projection.Writer
}

func New(s *store.CanonicalStore, a Adapter, id string, now func() time.Time) *Runtime {
	return &Runtime{Store: s, Adapter: a, DispatcherID: id, Now: now, Lease: 30 * time.Second, CallTimeout: 10 * time.Second}
}
func (r *Runtime) validate() error {
	if r.Store == nil || r.Now == nil || r.DispatcherID == "" || r.Lease <= 0 || r.CallTimeout <= 0 || r.CallTimeout >= r.Lease {
		return fmt.Errorf("invalid orchestration dependencies/policy")
	}
	if r.Adapter != nil && r.Adapter.SourceID() == "" {
		return fmt.Errorf("adapter source identity missing")
	}
	return nil
}
func (r *Runtime) Recover(ctx context.Context) error {
	if err := r.validate(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return r.Store.RecoverActions(r.Now())
}

// Tick handles at most one action and one acknowledgement. Storage is never
// locked across external IO. BLOCKED actions are not rescheduled without a new
// validated command, so idle ticks perform no canonical commits.
func (r *Runtime) Tick(ctx context.Context) error {
	if err := r.validate(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := r.Store.ExpireActionClaims(r.Now()); err != nil {
		return err
	}
	snap, err := r.Store.CanonicalSnapshot()
	if err != nil {
		return err
	}
	ids := make([]string, 0, len(snap.Orchestration.Actions))
	for id := range snap.Orchestration.Actions {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		a := snap.Orchestration.Actions[id]
		if a.Family == externalaction.GitHub {
			if _, bound, originErr := a.CanonicalOrigin(); originErr != nil || !bound {
				continue // legacy records remain historical until separate provenance resolution
			}
		}
		if a.Status == externalaction.Observed {
			if a.Family == externalaction.GitHub && a.Result != nil && a.Result.GitHub != nil && a.Result.GitHub.ResultStatus == "NOT_EVALUATED" {
				continue // provenance wait is quiescent until a new canonical command
			}
			obsIDs := []string{}
			for oid, o := range snap.Orchestration.Observations {
				if o.ActionID == id && !o.Stale && o.ClaimEpoch == a.Claim.Epoch {
					obsIDs = append(obsIDs, oid)
				}
			}
			sort.Strings(obsIDs)
			if len(obsIDs) == 0 {
				return fmt.Errorf("observed action without eligible observation")
			}
			if err := r.Store.AcceptObservation(obsIDs[0], r.Now()); err != nil {
				return err
			}
			break
		}
		if a.Terminal() || a.NextAttemptAt.After(r.Now()) || a.Claim.DispatcherID != "" && a.Claim.Deadline.After(r.Now()) {
			continue
		}
		if a.Status == externalaction.Dispatching || a.Status == externalaction.Dispatched {
			continue
		} // recovered at startup, never blind redispatched
		a, err = r.Store.ClaimAction(id, r.DispatcherID, r.Now(), r.Lease)
		if errors.Is(err, store.ErrActionClaim) {
			continue
		}
		if err != nil {
			return err
		}
		observing := a.Status == externalaction.Ambiguous
		if observing {
			err = r.Store.CountObservationAttempt(id, a.Claim.Epoch, r.Now())
		} else {
			a, err = r.Store.StartAction(id, r.DispatcherID, a.Claim.Epoch, r.Now())
		}
		if err != nil {
			return err
		}
		callCtx, cancel := context.WithTimeout(ctx, r.CallTimeout)
		var result externalaction.Result
		if r.Adapter == nil {
			err = fmt.Errorf("BLOCKED_EXTERNAL: no qualified external action adapter")
		} else if observing {
			result, err = r.Adapter.Observe(callCtx, a)
		} else {
			result, err = r.Adapter.Execute(callCtx, a)
		}
		cancel()
		if err != nil {
			return r.Store.ActionUncertain(id, a.Claim.Epoch, r.Now(), err.Error())
		}
		o := externalaction.Observation{ID: externalaction.Digest(struct {
			Key   string
			Epoch uint64
		}{a.Key, a.Claim.Epoch}), ActionID: id, Source: r.Adapter.SourceID(), ClaimEpoch: a.Claim.Epoch, RequestDigest: a.RequestDigest, Result: result, Digest: externalaction.Digest(result), At: r.Now()}
		if err := r.Store.RecordObservation(o); err != nil {
			return err
		}
		if err := r.Store.AcceptObservation(o.ID, r.Now()); err != nil {
			return err
		}
		break
	}
	snap, err = r.Store.CanonicalSnapshot()
	if err != nil {
		return err
	}
	receiptIDs := []string{}
	for id, receipt := range snap.Orchestration.Receipts {
		if receipt.AckPending && receipt.AckBlocker == "" && !receipt.NextAckAt.After(r.Now()) {
			receiptIDs = append(receiptIDs, id)
		}
	}
	sort.Strings(receiptIDs)
	if len(receiptIDs) > 0 && r.Adapter != nil {
		id := receiptIDs[0]
		receipt, claimErr := r.Store.BeginAcknowledgement(id, r.Now())
		if claimErr != nil {
			return claimErr
		}
		c, cancel := context.WithTimeout(ctx, r.CallTimeout)
		err = r.Adapter.Acknowledge(c, receipt)
		cancel()
		// Acknowledgement delivery failure does not roll back accepted work. Its
		// durable intent remains pending for a later tick; no action is re-executed.
		if err == nil {
			if err = r.Store.AcknowledgeActionReceipt(id); err != nil {
				return err
			}
		}
	}
	if r.Projector != nil {
		snap, err = r.Store.CanonicalSnapshot()
		if err != nil {
			return err
		}
		return r.Projector.Publish(snap)
	}
	return nil
}
