package fsm

import (
	"coord/envelope"
	"coord/roles"
	"coord/taskid"
	"errors"
	"fmt"
	"reflect"
	"time"
)

type Status string

const (
	Pending     Status = "PENDING"
	Accepted    Status = "ACCEPTED"
	Started     Status = "STARTED"
	Blocked     Status = "BLOCKED"
	Stalled     Status = "STALLED"
	ResultReady Status = "RESULT_READY"
	Done        Status = "DONE"
	Failed      Status = "FAILED"
)

// ErrOutOfOrder rejects delayed progress that would overwrite a newer heartbeat.
var ErrOutOfOrder = errors.New("out-of-order heartbeat")

// Valid reports whether a status belongs to the persisted state machine.
func (s Status) Valid() bool {
	switch s {
	case Pending, Accepted, Started, Blocked, Stalled, ResultReady, Done, Failed:
		return true
	}
	return false
}

// Run contains protocol state and durable retry/outbox bookkeeping.
type Run struct {
	Task                                           string
	TaskID                                         taskid.ID `json:"task_id,omitempty"`
	RunID                                          string
	Attempt                                        uint64
	TargetSHA                                      string
	OwnerSession                                   string
	AgentSessionGeneration                         uint64 `json:"agent_session_generation,omitempty"`
	Fence                                          uint64
	Status                                         Status
	ResumeStatus                                   Status
	AssignedAt, LastUpdate, NextUpdateBy, Deadline time.Time
	Worktree, PayloadRef                           string
	ProgressPct                                    int
	Blocker                                        string
	TaskMessages                                   []string
	Seen                                           map[string]bool
	Outbox                                         []envelope.Envelope
	Retries                                        int
	RetryAt                                        time.Time
	ProbeID, ProbeNonce                            string
	RecoveryNeeded                                 bool
	Result                                         *envelope.ResultPayload
	RecoveryClaim                                  string
	DeliveredAt                                    time.Time
	SendRetryAt                                    time.Time
	SendRetries                                    int
	CoordinatorSession                             string
	ResultMessageID                                string
	LastHeartbeatAt                                time.Time
	RequiredRole                                   roles.Requirement
	RoleHistory                                    []roles.Selection
	RoleSelection                                  *roles.Selection
}

func (r Run) Terminal() bool { return r.Status == Done || r.Status == Failed }

// SameDomainState reports whether a durable Run update changes task history.
// Delivery bookkeeping and heartbeat timestamps are intentionally excluded:
// they are transport/runtime facts, not DomainEvents.
func (r Run) SameDomainState(other Run) bool {
	return r.Task == other.Task && r.TaskID == other.TaskID && r.RunID == other.RunID &&
		r.Attempt == other.Attempt && r.TargetSHA == other.TargetSHA &&
		r.OwnerSession == other.OwnerSession && r.AgentSessionGeneration == other.AgentSessionGeneration && r.Fence == other.Fence &&
		r.Status == other.Status && r.ResumeStatus == other.ResumeStatus &&
		r.AssignedAt.Equal(other.AssignedAt) && r.Deadline.Equal(other.Deadline) &&
		r.Worktree == other.Worktree && r.PayloadRef == other.PayloadRef &&
		r.ProgressPct == other.ProgressPct && r.Blocker == other.Blocker &&
		reflect.DeepEqual(r.Result, other.Result) && r.RecoveryNeeded == other.RecoveryNeeded &&
		r.RecoveryClaim == other.RecoveryClaim &&
		reflect.DeepEqual(r.RequiredRole, other.RequiredRole) &&
		reflect.DeepEqual(r.RoleHistory, other.RoleHistory) &&
		reflect.DeepEqual(r.RoleSelection, other.RoleSelection)
}

