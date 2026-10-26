package store

import (
	"coord/agentsession"
	"coord/assignment"
	"coord/envelope"
	"coord/fsm"
	"coord/internal/externalaction"
	ownercredential "coord/ownercredential"
	"coord/roles"
	"coord/task"
	"coord/taskid"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"sync"
	"time"
)

const CanonicalSchemaVersion uint32 = 6

// ErrFenceExhausted prevents Task.Fence from wrapping to an earlier writer epoch.
var ErrFenceExhausted = errors.New("task fence exhausted")

var ErrOwnerCommandRequired = errors.New("OwnerCredential and Owner Task writes require the authenticated command boundary")

var ErrOwnerBootstrapClosed = errors.New("OwnerCredential bootstrap is closed for this canonical state")

// OutboxMessage preserves the wire envelope identity and, for new Task-scoped
// commands, the DomainEvent committed by the same command.
type OutboxMessage struct {
	Envelope envelope.Envelope `json:"envelope"`
	EventID  string            `json:"event_id,omitempty"`
}

// OutboxQueue is the durable delivery state for one Run. Message order and
// retry bookkeeping match the coordinator's legacy Run view.
type OutboxQueue struct {
	Messages []OutboxMessage `json:"messages"`
	RetryAt  time.Time       `json:"retry_at,omitempty"`
	Retries  int             `json:"retries,omitempty"`
}

// InboxReceipt records a committed, deduplicated Task command. ActorID is a
// stable authenticated principal, not a transport session address.
type InboxReceipt struct {
	ActorID                 string    `json:"actor_id"`
	MessageID               string    `json:"message_id"`
	AgentCommandDigest      string    `json:"agent_command_digest,omitempty"`
	AssignmentID            string    `json:"assignment_id,omitempty"`
	OwnerCommandDigest      string    `json:"owner_command_digest,omitempty"`
	OwnerNonce              string    `json:"owner_nonce,omitempty"`
	OwnerCredentialRevision uint64    `json:"owner_credential_revision,omitempty"`
	TaskID                  taskid.ID `json:"task_id"`
	RunID                   string    `json:"run_id,omitempty"`
	EventIDs                []string  `json:"event_ids"`
	ExpectedRevision        uint64    `json:"expected_revision"`
	CommittedRevision       uint64    `json:"committed_revision"`
	RecordedAt              time.Time `json:"recorded_at"`
}

type RevisionConflictError struct {
	TaskID   taskid.ID
	Expected uint64
	Actual   uint64
}

func (e *RevisionConflictError) Error() string {
	return fmt.Sprintf("task %q revision conflict: expected %d, actual %d", e.TaskID, e.Expected, e.Actual)
}

func InboxKey(actorID, messageID string) string {
	return fmt.Sprintf("%d:%s%s", len(actorID), actorID, messageID)
}

// CanonicalState is the versioned durable boundary for Coord-owned state.
type CanonicalState struct {
	AssignmentLedger       *assignment.Ledger                   `json:"assignment_ledger,omitempty"`
	OwnerCredentials       map[string]ownercredential.Aggregate `json:"owner_credentials"`
	CoordinatorDelegations map[string]DelegationRecord          `json:"coordinator_delegations,omitempty"`
	SchemaMigrations       []SchemaMigration                    `json:"schema_migrations,omitempty"`
	Revision               uint64                               `json:"revision"`
	Orchestration          externalaction.State                 `json:"orchestration"`
	SchemaVersion          uint32                               `json:"schema_version"`
	Tasks                  map[taskid.ID]task.Task              `json:"tasks"`
	Runs                   map[string]fsm.Run                   `json:"runs"`
	Events                 []task.DomainEvent                   `json:"events"`
	Inbox                  map[string]InboxReceipt              `json:"inbox"`
	Outbox                 map[string]OutboxQueue               `json:"outbox"`
	AgentSessions          map[string]agentsession.Session      `json:"agent_sessions"`
	AgentSessionEvents     []agentsession.Event                 `json:"agent_session_events"`
}

func EmptyCanonicalState() CanonicalState {
	return CanonicalState{
		SchemaVersion:    CanonicalSchemaVersion,
		OwnerCredentials: map[string]ownercredential.Aggregate{},
		// CoordinatorDelegations stays nil until schema 6 materializes a grant;
		// omitempty + nil preserves DeepEqual parity with pre-#93b snapshots.
		Orchestration:      externalaction.Empty(),
		Tasks:              map[taskid.ID]task.Task{},
		Runs:               map[string]fsm.Run{},
		Events:             []task.DomainEvent{},
		Inbox:              map[string]InboxReceipt{},
		Outbox:             map[string]OutboxQueue{},
		AgentSessions:      map[string]agentsession.Session{},
		AgentSessionEvents: []agentsession.Event{},
	}
}

// NormalizeCanonicalState initializes collections added compatibly to older
// canonical schemas. Older schemas remain unchanged in memory until a commit.
func NormalizeCanonicalState(state CanonicalState) CanonicalState {
	legacy := state.SchemaVersion == 1
	if legacy {
		state.SchemaVersion = CanonicalSchemaVersion
	}
	if legacy && state.Orchestration.Actions == nil && state.Orchestration.Observations == nil && state.Orchestration.Receipts == nil && state.Orchestration.Events == nil {
		state.Orchestration = externalaction.Empty()
	}
	if state.AgentSessions == nil {
		state.AgentSessions = map[string]agentsession.Session{}
	}
	if state.AgentSessionEvents == nil {
		state.AgentSessionEvents = []agentsession.Event{}
	}
	if state.OwnerCredentials == nil {
		state.OwnerCredentials = map[string]ownercredential.Aggregate{}
	}
	// Leave CoordinatorDelegations nil when empty so omitempty JSON round-trips
	// match pre-#93b snapshots (nil vs empty-map DeepEqual traps).
	if state.CoordinatorDelegations != nil && len(state.CoordinatorDelegations) == 0 {
		state.CoordinatorDelegations = nil
	}
	return state
}

// CanonicalPersistence stores the entire canonical state in one durable unit.
// Ownership is mandatory; an in-memory test implementation can provide a
// no-op lock explicitly. Unsupported durable ownership fails closed.
type CanonicalPersistence interface {
	Lock() (func() error, error)
	Load() (CanonicalState, error)
	Save(CanonicalState) error
}

// RunStore is the coordinator-facing Run view shared by legacy and canonical
// persistence. CanonicalStore persists every Run mutation through the full
// CanonicalState boundary.
type RunStore interface {
	All() map[string]fsm.Run
	Get(string) (fsm.Run, bool)
	Update(func(map[string]fsm.Run) error) error
	Close() error
}

type CanonicalStore struct {
	mu              sync.RWMutex
	state           CanonicalState
	p               CanonicalPersistence
	release         func() error
	now             func() time.Time
	closed          bool
	failed          error
	ledgerFrozen    bool
	ownerChallenges map[string]ownercredential.Command
	ownerRPID       string
	ownerOrigin     string
}

func NewCanonical(p CanonicalPersistence) (*CanonicalStore, error) {
	return NewCanonicalWithClock(p, time.Now)
}

