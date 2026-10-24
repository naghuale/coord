package store

import (
	"coord/assignment"
	"coord/envelope"
	"coord/fsm"
	"coord/roles"
	"coord/task"
	"coord/taskid"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"
)

var (
	ErrAgentCommandMalformed           = errors.New("AGENT_COMMAND_MALFORMED")
	ErrAgentCommandWrongRecipient      = errors.New("AGENT_COMMAND_WRONG_RECIPIENT")
	ErrAgentCommandUntrustedSignature  = errors.New("AGENT_COMMAND_UNTRUSTED_SIGNATURE")
	ErrAgentCommandUnknownTask         = errors.New("AGENT_COMMAND_UNKNOWN_TASK")
	ErrAgentCommandNotCurrentWriter    = errors.New("AGENT_COMMAND_NOT_CURRENT_WRITER")
	ErrAgentCommandStaleFence          = errors.New("AGENT_COMMAND_STALE_FENCE")
	ErrAgentCommandSubjectMismatch     = errors.New("AGENT_COMMAND_SUBJECT_MISMATCH")
	ErrAgentCommandFutureFence         = errors.New("AGENT_COMMAND_FUTURE_FENCE")
	ErrAgentCommandAttemptMismatch     = errors.New("AGENT_COMMAND_ATTEMPT_MISMATCH")
	ErrAgentCommandWrongSession        = errors.New("AGENT_COMMAND_WRONG_SESSION")
	ErrAgentCommandStaleGeneration     = errors.New("AGENT_COMMAND_STALE_GENERATION")
	ErrAgentCommandWrongRole           = errors.New("AGENT_COMMAND_WRONG_ROLE")
	ErrAgentCommandStageRoleMismatch   = errors.New("AGENT_COMMAND_STAGE_ROLE_MISMATCH")
	ErrAgentCommandMissingAgentID      = errors.New("AGENT_COMMAND_MISSING_AGENT_ID")
	ErrAgentCommandReplayMismatch      = errors.New("AGENT_COMMAND_REPLAY_MISMATCH")
	ErrAgentCommandTransitionForbidden = errors.New("AGENT_COMMAND_TRANSITION_FORBIDDEN")
	ErrAgentCommandVerdictRejected     = errors.New("AGENT_COMMAND_VERDICT_REJECTED")
)

type AgentResultDisposition string

const (
	AgentResultAccepted     AgentResultDisposition = "ACCEPTED"
	AgentResultNoAcceptance AgentResultDisposition = "NO_ACCEPTANCE"
)

