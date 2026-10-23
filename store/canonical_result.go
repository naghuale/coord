package store

import (
	"coord/assignment"
	"coord/envelope"
	"coord/fsm"
	"coord/roles"
	"coord/task"
	"coord/taskid"
	"fmt"
	"strings"
	"time"
)

// RecordExecutorResult commits a canonical Executor result, its Task transition,
// event, receipt and acknowledgement in one snapshot transaction. The actor is
// always selected from the persisted Run, never from result input. The RESULT
// envelope payload is translated to an ExecutorResult domain event; the legacy
// fsm.Run.Result field is left unset for canonical Runs (TaskID != "").
func (s *CanonicalStore) RecordExecutorResult(messageID string, id taskid.ID, runID string, expectedRevision uint64, result task.ExecutorResult, now time.Time, coordinator ...assignment.Principal) (InboxReceipt, bool, error) {
	run, ok := s.Get(runID)
	if !ok || run.TaskID != id || run.RoleSelection == nil || run.RoleSelection.AgentID == "" {
		return InboxReceipt{}, false, fmt.Errorf("canonical Run with Task-scoped RoleSelection required")
	}
	if run.RoleSelection.Profile.Role != roles.Executor {
		return InboxReceipt{}, false, fmt.Errorf("RecordExecutorResult requires an Executor Run")
	}
	actorID := run.RoleSelection.AgentID
	result = task.ExecutorResult{
		Outcome:            result.Outcome,
		Summary:            result.Summary,
		OutputArtifactRefs: cloneStrings(result.OutputArtifactRefs),
		CreatedAt:          result.CreatedAt,
	}
	return s.applyAssignmentResultCommand(actorID, messageID, id, runID, expectedRevision, now, coordinator, func(state *CanonicalState) (InboxReceipt, error) {
		currentRun, exists := state.Runs[runID]
		if !exists || currentRun.TaskID != id || currentRun.RoleSelection == nil || currentRun.RoleSelection.AgentID != actorID {
			return InboxReceipt{}, fmt.Errorf("canonical Run actor changed")
		}
		currentTask, exists := state.Tasks[id]
		if !exists {
			return InboxReceipt{}, fmt.Errorf("canonical Task %q not found", id)
		}
		if currentTask.Status != task.InProgress || currentTask.CurrentStage == nil || *currentTask.CurrentStage != task.Execution {
			return InboxReceipt{}, fmt.Errorf("task must be IN_PROGRESS at EXECUTION stage")
		}
		if currentRun.RoleSelection.Profile.Role != roles.Executor {
			return InboxReceipt{}, fmt.Errorf("ExecutorResult requires an Executor Run")
		}
		if currentRun.Terminal() || (currentRun.Status != fsm.Started && currentRun.Status != fsm.ResultReady) {
			return InboxReceipt{}, fmt.Errorf("executor Run must be working before result acceptance")
		}
		if now.IsZero() || now.Before(currentTask.UpdatedAt) {
			return InboxReceipt{}, fmt.Errorf("result timestamp must not precede Task state")
		}
		result.CreatedAt = now
		if err := result.Validate(); err != nil {
			return InboxReceipt{}, err
		}
		currentTask.Revision++
		currentTask.LastEventSeq++
		currentTask.Status = task.Blocked
		currentTask.UpdatedAt = now
		state.Tasks[id] = currentTask
		currentRun.Status = fsm.Done
		currentRun.LastUpdate = now
		state.Runs[runID] = currentRun
		event := task.DomainEvent{
			ID: task.EventID(id, currentTask.LastEventSeq), TaskID: id, RunID: runID,
			EventSeq: currentTask.LastEventSeq, TaskRevision: currentTask.Revision,
			TaskFence: currentTask.Fence, Type: task.ExecutorResultRecorded, ActorID: actorID,
			Summary: result.Summary, ExecutorResult: &result, CreatedAt: now,
		}
		state.Events = append(state.Events, event)
		ack, err := resultAck(currentRun, messageID, now)
		if err != nil {
			return InboxReceipt{}, err
		}
		state.Outbox[runID] = appendOutbox(state.Outbox[runID], OutboxMessage{Envelope: ack, EventID: event.ID})
		return InboxReceipt{ActorID: actorID, MessageID: messageID, TaskID: id, RunID: runID,
			EventIDs: []string{event.ID}, ExpectedRevision: expectedRevision,
			CommittedRevision: currentTask.Revision, RecordedAt: now}, nil
	})
}

