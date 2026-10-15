package store

import (
	"coord/agentsession"
	"coord/internal/externalaction"
	"coord/task"
	"coord/taskid"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"
)

var ErrActionClaim = errors.New("stale or invalid external action claim")
var ErrActionAuthority = errors.New("external action authority invalid")

// workflowAuthority checks existing canonical bindings, not free-text identity.
// These in-process methods are called only by the daemon's configured adapter;
// they are not exposed as unauthenticated HTTP or transport endpoints.
func workflowAuthority(state *CanonicalState, a externalaction.Action) error {
	b := a.Binding
	id := taskid.ID(b.TaskID)
	t, ok := state.Tasks[id]
	r, runOK := state.Runs[b.RunID]
	session, sessOK := state.AgentSessions[b.SessionID]
	if !ok || !runOK || !sessOK || r.TaskID != id || r.TargetSHA != b.SubjectSHA || r.OwnerSession != b.SessionID || r.AgentSessionGeneration != b.Generation || session.Generation != b.Generation || r.Fence != b.Fence || t.Fence != b.Fence {
		return ErrActionAuthority
	}
	if !session.Active() && a.Kind != externalaction.Stop && a.Kind != externalaction.Replace {
		return ErrActionAuthority
	}
	if a.Kind == externalaction.Replace {
		p := state.Orchestration.Actions[a.ParentID]
		if p.Kind != externalaction.Stop || p.Status != externalaction.Completed || p.Result == nil || !p.Result.Stopped || session.Active() {
			return ErrActionAuthority
		}
	}
	return nil
}
func (s *CanonicalStore) recordAction(state *CanonicalState, a externalaction.Action, at time.Time) {
	a.UpdatedAt = at
	state.Orchestration.Actions[a.ID] = a
	state.Orchestration.Events = append(state.Orchestration.Events, externalaction.Event{ActionID: a.ID, Revision: s.state.Revision + 1, Digest: externalaction.Digest(a), At: at})
	id := taskid.ID(a.Binding.TaskID)
	t := state.Tasks[id]
	// Multiple facts in one transaction share one Task revision.
	t.Revision = s.state.Tasks[id].Revision + 1
	t.LastEventSeq++
	t.UpdatedAt = at
	state.Tasks[id] = t
	state.Events = append(state.Events, task.DomainEvent{ID: task.EventID(id, t.LastEventSeq), TaskID: id, RunID: a.Binding.RunID, EventSeq: t.LastEventSeq, TaskRevision: t.Revision, TaskFence: t.Fence, Type: "EXTERNAL_ACTION_" + task.EventType(a.Status), ActorID: "coordd", Summary: a.ID, CreatedAt: at})
}
func (s *CanonicalStore) EnqueueAction(a externalaction.Action) error {
	return s.UpdateCanonical(func(state *CanonicalState) error {
		if a.Status != externalaction.Pending || a.ParentID != "" || a.Kind != externalaction.Deliver {
			return fmt.Errorf("only initial delivery can be enqueued")
		}
		if prior, ok := state.Orchestration.Actions[a.ID]; ok {
			if prior.Key == a.Key && prior.RequestDigest == a.RequestDigest && prior.Binding == a.Binding && externalaction.Key(a) == a.Key {
				return nil
			}
			return fmt.Errorf("same action ID different request")
		}
		if err := workflowAuthority(state, a); err != nil {
			return err
		}
		if err := a.Validate(); err != nil {
			return err
		}
		s.recordAction(state, a, a.CreatedAt)
		return nil
	})
}
func (s *CanonicalStore) ClaimAction(id, dispatcher string, at time.Time, lease time.Duration) (externalaction.Action, error) {
	var claimed externalaction.Action
	err := s.UpdateCanonical(func(state *CanonicalState) error {
		a, ok := state.Orchestration.Actions[id]
		if !ok || dispatcher == "" || lease <= 0 {
			return ErrActionClaim
		}
		if a.Terminal() || a.NextAttemptAt.After(at) || (a.Claim.DispatcherID != "" && a.Claim.Deadline.After(at)) {
			return ErrActionClaim
		}
		if a.Status != externalaction.Pending && a.Status != externalaction.Observed && a.Status != externalaction.Ambiguous && (a.Status != externalaction.Claimed || a.Claim.Deadline.After(at)) {
			return ErrActionClaim
		}
		if err := workflowAuthority(state, a); err != nil {
			return err
		}
		a.Claim.Epoch++
		a.Claim.DispatcherID = dispatcher
		a.Claim.Deadline = at.Add(lease)
		// AMBIGUOUS must remain visibly ambiguous: a new claim permits observation only.
		if a.Status != externalaction.Ambiguous && a.Status != externalaction.Observed {
			a.Status = externalaction.Claimed
		}
		s.recordAction(state, a, at)
		claimed = a
		return nil
	})
	return claimed, err
}
func (s *CanonicalStore) StartAction(id, dispatcher string, epoch uint64, at time.Time) (externalaction.Action, error) {
	var started externalaction.Action
	err := s.UpdateCanonical(func(state *CanonicalState) error {
		a, ok := state.Orchestration.Actions[id]
		if !ok || a.Status != externalaction.Claimed || a.Claim.DispatcherID != dispatcher || a.Claim.Epoch != epoch || !a.Claim.Deadline.After(at) {
			return ErrActionClaim
		}
		if err := workflowAuthority(state, a); err != nil {
			return err
		}
		a.Status = externalaction.Dispatching
		a.Attempts++
		s.recordAction(state, a, at)
		started = a
		return nil
	})
	return started, err
}