// NewCanonicalWithClock constructs a canonical store with an injected clock.
// Production callers should normally use NewCanonical; deterministic callers
// can provide a clock for timestamped canonical mutations.
func NewCanonicalWithClock(p CanonicalPersistence, now func() time.Time) (*CanonicalStore, error) {
	if p == nil {
		return nil, fmt.Errorf("canonical persistence required")
	}
	if now == nil {
		return nil, fmt.Errorf("canonical clock required")
	}
	release, err := p.Lock()
	if err != nil {
		return nil, err
	}
	if release == nil {
		return nil, fmt.Errorf("canonical persistence returned no ownership release")
	}
	if preparer, ok := p.(interface{ PrepareCanonicalOwned() error }); ok {
		if err := preparer.PrepareCanonicalOwned(); err != nil {
			_ = release()
			return nil, err
		}
	}
	state, err := p.Load()
	if err == nil {
		state = NormalizeCanonicalState(state)
		state, err = copyCanonicalState(state)
	}
	if err == nil {
		err = ValidateCanonicalState(state)
	}
	if err != nil {
		if release != nil {
			_ = release()
		}
		return nil, err
	}
	return &CanonicalStore{state: state, p: p, release: release, now: now, ownerChallenges: map[string]ownercredential.Command{}}, nil
}

func ValidateCanonicalState(state CanonicalState) error {
	state = NormalizeCanonicalState(state)
	if state.SchemaVersion != CanonicalSchemaVersion && state.SchemaVersion != 5 && state.SchemaVersion != 4 && state.SchemaVersion != 3 {
		return fmt.Errorf("unsupported canonical schema version %d", state.SchemaVersion)
	}
	if state.SchemaVersion == 3 && len(state.OwnerCredentials) != 0 {
		return fmt.Errorf("schema 3 cannot contain OwnerCredential aggregates")
	}
	if state.SchemaVersion < 6 && len(state.CoordinatorDelegations) != 0 {
		return fmt.Errorf("schema %d cannot contain CoordinatorDelegations", state.SchemaVersion)
	}
	// F3: fail-closed before assignment replay so disabled snapshots carrying
	// delegation records or OWNER_CREDENTIAL loss are rejected even when the
	// assignment history itself is incomplete/forged.
	if err := validateDelegationDisabledInvariants(state); err != nil {
		return err
	}
	if err := validateAssignments(state); err != nil {
		return err
	}
	if err := validateDelegations(state); err != nil {
		return err
	}
	if err := validateAssignmentDelegationScope(state); err != nil {
		return err
	}
	if err := validateSchemaMigrations(state); err != nil {
		return err
	}
	if err := externalaction.Validate(state.Orchestration, state.Revision); err != nil {
		return err
	}
	for _, a := range state.Orchestration.Actions {
		if a.Family == externalaction.GitHub {
			p, err := a.GitHubPayload()
			if err != nil {
				return err
			}
			if err := externalaction.ValidateMergeGate(p, nil); err != nil {
				return err
			}
			continue
		}
		r, ok := state.Runs[a.Binding.RunID]
		_, taskOK := state.Tasks[taskid.ID(a.Binding.TaskID)]
		_, sessionOK := state.AgentSessions[a.Binding.SessionID]
		if !ok || !taskOK || !sessionOK || r.TaskID.String() != a.Binding.TaskID {
			return fmt.Errorf("external action binding references missing canonical aggregate")
		}
	}

	if state.Tasks == nil || state.Runs == nil || state.Events == nil || state.Inbox == nil || state.Outbox == nil {
		return fmt.Errorf("canonical state collections must be present")
	}
	for id, value := range state.Tasks {
		if id != value.ID {
			return fmt.Errorf("canonical Task map key %q does not match ID %q", id, value.ID)
		}
		if err := value.Validate(); err != nil {
			return fmt.Errorf("invalid canonical Task %q: %w", id, err)
		}
	}
	for _, event := range state.Events {
		if err := event.Validate(); err != nil {
			return fmt.Errorf("invalid canonical DomainEvent: %w", err)
		}
	}
	activeByTask := make(map[taskid.ID]string)
	for id, run := range state.Runs {
		if id != run.RunID {
			return fmt.Errorf("invalid canonical run %q", id)
		}
		if err := run.Validate(); err != nil {
			return fmt.Errorf("invalid canonical run %q: %w", id, err)
		}
		if run.TaskID != "" {
			if err := validateTaskRunRole(run); err != nil {
				return fmt.Errorf("invalid canonical run %q role assignment: %w", id, err)
			}
		}
		if len(run.Outbox) != 0 || !run.SendRetryAt.IsZero() || run.SendRetries != 0 {
			return fmt.Errorf("canonical Run %q duplicates top-level Outbox state", id)
		}
		if run.TaskID != "" {
			t, ok := state.Tasks[run.TaskID]
			if err := run.TaskID.Validate(); err != nil || !ok {
				return fmt.Errorf("canonical Run %q references missing or invalid TaskID %q", id, run.TaskID)
			}
			if run.Fence > t.Fence {
				return fmt.Errorf("canonical Run %q fence exceeds Task fence", id)
			}
			if !run.Terminal() {
				if prior, exists := activeByTask[run.TaskID]; exists {
					return fmt.Errorf("task %q has multiple active runs %q and %q", run.TaskID, prior, id)
				}
				if run.Fence != t.Fence {
					return fmt.Errorf("active Run %q fence differs from Task fence", id)
				}
				activeByTask[run.TaskID] = id
			}
		}
	}
	if err := validateOutbox(state); err != nil {
		return err
	}
	if err := validateInbox(state); err != nil {
		return err
	}
	if err := validateTaskEventHistories(state); err != nil {
		return err
	}
	if err := validateCanonicalRoleIndependence(state); err != nil {
		return err
	}
	if err := validateCanonicalAgentSessionBindings(state); err != nil {
		return err
	}
	if err := validateTaskResultEvents(state); err != nil {
		return err
	}
	if err := validateCanonicalDiscussion(state); err != nil {
		return err
	}
	if err := agentsession.ValidateHistory(state.AgentSessions, state.AgentSessionEvents); err != nil {
		return err
	}
	if state.OwnerCredentials == nil {
		return fmt.Errorf("OwnerCredential collection must be present")
	}
	for principalID, aggregate := range state.OwnerCredentials {
		if principalID != aggregate.PrincipalID {
			return fmt.Errorf("OwnerCredential map key does not match principal ID")
		}
		if err := aggregate.Validate(); err != nil {
			return fmt.Errorf("invalid OwnerCredential aggregate %q: %w", principalID, err)
		}
	}
	return nil
}

