package store

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"coord/agentsession"
	"coord/fsm"
	"coord/internal/externalaction"
	"coord/roles"
	"coord/task"
)

func recoveryTestStore(t *testing.T) (*CanonicalStore, *canonicalMemory, RunRecoveryCommand) {
	t.Helper()
	state := taskStateWithRoleRun(roles.Executor, 1)
	run := state.Runs["run-1"]
	run.TargetSHA = strings.Repeat("a", 40)
	run.Status = fsm.Stalled
	run.ResumeStatus = fsm.Started
	run.RecoveryNeeded = true
	run.RecoveryClaim = "recovery-claim-1"
	bindCanonicalTestRun(&state, &run, "agent-executor", state.Tasks[run.TaskID].UpdatedAt)
	state.Runs[run.RunID] = run
	state.Revision = 2

	revokedAt := state.Tasks[run.TaskID].UpdatedAt.Add(time.Second)
	session := state.AgentSessions[run.OwnerSession]
	session.RevokedAt = revokedAt
	session.UpdatedAt = revokedAt
	session.RevocationReason = "confirmed supervisor fence"
	state.AgentSessions[run.OwnerSession] = session
	state.AgentSessionEvents = append(state.AgentSessionEvents, agentsession.Event{
		ID: agentsession.EventID(run.OwnerSession, 2), Address: run.OwnerSession,
		AgentID: session.AgentID, Sequence: 2, Type: agentsession.Revoked,
		Generation: session.Generation, Reason: session.RevocationReason, CreatedAt: revokedAt,
	})

	binding := externalaction.Binding{
		ProjectID: "coord-test", TaskID: run.TaskID.String(), RunID: run.RunID,
		AssignmentID: "assignment-1", SessionID: run.OwnerSession,
		SubjectSHA: run.TargetSHA, Generation: run.AgentSessionGeneration, Fence: run.Fence,
	}
	base := state.Tasks[run.TaskID].UpdatedAt
	addRecoveryTestAction(&state, "deliver-1", externalaction.Deliver, "", binding, "DELIVERED", base)
	addRecoveryTestAction(&state, "observe-1", externalaction.Observe, "deliver-1", binding, "UNAVAILABLE", base)
	addRecoveryTestAction(&state, "fence-1", externalaction.Fence, "observe-1", binding, "FENCED", base)
	addRecoveryTestAction(&state, "stop-1", externalaction.Stop, "fence-1", binding, "STOPPED", base)

	state = ownerEnrolledCanonicalState(t, state)
	p := &canonicalMemory{state: state}
	clock := &canonicalTestClock{now: base.Add(2 * time.Second)}
	s := newCanonicalWithTestClock(t, p, clock)
	cmd := RunRecoveryCommand{
		MessageID: "recovery-message-1", TaskID: run.TaskID, RunID: run.RunID,
		ExpectedRevision:   state.Tasks[run.TaskID].Revision,
		ExpectedGeneration: run.AgentSessionGeneration, ReasonCode: "SUPERVISOR_CONFIRMED_STOP",
		StopProofEvidenceID: "stop-1",
		AllowedTransitions:  []RunRecoveryTransition{RunRecoveryMarkInterrupted},
	}
	return s, p, cmd
}

func addRecoveryTestAction(state *CanonicalState, id string, kind externalaction.Kind, parent string,
	binding externalaction.Binding, outcome string, at time.Time) {
	request, _ := json.Marshal(map[string]string{"assignment": binding.AssignmentID})
	action := externalaction.New(id, kind, binding, parent, request, at)
	action.Status = externalaction.Completed
	action.Claim = externalaction.Claim{DispatcherID: "test-dispatcher", Epoch: 1, Deadline: at.Add(time.Hour)}
	result := externalaction.Result{Outcome: outcome, Evidence: "trusted adapter result"}
	if kind == externalaction.Stop {
		result.Stopped = true
		result.StopMethod = "SUPERVISOR_CONFIRMED_EXIT"
	}
	action.Result = &result
	state.Orchestration.Actions[id] = action
	observationID := id + "/observation"
	observation := externalaction.Observation{
		ID: observationID, ActionID: id, Source: "test-supervisor", ClaimEpoch: action.Claim.Epoch,
		RequestDigest: action.RequestDigest, Result: result, Digest: externalaction.Digest(result), At: at,
	}
	state.Orchestration.Observations[observationID] = observation
	state.Orchestration.Receipts[observationID] = externalaction.Receipt{
		ObservationID: observationID, ActionID: id, Digest: observation.Digest, Revision: state.Revision,
	}
	state.Orchestration.Events = append(state.Orchestration.Events, externalaction.Event{
		ActionID: id, Revision: state.Revision, Digest: externalaction.Digest(action), At: at,
	})
}

