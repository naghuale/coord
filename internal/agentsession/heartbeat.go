package agentsession

import "time"

// HeartbeatRecord is appended to the ops ledger when a heartbeat is received
// from a running session. It is not a mutable session field; each heartbeat
// is a new record.
type HeartbeatRecord struct {
	SessionID  string    `json:"sessionId"`
	Generation uint64    `json:"generation"`
	Sequence   uint64    `json:"sequence"`
	ReceivedAt time.Time `json:"receivedAt"`
	// RegistryRevision is the revision of the session registry at the time
	// the heartbeat was recorded, for idempotency and replay detection.
	RegistryRevision string `json:"registryRevision,omitempty"`
}

// IsStale returns true if the heartbeat has not been updated within timeout.
func (h HeartbeatRecord) IsStale(now time.Time, timeout time.Duration) bool {
	if h.ReceivedAt.IsZero() {
		return true
	}
	return now.Sub(h.ReceivedAt) >= timeout
}
