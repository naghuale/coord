//go:build unix

package supervisor

import (
	"context"
	"testing"
	"time"

	"coord/internal/agentsession"
)

// The retired Python case observed an active first heartbeat, then revoked the
// session on the second. Keep both the HTTP server and sleeping child real.
func TestHTTPAdapterSupervisorFenceLiveProcess(t *testing.T) {
	registry := newHTTPRevocationRegistry(t)
	process := &UnixProcess{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	t.Cleanup(func() {
		select {
		case <-process.Done():
		default:
			_ = process.TerminateGroup(context.Background(), time.Second)
		}
	})
	tick := &fakeTicker{ch: make(chan time.Time, 2)}
	tick.ch <- time.Unix(1, 0)
	tick.ch <- time.Unix(2, 0)
	loop := Loop{Registry: registry, Process: process, Clock: &fakeClock{ticker: tick}, Policy: Policy{
		HeartbeatInterval: time.Second, HeartbeatTimeout: time.Second,
		MaxConsecutiveFailures: 1, GracefulStopTimeout: time.Second,
	}}
	receipt, err := loop.Run(ctx, RunRequest{
		Session:   agentsession.Session{SessionID: "s1", Generation: 1, Status: agentsession.StatusAdmitted, LeaseTokenDigest: "sha256:lease"},
		ProjectID: "coord", AssignmentID: "a1", LeaseTokenDigest: "sha256:lease",
		Command: []string{"/bin/sleep", "30"}, WorkingDirectory: t.TempDir(),
	})
	if err == nil || !receipt.Fenced || receipt.FenceReason != FenceRegistryStatus {
		t.Fatalf("expected revoked-status fence and error, receipt=%+v err=%v", receipt, err)
	}
	select {
	case <-process.Done():
	default:
		t.Fatal("fenced receipt returned while child was still running")
	}
	if receipt.ExitCode == 0 {
		t.Fatal("terminated child reported success")
	}
}