func TestApplyRunRecoveryMarksInterruptedAtomically(t *testing.T) {
	s, p, cmd := recoveryTestStore(t)
	beforeTask := s.state.Tasks[cmd.TaskID]
	beforeRun := s.state.Runs[cmd.RunID]
	beforeSaves := p.saveCalls

	receipt, replayed, err := s.ApplyRunRecoveryCommand(cmd)
	if err != nil || replayed || receipt.Replayed {
		t.Fatalf("receipt=%+v replayed=%v err=%v", receipt, replayed, err)
	}
	if p.saveCalls != beforeSaves+1 {
		t.Fatalf("durable saves=%d, want exactly one additional snapshot", p.saveCalls-beforeSaves)
	}
	if got := s.state.Tasks[cmd.TaskID]; got.Revision != beforeTask.Revision+1 || got.LastEventSeq != beforeTask.LastEventSeq+3 ||
		got.Fence != beforeTask.Fence || got.Status != task.Blocked {
		t.Fatalf("Task commit=%+v, previous=%+v", got, beforeTask)
	}
	if got := s.state.Runs[cmd.RunID]; got.Status != fsm.Failed || got.Fence != beforeRun.Fence ||
		got.OwnerSession != beforeRun.OwnerSession || got.RecoveryNeeded || got.RecoveryClaim != "" ||
		got.Blocker != cmd.ReasonCode {
		t.Fatalf("Run commit=%+v", got)
	}
	if receipt.SelectedTransition != RunRecoveryMarkInterrupted || receipt.PreviousRevision != cmd.ExpectedRevision ||
		receipt.CommittedRevision != cmd.ExpectedRevision+1 || receipt.StopProofEvidenceID != cmd.StopProofEvidenceID ||
		len(receipt.EventIDs) != 3 {
		t.Fatalf("incomplete receipt: %+v", receipt)
	}
	wantTypes := []task.EventType{RunRecoveryRequestedEvent, RunStopProofRecordedEvent, RunRecoveryBlockedEvent}
	for i, id := range receipt.EventIDs {
		event := s.state.Events[len(s.state.Events)-len(receipt.EventIDs)+i]
		if event.ID != id || event.Type != wantTypes[i] || event.TaskRevision != receipt.CommittedRevision ||
			event.TaskFence != beforeTask.Fence || event.RunID != cmd.RunID || event.ActorID != runRecoveryActor {
			t.Fatalf("event %d=%+v", i, event)
		}
		identity, ok := decodeRunRecoveryIdentity(event.Summary)
		if !ok || identity.MessageID != cmd.MessageID || identity.StopProofEvidenceID != cmd.StopProofEvidenceID ||
			identity.SelectedTransition != RunRecoveryMarkInterrupted {
			t.Fatalf("event %d does not retain command identity: %+v ok=%v", i, identity, ok)
		}
	}
	key := InboxKey(runRecoveryActor, cmd.MessageID)
	stored, ok := s.state.Inbox[key]
	if !ok || stored.RunID != cmd.RunID || !reflect.DeepEqual(stored.EventIDs, receipt.EventIDs) {
		t.Fatalf("Inbox receipt=%+v exists=%v", stored, ok)
	}
	if len(s.state.Outbox[cmd.RunID].Messages) != 0 {
		t.Fatalf("recovery unexpectedly changed Outbox: %+v", s.state.Outbox[cmd.RunID])
	}
}

func TestApplyRunRecoveryExactReplayReturnsOriginalReceiptWithoutWrites(t *testing.T) {
	s, p, cmd := recoveryTestStore(t)
	first, replayed, err := s.ApplyRunRecoveryCommand(cmd)
	if err != nil || replayed {
		t.Fatalf("first receipt=%+v replayed=%v err=%v", first, replayed, err)
	}
	beforeMemory, _ := copyCanonicalState(s.state)
	beforeDisk, _ := copyCanonicalState(p.state)
	saves := p.saveCalls
	second, replayed, err := s.ApplyRunRecoveryCommand(cmd)
	if err != nil || !replayed || !second.Replayed {
		t.Fatalf("replay receipt=%+v replayed=%v err=%v", second, replayed, err)
	}
	if p.saveCalls != saves || !reflect.DeepEqual(s.state, beforeMemory) || !reflect.DeepEqual(p.state, beforeDisk) {
		t.Fatal("exact replay performed a state mutation or durable save")
	}
	second.Replayed = false
	first.Replayed = false
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("replay changed recorded receipt: first=%+v second=%+v", first, second)
	}
}

