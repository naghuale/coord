package store

import (
	"coord/agentsession"
	"coord/fsm"
	"coord/roles"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCanonicalStartupRequiresGenerationAndBindingForActiveTaskRun(t *testing.T) {
	for _, status := range []fsm.Status{fsm.Started, fsm.Stalled} {
		t.Run(string(status)+" missing generation", func(t *testing.T) {
			state := taskStateWithRun()
			run := state.Runs["run-1"]
			run.AgentSessionGeneration = 0
			if status == fsm.Stalled {
				run.Status = fsm.Stalled
				run.ResumeStatus = fsm.Started
				run.RecoveryNeeded = true
				run.RecoveryClaim = "claim"
			}
			state.Runs[run.RunID] = run
			err := ValidateCanonicalState(state)
			if err == nil || !strings.Contains(err.Error(), "has no AgentSession generation") {
				t.Fatalf("startup validation error=%v, want missing generation", err)
			}
		})

		t.Run(string(status)+" missing binding", func(t *testing.T) {
			state := taskStateWithRun()
			run := state.Runs["run-1"]
			run.AgentSessionGeneration = 1
			if status == fsm.Stalled {
				run.Status = fsm.Stalled
				run.ResumeStatus = fsm.Started
				run.RecoveryNeeded = true
				run.RecoveryClaim = "claim"
			}
			state.Runs[run.RunID] = run
			delete(state.AgentSessions, run.OwnerSession)
			state.AgentSessionEvents = nil
			err := ValidateCanonicalState(state)
			if err == nil || !strings.Contains(err.Error(), "references missing AgentSession") {
				t.Fatalf("startup validation error=%v, want missing binding", err)
			}
		})
	}
}

func TestCanonicalStartupAllowsStaleGenerationAndRevokedExistingBinding(t *testing.T) {
	t.Run("stale generation after key rotation", func(t *testing.T) {
		state := taskStateWithRun()
		rotateCanonicalTestSession(&state, state.Runs["run-1"].OwnerSession)
		if err := ValidateCanonicalState(state); err != nil {
			t.Fatalf("stale Run generation rejected at startup: %v", err)
		}
	})
	t.Run("revoked binding after address replacement", func(t *testing.T) {
		state := taskStateWithRun()
		revokeCanonicalTestSession(&state, state.Runs["run-1"].OwnerSession)
		if err := ValidateCanonicalState(state); err != nil {
			t.Fatalf("existing revoked binding rejected at startup: %v", err)
		}
	})
}

func TestCanonicalStartupRejectsForgedRunGenerationAndAgentID(t *testing.T) {
	t.Run("generation exceeds binding", func(t *testing.T) {
		state := taskStateWithRun()
		run := state.Runs["run-1"]
		run.AgentSessionGeneration = 2
		state.Runs[run.RunID] = run
		if err := ValidateCanonicalState(state); err == nil || !strings.Contains(err.Error(), "generation exceeds current binding generation") {
			t.Fatalf("startup validation error=%v, want forged generation rejection", err)
		}
	})
	t.Run("AgentID differs from registered identity", func(t *testing.T) {
		state := taskStateWithRun()
		run := state.Runs["run-1"]
		run.RoleSelection.AgentID = "agent-forged"
		run.RoleHistory[len(run.RoleHistory)-1].AgentID = "agent-forged"
		state.Runs[run.RunID] = run
		if err := ValidateCanonicalState(state); err == nil || !strings.Contains(err.Error(), "does not match AgentSession") {
			t.Fatalf("startup validation error=%v, want registered AgentID mismatch", err)
		}
	})
}

func TestLegacyRunDoesNotRequireAgentSessionGeneration(t *testing.T) {
	state := EmptyCanonicalState()
	now := time.Unix(5000, 0).UTC()
	state.Runs["legacy-run"] = fsm.Run{RunID: "legacy-run", Attempt: 1, Fence: 1,
		OwnerSession: "legacy-session", Worktree: "worktree", Deadline: now.Add(time.Hour), Status: fsm.Started}
	if err := ValidateCanonicalState(state); err != nil {
		t.Fatalf("legacy Run was affected by Task-scoped binding checks: %v", err)
	}
}

func TestCanonicalAssignmentTakesCurrentActiveAgentSessionBinding(t *testing.T) {
	previous := taskState()
	next := ownershipTransferCandidate(t, previous, "new-run", roles.Executor, 1, 1)
	if err := ValidateCanonicalTransition(previous, next); err != nil {
		t.Fatalf("valid assignment from active AgentSession rejected: %v", err)
	}

	for _, tc := range []struct {
		name   string
		mutate func(*CanonicalState, *fsm.Run)
		want   string
	}{
		{name: "generation omitted", mutate: func(_ *CanonicalState, run *fsm.Run) { run.AgentSessionGeneration = 0 }, want: "must record the current AgentSession generation"},
		{name: "generation is stale", mutate: func(state *CanonicalState, run *fsm.Run) {
			rotateCanonicalTestSession(state, run.OwnerSession)
		}, want: "current AgentSession generation"},
		{name: "binding revoked", mutate: func(state *CanonicalState, run *fsm.Run) {
			revokeCanonicalTestSession(state, run.OwnerSession)
		}, want: "requires an active AgentSession"},
		{name: "AgentID mismatch", mutate: func(_ *CanonicalState, run *fsm.Run) {
			run.RoleSelection.AgentID = "agent-forged"
			run.RoleHistory[len(run.RoleHistory)-1].AgentID = "agent-forged"
		}, want: "does not match AgentSession"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := ownershipTransferCandidate(t, previous, "new-run", roles.Executor, 1, 1)
			run := candidate.Runs["new-run"]
			tc.mutate(&candidate, &run)
			candidate.Runs[run.RunID] = run
			if err := ValidateCanonicalTransition(previous, candidate); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("assignment transition error=%v, want %q", err, tc.want)
			}
		})
	}
}

