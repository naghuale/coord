package store

import (
	"coord/envelope"
	"coord/fsm"
	"coord/task"
	"coord/taskid"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"
)

const CanonicalSchemaVersion uint32 = 1

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
	ActorID           string    `json:"actor_id"`
	MessageID         string    `json:"message_id"`
	TaskID            taskid.ID `json:"task_id"`
	RunID             string    `json:"run_id,omitempty"`
	EventIDs          []string  `json:"event_ids"`
	ExpectedRevision  uint64    `json:"expected_revision"`
	CommittedRevision uint64    `json:"committed_revision"`
	RecordedAt        time.Time `json:"recorded_at"`
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
	SchemaVersion uint32                  `json:"schema_version"`
	Tasks         map[taskid.ID]task.Task `json:"tasks"`
	Runs          map[string]fsm.Run      `json:"runs"`
	Events        []task.DomainEvent      `json:"events"`
	Inbox         map[string]InboxReceipt `json:"inbox"`
	Outbox        map[string]OutboxQueue  `json:"outbox"`
}

func EmptyCanonicalState() CanonicalState {
	return CanonicalState{
		SchemaVersion: CanonicalSchemaVersion,
		Tasks:         map[taskid.ID]task.Task{},
		Runs:          map[string]fsm.Run{},
		Events:        []task.DomainEvent{},
		Inbox:         map[string]InboxReceipt{},
		Outbox:        map[string]OutboxQueue{},
	}
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
	mu      sync.RWMutex
	state   CanonicalState
	p       CanonicalPersistence
	release func() error
	closed  bool
	failed  error
}

func NewCanonical(p CanonicalPersistence) (*CanonicalStore, error) {
	if p == nil {
		return nil, fmt.Errorf("canonical persistence required")
	}
	release, err := p.Lock()
	if err != nil {
		return nil, err
	}
	if release == nil {
		return nil, fmt.Errorf("canonical persistence returned no ownership release")
	}
	state, err := p.Load()
	if err == nil {
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
	return &CanonicalStore{state: state, p: p, release: release}, nil
}

func ValidateCanonicalState(state CanonicalState) error {
	if state.SchemaVersion != CanonicalSchemaVersion {
		return fmt.Errorf("unsupported canonical schema version %d", state.SchemaVersion)
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
		if receipt.RecordedAt.IsZero() || len(receipt.EventIDs) == 0 {
			return fmt.Errorf("inbox receipt %q lacks commit time or DomainEvent references", key)
		}
		if receipt.ExpectedRevision == ^uint64(0) || receipt.CommittedRevision != receipt.ExpectedRevision+1 || taskValue.Revision < receipt.CommittedRevision {
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
			if seen[eventID] {
				return fmt.Errorf("inbox receipt %q repeats DomainEvent %q", key, eventID)
			}
			if event.TaskRevision == receipt.CommittedRevision {
				revisionEvent = true
			}
			seen[eventID] = true
		}
		if !revisionEvent {
			return fmt.Errorf("inbox receipt %q does not reference its committed Task revision", key)
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
		if events[0].EventSeq != 1 || events[0].Type != task.TaskCreated || events[0].TaskRevision != 1 {
			return fmt.Errorf("task %q lacks its canonical creation event", id)
		}
		lastRevision := uint64(1)
		for i, event := range events {
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
		if lastRevision != value.Revision {
			return fmt.Errorf("task %q latest event revision differs from task revision", id)
		}
	}
	if len(state.Tasks) == 0 && len(state.Events) != 0 {
		return fmt.Errorf("domain events exist without tasks")
	}
	return nil
}

func copyCanonicalState(state CanonicalState) (CanonicalState, error) {
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
func (s *CanonicalStore) UpdateCanonical(fn func(*CanonicalState) error) error {
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
	if err := ValidateCanonicalState(next); err != nil {
		return err
	}
	if err := ValidateCanonicalTransition(s.state, next); err != nil {
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

// ApplyInboxCommand applies one already-authenticated command and commits its
// receipt with the resulting state and DomainEvents. Authentication and
// capability checks belong to the daemon command layer. taskID and
// expectedRevision come from that boundary; revision is checked before apply.
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
	receipt, err := apply(&next)
	if err != nil {
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
	if err := ValidateCanonicalState(next); err != nil {
		return InboxReceipt{}, false, err
	}
	if err := ValidateCanonicalTransition(s.state, next); err != nil {
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
	for key, receipt := range next.Inbox {
		if _, existed := previous.Inbox[key]; existed {
			continue
		}
		if len(receipt.EventIDs) == 0 {
			return fmt.Errorf("new Inbox receipt %q requires same-commit DomainEvent references", key)
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
		}
	}
	for id, oldRun := range previous.Runs {
		newRun, exists := next.Runs[id]
		if !exists {
			return fmt.Errorf("canonical Run %q cannot be deleted", id)
		}
		if oldRun.TaskID != newRun.TaskID {
			return fmt.Errorf("run %q task ID is immutable", id)
		}
		if oldRun.TaskID != "" && !oldRun.SameDomainState(newRun) {
			if newRun.Fence < oldRun.Fence {
				return fmt.Errorf("run %q fence cannot decrease", id)
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
	return nil
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

// CanonicalSnapshot returns an isolated copy for startup diagnostics and
// validation; callers cannot mutate the published state.
func (s *CanonicalStore) CanonicalSnapshot() (CanonicalState, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return copyCanonicalState(s.state)
}

var _ RunStore = (*Store)(nil)
var _ RunStore = (*CanonicalStore)(nil)
