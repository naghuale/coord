package watchdog

import (
	"context"
	"coord/fsm"
	"errors"
	"math"
	"reflect"
	"testing"
	"time"
)

type multiBackend struct{ backend }

func (b *multiBackend) Candidates(context.Context) ([]fsm.Run, error) {
	return []fsm.Run{{RunID: "one", Fence: 1, Status: fsm.Stalled, RecoveryNeeded: true}, {RunID: "two", Fence: 1, Status: fsm.Stalled, RecoveryNeeded: true}}, nil
}
func TestSweepDoesNotStarveOtherRunsAfterOneFailure(t *testing.T) {
	b := &multiBackend{}
	b.fenceErr = true
	if err := (Watchdog{Backend: b}).Sweep(context.Background()); err == nil {
		t.Fatal("expected fence error")
	}
	claims := 0
	for _, e := range b.events {
		if e == "claim" {
			claims++
		}
	}
	if claims != 2 {
		t.Fatalf("other run starved: %v", b.events)
	}
}

type canceledBackend struct {
	backend
	cancel context.CancelFunc
}

func (b *canceledBackend) Transfer(context.Context, string, string, uint64) error {
	b.cancel()
	b.events = append(b.events, "transfer")
	return errors.New("response lost")
}
func (b *canceledBackend) Stop(ctx context.Context, _ string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	b.events = append(b.events, "stop")
	return nil
}
func (b *canceledBackend) Release(ctx context.Context, _ string, _ uint64) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	b.events = append(b.events, "release")
	return nil
}
func TestCancellationStillAllowsBoundedCleanup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := &canceledBackend{cancel: cancel}
	if err := (Watchdog{Backend: b, Interval: time.Second}).Sweep(ctx); err == nil {
		t.Fatal("expected transfer error")
	}
	if !reflect.DeepEqual(b.events, []string{"claim", "fence", "verify", "spawn", "transfer", "stop", "release"}) {
		t.Fatalf("cleanup skipped: %v", b.events)
	}
}
func TestSpawnRejectsFenceOverflowBeforeLaunchingCommand(t *testing.T) {
	if _, err := (CommandBackend{}).Spawn(context.Background(), fsm.Run{Fence: math.MaxUint64}); err == nil {
		t.Fatal("overflow accepted")
	}
	// Error must identify exhaustion, rather than merely the absent command.
	_, err := (CommandBackend{}).Spawn(context.Background(), fsm.Run{Fence: math.MaxUint64})
	if !errors.Is(err, ErrFenceExhausted) {
		t.Fatalf("overflow reached launch: %v", err)
	}
}
func TestSweepRejectsMissingBackend(t *testing.T) {
	if err := (Watchdog{}).Sweep(context.Background()); err == nil {
		t.Fatal("missing backend accepted")
	}
}
