package coord

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"coord/fsm"
	"coord/internal/actionmap"
	"coord/internal/watchdog"
	"coord/store"
	"coord/task"
	"coord/taskid"
	"gopkg.in/yaml.v3"
)

type candidateSource struct {
	candidate CoordinatorCandidate
	ok        bool
	acked     int
	rejected  int
}

func (s *candidateSource) Next(context.Context) (CoordinatorCandidate, bool, error) {
	if !s.ok {
		return CoordinatorCandidate{}, false, nil
	}
	return s.candidate, true, nil
}
func (s *candidateSource) Acknowledge(_ context.Context, command string) error {
	if command != s.candidate.CommandID {
		return errors.New("ack command mismatch")
	}
	s.acked++
	s.ok = false
	return nil
}
func (s *candidateSource) Reject(_ context.Context, command, reason string) error {
	if command != s.candidate.CommandID || reason == "" {
		return errors.New("invalid rejection identity")
	}
	s.rejected++
	s.ok = false
	return nil
}

type candidateValidator func(context.Context, store.CanonicalState, CoordinatorCandidate) error

func (v candidateValidator) Validate(ctx context.Context, state store.CanonicalState, c CoordinatorCandidate) error {
	return v(ctx, state, c)
}

type candidateStore struct {
	snapshot store.CanonicalState
	record   int
	last     task.CoordinatorCompletion
	err      error
}

func (s *candidateStore) CanonicalSnapshot() (store.CanonicalState, error) { return s.snapshot, nil }
func (s *candidateStore) RecordCoordinatorCompletion(_ string, _ taskid.ID, _ string, _ uint64, c task.CoordinatorCompletion, _ time.Time) (store.InboxReceipt, bool, error) {
	s.record++
	s.last = c
	if s.err != nil {
		return store.InboxReceipt{}, false, s.err
	}
	if s.snapshot.Inbox == nil {
		s.snapshot.Inbox = map[string]store.InboxReceipt{}
	}
	receipt := store.InboxReceipt{ActorID: "coordd", MessageID: "command", TaskID: taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV"), RunID: "run", EventIDs: []string{task.EventID(taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV"), 1)}, ExpectedRevision: 1, CommittedRevision: 2, RecordedAt: time.Unix(101, 0).UTC()}
	s.snapshot.Inbox[store.InboxKey(receipt.ActorID, receipt.MessageID)] = receipt
	s.snapshot.Events = append(s.snapshot.Events, task.DomainEvent{ID: receipt.EventIDs[0], TaskID: receipt.TaskID, RunID: receipt.RunID, EventSeq: 1, TaskRevision: 2, Type: task.CoordinatorCompletionRecorded, ActorID: "coordd", Summary: c.DecisionDigest, CoordinatorCompletion: &c, CreatedAt: receipt.RecordedAt})
	return receipt, false, nil
}

type candidatePublisher struct {
	published int
	err       error
}

func (p *candidatePublisher) PublishCoordinatorCandidate(context.Context, CoordinatorCandidate, store.InboxReceipt) error {
	p.published++
	return p.err
}

