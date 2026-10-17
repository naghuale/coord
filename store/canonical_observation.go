package store

import (
	"coord/internal/externalaction"
	"coord/task"
	"coord/taskid"
	"fmt"
	"reflect"
	"time"
)

func (s *CanonicalStore) RecordCoordinatorCompletion(commandID string, id taskid.ID, runID string, expectedRevision uint64, completion task.CoordinatorCompletion, now time.Time) (InboxReceipt, bool, error) {
	if commandID == "" || id.Validate() != nil || runID == "" || now.IsZero() {
		return InboxReceipt{}, false, fmt.Errorf("valid coordinator completion identity required")
	}
	if err := completion.Validate(); err != nil {
		return InboxReceipt{}, false, err
	}
	return s.ApplyInboxCommand("coordd", commandID, id, expectedRevision, func(state *CanonicalState) (InboxReceipt, error) {
		current, ok := state.Tasks[id]
		run, runOK := state.Runs[runID]
		if !ok || !runOK || run.TaskID != id || run.TargetSHA != completion.Subject.CommitSHA {
			return InboxReceipt{}, fmt.Errorf("completion subject does not match canonical Run")
		}
		if now.Before(current.UpdatedAt) {
			return InboxReceipt{}, fmt.Errorf("completion timestamp precedes Task")
		}
		canonicalSubject, ok := findCanonicalSubject(state, id, run.TargetSHA)
		if !ok || !reflect.DeepEqual(canonicalSubject, completion.Subject) {
			return InboxReceipt{}, fmt.Errorf("completion subject lacks canonical provenance")
		}
		if current.Revision == ^uint64(0) || current.LastEventSeq == ^uint64(0) {
			return InboxReceipt{}, fmt.Errorf("task revision exhausted")
		}
		current.Revision++
		current.LastEventSeq++
		current.UpdatedAt = now
		state.Tasks[id] = current
		eventID := task.EventID(id, current.LastEventSeq)
		state.Events = append(state.Events, task.DomainEvent{ID: eventID, TaskID: id, RunID: runID, EventSeq: current.LastEventSeq, TaskRevision: current.Revision, TaskFence: current.Fence, Type: task.CoordinatorCompletionRecorded, ActorID: "coordd", Summary: completion.DecisionDigest, CoordinatorCompletion: &completion, CreatedAt: now})
		return InboxReceipt{ActorID: "coordd", MessageID: commandID, TaskID: id, RunID: runID, EventIDs: []string{eventID}, ExpectedRevision: expectedRevision, CommittedRevision: current.Revision, RecordedAt: now}, nil
	})
}

func findCanonicalSubject(state *CanonicalState, id taskid.ID, commitSHA string) (task.SubjectBinding, bool) {
	var result task.SubjectBinding
	found := false
	for _, event := range state.Events {
		if event.TaskID != id {
			continue
		}
		var candidate *task.SubjectBinding
		if event.ReviewResult != nil {
			candidate = event.ReviewResult.Subject
		}
		if event.AuditResult != nil {
			candidate = event.AuditResult.Subject
		}
		if candidate == nil || candidate.CommitSHA != commitSHA {
			continue
		}
		if found && !reflect.DeepEqual(result, *candidate) {
			return task.SubjectBinding{}, false
		}
		result, found = *candidate, true
	}
	return result, found
}