// RecoverActions must run before dispatch after daemon restart. Even a marker
// committed immediately before a crash is ambiguous; persisted RUNNING is never
// consulted as liveness evidence.
func (s *CanonicalStore) RecoverActions(at time.Time) error     { return s.recoverActions(at, true) }
func (s *CanonicalStore) ExpireActionClaims(at time.Time) error { return s.recoverActions(at, false) }
func (s *CanonicalStore) recoverActions(at time.Time, startup bool) error {
	snap, err := s.CanonicalSnapshot()
	if err != nil {
		return err
	}
	ids := []string{}
	for id, a := range snap.Orchestration.Actions {
		if (a.Status == externalaction.Dispatching || a.Status == externalaction.Dispatched || a.Status == externalaction.Claimed) && (startup || !a.Claim.Deadline.After(at)) {
			ids = append(ids, id)
		}
	}
	// A completed liveness observation from a previous process is not fresh proof.
	// New operation identity avoids returning a cached old RUNNING observation.
	refresh := []externalaction.Action{}
	if startup {
		for _, a := range snap.Orchestration.Actions {
			if a.Kind != externalaction.Observe || a.Status != externalaction.Completed || a.Result == nil || a.Result.Outcome != "RUNNING" {
				continue
			}
			covered := false
			for _, other := range snap.Orchestration.Actions {
				if other.Binding == a.Binding && other.ID != a.ID && (!other.Terminal() || other.Kind == externalaction.Fence || other.Kind == externalaction.Stop || other.Kind == externalaction.Replace) {
					covered = true
				}
			}
			if !covered {
				refresh = append(refresh, externalaction.New(a.ID+"/restart/"+fmt.Sprint(snap.Revision+1), externalaction.Observe, a.Binding, a.ID, a.Request, at))
			}
		}
	}
	if len(ids) == 0 && len(refresh) == 0 {
		return nil
	}
	return s.UpdateCanonical(func(state *CanonicalState) error {
		for _, id := range ids {
			a := state.Orchestration.Actions[id]
			a.Status = externalaction.Ambiguous
			a.Claim.Epoch++
			a.Claim.DispatcherID = ""
			a.Claim.Deadline = at
			a.NextAttemptAt = at
			s.recordAction(state, a, at)
		}
		for _, a := range refresh {
			if err := workflowAuthority(state, a); err != nil {
				return err
			}
			s.recordAction(state, a, at)
		}
		return nil
	})
}

