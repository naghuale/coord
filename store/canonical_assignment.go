package store

import (
	"coord/assignment"
	"coord/taskid"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrAssignmentUnknown = errors.New("UNKNOWN_ASSIGNMENT")
var ErrLedgerFrozen = errors.New("LEDGER_FROZEN")

// FreezeAssignmentLedger is a startup/operator control. It does not disable
// safety checks or erase data. Returning to normal mode requires a deliberate
// restart with changed operator configuration.
func (s *CanonicalStore) FreezeAssignmentLedger() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ledgerFrozen = true
}

func (s *CanonicalStore) validateAssignmentMode(next CanonicalState) error {
	if !s.ledgerFrozen || next.AssignmentLedger == nil {
		return nil
	}
	if s.state.AssignmentLedger == nil {
		return ErrLedgerFrozen
	}
	if next.AssignmentLedger.Version != s.state.AssignmentLedger.Version {
		return ErrLedgerFrozen
	}
	for id := range next.AssignmentLedger.Records {
		if _, exists := s.state.AssignmentLedger.Records[id]; !exists {
			return ErrLedgerFrozen
		}
	}
	return nil
}

// AssignmentStatus is a read-only observation, never a durable permit. The
// caller must also compare its exact issuer/subject/role/session before work.
type AssignmentStatus struct {
	AssignmentID          string           `json:"assignmentId"`
	State                 assignment.State `json:"state"`
	ValidUntil            time.Time        `json:"validUntil"`
	SubjectSHA            string           `json:"subjectSha"`
	Assignee              string           `json:"assignee"`
	Issuer                string           `json:"issuer"`
	CurrentCoordinator    string           `json:"currentCoordinator"`
	CoordinatorGeneration uint64           `json:"coordinatorGeneration"`
	HandoffValid          bool             `json:"handoffValid"`
	WorkMayStart          bool             `json:"workMayStart"`
	Reason                string           `json:"reason"`
}

func assignmentPrincipal(state CanonicalState, p assignment.Principal) error {
	s, ok := state.AgentSessions[p.SessionID]
	if !ok || !p.Valid() || !s.Active() || s.AgentID != p.AgentID || s.Generation != p.Generation {
		return fmt.Errorf("%w: unknown/revoked session or stale generation", assignment.ErrInvalid)
	}
	return nil
}

func validateAssignments(state CanonicalState) error {
	if err := assignment.Validate(state.AssignmentLedger); err != nil {
		return err
	}
	if state.AssignmentLedger == nil {
		return nil
	}
	bindings := map[string]bool{}
	for _, r := range state.AssignmentLedger.Records {
		// Historical revoked/older generations must remain readable, not revived.
		for _, p := range []assignment.Principal{r.Target(), {SessionID: r.Issuer, AgentID: r.IssuerAgentID, Generation: r.IssuerGeneration}, r.Coordinator()} {
			s, ok := state.AgentSessions[p.SessionID]
			if !ok || s.AgentID != p.AgentID || p.Generation > s.Generation {
				return fmt.Errorf("%w: missing canonical session binding", assignment.ErrInvalid)
			}
		}
		if r.RunBinding != nil {
			b := r.RunBinding
			if _, ok := state.Tasks[taskid.ID(b.TaskID)]; !ok {
				return fmt.Errorf("%w: missing optional Task", assignment.ErrInvalid)
			}
			if run, ok := state.Runs[b.RunID]; !ok || run.TaskID.String() != b.TaskID || bindings[b.RunID] {
				return fmt.Errorf("%w: missing/duplicate optional Run", assignment.ErrInvalid)
			}
			bindings[b.RunID] = true
		}
	}
	return nil
}

