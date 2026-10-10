package store

import (
	"coord/envelope"
	"coord/fsm"
	"coord/roles"
	"coord/task"
	"coord/taskid"
	"crypto/ed25519"
	"errors"
	"fmt"
	"reflect"
	"strings"
)

var (
	ErrAgentCommandMalformed           = errors.New("AGENT_COMMAND_MALFORMED")
	ErrAgentCommandWrongRecipient      = errors.New("AGENT_COMMAND_WRONG_RECIPIENT")
	ErrAgentCommandUntrustedSignature  = errors.New("AGENT_COMMAND_UNTRUSTED_SIGNATURE")
	ErrAgentCommandUnknownTask         = errors.New("AGENT_COMMAND_UNKNOWN_TASK")
	ErrAgentCommandNotCurrentWriter    = errors.New("AGENT_COMMAND_NOT_CURRENT_WRITER")
	ErrAgentCommandStaleFence          = errors.New("AGENT_COMMAND_STALE_FENCE")
	ErrAgentCommandFutureFence         = errors.New("AGENT_COMMAND_FUTURE_FENCE")
	ErrAgentCommandAttemptMismatch     = errors.New("AGENT_COMMAND_ATTEMPT_MISMATCH")
	ErrAgentCommandWrongSession        = errors.New("AGENT_COMMAND_WRONG_SESSION")
	ErrAgentCommandStaleGeneration     = errors.New("AGENT_COMMAND_STALE_GENERATION")
	ErrAgentCommandWrongRole           = errors.New("AGENT_COMMAND_WRONG_ROLE")
	ErrAgentCommandStageRoleMismatch   = errors.New("AGENT_COMMAND_STAGE_ROLE_MISMATCH")
	ErrAgentCommandMissingAgentID      = errors.New("AGENT_COMMAND_MISSING_AGENT_ID")
	ErrAgentCommandReplayMismatch      = errors.New("AGENT_COMMAND_REPLAY_MISMATCH")
	ErrAgentCommandTransitionForbidden = errors.New("AGENT_COMMAND_TRANSITION_FORBIDDEN")
)

// AgentCommandFrame is the narrow transport evidence consumed by the canonical
// boundary. transport implements it without importing store; the boundary
// rechecks every mutable AgentSession fact while holding s.mu. Implementations
// passed here must come from the trusted authenticated transport adapter:
// ReceivedBy is trusted evidence that the adapter checked the local recipient.
type AgentCommandFrame interface {
	Envelope() envelope.Envelope
	RawSignedFrame() []byte
	SenderAddress() string
	VerifiedPublicKey() ed25519.PublicKey
	SessionGeneration() uint64
	// ReceivedBy is set by the authenticated adapter only after checking that
	// the signed envelope recipient matches its configured local session.
	ReceivedBy() string
}