func TestCanonicalRunGenerationCannotChangeWithoutRecoveryTransfer(t *testing.T) {
	previous := taskStateWithRun()
	next, err := copyCanonicalState(previous)
	if err != nil {
		t.Fatal(err)
	}
	run := next.Runs["run-1"]
	rotateCanonicalTestSession(&next, run.OwnerSession)
	run.AgentSessionGeneration = 2
	next.Runs[run.RunID] = run
	appendTaskDomainEvent(&next, run.TaskID, run.RunID, "RUN_GENERATION_CHANGED")
	err = ValidateCanonicalTransition(previous, next)
	if err == nil || !strings.Contains(err.Error(), "can change only with ownership transfer") {
		t.Fatalf("same-owner generation update error=%v, want recovery-transfer requirement", err)
	}
}

func TestCanonicalRecoveryTransferChangesAgentWithOwnerAndHistory(t *testing.T) {
	previous := claimedRecoveryState()
	next := ownershipTransferCandidate(t, previous, "run-1", roles.Executor, 1, 2)
	run := next.Runs["run-1"]
	selection := *run.RoleSelection
	selection.AgentID = "agent-recovered"
	selection.Session = "recovery-session"
	run.OwnerSession = selection.Session
	run.AgentSessionGeneration = 1
	run.RoleSelection = &selection
	run.RoleHistory = append(run.RoleHistory, selection)
	run.Fence = 2
	run.Status = fsm.Pending
	run.ResumeStatus = ""
	run.RecoveryNeeded = false
	run.RecoveryClaim = ""
	ensureCanonicalTestAgentSession(&next, selection.Session, selection.AgentID, next.Tasks[run.TaskID].UpdatedAt)
	next.Runs[run.RunID] = run
	if err := ValidateCanonicalTransition(previous, next); err != nil {
		t.Fatalf("recovery transfer with new AgentSession binding rejected: %v", err)
	}
}

