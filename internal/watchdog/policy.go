package watchdog

import (
	"fmt"
	"time"
)

type Policy struct {
	Enabled            bool
	Heartbeat          HeartbeatPolicy
	Reconcile          ReconcilePolicy
	Actions            ActionPolicy
	PassiveWaiting     PassiveWaitingPolicy
	StartupGracePeriod time.Duration
}

type HeartbeatPolicy struct {
	Interval  time.Duration
	Timeout   time.Duration
	MaxMissed int
}

type ReconcilePolicy struct {
	Timeout     time.Duration
	MaxAttempts int
}

type ActionPolicy struct {
	OnFirstMiss        string
	OnFailureLimit     string
	OnReconcileFailure string
}

type PassiveWaitingPolicy struct {
	Allowed bool
}

func DefaultPolicy() Policy {
	return Policy{
		Enabled: true,
		Heartbeat: HeartbeatPolicy{
			Interval:  30 * time.Second,
			Timeout:   90 * time.Second,
			MaxMissed: 3,
		},
		Reconcile: ReconcilePolicy{
			Timeout:     30 * time.Second,
			MaxAttempts: 2,
		},
		Actions: ActionPolicy{
			OnFirstMiss:        "MARK_SUSPECT",
			OnFailureLimit:     "RECONCILE_SESSION",
			OnReconcileFailure: "FENCE_AND_REPLACE",
		},
		PassiveWaiting:     PassiveWaitingPolicy{Allowed: false},
		StartupGracePeriod: 60 * time.Second,
	}
}

const (
	ActionMarkSuspect      = "MARK_SUSPECT"
	ActionReconcileSession = "RECONCILE_SESSION"
	ActionFenceAndReplace  = "FENCE_AND_REPLACE"
)

func (p Policy) Validate() error {
	if p.Heartbeat.Interval <= 0 {
		return fmt.Errorf("heartbeat interval must be > 0")
	}
	if p.Heartbeat.Timeout < p.Heartbeat.Interval {
		return fmt.Errorf("heartbeat timeout must be >= interval")
	}
	if p.Heartbeat.MaxMissed <= 0 {
		return fmt.Errorf("heartbeat maxMissed must be > 0")
	}
	if p.Reconcile.Timeout <= 0 {
		return fmt.Errorf("reconcile timeout must be > 0")
	}
	if p.StartupGracePeriod < p.Heartbeat.Interval {
		return fmt.Errorf("startupGracePeriod must be >= heartbeat interval")
	}
	if p.Actions.OnFirstMiss != ActionMarkSuspect {
		return fmt.Errorf("invalid action onFirstMiss: %q (must be %q)", p.Actions.OnFirstMiss, ActionMarkSuspect)
	}
	if p.Actions.OnFailureLimit != ActionReconcileSession {
		return fmt.Errorf("invalid action onFailureLimit: %q (must be %q)", p.Actions.OnFailureLimit, ActionReconcileSession)
	}
	if p.Actions.OnReconcileFailure != ActionFenceAndReplace {
		return fmt.Errorf("invalid action onReconcileFailure: %q (must be %q)", p.Actions.OnReconcileFailure, ActionFenceAndReplace)
	}
	return nil
}