// Validate checks invariants required of a durable Run snapshot. It does not
// validate workflow transitions; those are enforced by Apply and the
// coordinator command boundary.
func (r Run) Validate() error {
	if r.RunID == "" || r.Attempt == 0 || r.Fence == 0 || r.OwnerSession == "" {
		return fmt.Errorf("run identity, attempt, fence and owner session are required")
	}
	if !r.Status.Valid() || r.Worktree == "" || r.Deadline.IsZero() {
		return fmt.Errorf("run status, worktree and deadline must be valid")
	}
	if r.Status == Stalled {
		if r.ResumeStatus == "" || !r.ResumeStatus.Valid() || r.ResumeStatus == Stalled || r.ResumeStatus == Done || r.ResumeStatus == Failed {
			return fmt.Errorf("stalled run requires a resumable status")
		}
	} else if r.ResumeStatus != "" {
		return fmt.Errorf("non-stalled run cannot retain a resume status")
	}
	if r.RecoveryClaim != "" && (!r.RecoveryNeeded || r.Status != Stalled) {
		return fmt.Errorf("recovery claim requires a stalled run marked for recovery")
	}
	if r.RecoveryNeeded && r.Status != Stalled {
		return fmt.Errorf("run marked for recovery must be stalled")
	}
	if r.Terminal() && (r.RecoveryNeeded || r.RecoveryClaim != "") {
		return fmt.Errorf("terminal run cannot require recovery or retain a claim")
	}
	// Legacy Runs carry their accepted result directly. Task-scoped Runs use
	// canonical DomainEvents as the result source; CanonicalState validation
	// verifies the corresponding typed result event before accepting the Run.
	if r.Status == Done && r.Result == nil && r.TaskID == "" {
		return fmt.Errorf("done run requires an accepted result")
	}
	return nil
}

func (r *Run) Timeout(now time.Time) error {
	if r.Terminal() || r.Status == Stalled {
		return fmt.Errorf("cannot time out %s", r.Status)
	}
	r.ResumeStatus = r.Status
	r.Status = Stalled
	r.LastUpdate = now
	return nil
}

// Apply rejects forbidden transitions without changing the run.
func (r *Run) Apply(e envelope.Envelope, now time.Time) error {
	if r.Terminal() {
		return fmt.Errorf("terminal run")
	}
	status := r.Status
	if status == Stalled {
		status = r.ResumeStatus
	}
	var next Status
	switch e.Kind {
	case envelope.Ack:
		if e.AckPayload == nil || (e.AckPayload.State != "ACCEPTED" && e.AckPayload.State != "REJECTED") {
			return fmt.Errorf("invalid ACK")
		}
		if status != Pending {
			return fmt.Errorf("ACK after acceptance")
		}
		if e.AckPayload.State == "REJECTED" {
			next = Failed
		} else {
			next = Accepted
		}
	case envelope.Heartbeat:
		if e.HeartbeatPayload == nil || (e.HeartbeatPayload.State != "STARTED" && e.HeartbeatPayload.State != "BLOCKED" && e.HeartbeatPayload.State != "RESULT_READY") {
			return fmt.Errorf("invalid heartbeat")
		}
		if status != Accepted && status != Started && status != Blocked && status != ResultReady {
			return fmt.Errorf("heartbeat before ACK")
		}
		if e.Timestamp.Before(r.LastHeartbeatAt) || e.HeartbeatPayload.ProgressPct < r.ProgressPct {
			return ErrOutOfOrder
		}
		next = Status(e.HeartbeatPayload.State)
		if status == ResultReady && next != ResultReady {
			return fmt.Errorf("result readiness is irreversible")
		}
	case envelope.Result:
		if e.ResultPayload == nil || (e.ResultPayload.Outcome != "SUCCESS" && e.ResultPayload.Outcome != "FAILURE" && e.ResultPayload.Outcome != "PARTIAL") {
			return fmt.Errorf("invalid result")
		}
		if status != ResultReady {
			return fmt.Errorf("result before readiness")
		}
		if e.ResultPayload.Outcome == "SUCCESS" {
			next = Done
		} else {
			next = Failed
		}
	default:
		return fmt.Errorf("not a task event")
	}
	// Единственная точка выхода из STALLED: ResumeStatus очищается здесь, а не
	// в каждом терминальном переходе, иначе появятся копии этой проверки, и
	// выход в нетерминальный статус сохранит несуществующую точку продолжения.
	if r.Status == Stalled {
		r.Status = next
		r.ResumeStatus = ""
	} else {
		r.Status = next
	}
	r.LastUpdate = now
	r.Retries = 0
	r.RetryAt = time.Time{}
	r.RecoveryNeeded = false
	r.ProbeID = ""
	r.ProbeNonce = ""
	switch e.Kind {
	case envelope.Ack:
		r.NextUpdateBy = e.AckPayload.NextUpdateBy
	case envelope.Heartbeat:
		r.LastHeartbeatAt = e.Timestamp
		r.NextUpdateBy = e.HeartbeatPayload.NextUpdateBy
		r.ProgressPct = e.HeartbeatPayload.ProgressPct
		r.Blocker = e.HeartbeatPayload.Blocker
	case envelope.Result:
		p := *e.ResultPayload
		r.Result = &p
	}
	return nil
}