// RecordReviewResult commits a canonical Review verdict, its Task transition,
// event, receipt and acknowledgement in one snapshot transaction. The actor is
// always selected from the persisted Run, never from result input.
func (s *CanonicalStore) RecordReviewResult(messageID string, id taskid.ID, runID string, expectedRevision uint64, result task.ReviewResult, now time.Time, coordinator ...assignment.Principal) (InboxReceipt, bool, error) {
	result = cloneReviewResult(result)
	return s.recordRoleResult(messageID, id, runID, expectedRevision, now, func() (*task.ReviewResult, *task.AuditResult) {
		return &result, nil
	}, coordinator...)
}

// RecordAuditResult commits a canonical Audit verdict and its acknowledgement
// through the same durable boundary as Review results.
func (s *CanonicalStore) RecordAuditResult(messageID string, id taskid.ID, runID string, expectedRevision uint64, result task.AuditResult, now time.Time, coordinator ...assignment.Principal) (InboxReceipt, bool, error) {
	result = cloneAuditResult(result)
	return s.recordRoleResult(messageID, id, runID, expectedRevision, now, func() (*task.ReviewResult, *task.AuditResult) {
		return nil, &result
	}, coordinator...)
}

func cloneReviewResult(result task.ReviewResult) task.ReviewResult {
	result.Findings = cloneFindings(result.Findings)
	result.InputArtifactRefs = cloneStrings(result.InputArtifactRefs)
	result.OutputArtifactRefs = cloneStrings(result.OutputArtifactRefs)
	if result.Subject != nil {
		cloned := *result.Subject
		result.Subject = &cloned
	}
	return result
}

func cloneAuditResult(result task.AuditResult) task.AuditResult {
	result.Findings = cloneFindings(result.Findings)
	result.InputArtifactRefs = cloneStrings(result.InputArtifactRefs)
	result.OutputArtifactRefs = cloneStrings(result.OutputArtifactRefs)
	if result.Subject != nil {
		cloned := *result.Subject
		result.Subject = &cloned
	}
	if result.Properties != nil {
		props := make([]task.AuditedProperty, len(result.Properties))
		copy(props, result.Properties)
		result.Properties = props
	}
	return result
}

func cloneStrings(values []string) []string {
	if values == nil {
		return nil
	}
	cloned := make([]string, len(values))
	copy(cloned, values)
	return cloned
}

func cloneFindings(findings []task.Finding) []task.Finding {
	if findings == nil {
		return nil
	}
	cloned := make([]task.Finding, len(findings))
	copy(cloned, findings)
	for i := range cloned {
		if findings[i].Evidence != nil {
			cloned[i].Evidence = append([]task.Reference{}, findings[i].Evidence...)
		}
	}
	return cloned
}