func assignmentReady(state CanonicalState, r assignment.Record, now time.Time) error {
	if err := r.Ready(now); err != nil {
		return err
	}
	if err := assignmentPrincipal(state, r.Coordinator()); err != nil {
		return err
	}
	if err := assignmentPrincipal(state, r.Target()); err != nil {
		return err
	}
	if r.RunBinding != nil {
		b := r.RunBinding
		run, ok := state.Runs[b.RunID]
		t, taskOK := state.Tasks[taskid.ID(b.TaskID)]
		if !ok || !taskOK || run.Terminal() || run.TaskID.String() != b.TaskID || run.Fence != b.Fence || t.Fence != b.Fence ||
			run.Attempt != b.Attempt || run.TargetSHA != r.SubjectSHA || run.OwnerSession != r.SessionID ||
			run.AgentSessionGeneration != r.AssigneeGeneration || run.RoleSelection == nil ||
			run.RoleSelection.AgentID != r.StableAgentID || string(run.RoleSelection.Profile.Role) != r.Role {
			return fmt.Errorf("%w: optional Task/Run/subject/fence binding changed", assignment.ErrInvalid)
		}
	}
	return nil
}

// InspectAssignment never creates a store, writes expiry, or treats a stored
// ACTIVE record with an elapsed deadline as executable.
func InspectAssignment(state CanonicalState, id string, now time.Time) (AssignmentStatus, error) {
	status := AssignmentStatus{AssignmentID: id, Reason: ErrAssignmentUnknown.Error()}
	if state.AssignmentLedger == nil {
		return status, ErrAssignmentUnknown
	}
	r, ok := state.AssignmentLedger.Records[id]
	if !ok {
		return status, ErrAssignmentUnknown
	}
	status.State, status.ValidUntil, status.SubjectSHA, status.Assignee = r.State, r.ValidUntil, r.SubjectSHA, r.Assignee
	status.Issuer, status.CurrentCoordinator, status.CoordinatorGeneration = r.Issuer, r.CurrentCoordinator, r.CoordinatorGeneration
	status.HandoffValid = r.Validate() == nil && !r.CoordinatorLost
	if err := assignmentReady(state, r, now); err != nil {
		status.Reason = err.Error()
		return status, nil
	}
	status.WorkMayStart, status.Reason = true, "ACTIVE"
	return status, nil
}

// OfferAssignment is called only by the authenticated coordinator/operator
// boundary, as with RegisterAgentSession. Caller-owned identity claims are not
// authorization. Process assignments need no Task or Run.
func (s *CanonicalStore) OfferAssignment(actor assignment.Principal, r assignment.Record, evidence string) error {
	return s.UpdateCanonical(func(state *CanonicalState) error {
		if s.ledgerFrozen {
			return ErrLedgerFrozen
		}
		if err := assignmentPrincipal(*state, actor); err != nil {
			return err
		}
		if err := assignmentPrincipal(*state, r.Target()); err != nil {
			return err
		}
		if state.AssignmentLedger == nil {
			state.AssignmentLedger = assignment.Empty()
		}
		if _, exists := state.AssignmentLedger.Records[r.AssignmentID]; exists {
			return fmt.Errorf("%w: assignmentId reuse", assignment.ErrInvalid)
		}
		now := s.now().UTC()
		if r.Coordinator() != actor || r.State != assignment.Offered || r.IssuedAt.After(now) || !now.Before(r.ValidUntil) {
			return assignment.ErrInvalid
		}
		if err := enforceDelegatedOfferOrAccept(*state, actor, DelegatedActionOffer, now); err != nil {
			return err
		}
		if err := bindAssignmentScopeFromGrant(*state, actor, &r, now); err != nil {
			return err
		}
		if err := enforceDelegatedMutation(*state, actor, DelegatedActionOffer, r, now); err != nil {
			return err
		}
		// Issuance precedes commit in real callers. Preserve the explicit issuedAt
		// and deadline; only the commit observation is owned by this store clock.
		r.UpdatedAt = now
		return state.AssignmentLedger.Append(assignment.Event{AssignmentID: r.AssignmentID, Revision: r.Revision, Kind: "OFFER", Actor: actor, EvidenceRef: evidence, At: now, Record: r})
	})
}

// ChangeAssignment handles only explicit coordinator commands or a verified
// exact ACK. Transport receipt/delivery is never an activation command.
func (s *CanonicalStore) ChangeAssignment(actor assignment.Principal, id string, revision uint64, kind, evidence string) error {
	return s.changeAssignment(actor, id, revision, kind, evidence, scopeClaim{})
}