// EnqueueGitHubObservation is the only admission path for GitHub read actions.
// It derives origin, subject and assignment from canonical records and commits
// Task event, ExternalAction and Inbox receipt in one State + Event transaction.
func (s *CanonicalStore) EnqueueGitHubObservation(commandID string, id taskid.ID, runID string, expectedRevision uint64, originKind task.OriginKind, request externalaction.GitHubActionPayload, now time.Time) (InboxReceipt, bool, error) {
	if commandID == "" || id.Validate() != nil || runID == "" || now.IsZero() {
		return InboxReceipt{}, false, fmt.Errorf("valid canonical observation command identity required")
	}
	return s.ApplyInboxCommand("coordd", commandID, id, expectedRevision, func(state *CanonicalState) (InboxReceipt, error) {
		currentTask, ok := state.Tasks[id]
		if !ok || currentTask.Status == task.Completed || currentTask.Status == task.Failed {
			return InboxReceipt{}, fmt.Errorf("active canonical Task required")
		}
		if now.Before(currentTask.UpdatedAt) {
			return InboxReceipt{}, fmt.Errorf("observation command time precedes Task")
		}
		run, ok := state.Runs[runID]
		if !ok || run.TaskID != id || run.TargetSHA == "" {
			return InboxReceipt{}, fmt.Errorf("canonical Run required")
		}
		projectID, assignmentID, subject, err := resolveGitHubOrigin(state, id, runID)
		if err != nil {
			return InboxReceipt{}, err
		}
		if request.Operation != externalaction.ObserveIssue && request.Operation != externalaction.ObservePullRequest && request.Operation != externalaction.ObservePipeline {
			return InboxReceipt{}, fmt.Errorf("GitHub writes are disabled")
		}
		if !validGitHubOriginKind(originKind, request.Operation) {
			return InboxReceipt{}, fmt.Errorf("origin kind does not authorize observation operation")
		}
		if request.Target.Repository != subject.RepositoryID {
			return InboxReceipt{}, fmt.Errorf("GitHub repository differs from canonical subject")
		}
		if request.Operation == externalaction.ObservePullRequest && request.Target.ExpectedHead != subject.CommitSHA {
			return InboxReceipt{}, fmt.Errorf("PR observation subject SHA mismatch")
		}
		if request.Operation == externalaction.ObservePipeline && (request.Pipeline == nil || request.Pipeline.CommitSHA != subject.CommitSHA) {
			return InboxReceipt{}, fmt.Errorf("pipeline selector subject SHA mismatch")
		}
		if request.Target.ExpectedState == "" {
			request.Target.ExpectedState = "ANY"
		}
		if currentTask.LastEventSeq == ^uint64(0) || currentTask.Revision == ^uint64(0) {
			return InboxReceipt{}, fmt.Errorf("task revision exhausted")
		}
		messageEventID := task.EventID(id, currentTask.LastEventSeq+1)
		actionID := "github-observe/" + externalaction.Digest(struct {
			Task    string
			Command string
		}{id.String(), commandID})
		origin := task.ExternalActionOrigin{Kind: originKind, ProjectID: projectID, TaskID: id.String(), RunID: runID, AssignmentID: assignmentID, Subject: subject, CommandID: commandID, EventID: messageEventID}
		request.ProjectID = projectID
		request.OriginAssignmentID = assignmentID
		request.Origin = &origin
		request.ScopeDigest = externalaction.Digest(struct {
			Version uint32
			Origin  task.ExternalActionOrigin
		}{1, origin})
		action, err := externalaction.NewGitHub(actionID, request, now)
		if err != nil {
			return InboxReceipt{}, err
		}
		if prior, exists := state.Orchestration.Actions[action.ID]; exists {
			if prior.Key == action.Key && prior.RequestDigest == action.RequestDigest {
				return InboxReceipt{}, fmt.Errorf("idempotent command replay must be resolved by Inbox")
			}
			return InboxReceipt{}, fmt.Errorf("observation action identity conflict")
		}
		currentTask.Revision++
		currentTask.LastEventSeq++
		currentTask.UpdatedAt = now
		state.Tasks[id] = currentTask
		state.Events = append(state.Events, task.DomainEvent{ID: messageEventID, TaskID: id, RunID: runID, EventSeq: currentTask.LastEventSeq, TaskRevision: currentTask.Revision, TaskFence: currentTask.Fence, Type: task.ExternalActionStateRecorded, ActorID: "coordd", Summary: action.ID, ExternalActionID: action.ID, ExternalActionStatus: string(externalaction.Pending), CreatedAt: now})
		actionEvent := externalaction.Event{ActionID: action.ID, Revision: state.Revision + 1, Digest: externalaction.Digest(action), At: now}
		state.Orchestration.Actions[action.ID] = action
		state.Orchestration.Events = append(state.Orchestration.Events, actionEvent)
		return InboxReceipt{ActorID: "coordd", MessageID: commandID, TaskID: id, RunID: runID, EventIDs: []string{messageEventID}, ExpectedRevision: expectedRevision, CommittedRevision: currentTask.Revision, RecordedAt: now}, nil
	})
}

func validGitHubOriginKind(kind task.OriginKind, operation externalaction.GitHubOperation) bool {
	if operation == externalaction.ObservePipeline {
		return kind == task.OriginCIPipeline
	}
	return kind == task.OriginTrackingProjection || kind == task.OriginReviewProjection || kind == task.OriginAuditProjection
}