// ApplyAgentCommand validates an authenticated frame against the current
// canonical state before replay lookup, then uses the normal replay-before-CAS
// durable commit path for a new command. The callback is the command-specific
// transition handler and runs only after all identity, epoch, role and Stage
// checks have passed under the store lock.
func (s *CanonicalStore) ApplyAgentCommand(frame AgentCommandFrame, id taskid.ID, expectedRevision uint64, apply func(*CanonicalState, fsm.Run) (InboxReceipt, error)) (InboxReceipt, bool, error) {
	if frame == nil || apply == nil || id.Validate() != nil {
		return InboxReceipt{}, false, ErrAgentCommandMalformed
	}
	e := frame.Envelope()
	rawSigned := append([]byte(nil), frame.RawSignedFrame()...)
	senderAddress := frame.SenderAddress()
	verifiedPublicKey := append(ed25519.PublicKey(nil), frame.VerifiedPublicKey()...)
	sessionGeneration := frame.SessionGeneration()
	receivedBy := frame.ReceivedBy()
	if err := e.Validate(); err != nil || e.Task != id.String() {
		return InboxReceipt{}, false, fmt.Errorf("%w: invalid envelope or TaskID", ErrAgentCommandMalformed)
	}
	if receivedBy == "" || receivedBy != e.To {
		return InboxReceipt{}, false, ErrAgentCommandWrongRecipient
	}
	if senderAddress == "" || senderAddress != e.From || len(verifiedPublicKey) != ed25519.PublicKeySize || len(rawSigned) == 0 || sessionGeneration == 0 {
		return InboxReceipt{}, false, ErrAgentCommandUntrustedSignature
	}
	verifiedEnvelope, verifyErr := envelope.Verify(rawSigned, map[string]ed25519.PublicKey{senderAddress: verifiedPublicKey})
	if verifyErr != nil || !reflect.DeepEqual(verifiedEnvelope, e) {
		return InboxReceipt{}, false, ErrAgentCommandUntrustedSignature
	}
	if strings.TrimSpace(e.MessageID) != e.MessageID {
		return InboxReceipt{}, false, ErrAgentCommandMalformed
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return InboxReceipt{}, false, fmt.Errorf("store closed")
	}
	if s.failed != nil {
		return InboxReceipt{}, false, fmt.Errorf("store requires reopen after durability failure: %w", s.failed)
	}
	// Resolve and validate the current session before consulting Run/Fence or
	// replay state. A revoked or stale binding must not obtain an old receipt.
	session, ok := s.state.AgentSessions[senderAddress]
	if !ok || session.Address != senderAddress || !session.Active() {
		return InboxReceipt{}, false, ErrAgentCommandWrongSession
	}
	if sessionGeneration != session.Generation {
		return InboxReceipt{}, false, ErrAgentCommandStaleGeneration
	}
	if !verifiedPublicKey.Equal(ed25519.PublicKey(session.PublicKey)) {
		return InboxReceipt{}, false, ErrAgentCommandUntrustedSignature
	}
	if strings.TrimSpace(session.AgentID) == "" {
		return InboxReceipt{}, false, ErrAgentCommandMissingAgentID
	}

	currentTask, ok := s.state.Tasks[id]
	if !ok {
		return InboxReceipt{}, false, ErrAgentCommandUnknownTask
	}
	run, ok := s.state.Runs[e.Run]
	if !ok || run.TaskID != id {
		return InboxReceipt{}, false, ErrAgentCommandNotCurrentWriter
	}
	if e.From != run.OwnerSession || run.RoleSelection == nil || run.RoleSelection.Session != e.From {
		return InboxReceipt{}, false, ErrAgentCommandWrongSession
	}
	if run.AgentSessionGeneration != sessionGeneration {
		return InboxReceipt{}, false, ErrAgentCommandStaleGeneration
	}
	activeWriters := 0
	activeWriterID := ""
	for runID, candidate := range s.state.Runs {
		if candidate.TaskID == id && !candidate.Terminal() {
			activeWriters++
			activeWriterID = runID
		}
	}
	if activeWriters != 1 || activeWriterID != run.RunID {
		return InboxReceipt{}, false, ErrAgentCommandNotCurrentWriter
	}
	if run.Terminal() || run.Fence != currentTask.Fence {
		return InboxReceipt{}, false, ErrAgentCommandNotCurrentWriter
	}
	if e.Fence < currentTask.Fence {
		return InboxReceipt{}, false, ErrAgentCommandStaleFence
	}
	if e.Fence > currentTask.Fence {
		return InboxReceipt{}, false, ErrAgentCommandFutureFence
	}
	if e.Attempt != run.Attempt {
		return InboxReceipt{}, false, ErrAgentCommandAttemptMismatch
	}
	if strings.TrimSpace(run.RoleSelection.AgentID) == "" {
		return InboxReceipt{}, false, ErrAgentCommandMissingAgentID
	}
	if run.RoleSelection.AgentID != session.AgentID {
		return InboxReceipt{}, false, ErrAgentCommandWrongRole
	}
	if err := validateAgentCommandRoleStage(currentTask, run); err != nil {
		return InboxReceipt{}, false, err
	}
	if err := validateCanonicalRoleIndependence(s.state); err != nil {
		return InboxReceipt{}, false, err
	}

	actorID := run.RoleSelection.AgentID
	key := InboxKey(actorID, e.MessageID)
	if receipt, exists := s.state.Inbox[key]; exists {
		if receipt.TaskID != id || receipt.ExpectedRevision != expectedRevision {
			return InboxReceipt{}, false, ErrAgentCommandReplayMismatch
		}
		return receipt, true, nil
	}
	if currentTask.Revision != expectedRevision {
		return InboxReceipt{}, false, &RevisionConflictError{TaskID: id, Expected: expectedRevision, Actual: currentTask.Revision}
	}
	receipt, replayed, err := s.applyInboxCommandLocked(actorID, e.MessageID, id, expectedRevision, func(state *CanonicalState) (InboxReceipt, error) {
		currentRun, exists := state.Runs[run.RunID]
		if !exists || currentRun.TaskID != id || !reflectAgentCommandRunEqual(run, currentRun) {
			return InboxReceipt{}, ErrAgentCommandNotCurrentWriter
		}
		priorOutbox := len(state.Outbox[run.RunID].Messages)
		receipt, err := apply(state, currentRun)
		if err != nil {
			return InboxReceipt{}, fmt.Errorf("%w: %v", ErrAgentCommandTransitionForbidden, err)
		}
		queue := state.Outbox[run.RunID]
		if len(queue.Messages) < priorOutbox || !hasCommandAcknowledgement(queue.Messages[priorOutbox:], e, receipt) {
			return InboxReceipt{}, fmt.Errorf("%w: command acknowledgement must reference the committed event", ErrAgentCommandTransitionForbidden)
		}
		return receipt, nil
	})
	if err != nil && !errors.Is(err, ErrPersistence) && !errors.Is(err, ErrAgentCommandTransitionForbidden) {
		var conflict *RevisionConflictError
		if !errors.As(err, &conflict) {
			err = fmt.Errorf("%w: %v", ErrAgentCommandTransitionForbidden, err)
		}
	}
	return receipt, replayed, err
}