// scopeClaim is a command-supplied grant binding. It is never written onto
// the assignment. When set, it must equal the persisted binding.
type scopeClaim struct {
	grantID         string
	grantGeneration uint64
	repository      string
	project         string
	workItem        string
	set             bool
}

func (s *CanonicalStore) changeAssignment(actor assignment.Principal, id string, revision uint64, kind, evidence string, claim scopeClaim) error {
	return s.UpdateCanonical(func(state *CanonicalState) error {
		r, err := assignmentRecord(*state, id, revision)
		if err != nil {
			return err
		}
		if err := assignmentPrincipal(*state, actor); err != nil {
			return err
		}
		if err := assignmentPrincipal(*state, r.Coordinator()); err != nil {
			return err
		}
		if claim.set {
			b := r.DelegationBinding
			if b == nil || claim.grantID != b.GrantID || claim.grantGeneration != b.GrantGeneration ||
				claim.repository != b.Repository || claim.project != b.Project ||
				(claim.workItem != "" && claim.workItem != b.AllowedWorkItem) {
				return fmt.Errorf("%w: command grant binding differs from persisted assignment", ErrCoordinatorDelegationInvalid)
			}
		}
		next := r
		now := s.now().UTC()
		switch kind {
		case "ACK":
			next.State = assignment.Acked
			next.AcknowledgedAt = now
		case "ACTIVATE":
			if err := assignmentPrincipal(*state, r.Target()); err != nil {
				return err
			}
			next.State = assignment.Active
		case "SUSPEND":
			next.State = assignment.Suspended
		case "RECONCILE":
			next.State = assignment.Acked
			if r.AcknowledgedAt.IsZero() {
				next.State = assignment.Offered
			}
		case "VOID":
			next.State = assignment.Void
		case "EXPIRE":
			next.State = assignment.Expired
		default:
			return fmt.Errorf("%w: unsupported authority command", assignment.ErrInvalid)
		}
		// ACK is the target's command; delegation gates apply to coordinator actions.
		if kind != "ACK" {
			action := kind
			if kind == "ACTIVATE" {
				action = DelegatedActionActivate
			}
			if err := enforceDelegatedMutation(*state, actor, action, r, now); err != nil {
				return err
			}
		}
		return appendAssignment(state, actor, r, next, kind, evidence, now)
	})
}

func assignmentRecord(state CanonicalState, id string, revision uint64) (assignment.Record, error) {
	if state.AssignmentLedger == nil {
		return assignment.Record{}, ErrAssignmentUnknown
	}
	r, ok := state.AssignmentLedger.Records[id]
	if !ok {
		return r, ErrAssignmentUnknown
	}
	if r.Revision != revision {
		return r, fmt.Errorf("%w: stale assignment revision", assignment.ErrInvalid)
	}
	return r, nil
}

func appendAssignment(state *CanonicalState, actor assignment.Principal, prior, next assignment.Record, kind, evidence string, now time.Time) error {
	next.Revision, next.UpdatedAt = prior.Revision+1, now
	return state.AssignmentLedger.Append(assignment.Event{AssignmentID: prior.AssignmentID, Revision: next.Revision, Kind: kind, Actor: actor, EvidenceRef: evidence, At: now, Record: next})
}

