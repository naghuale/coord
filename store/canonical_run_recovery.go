package store

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"coord/fsm"
	"coord/internal/externalaction"
	"coord/task"
	"coord/taskid"
)

type RunRecoveryTransition string

const (
	RunRecoveryFenceOnly          RunRecoveryTransition = "FENCE_ONLY"
	RunRecoveryPrepareReplacement RunRecoveryTransition = "PREPARE_REPLACEMENT"
	RunRecoveryMarkInterrupted    RunRecoveryTransition = "MARK_INTERRUPTED"
	RunRecoveryCompleteExisting   RunRecoveryTransition = "COMPLETE_EXISTING"
	RunRecoveryResumeObservation  RunRecoveryTransition = "RESUME_OBSERVATION"
)

const (
	RunRecoveryRequestedEvent task.EventType = "RUN_RECOVERY_REQUESTED"
	RunGenerationFencedEvent  task.EventType = "RUN_GENERATION_FENCED"
	RunStopProofRecordedEvent task.EventType = "RUN_STOP_PROOF_RECORDED"
	RunRecoveryCompletedEvent task.EventType = "RUN_RECOVERY_COMPLETED"
	RunRecoveryBlockedEvent   task.EventType = "RUN_RECOVERY_BLOCKED"
	runRecoverySummaryPrefix                 = "run-recovery:v1:"
	runRecoveryActor                         = "coordd"
)

var (
	ErrRunRecoveryObservationMissing       = errors.New("RUN_RECOVERY_OBSERVATION_MISSING")
	ErrRunRecoveryGenerationMismatch       = errors.New("RUN_RECOVERY_GENERATION_MISMATCH")
	ErrRunRecoveryRevisionConflict         = errors.New("RUN_RECOVERY_REVISION_CONFLICT")
	ErrRunRecoveryStopProofInsufficient    = errors.New("RUN_RECOVERY_STOP_PROOF_INSUFFICIENT")
	ErrRunRecoveryFenceNotCommitted        = errors.New("RUN_RECOVERY_FENCE_NOT_COMMITTED")
	ErrRunRecoveryAuthorityNotProven       = errors.New("RUN_RECOVERY_AUTHORITY_NOT_PROVEN")
	ErrRunRecoveryDuplicateCommandConflict = errors.New("RUN_RECOVERY_DUPLICATE_COMMAND_CONFLICT")

	ErrRunRecoveryTransitionForbidden = errors.New("RUN_RECOVERY_TRANSITION_FORBIDDEN")
)

// RunRecoveryCommand carries only existing canonical identities. Replacement
// identity fields remain absent while Q3 is unresolved.
type RunRecoveryCommand struct {
	MessageID           string
	TaskID              taskid.ID
	RunID               string
	ExpectedRevision    uint64
	ExpectedGeneration  uint64
	ReasonCode          string
	StopProofEvidenceID string
	AllowedTransitions  []RunRecoveryTransition
}

// RunRecoveryReceipt is projected from the durable InboxReceipt and its
// same-commit events. Replayed is return metadata and is never persisted.
type RunRecoveryReceipt struct {
	MessageID           string
	TaskID              taskid.ID
	RunID               string
	SelectedTransition  RunRecoveryTransition
	PreviousRevision    uint64
	CommittedRevision   uint64
	EventIDs            []string
	StopProofEvidenceID string
	Replayed            bool
}

type runRecoveryIdentity struct {
	ActorID             string
	MessageID           string
	TaskID              taskid.ID
	RunID               string
	ExpectedRevision    uint64
	ExpectedGeneration  uint64
	ReasonCode          string
	StopProofEvidenceID string
	AllowedTransitions  []RunRecoveryTransition
	SelectedTransition  RunRecoveryTransition
}