func TestCanonicalRecoveryTransferRequiresCurrentActiveAgentSession(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*CanonicalState, string)
		want   string
	}{
		{
			name: "revoked binding",
			mutate: func(state *CanonicalState, address string) {
				revokeCanonicalTestSession(state, address)
			},
			want: "requires an active AgentSession",
		},
		{
			name: "stale generation",
			mutate: func(state *CanonicalState, address string) {
				rotateCanonicalTestSession(state, address)
			},
			want: "must record the current AgentSession generation",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			previous := claimedRecoveryState()
			persistence := &canonicalMemory{state: previous}
			store, err := NewCanonical(persistence)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = store.Close() }()
			beforeStore, err := copyCanonicalState(store.state)
			if err != nil {
				t.Fatal(err)
			}

			next := ownershipTransferCandidate(t, previous, "run-1", roles.Executor, 1, 2)
			run := next.Runs["run-1"]
			selection := *run.RoleSelection
			selection.AgentID = "agent-recovered"
			selection.Session = "recovery-session"
			run.OwnerSession = selection.Session
			run.AgentSessionGeneration = 1
			run.RoleSelection = &selection
			run.RoleHistory = append(run.RoleHistory, selection)
			run.Fence = 2
			run.Status = fsm.Pending
			run.ResumeStatus = ""
			run.RecoveryNeeded = false
			run.RecoveryClaim = ""
			ensureCanonicalTestAgentSession(&next, selection.Session, selection.AgentID, next.Tasks[run.TaskID].UpdatedAt)
			tc.mutate(&next, selection.Session)
			next.Runs[run.RunID] = run

			err = store.UpdateCanonical(func(state *CanonicalState) error {
				*state = next
				return nil
			})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("recovery transfer error=%v, want %q", err, tc.want)
			}
			if persistence.saveCalls != 0 {
				t.Fatalf("rejected recovery transfer performed %d durable saves, want none", persistence.saveCalls)
			}
			if !reflect.DeepEqual(store.state, beforeStore) || !reflect.DeepEqual(persistence.state, previous) {
				t.Fatal("rejected recovery transfer changed in-memory or persisted state")
			}
		})
	}
}

func TestCanonicalRecoveryTransferCannotChangeAgentWithoutRoleHistoryAppend(t *testing.T) {
	previous := claimedRecoveryState()
	next := ownershipTransferCandidate(t, previous, "run-1", roles.Executor, 1, 2)
	run := next.Runs["run-1"]
	selection := *run.RoleSelection
	selection.AgentID = "agent-recovered"
	selection.Session = "recovery-session"
	run.OwnerSession = selection.Session
	run.AgentSessionGeneration = 1
	run.RoleSelection = &selection
	run.Fence = 2
	run.Status = fsm.Pending
	run.ResumeStatus = ""
	run.RecoveryNeeded = false
	run.RecoveryClaim = ""
	ensureCanonicalTestAgentSession(&next, selection.Session, selection.AgentID, next.Tasks[run.TaskID].UpdatedAt)
	next.Runs[run.RunID] = run
	err := ValidateCanonicalTransition(previous, next)
	if err == nil || !strings.Contains(err.Error(), "RoleSelection change requires the same selection appended to RoleHistory") {
		t.Fatalf("recovery AgentID change error=%v, want append-only identity history", err)
	}
}

func rotateCanonicalTestSession(state *CanonicalState, address string) {
	session := state.AgentSessions[address]
	now := session.UpdatedAt.Add(time.Second)
	key := append([]byte(nil), session.PublicKey...)
	key[0] ^= 0xff
	event := agentsession.Event{ID: agentsession.EventID(address, 2), Address: address, AgentID: session.AgentID,
		Sequence: 2, Type: agentsession.KeyRotated, Generation: session.Generation + 1, PublicKey: key, CreatedAt: now}
	session.Generation++
	session.PublicKey = key
	session.UpdatedAt = now
	state.AgentSessions[address] = session
	state.AgentSessionEvents = append(state.AgentSessionEvents, event)
}

func revokeCanonicalTestSession(state *CanonicalState, address string) {
	session := state.AgentSessions[address]
	now := session.UpdatedAt.Add(time.Second)
	state.AgentSessionEvents = append(state.AgentSessionEvents, agentsession.Event{ID: agentsession.EventID(address, 2),
		Address: address, AgentID: session.AgentID, Sequence: 2, Type: agentsession.Revoked,
		Generation: session.Generation, Reason: "test revocation", CreatedAt: now})
	session.RevokedAt = now
	session.RevocationReason = "test revocation"
	session.UpdatedAt = now
	state.AgentSessions[address] = session
}

func TestFSMRunDomainStateIncludesAgentSessionGeneration(t *testing.T) {
	base := fsm.Run{RunID: "run", AgentSessionGeneration: 1}
	changed := base
	changed.AgentSessionGeneration++
	if base.SameDomainState(changed) {
		t.Fatal("AgentSession generation change was omitted from Run domain state")
	}
}
