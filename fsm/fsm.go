package fsm

import (
	"coord/envelope"
	"coord/roles"
	"fmt"
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

// Run contains protocol state and durable retry/outbox bookkeeping.
type Run struct {
	Task                                           string
	RunID                                          string
	Attempt                                        uint64
	TargetSHA                                      string
	OwnerSession                                   string
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
	RequiredRole                                   roles.Requirement
	RoleHistory                                    []roles.Selection
	RoleSelection                                  *roles.Selection
}

func (r Run) Terminal() bool { return r.Status == Done || r.Status == Failed }
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
	next := status
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
	r.Status = next
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
		r.NextUpdateBy = e.HeartbeatPayload.NextUpdateBy
		r.ProgressPct = e.HeartbeatPayload.ProgressPct
		r.Blocker = e.HeartbeatPayload.Blocker
	case envelope.Result:
		p := *e.ResultPayload
		r.Result = &p
	}
	return nil
}
