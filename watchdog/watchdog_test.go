package watchdog

import (
	"context"
	"coord/fsm"
	"errors"
	"reflect"
	"testing"
	"time"
)

type backend struct {
	events                           []string
	fenceErr, verifyErr, transferErr bool
	stopErr                          bool
}

func (b *backend) Candidates(context.Context) ([]fsm.Run, error) {
	return []fsm.Run{{RunID: "run", Status: fsm.Stalled, RecoveryNeeded: true, Fence: 1}}, nil
}
func (b *backend) Claim(context.Context, string, uint64) (bool, error) {
	b.events = append(b.events, "claim")
	return true, nil
}
func (b *backend) Release(context.Context, string, uint64) error {
	b.events = append(b.events, "release")
	return nil
}
func (b *backend) FenceOld(context.Context, fsm.Run) error {
	b.events = append(b.events, "fence")
	if b.fenceErr {
		return errors.New("old writer live")
	}
	return nil
}
func (b *backend) VerifyWorktree(context.Context, fsm.Run) error {
	b.events = append(b.events, "verify")
	if b.verifyErr {
		return errors.New("wrong SHA")
	}
	return nil
}
func (b *backend) Spawn(context.Context, fsm.Run) (string, error) {
	b.events = append(b.events, "spawn")
	return "new", nil
}
func (b *backend) Transfer(context.Context, string, string, uint64) error {
	b.events = append(b.events, "transfer")
	if b.transferErr {
		return errors.New("CAS refused")
	}
	return nil
}
func (b *backend) Stop(context.Context, string) error {
	b.events = append(b.events, "stop")
	if b.stopErr {
		return errors.New("replacement still live")
	}
	return nil
}
func (b *backend) Fail(context.Context, string, uint64, string) error {
	b.events = append(b.events, "fail")
	return nil
}
func TestRecoveryOrdering(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		fence, verify, transfer bool
		want                    []string
	}{{"success", false, false, false, []string{"claim", "fence", "verify", "spawn", "transfer", "release"}}, {"live writer", true, false, false, []string{"claim", "fence", "release"}}, {"damaged tree", false, true, false, []string{"claim", "fence", "verify", "fail", "release"}}, {"lost CAS", false, false, true, []string{"claim", "fence", "verify", "spawn", "transfer", "stop", "release"}}} {
		t.Run(tc.name, func(t *testing.T) {
			b := &backend{fenceErr: tc.fence, verifyErr: tc.verify, transferErr: tc.transfer}
			w := Watchdog{Backend: b, Interval: time.Second}
			_ = w.Sweep(context.Background())
			if !reflect.DeepEqual(b.events, tc.want) {
				t.Fatalf("events %v", b.events)
			}
		})
	}
}

func TestFailedReplacementStopRetainsClaim(t *testing.T) {
	b := &backend{transferErr: true, stopErr: true}
	w := Watchdog{Backend: b, Interval: time.Second}
	if err := w.Sweep(context.Background()); err == nil {
		t.Fatal("expected cleanup error")
	}
	for _, event := range b.events {
		if event == "release" {
			t.Fatal("released claim while replacement may still be live")
		}
	}
}