// ObserveAssignmentCoordinatorLoss records a trusted supervisor/operator
// observation by a current active observer, separate from the lost coordinator
// subject (which may already be revoked/rotated). The command boundary must
// authorize the observation; a principal argument is not a credential.
// It is not physical process detection or a new Session Registry.
func (s *CanonicalStore) ObserveAssignmentCoordinatorLoss(observer, coordinator assignment.Principal, evidence string) error {
	return s.UpdateCanonical(func(state *CanonicalState) error {
		if err := assignmentPrincipal(*state, observer); err != nil {
			return err
		}
		if state.AssignmentLedger == nil {
			return ErrAssignmentUnknown
		}
		if observer == coordinator {
			return fmt.Errorf("%w: coordinator cannot observe its own loss", assignment.ErrInvalid)
		}
		now := s.now().UTC()
		// Under an active grant, path (a) observer cannot later become successor for that loss;
		// restarted primary session cannot observe own prior loss (ADR §8/§9).
		if grant, ok := findGrantCoveringCoordinator(*state, coordinator, now); ok {
			obs := principalFromAssignment(observer)
			if grant.hasSuccessor(obs) {
				return fmt.Errorf("%w: path (a) observer cannot be a named successor for that loss", ErrCoordinatorDelegationInvalid)
			}
			if grant.PrimaryRestart != nil && observer.AgentID == grant.PrimaryRestart.StableAgentID {
				return fmt.Errorf("%w: restarted session cannot observe own prior loss", ErrCoordinatorDelegationInvalid)
			}
		}
		// Preflight the whole batch before appending any loss observation.
		for _, r := range state.AssignmentLedger.Records {
			if r.Coordinator() == coordinator && !r.State.Terminal() && !r.CoordinatorLost && observer == r.Target() {
				return fmt.Errorf("%w: target cannot observe coordinator loss", assignment.ErrInvalid)
			}
		}
		for _, r := range state.AssignmentLedger.Records {
			if r.Coordinator() != coordinator || r.State.Terminal() || r.CoordinatorLost {
				continue
			}
			next := r
			next.CoordinatorLost = true
			if err := appendAssignment(state, observer, r, next, "COORDINATOR_LOST", evidence, now); err != nil {
				return err
			}
		}
		return nil
	})
}

// HandoffAssignment records a verified coordinator/recovery decision. The
// command boundary must authorize predecessor/recovery evidence; a bare
// transport announcement is not such a command. Issuer and deadline stay fixed.
// A normal handoff requires the current active predecessor. Recovery may use a
// historical predecessor only after recorded loss, with the active successor
// as the event actor and nonempty verified recovery evidence.
func (s *CanonicalStore) HandoffAssignment(previous, nextCoordinator assignment.Principal, id string, revision uint64, evidence string) error {
	return s.UpdateCanonical(func(state *CanonicalState) error {
		r, err := assignmentRecord(*state, id, revision)
		if err != nil {
			return err
		}
		if previous != r.Coordinator() {
			return fmt.Errorf("%w: wrong handoff predecessor", assignment.ErrInvalid)
		}
		actor := previous
		if r.CoordinatorLost {
			observer, open := state.AssignmentLedger.OpenCoordinatorLossObserver(id)
			if open {
				if nextCoordinator == observer || nextCoordinator == r.Target() {
					return fmt.Errorf("%w: loss observer or target cannot recover to itself", assignment.ErrInvalid)
				}
			} else {
				// Main/#90 parity at const=false: no open AGENT_SESSION observer => reject.
				// OWNER_CREDENTIAL loss may proceed only when delegation is active and the
				// same-commit receipt is verified (G93b-1).
				if !ownerCredentialLossAllowsHandoffWithoutObserver(*state, id) {
					return fmt.Errorf("%w: loss observer or target cannot recover to itself", assignment.ErrInvalid)
				}
				if nextCoordinator == r.Target() {
					return fmt.Errorf("%w: loss observer or target cannot recover to itself", assignment.ErrInvalid)
				}
			}
			actor = nextCoordinator
		} else if err := assignmentPrincipal(*state, previous); err != nil {
			return err
		}
		if err := assignmentPrincipal(*state, nextCoordinator); err != nil {
			return err
		}
		now := s.now().UTC()
		if err := enforceDelegatedHandoff(state, previous, nextCoordinator, id, now); err != nil {
			return err
		}
		next := r
		next.CurrentCoordinator, next.CoordinatorAgentID, next.CoordinatorGeneration = nextCoordinator.SessionID, nextCoordinator.AgentID, nextCoordinator.Generation
		next.HandoffEvidence = append(append([]assignment.Handoff{}, r.HandoffEvidence...), assignment.Handoff{From: previous, To: nextCoordinator, EvidenceRef: evidence, At: now})
		next.CoordinatorLost = false
		return appendAssignment(state, actor, r, next, "HANDOFF", evidence, now)
	})
}