func hasCommandAcknowledgement(messages []OutboxMessage, command envelope.Envelope, receipt InboxReceipt) bool {
	for _, message := range messages {
		out := message.Envelope
		if out.InReplyTo != command.MessageID || out.From != command.To || out.To != command.From ||
			out.Task != command.Task || out.Run != command.Run || out.Attempt != command.Attempt || out.Fence != command.Fence {
			continue
		}
		for _, eventID := range receipt.EventIDs {
			if message.EventID == eventID {
				return true
			}
		}
	}
	return false
}

// validateAgentCommandRoleStage is called after ApplyAgentCommand establishes
// that RoleSelection is present and bound to the authenticated session.
func validateAgentCommandRoleStage(t task.Task, run fsm.Run) error {
	if run.RoleSelection.Profile.Role != run.RequiredRole.Role {
		return ErrAgentCommandWrongRole
	}
	if t.Status != task.InProgress || t.CurrentStage == nil {
		return ErrAgentCommandStageRoleMismatch
	}
	var requiredStage task.Stage
	switch run.RoleSelection.Profile.Role {
	case roles.Executor:
		requiredStage = task.Execution
	case roles.Reviewer:
		requiredStage = task.Review
	case roles.Auditor:
		requiredStage = task.Audit
	default:
		return ErrAgentCommandWrongRole
	}
	if *t.CurrentStage != requiredStage {
		return ErrAgentCommandStageRoleMismatch
	}
	return nil
}

func reflectAgentCommandRunEqual(before, after fsm.Run) bool {
	return before.RunID == after.RunID && before.TaskID == after.TaskID &&
		before.OwnerSession == after.OwnerSession && before.Fence == after.Fence &&
		before.Attempt == after.Attempt && before.AgentSessionGeneration == after.AgentSessionGeneration &&
		before.RoleSelection != nil && after.RoleSelection != nil &&
		before.RoleSelection.AgentID == after.RoleSelection.AgentID &&
		before.RoleSelection.Session == after.RoleSelection.Session &&
		before.RoleSelection.Profile.Role == after.RoleSelection.Profile.Role
}