// ClassifyAgentResult is the explicit role-scoped protocol table. A no-
// acceptance verdict receives a durable dedupe receipt but never a Task or
// Assignment result transition.
func ClassifyAgentResult(role roles.Role, p envelope.AgentResultPayload) (AgentResultDisposition, error) {
	switch role {
	case roles.Executor:
		if p.Verdict != "" || p.Outcome == "" {
			return "", ErrAgentCommandVerdictRejected
		}
		switch task.ExecutorOutcome(p.Outcome) {
		case task.ExecutorSuccess, task.ExecutorFailure, task.ExecutorPartial:
			return AgentResultAccepted, nil
		default:
			return "", ErrAgentCommandVerdictRejected
		}
	case roles.Reviewer:
		if p.Outcome != "" || len(p.Properties) != 0 {
			return "", ErrAgentCommandVerdictRejected
		}
		switch p.Verdict {
		case "REVIEW_ACCEPTED", "CHANGES_REQUIRED":
			return AgentResultAccepted, nil
		case "NOT_EVALUATED":
			return AgentResultNoAcceptance, nil
		case "INVALID_SUBMISSION":
			return "", ErrAgentCommandVerdictRejected
		default:
			return "", ErrAgentCommandVerdictRejected
		}
	case roles.Auditor:
		if p.Outcome != "" {
			return "", ErrAgentCommandVerdictRejected
		}
		switch p.Verdict {
		case "AUDIT_ACCEPTED", "CHANGES_REQUIRED":
			return AgentResultAccepted, nil
		case "AUDIT_INCOMPLETE", "AUDIT_BLOCKED":
			return AgentResultNoAcceptance, nil
		case "AUDIT_INELIGIBLE":
			return "", ErrAgentCommandVerdictRejected
		default:
			return "", ErrAgentCommandVerdictRejected
		}
	default:
		return "", ErrAgentCommandVerdictRejected
	}
}

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
	if frame == nil || id.Validate() != nil {
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
	if e.SchemaVersion == envelope.SchemaVersionLegacyAgent && apply == nil {
		return InboxReceipt{}, false, ErrAgentCommandMalformed
	}
	if e.SchemaVersion == envelope.SchemaVersionAgentResult && e.AgentResultPayload == nil {
		return InboxReceipt{}, false, ErrAgentCommandMalformed
	}
	if e.SchemaVersion == envelope.SchemaVersionAgentResult && apply != nil {
		return InboxReceipt{}, false, ErrAgentCommandMalformed
	}
	if receivedBy == "" || receivedBy != e.To {
		return InboxReceipt{}, false, ErrAgentCommandWrongRecipient
	}
	if senderAddress == "" || senderAddress != e.From || len(verifiedPublicKey) != ed25519.PublicKeySize || len(rawSigned) == 0 || sessionGeneration == 0 {
		return InboxReceipt{}, false, ErrAgentCommandUntrustedSignature
	}
	verifiedEnvelope, verifyErr := envelope.Verify(rawSigned, map[string]ed25519.PublicKey{senderAddress: verifiedPublicKey})
	if verifyErr != nil || !reflect.DeepEqual(verifiedEnvelope, e) {
		return InboxReceipt{}, false, fmt.Errorf("%w: signed envelope verification failed: %v", ErrAgentCommandUntrustedSignature, verifyErr)
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
		return InboxReceipt{}, false, fmt.Errorf("%w: signing key differs from current AgentSession", ErrAgentCommandUntrustedSignature)
	}
	if strings.TrimSpace(session.AgentID) == "" {
		return InboxReceipt{}, false, ErrAgentCommandMissingAgentID
	}
	if e.SchemaVersion == envelope.SchemaVersionAgentResult {
		return s.applyAuthenticatedAgentResultLocked(e, id, expectedRevision, rawSigned, senderAddress, session.AgentID, sessionGeneration)
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
		// Transport receipt/ACK/heartbeat cannot grant or rewrite assignment
		// authority. The coordinator result command has a separate atomic gate.
		if !reflect.DeepEqual(s.state.AssignmentLedger, state.AssignmentLedger) {
			return InboxReceipt{}, fmt.Errorf("%w: transport cannot change assignment state", ErrAgentCommandTransitionForbidden)
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

func (s *CanonicalStore) applyAuthenticatedAgentResultLocked(e envelope.Envelope, id taskid.ID, expectedRevision uint64, rawSigned []byte, senderAddress, actorID string, generation uint64) (InboxReceipt, bool, error) {
	p := e.AgentResultPayload
	if p == nil || p.ExpectedRevision != expectedRevision || id.String() != e.Task {
		return InboxReceipt{}, false, ErrAgentCommandMalformed
	}
	digestBytes := sha256.Sum256(rawSigned)
	digest := hex.EncodeToString(digestBytes[:])
	key := InboxKey(actorID, e.MessageID)
	if prior, exists := s.state.Inbox[key]; exists {
		if prior.AgentCommandDigest != digest || prior.AssignmentID != p.AssignmentID || prior.TaskID != id || prior.RunID != e.Run || prior.ActorID != actorID || prior.MessageID != e.MessageID || prior.ExpectedRevision != expectedRevision {
			return InboxReceipt{}, false, ErrAgentCommandReplayMismatch
		}
		return prior, true, nil
	}
	currentTask, ok := s.state.Tasks[id]
	if !ok {
		return InboxReceipt{}, false, ErrAgentCommandUnknownTask
	}
	run, ok := s.state.Runs[e.Run]
	if !ok || run.TaskID != id || run.RoleSelection == nil {
		return InboxReceipt{}, false, ErrAgentCommandNotCurrentWriter
	}
	correlated := false
	for _, messageID := range run.TaskMessages {
		if messageID == e.InReplyTo {
			correlated = true
			break
		}
	}
	if !correlated {
		return InboxReceipt{}, false, ErrAgentCommandMalformed
	}
	if e.From != run.OwnerSession || run.RoleSelection.Session != e.From {
		return InboxReceipt{}, false, ErrAgentCommandWrongSession
	}
	if run.AgentSessionGeneration != generation {
		return InboxReceipt{}, false, ErrAgentCommandStaleGeneration
	}
	if run.RoleSelection.AgentID != actorID {
		return InboxReceipt{}, false, ErrAgentCommandWrongRole
	}
	activeWriters, activeWriterID := 0, ""
	for runID, candidate := range s.state.Runs {
		if candidate.TaskID == id && !candidate.Terminal() {
			activeWriters++
			activeWriterID = runID
		}
	}
	if activeWriters != 1 || activeWriterID != run.RunID || run.Terminal() || run.Fence != currentTask.Fence {
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
	if e.TargetSHA != run.TargetSHA {
		return InboxReceipt{}, false, ErrAgentCommandSubjectMismatch
	}
	if err := validateAgentCommandRoleStage(currentTask, run); err != nil {
		return InboxReceipt{}, false, err
	}
	if err := validateCanonicalRoleIndependence(s.state); err != nil {
		return InboxReceipt{}, false, err
	}
	disposition, err := ClassifyAgentResult(run.RoleSelection.Profile.Role, *p)
	if err != nil {
		return InboxReceipt{}, false, err
	}
	ledger := s.state.AssignmentLedger
	if ledger == nil {
		return InboxReceipt{}, false, ErrAssignmentUnknown
	}
	r, exists := ledger.Records[p.AssignmentID]
	if !exists {
		return InboxReceipt{}, false, ErrAssignmentUnknown
	}
	now := s.now().UTC()
	target := assignment.Principal{SessionID: senderAddress, AgentID: actorID, Generation: generation}
	if r.AssignmentID != p.AssignmentID || r.Target() != target || r.Issuer != p.Issuer ||
		r.CurrentCoordinator != e.To || r.CoordinatorGeneration != p.CoordinatorGeneration ||
		r.SubjectSHA != e.TargetSHA || r.Role != string(run.RoleSelection.Profile.Role) || r.WorkStartedAt.IsZero() ||
		r.RunBinding == nil || r.RunBinding.TaskID != e.Task || r.RunBinding.RunID != e.Run ||
		r.RunBinding.Fence != e.Fence || r.RunBinding.Attempt != e.Attempt {
		return InboxReceipt{}, false, ErrAgentCommandTransitionForbidden
	}
	if run.Status != fsm.Started && run.Status != fsm.ResultReady {
		return InboxReceipt{}, false, ErrAgentCommandNotCurrentWriter
	}
	if err := assignmentReady(s.state, r, now); err != nil {
		return InboxReceipt{}, false, fmt.Errorf("%w: %v", ErrAgentCommandTransitionForbidden, err)
	}
	if currentTask.Revision != expectedRevision {
		return InboxReceipt{}, false, &RevisionConflictError{TaskID: id, Expected: expectedRevision, Actual: currentTask.Revision}
	}
	if disposition == AgentResultNoAcceptance {
		if err := validateNoAcceptanceAgentResult(s.state, currentTask, run, *p, now); err != nil {
			return InboxReceipt{}, false, fmt.Errorf("%w: invalid no-acceptance result evidence: %v", ErrAgentCommandTransitionForbidden, err)
		}
		return s.recordAgentNoAcceptanceLocked(actorID, e, digest, id, expectedRevision, now)
	}
	binding := &agentAssignmentResultBinding{
		AssignmentID: r.AssignmentID, CommandDigest: digest, Issuer: r.Issuer,
		CoordinatorGeneration: r.CoordinatorGeneration, Target: target,
	}
	return s.applyAssignmentResultCommandLocked(actorID, e.MessageID, id, e.Run, expectedRevision, now,
		[]assignment.Principal{r.Coordinator()}, binding, func(state *CanonicalState) (InboxReceipt, error) {
			priorOutbox := len(state.Outbox[run.RunID].Messages)
			receipt, err := applyAgentResultToState(state, actorID, run, e, now)
			if err != nil {
				return InboxReceipt{}, err
			}
			if !hasCommandAcknowledgement(state.Outbox[run.RunID].Messages[priorOutbox:], e, receipt) {
				return InboxReceipt{}, fmt.Errorf("%w: version-2 result acknowledgement must reference the committed event", ErrAgentCommandTransitionForbidden)
			}
			return receipt, nil
		})
}

func (s *CanonicalStore) recordAgentNoAcceptanceLocked(actorID string, e envelope.Envelope, digest string, id taskid.ID, expectedRevision uint64, now time.Time) (InboxReceipt, bool, error) {
	if s.closed || s.failed != nil {
		return InboxReceipt{}, false, fmt.Errorf("store unavailable")
	}
	key := InboxKey(actorID, e.MessageID)
	if prior, exists := s.state.Inbox[key]; exists {
		if prior.AgentCommandDigest != digest || prior.AssignmentID != e.AgentResultPayload.AssignmentID || prior.ExpectedRevision != expectedRevision || prior.TaskID != id || prior.RunID != e.Run {
			return InboxReceipt{}, false, ErrAgentCommandReplayMismatch
		}
		return prior, true, nil
	}
	current, exists := s.state.Tasks[id]
	if !exists {
		return InboxReceipt{}, false, ErrAgentCommandUnknownTask
	}
	if current.Revision != expectedRevision {
		return InboxReceipt{}, false, &RevisionConflictError{TaskID: id, Expected: expectedRevision, Actual: current.Revision}
	}
	next, err := copyCanonicalState(s.state)
	if err != nil {
		return InboxReceipt{}, false, fmt.Errorf("copy canonical state: %w", err)
	}
	next.SchemaVersion = CanonicalSchemaVersion
	receipt := InboxReceipt{
		ActorID: actorID, MessageID: e.MessageID, AgentCommandDigest: digest,
		AssignmentID: e.AgentResultPayload.AssignmentID, TaskID: id, RunID: e.Run,
		EventIDs: []string{}, ExpectedRevision: expectedRevision, CommittedRevision: expectedRevision, RecordedAt: now,
	}
	next.Inbox[key] = receipt
	if err := rejectUnauthorizedGrantMutation(s.state, next); err != nil {
		return InboxReceipt{}, false, err
	}
	if s.state.Revision == ^uint64(0) {
		return InboxReceipt{}, false, fmt.Errorf("canonical revision exhausted")
	}
	next.Revision = s.state.Revision + 1
	if err := ValidateCanonicalState(next); err != nil {
		return InboxReceipt{}, false, err
	}
	if err := ValidateCanonicalTransition(s.state, next); err != nil {
		return InboxReceipt{}, false, err
	}
	if err := s.validateAssignmentMode(next); err != nil {
		return InboxReceipt{}, false, err
	}
	committed, err := copyCanonicalState(next)
	if err != nil {
		return InboxReceipt{}, false, fmt.Errorf("copy updated canonical state: %w", err)
	}
	if err := s.p.Save(committed); err != nil {
		s.failed = fmt.Errorf("%w: %w", ErrPersistence, err)
		return InboxReceipt{}, false, s.failed
	}
	s.state = committed
	return receipt, false, nil
}

func hasCommandAcknowledgement(messages []OutboxMessage, command envelope.Envelope, receipt InboxReceipt) bool {
	for _, message := range messages {
		out := message.Envelope
		if out.InReplyTo != command.MessageID || out.From != command.To || out.To != command.From ||
			out.Task != command.Task || out.Run != command.Run || out.Attempt != command.Attempt || out.Fence != command.Fence {
			continue
		}
		if command.SchemaVersion == envelope.SchemaVersionAgentResult {
			if command.AgentResultPayload == nil || out.SchemaVersion != envelope.SchemaVersionAgentResult || out.Kind != envelope.ResultAck ||
				out.MessageID != command.MessageID+".result-ack" || out.ResultAckPayload == nil ||
				out.ResultAckPayload.AssignmentID != command.AgentResultPayload.AssignmentID || out.ResultAckPayload.Disposition != "ACCEPTED" ||
				out.ResultAckPayload.Outcome != "SUCCESS" || !out.Timestamp.Equal(receipt.RecordedAt) {
				continue
			}
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