func (s *CanonicalStore) ActionUncertain(id string, epoch uint64, at time.Time, cause string) error {
	return s.UpdateCanonical(func(state *CanonicalState) error {
		a := state.Orchestration.Actions[id]
		if a.Claim.Epoch != epoch || a.Status != externalaction.Dispatching && a.Status != externalaction.Ambiguous {
			return ErrActionClaim
		}
		a.Status = externalaction.Ambiguous
		a.Blocker = cause
		// Observation retries are bounded too; they do not redispatch the effect.
		if a.Attempts >= a.MaxAttempts {
			a.Status = externalaction.Blocked
			a.Blocker = "BLOCKED_EXTERNAL: " + cause
		} else {
			a.NextAttemptAt = at.Add(time.Second << a.Attempts)
		}
		s.recordAction(state, a, at)
		return nil
	})
}
func (s *CanonicalStore) CountObservationAttempt(id string, epoch uint64, at time.Time) error {
	return s.UpdateCanonical(func(state *CanonicalState) error {
		a := state.Orchestration.Actions[id]
		if a.Status != externalaction.Ambiguous || a.Claim.Epoch != epoch || !a.Claim.Deadline.After(at) {
			return ErrActionClaim
		}
		a.Attempts++
		s.recordAction(state, a, at)
		return nil
	})
}
func (s *CanonicalStore) RecordObservation(o externalaction.Observation) error {
	stale := false
	err := s.UpdateCanonical(func(state *CanonicalState) error {
		a, ok := state.Orchestration.Actions[o.ActionID]
		if !ok || o.ID == "" || o.Source == "" || o.Result.Evidence == "" || o.RequestDigest != a.RequestDigest || o.Digest != externalaction.Digest(o.Result) || o.At.IsZero() {
			return fmt.Errorf("unbound observation")
		}
		if prior, ok := state.Orchestration.Observations[o.ID]; ok {
			if reflect.DeepEqual(prior, o) {
				return nil
			}
			return fmt.Errorf("same observation ID different body")
		}
		if a.Claim.Epoch != o.ClaimEpoch || a.Terminal() || !a.Claim.Deadline.After(o.At) {
			o.Stale = true
			stale = true
			state.Orchestration.Observations[o.ID] = o
			return nil
		}
		if a.Status != externalaction.Dispatching && a.Status != externalaction.Ambiguous {
			return ErrActionClaim
		}
		if err := workflowAuthority(state, a); err != nil {
			return err
		}
		o.Stale = false
		state.Orchestration.Observations[o.ID] = o
		a.Status = externalaction.Observed
		a.Result = &o.Result
		s.recordAction(state, a, o.At)
		return nil
	})
	if err == nil && stale {
		return ErrActionClaim
	}
	return err
}
func (s *CanonicalStore) AcceptObservation(id string, at time.Time) error {
	return s.UpdateCanonical(func(state *CanonicalState) error {
		o, ok := state.Orchestration.Observations[id]
		if !ok || o.Stale {
			return fmt.Errorf("observation not eligible")
		}
		a := state.Orchestration.Actions[o.ActionID]
		if prior, ok := state.Orchestration.Receipts[id]; ok {
			if prior.Digest == o.Digest {
				return nil
			}
			return fmt.Errorf("receipt body conflict")
		}
		if a.Status != externalaction.Observed || a.Claim.Epoch != o.ClaimEpoch {
			return ErrActionClaim
		}
		if err := workflowAuthority(state, a); err != nil {
			return err
		}
		var next externalaction.Kind
		switch a.Kind {
		case externalaction.Deliver:
			if o.Result.Outcome != "DELIVERED" {
				return fmt.Errorf("delivery result invalid")
			}
			next = externalaction.Observe
		case externalaction.Observe:
			switch o.Result.Outcome {
			case "UNAVAILABLE":
				next = externalaction.Fence
			case "RUNNING":
			default:
				return fmt.Errorf("liveness not confirmed")
			}
		case externalaction.Fence:
			if o.Result.Outcome != "FENCED" {
				return fmt.Errorf("fence not confirmed")
			}
			session := state.AgentSessions[a.Binding.SessionID]
			seq, err := nextAgentSessionSequence(state.AgentSessionEvents, session.Address)
			if err != nil {
				return err
			}
			session.RevokedAt = at
			session.UpdatedAt = at
			session.RevocationReason = "orchestration fence " + a.ID
			state.AgentSessions[session.Address] = session
			state.AgentSessionEvents = append(state.AgentSessionEvents, agentsession.Event{ID: agentsession.EventID(session.Address, seq), Address: session.Address, AgentID: session.AgentID, Sequence: seq, Type: agentsession.Revoked, Generation: session.Generation, Reason: session.RevocationReason, CreatedAt: at})
			next = externalaction.Stop
		case externalaction.Stop:
			if !o.Result.Stopped || o.Result.StopMethod != "SUPERVISOR_CONFIRMED_EXIT" || o.Result.Outcome != "STOPPED" {
				return fmt.Errorf("required stop proof not satisfied")
			}
			next = externalaction.Replace
		case externalaction.Replace:
			if o.Result.Outcome != "REPLACEMENT_CREATED" {
				return fmt.Errorf("replacement not confirmed")
			}
		}
		a.Status = externalaction.Completed
		s.recordAction(state, a, at)
		if next != "" {
			req := a.Request
			if next == externalaction.Replace {
				if a.Binding.Generation == ^uint64(0) {
					return fmt.Errorf("generation exhausted")
				}
				req, _ = json.Marshal(struct {
					PreviousSession string `json:"previousSession"`
					Generation      uint64 `json:"generation"`
				}{a.Binding.SessionID, a.Binding.Generation + 1})
			}
			n := externalaction.New(a.ID+"/"+string(next), next, a.Binding, a.ID, req, at)
			s.recordAction(state, n, at)
		}
		rev := s.state.Revision + 1
		state.Orchestration.Receipts[id] = externalaction.Receipt{ObservationID: id, ActionID: a.ID, Digest: o.Digest, Revision: rev, AckPending: true}
		tid := taskid.ID(a.Binding.TaskID)
		before := s.state.Tasks[tid]
		after := state.Tasks[tid]
		eventIDs := []string{}
		for _, e := range state.Events[len(s.state.Events):] {
			if e.TaskID == tid {
				eventIDs = append(eventIDs, e.ID)
			}
		}
		state.Inbox[InboxKey(o.Source, id)] = InboxReceipt{ActorID: o.Source, MessageID: id, TaskID: tid, RunID: a.Binding.RunID, EventIDs: eventIDs, ExpectedRevision: before.Revision, CommittedRevision: after.Revision, RecordedAt: at}
		return nil
	})
}
func (s *CanonicalStore) AcknowledgeActionReceipt(id string) error {
	return s.UpdateCanonical(func(state *CanonicalState) error {
		r, ok := state.Orchestration.Receipts[id]
		if !ok {
			return fmt.Errorf("receipt missing")
		}
		r.AckPending = false
		r.AckBlocker = ""
		state.Orchestration.Receipts[id] = r
		return nil
	})
}

// BeginAcknowledgement persists the retry budget before sending a receipt.
func (s *CanonicalStore) BeginAcknowledgement(id string, at time.Time) (externalaction.Receipt, error) {
	var receipt externalaction.Receipt
	err := s.UpdateCanonical(func(state *CanonicalState) error {
		r, ok := state.Orchestration.Receipts[id]
		if !ok || !r.AckPending || r.AckBlocker != "" || r.NextAckAt.After(at) || r.AckAttempts >= 3 {
			return ErrActionClaim
		}
		r.AckAttempts++
		r.NextAckAt = at.Add(time.Second << r.AckAttempts)
		if r.AckAttempts == 3 {
			r.AckBlocker = "BLOCKED_EXTERNAL: acknowledgement retry budget exhausted"
		}
		state.Orchestration.Receipts[id] = r
		receipt = r
		return nil
	})
	return receipt, err
}