func resolveGitHubOrigin(state *CanonicalState, id taskid.ID, runID string) (string, string, task.SubjectBinding, error) {
	run := state.Runs[runID]
	project, assignment := "", ""
	for _, action := range state.Orchestration.Actions {
		if action.Family != externalaction.AgentSession || action.Kind != externalaction.Deliver || action.Binding.TaskID != id.String() || action.Binding.RunID != runID {
			continue
		}
		if project != "" && (project != action.Binding.ProjectID || assignment != action.Binding.AssignmentID) {
			return "", "", task.SubjectBinding{}, fmt.Errorf("ambiguous canonical assignment origin")
		}
		project, assignment = action.Binding.ProjectID, action.Binding.AssignmentID
	}
	if project == "" || assignment == "" {
		return "", "", task.SubjectBinding{}, fmt.Errorf("canonical assignment origin missing")
	}
	var subject *task.SubjectBinding
	for _, event := range state.Events {
		if event.TaskID != id {
			continue
		}
		var candidate *task.SubjectBinding
		if event.ReviewResult != nil {
			candidate = event.ReviewResult.Subject
		}
		if event.AuditResult != nil {
			candidate = event.AuditResult.Subject
		}
		if candidate == nil || candidate.CommitSHA != run.TargetSHA {
			continue
		}
		copy := *candidate
		if subject != nil && !reflect.DeepEqual(*subject, copy) {
			return "", "", task.SubjectBinding{}, fmt.Errorf("ambiguous canonical subject binding")
		}
		subject = &copy
	}
	if subject == nil {
		return "", "", task.SubjectBinding{}, fmt.Errorf("canonical full subject evidence missing")
	}
	if err := subject.Validate(); err != nil {
		return "", "", task.SubjectBinding{}, err
	}
	return project, assignment, *subject, nil
}

func githubWorkflowAuthority(state *CanonicalState, action externalaction.Action) error {
	origin, ok, err := action.CanonicalOrigin()
	if err != nil || !ok || origin.TaskID == "" {
		return ErrActionAuthority
	}
	id := taskid.ID(origin.TaskID)
	currentTask, taskOK := state.Tasks[id]
	run, runOK := state.Runs[origin.RunID]
	if !taskOK || !runOK || run.TaskID != id || run.TargetSHA != origin.Subject.CommitSHA {
		return ErrActionAuthority
	}
	project, assignment, subject, err := resolveGitHubOrigin(state, id, origin.RunID)
	if err != nil || project != origin.ProjectID || assignment != origin.AssignmentID || !reflect.DeepEqual(subject, origin.Subject) {
		return ErrActionAuthority
	}
	payload, err := action.GitHubPayload()
	if err != nil || !validGitHubOriginKind(origin.Kind, payload.Operation) {
		return ErrActionAuthority
	}
	if payload.Target.Repository != origin.Subject.RepositoryID || (payload.Operation == externalaction.ObservePullRequest && payload.Target.ExpectedHead != origin.Subject.CommitSHA) || (payload.Operation == externalaction.ObservePipeline && (payload.Pipeline == nil || payload.Pipeline.CommitSHA != origin.Subject.CommitSHA)) {
		return ErrActionAuthority
	}
	var sourceEvent *task.DomainEvent
	for i := range state.Events {
		if state.Events[i].ID == origin.EventID {
			sourceEvent = &state.Events[i]
			break
		}
	}
	if sourceEvent == nil || sourceEvent.TaskID != id || sourceEvent.RunID != origin.RunID || sourceEvent.Type != task.ExternalActionStateRecorded || sourceEvent.ExternalActionID != action.ID || sourceEvent.ExternalActionStatus != string(externalaction.Pending) {
		return ErrActionAuthority
	}
	commandReceipt, found := false, false
	for _, receipt := range state.Inbox {
		if receipt.ActorID == "coordd" && receipt.MessageID == origin.CommandID && receipt.TaskID == id && receipt.RunID == origin.RunID {
			commandReceipt = true
			for _, eventID := range receipt.EventIDs {
				if eventID == origin.EventID {
					found = true
				}
			}
		}
	}
	if !commandReceipt || !found || currentTask.Revision < sourceEvent.TaskRevision {
		return ErrActionAuthority
	}
	return nil
}