// CheckAssignmentWork rechecks the actual expected assignment identity. It is
// suitable for agent preflight; cached status or ACK cannot substitute it.
func (s *CanonicalStore) CheckAssignmentWork(actor assignment.Principal, id, subjectSHA, role, issuer, currentCoordinator string, generation uint64) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed || s.failed != nil {
		return fmt.Errorf("%w: store unavailable", assignment.ErrInvalid)
	}
	if s.state.AssignmentLedger == nil {
		return ErrAssignmentUnknown
	}
	r, ok := s.state.AssignmentLedger.Records[id]
	if !ok {
		return ErrAssignmentUnknown
	}
	if actor != r.Target() || subjectSHA != r.SubjectSHA || role != r.Role || issuer != r.Issuer || currentCoordinator != r.CurrentCoordinator || generation != r.CoordinatorGeneration {
		return assignment.ErrInvalid
	}
	return assignmentReady(s.state, r, s.now().UTC())
}

func (s *CanonicalStore) RecordAssignmentWorkStarted(actor assignment.Principal, id string, revision uint64, subjectSHA, role, issuer, currentCoordinator string, generation uint64, evidence string) error {
	return s.UpdateCanonical(func(state *CanonicalState) error {
		r, err := assignmentRecord(*state, id, revision)
		if err != nil {
			return err
		}
		if actor != r.Target() || subjectSHA != r.SubjectSHA || role != r.Role || issuer != r.Issuer || currentCoordinator != r.CurrentCoordinator || generation != r.CoordinatorGeneration {
			return assignment.ErrInvalid
		}
		now := s.now().UTC()
		if err := assignmentReady(*state, r, now); err != nil {
			return err
		}
		next := r
		next.WorkStartedAt = now
		return appendAssignment(state, actor, r, next, "WORK_STARTED", evidence, now)
	})
}

// AcceptAssignmentResult commits a standalone process result receipt and
// COMPLETED atomically. It records provenance/evidence, not a canonical Task
// Review/Audit verdict. Bound Run results must use their typed result API.
func (s *CanonicalStore) AcceptAssignmentResult(coordinator, sender assignment.Principal, claim assignment.Result) error {
	return s.UpdateCanonical(func(state *CanonicalState) error {
		if state.AssignmentLedger == nil {
			return ErrAssignmentUnknown
		}
		r, ok := state.AssignmentLedger.Records[claim.AssignmentID]
		if !ok {
			return ErrAssignmentUnknown
		}
		if r.RunBinding != nil {
			return fmt.Errorf("%w: use typed Task/Run result gate", assignment.ErrInvalid)
		}
		if coordinator != r.Coordinator() || sender != r.Target() {
			return assignment.ErrInvalid
		}
		if err := r.MatchResult(claim); err != nil {
			return err
		}
		now := s.now().UTC()
		if err := assignmentReady(*state, r, now); err != nil {
			return err
		}
		if err := enforceDelegatedOfferOrAccept(*state, coordinator, DelegatedActionAccept, now); err != nil {
			return err
		}
		if err := enforceDelegatedMutation(*state, coordinator, DelegatedActionAccept, r, now); err != nil {
			return err
		}
		next := r
		next.State, next.AcceptedResult = assignment.Completed, &claim
		return appendAssignment(state, coordinator, r, next, "RESULT", claim.EvidenceRef, now)
	})
}

