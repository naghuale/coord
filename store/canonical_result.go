package store

import (
	"coord/envelope"
	"coord/fsm"
	"coord/roles"
	"coord/task"
	"coord/taskid"
	"fmt"
	"strings"
	"time"
)

// RecordReviewResult commits a canonical Review verdict, its Task transition,
// event, receipt and acknowledgement in one snapshot transaction. The actor is
// always selected from the persisted Run, never from result input.
func (s *CanonicalStore) RecordReviewResult(messageID string, id taskid.ID, runID string, expectedRevision uint64, result task.ReviewResult, now time.Time) (InboxReceipt, bool, error) {
	result = cloneReviewResult(result)
	return s.recordRoleResult(messageID, id, runID, expectedRevision, now, func() (*task.ReviewResult, *task.AuditResult) {
		return &result, nil
	})
}

// RecordAuditResult commits a canonical Audit verdict and its acknowledgement
// through the same durable boundary as Review results.
func (s *CanonicalStore) RecordAuditResult(messageID string, id taskid.ID, runID string, expectedRevision uint64, result task.AuditResult, now time.Time) (InboxReceipt, bool, error) {
	result = cloneAuditResult(result)
	return s.recordRoleResult(messageID, id, runID, expectedRevision, now, func() (*task.ReviewResult, *task.AuditResult) {
		return nil, &result
	})
}

func cloneReviewResult(result task.ReviewResult) task.ReviewResult {
	result.Findings = cloneFindings(result.Findings)
	result.InputArtifactRefs = cloneStrings(result.InputArtifactRefs)
	result.OutputArtifactRefs = cloneStrings(result.OutputArtifactRefs)
	return result
}

func cloneAuditResult(result task.AuditResult) task.AuditResult {
	result.Findings = cloneFindings(result.Findings)
	result.InputArtifactRefs = cloneStrings(result.InputArtifactRefs)
	result.OutputArtifactRefs = cloneStrings(result.OutputArtifactRefs)
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

func (s *CanonicalStore) recordRoleResult(messageID string, id taskid.ID, runID string, expectedRevision uint64, now time.Time, payload func() (*task.ReviewResult, *task.AuditResult)) (InboxReceipt, bool, error) {
	run, ok := s.Get(runID)
	if !ok || run.TaskID != id || run.RoleSelection == nil || run.RoleSelection.AgentID == "" {
		return InboxReceipt{}, false, fmt.Errorf("canonical Run with Task-scoped RoleSelection required")
	}
	actorID := run.RoleSelection.AgentID
	return s.ApplyInboxCommand(actorID, messageID, id, expectedRevision, func(state *CanonicalState) (InboxReceipt, error) {
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
			eventType = task.ReviewResultRecorded
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
			eventType = task.AuditResultRecorded
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
			if role == roles.Auditor {
				expectedStage = task.Audit
			}
			taskValue := state.Tasks[event.TaskID]
			if taskValue.Status != task.Blocked || taskValue.CurrentStage == nil || *taskValue.CurrentStage != expectedStage {
				return fmt.Errorf("latest result event %q must leave Task BLOCKED at %s awaiting supervisor stop", event.ID, expectedStage)
			}
		}
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