func (s *CanonicalStore) recordRoleResult(messageID string, id taskid.ID, runID string, expectedRevision uint64, now time.Time, payload func() (*task.ReviewResult, *task.AuditResult), coordinator ...assignment.Principal) (InboxReceipt, bool, error) {
	run, ok := s.Get(runID)
	if !ok || run.TaskID != id || run.RoleSelection == nil || run.RoleSelection.AgentID == "" {
		return InboxReceipt{}, false, fmt.Errorf("canonical Run with Task-scoped RoleSelection required")
	}
	actorID := run.RoleSelection.AgentID
	return s.applyAssignmentResultCommand(actorID, messageID, id, runID, expectedRevision, now, coordinator, func(state *CanonicalState) (InboxReceipt, error) {
		currentRun, exists := state.Runs[runID]
		if !exists || currentRun.TaskID != id || currentRun.RoleSelection == nil || currentRun.RoleSelection.AgentID != actorID {
			return InboxReceipt{}, fmt.Errorf("canonical Run actor changed")
		}
		currentTask, exists := state.Tasks[id]
		if !exists {
			return InboxReceipt{}, fmt.Errorf("canonical Task %q not found", id)
		}
		if currentTask.Status != task.InProgress || currentTask.CurrentStage == nil {
			return InboxReceipt{}, fmt.Errorf("task must be IN_PROGRESS at a review stage")
		}
		if currentRun.Terminal() || (currentRun.Status != fsm.Started && currentRun.Status != fsm.ResultReady) {
			return InboxReceipt{}, fmt.Errorf("role Run must be working before result acceptance")
		}
		if now.IsZero() || now.Before(currentTask.UpdatedAt) {
			return InboxReceipt{}, fmt.Errorf("result timestamp must not precede Task state")
		}
		review, audit := payload()
		var eventType task.EventType
		if review != nil {
			if currentTask.CurrentStage == nil || *currentTask.CurrentStage != task.Review || currentRun.RoleSelection.Profile.Role != roles.Reviewer {
				return InboxReceipt{}, fmt.Errorf("ReviewResult requires a REVIEW Task and Reviewer Run")
			}
			if taskAgentsByRole(*state, id, roles.Executor)[actorID] {
				return InboxReceipt{}, fmt.Errorf("reviewer %q also executed this Task: independence violation", actorID)
			}
			if review.Subject != nil && currentRun.TargetSHA != "" && review.Subject.CommitSHA != currentRun.TargetSHA {
				return InboxReceipt{}, fmt.Errorf("ReviewResult subject SHA %q does not match run TargetSHA %q", review.Subject.CommitSHA, currentRun.TargetSHA)
			}
			eventType = task.ReviewResultRecorded
			review.ResultID = task.EventID(id, currentTask.LastEventSeq+1)
			review.ReviewerID = actorID
			review.Round = uint32(countEventsByType(*state, id, task.ReviewResultRecorded) + 1)
			review.CreatedAt = now
			if err := assignFindingIDs(review.Findings, currentTask.ID, currentTask.LastEventSeq+1); err != nil {
				return InboxReceipt{}, err
			}
			stampFindings(review.Findings, now)
			if err := review.Validate(); err != nil {
				return InboxReceipt{}, err
			}
		} else if audit != nil {
			if currentTask.CurrentStage == nil || *currentTask.CurrentStage != task.Audit || currentRun.RoleSelection.Profile.Role != roles.Auditor {
				return InboxReceipt{}, fmt.Errorf("AuditResult requires an AUDIT Task and Auditor Run")
			}
			if taskAgentsByRole(*state, id, roles.Executor)[actorID] {
				return InboxReceipt{}, fmt.Errorf("auditor %q also executed this Task: independence violation", actorID)
			}
			if taskAgentsByRole(*state, id, roles.Reviewer)[actorID] {
				return InboxReceipt{}, fmt.Errorf("auditor %q also reviewed this Task: independence violation", actorID)
			}
			if audit.Subject != nil && currentRun.TargetSHA != "" && audit.Subject.CommitSHA != currentRun.TargetSHA {
				return InboxReceipt{}, fmt.Errorf("AuditResult subject SHA %q does not match run TargetSHA %q", audit.Subject.CommitSHA, currentRun.TargetSHA)
			}
			eventType = task.AuditResultRecorded
			audit.ResultID = task.EventID(id, currentTask.LastEventSeq+1)
			audit.AuditorID = actorID
			audit.Round = uint32(countEventsByType(*state, id, task.AuditResultRecorded) + 1)
			audit.CreatedAt = now
			if err := assignFindingIDs(audit.Findings, currentTask.ID, currentTask.LastEventSeq+1); err != nil {
				return InboxReceipt{}, err
			}
			stampFindings(audit.Findings, now)
			if err := audit.Validate(); err != nil {
				return InboxReceipt{}, err
			}
		} else {
			return InboxReceipt{}, fmt.Errorf("ReviewResult or AuditResult required")
		}

		if err := validateResultReferences(*state, id, currentTask.LastEventSeq+1, review, audit); err != nil {
			return InboxReceipt{}, err
		}
		currentTask.Revision++
		currentTask.LastEventSeq++
		currentTask.Status = task.Blocked
		currentTask.UpdatedAt = now
		state.Tasks[id] = currentTask
		currentRun.Status = fsm.Done
		currentRun.LastUpdate = now
		state.Runs[runID] = currentRun
		event := task.DomainEvent{
			ID: task.EventID(id, currentTask.LastEventSeq), TaskID: id, RunID: runID,
			EventSeq: currentTask.LastEventSeq, TaskRevision: currentTask.Revision,
			TaskFence: currentTask.Fence, Type: eventType, ActorID: actorID,
			CreatedAt: now, ReviewResult: review, AuditResult: audit,
		}
		if review != nil {
			event.Summary = review.Summary
		} else {
			event.Summary = audit.Summary
		}
		state.Events = append(state.Events, event)
		ack, err := resultAck(currentRun, messageID, now)
		if err != nil {
			return InboxReceipt{}, err
		}
		state.Outbox[runID] = appendOutbox(state.Outbox[runID], OutboxMessage{Envelope: ack, EventID: event.ID})
		return InboxReceipt{ActorID: actorID, MessageID: messageID, TaskID: id, RunID: runID,
			EventIDs: []string{event.ID}, ExpectedRevision: expectedRevision,
			CommittedRevision: currentTask.Revision, RecordedAt: now}, nil
	})
}