func TestApplyRunRecoveryConflictingReplayDoesNotReturnCachedSuccess(t *testing.T) {
	s, p, cmd := recoveryTestStore(t)
	if _, _, err := s.ApplyRunRecoveryCommand(cmd); err != nil {
		t.Fatal(err)
	}
	before, _ := copyCanonicalState(s.state)
	saves := p.saveCalls
	cmd.ReasonCode = "DIFFERENT_INTENT"
	if _, _, err := s.ApplyRunRecoveryCommand(cmd); !errors.Is(err, ErrRunRecoveryDuplicateCommandConflict) {
		t.Fatalf("conflicting replay error=%v", err)
	}
	if p.saveCalls != saves || !reflect.DeepEqual(before, s.state) {
		t.Fatal("conflicting replay mutated canonical state")
	}
}

func TestRunRecoveryGuardCodesAndPrecedence(t *testing.T) {
	tests := []struct {
		name string
		want error
		edit func(*CanonicalStore, *RunRecoveryCommand)
	}{
		{"revision conflict precedes later guards", ErrRunRecoveryRevisionConflict, func(_ *CanonicalStore, cmd *RunRecoveryCommand) { cmd.ExpectedRevision-- }},
		{"revision conflict precedes authority check", ErrRunRecoveryRevisionConflict, func(_ *CanonicalStore, cmd *RunRecoveryCommand) {
			cmd.ExpectedRevision--
			// The fixture's owner session is already revoked, so both predicates fail.
		}},
		{"generation mismatch", ErrRunRecoveryGenerationMismatch, func(_ *CanonicalStore, cmd *RunRecoveryCommand) { cmd.ExpectedGeneration++ }},
		{"missing evidence reference", ErrRunRecoveryObservationMissing, func(_ *CanonicalStore, cmd *RunRecoveryCommand) { cmd.StopProofEvidenceID = "" }},
		{"missing action", ErrRunRecoveryObservationMissing, func(_ *CanonicalStore, cmd *RunRecoveryCommand) { cmd.StopProofEvidenceID = "missing-stop" }},
		{"FENCE_ONLY is never a successful transition", ErrRunRecoveryFenceNotCommitted, func(_ *CanonicalStore, cmd *RunRecoveryCommand) {
			cmd.AllowedTransitions = []RunRecoveryTransition{RunRecoveryFenceOnly}
		}},
		{"active same-generation session", ErrRunRecoveryAuthorityNotProven, func(s *CanonicalStore, _ *RunRecoveryCommand) {
			run := s.state.Runs["run-1"]
			session := s.state.AgentSessions[run.OwnerSession]
			session.RevokedAt = time.Time{}
			s.state.AgentSessions[run.OwnerSession] = session
		}},
		{"claim absent means fence not committed", ErrRunRecoveryFenceNotCommitted, func(s *CanonicalStore, _ *RunRecoveryCommand) {
			run := s.state.Runs["run-1"]
			run.RecoveryNeeded = false
			run.RecoveryClaim = ""
			s.state.Runs["run-1"] = run
		}},
		{"fence-not-committed precedes stop-proof check", ErrRunRecoveryFenceNotCommitted, func(s *CanonicalStore, cmd *RunRecoveryCommand) {
			run := s.state.Runs["run-1"]
			run.RecoveryClaim = ""
			s.state.Runs["run-1"] = run
			cmd.StopProofEvidenceID = ""
		}},
		{"wrong action kind", ErrRunRecoveryStopProofInsufficient, func(s *CanonicalStore, _ *RunRecoveryCommand) {
			action := s.state.Orchestration.Actions["stop-1"]
			action.Kind = externalaction.Observe
			s.state.Orchestration.Actions["stop-1"] = action
		}},
		{"OBSERVED does not satisfy KSC-C", ErrRunRecoveryObservationMissing, func(s *CanonicalStore, _ *RunRecoveryCommand) {
			action := s.state.Orchestration.Actions["stop-1"]
			action.Status = externalaction.Observed
			s.state.Orchestration.Actions["stop-1"] = action
		}},
		{"DISPATCHED does not satisfy terminal proof guard", ErrRunRecoveryObservationMissing, func(s *CanonicalStore, _ *RunRecoveryCommand) {
			action := s.state.Orchestration.Actions["stop-1"]
			action.Status = externalaction.Dispatched
			s.state.Orchestration.Actions["stop-1"] = action
		}},
		{"non-Stop completed action", ErrRunRecoveryStopProofInsufficient, func(s *CanonicalStore, _ *RunRecoveryCommand) {
			action := s.state.Orchestration.Actions["stop-1"]
			action.Kind = externalaction.Observe
			s.state.Orchestration.Actions["stop-1"] = action
		}},
		{"missing recovery claim", ErrRunRecoveryFenceNotCommitted, func(s *CanonicalStore, _ *RunRecoveryCommand) {
			run := s.state.Runs["run-1"]
			run.RecoveryClaim = ""
			s.state.Runs["run-1"] = run
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s, p, cmd := recoveryTestStore(t)
			test.edit(s, &cmd)
			before, _ := copyCanonicalState(s.state)
			diskBefore, _ := copyCanonicalState(p.state)
			saves := p.saveCalls
			if _, _, err := s.ApplyRunRecoveryCommand(cmd); !errors.Is(err, test.want) {
				t.Fatalf("error=%v, want %v", err, test.want)
			}
			if p.saveCalls != saves || !reflect.DeepEqual(before, s.state) || !reflect.DeepEqual(diskBefore, p.state) {
				t.Fatal("rejected recovery command changed canonical state or persisted a snapshot")
			}
		})
	}
}

