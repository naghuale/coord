package orchestration

import (
	"context"
	coord "coord"
	"coord/agentsession"
	"coord/checkpoint"
	"coord/envelope"
	"coord/fsm"
	"coord/internal/externalaction"
	"coord/internal/projection"
	"coord/roles"
	"coord/store"
	"coord/task"
	"coord/taskid"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const sha = "0123456789012345678901234567890123456789"

func fixture(t *testing.T) (*store.CanonicalStore, checkpoint.CanonicalFile, externalaction.Action, *time.Time) {
	t.Helper()
	now := time.Unix(1760000000, 0).UTC()
	created := now.Add(-time.Second)
	id := taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	stage := task.Execution
	s := store.EmptyCanonicalState()
	s.Tasks[id] = task.Task{ID: id, Title: "orchestration fixture", OwnerID: "owner", Revision: 2, LastEventSeq: 2, StagePlan: task.StagePlan{Stages: []task.Stage{task.Execution}, PolicyVersion: task.StagePlanPolicyVersion}, CurrentStage: &stage, Status: task.InProgress, Fence: 1, CreatedAt: created, UpdatedAt: now}
	key := ed25519.PublicKey(strings.Repeat("k", 32))
	session := "session-old"
	s.AgentSessions[session] = agentsession.Session{Address: session, AgentID: "agent", Generation: 1, PublicKey: key, RegisteredAt: now, UpdatedAt: now}
	s.AgentSessionEvents = []agentsession.Event{{ID: agentsession.EventID(session, 1), Address: session, AgentID: "agent", Sequence: 1, Type: agentsession.Registered, Generation: 1, PublicKey: key, CreatedAt: now}}
	sel := roles.Selection{AgentID: "agent", Session: session, Profile: roles.Profile{Role: roles.Executor}}
	s.Runs["run-1"] = fsm.Run{Task: "task", TaskID: id, RunID: "run-1", Attempt: 1, TargetSHA: sha, OwnerSession: session, AgentSessionGeneration: 1, Fence: 1, Worktree: "worktree", Deadline: now.Add(time.Hour), Status: fsm.Started, RequiredRole: roles.Requirement{Role: roles.Executor}, RoleSelection: &sel, RoleHistory: []roles.Selection{sel}}
	s.Events = []task.DomainEvent{{ID: task.EventID(id, 1), TaskID: id, EventSeq: 1, TaskRevision: 1, Type: task.TaskCreated, ActorID: "owner", CreatedAt: created}, {ID: task.EventID(id, 2), TaskID: id, RunID: "run-1", EventSeq: 2, TaskRevision: 2, TaskFence: 1, Type: task.TaskOwnershipTransferred, CreatedAt: now}}
	file := checkpoint.CanonicalFile{Path: filepath.Join(t.TempDir(), "canonical.json")}
	if err := file.Save(s); err != nil {
		t.Fatal(err)
	}
	st, err := store.NewCanonical(file)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	a := externalaction.New("delivery-1", externalaction.Deliver, externalaction.Binding{ProjectID: "coord", TaskID: string(id), RunID: "run-1", AssignmentID: "assignment-1", SessionID: session, SubjectSHA: sha, Generation: 1, Fence: 1}, "", json.RawMessage(`{"assignment":"assignment-1"}`), now)
	return st, file, a, &now
}

type fakeAdapter struct {
	results        map[string]externalaction.Result
	bodies         map[string]string
	launches       int
	calls          []string
	crash          bool
	unavailable    bool
	observeErr     bool
	proof          bool
	ackErr         bool
	keys           []string
	before         func(externalaction.Action)
	fenceConfirmed bool
	ackCalls       int
}

func fake() *fakeAdapter {
	return &fakeAdapter{results: map[string]externalaction.Result{}, bodies: map[string]string{}, proof: true, fenceConfirmed: true}
}
func (f *fakeAdapter) SourceID() string { return "trusted-fake-supervisor" }
func (f *fakeAdapter) Execute(_ context.Context, a externalaction.Action) (externalaction.Result, error) {
	f.calls = append(f.calls, "execute:"+string(a.Kind))
	f.keys = append(f.keys, a.Key)
	if f.before != nil {
		f.before(a)
	}
	if d, ok := f.bodies[a.Key]; ok && d != a.RequestDigest {
		return externalaction.Result{}, fmt.Errorf("key/body conflict")
	}
	if r, ok := f.results[a.Key]; ok {
		return r, nil
	}
	f.bodies[a.Key] = a.RequestDigest
	r := externalaction.Result{Evidence: "fake-external-record/" + a.OperationInstanceID}
	switch a.Kind {
	case externalaction.Deliver:
		r.Outcome = "DELIVERED"
	case externalaction.Observe:
		if f.unavailable {
			r.Outcome = "UNAVAILABLE"
		} else {
			r.Outcome = "RUNNING"
		}
	case externalaction.Fence:
		if f.fenceConfirmed {
			r.Outcome = "FENCED"
		} else {
			r.Outcome = "DISPATCHED"
		}
	case externalaction.Stop:
		r.Outcome = "STOPPED"
		r.Stopped = f.proof
		r.StopMethod = "SUPERVISOR_CONFIRMED_EXIT"
	case externalaction.Replace:
		r.Outcome = "REPLACEMENT_CREATED"
		f.launches++
	}
	f.results[a.Key] = r
	if f.crash {
		f.crash = false
		return externalaction.Result{}, errors.New("response lost after external effect")
	}
	return r, nil
}
func (f *fakeAdapter) Observe(_ context.Context, a externalaction.Action) (externalaction.Result, error) {
	f.calls = append(f.calls, "observe:"+string(a.Kind))
	f.keys = append(f.keys, a.Key)
	if f.observeErr {
		return externalaction.Result{}, errors.New("external observer unavailable")
	}
	r, ok := f.results[a.Key]
	if !ok {
		return r, errors.New("operation outcome unknown")
	}
	return r, nil
}
func (f *fakeAdapter) Acknowledge(_ context.Context, _ externalaction.Receipt) error {
	f.ackCalls++
	if f.ackErr {
		return errors.New("ack unavailable")
	}
	return nil
}
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func tick(t *testing.T, r *Runtime) { t.Helper(); must(t, r.Tick(context.Background())) }
func snapshot(t *testing.T, s *store.CanonicalStore) store.CanonicalState {
	t.Helper()
	v, e := s.CanonicalSnapshot()
	must(t, e)
	return v
}
func reopen(t *testing.T, s *store.CanonicalStore, f checkpoint.CanonicalFile) *store.CanonicalStore {
	t.Helper()
	must(t, s.Close())
	n, e := store.NewCanonical(f)
	must(t, e)
	t.Cleanup(func() { _ = n.Close() })
	return n
}
func TestCrashBeforeIntentDoesNothing(t *testing.T) {
	s, _, _, now := fixture(t)
	f := fake()
	r := New(s, f, "dispatcher", func() time.Time { return *now })
	tick(t, r)
	if len(f.calls) != 0 {
		t.Fatal("side effect without durable intent")
	}
}
func TestCrashAfterIntentDispatchesOnce(t *testing.T) {
	s, file, a, now := fixture(t)
	must(t, s.EnqueueAction(a))
	s = reopen(t, s, file)
	f := fake()
	r := New(s, f, "dispatcher", func() time.Time { return *now })
	must(t, r.Recover(context.Background()))
	tick(t, r)
	if len(f.calls) != 1 {
		t.Fatalf("calls %v", f.calls)
	}
	if snapshot(t, s).Orchestration.Actions[a.ID].Status != externalaction.Completed {
		t.Fatal("delivery not completed")
	}
}
func TestCrashAfterDispatchBeforeResponseObservesBeforeRetry(t *testing.T) {
	s, file, a, now := fixture(t)
	must(t, s.EnqueueAction(a))
	f := fake()
	f.crash = true
	r := New(s, f, "d1", func() time.Time { return *now })
	tick(t, r)
	s = reopen(t, s, file)
	*now = now.Add(time.Minute)
	r = New(s, f, "d2", func() time.Time { return *now })
	must(t, r.Recover(context.Background()))
	tick(t, r)
	if len(f.calls) != 2 || f.calls[1] != "observe:DELIVER_ASSIGNMENT" {
		t.Fatalf("blind redispatch: %v", f.calls)
	}
}
func TestDispatchingMarkerPrecedesExternalCall(t *testing.T) {
	s, _, a, now := fixture(t)
	must(t, s.EnqueueAction(a))
	claimed, e := s.ClaimAction(a.ID, "d", *now, time.Minute)
	must(t, e)
	started, e := s.StartAction(a.ID, "d", claimed.Claim.Epoch, *now)
	must(t, e)
	if started.Status != externalaction.Dispatching || snapshot(t, s).Orchestration.Actions[a.ID].Status != externalaction.Dispatching {
		t.Fatal("dispatch start not durable")
	}
}
func TestExpiredClaimReconciledBeforeRedispatch(t *testing.T) {
	s, file, a, now := fixture(t)
	must(t, s.EnqueueAction(a))
	c, e := s.ClaimAction(a.ID, "old", *now, time.Second)
	must(t, e)
	_, e = s.StartAction(a.ID, "old", c.Claim.Epoch, *now)
	must(t, e)
	s = reopen(t, s, file)
	*now = now.Add(time.Minute)
	f := fake()
	r := New(s, f, "new", func() time.Time { return *now })
	must(t, r.Recover(context.Background()))
	tick(t, r)
	if len(f.calls) != 1 || f.calls[0] != "observe:DELIVER_ASSIGNMENT" {
		t.Fatalf("blind launch: %v", f.calls)
	}
}
func TestLateClaimResultCannotCompleteNewClaim(t *testing.T) {
	s, _, a, now := fixture(t)
	must(t, s.EnqueueAction(a))
	c, e := s.ClaimAction(a.ID, "old", *now, time.Second)
	must(t, e)
	_, e = s.StartAction(a.ID, "old", c.Claim.Epoch, *now)
	must(t, e)
	*now = now.Add(time.Minute)
	must(t, s.RecoverActions(*now))
	_, e = s.ClaimAction(a.ID, "new", *now, time.Minute)
	must(t, e)
	result := externalaction.Result{Outcome: "DELIVERED", Evidence: "old-result"}
	o := externalaction.Observation{ID: "late", ActionID: a.ID, Source: "trusted-fake-supervisor", ClaimEpoch: c.Claim.Epoch, RequestDigest: a.RequestDigest, Result: result, Digest: externalaction.Digest(result), At: *now}
	if !errors.Is(s.RecordObservation(o), store.ErrActionClaim) {
		t.Fatal("late claim accepted")
	}
	if !snapshot(t, s).Orchestration.Observations[o.ID].Stale {
		t.Fatal("stale evidence lost")
	}
	if s.AcceptObservation(o.ID, *now) == nil {
		t.Fatal("late result completed action")
	}
}
func TestConfirmedFenceAndStopProofPrecedeReplacement(t *testing.T) {
	s, _, a, now := fixture(t)
	must(t, s.EnqueueAction(a))
	f := fake()
	f.unavailable = true
	r := New(s, f, "d", func() time.Time { return *now })
	for i := 0; i < 3; i++ {
		tick(t, r)
	}
	snap := snapshot(t, s)
	if snap.AgentSessions[a.Binding.SessionID].Active() {
		t.Fatal("old generation retains mutation authority")
	}
	if f.launches != 0 {
		t.Fatal("replacement before stop proof")
	}
	tick(t, r)
	if f.launches != 0 {
		t.Fatal("replacement executed before durable intent")
	}
	tick(t, r)
	if f.launches != 1 {
		t.Fatalf("replacement launches=%d", f.launches)
	}
	for _, op := range snapshot(t, s).Orchestration.Actions {
		if op.Kind == externalaction.Replace {
			var req struct {
				Generation uint64 `json:"generation"`
			}
			must(t, json.Unmarshal(op.Request, &req))
			if req.Generation != 2 {
				t.Fatal("replacement did not request next generation")
			}
		}
	}
}
func TestMissingStopProofRejectsReplacement(t *testing.T) {
	s, _, a, now := fixture(t)
	must(t, s.EnqueueAction(a))
	f := fake()
	f.unavailable = true
	f.proof = false
	r := New(s, f, "d", func() time.Time { return *now })
	for i := 0; i < 3; i++ {
		tick(t, r)
	}
	if r.Tick(context.Background()) == nil {
		t.Fatal("missing stop proof accepted")
	}
	if f.launches != 0 {
		t.Fatal("replacement before proof")
	}
}
func TestRetryExhaustionCreatesDurableBlockerAndNoBusyLoop(t *testing.T) {
	s, _, a, now := fixture(t)
	must(t, s.EnqueueAction(a))
	f := fake()
	f.crash = true
	f.observeErr = true
	r := New(s, f, "d", func() time.Time { return *now })
	tick(t, r)
	for i := 0; i < 2; i++ {
		*now = now.Add(time.Minute)
		tick(t, r)
	}
	snap := snapshot(t, s)
	if snap.Orchestration.Actions[a.ID].Status != externalaction.Blocked {
		t.Fatal("retry exhaustion not blocked")
	}
	rev := snap.Revision
	calls := len(f.calls)
	for i := 0; i < 50; i++ {
		tick(t, r)
	}
	if snapshot(t, s).Revision != rev || len(f.calls) != calls {
		t.Fatal("blocked action busy loop")
	}
}
func TestSameKeyDifferentBodyRejected(t *testing.T) {
	s, _, a, _ := fixture(t)
	must(t, s.EnqueueAction(a))
	b := a
	b.Request = json.RawMessage(`{"assignment":"other"}`)
	b.RequestDigest = externalaction.Digest(b.Request)
	if s.EnqueueAction(b) == nil {
		t.Fatal("same key/ID different body accepted")
	}
}
func TestNewObservationInstanceHasDifferentKey(t *testing.T) {
	_, _, a, _ := fixture(t)
	b := a
	b.OperationInstanceID = "observe-second"
	if externalaction.Key(a) == externalaction.Key(b) {
		t.Fatal("new observation reused cache key")
	}
}
func TestCompletionAndReceiptIntentCommitTogether(t *testing.T) {
	s, _, a, now := fixture(t)
	must(t, s.EnqueueAction(a))
	f := fake()
	f.ackErr = true
	r := New(s, f, "d", func() time.Time { return *now })
	tick(t, r)
	snap := snapshot(t, s)
	if snap.Orchestration.Actions[a.ID].Status != externalaction.Completed || len(snap.Inbox) != 1 || len(snap.Orchestration.Receipts) != 1 {
		t.Fatal("acceptance/receipt not atomic")
	}
	for _, receipt := range snap.Orchestration.Receipts {
		if !receipt.AckPending {
			t.Fatal("receipt intent lost after ack failure")
		}
	}
}

func TestUnstartedClaimReclaimSeparatesEpoch(t *testing.T) {
	s, _, a, now := fixture(t)
	must(t, s.EnqueueAction(a))
	oldClaim, err := s.ClaimAction(a.ID, "first-dispatcher", *now, time.Second)
	must(t, err)

	// No dispatch marker or external call was made for this claim. After its
	// lease expires, the next dispatcher must receive a distinct claim epoch.
	*now = now.Add(2 * time.Second)
	newClaim, err := s.ClaimAction(a.ID, "second-dispatcher", *now, time.Minute)
	must(t, err)
	if newClaim.Claim.Epoch <= oldClaim.Claim.Epoch {
		t.Fatalf("reclaimed epoch did not advance: old=%d new=%d", oldClaim.Claim.Epoch, newClaim.Claim.Epoch)
	}

	// The new worker durably starts dispatch, but an observation from the old
	// worker must still be rejected before it can complete the action.
	_, err = s.StartAction(a.ID, "second-dispatcher", newClaim.Claim.Epoch, *now)
	must(t, err)
	oldResult := externalaction.Result{Outcome: "DELIVERED", Evidence: "late-old-claim"}
	oldObservation := externalaction.Observation{
		ID:            "late-unstarted-claim",
		ActionID:      a.ID,
		Source:        "trusted-fake-supervisor",
		ClaimEpoch:    oldClaim.Claim.Epoch,
		RequestDigest: a.RequestDigest,
		Result:        oldResult,
		Digest:        externalaction.Digest(oldResult),
		At:            *now,
	}
	if !errors.Is(s.RecordObservation(oldObservation), store.ErrActionClaim) {
		t.Fatal("observation from unstarted prior claim was accepted")
	}
	if err := s.AcceptObservation(oldObservation.ID, *now); err == nil {
		t.Fatal("old claim completed the action")
	}
}

func TestRecoveryFencesClaimEpochBeforeLateResult(t *testing.T) {
	s, _, a, now := fixture(t)
	must(t, s.EnqueueAction(a))
	oldClaim, err := s.ClaimAction(a.ID, "old-dispatcher", *now, time.Second)
	must(t, err)
	_, err = s.StartAction(a.ID, "old-dispatcher", oldClaim.Claim.Epoch, *now)
	must(t, err)
	oldResultAt := now.Add(500 * time.Millisecond)

	// Recovery fences an in-flight claim before accepting any late result.
	*now = now.Add(2 * time.Second)
	must(t, s.RecoverActions(*now))
	recovered := snapshot(t, s).Orchestration.Actions[a.ID]
	if recovered.Claim.Epoch <= oldClaim.Claim.Epoch {
		t.Fatalf("recovery did not fence old claim epoch: old=%d recovered=%d", oldClaim.Claim.Epoch, recovered.Claim.Epoch)
	}

	result := externalaction.Result{Outcome: "DELIVERED", Evidence: "result-observed-before-recovery"}
	observation := externalaction.Observation{
		ID:            "late-after-recovery",
		ActionID:      a.ID,
		Source:        "trusted-fake-supervisor",
		ClaimEpoch:    oldClaim.Claim.Epoch,
		RequestDigest: a.RequestDigest,
		Result:        result,
		Digest:        externalaction.Digest(result),
		At:            oldResultAt,
	}
	if !errors.Is(s.RecordObservation(observation), store.ErrActionClaim) {
		t.Fatal("late recovery result accepted")
	}
	if err := s.AcceptObservation(observation.ID, *now); err == nil {
		t.Fatal("late recovery result completed the action")
	}
}

func TestSubjectGenerationFenceMismatchRejected(t *testing.T) {
	for _, field := range []string{"subject", "generation", "fence"} {
		t.Run(field, func(t *testing.T) {
			s, _, a, _ := fixture(t)
			switch field {
			case "subject":
				a.Binding.SubjectSHA = strings.Repeat("a", 40)
			case "generation":
				a.Binding.Generation++
			case "fence":
				a.Binding.Fence++
			}
			a.Key = externalaction.Key(a)
			if s.EnqueueAction(a) == nil {
				t.Fatal("stale binding accepted")
			}
		})
	}
}
func TestRestartDoesNotDeclareHealthySessionDead(t *testing.T) {
	s, file, a, now := fixture(t)
	must(t, s.EnqueueAction(a))
	s = reopen(t, s, file)
	f := fake()
	r := New(s, f, "d", func() time.Time { return *now })
	must(t, r.Recover(context.Background()))
	tick(t, r)
	tick(t, r)
	if !snapshot(t, s).AgentSessions[a.Binding.SessionID].Active() || f.launches != 0 {
		t.Fatal("healthy session automatically fenced")
	}
}
func TestProjectionBundleUsesOneRevisionAndRebuilds(t *testing.T) {
	s, _, a, now := fixture(t)
	must(t, s.EnqueueAction(a))
	p := &projection.Writer{Root: filepath.Join(t.TempDir(), "operations")}
	r := New(s, fake(), "d", func() time.Time { return *now })
	r.Projector = p
	tick(t, r)
	snap := snapshot(t, s)
	b, e := os.ReadFile(filepath.Join(p.Root, "current.json"))
	must(t, e)
	var m projection.Manifest
	must(t, json.Unmarshal(b, &m))
	if m.Revision != snap.Revision {
		t.Fatal("projection revision skew")
	}
	dir := filepath.Join(p.Root, "revisions", fmt.Sprint(m.Revision))
	for _, name := range []string{"CURRENT.yaml", "OPEN-WORK.yaml", "LEDGER.yaml"} {
		b, e := os.ReadFile(filepath.Join(dir, name))
		must(t, e)
		var v struct {
			SourceRevision uint64 `json:"sourceRevision"`
		}
		must(t, json.Unmarshal(b, &v))
		if v.SourceRevision != m.Revision {
			t.Fatal("mixed projection revisions")
		}
	}
	must(t, os.Remove(filepath.Join(dir, "OPEN-WORK.yaml")))
	must(t, p.Publish(snap))
	if _, e := os.Stat(filepath.Join(dir, "OPEN-WORK.yaml")); e != nil {
		t.Fatal("missing projection not rebuilt")
	}
	older := snap
	older.Revision--
	must(t, p.Publish(older))
	b, e = os.ReadFile(filepath.Join(p.Root, "current.json"))
	must(t, e)
	must(t, json.Unmarshal(b, &m))
	if m.Revision != snap.Revision {
		t.Fatal("old job regressed pointer")
	}
}

func TestExpiredDispatchRecoveredWithoutDaemonRestart(t *testing.T) {
	s, _, a, now := fixture(t)
	must(t, s.EnqueueAction(a))
	c, e := s.ClaimAction(a.ID, "crashed-worker", *now, time.Second)
	must(t, e)
	_, e = s.StartAction(a.ID, "crashed-worker", c.Claim.Epoch, *now)
	must(t, e)
	*now = now.Add(time.Minute)
	f := fake()
	r := New(s, f, "replacement-dispatcher", func() time.Time { return *now })
	tick(t, r)
	if len(f.calls) != 1 || f.calls[0] != "observe:DELIVER_ASSIGNMENT" {
		t.Fatalf("expired dispatch not observed: %v", f.calls)
	}
}
func TestRestartDoesNotTrustPersistedRunningState(t *testing.T) {
	s, file, a, now := fixture(t)
	must(t, s.EnqueueAction(a))
	f := fake()
	r := New(s, f, "d1", func() time.Time { return *now })
	tick(t, r)
	tick(t, r)
	s = reopen(t, s, file)
	*now = now.Add(time.Minute)
	r = New(s, f, "d2", func() time.Time { return *now })
	must(t, r.Recover(context.Background()))
	snap := snapshot(t, s)
	pending := 0
	keys := map[string]bool{}
	for _, op := range snap.Orchestration.Actions {
		if op.Kind == externalaction.Observe {
			if keys[op.Key] {
				t.Fatal("reused observation key")
			}
			keys[op.Key] = true
			if op.Status == externalaction.Pending {
				pending++
			}
		}
	}
	if pending != 1 {
		t.Fatalf("persisted RUNNING trusted, pending observations=%d", pending)
	}
	if !snap.AgentSessions[a.Binding.SessionID].Active() {
		t.Fatal("healthy session declared dead without observation")
	}
}
func TestDuplicateObservationDoesNotRepeatTransition(t *testing.T) {
	s, _, a, now := fixture(t)
	must(t, s.EnqueueAction(a))
	f := fake()
	r := New(s, f, "d", func() time.Time { return *now })
	tick(t, r)
	snap := snapshot(t, s)
	for id := range snap.Orchestration.Receipts {
		must(t, s.AcceptObservation(id, *now))
	}
	if snapshot(t, s).Revision != snap.Revision {
		t.Fatal("duplicate acceptance produced new transition")
	}
}
func TestProjectionSymlinkEscapeRejected(t *testing.T) {
	s, _, _, _ := fixture(t)
	root := t.TempDir()
	outside := t.TempDir()
	must(t, os.Symlink(outside, filepath.Join(root, "escape")))
	p := &projection.Writer{Root: filepath.Join(root, "escape", "operations")}
	if p.Publish(snapshot(t, s)) == nil {
		t.Fatal("projection escaped namespace through symlink")
	}
}

type failBeforeSave struct {
	checkpoint.CanonicalFile
	fail bool
}

func (f *failBeforeSave) Save(s store.CanonicalState) error {
	if f.fail {
		return errors.New("durable commit rejected")
	}
	return f.CanonicalFile.Save(s)
}
func TestFailedIntentCommitCannotExecuteExternalAction(t *testing.T) {
	s, file, a, now := fixture(t)
	must(t, s.Close())
	p := &failBeforeSave{CanonicalFile: file, fail: true}
	s, e := store.NewCanonical(p)
	must(t, e)
	defer func() {
		if err := s.Close(); err != nil {
			t.Errorf("close canonical store: %v", err)
		}
	}()
	if s.EnqueueAction(a) == nil {
		t.Fatal("expected save failure")
	}
	f := fake()
	r := New(s, f, "d", func() time.Time { return *now })
	_ = r.Tick(context.Background()) // read-only idle ticks may succeed; no side effect is permitted.
	if len(f.calls) != 0 {
		t.Fatal("external side effect before successful intent commit")
	}
}
func TestCrashAfterObservedReplaysCanonicalAcceptance(t *testing.T) {
	s, file, a, now := fixture(t)
	must(t, s.EnqueueAction(a))
	c, e := s.ClaimAction(a.ID, "old", *now, time.Minute)
	must(t, e)
	a, e = s.StartAction(a.ID, "old", c.Claim.Epoch, *now)
	must(t, e)
	f := fake()
	result, e := f.Execute(context.Background(), a)
	must(t, e)
	o := externalaction.Observation{ID: "observed-before-crash", ActionID: a.ID, Source: f.SourceID(), ClaimEpoch: a.Claim.Epoch, RequestDigest: a.RequestDigest, Result: result, Digest: externalaction.Digest(result), At: *now}
	must(t, s.RecordObservation(o))
	s = reopen(t, s, file)
	r := New(s, f, "new", func() time.Time { return *now })
	must(t, r.Recover(context.Background()))
	tick(t, r)
	if len(f.calls) != 1 || snapshot(t, s).Orchestration.Actions[a.ID].Status != externalaction.Completed {
		t.Fatal("observed action re-executed rather than accepted")
	}
}
func TestRetryUsesSameOperationKey(t *testing.T) {
	s, file, a, now := fixture(t)
	must(t, s.EnqueueAction(a))
	c, e := s.ClaimAction(a.ID, "d1", *now, time.Second)
	must(t, e)
	_, e = s.StartAction(a.ID, "d1", c.Claim.Epoch, *now)
	must(t, e)
	s = reopen(t, s, file)
	*now = now.Add(time.Minute)
	must(t, s.RecoverActions(*now))
	c, e = s.ClaimAction(a.ID, "d2", *now, time.Minute)
	must(t, e)
	if c.Key != a.Key || c.OperationInstanceID != a.OperationInstanceID {
		t.Fatal("retry identity changed")
	}
}
func TestUnknownOrFreeTextObservationRejected(t *testing.T) {
	s, _, a, now := fixture(t)
	must(t, s.EnqueueAction(a))
	c, e := s.ClaimAction(a.ID, "d", *now, time.Minute)
	must(t, e)
	_, e = s.StartAction(a.ID, "d", c.Claim.Epoch, *now)
	must(t, e)
	r := externalaction.Result{Outcome: "OWNER_REQUIRED"}
	o := externalaction.Observation{ID: "free-text", ActionID: a.ID, Source: "model", ClaimEpoch: c.Claim.Epoch, RequestDigest: a.RequestDigest, Result: r, Digest: externalaction.Digest(r), At: *now}
	if s.RecordObservation(o) == nil {
		t.Fatal("free text without evidence accepted")
	}
}
func TestOldSnapshotMigrationAndUnknownSchemaFailClosed(t *testing.T) {
	s, file, _, _ := fixture(t)
	snap := snapshot(t, s)
	must(t, s.Close())
	b, e := json.Marshal(snap)
	must(t, e)
	var fields map[string]json.RawMessage
	must(t, json.Unmarshal(b, &fields))
	fields["schema_version"] = json.RawMessage("1")
	delete(fields, "orchestration")
	delete(fields, "revision")
	b, e = json.Marshal(fields)
	must(t, e)
	must(t, os.WriteFile(file.Path, b, 0600))
	s, e = store.NewCanonical(file)
	must(t, e)
	if snapshot(t, s).SchemaVersion != store.CanonicalSchemaVersion {
		t.Fatal("old schema not migrated")
	}
	must(t, s.Close())
	fields["schema_version"] = json.RawMessage("999")
	b, e = json.Marshal(fields)
	must(t, e)
	must(t, os.WriteFile(file.Path, b, 0600))
	if _, e := store.NewCanonical(file); e == nil {
		t.Fatal("unknown schema accepted")
	}
}

type workflowTransport struct{ inbox chan envelope.Envelope }

func (t *workflowTransport) WhoAmI() string                                        { return "coordd" }
func (t *workflowTransport) Send(context.Context, string, envelope.Envelope) error { return nil }
func (t *workflowTransport) Receive(context.Context) <-chan envelope.Envelope      { return t.inbox }
func TestExistingCoordinatorTickExecutesDurableWorkflow(t *testing.T) {
	s, _, a, now := fixture(t)
	must(t, s.EnqueueAction(a))
	f := fake()
	r := New(s, f, "d", func() time.Time { return *now })
	cfg := coord.DefaultConfig()
	cfg.Now = func() time.Time { return *now }
	c, e := coord.New(&workflowTransport{inbox: make(chan envelope.Envelope)}, s, cfg)
	must(t, e)
	c.Workflow = r
	must(t, c.Tick(context.Background()))
	if len(f.calls) != 1 {
		t.Fatal("existing Coordinator.Tick did not execute workflow")
	}
}

func TestCompletionRejectsPendingDeliveryAndFuturePromise(t *testing.T) {
	s, _, a, _ := fixture(t)
	must(t, s.EnqueueAction(a))
	snap := snapshot(t, s)
	if ValidateCompletion(snap, "", false, false) == nil {
		t.Fatal("pending delivery counted as completion")
	}
	empty := store.EmptyCanonicalState()
	if ValidateCompletion(empty, "will receive later", false, false) == nil {
		t.Fatal("passive future promise accepted")
	}
	if ValidateCompletion(empty, "", true, false) == nil {
		t.Fatal("independent work skipped")
	}
}

func TestExternalCallRequiresDurableDispatchingMarker(t *testing.T) {
	s, _, a, now := fixture(t)
	must(t, s.EnqueueAction(a))
	f := fake()
	f.before = func(op externalaction.Action) {
		if snapshot(t, s).Orchestration.Actions[op.ID].Status != externalaction.Dispatching {
			t.Fatal("external call preceded durable DISPATCHING")
		}
	}
	r := New(s, f, "d", func() time.Time { return *now })
	tick(t, r)
}
func TestRuntimeRetryPreservesIdempotencyKey(t *testing.T) {
	s, file, a, now := fixture(t)
	must(t, s.EnqueueAction(a))
	f := fake()
	f.crash = true
	r := New(s, f, "d1", func() time.Time { return *now })
	tick(t, r)
	s = reopen(t, s, file)
	*now = now.Add(time.Minute)
	r = New(s, f, "d2", func() time.Time { return *now })
	must(t, r.Recover(context.Background()))
	tick(t, r)
	if len(f.keys) != 2 || f.keys[0] != f.keys[1] {
		t.Fatalf("retry idempotency key changed: %v", f.keys)
	}
}
func TestFreeTextOwnerReasonCannotBecomeAuthority(t *testing.T) {
	s, _, a, now := fixture(t)
	must(t, s.EnqueueAction(a))
	c, e := s.ClaimAction(a.ID, "d", *now, time.Minute)
	must(t, e)
	a, e = s.StartAction(a.ID, "d", c.Claim.Epoch, *now)
	must(t, e)
	result := externalaction.Result{Outcome: "OWNER_REQUIRED", Evidence: "NEW_NORM_REQUIRED"}
	o := externalaction.Observation{ID: "unsupported-owner", ActionID: a.ID, Source: "trusted-fake-supervisor", ClaimEpoch: a.Claim.Epoch, RequestDigest: a.RequestDigest, Result: result, Digest: externalaction.Digest(result), At: *now}
	must(t, s.RecordObservation(o))
	if s.AcceptObservation(o.ID, *now) == nil {
		t.Fatal("free-text NEW_NORM_REQUIRED became authority")
	}
}

func TestFenceDeliveryIsNotFenceConfirmation(t *testing.T) {
	s, _, a, now := fixture(t)
	must(t, s.EnqueueAction(a))
	f := fake()
	f.unavailable = true
	f.fenceConfirmed = false
	r := New(s, f, "d", func() time.Time { return *now })
	tick(t, r)
	tick(t, r)
	if r.Tick(context.Background()) == nil {
		t.Fatal("fence delivery counted as logical fence confirmation")
	}
	if !snapshot(t, s).AgentSessions[a.Binding.SessionID].Active() {
		t.Fatal("unconfirmed fence revoked authority")
	}
	if f.launches != 0 {
		t.Fatal("replacement before confirmed fence")
	}
}
func TestLegacyTransportCannotBypassOrchestratedRun(t *testing.T) {
	s, _, a, now := fixture(t)
	must(t, s.EnqueueAction(a))
	r := New(s, fake(), "d", func() time.Time { return *now })
	cfg := coord.DefaultConfig()
	cfg.Now = func() time.Time { return *now }
	c, e := coord.New(&workflowTransport{inbox: make(chan envelope.Envelope)}, s, cfg)
	must(t, e)
	c.Workflow = r
	msg := envelope.Envelope{SchemaVersion: 1, MessageID: "old-raw-command", Timestamp: *now, From: a.Binding.SessionID, To: "coordd", Kind: envelope.Ping, Task: a.Binding.TaskID, Run: a.Binding.RunID, Attempt: 1, Fence: 1, TargetSHA: sha, PingPayload: &envelope.PingPayload{Nonce: "probe"}}
	must(t, msg.Validate())
	if err := c.Handle(context.Background(), msg); err == nil || !strings.Contains(err.Error(), "canonical command boundary") {
		t.Fatalf("legacy path bypassed authority: %v", err)
	}
}

func TestAcknowledgementRetriesAreBoundedAndDurable(t *testing.T) {
	s, _, a, now := fixture(t)
	must(t, s.EnqueueAction(a))
	f := fake()
	f.ackErr = true
	r := New(s, f, "d", func() time.Time { return *now })
	tick(t, r)
	tick(t, r)
	for i := 0; i < 6; i++ {
		*now = now.Add(time.Minute)
		tick(t, r)
	}
	snap := snapshot(t, s)
	if f.ackCalls != 6 {
		t.Fatalf("two receipt retry budgets: calls=%d", f.ackCalls)
	}
	for _, receipt := range snap.Orchestration.Receipts {
		if receipt.AckAttempts != 3 || receipt.AckBlocker == "" || !receipt.AckPending {
			t.Fatal("ack failure lost durable blocker")
		}
	}
	rev := snap.Revision
	for i := 0; i < 20; i++ {
		tick(t, r)
	}
	if snapshot(t, s).Revision != rev || f.ackCalls != 6 {
		t.Fatal("acknowledgement busy loop")
	}
}

func TestV2SnapshotCannotSilentlyDropActionCollections(t *testing.T) {
	s, file, _, _ := fixture(t)
	snap := snapshot(t, s)
	must(t, s.Close())
	b, e := json.Marshal(snap)
	must(t, e)
	var fields map[string]json.RawMessage
	must(t, json.Unmarshal(b, &fields))
	delete(fields, "orchestration")
	b, e = json.Marshal(fields)
	must(t, e)
	must(t, os.WriteFile(file.Path, b, 0600))
	if _, e := store.NewCanonical(file); e == nil {
		t.Fatal("v2 missing orchestration silently accepted")
	}
}
