//go:build unix

package supervisor

import (
	"context"
	"errors"
	"syscall"
	"testing"
	"time"

	"coord/internal/agentsession"
)

func TestRunWithSignalsCancelsOnSIGTERM(t *testing.T) {
	reg := &fakeRegistry{}
	proc := &fakeProcess{}
	clk := &fakeClock{ticker: &fakeTicker{ch: make(chan time.Time)}}
	loop := &Loop{
		Registry: reg,
		Process:  proc,
		Clock:    clk,
		Policy: Policy{
			HeartbeatInterval:      time.Second,
			GracefulStopTimeout:    100 * time.Millisecond,
			MaxConsecutiveFailures: 3,
			HeartbeatTimeout:       time.Second,
		},
	}

	done := make(chan struct{})
	var receipt Receipt
	var runErr error
	go func() {
		receipt, runErr = loop.RunWithSignals(context.Background(), RunRequest{
			Session: agentsession.Session{Status: agentsession.StatusActive, Generation: 1},
		})
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("send SIGTERM: %v", err)
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunWithSignals did not stop after SIGTERM")
	}
	if !errors.Is(runErr, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", runErr)
	}
	if !receipt.Fenced {
		t.Fatal("expected Fenced=true after SIGTERM")
	}
	if receipt.FenceReason != FenceContext {
		t.Fatalf("expected FenceContext, got %s", receipt.FenceReason)
	}
}