func validateInbox(state CanonicalState) error {
	events := make(map[string]task.DomainEvent, len(state.Events))
	for _, event := range state.Events {
		events[event.ID] = event
	}
	for key, receipt := range state.Inbox {
		if receipt.ActorID == "" || strings.TrimSpace(receipt.ActorID) != receipt.ActorID || receipt.MessageID == "" || strings.TrimSpace(receipt.MessageID) != receipt.MessageID || key != InboxKey(receipt.ActorID, receipt.MessageID) {
			return fmt.Errorf("invalid Inbox receipt identity %q", key)
		}
		if err := receipt.TaskID.Validate(); err != nil {
			return fmt.Errorf("inbox receipt %q has invalid TaskID: %w", key, err)
		}
		taskValue, ok := state.Tasks[receipt.TaskID]
		if !ok {
			return fmt.Errorf("inbox receipt %q references missing Task %q", key, receipt.TaskID)
		}
		if receipt.RecordedAt.IsZero() {
			return fmt.Errorf("inbox receipt %q lacks commit time", key)
		}
		agentFields := receipt.AgentCommandDigest != "" || receipt.AssignmentID != ""
		if agentFields {
			if len(receipt.AgentCommandDigest) != 64 || receipt.AssignmentID == "" || strings.TrimSpace(receipt.AssignmentID) != receipt.AssignmentID || receipt.RunID == "" {
				return fmt.Errorf("agent command receipt %q has incomplete binding", key)
			}
			digest, err := hex.DecodeString(receipt.AgentCommandDigest)
			if err != nil || len(digest) != 32 {
				return fmt.Errorf("agent command receipt %q has invalid digest", key)
			}
			ledger := state.AssignmentLedger
			assignmentRecord, exists := assignment.Record{}, false
			if ledger != nil {
				assignmentRecord, exists = ledger.Records[receipt.AssignmentID]
			}
			if !exists || assignmentRecord.RunBinding == nil || assignmentRecord.RunBinding.TaskID != receipt.TaskID.String() || assignmentRecord.RunBinding.RunID != receipt.RunID {
				return fmt.Errorf("agent command receipt %q references missing or unrelated Assignment", key)
			}
		} else if receipt.AssignmentID != "" || len(receipt.EventIDs) == 0 {
			return fmt.Errorf("inbox receipt %q lacks DomainEvent references", key)
		}
		ownerFields := receipt.OwnerCommandDigest != "" || receipt.OwnerNonce != "" || receipt.OwnerCredentialRevision != 0
		if ownerFields {
			if len(receipt.OwnerCommandDigest) != 64 || receipt.OwnerNonce == "" || receipt.OwnerCredentialRevision == 0 {
				return fmt.Errorf("owner task receipt %q has incomplete authorization binding", key)
			}
			digest, err := hex.DecodeString(receipt.OwnerCommandDigest)
			if err != nil || len(digest) != 32 {
				return fmt.Errorf("owner task receipt %q has invalid command digest", key)
			}
			aggregate, exists := state.OwnerCredentials[receipt.ActorID]
			ownerReceipt, receiptExists := aggregate.Receipts[receipt.MessageID]
			if !exists || !receiptExists || ownerReceipt.CommandKind != ownercredential.CommandCreateTask || ownerReceipt.CredentialID == "" || ownerReceipt.CommandDigest != receipt.OwnerCommandDigest || ownerReceipt.Nonce != receipt.OwnerNonce || ownerReceipt.ExpectedCredentialRevision != receipt.OwnerCredentialRevision || ownerReceipt.EventSequence != 0 {
				return fmt.Errorf("owner task receipt %q is not bound to its credential aggregate", key)
			}
		}
		noAcceptance := agentFields && len(receipt.EventIDs) == 0
		if noAcceptance {
			if receipt.CommittedRevision != receipt.ExpectedRevision || taskValue.Revision < receipt.CommittedRevision {
				return fmt.Errorf("no-acceptance inbox receipt %q has invalid Task revision", key)
			}
		} else if receipt.ExpectedRevision == ^uint64(0) || receipt.CommittedRevision != receipt.ExpectedRevision+1 || taskValue.Revision < receipt.CommittedRevision {
			return fmt.Errorf("inbox receipt %q has invalid expected/committed Task revision", key)
		}
		if receipt.RunID != "" {
			run, ok := state.Runs[receipt.RunID]
			if !ok || run.TaskID != receipt.TaskID {
				return fmt.Errorf("inbox receipt %q references missing or unrelated Run %q", key, receipt.RunID)
			}
		}
		seen := make(map[string]bool, len(receipt.EventIDs))
		revisionEvent := false
		for _, eventID := range receipt.EventIDs {
			event, ok := events[eventID]
			if !ok || event.TaskID != receipt.TaskID || (receipt.RunID != "" && event.RunID != receipt.RunID) {
				return fmt.Errorf("inbox receipt %q references missing or unrelated DomainEvent %q", key, eventID)
			}
			if event.TaskRevision != receipt.CommittedRevision {
				return fmt.Errorf("inbox receipt %q references DomainEvent %q outside committed Task revision %d", key, eventID, receipt.CommittedRevision)
			}
			if seen[eventID] {
				return fmt.Errorf("inbox receipt %q repeats DomainEvent %q", key, eventID)
			}
			if event.TaskRevision == receipt.CommittedRevision {
				revisionEvent = true
			}
			seen[eventID] = true
		}
		if !noAcceptance && !revisionEvent {
			return fmt.Errorf("inbox receipt %q does not reference its committed Task revision", key)
		}
		if noAcceptance && len(receipt.EventIDs) != 0 {
			return fmt.Errorf("no-acceptance inbox receipt %q references DomainEvents", key)
		}
	}
	for principalID, aggregate := range state.OwnerCredentials {
		for messageID, ownerReceipt := range aggregate.Receipts {
			if ownerReceipt.CommandKind != ownercredential.CommandCreateTask {
				continue
			}
			taskID := taskid.ID(ownerReceipt.TaskID)
			if err := taskID.Validate(); err != nil {
				return fmt.Errorf("owner task receipt %q has invalid TaskID: %w", messageID, err)
			}
			inbox, exists := state.Inbox[InboxKey(principalID, messageID)]
			if !exists || inbox.TaskID != taskID || inbox.OwnerCommandDigest != ownerReceipt.CommandDigest || inbox.OwnerNonce != ownerReceipt.Nonce || inbox.OwnerCredentialRevision != ownerReceipt.ExpectedCredentialRevision {
				return fmt.Errorf("owner task receipt %q has no matching canonical Inbox receipt", messageID)
			}
		}
	}
	return nil
}

func validateOutbox(state CanonicalState) error {
	seen := make(map[string]string)
	events := make(map[string]task.DomainEvent, len(state.Events))
	for _, event := range state.Events {
		events[event.ID] = event
	}
	for runID, queue := range state.Outbox {
		run, ok := state.Runs[runID]
		if !ok {
			return fmt.Errorf("outbox references missing run %q", runID)
		}
		if queue.Retries < 0 {
			return fmt.Errorf("outbox for run %q has negative retry count", runID)
		}
		for _, item := range queue.Messages {
			e := item.Envelope
			if err := e.Validate(); err != nil {
				return fmt.Errorf("invalid Outbox envelope for Run %q: %w", runID, err)
			}
			if e.Run != runID {
				return fmt.Errorf("outbox envelope run %q differs from queue run %q", e.Run, runID)
			}
			if prior, exists := seen[e.MessageID]; exists {
				return fmt.Errorf("duplicate Outbox message ID %q in Runs %q and %q", e.MessageID, prior, runID)
			}
			seen[e.MessageID] = runID
			if item.EventID != "" {
				event, exists := events[item.EventID]
				if !exists || event.RunID != runID || run.TaskID == "" || event.TaskID != run.TaskID {
					return fmt.Errorf("outbox message %q references missing or unrelated DomainEvent %q", e.MessageID, item.EventID)
				}
			} else if run.TaskID != "" {
				return fmt.Errorf("task-scoped outbox message %q has no DomainEvent reference", e.MessageID)
			}
		}
	}
	return nil
}

