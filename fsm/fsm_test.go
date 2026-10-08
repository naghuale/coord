package fsm

import (
	"coord/envelope"
	"testing"
	"time"
)

func TestHeartbeatTransitions(t *testing.T) {
	for _, from := range []Status{Pending, Accepted, Started, Blocked, ResultReady, Done, Failed} {
		for _, to := range []Status{Started, Blocked, ResultReady} {
			t.Run(string(from)+"/"+string(to), func(t *testing.T) {
				r := Run{Status: from}
				e := envelope.Envelope{Kind: envelope.Heartbeat, HeartbeatPayload: &envelope.HeartbeatPayload{State: string(to)}}
				err := r.Apply(e, time.Now())
				allowed := from == Accepted || from == Started || from == Blocked || (from == ResultReady && to == ResultReady)
				if (err == nil) != allowed {
					t.Fatalf("allowed=%v err=%v", allowed, err)
				}
				if !allowed && r.Status != from {
					t.Fatal("invalid transition mutated state")
				}
			})
		}
	}
}

func TestRunValidateRejectsPersistedFSMInvariantViolations(t *testing.T) {
	now := time.Now().UTC()
	valid := Run{
		RunID: "run-1", Attempt: 1, Fence: 1, OwnerSession: "executor",
		Worktree: "worktree", Deadline: now.Add(time.Hour), Status: Started,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid run rejected: %v", err)
	}
	tests := map[string]func(*Run){
		"unknown status":                  func(r *Run) { r.Status = "UNKNOWN" },
		"stalled without resume point":    func(r *Run) { r.Status = Stalled },
		"resume point outside stalled":    func(r *Run) { r.ResumeStatus = Started },
		"recovery claim without recovery": func(r *Run) { r.RecoveryClaim = "claim" },
		"recovery outside stalled":        func(r *Run) { r.RecoveryNeeded = true },
		"terminal recovery":               func(r *Run) { r.Status = Failed; r.RecoveryNeeded = true },
		"done without result":             func(r *Run) { r.Status = Done },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			run := valid
			mutate(&run)
			if err := run.Validate(); err == nil {
				t.Fatal("invalid persisted run was accepted")
			}
		})
	}
}