func TestRunRecoveryStopProofMutationMatrixFailsClosed(t *testing.T) {
	tests := []struct {
		name   string
		mutant func(*CanonicalStore)
	}{
		{"subject SHA binding (direct)", func(s *CanonicalStore) {
			mutateStopAndFenceBinding(s, func(b *externalaction.Binding) { b.SubjectSHA = strings.Repeat("b", 40) })
		}},
		{"generation binding (direct)", func(s *CanonicalStore) {
			mutateStopAndFenceBinding(s, func(b *externalaction.Binding) { b.Generation++ })
		}},
		{"fence binding (direct)", func(s *CanonicalStore) {
			mutateStopAndFenceBinding(s, func(b *externalaction.Binding) { b.Fence++ })
		}},
		{"session binding (direct)", func(s *CanonicalStore) {
			mutateStopAndFenceBinding(s, func(b *externalaction.Binding) { b.SessionID = "other-session" })
		}},
		{"task binding (direct)", func(s *CanonicalStore) {
			mutateStopAndFenceBinding(s, func(b *externalaction.Binding) { b.TaskID = "other-task" })
		}},
		{"run binding (direct)", func(s *CanonicalStore) {
			mutateStopAndFenceBinding(s, func(b *externalaction.Binding) { b.RunID = "other-run" })
		}},
		{"session binding", func(s *CanonicalStore) {
			mutateStopBinding(s, func(b *externalaction.Binding) { b.SessionID = "other" })
		}},
		{"task binding", func(s *CanonicalStore) {
			mutateStopBinding(s, func(b *externalaction.Binding) { b.TaskID = "other-task" })
		}},
		{"run binding", func(s *CanonicalStore) {
			mutateStopBinding(s, func(b *externalaction.Binding) { b.RunID = "other-run" })
		}},
		{"subject binding", func(s *CanonicalStore) {
			mutateStopBinding(s, func(b *externalaction.Binding) { b.SubjectSHA = strings.Repeat("b", 40) })
		}},
		{"generation binding", func(s *CanonicalStore) { mutateStopBinding(s, func(b *externalaction.Binding) { b.Generation++ }) }},
		{"fence binding", func(s *CanonicalStore) { mutateStopBinding(s, func(b *externalaction.Binding) { b.Fence++ }) }},
		{"assignment binding", func(s *CanonicalStore) {
			mutateStopBinding(s, func(b *externalaction.Binding) { b.AssignmentID = "other-assignment" })
		}},
		{"claim epoch", func(s *CanonicalStore) {
			o := s.state.Orchestration.Observations["stop-1/observation"]
			o.ClaimEpoch++
			s.state.Orchestration.Observations[o.ID] = o
		}},
		{"request digest", func(s *CanonicalStore) {
			o := s.state.Orchestration.Observations["stop-1/observation"]
			o.RequestDigest = "wrong-digest"
			s.state.Orchestration.Observations[o.ID] = o
		}},
		{"result digest", func(s *CanonicalStore) {
			o := s.state.Orchestration.Observations["stop-1/observation"]
			o.Digest = "wrong-digest"
			s.state.Orchestration.Observations[o.ID] = o
		}},
		{"stale observation", func(s *CanonicalStore) {
			o := s.state.Orchestration.Observations["stop-1/observation"]
			o.Stale = true
			s.state.Orchestration.Observations[o.ID] = o
		}},
		{"receipt missing", func(s *CanonicalStore) { delete(s.state.Orchestration.Receipts, "stop-1/observation") }},
		{"outcome predicate", func(s *CanonicalStore) {
			mutateStopResultConsistent(s, func(r *externalaction.Result) { r.Outcome = "RUNNING" })
		}},
		{"stopped flag predicate", func(s *CanonicalStore) {
			mutateStopResultConsistent(s, func(r *externalaction.Result) { r.Stopped = false })
		}},
		{"stop method", func(s *CanonicalStore) {
			mutateStopResultConsistent(s, func(r *externalaction.Result) { r.StopMethod = "EXIT_CODE_ONLY" })
		}},
		{"parent Fence not completed", func(s *CanonicalStore) {
			mutateFenceAction(s, func(a *externalaction.Action) { a.Status = externalaction.Pending })
		}},
		{"parent Fence outcome not fenced", func(s *CanonicalStore) {
			mutateFenceAction(s, func(a *externalaction.Action) { a.Result.Outcome = "FAILED" })
		}},
		{"failed Stop", func(s *CanonicalStore) { mutateStopStatus(s, externalaction.Failed) }},
		{"blocked Stop", func(s *CanonicalStore) { mutateStopStatus(s, externalaction.Blocked) }},
		{"fenced Stop", func(s *CanonicalStore) { mutateStopStatus(s, externalaction.Fenced) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s, p, cmd := recoveryTestStore(t)
			test.mutant(s)
			before, _ := copyCanonicalState(s.state)
			saves := p.saveCalls
			if _, _, err := s.ApplyRunRecoveryCommand(cmd); !errors.Is(err, ErrRunRecoveryStopProofInsufficient) {
				t.Fatalf("error=%v, want STOP_PROOF_INSUFFICIENT", err)
			}
			if p.saveCalls != saves || !reflect.DeepEqual(before, s.state) {
				t.Fatal("invalid stop proof mutated recovery state")
			}
		})
	}
}