func validateTaskEventHistories(state CanonicalState) error {
	byTask := make(map[taskid.ID][]task.DomainEvent, len(state.Tasks))
	seenEvents := make(map[string]bool, len(state.Events))
	eventRuns := make(map[string]bool)
	for _, event := range state.Events {
		if _, ok := state.Tasks[event.TaskID]; !ok {
			return fmt.Errorf("domain event %q references missing task %q", event.ID, event.TaskID)
		}
		if seenEvents[event.ID] {
			return fmt.Errorf("duplicate DomainEvent ID %q", event.ID)
		}
		seenEvents[event.ID] = true
		if event.RunID != "" {
			run, ok := state.Runs[event.RunID]
			if !ok || run.TaskID != event.TaskID {
				return fmt.Errorf("domain event %q references missing or unrelated run %q", event.ID, event.RunID)
			}
			eventRuns[event.RunID] = true
		}
		if event.Type == task.CoordinatorCompletionRecorded {
			run, ok := state.Runs[event.RunID]
			if !ok || run.TargetSHA != event.CoordinatorCompletion.Subject.CommitSHA {
				return fmt.Errorf("coordinator completion subject differs from canonical Run")
			}
			subject, found := findCanonicalSubject(&state, event.TaskID, run.TargetSHA)
			if !found || !reflect.DeepEqual(subject, event.CoordinatorCompletion.Subject) {
				return fmt.Errorf("coordinator completion lacks exact canonical subject provenance")
			}
		}
		if event.Type == task.ExternalActionStateRecorded {
			action, exists := state.Orchestration.Actions[event.ExternalActionID]
			if !exists || action.Family != externalaction.GitHub {
				return fmt.Errorf("domain event %q references missing or non-GitHub action", event.ID)
			}
			origin, bound, err := action.CanonicalOrigin()
			if err != nil || !bound || origin.TaskID != event.TaskID.String() || origin.RunID != event.RunID {
				return fmt.Errorf("domain event %q has unbound GitHub action origin", event.ID)
			}
			if event.ExternalObservationID != "" {
				observation, ok := state.Orchestration.Observations[event.ExternalObservationID]
				if !ok || observation.ActionID != action.ID || observation.Stale {
					return fmt.Errorf("domain event %q references missing or stale observation", event.ID)
				}
			}
		}
		byTask[event.TaskID] = append(byTask[event.TaskID], event)
	}
	for id, run := range state.Runs {
		if run.TaskID != "" && !eventRuns[id] {
			return fmt.Errorf("task-scoped run %q has no domain event history", id)
		}
	}
	for id, value := range state.Tasks {
		events := byTask[id]
		if len(events) == 0 || uint64(len(events)) != value.LastEventSeq {
			return fmt.Errorf("task %q event history does not reach last event sequence", id)
		}
		// Events are appended in canonical order; require continuous sequence
		// numbers and a creation event before any subsequent mutation.
		if events[0].EventSeq != 1 || events[0].Type != task.TaskCreated || events[0].TaskRevision != 1 || events[0].TaskFence != 0 {
			return fmt.Errorf("task %q lacks its canonical creation event", id)
		}
		lastRevision := uint64(1)
		lastFence := uint64(0)
		latestTransferFence := make(map[string]uint64)
		for i, event := range events {
			if event.Type == task.TaskCreated {
				if i != 0 {
					return fmt.Errorf("task %q has TASK_CREATED after its first event", id)
				}
			}
			if event.EventSeq != uint64(i+1) {
				return fmt.Errorf("task %q domain event sequence has a gap or duplicate", id)
			}
			if i > 0 {
				if event.TaskRevision < lastRevision {
					return fmt.Errorf("task %q event revision moved backwards", id)
				}
				if event.TaskRevision > lastRevision && event.TaskRevision-lastRevision > 1 {
					return fmt.Errorf("task %q event revision has a gap", id)
				}
				if event.TaskRevision > lastRevision {
					lastRevision = event.TaskRevision
				}
			}
		}
		// TaskFence is a commit-level snapshot. All events written at one
		// revision share it; exactly one transfer marker advances it once.
		for start := 0; start < len(events); {
			revision := events[start].TaskRevision
			end := start
			transfers := 0
			for end < len(events) && events[end].TaskRevision == revision {
				if events[end].Type == task.TaskOwnershipTransferred {
					transfers++
				}
				end++
			}
			if revision == 1 {
				if end-start != 1 || events[start].Type != task.TaskCreated {
					return fmt.Errorf("task %q revision 1 must contain exactly one TASK_CREATED event", id)
				}
			} else if transfers > 1 {
				return fmt.Errorf("task %q revision %d has multiple ownership transfer markers", id, revision)
			} else if transfers == 1 {
				var err error
				lastFence, err = incrementTaskFence(lastFence)
				if err != nil {
					return err
				}
			}
			for _, event := range events[start:end] {
				if event.TaskFence != lastFence {
					return fmt.Errorf("task %q event %q fence snapshot %d does not match revision fence %d", id, event.ID, event.TaskFence, lastFence)
				}
				if event.Type == task.TaskOwnershipTransferred {
					if event.RunID == "" {
						return fmt.Errorf("task %q ownership transfer event has no RunID", id)
					}
					latestTransferFence[event.RunID] = event.TaskFence
				}
			}
			start = end
		}
		if lastFence != value.Fence {
			return fmt.Errorf("task %q final event fence %d differs from Task fence %d", id, lastFence, value.Fence)
		}
		if lastRevision != value.Revision {
			return fmt.Errorf("task %q latest event revision differs from task revision", id)
		}
		for runID, run := range state.Runs {
			if run.TaskID != id {
				continue
			}
			fence, ok := latestTransferFence[runID]
			if !ok {
				return fmt.Errorf("task-scoped Run %q has no ownership transfer event", runID)
			}
			if run.Fence != fence {
				return fmt.Errorf("task-scoped Run %q fence %d differs from latest transfer fence %d", runID, run.Fence, fence)
			}
		}
	}
	if len(state.Tasks) == 0 && len(state.Events) != 0 {
		return fmt.Errorf("domain events exist without tasks")
	}
	return nil
}

func incrementTaskFence(fence uint64) (uint64, error) {
	if fence == math.MaxUint64 {
		return 0, ErrFenceExhausted
	}
	return fence + 1, nil
}

func copyCanonicalState(state CanonicalState) (CanonicalState, error) {
	state = NormalizeCanonicalState(state)
	b, err := json.Marshal(state)
	if err != nil {
		return CanonicalState{}, err
	}
	var out CanonicalState
	if err := json.Unmarshal(b, &out); err != nil {
		return CanonicalState{}, err
	}
	return out, nil
}

func (s *CanonicalStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if s.release != nil {
		return s.release()
	}
	return nil
}

func (s *CanonicalStore) All() map[string]fsm.Run {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out, _ := copyRuns(hydrateRuns(s.state))
	return out
}

func (s *CanonicalStore) Get(id string) (fsm.Run, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	run, ok := hydrateRuns(s.state)[id]
	if !ok {
		return fsm.Run{}, false
	}
	out, err := copyRuns(map[string]fsm.Run{id: run})
	if err != nil {
		return fsm.Run{}, false
	}
	return out[id], true
}

func (s *CanonicalStore) Update(fn func(map[string]fsm.Run) error) error {
	return s.UpdateCanonical(func(state *CanonicalState) error {
		runs := hydrateRuns(*state)
		if err := fn(runs); err != nil {
			return err
		}
		return dehydrateRuns(state, runs)
	})
}

func hydrateRuns(state CanonicalState) map[string]fsm.Run {
	runs, _ := copyRuns(state.Runs)
	for runID, queue := range state.Outbox {
		run, ok := runs[runID]
		if !ok {
			continue
		}
		run.Outbox = make([]envelope.Envelope, len(queue.Messages))
		for i, item := range queue.Messages {
			run.Outbox[i] = item.Envelope
		}
		run.SendRetryAt, run.SendRetries = queue.RetryAt, queue.Retries
		runs[runID] = run
	}
	return runs
}

func dehydrateRuns(state *CanonicalState, runs map[string]fsm.Run) error {
	if len(runs) < len(state.Runs) {
		return fmt.Errorf("canonical Runs cannot be deleted through RunStore")
	}
	for runID, run := range runs {
		if _, ok := state.Runs[runID]; !ok && run.RunID != runID {
			return fmt.Errorf("canonical Run key %q does not match RunID %q", runID, run.RunID)
		}
		prior := state.Outbox[runID]
		oldByMessage := make(map[string]OutboxMessage, len(prior.Messages))
		for _, item := range prior.Messages {
			oldByMessage[item.Envelope.MessageID] = item
		}
		queue := OutboxQueue{RetryAt: run.SendRetryAt, Retries: run.SendRetries}
		for _, e := range run.Outbox {
			item, exists := oldByMessage[e.MessageID]
			if exists && reflect.DeepEqual(item.Envelope, e) {
				item.Envelope = e
			} else {
				item = OutboxMessage{Envelope: e}
			}
			queue.Messages = append(queue.Messages, item)
		}
		if len(queue.Messages) == 0 && queue.RetryAt.IsZero() && queue.Retries == 0 {
			delete(state.Outbox, runID)
		} else {
			state.Outbox[runID] = queue
		}
		run.Outbox = nil
		run.SendRetryAt = time.Time{}
		run.SendRetries = 0
		state.Runs[runID] = run
	}
	return nil
}