func normalizeRunRecoveryTransitions(values []RunRecoveryTransition) ([]RunRecoveryTransition, error) {
	if len(values) == 0 {
		return nil, ErrRunRecoveryTransitionForbidden
	}
	seen := make(map[RunRecoveryTransition]bool, len(values))
	out := append([]RunRecoveryTransition(nil), values...)
	for _, value := range out {
		switch value {
		case RunRecoveryFenceOnly, RunRecoveryPrepareReplacement, RunRecoveryMarkInterrupted,
			RunRecoveryCompleteExisting, RunRecoveryResumeObservation:
		default:
			return nil, ErrRunRecoveryTransitionForbidden
		}
		if seen[value] {
			return nil, ErrRunRecoveryTransitionForbidden
		}
		seen[value] = true
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

func encodeRunRecoveryIdentity(identity runRecoveryIdentity) (string, error) {
	data, err := json.Marshal(identity)
	if err != nil {
		return "", err
	}
	return runRecoverySummaryPrefix + base64.RawURLEncoding.EncodeToString(data), nil
}

func decodeRunRecoveryIdentity(summary string) (runRecoveryIdentity, bool) {
	if !strings.HasPrefix(summary, runRecoverySummaryPrefix) {
		return runRecoveryIdentity{}, false
	}
	data, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(summary, runRecoverySummaryPrefix))
	if err != nil {
		return runRecoveryIdentity{}, false
	}
	var identity runRecoveryIdentity
	if err := json.Unmarshal(data, &identity); err != nil {
		return runRecoveryIdentity{}, false
	}
	return identity, true
}

// ApplyRunRecoveryCommand commits MARK_INTERRUPTED through the canonical Inbox
// transaction. The other transitions remain explicit fail-closed outcomes:
// FO-C disallows standalone fencing, Q3 lacks a replacement source, CEE lacks
// exact result provenance, and stopped evidence cannot resume its writer.
func (s *CanonicalStore) ApplyRunRecoveryCommand(cmd RunRecoveryCommand) (RunRecoveryReceipt, bool, error) {
	actorID := runRecoveryActor
	transitions, err := normalizeRunRecoveryTransitions(cmd.AllowedTransitions)
	if err != nil {
		return RunRecoveryReceipt{}, false, err
	}
	if strings.TrimSpace(cmd.MessageID) == "" || strings.TrimSpace(cmd.MessageID) != cmd.MessageID ||
		strings.TrimSpace(cmd.RunID) == "" || strings.TrimSpace(cmd.RunID) != cmd.RunID ||
		cmd.TaskID.Validate() != nil || cmd.ExpectedGeneration == 0 || strings.TrimSpace(cmd.ReasonCode) == "" || strings.TrimSpace(cmd.ReasonCode) != cmd.ReasonCode {
		return RunRecoveryReceipt{}, false, ErrRunRecoveryTransitionForbidden
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return RunRecoveryReceipt{}, false, errors.New("store closed")
	}
	if s.failed != nil {
		return RunRecoveryReceipt{}, false, fmt.Errorf("store requires reopen after durability failure: %w", s.failed)
	}

	identity := runRecoveryIdentity{
		ActorID: actorID, MessageID: cmd.MessageID, TaskID: cmd.TaskID, RunID: cmd.RunID,
		ExpectedRevision: cmd.ExpectedRevision, ExpectedGeneration: cmd.ExpectedGeneration,
		ReasonCode: cmd.ReasonCode, StopProofEvidenceID: cmd.StopProofEvidenceID,
		AllowedTransitions: transitions,
	}
	// MARK_INTERRUPTED is the only transition that can commit under the closed
	// fail-closed contract. Include it in the command identity before replay lookup.
	identity.SelectedTransition = RunRecoveryMarkInterrupted
	key := InboxKey(actorID, cmd.MessageID)
	if prior, exists := s.state.Inbox[key]; exists {
		if prior.TaskID != cmd.TaskID || prior.RunID != cmd.RunID || prior.ExpectedRevision != cmd.ExpectedRevision {
			return RunRecoveryReceipt{}, false, ErrRunRecoveryDuplicateCommandConflict
		}
		recorded, ok := recoveryIdentityForReceipt(s.state, prior)
		if !ok || !reflect.DeepEqual(recorded, identity) {
			return RunRecoveryReceipt{}, false, ErrRunRecoveryDuplicateCommandConflict
		}
		receipt := runRecoveryReceipt(prior, recorded)
		receipt.Replayed = true
		return receipt, true, nil
	}
	if priorRecoveryForRun(s.state, cmd.TaskID, cmd.RunID) {
		return RunRecoveryReceipt{}, false, ErrRunRecoveryDuplicateCommandConflict
	}

	currentTask, taskOK := s.state.Tasks[cmd.TaskID]
	run, runOK := s.state.Runs[cmd.RunID]
	if !taskOK || !runOK || run.TaskID != cmd.TaskID {
		return RunRecoveryReceipt{}, false, ErrRunRecoveryTransitionForbidden
	}
	if cmd.ExpectedRevision != currentTask.Revision {
		return RunRecoveryReceipt{}, false, fmt.Errorf("%w: %w", ErrRunRecoveryRevisionConflict,
			&RevisionConflictError{TaskID: cmd.TaskID, Expected: cmd.ExpectedRevision, Actual: currentTask.Revision})
	}
	if cmd.ExpectedGeneration != run.AgentSessionGeneration {
		return RunRecoveryReceipt{}, false, ErrRunRecoveryGenerationMismatch
	}
	if run.Terminal() || run.Status != fsm.Stalled || !run.RecoveryNeeded || run.RecoveryClaim == "" {
		return RunRecoveryReceipt{}, false, ErrRunRecoveryFenceNotCommitted
	}
	if session, exists := s.state.AgentSessions[run.OwnerSession]; exists &&
		session.Active() && session.Generation == run.AgentSessionGeneration {
		return RunRecoveryReceipt{}, false, ErrRunRecoveryAuthorityNotProven
	}
	if _, err := validateRunRecoveryStopProof(s.state, cmd); err != nil {
		return RunRecoveryReceipt{}, false, err
	}

	selected, err := selectRunRecoveryTransition(transitions)
	if err != nil {
		return RunRecoveryReceipt{}, false, err
	}
	identity.SelectedTransition = selected
	if selected == RunRecoveryFenceOnly {
		return RunRecoveryReceipt{}, false, ErrRunRecoveryFenceNotCommitted
	}
	if selected != RunRecoveryMarkInterrupted {
		return RunRecoveryReceipt{}, false, ErrRunRecoveryTransitionForbidden
	}

	var committed runRecoveryIdentity
	inboxReceipt, replayed, err := s.applyInboxCommandLocked(actorID, cmd.MessageID, cmd.TaskID, cmd.ExpectedRevision,
		func(state *CanonicalState) (InboxReceipt, error) {
			candidateTask, ok := state.Tasks[cmd.TaskID]
			if !ok || candidateTask.Revision != cmd.ExpectedRevision {
				actual := uint64(0)
				if ok {
					actual = candidateTask.Revision
				}
				return InboxReceipt{}, &RevisionConflictError{TaskID: cmd.TaskID, Expected: cmd.ExpectedRevision, Actual: actual}
			}
			candidateRun, ok := state.Runs[cmd.RunID]
			if !ok || candidateRun.AgentSessionGeneration != cmd.ExpectedGeneration ||
				candidateRun.RecoveryClaim != run.RecoveryClaim || candidateRun.OwnerSession != run.OwnerSession {
				return InboxReceipt{}, ErrRunRecoveryGenerationMismatch
			}
			if _, err := validateRunRecoveryStopProof(*state, cmd); err != nil {
				return InboxReceipt{}, err
			}
			now := s.now()
			if now.IsZero() || now.Before(candidateTask.UpdatedAt) {
				return InboxReceipt{}, fmt.Errorf("recovery timestamp precedes Task state")
			}
			eventSummary, err := encodeRunRecoveryIdentity(identity)
			if err != nil {
				return InboxReceipt{}, err
			}
			candidateRun.Status = fsm.Failed
			candidateRun.ResumeStatus = ""
			candidateRun.RecoveryNeeded = false
			candidateRun.RecoveryClaim = ""
			candidateRun.Blocker = cmd.ReasonCode
			candidateRun.LastUpdate = now
			state.Runs[cmd.RunID] = candidateRun

			candidateTask.Revision++
			candidateTask.LastEventSeq += 3
			candidateTask.Status = task.Blocked
			candidateTask.UpdatedAt = now
			state.Tasks[cmd.TaskID] = candidateTask
			eventTypes := []task.EventType{RunRecoveryRequestedEvent, RunStopProofRecordedEvent, RunRecoveryBlockedEvent}
			eventIDs := make([]string, 0, len(eventTypes))
			for i, eventType := range eventTypes {
				eventSeq := candidateTask.LastEventSeq - uint64(len(eventTypes)-1-i)
				eventID := task.EventID(cmd.TaskID, eventSeq)
				eventIDs = append(eventIDs, eventID)
				state.Events = append(state.Events, task.DomainEvent{
					ID: eventID, TaskID: cmd.TaskID, RunID: cmd.RunID, EventSeq: eventSeq,
					TaskRevision: candidateTask.Revision, TaskFence: candidateTask.Fence,
					Type: eventType, ActorID: actorID, Summary: eventSummary, CreatedAt: now,
				})
			}
			committed = identity
			return InboxReceipt{
				ActorID: actorID, MessageID: cmd.MessageID, TaskID: cmd.TaskID, RunID: cmd.RunID,
				EventIDs: eventIDs, ExpectedRevision: cmd.ExpectedRevision,
				CommittedRevision: candidateTask.Revision, RecordedAt: now,
			}, nil
		})
	if err != nil {
		var revisionConflict *RevisionConflictError
		if errors.As(err, &revisionConflict) {
			return RunRecoveryReceipt{}, false, fmt.Errorf("%w: %w", ErrRunRecoveryRevisionConflict, err)
		}
		return RunRecoveryReceipt{}, false, err
	}
	committed.SelectedTransition = RunRecoveryMarkInterrupted
	return runRecoveryReceipt(inboxReceipt, committed), replayed, nil
}

// AllowedTransitions is an unordered set. A successful transition is selected
// only when it is the unique implemented member; input order never assigns a
// hidden fallback priority.
func selectRunRecoveryTransition(allowed []RunRecoveryTransition) (RunRecoveryTransition, error) {
	eligible := make([]RunRecoveryTransition, 0, 1)
	for _, transition := range allowed {
		if transition == RunRecoveryMarkInterrupted {
			eligible = append(eligible, transition)
		}
	}
	if len(eligible) > 1 {
		return "", ErrRunRecoveryTransitionForbidden
	}
	if len(eligible) == 1 {
		return eligible[0], nil
	}
	if len(allowed) == 1 {
		return allowed[0], nil
	}
	return "", ErrRunRecoveryTransitionForbidden
}

func validateRunRecoveryStopProof(state CanonicalState, cmd RunRecoveryCommand) (externalaction.Action, error) {
	if strings.TrimSpace(cmd.StopProofEvidenceID) == "" {
		return externalaction.Action{}, ErrRunRecoveryObservationMissing
	}
	action, exists := state.Orchestration.Actions[cmd.StopProofEvidenceID]
	if !exists {
		return externalaction.Action{}, ErrRunRecoveryObservationMissing
	}
	run, runOK := state.Runs[cmd.RunID]
	if !runOK {
		return externalaction.Action{}, ErrRunRecoveryStopProofInsufficient
	}
	if action.Status == externalaction.Observed || action.Status == externalaction.Dispatched || action.Status == externalaction.Pending {
		return externalaction.Action{}, ErrRunRecoveryObservationMissing
	}
	if action.Status != externalaction.Completed {
		return externalaction.Action{}, ErrRunRecoveryStopProofInsufficient
	}
	if action.Kind != externalaction.Stop ||
		action.Binding.TaskID != cmd.TaskID.String() || action.Binding.RunID != cmd.RunID ||
		action.Binding.SessionID != run.OwnerSession || action.Binding.SubjectSHA != run.TargetSHA ||
		action.Binding.Generation != run.AgentSessionGeneration || action.Binding.Fence != run.Fence ||
		action.Result == nil || action.Result.Outcome != "STOPPED" || !action.Result.Stopped ||
		action.Result.StopMethod != "SUPERVISOR_CONFIRMED_EXIT" {
		return externalaction.Action{}, ErrRunRecoveryStopProofInsufficient
	}
	parent, parentOK := state.Orchestration.Actions[action.ParentID]
	if !parentOK || parent.Kind != externalaction.Fence || parent.Status != externalaction.Completed ||
		parent.Binding != action.Binding || parent.Result == nil || parent.Result.Outcome != "FENCED" {
		return externalaction.Action{}, ErrRunRecoveryStopProofInsufficient
	}
	observationFound, receiptFound := false, false
	for observationID, observation := range state.Orchestration.Observations {
		if observation.ActionID != action.ID || observation.Stale || observation.ClaimEpoch != action.Claim.Epoch ||
			observation.RequestDigest != action.RequestDigest || observation.Digest != externalaction.Digest(*action.Result) ||
			!reflect.DeepEqual(observation.Result, *action.Result) {
			continue
		}
		observationFound = true
		if receipt, ok := state.Orchestration.Receipts[observationID]; ok &&
			receipt.ObservationID == observationID && receipt.ActionID == action.ID &&
			receipt.Digest == observation.Digest && receipt.Revision > 0 && receipt.Revision <= state.Revision {
			receiptFound = true
		}
	}
	if !observationFound || !receiptFound {
		return externalaction.Action{}, ErrRunRecoveryStopProofInsufficient
	}
	return action, nil
}

func recoveryIdentityForReceipt(state CanonicalState, receipt InboxReceipt) (runRecoveryIdentity, bool) {
	for _, eventID := range receipt.EventIDs {
		for _, event := range state.Events {
			if event.ID != eventID {
				continue
			}
			identity, ok := decodeRunRecoveryIdentity(event.Summary)
			if ok && identity.MessageID == receipt.MessageID && identity.TaskID == receipt.TaskID &&
				identity.RunID == receipt.RunID && identity.ExpectedRevision == receipt.ExpectedRevision {
				return identity, true
			}
		}
	}
	return runRecoveryIdentity{}, false
}

func priorRecoveryForRun(state CanonicalState, taskID taskid.ID, runID string) bool {
	for _, event := range state.Events {
		if event.TaskID == taskID && event.RunID == runID {
			switch event.Type {
			case RunRecoveryRequestedEvent, RunStopProofRecordedEvent, RunRecoveryCompletedEvent, RunRecoveryBlockedEvent:
				return true
			}
		}
	}
	return false
}

func runRecoveryReceipt(receipt InboxReceipt, identity runRecoveryIdentity) RunRecoveryReceipt {
	return RunRecoveryReceipt{
		MessageID: receipt.MessageID, TaskID: receipt.TaskID, RunID: receipt.RunID,
		SelectedTransition: identity.SelectedTransition, PreviousRevision: receipt.ExpectedRevision,
		CommittedRevision: receipt.CommittedRevision, EventIDs: append([]string(nil), receipt.EventIDs...),
		StopProofEvidenceID: identity.StopProofEvidenceID,
	}
}
