package supervisor

import (
	"context"
	"coord/internal/agentsession"
	"time"
)

type FenceReason string

const (
	FenceRegistryStatus FenceReason = "registry_status_fenced"
	FenceGeneration     FenceReason = "generation_mismatch"
	FenceLease          FenceReason = "lease_digest_mismatch"
	FenceHeartbeat      FenceReason = "heartbeat_failure_limit"
	FenceContext        FenceReason = "context_cancelled"
)

type Process interface {
	Start(context.Context, []string, []string, string) error
	Done() <-chan error
	TerminateGroup(context.Context, time.Duration) error
	ExitCode() int
}
type Clock interface {
	NewTicker(time.Duration) Ticker
	Now() time.Time
}
type Ticker interface {
	C() <-chan time.Time
	Stop()
}
type ReceiptWriter interface {
	WriteReceipt(context.Context, Receipt) error
}
type Policy struct {
	HeartbeatInterval      time.Duration
	HeartbeatTimeout       time.Duration
	MaxConsecutiveFailures int
	GracefulStopTimeout    time.Duration
}
type Receipt struct {
	SessionID   string
	Generation  uint64
	Fenced      bool
	FenceReason FenceReason
	ExitCode    int
	Events      []Event
}
type Event struct {
	Type, Status, Revision string
	At                     time.Time
}
type Supervisor interface {
	Run(context.Context, RunRequest) (Receipt, error)
}
type RunRequest struct {
	Session                                                     agentsession.Session
	ProjectID, AssignmentID, LeaseTokenDigest, WorkingDirectory string
	Command, Environment                                        []string
}
