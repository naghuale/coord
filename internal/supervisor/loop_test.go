package supervisor

import (
	"context"
	"errors"
	"testing"
	"time"

	"coord/internal/agentsession"
)

type fakeRegistry struct {
	heartbeats int
	fenced     bool
	completed  bool
}

func (f *fakeRegistry) Admit(context.Context, agentsession.AdmitRequest) (agentsession.Session, error) {
	return agentsession.Session{}, nil
}
func (f *fakeRegistry) Heartbeat(context.Context, agentsession.HeartbeatRequest) (agentsession.HeartbeatResult, error) {
	f.heartbeats++
	return agentsession.HeartbeatResult{SessionID: "s1", Generation: 2, LeaseTokenDigest: "lease", Status: agentsession.StatusAdmitted}, nil
}
func (f *fakeRegistry) Complete(context.Context, agentsession.CompleteRequest) error {
	f.completed = true
	return nil
}
func (f *fakeRegistry) Fence(context.Context, agentsession.FenceRequest) error {
	f.fenced = true
	return nil
}

type fakeProcess struct {
	done       chan error
	terminated bool
}

func (p *fakeProcess) Start(context.Context, []string, []string, string) error {
	p.done = make(chan error, 1)
	return nil
}
func (p *fakeProcess) Done() <-chan error { return p.done }
func (p *fakeProcess) TerminateGroup(context.Context, time.Duration) error {
	p.terminated = true
	return nil
}
func (p *fakeProcess) ExitCode() int { return 137 }

type fakeTicker struct{ ch chan time.Time }

func (t *fakeTicker) C() <-chan time.Time { return t.ch }
func (*fakeTicker) Stop()                 {}

type fakeClock struct{ ticker *fakeTicker }

func (c *fakeClock) NewTicker(time.Duration) Ticker { return c.ticker }
func (*fakeClock) Now() time.Time                   { return time.Unix(1, 0) }

func TestLoopFencesGenerationMismatch(t *testing.T) {
	r := &fakeRegistry{}
	p := &fakeProcess{}
	tick := &fakeTicker{ch: make(chan time.Time, 1)}
	s := Loop{Registry: r, Process: p, Clock: &fakeClock{ticker: tick}, Policy: Policy{HeartbeatInterval: time.Second, HeartbeatTimeout: time.Second, MaxConsecutiveFailures: 2, GracefulStopTimeout: time.Second}}
	result := make(chan Receipt, 1)
	go func() {
		receipt, _ := s.Run(context.Background(), RunRequest{Session: agentsession.Session{SessionID: "s1", Generation: 1, Status: agentsession.StatusAdmitted}, ProjectID: "coord", AssignmentID: "a1", LeaseTokenDigest: "lease", Command: []string{"agent"}})
		result <- receipt
	}()
	tick.ch <- time.Now()
	select {
	case receipt := <-result:
		if !receipt.Fenced || receipt.FenceReason != FenceGeneration {
			t.Fatalf("%+v", receipt)
		}
		if !p.terminated || !r.fenced || !r.completed {
			t.Fatal("expected termination, fence and complete")
		}
	case <-time.After(time.Second):
		t.Fatal(errors.New("timeout"))
	}
}
