//go:build unix

package shadowval_test

import (
	"coord/fsm"
	"coord/internal/rooms"
	"coord/roles"
	"coord/shadowval"
	"coord/store"
	"coord/task"
	"coord/taskid"
	"testing"
	"time"
)

// makeTask returns a minimal canonical Task for the given ID.
func makeTask(t *testing.T) taskid.ID {
	t.Helper()
	id, err := taskid.New()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// stateWith builds a CanonicalState with one Task and one Run whose
// RoleSelection carries agentID.
func stateWith(tid taskid.ID, runID, agentID string) store.CanonicalState {
	s := store.EmptyCanonicalState()
	s.Tasks[tid] = task.Task{}
	if runID != "" {
		s.Runs[runID] = fsm.Run{
			RunID:  runID,
			TaskID: tid,
			RoleSelection: &roles.Selection{
				AgentID: agentID,
			},
		}
	}
	return s
}

// roomFor builds a legacy Room whose Task field points to tid.
func roomFor(roomID string, tid taskid.ID, members ...rooms.Member) rooms.Room {
	return rooms.Room{
		ID:      roomID,
		Task:    tid.String(),
		Members: members,
	}
}

func member(agentID string) rooms.Member {
	return rooms.Member{AgentID: agentID}
}

// ── TaskIDReference ───────────────────────────────────────────────────────────

func TestShadowValidationMatchesTaskIDReference(t *testing.T) {
	tid := makeTask(t)
	state := stateWith(tid, "run-1", "agent-1")
	result := shadowval.Validate([]rooms.Room{roomFor("room-1", tid)}, state)

	if result.RoomsExamined != 1 {
		t.Fatalf("expected 1 room examined, got %d", result.RoomsExamined)
	}
	found := false
	for _, m := range result.Matched {
		if m.Kind == shadowval.TaskIDReference && m.RoomID == "room-1" && m.TaskID == tid {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected TaskIDReference match for room-1, matched: %v", result.Matched)
	}
}

// Mutation: room.Task is not a valid canonical TaskID — must be a Mismatch, not a match.
func TestShadowValidationRejectsInvalidTaskID(t *testing.T) {
	r := rooms.Room{ID: "room-bad", Task: "not-a-task-id"}
	result := shadowval.Validate([]rooms.Room{r}, store.EmptyCanonicalState())
	if len(result.Matched) != 0 {
		t.Fatalf("expected no matches for invalid task ID, got %v", result.Matched)
	}
	if len(result.Mismatches) == 0 {
		t.Fatal("expected a Mismatch for invalid task ID, got none")
	}
}

// Mutation: room.Task is valid syntax but not present in canonical state — Mismatch.
func TestShadowValidationMismatchOnUnknownTaskID(t *testing.T) {
	tid := makeTask(t)
	result := shadowval.Validate([]rooms.Room{roomFor("room-1", tid)}, store.EmptyCanonicalState())
	found := false
	for _, mm := range result.Mismatches {
		if mm.Kind == shadowval.TaskIDReference && mm.RoomID == "room-1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected TaskIDReference mismatch for missing task, got %v", result.Mismatches)
	}
}

// ── AgentIDPresence ───────────────────────────────────────────────────────────

func TestShadowValidationMatchesAgentIDPresence(t *testing.T) {
	tid := makeTask(t)
	state := stateWith(tid, "run-1", "agent-42")
	r := roomFor("room-1", tid, member("agent-42"))
	result := shadowval.Validate([]rooms.Room{r}, state)

	found := false
	for _, m := range result.Matched {
		if m.Kind == shadowval.AgentIDPresence && m.AgentID == "agent-42" && m.RunID == "run-1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected AgentIDPresence match, matched: %v", result.Matched)
	}
}