// UpdateCanonical serializes a complete state transition and publishes it in
// memory only after the single durable snapshot commit succeeds.
// Callers cannot authorize a CoordinatorDelegations mutation through this
// entry: grant acting/history/visibility changes require the unexported
// handoff or primaryRestart commit seam.
func (s *CanonicalStore) UpdateCanonical(fn func(*CanonicalState) error) error {
	return s.commitCanonical(delegationCommitGeneric, fn)
}

func (s *CanonicalStore) commitCanonical(kind delegationCommitKind, fn func(*CanonicalState) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("store closed")
	}
	if s.failed != nil {
		return fmt.Errorf("store requires reopen after durability failure: %w", s.failed)
	}
	next, err := copyCanonicalState(s.state)
	if err != nil {
		return fmt.Errorf("copy canonical state: %w", err)
	}
	if err := fn(&next); err != nil {
		return err
	}
	if err := rejectGenericOwnerLossEvents(s.state.AssignmentLedger, next.AssignmentLedger); err != nil {
		return err
	}
	if !reflect.DeepEqual(s.state.OwnerCredentials, next.OwnerCredentials) {
		return ErrOwnerCommandRequired
	}
	if !reflect.DeepEqual(s.state.CoordinatorDelegations, next.CoordinatorDelegations) {
		if !coordinatorDelegationActive() {
			return ErrCoordinatorDelegationDisabled
		}
		if err := validateDelegatedWriterDelta(kind, s.state, next); err != nil {
			return err
		}
	}
	next.SchemaVersion = CanonicalSchemaVersion
	if reflect.DeepEqual(s.state, next) {
		return nil
	}
	if s.state.Revision == ^uint64(0) {
		return fmt.Errorf("canonical revision exhausted")
	}
	next.Revision = s.state.Revision + 1
	stampNewDelegationHistory(s.state.CoordinatorDelegations, next.CoordinatorDelegations, next.Revision)
	if err := ValidateCanonicalState(next); err != nil {
		return err
	}
	if err := ValidateCanonicalTransition(s.state, next); err != nil {
		return err
	}
	if err := s.validateAssignmentMode(next); err != nil {
		return err
	}
	committed, err := copyCanonicalState(next)
	if err != nil {
		return fmt.Errorf("copy updated canonical state: %w", err)
	}
	if err := s.p.Save(committed); err != nil {
		s.failed = fmt.Errorf("%w: %w", ErrPersistence, err)
		return s.failed
	}
	s.state = committed
	return nil
}

// BootstrapOwnerCredentials is an initialization-only operation. It is not
// mounted on the daemon listener; the caller must hold the canonical store
// lock during the explicit Owner setup ceremony before normal operation.
func (s *CanonicalStore) BootstrapOwnerCredentials(principalID, messageID string, credentials []ownercredential.Credential) (ownercredential.Receipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ownercredential.Receipt{}, fmt.Errorf("store closed")
	}
	if s.failed != nil {
		return ownercredential.Receipt{}, fmt.Errorf("store requires reopen after durability failure: %w", s.failed)
	}
	now := s.now().UTC()
	aggregate, err := ownercredential.Bootstrap(principalID, messageID, credentials, now)
	if err != nil {
		return ownercredential.Receipt{}, err
	}
	if len(s.state.OwnerCredentials) != 0 {
		if prior, exists := s.state.OwnerCredentials[principalID]; exists {
			if receipt, ok := prior.Receipts[messageID]; ok && receipt.CommandKind == "bootstrap" && receipt.CommandDigest == aggregate.Receipts[messageID].CommandDigest {
				return receipt, nil
			}
		}
		return ownercredential.Receipt{}, ErrOwnerBootstrapClosed
	}
	if len(s.state.Tasks) != 0 {
		return ownercredential.Receipt{}, ErrOwnerBootstrapClosed
	}
	next, err := copyCanonicalState(s.state)
	if err != nil {
		return ownercredential.Receipt{}, fmt.Errorf("copy canonical state: %w", err)
	}
	next.OwnerCredentials[principalID] = aggregate
	if err := rejectUnauthorizedGrantMutation(s.state, next); err != nil {
		return ownercredential.Receipt{}, err
	}
	if s.state.Revision == ^uint64(0) {
		return ownercredential.Receipt{}, fmt.Errorf("canonical revision exhausted")
	}
	next.SchemaVersion = CanonicalSchemaVersion
	next.Revision = s.state.Revision + 1
	if err := ValidateCanonicalState(next); err != nil {
		return ownercredential.Receipt{}, err
	}
	if err := ValidateCanonicalTransition(s.state, next); err != nil {
		return ownercredential.Receipt{}, err
	}
	if err := s.validateAssignmentMode(next); err != nil {
		return ownercredential.Receipt{}, err
	}
	committed, err := copyCanonicalState(next)
	if err != nil {
		return ownercredential.Receipt{}, fmt.Errorf("copy updated canonical state: %w", err)
	}
	if err := s.p.Save(committed); err != nil {
		s.failed = fmt.Errorf("%w: %w", ErrPersistence, err)
		return ownercredential.Receipt{}, s.failed
	}
	s.state = committed
	return aggregate.Receipts[messageID], nil
}