// applyAssignmentResultCommand extends the existing typed result transaction;
// authority is checked under the same mutex BEFORE Inbox replay lookup.
func (s *CanonicalStore) applyAssignmentResultCommand(actor, messageID string, id taskid.ID, runID string, revision uint64, now time.Time, coordinator []assignment.Principal, apply func(*CanonicalState) (InboxReceipt, error)) (InboxReceipt, bool, error) {
	if actor == "" || strings.TrimSpace(actor) != actor || messageID == "" || strings.TrimSpace(messageID) != messageID || id.Validate() != nil || apply == nil {
		return InboxReceipt{}, false, fmt.Errorf("valid command identity and handler are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.applyAssignmentResultCommandLocked(actor, messageID, id, runID, revision, now, coordinator, nil, apply)
}

type agentAssignmentResultBinding struct {
	AssignmentID          string
	CommandDigest         string
	Issuer                string
	CoordinatorGeneration uint64
	Target                assignment.Principal
}

// applyAssignmentResultCommandLocked is shared by trusted canonical result
// writers and the authenticated Telepathy ingress. Caller holds s.mu; result,
// Inbox, outbox and Assignment completion are committed in the same snapshot.
func (s *CanonicalStore) applyAssignmentResultCommandLocked(actor, messageID string, id taskid.ID, runID string, revision uint64, now time.Time, coordinator []assignment.Principal, agentBinding *agentAssignmentResultBinding, apply func(*CanonicalState) (InboxReceipt, error)) (InboxReceipt, bool, error) {
	if actor == "" || strings.TrimSpace(actor) != actor || messageID == "" || strings.TrimSpace(messageID) != messageID || id.Validate() != nil || apply == nil {
		return InboxReceipt{}, false, fmt.Errorf("valid command identity and handler are required")
	}
	if s.closed || s.failed != nil {
		return InboxReceipt{}, false, fmt.Errorf("store unavailable")
	}
	var record *assignment.Record
	authorityNow := s.now().UTC()
	if s.state.AssignmentLedger != nil {
		for _, r := range s.state.AssignmentLedger.Records {
			if r.RunBinding != nil && r.RunBinding.RunID == runID {
				if agentBinding != nil && r.AssignmentID != agentBinding.AssignmentID {
					continue
				}
				if record != nil {
					return InboxReceipt{}, false, fmt.Errorf("%w: Run is bound to multiple Assignments", assignment.ErrInvalid)
				}
				copy := r
				record = &copy
			}
		}
		if record == nil {
			return InboxReceipt{}, false, ErrAssignmentUnknown
		}
		if len(coordinator) != 1 || coordinator[0] != record.Coordinator() {
			return InboxReceipt{}, false, fmt.Errorf("%w: currentCoordinator/generation required for typed result", assignment.ErrInvalid)
		}
		if record.StableAgentID != actor || record.RunBinding.TaskID != id.String() || record.RunBinding.RunID != runID {
			return InboxReceipt{}, false, assignment.ErrInvalid
		}
		if agentBinding != nil && (record.AssignmentID != agentBinding.AssignmentID || record.Issuer != agentBinding.Issuer || record.CoordinatorGeneration != agentBinding.CoordinatorGeneration || record.Target() != agentBinding.Target || len(agentBinding.CommandDigest) != 64) {
			return InboxReceipt{}, false, assignment.ErrInvalid
		}
		if err := assignmentReady(s.state, *record, authorityNow); err != nil {
			return InboxReceipt{}, false, err
		}
	}
	return s.applyInboxCommandLocked(actor, messageID, id, revision, func(state *CanonicalState) (InboxReceipt, error) {
		receipt, err := apply(state)
		if err != nil || record == nil {
			return receipt, err
		}
		if agentBinding != nil {
			receipt.AssignmentID = record.AssignmentID
			receipt.AgentCommandDigest = agentBinding.CommandDigest
		}
		if len(receipt.EventIDs) == 0 {
			return InboxReceipt{}, falseResultError()
		}
		if err := appendAssignmentResult(state, *record, receipt, messageID, authorityNow); err != nil {
			return InboxReceipt{}, err
		}
		return receipt, nil
	})
}

func appendAssignmentResult(state *CanonicalState, prior assignment.Record, receipt InboxReceipt, messageID string, now time.Time) error {
	raw, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(raw)
	result := assignment.Result{
		AssignmentID: prior.AssignmentID, SubjectSHA: prior.SubjectSHA, Assignee: prior.Assignee,
		SessionID: prior.SessionID, StableAgentID: prior.StableAgentID, Role: prior.Role,
		CoordinatorGeneration: prior.CoordinatorGeneration, ResultID: messageID,
		Digest: hex.EncodeToString(digest[:]), EvidenceRef: receipt.EventIDs[0],
	}
	next := prior
	next.State, next.AcceptedResult = assignment.Completed, &result
	return appendAssignment(state, prior.Coordinator(), prior, next, "RESULT", result.EvidenceRef, now)
}

func falseResultError() error {
	return fmt.Errorf("%w: typed result event required", assignment.ErrInvalid)
}