func stampFindings(findings []task.Finding, now time.Time) {
	for i := range findings {
		findings[i].CreatedAt = now
	}
}

func assignFindingIDs(findings []task.Finding, id taskid.ID, seq uint64) error {
	eventID := task.EventID(id, seq)
	for i := range findings {
		if findings[i].ID != "" {
			return fmt.Errorf("finding ID is coord-generated and must be omitted by caller")
		}
		findings[i].ID = fmt.Sprintf("%s/finding/%d", eventID, i+1)
	}
	return nil
}

func resultAck(run fsm.Run, messageID string, now time.Time) (envelope.Envelope, error) {
	if run.CoordinatorSession == "" || run.RoleSelection == nil || run.RoleSelection.Session == "" || run.TargetSHA == "" {
		return envelope.Envelope{}, fmt.Errorf("run coordinator, role session and target SHA are required for result acknowledgement")
	}
	ack := envelope.Envelope{
		SchemaVersion: 1, MessageID: envelope.NewID(), InReplyTo: messageID,
		Timestamp: now, From: run.CoordinatorSession, To: run.RoleSelection.Session,
		Kind: envelope.ResultAck, Task: run.Task, Run: run.RunID, Attempt: run.Attempt,
		TargetSHA: run.TargetSHA, Fence: run.Fence,
		ResultAckPayload: &envelope.ResultAckPayload{Outcome: "SUCCESS"},
	}
	if err := ack.Validate(); err != nil {
		return envelope.Envelope{}, err
	}
	return ack, nil
}

func appendOutbox(queue OutboxQueue, item OutboxMessage) OutboxQueue {
	queue.Messages = append(queue.Messages, item)
	return queue
}

func validateTaskResultEvents(state CanonicalState) error {
	events := make(map[string]task.DomainEvent, len(state.Events))
	for _, event := range state.Events {
		events[event.ID] = event
	}
	findingIDs := make(map[string]bool)
	resultRuns := make(map[string]bool)
	for _, event := range state.Events {
		var role roles.Role
		var findings []task.Finding
		switch event.Type {
		case task.ReviewResultRecorded:
			role = roles.Reviewer
			findings = event.ReviewResult.Findings
			if event.Summary != event.ReviewResult.Summary || !event.CreatedAt.Equal(event.ReviewResult.CreatedAt) {
				return fmt.Errorf("DomainEvent %q metadata differs from ReviewResult payload", event.ID)
			}
		case task.AuditResultRecorded:
			role = roles.Auditor
			findings = event.AuditResult.Findings
			if event.Summary != event.AuditResult.Summary || !event.CreatedAt.Equal(event.AuditResult.CreatedAt) {
				return fmt.Errorf("DomainEvent %q metadata differs from AuditResult payload", event.ID)
			}
		case task.ExecutorResultRecorded:
			role = roles.Executor
			if event.Summary != event.ExecutorResult.Summary || !event.CreatedAt.Equal(event.ExecutorResult.CreatedAt) {
				return fmt.Errorf("DomainEvent %q metadata differs from ExecutorResult payload", event.ID)
			}
		default:
			continue
		}
		run, ok := state.Runs[event.RunID]
		if !ok || run.TaskID != event.TaskID || run.RoleSelection == nil || run.RoleSelection.AgentID != event.ActorID || run.RoleSelection.Profile.Role != role {
			return fmt.Errorf("result DomainEvent %q does not match its Task-scoped role Run", event.ID)
		}
		if !run.Terminal() {
			return fmt.Errorf("result DomainEvent %q references a nonterminal Run", event.ID)
		}
		if event.EventSeq == state.Tasks[event.TaskID].LastEventSeq {
			expectedStage := task.Review
			switch role {
			case roles.Auditor:
				expectedStage = task.Audit
			case roles.Executor:
				expectedStage = task.Execution
			}
			taskValue := state.Tasks[event.TaskID]
			if taskValue.Status != task.Blocked || taskValue.CurrentStage == nil || *taskValue.CurrentStage != expectedStage {
				return fmt.Errorf("latest result event %q must leave Task BLOCKED at %s awaiting supervisor stop", event.ID, expectedStage)
			}
		}
		if role != roles.Executor {
			// Reviewer and Auditor runs must have a corresponding Executor Run for
			// the same Attempt; Executor is its own evidence.
			hasExecutorAttempt := false
			for _, candidate := range state.Runs {
				if candidate.TaskID == event.TaskID && candidate.Attempt == run.Attempt && candidate.RoleSelection != nil && candidate.RoleSelection.Profile.Role == roles.Executor {
					hasExecutorAttempt = true
					break
				}
			}
			if !hasExecutorAttempt {
				return fmt.Errorf("result Run %q has no Executor Run for the same Attempt", run.RunID)
			}
		}
		for i, finding := range findings {
			expectedID := fmt.Sprintf("%s/finding/%d", event.ID, i+1)
			if finding.ID != expectedID {
				return fmt.Errorf("finding %q ID is not coord-generated for DomainEvent %q", finding.ID, event.ID)
			}
			if findingIDs[finding.ID] {
				return fmt.Errorf("duplicate Finding ID %q", finding.ID)
			}
			findingIDs[finding.ID] = true
			if err := validateReferences(state, events, event.TaskID, event.EventSeq, finding.Evidence); err != nil {
				return fmt.Errorf("finding %q: %w", finding.ID, err)
			}
		}
		resultRuns[event.RunID] = true
	}
	for runID, run := range state.Runs {
		if run.TaskID != "" && run.Status == fsm.Done {
			role := roles.Role("")
			if run.RoleSelection != nil {
				role = run.RoleSelection.Profile.Role
			}
			// Canonical path Runs leave run.Result nil; legacy Runs carry it.
			// Reviewer and Auditor always require a DomainEvent.
			requiresDomainResult := run.Result == nil || role == roles.Reviewer || role == roles.Auditor
			if requiresDomainResult && !resultRuns[runID] {
				return fmt.Errorf("task-scoped DONE Run %q has no canonical result event", runID)
			}
		}
	}
	return nil
}