// ApplyInboxCommand is a legacy generic Task command seam. Owner credential
// enrollment does not disable it: the method stays callable for Task commands
// after enroll. It is not a grant writer. OwnerCredentials mutation fails
// closed with ErrOwnerCommandRequired; CoordinatorDelegations mutation fails
// closed. Authenticated agent writes use ApplyAgentCommand; Owner
// grant/revoke/loss/visibility use ApplyOwnerCredentialCommand so verification
// and effect share one lock.
func (s *CanonicalStore) ApplyInboxCommand(actorID, messageID string, taskID taskid.ID, expectedRevision uint64, apply func(*CanonicalState) (InboxReceipt, error)) (InboxReceipt, bool, error) {
	if actorID == "" || strings.TrimSpace(actorID) != actorID || messageID == "" || strings.TrimSpace(messageID) != messageID || taskID.Validate() != nil || apply == nil {
		return InboxReceipt{}, false, fmt.Errorf("valid command identity and handler are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return InboxReceipt{}, false, fmt.Errorf("store closed")
	}
	if s.failed != nil {
		return InboxReceipt{}, false, fmt.Errorf("store requires reopen after durability failure: %w", s.failed)
	}
	return s.applyInboxCommandLocked(actorID, messageID, taskID, expectedRevision, apply)
}

func (s *CanonicalStore) applyInboxCommandLocked(actorID, messageID string, taskID taskid.ID, expectedRevision uint64, apply func(*CanonicalState) (InboxReceipt, error)) (InboxReceipt, bool, error) {
	key := InboxKey(actorID, messageID)
	if receipt, exists := s.state.Inbox[key]; exists {
		if receipt.TaskID != taskID || receipt.ExpectedRevision != expectedRevision {
			return InboxReceipt{}, false, fmt.Errorf("command identity was replayed with a different Task or expected revision")
		}
		return receipt, true, nil
	}
	actualRevision := uint64(0)
	if current, exists := s.state.Tasks[taskID]; exists {
		actualRevision = current.Revision
	}
	if expectedRevision != actualRevision {
		return InboxReceipt{}, false, &RevisionConflictError{TaskID: taskID, Expected: expectedRevision, Actual: actualRevision}
	}
	next, err := copyCanonicalState(s.state)
	if err != nil {
		return InboxReceipt{}, false, fmt.Errorf("copy canonical state: %w", err)
	}
	next.SchemaVersion = CanonicalSchemaVersion
	receipt, err := apply(&next)
	if err != nil {
		return InboxReceipt{}, false, err
	}
	if err := rejectGenericOwnerLossEvents(s.state.AssignmentLedger, next.AssignmentLedger); err != nil {
		return InboxReceipt{}, false, err
	}
	if !reflect.DeepEqual(s.state.OwnerCredentials, next.OwnerCredentials) {
		return InboxReceipt{}, false, ErrOwnerCommandRequired
	}
	if err := rejectUnauthorizedGrantMutation(s.state, next); err != nil {
		return InboxReceipt{}, false, err
	}
	if !reflect.DeepEqual(next.Inbox, s.state.Inbox) {
		return InboxReceipt{}, false, fmt.Errorf("command handler cannot mutate Inbox directly")
	}
	if receipt.ActorID != actorID || receipt.MessageID != messageID || receipt.TaskID != taskID || receipt.ExpectedRevision != expectedRevision {
		return InboxReceipt{}, false, fmt.Errorf("command receipt identity differs from command")
	}
	if actualRevision == ^uint64(0) || receipt.CommittedRevision != actualRevision+1 {
		return InboxReceipt{}, false, fmt.Errorf("command receipt must commit exactly one Task revision")
	}
	updated, exists := next.Tasks[receipt.TaskID]
	if !exists || updated.Revision != receipt.CommittedRevision {
		return InboxReceipt{}, false, fmt.Errorf("command receipt revision does not match updated Task")
	}
	next.Inbox[key] = receipt
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

// ValidateCanonicalTransition enforces append-only history and monotonic Task
// versions across commits. Snapshot validation alone cannot prove these rules.
func ValidateCanonicalTransition(previous, next CanonicalState) error {
	if previous.SchemaVersion != next.SchemaVersion {
		okPrior := previous.SchemaVersion == 3 || previous.SchemaVersion == 4 || previous.SchemaVersion == 5
		if !okPrior || next.SchemaVersion != CanonicalSchemaVersion {
			return fmt.Errorf("canonical schema may only advance from 3, 4 or 5 to %d", CanonicalSchemaVersion)
		}
	}
	if err := assignment.ValidateTransition(previous.AssignmentLedger, next.AssignmentLedger); err != nil {
		return err
	}
	if !reflect.DeepEqual(previous.SchemaMigrations, next.SchemaMigrations) {
		return fmt.Errorf("schema migration history is immutable during runtime")
	}
	previous = NormalizeCanonicalState(previous)
	next = NormalizeCanonicalState(next)
	if err := ownercredential.ValidateTransition(previous.OwnerCredentials, next.OwnerCredentials); err != nil {
		return err
	}
	// F2: while delegation is disabled, CoordinatorDelegations must be byte-stable
	// across transitions (no generic insert/edit/delete of the grant slice).
	if !coordinatorDelegationActive() && !reflect.DeepEqual(previous.CoordinatorDelegations, next.CoordinatorDelegations) {
		return fmt.Errorf("%w: CoordinatorDelegations immutable while delegation disabled", ErrCoordinatorDelegationDisabled)
	}
	if err := externalaction.ValidateTransition(previous.Orchestration, next.Orchestration, next.Revision); err != nil {
		return err
	}
	if err := agentsession.ValidateTransition(previous.AgentSessions, next.AgentSessions, previous.AgentSessionEvents, next.AgentSessionEvents); err != nil {
		return err
	}
	if len(next.Inbox) < len(previous.Inbox) {
		return fmt.Errorf("canonical Inbox receipts cannot be deleted")
	}
	for key, receipt := range previous.Inbox {
		if nextReceipt, exists := next.Inbox[key]; !exists || !reflect.DeepEqual(receipt, nextReceipt) {
			return fmt.Errorf("canonical Inbox receipt %q is immutable", key)
		}
	}
	if len(next.Events) < len(previous.Events) {
		return fmt.Errorf("canonical DomainEvent history cannot shrink")
	}
	for i, event := range previous.Events {
		if !reflect.DeepEqual(event, next.Events[i]) {
			return fmt.Errorf("canonical DomainEvent history is append-only")
		}
	}
	newEvents := make(map[taskid.ID][]task.DomainEvent)
	newEventIDs := make(map[string]task.DomainEvent)
	for _, event := range next.Events[len(previous.Events):] {
		newEvents[event.TaskID] = append(newEvents[event.TaskID], event)
		newEventIDs[event.ID] = event
	}
	if err := validateDiscussionOnlyMutations(previous, next, newEvents); err != nil {
		return err
	}
	for key, receipt := range next.Inbox {
		if _, existed := previous.Inbox[key]; existed {
			continue
		}
		if len(receipt.EventIDs) == 0 {
			if receipt.AgentCommandDigest == "" || receipt.AssignmentID == "" || receipt.CommittedRevision != receipt.ExpectedRevision {
				return fmt.Errorf("new Inbox receipt %q requires same-commit DomainEvent references", key)
			}
			before := previous
			before.Inbox = next.Inbox
			before.Revision = next.Revision
			before.SchemaVersion = next.SchemaVersion
			before.SchemaMigrations = next.SchemaMigrations
			if !reflect.DeepEqual(before, next) {
				return fmt.Errorf("receipt-only agent command %q changed canonical state outside its receipt", key)
			}
			oldTask, exists := previous.Tasks[receipt.TaskID]
			if !exists || receipt.ExpectedRevision != oldTask.Revision || receipt.CommittedRevision != oldTask.Revision {
				return fmt.Errorf("receipt-only agent command %q changed Task revision", key)
			}
			continue
		}
		for _, eventID := range receipt.EventIDs {
			if _, committed := newEventIDs[eventID]; !committed {
				return fmt.Errorf("new Inbox receipt %q references an event outside its commit", key)
			}
		}
		oldTask, existed := previous.Tasks[receipt.TaskID]
		newTask, exists := next.Tasks[receipt.TaskID]
		actualRevision := uint64(0)
		if existed {
			actualRevision = oldTask.Revision
		}
		if !exists || receipt.ExpectedRevision != actualRevision || actualRevision == ^uint64(0) || receipt.CommittedRevision != actualRevision+1 || newTask.Revision != receipt.CommittedRevision {
			return fmt.Errorf("new Inbox receipt %q does not match the Task revision committed with it", key)
		}
	}
	previousOutboxMessages := make(map[string]OutboxMessage)
	previousOrder := make(map[string][]string)
	for _, queue := range previous.Outbox {
		for _, message := range queue.Messages {
			id := message.Envelope.MessageID
			previousOutboxMessages[id] = message
			previousOrder[message.Envelope.Run] = append(previousOrder[message.Envelope.Run], id)
		}
	}
	for runID, queue := range next.Outbox {
		run := next.Runs[runID]
		retainedOrder := make([]string, 0, len(queue.Messages))
		for _, message := range queue.Messages {
			id := message.Envelope.MessageID
			if prior, exists := previousOutboxMessages[id]; exists {
				if !reflect.DeepEqual(prior, message) {
					return fmt.Errorf("outbox message %q identity and event reference are immutable", id)
				}
				retainedOrder = append(retainedOrder, id)
				continue
			}
			if run.TaskID != "" {
				if _, committed := newEventIDs[message.EventID]; !committed {
					return fmt.Errorf("task-scoped outbox message %q requires a same-commit DomainEvent", id)
				}
			}
		}
		prior := previousOrder[runID]
		cursor := 0
		for _, id := range retainedOrder {
			for cursor < len(prior) && prior[cursor] != id {
				cursor++
			}
			if cursor == len(prior) {
				return fmt.Errorf("outbox message order for run %q changed", runID)
			}
			cursor++
		}
	}
	for id, oldTask := range previous.Tasks {
		newTask, exists := next.Tasks[id]
		if !exists {
			return fmt.Errorf("canonical Task %q cannot be deleted", id)
		}
		changed := !reflect.DeepEqual(oldTask, newTask)
		added := newEvents[id]
		if !changed && len(added) > 0 {
			return fmt.Errorf("task %q event append must update its last event sequence and revision", id)
		}
		if changed {
			if newTask.Revision != oldTask.Revision+1 {
				return fmt.Errorf("task %q revision must increase by one per mutation", id)
			}
			if newTask.Fence < oldTask.Fence {
				return fmt.Errorf("task %q fence cannot decrease", id)
			}
			if len(added) == 0 {
				return fmt.Errorf("task %q mutation requires a same-commit event at its new revision", id)
			}
			for _, event := range added {
				if event.TaskRevision != newTask.Revision {
					return fmt.Errorf("task %q same-commit events must all reference new revision %d", id, newTask.Revision)
				}
			}
		}
	}
	for id, newTask := range next.Tasks {
		if _, exists := previous.Tasks[id]; !exists {
			added := newEvents[id]
			if newTask.Revision != 1 || newTask.Fence != 0 || len(added) != 1 || added[0].Type != task.TaskCreated || added[0].TaskRevision != 1 {
				return fmt.Errorf("new Task %q requires revision 1, fence 0 and one creation event in the same commit", id)
			}
			ownerReceiptFound := false
			for key, receipt := range next.Inbox {
				if _, wasPresent := previous.Inbox[key]; wasPresent || receipt.TaskID != id {
					continue
				}
				if receipt.ActorID != newTask.OwnerID || receipt.OwnerCommandDigest == "" || receipt.OwnerNonce == "" || receipt.OwnerCredentialRevision == 0 {
					return fmt.Errorf("new Task %q requires an authenticated Owner create receipt", id)
				}
				ownerReceiptFound = true
			}
			if !ownerReceiptFound {
				return fmt.Errorf("new Task %q requires a same-commit authenticated Owner receipt", id)
			}
		}
	}
	if err := validateTaskOwnershipTransfers(previous, next, newEvents); err != nil {
		return err
	}
	for id, oldRun := range previous.Runs {
		newRun, exists := next.Runs[id]
		if !exists {
			return fmt.Errorf("canonical Run %q cannot be deleted", id)
		}
		if oldRun.TaskID != newRun.TaskID {
			return fmt.Errorf("run %q task ID is immutable", id)
		}
		if oldRun.TaskID != "" {
			if oldRun.RequiredRole.Role != newRun.RequiredRole.Role {
				return fmt.Errorf("task-scoped Run %q role is immutable", id)
			}
			if oldRun.RoleSelection != nil && (newRun.RoleSelection == nil || oldRun.RoleSelection.Profile.Role != newRun.RoleSelection.Profile.Role) {
				return fmt.Errorf("task-scoped Run %q selected role is immutable", id)
			}
			if !roleHistoryHasPrefix(oldRun.RoleHistory, newRun.RoleHistory) {
				return fmt.Errorf("task-scoped Run %q RoleHistory is append-only", id)
			}
			if !reflect.DeepEqual(oldRun.RoleSelection, newRun.RoleSelection) {
				if newRun.RoleSelection == nil || len(newRun.RoleHistory) != len(oldRun.RoleHistory)+1 ||
					!reflect.DeepEqual(newRun.RoleHistory[len(newRun.RoleHistory)-1], *newRun.RoleSelection) {
					return fmt.Errorf("task-scoped Run %q RoleSelection change requires the same selection appended to RoleHistory", id)
				}
			}
		}
		if oldRun.TaskID != "" && !oldRun.SameDomainState(newRun) {
			if oldRun.Attempt != newRun.Attempt {
				return fmt.Errorf("run %q Attempt is immutable", id)
			}
			if err := requireTaskRunEvent(previous, next, newEvents, id, newRun.TaskID); err != nil {
				return err
			}
		}
	}
	for id, newRun := range next.Runs {
		if _, existed := previous.Runs[id]; existed || newRun.TaskID == "" {
			continue
		}
		if err := requireTaskRunEvent(previous, next, newEvents, id, newRun.TaskID); err != nil {
			return err
		}
	}
	if err := validateCanonicalRoleIndependence(next); err != nil {
		return err
	}
	if err := validateCanonicalAgentSessionAssignments(previous, next); err != nil {
		return err
	}
	if err := validateCanonicalDiscussion(next); err != nil {
		return err
	}
	return nil
}

func validateDiscussionOnlyMutations(previous, next CanonicalState, events map[taskid.ID][]task.DomainEvent) error {
	hasAnyDiscussion := false
	for id, added := range events {
		taskHasDiscussion := false
		for _, event := range added {
			if event.Type == task.DiscussionNodeRecorded || event.Type == task.DiscussionEdgeRecorded {
				taskHasDiscussion = true
			}
		}
		if !taskHasDiscussion {
			continue
		}
		hasAnyDiscussion = true
		for _, event := range added {
			if event.Type != task.DiscussionNodeRecorded && event.Type != task.DiscussionEdgeRecorded {
				return fmt.Errorf("discussion events cannot share a Task revision with workflow events")
			}
		}
		oldTask, hadOld := previous.Tasks[id]
		newTask, hasNew := next.Tasks[id]
		if !hadOld || !hasNew {
			return fmt.Errorf("discussion event requires an existing Task")
		}
		oldTask.Revision, newTask.Revision = 0, 0
		oldTask.LastEventSeq, newTask.LastEventSeq = 0, 0
		oldTask.UpdatedAt, newTask.UpdatedAt = time.Time{}, time.Time{}
		if !reflect.DeepEqual(oldTask, newTask) {
			return fmt.Errorf("discussion event cannot change Task workflow or other Task fields")
		}
	}
	if hasAnyDiscussion && !reflect.DeepEqual(previous.Runs, next.Runs) {
		return fmt.Errorf("discussion event cannot change Run state")
	}
	return nil
}

// validateTaskOwnershipTransfers checks the durable shape of Task writer
// transfers. It does not authorize a transfer or prove that a process stopped;
// those checks belong to the daemon command boundary and supervisor.
func validateTaskOwnershipTransfers(previous, next CanonicalState, events map[taskid.ID][]task.DomainEvent) error {
	activeCounts := make(map[taskid.ID]int)
	for _, run := range next.Runs {
		if run.TaskID != "" && !run.Terminal() {
			activeCounts[run.TaskID]++
			if activeCounts[run.TaskID] > 1 {
				return fmt.Errorf("task %q has multiple active writer runs", run.TaskID)
			}
		}
	}
	previousActive := activeTaskWriters(previous)
	nextActive := activeTaskWriters(next)
	for runID, oldRun := range previous.Runs {
		if oldRun.TaskID == "" {
			continue
		}
		newRun, exists := next.Runs[runID]
		if exists && oldRun.Fence != newRun.Fence {
			recoveryTransfer := oldRun.OwnerSession != newRun.OwnerSession && !oldRun.Terminal() && !newRun.Terminal()
			if !recoveryTransfer {
				return fmt.Errorf("task-scoped Run %q fence can change only during recovery ownership transfer", runID)
			}
		}
		if exists && oldRun.OwnerSession != newRun.OwnerSession && (oldRun.Terminal() || newRun.Terminal()) {
			return fmt.Errorf("task %q terminal Run %q cannot change owner", oldRun.TaskID, runID)
		}
	}
	for taskID, oldTask := range previous.Tasks {
		newTask := next.Tasks[taskID]
		oldWriter, hadOldWriter := previousActive[taskID]
		newWriter, hasNewWriter := nextActive[taskID]
		transfer := false
		transferredRun := fsm.Run{}
		recovery := false

		if hasNewWriter {
			prior, existed := previous.Runs[newWriter.RunID]
			switch {
			case !existed:
				transfer = true
				transferredRun = newWriter
			case prior.OwnerSession != newWriter.OwnerSession:
				if prior.Terminal() {
					return fmt.Errorf("task %q terminal Run %q cannot become an active writer again", taskID, newWriter.RunID)
				}
				transfer = true
				recovery = true
				transferredRun = newWriter
			case prior.Terminal():
				return fmt.Errorf("task %q terminal Run %q cannot become an active writer again", taskID, newWriter.RunID)
			case hadOldWriter && oldWriter.RunID != newWriter.RunID:
				transfer = true
				transferredRun = newWriter
			}
		}

		if transfer {
			if hadOldWriter && oldWriter.RunID != newWriter.RunID {
				return fmt.Errorf("task %q ownership transfer requires previous writer %q to be terminal before this commit", taskID, oldWriter.RunID)
			}
			if oldTask.Fence == math.MaxUint64 {
				return ErrFenceExhausted
			}
			if newTask.Fence != oldTask.Fence+1 {
				if newTask.Fence < oldTask.Fence {
					return fmt.Errorf("task %q fence cannot decrease", taskID)
				}
				return fmt.Errorf("task %q ownership transfer must increment Task.Fence by exactly one", taskID)
			}
			if transferredRun.Fence != newTask.Fence {
				return fmt.Errorf("run %q fence does not match Task %q fence", transferredRun.RunID, taskID)
			}
			if recovery {
				prior := previous.Runs[transferredRun.RunID]
				if prior.Status != fsm.Stalled || !prior.RecoveryNeeded || prior.RecoveryClaim == "" ||
					transferredRun.Attempt != prior.Attempt || transferredRun.RunID != prior.RunID ||
					transferredRun.RecoveryNeeded || transferredRun.RecoveryClaim != "" {
					return fmt.Errorf("task %q recovery transfer requires stalled Run with RecoveryNeeded and RecoveryClaim, preserved RunID/Attempt, and cleared claim", taskID)
				}
			}
			if err := requireOwnershipTransferEvent(newTask, events[taskID], transferredRun.RunID); err != nil {
				return err
			}
			if !recovery {
				if err := validateTransferredAttempt(previous, taskID, transferredRun); err != nil {
					return err
				}
			}
		} else if newTask.Fence != oldTask.Fence {
			return fmt.Errorf("task %q Task.Fence changed without ownership transfer", taskID)
		}

		for _, event := range events[taskID] {
			if event.Type == task.TaskOwnershipTransferred && !transfer {
				return fmt.Errorf("task %q has TASK_OWNERSHIP_TRANSFERRED event without ownership transfer", taskID)
			}
		}
	}
	return nil
}

func activeTaskWriters(state CanonicalState) map[taskid.ID]fsm.Run {
	writers := make(map[taskid.ID]fsm.Run)
	for _, run := range state.Runs {
		if run.TaskID != "" && !run.Terminal() {
			writers[run.TaskID] = run
		}
	}
	return writers
}

func requireOwnershipTransferEvent(taskValue task.Task, events []task.DomainEvent, runID string) error {
	count := 0
	for _, event := range events {
		if event.Type != task.TaskOwnershipTransferred {
			continue
		}
		if event.RunID != runID {
			return fmt.Errorf("task %q TASK_OWNERSHIP_TRANSFERRED event references run %q; want transferred run %q", taskValue.ID, event.RunID, runID)
		}
		count++
	}
	if count != 1 {
		return fmt.Errorf("task %q ownership transfer requires exactly one same-commit TASK_OWNERSHIP_TRANSFERRED event for run %q", taskValue.ID, runID)
	}
	return nil
}

func validateTaskRunRole(run fsm.Run) error {
	if run.RoleSelection == nil {
		return nil
	}
	if run.RequiredRole.Role == "" || run.RoleSelection.Profile.Role != run.RequiredRole.Role {
		return fmt.Errorf("selected profile role must match required role")
	}
	if run.RoleSelection.Session != run.OwnerSession {
		return fmt.Errorf("selected role session must match Run owner session")
	}
	if len(run.RoleHistory) == 0 || !reflect.DeepEqual(run.RoleHistory[len(run.RoleHistory)-1], *run.RoleSelection) {
		return fmt.Errorf("selected role must match the latest RoleHistory entry")
	}
	return nil
}

func roleHistoryHasPrefix(previous, next []roles.Selection) bool {
	if len(next) < len(previous) {
		return false
	}
	for i, prior := range previous {
		if prior.AgentID != next[i].AgentID || prior.Profile.Role != next[i].Profile.Role {
			return false
		}
	}
	return true
}

func validateTransferredAttempt(previous CanonicalState, taskID taskid.ID, run fsm.Run) error {
	role := runRole(run)
	if role == "" {
		return nil // Legacy or not-yet-role-bound Runs are checked by their command boundary.
	}
	if role == roles.Executor {
		maxAttempt := uint64(0)
		for _, prior := range previous.Runs {
			if prior.TaskID == taskID && runRole(prior) == roles.Executor && prior.Attempt > maxAttempt {
				maxAttempt = prior.Attempt
			}
		}
		if maxAttempt == math.MaxUint64 || run.Attempt != maxAttempt+1 {
			return fmt.Errorf("task %q new Executor Run must use the next Attempt", taskID)
		}
		return nil
	}
	if role == roles.Reviewer || role == roles.Auditor {
		maxExecutorAttempt := uint64(0)
		for _, prior := range previous.Runs {
			if prior.TaskID == taskID && runRole(prior) == roles.Executor && prior.Attempt > maxExecutorAttempt {
				maxExecutorAttempt = prior.Attempt
			}
		}
		if maxExecutorAttempt == 0 || run.Attempt != maxExecutorAttempt {
			return fmt.Errorf("task %q %s Run must preserve the current Executor Attempt", taskID, role)
		}
	}
	return nil
}

func runRole(run fsm.Run) roles.Role {
	if run.RoleSelection != nil {
		return run.RoleSelection.Profile.Role
	}
	return run.RequiredRole.Role
}

func requireTaskRunEvent(previous, next CanonicalState, events map[taskid.ID][]task.DomainEvent, runID string, taskID taskid.ID) error {
	oldTask, existed := previous.Tasks[taskID]
	newTask, exists := next.Tasks[taskID]
	if !existed || !exists || newTask.Revision != oldTask.Revision+1 {
		return fmt.Errorf("run %q domain mutation requires a Task revision increment in the same commit", runID)
	}
	for _, event := range events[taskID] {
		if event.RunID == runID && event.TaskRevision == newTask.Revision {
			return nil
		}
	}
	return fmt.Errorf("run %q domain mutation requires a same-commit event at Task revision %d", runID, newTask.Revision)
}

// TaskRevision returns the current revision of the Task identified by id.
// It is the caller's responsibility to pass this revision as expectedRevision to
// any subsequent ApplyInboxCommand call; the CAS check inside that call guards
// against concurrent mutations between this read and the write.
func (s *CanonicalStore) TaskRevision(id taskid.ID) (uint64, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.state.Tasks[id]
	return t.Revision, ok
}

// CanonicalSnapshot returns an isolated copy for startup diagnostics and
// validation; callers cannot mutate the published state.
func (s *CanonicalStore) CanonicalSnapshot() (CanonicalState, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return copyCanonicalState(s.state)
}

var _ RunStore = (*Store)(nil)
var _ RunStore = (*CanonicalStore)(nil)
