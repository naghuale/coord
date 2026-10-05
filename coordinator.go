// Package coord implements a durable, transport-independent coordinator.
package coord

import (
	"context"
	"coord/envelope"
	"coord/fsm"
	"coord/retry"
	"coord/roles"
	"coord/store"
	"coord/transport"
	"errors"
	"fmt"
	"sync"
	"time"
)

var ErrTransportClosed = errors.New("transport closed")
var ErrStale = errors.New("noncanonical identity")

type Config struct {
	AckTimeout, ProbeTimeout, SendTimeout, TickInterval time.Duration
	Retry                                               retry.Policy
	Now                                                 func() time.Time
}

func DefaultConfig() Config {
	return Config{30 * time.Second, 5 * time.Second, 5 * time.Second, time.Second, retry.Default(), time.Now}
}

type Coordinator struct {
	mu           sync.Mutex
	tp           transport.Transport
	store        *store.Store
	cfg          Config
	Metrics      *Metrics
	RoleRegistry *roles.Registry
}

func New(tp transport.Transport, s *store.Store, cfg Config) (*Coordinator, error) {
	if tp == nil || s == nil || tp.WhoAmI() == "" || cfg.Now == nil || cfg.AckTimeout <= 0 || cfg.ProbeTimeout <= 0 || cfg.SendTimeout <= 0 || cfg.TickInterval <= 0 {
		return nil, fmt.Errorf("invalid coordinator configuration")
	}
	if err := cfg.Retry.Validate(); err != nil {
		return nil, err
	}
	return &Coordinator{tp: tp, store: s, cfg: cfg, Metrics: NewMetrics()}, nil
}
func (c *Coordinator) base(k envelope.Kind, r fsm.Run) envelope.Envelope {
	return envelope.Envelope{SchemaVersion: 1, MessageID: envelope.NewID(), Timestamp: c.cfg.Now().UTC(), From: c.tp.WhoAmI(), To: r.OwnerSession, Kind: k, Task: r.Task, Run: r.RunID, Attempt: r.Attempt, TargetSHA: r.TargetSHA, Fence: r.Fence}
}
func (c *Coordinator) task(r fsm.Run) envelope.Envelope {
	e := c.base(envelope.Task, r)
	e.TaskPayload = &envelope.TaskPayload{Worktree: r.Worktree, PayloadRef: r.PayloadRef, Deadline: r.Deadline, ProgressPct: r.ProgressPct, AssignedRole: r.RoleSelection}
	return e
}

