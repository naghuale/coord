package watchdog

import "time"

type SessionState string

const (
	SessionDelivered   SessionState = "DELIVERED"
	SessionQueued      SessionState = "QUEUED"
	SessionAdmitted    SessionState = "ADMITTED"
	SessionRunning     SessionState = "RUNNING"
	SessionSuspect     SessionState = "SUSPECT"
	SessionCompleted   SessionState = "COMPLETED"
	SessionInterrupted SessionState = "INTERRUPTED"
	SessionFenced      SessionState = "FENCED"
	SessionUnknown     SessionState = "UNKNOWN"
)

type WatchdogEvent string

const (
	EventHeartbeatReceived WatchdogEvent = "HEARTBEAT_RECEIVED"
	EventHeartbeatMissed   WatchdogEvent = "HEARTBEAT_MISSED"
	EventHeartbeatStale    WatchdogEvent = "HEARTBEAT_STALE"
	EventSessionRecovered  WatchdogEvent = "SESSION_RECOVERED"
	EventSessionFenced     WatchdogEvent = "SESSION_FENCED"
	// EventSessionRejection records a session whose claimed state was refused.
	// The sweep continues past it, so without this event the violation would
	// exist only as an unlabelled aggregate counter.
	EventSessionRejection WatchdogEvent = "SESSION_REJECTION"
)

type ReasonCode string

const (
	ReasonHeartbeatMissed        ReasonCode = "HEARTBEAT_MISSED"
	ReasonHeartbeatStale         ReasonCode = "HEARTBEAT_STALE"
	ReasonHeartbeatNeverSeen     ReasonCode = "HEARTBEAT_NEVER_SEEN"
	ReasonGenerationMismatch     ReasonCode = "GENERATION_MISMATCH"
	ReasonRegistryUnavailable    ReasonCode = "REGISTRY_UNAVAILABLE"
	ReasonReconcileFailed        ReasonCode = "SESSION_RECONCILE_FAILED"
	ReasonReplacementRequired    ReasonCode = "SESSION_REPLACEMENT_REQUIRED"
	ReasonPassiveWaitProhibited  ReasonCode = "PASSIVE_WAIT_PROHIBITED"
	ReasonInvalidWatchdogPolicy  ReasonCode = "INVALID_WATCHDOG_POLICY"
	ReasonDeliveredNotAdmitted   ReasonCode = "DELIVERED_NOT_ADMITTED"
	ReasonQueuedNotRunning       ReasonCode = "QUEUED_NOT_RUNNING"
	ReasonInvalidStateTransition ReasonCode = "INVALID_STATE_TRANSITION"
)

type Session struct {
	ID         string
	Generation uint64
	AdmittedAt time.Time
	Assignment AssignmentBinding
	State      SessionState
}

type AssignmentBinding struct {
	ID         string
	ProjectID  string
	Role       string
	SubjectSHA string
}

type HeartbeatState struct {
	SessionID        string
	Generation       uint64
	Sequence         uint64
	LastReceivedAt   time.Time
	RegistryRevision string
	Status           SessionState
	MissedHeartbeats int
}

type Observation struct {
	SessionID        string
	Generation       uint64
	ObservedAt       time.Time
	LastHeartbeatAt  time.Time
	MissedHeartbeats int
	State            SessionState
	ReasonCodes      []ReasonCode
}

type Event struct {
	Type       WatchdogEvent
	SessionID  string
	Generation uint64
	Reason     ReasonCode
	OccurredAt time.Time
}

type FenceRequest struct {
	SessionID  string
	Generation uint64
	Reason     ReasonCode
}

type DispatchResult struct {
	NewSessionID string
	Dispatched   bool
}

type ReconcileResult struct {
	Running    bool
	Generation uint64
}