func mutateStopBinding(s *CanonicalStore, mutate func(*externalaction.Binding)) {
	action := s.state.Orchestration.Actions["stop-1"]
	mutate(&action.Binding)
	action.Key = externalaction.Key(action)
	s.state.Orchestration.Actions["stop-1"] = action
}

func mutateStopAndFenceBinding(s *CanonicalStore, mutate func(*externalaction.Binding)) {
	for _, id := range []string{"stop-1", "fence-1"} {
		action := s.state.Orchestration.Actions[id]
		mutate(&action.Binding)
		action.Key = externalaction.Key(action)
		s.state.Orchestration.Actions[id] = action
	}
}

func mutateStopResult(s *CanonicalStore, mutate func(*externalaction.Result)) {
	action := s.state.Orchestration.Actions["stop-1"]
	mutate(action.Result)
	s.state.Orchestration.Actions["stop-1"] = action
}

func mutateStopResultConsistent(s *CanonicalStore, mutate func(*externalaction.Result)) {
	mutateStopResult(s, mutate)
	action := s.state.Orchestration.Actions["stop-1"]
	observationID := "stop-1/observation"
	observation := s.state.Orchestration.Observations[observationID]
	observation.Result = *action.Result
	observation.Digest = externalaction.Digest(*action.Result)
	s.state.Orchestration.Observations[observationID] = observation
	receipt := s.state.Orchestration.Receipts[observationID]
	receipt.Digest = observation.Digest
	s.state.Orchestration.Receipts[observationID] = receipt
}

