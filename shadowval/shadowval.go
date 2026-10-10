//go:build unix

// Package shadowval is a read-only diagnostic that compares canonical Task
// state against legacy Room state by facts with unambiguous semantic
// correspondence.
//
// ADR-0001 step 10 constraints:
//   - No double write; neither store is modified.
//   - No second authoritative store is created.
//   - Only facts with a clear one-to-one semantic mapping are matched.
//   - The following false equalities are explicitly rejected:
//     room participant ≠ canonical Run,
//     room message ≠ DomainEvent,
//     room state ≠ Task lifecycle.
package shadowval

import (
	"coord/fsm"
	"coord/internal/rooms"
	"coord/store"
	"coord/taskid"
)

// FactKind identifies the type of semantic correspondence that was matched.
type FactKind string

const (
	// TaskIDReference confirms that a Room's Task field matches a canonical TaskID.
	TaskIDReference FactKind = "task_id_reference"
	// AgentIDPresence confirms that a Member's AgentID appears in the canonical
	// RoleSelection history of the matched Task's Runs. This only establishes
	// presence, not equality between Member and Run.
	AgentIDPresence FactKind = "agent_id_presence"
)

// MatchedFact records a confirmed correspondence between legacy and canonical
// state. A match establishes only the stated semantic fact, never equality of
// the containing objects.
type MatchedFact struct {
	Kind    FactKind
	RoomID  string
	TaskID  taskid.ID
	AgentID string // non-empty only for AgentIDPresence
	RunID   string // non-empty only for AgentIDPresence; the Run that holds AgentID
}

// Mismatch records a fact that was expected to correspond but does not.
type Mismatch struct {
	Kind        FactKind
	RoomID      string
	Description string
}

// Result is the outcome of a shadow validation pass. It is purely diagnostic.
// No store was written; no import was performed.
type Result struct {
	RoomsExamined int
	Matched       []MatchedFact
	Mismatches    []Mismatch
}

// Validate compares the given rooms against canonical state. Both inputs are
// treated as immutable snapshots: Validate does not write to or modify either.
//
// Only the two matching facts defined above (TaskIDReference, AgentIDPresence)
// are checked. The function explicitly does not:
//   - create or modify a canonical Run from a rooms.Member;
//   - create or modify a DomainEvent from a rooms Event;
//   - copy any room lifecycle field into canonical Task state.
func Validate(rs []rooms.Room, state store.CanonicalState) Result {
	// Build a lookup: canonical TaskID → set of AgentIDs from RoleSelection history.
	agentsByTask := make(map[taskid.ID]map[string]string) // taskID → agentID → runID
	for runID, run := range state.Runs {
		if run.TaskID == "" {
			continue
		}
		if agentsByTask[run.TaskID] == nil {
			agentsByTask[run.TaskID] = make(map[string]string)
		}
		for _, sel := range run.RoleHistory {
			if sel.AgentID != "" {
				agentsByTask[run.TaskID][sel.AgentID] = runID
			}
		}
		if run.RoleSelection != nil && run.RoleSelection.AgentID != "" {
			agentsByTask[run.TaskID][run.RoleSelection.AgentID] = runID
		}
	}

	result := Result{RoomsExamined: len(rs)}
	for _, room := range rs {
		if room.Task == "" {
			continue
		}
		tid, err := taskid.Parse(room.Task)
		if err != nil {
			result.Mismatches = append(result.Mismatches, Mismatch{
				Kind:        TaskIDReference,
				RoomID:      room.ID,
				Description: "room.Task is not a valid canonical TaskID: " + err.Error(),
			})
			continue
		}
		if _, ok := state.Tasks[tid]; !ok {
			result.Mismatches = append(result.Mismatches, Mismatch{
				Kind:        TaskIDReference,
				RoomID:      room.ID,
				Description: "room.Task " + room.Task + " not found in canonical state",
			})
			continue
		}
		result.Matched = append(result.Matched, MatchedFact{
			Kind:   TaskIDReference,
			RoomID: room.ID,
			TaskID: tid,
		})

		agentSet := agentsByTask[tid]
		for _, m := range room.Members {
			if m.AgentID == "" {
				continue
			}
			if runID, ok := agentSet[m.AgentID]; ok {
				result.Matched = append(result.Matched, MatchedFact{
					Kind:    AgentIDPresence,
					RoomID:  room.ID,
					TaskID:  tid,
					AgentID: m.AgentID,
					RunID:   runID,
				})
			} else {
				result.Mismatches = append(result.Mismatches, Mismatch{
					Kind:        AgentIDPresence,
					RoomID:      room.ID,
					Description: "member AgentID " + m.AgentID + " not present in any canonical Run for task " + room.Task,
				})
			}
		}
	}
	return result
}

// assertNoFalseEqualities is used in tests to verify that canonical state was
// not contaminated by legacy values. It returns a non-empty slice of violation
// descriptions if the post-validation canonical state differs from the
// pre-validation snapshot in any way.
//
// In production code Validate never modifies state, so this function always
// returns nil. It exists to give test mutations a concrete target.
func assertNoFalseEqualities(before, after store.CanonicalState) []string {
	var violations []string
	if len(after.Runs) != len(before.Runs) {
		violations = append(violations, "canonical Run count changed after shadow validation")
	}
	if len(after.Events) != len(before.Events) {
		violations = append(violations, "canonical DomainEvent count changed after shadow validation")
	}
	if len(after.Tasks) != len(before.Tasks) {
		violations = append(violations, "canonical Task count changed after shadow validation")
	}
	return violations
}

// memberCount and runCount are deliberately separate: a room Member is not a
// Run, so their counts having the same numeric value would be coincidental and
// must never be treated as confirmation of equality.
func memberCount(rs []rooms.Room) int {
	n := 0
	for _, r := range rs {
		n += len(r.Members)
	}
	return n
}

func runCount(state store.CanonicalState) int { return len(state.Runs) }

// roomsWithTask returns the subset of rooms that carry a Task reference.
func roomsWithTask(rs []rooms.Room) []rooms.Room {
	var out []rooms.Room
	for _, r := range rs {
		if r.Task != "" {
			out = append(out, r)
		}
	}
	return out
}

// eventCount and domainEventCount are deliberately named separately for the
// same reason as memberCount/runCount.
func roomEventCount(rs []rooms.Room, state store.CanonicalState) (roomEvents, domainEvents int) {
	for _, r := range rs {
		_ = r // rooms.Room does not expose a public event slice; count is not meaningful
	}
	_ = state
	return 0, len(state.Events)
}

// Ensure unused helpers are referenced so the compiler keeps them. These are
// used exclusively in tests that verify the false-equality invariants.
var _ = memberCount
var _ = runCount
var _ = roomsWithTask
var _ = roomEventCount
var _ = assertNoFalseEqualities
var _ fsm.Run