func validateResultReferences(state CanonicalState, id taskid.ID, currentSeq uint64, review *task.ReviewResult, audit *task.AuditResult) error {
	refs := make([]task.Reference, 0)
	appendResultRefs := func(findings []task.Finding) {
		for _, finding := range findings {
			refs = append(refs, finding.Evidence...)
		}
	}
	if review != nil {
		appendResultRefs(review.Findings)
	} else if audit != nil {
		appendResultRefs(audit.Findings)
	}
	events := make(map[string]task.DomainEvent, len(state.Events))
	for _, event := range state.Events {
		events[event.ID] = event
	}
	return validateReferences(state, events, id, currentSeq, refs)
}

// taskAgentsByRole returns all AgentIDs that have played the given role for a Task,
// looking at both current RoleSelection and historical RoleHistory of all Runs.
func taskAgentsByRole(state CanonicalState, id taskid.ID, role roles.Role) map[string]bool {
	agents := make(map[string]bool)
	for _, run := range state.Runs {
		if run.TaskID != id {
			continue
		}
		for _, sel := range run.RoleHistory {
			if sel.Profile.Role == role && sel.AgentID != "" {
				agents[sel.AgentID] = true
			}
		}
		if run.RoleSelection != nil && run.RoleSelection.Profile.Role == role && run.RoleSelection.AgentID != "" {
			agents[run.RoleSelection.AgentID] = true
		}
	}
	return agents
}

// countEventsByType returns the number of DomainEvents of a given type for a Task.
func countEventsByType(state CanonicalState, id taskid.ID, eventType task.EventType) int {
	count := 0
	for _, event := range state.Events {
		if event.TaskID == id && event.Type == eventType {
			count++
		}
	}
	return count
}

// RecordReviewResultSuperseded emits a supersession event that marks a prior
// ReviewResult ineligible for readiness evaluation. The coordinatorRunID must
// be a coordinator Run for this Task; the supersededResultID must name a prior
// ReviewResultRecorded event ID for this Task.
func (s *CanonicalStore) RecordReviewResultSuperseded(messageID string, id taskid.ID, coordinatorRunID string, expectedRevision uint64, supersededResultID string, reason string, now time.Time) (InboxReceipt, bool, error) {
	return s.recordResultSuperseded(messageID, id, coordinatorRunID, expectedRevision, supersededResultID, reason, task.ReviewResultSuperseded, task.ReviewResultRecorded, now)
}