// Assign durably records ownership and the outbox before attempting delivery.
func (c *Coordinator) Assign(ctx context.Context, r fsm.Run) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.cfg.Now()
	r.Status = fsm.Pending
	r.AssignedAt = now
	r.LastUpdate = now
	r.NextUpdateBy = now.Add(c.cfg.AckTimeout)
	r.Seen = map[string]bool{}
	r.Retries = 0
	r.RecoveryNeeded = false
	r.RecoveryClaim = ""
	r.DeliveredAt = time.Time{}
	r.Result = nil
	if err := c.selectRole(&r); err != nil {
		return err
	}
	r.ResumeStatus = ""
	r.ProbeID = ""
	r.ProbeNonce = ""
	r.RetryAt = time.Time{}
	r.SendRetryAt = time.Time{}
	r.SendRetries = 0
	e := c.task(r)
	if err := e.Validate(); err != nil {
		return err
	}
	r.TaskMessages = []string{e.MessageID}
	r.Outbox = []envelope.Envelope{e}
	if err := c.store.Update(func(runs map[string]fsm.Run) error {
		if _, ok := runs[r.RunID]; ok {
			return fmt.Errorf("run already exists")
		}
		runs[r.RunID] = r
		return nil
	}); err != nil {
		return err
	}
	return c.flush(ctx, r.RunID)
}
func (c *Coordinator) send(ctx context.Context, e envelope.Envelope) error {
	child, cancel := context.WithTimeout(ctx, c.cfg.SendTimeout)
	defer cancel()
	started := time.Now()
	err := c.tp.Send(child, e.To, e)
	if err == nil {
		c.Metrics.Inc(fmt.Sprintf("outbound_total{kind=%q}", e.Kind))
		c.Metrics.DeliveryLatency(time.Since(started).Seconds())
	}
	return err
}
func (c *Coordinator) flush(ctx context.Context, id string) error {
	r, ok := c.store.Get(id)
	if !ok {
		return nil
	}
	if c.cfg.Now().Before(r.SendRetryAt) {
		return nil
	}
	for _, e := range r.Outbox {
		if err := c.send(ctx, e); err != nil {
			c.Metrics.Inc("send_errors_total")
			return c.store.Update(func(runs map[string]fsm.Run) error {
				x := runs[id]
				x.SendRetryAt = c.cfg.Now().Add(c.cfg.Retry.DelayFor(x.SendRetries))
				x.SendRetries++
				runs[id] = x
				return nil
			})
		}
		if err := c.store.Update(func(runs map[string]fsm.Run) error {
			x := runs[id]
			out := x.Outbox[:0]
			for _, p := range x.Outbox {
				if p.MessageID != e.MessageID {
					out = append(out, p)
				}
			}
			x.Outbox = out
			x.SendRetryAt = time.Time{}
			x.SendRetries = 0
			if e.Kind == envelope.Task && x.DeliveredAt.IsZero() {
				x.DeliveredAt = c.cfg.Now()
			}
			runs[id] = x
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}
func contains(ids []string, id string) bool {
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
}

// Handle checks identity, correlation, deduplication and FSM before any update.
func (c *Coordinator) Handle(ctx context.Context, e envelope.Envelope) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := e.Validate(); err != nil {
		c.Metrics.Inc("malformed_messages_total")
		return err
	}
	if e.To != c.tp.WhoAmI() {
		return fmt.Errorf("wrong recipient")
	}
	c.Metrics.Inc(fmt.Sprintf("inbound_total{kind=%q}", e.Kind))
	if e.Kind == envelope.RoleUpdate {
		if c.RoleRegistry == nil {
			return fmt.Errorf("role registry unavailable")
		}
		if err := c.RoleRegistry.Apply(e.From, e.MessageID, *e.RoleUpdatePayload, c.cfg.Now()); err != nil {
			return err
		}
		c.Metrics.Inc("role_updates_total")
		a, _ := c.RoleRegistry.Agent(e.From)
		ack := envelope.Envelope{SchemaVersion: 1, MessageID: envelope.NewID(), Timestamp: c.cfg.Now().UTC(), From: c.tp.WhoAmI(), To: e.From, Kind: envelope.RoleAck, InReplyTo: e.MessageID, RoleAckPayload: &envelope.RoleAckPayload{State: "ACCEPTED", Revision: a.Revision}}
		return c.send(ctx, ack)
	}
	if e.Kind == envelope.Ping {
		p := envelope.Envelope{SchemaVersion: 1, MessageID: envelope.NewID(), Timestamp: c.cfg.Now().UTC(), From: c.tp.WhoAmI(), To: e.From, Kind: envelope.Pong, InReplyTo: e.MessageID, PongPayload: &envelope.PongPayload{Nonce: e.PingPayload.Nonce, State: "BUSY"}}
		return c.send(ctx, p)
	}
	r, ok := c.store.Get(e.Run)
	if !ok {
		return fmt.Errorf("unknown run")
	}
	if r.OwnerSession != e.From || r.Attempt != e.Attempt || r.TargetSHA != e.TargetSHA || r.Task != e.Task || r.Fence != e.Fence {
		c.Metrics.Inc("split_brain_events_total")
		a := c.base(envelope.Abort, r)
		a.To = e.From
		a.InReplyTo = e.MessageID
		a.AbortPayload = &envelope.AbortPayload{Reason: "not canonical owner/attempt/fence"}
		_ = c.send(ctx, a)
		return ErrStale
	}
	if r.Seen[e.MessageID] {
		c.Metrics.Inc("duplicate_messages_total")
		return nil
	}
	if r.RecoveryClaim != "" {
		return fmt.Errorf("run is fenced for recovery")
	}
	if e.Kind == envelope.Pong {
		if r.ProbeID == "" || e.InReplyTo != r.ProbeID || e.PongPayload.Nonce != r.ProbeNonce {
			return fmt.Errorf("uncorrelated PONG")
		}
		// Liveness cannot substitute for an application heartbeat.
		return c.store.Update(func(runs map[string]fsm.Run) error {
			x := runs[r.RunID]
			x.Seen[e.MessageID] = true
			x.ProbeID = ""
			x.ProbeNonce = ""
			runs[x.RunID] = x
			return nil
		})
	}
	if !contains(r.TaskMessages, e.InReplyTo) {
		return fmt.Errorf("uncorrelated task response")
	}
	now := c.cfg.Now()
	if (e.Kind == envelope.Ack && e.AckPayload.State == "ACCEPTED" && !e.AckPayload.NextUpdateBy.After(now)) || (e.Kind == envelope.Heartbeat && !e.HeartbeatPayload.NextUpdateBy.After(now)) {
		return fmt.Errorf("expired next_update_by")
	}
	if err := r.Apply(e, now); err != nil {
		c.Metrics.Inc("fsm_errors_total")
		return err
	}
	r.Seen[e.MessageID] = true
	r.Outbox = nil
	if err := c.store.Update(func(runs map[string]fsm.Run) error { runs[r.RunID] = r; return nil }); err != nil {
		return err
	}
	if e.Kind == envelope.Ack {
		if !r.DeliveredAt.IsZero() {
			c.Metrics.AckLatency(now.Sub(r.DeliveredAt).Seconds())
		}
	}
	if e.Kind == envelope.Heartbeat {
		c.Metrics.Inc("heartbeat_received_total")
	}
	return nil
}

// Tick performs bounded retries. Transport retries preserve the attempt and owner.
func (c *Coordinator) Tick(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.cfg.Now()
	for id, r := range c.store.All() {
		if r.Terminal() {
			continue
		}
		if !now.Before(r.Deadline) {
			r.Status = fsm.Failed
			r.Blocker = "task deadline exceeded"
			r.Outbox = nil
			if err := c.put(r); err != nil {
				return err
			}
			continue
		}
		if r.RecoveryNeeded {
			continue
		}
		if r.Status != fsm.Stalled && !now.Before(r.NextUpdateBy) {
			if err := r.Timeout(now); err != nil {
				return err
			}
			r.RetryAt = now
			c.Metrics.Inc("stall_total")
			if r.ResumeStatus == fsm.Pending {
				c.Metrics.Inc("ack_timeouts_total")
			} else {
				c.Metrics.Inc("heartbeat_miss_total")
			}
			if err := c.put(r); err != nil {
				return err
			}
		}
		if r.Status == fsm.Stalled && !now.Before(r.RetryAt) {
			if r.Retries >= c.cfg.Retry.MaxRetries {
				r.RecoveryNeeded = true
				r.Outbox = nil
				if err := c.put(r); err != nil {
					return err
				}
				continue
			}
			var e envelope.Envelope
			if r.ResumeStatus == fsm.Pending {
				e = c.task(r)
				r.TaskMessages = append(r.TaskMessages, e.MessageID)
			} else {
				e = c.base(envelope.Ping, r)
				e.PingPayload = &envelope.PingPayload{Nonce: envelope.NewID()}
				r.ProbeID = e.MessageID
				r.ProbeNonce = e.PingPayload.Nonce
			}
			r.Outbox = []envelope.Envelope{e}
			r.SendRetryAt = time.Time{}
			r.SendRetries = 0
			r.RetryAt = now.Add(c.cfg.Retry.DelayFor(r.Retries))
			if r.RetryAt.Before(now.Add(c.cfg.ProbeTimeout)) {
				r.RetryAt = now.Add(c.cfg.ProbeTimeout)
			}
			r.Retries++
			if err := c.put(r); err != nil {
				return err
			}
		}
		if err := c.flush(ctx, id); err != nil {
			return err
		}
	}
	return nil
}
func (c *Coordinator) put(r fsm.Run) error {
	return c.store.Update(func(runs map[string]fsm.Run) error { runs[r.RunID] = r; return nil })
}
func (c *Coordinator) recover(ctx context.Context, id, owner string, expectedFence uint64, claim string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.store.Get(id)
	if !ok || !r.RecoveryNeeded || r.Status != fsm.Stalled || r.Fence != expectedFence || claim == "" || r.RecoveryClaim != claim || owner == "" || owner == r.OwnerSession {
		return fmt.Errorf("invalid recovery")
	}
	r.OwnerSession = owner
	if err := c.selectRole(&r); err != nil {
		return err
	}
	r.Fence++
	r.RecoveryClaim = ""
	r.Status = fsm.Pending
	r.NextUpdateBy = c.cfg.Now().Add(c.cfg.AckTimeout)
	r.AssignedAt = c.cfg.Now()
	r.DeliveredAt = time.Time{}
	r.SendRetryAt = time.Time{}
	r.SendRetries = 0
	r.Retries = 0
	r.RecoveryNeeded = false
	r.Seen = map[string]bool{}
	r.ProbeID = ""
	r.ProbeNonce = ""
	e := c.task(r)
	if err := e.Validate(); err != nil {
		return err
	}
	r.TaskMessages = []string{e.MessageID}
	r.Outbox = []envelope.Envelope{e}
	if err := c.put(r); err != nil {
		return err
	}
	c.Metrics.Inc("watchdog_restarts_total")
	return c.flush(ctx, id)
}
func (c *Coordinator) Run(ctx context.Context) error {
	inbox := c.tp.Receive(ctx)
	tick := time.NewTicker(c.cfg.TickInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case e, ok := <-inbox:
			if !ok {
				return ErrTransportClosed
			}
			if err := c.Handle(ctx, e); err != nil {
				if errors.Is(err, store.ErrPersistence) || errors.Is(err, roles.ErrPersistence) {
					return err
				}
				c.Metrics.Inc("rejected_messages_total")
			}
		case <-tick.C:
			if err := c.Tick(ctx); err != nil {
				return err
			}
		}
	}
}
