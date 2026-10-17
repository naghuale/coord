package coord

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"coord/internal/actionmap"
	"coord/internal/watchdog"
	"coord/store"
	"coord/task"
	"coord/taskid"
)

var ErrCoordinatorCandidateRejected = errors.New("coordinator candidate rejected")

// CoordinatorCandidate contains a typed decision and canonical references.
// It carries no evidence map, state mutation callback, credentials or output text.
type CoordinatorCandidate struct {
	CommandID            string
	TaskID               taskid.ID
	RunID                string
	ExpectedTaskRevision uint64
	Subject              task.SubjectBinding
	Transition           string
	Decision             actionmap.CompletionDecision
	Result               watchdog.CoordinatorResult
	CreatedAt            time.Time
}

type CoordinatorCandidateSource interface {
	Next(context.Context) (CoordinatorCandidate, bool, error)
	Acknowledge(context.Context, string) error
	Reject(context.Context, string, string) error
}
type CoordinatorCandidateValidator interface {
	Validate(context.Context, store.CanonicalState, CoordinatorCandidate) error
}
type CoordinatorCandidateStore interface {
	CanonicalSnapshot() (store.CanonicalState, error)
	RecordCoordinatorCompletion(string, taskid.ID, string, uint64, task.CoordinatorCompletion, time.Time) (store.InboxReceipt, bool, error)
}
type CoordinatorCandidatePublisher interface {
	PublishCoordinatorCandidate(context.Context, CoordinatorCandidate, store.InboxReceipt) error
}

type CoordinatorCandidateProcessor struct {
	Source    CoordinatorCandidateSource
	Store     CoordinatorCandidateStore
	Validator CoordinatorCandidateValidator
	Publisher CoordinatorCandidatePublisher
}

func (p *CoordinatorCandidateProcessor) ProcessNext(ctx context.Context) error {
	if p == nil || p.Source == nil {
		return nil
	}
	if p.Store == nil || p.Validator == nil {
		return fmt.Errorf("candidate processor requires canonical store and strict validator")
	}
	candidate, ok, err := p.Source.Next(ctx)
	if err != nil || !ok {
		return err
	}
	snapshot, err := p.Store.CanonicalSnapshot()
	if err != nil {
		return err
	}
	decisionDigest, err := candidateDigest(candidate)
	if err != nil {
		return err
	}
	completion := candidateCompletion(candidate, decisionDigest)
	receipt, committed, err := existingCoordinatorCompletion(snapshot, candidate, decisionDigest)
	if err != nil {
		return err
	}
	if !committed {
		if err = p.Validator.Validate(ctx, snapshot, candidate); err != nil {
			if rejectErr := p.Source.Reject(ctx, candidate.CommandID, err.Error()); rejectErr != nil {
				return fmt.Errorf("strict candidate validation failed (%v) and quarantine failed: %w", err, rejectErr)
			}
			return fmt.Errorf("%w: %v", ErrCoordinatorCandidateRejected, err)
		}
		receipt, _, err = p.Store.RecordCoordinatorCompletion(candidate.CommandID, candidate.TaskID, candidate.RunID, candidate.ExpectedTaskRevision, completion, candidate.CreatedAt)
		if err != nil {
			return err
		}
	}
	if p.Publisher != nil {
		if err := p.Publisher.PublishCoordinatorCandidate(ctx, candidate, receipt); err != nil {
			return err
		}
	}
	// Keep the source item available until canonical commit and publication both
	// succeed. A replay is safe because the canonical Inbox command is idempotent.
	return p.Source.Acknowledge(ctx, candidate.CommandID)
}

func candidateCompletion(candidate CoordinatorCandidate, digest string) task.CoordinatorCompletion {
	boundary := task.CompletionNoBoundary
	switch {
	case candidate.Decision.OwnerRequired:
		boundary = task.CompletionOwnerBoundary
	case candidate.Decision.HumanActionRequired:
		boundary = task.CompletionHumanBoundary
	case candidate.Decision.BlockedExternal:
		boundary = task.CompletionExternalBoundary
	}
	return task.CoordinatorCompletion{DecisionDigest: digest, Transition: candidate.Transition, Boundary: boundary, Subject: candidate.Subject}
}

func existingCoordinatorCompletion(state store.CanonicalState, candidate CoordinatorCandidate, digest string) (store.InboxReceipt, bool, error) {
	for _, receipt := range state.Inbox {
		if receipt.MessageID != candidate.CommandID {
			continue
		}
		if receipt.ActorID != "coordd" || receipt.TaskID != candidate.TaskID || receipt.RunID != candidate.RunID {
			return store.InboxReceipt{}, false, fmt.Errorf("coordinator completion command identity conflict")
		}
		for _, eventID := range receipt.EventIDs {
			for _, event := range state.Events {
				if event.ID != eventID {
					continue
				}
				completion := event.CoordinatorCompletion
				expected := candidateCompletion(candidate, digest)
				if event.Type != task.CoordinatorCompletionRecorded || event.TaskID != candidate.TaskID || event.RunID != candidate.RunID || completion == nil || *completion != expected || event.Summary != digest {
					return store.InboxReceipt{}, false, fmt.Errorf("coordinator completion command body conflict")
				}
				return receipt, true, nil
			}
		}
		return store.InboxReceipt{}, false, fmt.Errorf("coordinator completion receipt has no matching event")
	}
	return store.InboxReceipt{}, false, nil
}