func validCandidate() CoordinatorCandidate {
	return CoordinatorCandidate{CommandID: "command", TaskID: taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV"), RunID: "run", ExpectedTaskRevision: 1,
		Subject:    task.SubjectBinding{RepositoryID: "naghuale/coord", CommitSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", TreeHash: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		Transition: "CONTINUE_AUTONOMOUSLY", CreatedAt: time.Unix(100, 0).UTC()}
}

func TestProductionCoordinatorInvokesStrictCompletionValidation(t *testing.T) {
	source := &candidateSource{candidate: validCandidate(), ok: true}
	db := &candidateStore{}
	published := &candidatePublisher{}
	called := false
	p := &CoordinatorCandidateProcessor{Source: source, Store: db, Validator: candidateValidator(func(context.Context, store.CanonicalState, CoordinatorCandidate) error { called = true; return nil }), Publisher: published}
	if err := p.ProcessNext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !called || db.record != 1 || published.published != 1 {
		t.Fatalf("validator=%v record=%d published=%d", called, db.record, published.published)
	}
}

func TestRunWithReadinessProcessesCandidateBeforeReceivingNextMessage(t *testing.T) {
	f := setup(t)
	close(f.tp.in)
	source := &candidateSource{candidate: validCandidate(), ok: true}
	db := &candidateStore{}
	validated := false
	f.c.CandidateProcessor = &CoordinatorCandidateProcessor{Source: source, Store: db, Validator: candidateValidator(func(context.Context, store.CanonicalState, CoordinatorCandidate) error { validated = true; return nil })}
	err := f.c.RunWithReadiness(context.Background(), nil)
	if !errors.Is(err, ErrTransportClosed) {
		t.Fatalf("loop exit=%v", err)
	}
	if !validated || db.record != 1 || source.acked != 1 {
		t.Fatalf("candidate validation/commit/ack not run: valid=%v records=%d ack=%d", validated, db.record, source.acked)
	}
}

func TestUnsupportedOwnerBoundaryIsNotPublished(t *testing.T) {
	candidateMustReject(t, func(c *CoordinatorCandidate) { c.Decision.OwnerRequired = true }, errors.New("OWNER_BOUNDARY_NOT_PROVEN"))
}
func TestUnsupportedHumanBoundaryIsNotPublished(t *testing.T) {
	candidateMustReject(t, func(c *CoordinatorCandidate) { c.Decision.HumanActionRequired = true }, errors.New("HUMAN_BOUNDARY_NOT_PROVEN"))
}
func TestAvailableAutomationRejectsHumanAction(t *testing.T) {
	candidateMustReject(t, func(c *CoordinatorCandidate) { c.Decision.HumanActionRequired = true }, errors.New("AUTOMATION_PATH_AVAILABLE"))
}
func TestAvailableReplacementRejectsHumanAction(t *testing.T) {
	candidateMustReject(t, func(c *CoordinatorCandidate) { c.Decision.HumanActionRequired = true }, errors.New("ELIGIBLE_REPLACEMENT_AVAILABLE"))
}
func TestExternalBlockerDoesNotBecomeOwnerRequired(t *testing.T) {
	c := validCandidate()
	c.Decision.BlockedExternal = true
	db := &candidateStore{}
	p := &CoordinatorCandidateProcessor{Source: &candidateSource{candidate: c, ok: true}, Store: db, Validator: candidateValidator(func(context.Context, store.CanonicalState, CoordinatorCandidate) error { return nil })}
	if err := p.ProcessNext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if db.record != 1 || db.last.Boundary != task.CompletionExternalBoundary {
		t.Fatalf("completion=%+v", db.last)
	}
}
func TestDeterministicTransitionMustBeExecuted(t *testing.T) {
	candidateMustReject(t, func(c *CoordinatorCandidate) {
		c.Result.NextTransition = c.Transition
		c.Result.TransitionExecuted = false
	}, errors.New("DETERMINISTIC_TRANSITION_NOT_EXECUTED"))
}
func TestInvalidResultCreatesNoCanonicalEvent(t *testing.T) {
	candidateMustReject(t, nil, errors.New("invalid"))
}
func TestInvalidResultCreatesNoExternalAction(t *testing.T) {
	candidateMustReject(t, nil, errors.New("invalid"))
}
func TestValidResultContinuesToCanonicalTransition(t *testing.T) {
	c := validCandidate()
	db := &candidateStore{}
	pub := &candidatePublisher{}
	p := &CoordinatorCandidateProcessor{Source: &candidateSource{candidate: c, ok: true}, Store: db, Validator: candidateValidator(func(context.Context, store.CanonicalState, CoordinatorCandidate) error { return nil }), Publisher: pub}
	if err := p.ProcessNext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if db.record != 1 || pub.published != 1 || db.last.Transition != c.Transition {
		t.Fatalf("record=%d publish=%d completion=%+v", db.record, pub.published, db.last)
	}
}

func TestPublisherRunsOnlyAfterCanonicalCommit(t *testing.T) {
	db := &candidateStore{err: errors.New("commit failed")}
	pub := &candidatePublisher{}
	source := &candidateSource{candidate: validCandidate(), ok: true}
	p := &CoordinatorCandidateProcessor{Source: source, Store: db, Validator: candidateValidator(func(context.Context, store.CanonicalState, CoordinatorCandidate) error { return nil }), Publisher: pub}
	if err := p.ProcessNext(context.Background()); err == nil {
		t.Fatal("failed canonical commit accepted")
	}
	if pub.published != 0 || source.acked != 0 {
		t.Fatalf("published/acked before commit: published=%d ack=%d", pub.published, source.acked)
	}
}

func TestPublisherRetryReusesCommittedCoordinatorCompletion(t *testing.T) {
	db := &candidateStore{}
	source := &candidateSource{candidate: validCandidate(), ok: true}
	calls := 0
	publisher := &candidatePublisher{err: errors.New("publisher unavailable")}
	validator := candidateValidator(func(context.Context, store.CanonicalState, CoordinatorCandidate) error { calls++; return nil })
	p := &CoordinatorCandidateProcessor{Source: source, Store: db, Validator: validator, Publisher: publisher}
	if err := p.ProcessNext(context.Background()); err == nil {
		t.Fatal("publisher failure was hidden")
	}
	if db.record != 1 || source.acked != 0 || calls != 1 {
		t.Fatalf("first try commit=%d ack=%d validations=%d", db.record, source.acked, calls)
	}
	publisher.err = nil
	if err := p.ProcessNext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if db.record != 1 || source.acked != 1 || calls != 1 || publisher.published != 2 {
		t.Fatalf("retry duplicated completion or revalidated stale revision: records=%d ack=%d validations=%d publications=%d", db.record, source.acked, calls, publisher.published)
	}
}

func TestStrictCompletionRejectsUnprovenOwnerBoundary(t *testing.T) {
	v, state, c := strictCandidateFixture(t, false)
	if err := v.Validate(context.Background(), state, c); !errors.Is(err, actionmap.ErrOwnerBoundaryNotProven) {
		t.Fatalf("unproven OWNER boundary result=%v", err)
	}
}

func TestStrictCompletionRejectsHumanActionWhenAutomationExists(t *testing.T) {
	v, state, c := strictCandidateFixture(t, true)
	if err := v.Validate(context.Background(), state, c); !errors.Is(err, actionmap.ErrAutomationPathAvailable) {
		t.Fatalf("available automation result=%v", err)
	}
}

func strictCandidateFixture(t *testing.T, human bool) (StrictCoordinatorCandidateValidator, store.CanonicalState, CoordinatorCandidate) {
	t.Helper()
	read := func(name string) []byte {
		b, err := os.ReadFile(filepath.Join("docs/operations", name))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	var am actionmap.ActionMap
	if err := yaml.Unmarshal(read("ACTION-MAP.yaml"), &am); err != nil {
		t.Fatal(err)
	}
	catalog, err := actionmap.LoadWorkflowCatalog(strings.NewReader(string(read("COORDINATOR-WORKFLOWS.yaml"))), am)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := actionmap.LoadCompletionPolicy(strings.NewReader(string(read("COORDINATOR-COMPLETION-POLICY.yaml"))), catalog, am)
	if err != nil {
		t.Fatal(err)
	}
	id := taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	subject := task.SubjectBinding{RepositoryID: "naghuale/coord", CommitSHA: strings.Repeat("a", 40), TreeHash: strings.Repeat("b", 40)}
	state := store.EmptyCanonicalState()
	state.Tasks[id] = task.Task{ID: id, Revision: 1}
	state.Runs["run"] = fsm.Run{TaskID: id, RunID: "run", TargetSHA: subject.CommitSHA}
	subjectCopy := subject
	state.Events = []task.DomainEvent{{TaskID: id, ReviewResult: &task.ReviewResult{Subject: &subjectCopy}}}
	c := validCandidate()
	c.TaskID = id
	c.Subject = subject
	c.Transition = "ADVANCE_DISPATCHED_WORK"
	c.Result.NextTransition = c.Transition
	ctx := actionmap.CompletionContext{Now: time.Unix(200, 0).UTC(), Facts: actionmap.Facts{}}
	d := actionmap.CompletionDecision{ObservationID: "proof", PendingActionID: "action", PendingAssignmentID: "assignment", SubjectSHA: subject.CommitSHA}
	if human {
		d.HumanActionRequired = true
		d.ReasonCode = "SYSTEM_DIALOG_REQUIRED"
		d.HumanAction = actionmap.HumanActionCard{Command: "approved-client action", WorkingDirectory: "/workspace", ExpectedResult: "done", PostChecks: []string{"observe receipt"}, FailureTransition: "BLOCKED_EXTERNAL", Continuation: "reconcile"}
		ctx.Observations = map[string]actionmap.BoundaryObservation{"proof": {ID: "proof", Source: "CANONICAL_OBSERVATION", Digest: strings.Repeat("c", 64), ActionID: "action", AssignmentID: "assignment", SubjectSHA: subject.CommitSHA, ObservedAt: ctx.Now.Add(-time.Second), ValidUntil: ctx.Now.Add(time.Minute), ReasonCode: d.ReasonCode, EvidenceRefs: []string{"evidence"}, DecisionAlreadyAccepted: true, OwnerBoundaryAbsent: true, Automation: actionmap.AvailabilityAvailable, Replacement: actionmap.AvailabilityUnavailable, InternalRemedy: actionmap.AvailabilityUnavailable, PhysicalActionRequired: true}}
		ctx.Evidence = map[string]actionmap.BoundaryEvidence{"evidence": {ID: "evidence", Source: "CANONICAL_EVENT", Digest: strings.Repeat("d", 64), ActionID: "action", AssignmentID: "assignment", SubjectSHA: subject.CommitSHA}}
		ctx.Facts["automation.available"] = actionmap.BoolValue(true)
		ctx.Facts["replacement.eligible"] = actionmap.BoolValue(false)
		c.Decision = d
		c.Result.HumanActionRequired = true
	} else {
		d.OwnerRequired = true
		d.ReasonCode = "NEW_NORM_REQUIRED"
		c.Decision = d
		c.Result.OwnerRequired = true
	}
	v := StrictCoordinatorCandidateValidator{ActionMap: am, WorkflowCatalog: catalog, CompletionPolicy: policy, Now: func() time.Time { return ctx.Now }, Resolve: func(context.Context, store.CanonicalState, CoordinatorCandidate) (actionmap.CompletionContext, watchdog.CoordinatorState, error) {
		return ctx, watchdog.CoordinatorState{}, nil
	}}
	return v, state, c
}

func candidateMustReject(t *testing.T, mutate func(*CoordinatorCandidate), want error) {
	t.Helper()
	c := validCandidate()
	if mutate != nil {
		mutate(&c)
	}
	if want != nil && want.Error() == "invalid" {
		c.Decision.OwnerRequired = true
	}
	db := &candidateStore{}
	pub := &candidatePublisher{}
	validator := candidateValidator(func(context.Context, store.CanonicalState, CoordinatorCandidate) error { return want })
	p := &CoordinatorCandidateProcessor{Source: &candidateSource{candidate: c, ok: true}, Store: db, Validator: validator, Publisher: pub}
	err := p.ProcessNext(context.Background())
	if err == nil {
		t.Fatal("invalid candidate accepted")
	}
	if db.record != 0 || pub.published != 0 {
		t.Fatalf("invalid candidate had side effects: record=%d publish=%d", db.record, pub.published)
	}
	if source, ok := p.Source.(*candidateSource); !ok || source.rejected != 1 {
		t.Fatalf("invalid candidate was not quarantined: %#v", p.Source)
	}
}