// RecordAuditResultSuperseded emits a supersession event for a prior AuditResult.
func (s *CanonicalStore) RecordAuditResultSuperseded(messageID string, id taskid.ID, coordinatorRunID string, expectedRevision uint64, supersededResultID string, reason string, now time.Time) (InboxReceipt, bool, error) {
	return s.recordResultSuperseded(messageID, id, coordinatorRunID, expectedRevision, supersededResultID, reason, task.AuditResultSuperseded, task.AuditResultRecorded, now)
}

func (s *CanonicalStore) recordResultSuperseded(messageID string, id taskid.ID, coordinatorRunID string, expectedRevision uint64, supersededResultID string, reason string, supersededType task.EventType, originalType task.EventType, now time.Time) (InboxReceipt, bool, error) {
	if supersededResultID == "" || reason == "" {
		return InboxReceipt{}, false, fmt.Errorf("superseded_result_id and reason are required")
	}
	run, ok := s.Get(coordinatorRunID)
	if !ok || run.TaskID != id {
		return InboxReceipt{}, false, fmt.Errorf("coordinator Run not found for this Task")
	}
	actorID := run.RoleSelection.AgentID
	if actorID == "" {
		return InboxReceipt{}, false, fmt.Errorf("coordinator Run requires a RoleSelection with AgentID")
	}
	return s.ApplyInboxCommand(actorID, messageID, id, expectedRevision, func(state *CanonicalState) (InboxReceipt, error) {
		currentTask, exists := state.Tasks[id]
		if !exists {
			return InboxReceipt{}, fmt.Errorf("canonical Task %q not found", id)
		}
		foundOriginal := false
		alreadySuperseded := false
		for _, event := range state.Events {
			if event.TaskID != id {
				continue
			}
			if event.Type == originalType && event.ID == supersededResultID {
				foundOriginal = true
			}
			if event.Type == supersededType && event.SupersededResultID == supersededResultID {
				alreadySuperseded = true
			}
		}
		if !foundOriginal {
			return InboxReceipt{}, fmt.Errorf("result %q not found as %s for this Task", supersededResultID, originalType)
		}
		if alreadySuperseded {
			return InboxReceipt{}, fmt.Errorf("result %q is already superseded", supersededResultID)
		}
		currentTask.Revision++
		currentTask.LastEventSeq++
		currentTask.UpdatedAt = now
		state.Tasks[id] = currentTask
		event := task.DomainEvent{
			ID: task.EventID(id, currentTask.LastEventSeq), TaskID: id,
			RunID: coordinatorRunID, EventSeq: currentTask.LastEventSeq,
			TaskRevision: currentTask.Revision, TaskFence: currentTask.Fence,
			Type: supersededType, ActorID: actorID, Summary: reason,
			SupersededResultID: supersededResultID, CreatedAt: now,
		}
		state.Events = append(state.Events, event)
		return InboxReceipt{
			ActorID: actorID, MessageID: messageID, TaskID: id,
			RunID: coordinatorRunID, EventIDs: []string{event.ID},
			ExpectedRevision: expectedRevision, CommittedRevision: currentTask.Revision,
			RecordedAt: now,
		}, nil
	})
}

func validateReferences(state CanonicalState, events map[string]task.DomainEvent, id taskid.ID, currentSeq uint64, refs []task.Reference) error {
	for _, ref := range refs {
		if strings.TrimSpace(ref.ID) == "" || strings.TrimSpace(ref.Kind) != ref.Kind {
			return fmt.Errorf("reference kind and ID are required")
		}
		switch ref.Kind {
		case task.ReferenceTask:
			if ref.ID != string(id) {
				return fmt.Errorf("reference points outside Task scope")
			}
		case task.ReferenceRun:
			run, ok := state.Runs[ref.ID]
			if !ok || run.TaskID != id {
				return fmt.Errorf("reference points to missing or unrelated Run %q", ref.ID)
			}
		case task.ReferenceDomainEvent:
			event, ok := events[ref.ID]
			if !ok || event.TaskID != id || event.EventSeq >= currentSeq {
				return fmt.Errorf("reference points to missing, future or unrelated DomainEvent %q", ref.ID)
			}
		case task.ReferenceArtifact:
			return fmt.Errorf("artifact metadata not defined")
		default:
			return fmt.Errorf("unsupported reference kind %q", ref.Kind)
		}
	}
	return nil
}