func candidateDigest(candidate CoordinatorCandidate) (string, error) {
	value := struct {
		Version              uint32
		CommandID            string
		TaskID               taskid.ID
		RunID                string
		ExpectedTaskRevision uint64
		Subject              task.SubjectBinding
		Transition           string
		Decision             actionmap.CompletionDecision
		Result               watchdog.CoordinatorResult
	}{1, candidate.CommandID, candidate.TaskID, candidate.RunID, candidate.ExpectedTaskRevision, candidate.Subject, candidate.Transition, candidate.Decision, candidate.Result}
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// StrictCoordinatorCandidateValidator uses evidence resolved from canonical
// state. A candidate supplies references and decisions only, never trusted facts.
type StrictCoordinatorCandidateValidator struct {
	ActionMap        actionmap.ActionMap
	WorkflowCatalog  actionmap.WorkflowCatalog
	CompletionPolicy actionmap.CompletionPolicy
	Resolve          func(context.Context, store.CanonicalState, CoordinatorCandidate) (actionmap.CompletionContext, watchdog.CoordinatorState, error)
	Now              func() time.Time
}

func (v StrictCoordinatorCandidateValidator) Validate(ctx context.Context, state store.CanonicalState, c CoordinatorCandidate) error {
	if v.Resolve == nil || v.Now == nil || c.CommandID == "" || c.TaskID.Validate() != nil || c.RunID == "" || c.Transition == "" || c.CreatedAt.IsZero() {
		return fmt.Errorf("candidate provenance resolver and complete typed candidate required")
	}
	t, ok := state.Tasks[c.TaskID]
	if !ok || t.Revision != c.ExpectedTaskRevision {
		return fmt.Errorf("candidate Task revision mismatch")
	}
	run, ok := state.Runs[c.RunID]
	if !ok || run.TaskID != c.TaskID || run.TargetSHA != c.Subject.CommitSHA {
		return fmt.Errorf("candidate Run subject mismatch")
	}
	canonicalSubject, found := canonicalCandidateSubject(state, c.TaskID, run.TargetSHA)
	if !found || !reflect.DeepEqual(canonicalSubject, c.Subject) {
		return fmt.Errorf("candidate exact subject is not canonical")
	}
	if c.Decision.SubjectSHA != "" && c.Decision.SubjectSHA != c.Subject.CommitSHA {
		return fmt.Errorf("completion decision subject mismatch")
	}
	if !candidateTransitionExists(v.ActionMap, c.Transition) {
		return fmt.Errorf("unknown canonical transition")
	}
	completionContext, watchdogState, err := v.Resolve(ctx, state, c)
	if err != nil {
		return err
	}
	completionContext.Now = v.Now()
	if err = actionmap.ValidateCompletionDecision(v.CompletionPolicy, v.WorkflowCatalog, v.ActionMap, completionContext, c.Decision); err != nil {
		return err
	}
	if c.Decision.OwnerRequired != c.Result.OwnerRequired || c.Decision.HumanActionRequired != c.Result.HumanActionRequired || c.Decision.BlockedExternal != c.Result.BlockedExternal {
		return fmt.Errorf("completion boundary declarations differ")
	}
	if c.Result.NextTransition != c.Transition {
		return fmt.Errorf("selected transition differs from completion result")
	}
	// Boundary proof is evaluated above. Run the watchdog's non-boundary and
	// session checks with boundary bits cleared so they cannot short-circuit.
	progress := c.Result
	progress.OwnerRequired = false
	progress.HumanActionRequired = false
	progress.BlockedExternal = false
	return watchdog.ValidateCoordinatorCompletion(watchdogState, progress)
}

func canonicalCandidateSubject(state store.CanonicalState, id taskid.ID, sha string) (task.SubjectBinding, bool) {
	var value task.SubjectBinding
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
		if candidate == nil || candidate.CommitSHA != sha {
			continue
		}
		if found && !reflect.DeepEqual(value, *candidate) {
			return task.SubjectBinding{}, false
		}
		value = *candidate
		found = true
	}
	return value, found
}

func candidateTransitionExists(am actionmap.ActionMap, transition string) bool {
	for _, event := range am.Events {
		if event.Transition == transition {
			return true
		}
	}
	_, ok := am.RecoveryTransitions[transition]
	return ok
}

func (c *Coordinator) processCoordinatorCandidate(ctx context.Context) error {
	if c.CandidateProcessor == nil {
		return nil
	}
	err := c.CandidateProcessor.ProcessNext(ctx)
	if errors.Is(err, ErrCoordinatorCandidateRejected) {
		c.Metrics.Inc("coordinator_candidate_rejected_total")
		return nil
	}
	return err
}
