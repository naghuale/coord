package agentsession

import (
	"context"
	"time"
)

type Status string

const (
	StatusAdmitted Status = "ADMITTED"
	StatusActive   Status = "ACTIVE"
	StatusFenced   Status = "FENCED"
	StatusRevoked  Status = "REVOKED"
	StatusExpired  Status = "EXPIRED"
	StatusRejected Status = "REJECTED"
)

type AdmitRequest struct {
	ProjectID        string `json:"projectId"`
	AssignmentID     string `json:"assignmentId"`
	BaseSHA          string `json:"baseSha"`
	Role             string `json:"role"`
	AgentID          string `json:"agentId"`
	LeaseTokenDigest string `json:"leaseTokenDigest"`
	IdempotencyKey   string `json:"idempotencyKey"`
}

type Session struct {
	ProjectID        string `json:"projectId"`
	AssignmentID     string `json:"assignmentId"`
	SessionID        string `json:"sessionId"`
	TrustedAgentID   string `json:"trustedAgentId"`
	LeaseTokenDigest string `json:"leaseTokenDigest"`
	RegistryRevision string `json:"registryRevision"`
	Generation       uint64 `json:"generation"`
	Status           Status `json:"status"`
}

type HeartbeatRequest struct {
	ProjectID        string `json:"projectId"`
	AssignmentID     string `json:"assignmentId"`
	SessionID        string `json:"sessionId"`
	LeaseTokenDigest string `json:"leaseTokenDigest"`
	RegistryRevision string `json:"registryRevision"`
	IdempotencyKey   string `json:"idempotencyKey"`
	Generation       uint64 `json:"generation"`
	Sequence         uint64 `json:"heartbeatSequence"`
}

type HeartbeatResult struct {
	SessionID        string `json:"sessionId"`
	LeaseTokenDigest string `json:"leaseTokenDigest"`
	RegistryRevision string `json:"registryRevision"`
	Generation       uint64 `json:"generation"`
	Sequence         uint64 `json:"heartbeatSequence"`
	Status           Status `json:"status"`
	FenceReason      string `json:"fenceReason,omitempty"`
}

type CompleteRequest struct {
	SessionID      string `json:"sessionId"`
	IdempotencyKey string `json:"idempotencyKey"`
	Generation     uint64 `json:"generation"`
	ExitCode       int    `json:"exitCode"`
	Fenced         bool   `json:"fenced"`
}

type FenceRequest struct {
	SessionID      string `json:"sessionId"`
	IdempotencyKey string `json:"idempotencyKey"`
	Reason         string `json:"reason"`
	Generation     uint64 `json:"generation"`
}

type Registry interface {
	Admit(context.Context, AdmitRequest) (Session, error)
	Heartbeat(context.Context, HeartbeatRequest) (HeartbeatResult, error)
	Complete(context.Context, CompleteRequest) error
	Fence(context.Context, FenceRequest) error
}

type RetryPolicy struct {
	MaxAttempts    int
	InitialDelay   time.Duration
	MaxDelay       time.Duration
	Multiplier     float64
	JitterFraction float64
}