func mutateFenceAction(s *CanonicalStore, mutate func(*externalaction.Action)) {
	action := s.state.Orchestration.Actions["fence-1"]
	mutate(&action)
	s.state.Orchestration.Actions["fence-1"] = action
}

func mutateStopStatus(s *CanonicalStore, status externalaction.Status) {
	action := s.state.Orchestration.Actions["stop-1"]
	action.Status = status
	s.state.Orchestration.Actions["stop-1"] = action
}

func TestRunRecoveryFailClosedTransitionsHaveNoSideEffects(t *testing.T) {
	tests := []struct {
		transition RunRecoveryTransition
		want       error
	}{
		{RunRecoveryFenceOnly, ErrRunRecoveryFenceNotCommitted},
		{RunRecoveryPrepareReplacement, ErrRunRecoveryTransitionForbidden},
		{RunRecoveryCompleteExisting, ErrRunRecoveryTransitionForbidden},
		{RunRecoveryResumeObservation, ErrRunRecoveryTransitionForbidden},
	}
	for _, test := range tests {
		t.Run(string(test.transition), func(t *testing.T) {
			s, p, cmd := recoveryTestStore(t)
			cmd.AllowedTransitions = []RunRecoveryTransition{test.transition}
			before, _ := copyCanonicalState(s.state)
			diskBefore, _ := copyCanonicalState(p.state)
			saves := p.saveCalls
			if _, _, err := s.ApplyRunRecoveryCommand(cmd); !errors.Is(err, test.want) {
				t.Fatalf("error=%v, want %v", err, test.want)
			}
			if p.saveCalls != saves || !reflect.DeepEqual(before, s.state) || !reflect.DeepEqual(diskBefore, p.state) {
				t.Fatal("fail-closed transition changed canonical state")
			}
		})
	}
}

func TestRunRecoveryAllowedTransitionsAreAnUnorderedSet(t *testing.T) {
	a := []RunRecoveryTransition{RunRecoveryPrepareReplacement, RunRecoveryMarkInterrupted}
	b := []RunRecoveryTransition{RunRecoveryMarkInterrupted, RunRecoveryPrepareReplacement}
	na, err := normalizeRunRecoveryTransitions(a)
	if err != nil {
		t.Fatal(err)
	}
	nb, err := normalizeRunRecoveryTransitions(b)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(na, nb) {
		t.Fatalf("set normalization depends on input order: %v vs %v", na, nb)
	}
	selected, err := selectRunRecoveryTransition(na)
	if err != nil || selected != RunRecoveryMarkInterrupted {
		t.Fatalf("selected=%q err=%v", selected, err)
	}
}

func TestRunRecoveryMultipleFailClosedTransitionsUseForbiddenCode(t *testing.T) {
	s, p, cmd := recoveryTestStore(t)
	cmd.AllowedTransitions = []RunRecoveryTransition{RunRecoveryPrepareReplacement, RunRecoveryCompleteExisting}
	before, _ := copyCanonicalState(s.state)
	diskBefore, _ := copyCanonicalState(p.state)
	saves := p.saveCalls
	if _, _, err := s.ApplyRunRecoveryCommand(cmd); !errors.Is(err, ErrRunRecoveryTransitionForbidden) {
		t.Fatalf("error=%v, want RUN_RECOVERY_TRANSITION_FORBIDDEN", err)
	}
	if p.saveCalls != saves || !reflect.DeepEqual(before, s.state) || !reflect.DeepEqual(diskBefore, p.state) {
		t.Fatal("unsupported transition set changed canonical state")
	}
}

func TestRunRecoveryDuplicateTransitionRejected(t *testing.T) {
	s, p, cmd := recoveryTestStore(t)
	cmd.AllowedTransitions = []RunRecoveryTransition{RunRecoveryMarkInterrupted, RunRecoveryMarkInterrupted}
	before, _ := copyCanonicalState(s.state)
	diskBefore, _ := copyCanonicalState(p.state)
	saves := p.saveCalls
	if _, _, err := s.ApplyRunRecoveryCommand(cmd); !errors.Is(err, ErrRunRecoveryTransitionForbidden) {
		t.Fatalf("error=%v, want RUN_RECOVERY_TRANSITION_FORBIDDEN", err)
	}
	if p.saveCalls != saves || !reflect.DeepEqual(before, s.state) || !reflect.DeepEqual(diskBefore, p.state) {
		t.Fatal("duplicate transition input mutated canonical state")
	}
}