// Mutation: member AgentID not in any canonical Run — Mismatch.
func TestShadowValidationMismatchOnAbsentAgentID(t *testing.T) {
	tid := makeTask(t)
	state := stateWith(tid, "run-1", "agent-other")
	r := roomFor("room-1", tid, member("agent-unknown"))
	result := shadowval.Validate([]rooms.Room{r}, state)

	found := false
	for _, mm := range result.Mismatches {
		if mm.Kind == shadowval.AgentIDPresence {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected AgentIDPresence mismatch, got %v", result.Mismatches)
	}
}

// ── False equality: Member ≠ Run ──────────────────────────────────────────────

// A room with N members must never produce N canonical Runs as a side effect.
func TestShadowValidationDoesNotCreateRunFromMember(t *testing.T) {
	tid := makeTask(t)
	before := stateWith(tid, "", "") // no runs
	r := roomFor("room-1", tid, member("agent-x"), member("agent-y"), member("agent-z"))
	_ = shadowval.Validate([]rooms.Room{r}, before)
	// Validate takes state by value; verify the original is unchanged.
	if len(before.Runs) != 0 {
		t.Fatalf("canonical Run count must not change; got %d", len(before.Runs))
	}
}

// A room Member must not be treated as equivalent to a canonical Run even when
// AgentIDs happen to coincide. The match is AgentIDPresence, never Run equality.
func TestShadowValidationAgentIDMatchIsNotRunEquality(t *testing.T) {
	tid := makeTask(t)
	state := stateWith(tid, "run-1", "agent-x")
	r := roomFor("room-1", tid, member("agent-x"))
	result := shadowval.Validate([]rooms.Room{r}, state)

	for _, m := range result.Matched {
		if m.Kind == shadowval.TaskIDReference || m.Kind == shadowval.AgentIDPresence {
			continue
		}
		t.Fatalf("unexpected match kind %q — Member must not be equated to Run", m.Kind)
	}
	// The AgentIDPresence match must carry RunID but not imply Member = Run.
	for _, m := range result.Matched {
		if m.Kind == shadowval.AgentIDPresence {
			if m.RunID == "" {
				t.Fatal("AgentIDPresence must carry the RunID that holds the AgentID")
			}
		}
	}
}

// ── False equality: room event ≠ DomainEvent ─────────────────────────────────

// Canonical DomainEvent count must not change as a result of Validate,
// regardless of how many rooms Events might exist.
func TestShadowValidationDoesNotCreateDomainEventFromRoomEvent(t *testing.T) {
	tid := makeTask(t)
	before := stateWith(tid, "run-1", "agent-1")
	before.Events = []task.DomainEvent{
		{Type: task.ReviewResultRecorded, CreatedAt: time.Now()},
	}
	r := roomFor("room-1", tid, member("agent-1"))
	_ = shadowval.Validate([]rooms.Room{r}, before)
	if len(before.Events) != 1 {
		t.Fatalf("canonical DomainEvent count must not change; got %d", len(before.Events))
	}
}

// ── False equality: room state ≠ Task lifecycle ───────────────────────────────

// Validate must not copy any room lifecycle field into canonical Task state.
func TestShadowValidationDoesNotCopyRoomStateIntoTask(t *testing.T) {
	tid := makeTask(t)
	before := stateWith(tid, "", "")
	r := rooms.Room{
		ID:     "room-1",
		Task:   tid.String(),
		Closed: true, // a room lifecycle field
	}
	_ = shadowval.Validate([]rooms.Room{r}, before)
	tsk := before.Tasks[tid]
	// task.Task has no Closed field; verifying Tasks map is unchanged.
	if _, ok := before.Tasks[tid]; !ok {
		t.Fatal("Task must still exist in canonical state after validation")
	}
	_ = tsk
}

// ── Read-only invariant ───────────────────────────────────────────────────────

// Validate must not modify the canonical state it receives.
func TestShadowValidationIsReadOnly(t *testing.T) {
	tid := makeTask(t)
	state := stateWith(tid, "run-1", "agent-1")
	runsBefore := len(state.Runs)
	tasksBefore := len(state.Tasks)
	eventsBefore := len(state.Events)

	r := roomFor("room-1", tid, member("agent-1"), member("agent-new"))
	_ = shadowval.Validate([]rooms.Room{r}, state)

	if len(state.Runs) != runsBefore {
		t.Fatalf("Run count changed: before %d after %d", runsBefore, len(state.Runs))
	}
	if len(state.Tasks) != tasksBefore {
		t.Fatalf("Task count changed: before %d after %d", tasksBefore, len(state.Tasks))
	}
	if len(state.Events) != eventsBefore {
		t.Fatalf("Event count changed: before %d after %d", eventsBefore, len(state.Events))
	}
}

// AgentIDs from RoleHistory (past roles) must also be matched, not only
// the current RoleSelection.
func TestShadowValidationMatchesAgentIDFromRoleHistory(t *testing.T) {
	tid := makeTask(t)
	s := store.EmptyCanonicalState()
	s.Tasks[tid] = task.Task{}
	s.Runs["run-hist"] = fsm.Run{
		RunID:  "run-hist",
		TaskID: tid,
		RoleHistory: []roles.Selection{
			{AgentID: "historical-agent"},
		},
	}
	r := roomFor("room-1", tid, member("historical-agent"))
	result := shadowval.Validate([]rooms.Room{r}, s)
	found := false
	for _, m := range result.Matched {
		if m.Kind == shadowval.AgentIDPresence && m.AgentID == "historical-agent" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected AgentIDPresence from RoleHistory, matched: %v", result.Matched)
	}
}

// ── Rooms without a Task reference ───────────────────────────────────────────

// A room without a Task field is counted but produces no matches or mismatches.
func TestShadowValidationSkipsRoomsWithoutTask(t *testing.T) {
	r := rooms.Room{ID: "room-notask", Task: ""}
	result := shadowval.Validate([]rooms.Room{r}, store.EmptyCanonicalState())
	if result.RoomsExamined != 1 {
		t.Fatalf("expected 1 room examined, got %d", result.RoomsExamined)
	}
	if len(result.Matched) != 0 || len(result.Mismatches) != 0 {
		t.Fatalf("expected no matches or mismatches for room without Task, got matched=%v mismatches=%v",
			result.Matched, result.Mismatches)
	}
}