func TestRunRecoveryNormalizerRejectsDuplicateTransition(t *testing.T) {
	transitions, err := normalizeRunRecoveryTransitions([]RunRecoveryTransition{
		RunRecoveryMarkInterrupted, RunRecoveryMarkInterrupted,
	})
	if !errors.Is(err, ErrRunRecoveryTransitionForbidden) {
		t.Fatalf("err=%v, want ErrRunRecoveryTransitionForbidden", err)
	}
	if transitions != nil {
		t.Fatalf("transitions=%v, want nil on duplicate rejection", transitions)
	}
}

func TestRunRecoveryRequiresReasonCode(t *testing.T) {
	s, p, cmd := recoveryTestStore(t)
	cmd.ReasonCode = ""
	before, _ := copyCanonicalState(s.state)
	saves := p.saveCalls
	if _, _, err := s.ApplyRunRecoveryCommand(cmd); !errors.Is(err, ErrRunRecoveryTransitionForbidden) {
		t.Fatalf("empty reason error=%v", err)
	}
	if p.saveCalls != saves || !reflect.DeepEqual(before, s.state) {
		t.Fatal("invalid command mutated state")
	}
}

func TestRunRecoveryReceiptDoesNotPersistReturnMetadata(t *testing.T) {
	s, _, cmd := recoveryTestStore(t)
	receipt, _, err := s.ApplyRunRecoveryCommand(cmd)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Replayed {
		t.Fatal("fresh receipt unexpectedly marked replayed")
	}
	for _, stored := range s.state.Inbox {
		if stored.MessageID == cmd.MessageID {
			data, err := json.Marshal(stored)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(data), "replayed") || strings.Contains(string(data), "selected_transition") {
				t.Fatalf("return metadata or projection fields persisted in common InboxReceipt: %s", data)
			}
			return
		}
	}
	t.Fatalf("Inbox receipt for %q missing", cmd.MessageID)
}

func TestRunRecoveryPublicEntryMapsAllSevenAcceptedCodes(t *testing.T) {
	cases := []struct {
		name string
		want error
		edit func(*CanonicalStore, *RunRecoveryCommand)
	}{
		{"observation missing", ErrRunRecoveryObservationMissing, func(_ *CanonicalStore, c *RunRecoveryCommand) { c.StopProofEvidenceID = "" }},
		{"generation mismatch", ErrRunRecoveryGenerationMismatch, func(_ *CanonicalStore, c *RunRecoveryCommand) { c.ExpectedGeneration++ }},
		{"revision conflict", ErrRunRecoveryRevisionConflict, func(_ *CanonicalStore, c *RunRecoveryCommand) { c.ExpectedRevision-- }},
		{"stop proof insufficient", ErrRunRecoveryStopProofInsufficient, func(s *CanonicalStore, _ *RunRecoveryCommand) {
			mutateStopResult(s, func(r *externalaction.Result) { r.Stopped = false })
		}},
		{"fence not committed", ErrRunRecoveryFenceNotCommitted, func(_ *CanonicalStore, c *RunRecoveryCommand) {
			c.AllowedTransitions = []RunRecoveryTransition{RunRecoveryFenceOnly}
		}},
		{"authority not proven", ErrRunRecoveryAuthorityNotProven, func(s *CanonicalStore, _ *RunRecoveryCommand) {
			run := s.state.Runs["run-1"]
			session := s.state.AgentSessions[run.OwnerSession]
			session.RevokedAt = time.Time{}
			s.state.AgentSessions[run.OwnerSession] = session
		}},
		{"duplicate command conflict", ErrRunRecoveryDuplicateCommandConflict, func(_ *CanonicalStore, c *RunRecoveryCommand) { c.ReasonCode = "changed" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, p, cmd := recoveryTestStore(t)
			if tc.name == "duplicate command conflict" {
				if _, _, err := s.ApplyRunRecoveryCommand(cmd); err != nil {
					t.Fatal(err)
				}
			}
			tc.edit(s, &cmd)
			before, _ := copyCanonicalState(s.state)
			saves := p.saveCalls
			if _, _, err := s.ApplyRunRecoveryCommand(cmd); !errors.Is(err, tc.want) {
				t.Fatalf("error=%v, want %v", err, tc.want)
			}
			if p.saveCalls != saves || !reflect.DeepEqual(before, s.state) {
				t.Fatal("guard failure changed state")
			}
		})
	}
}
