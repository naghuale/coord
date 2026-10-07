package watchdog

import (
	"context"
	"coord/fsm"
	"errors"
	"testing"
)

type partialSpawn struct {
	backend
	known bool
}

func (b *partialSpawn) Spawn(context.Context, fsm.Run) (string, error) {
	b.events = append(b.events, "spawn")
	if b.known {
		return "partial", errors.New("spawn failed")
	}
	return "", errors.New("spawn failed")
}
func TestFailedSpawnRequiresConfirmedCleanup(t *testing.T) {
	for _, known := range []bool{true, false} {
		t.Run(map[bool]string{true: "known replacement", false: "unknown replacement"}[known], func(t *testing.T) {
			b := &partialSpawn{known: known}
			if err := (Watchdog{Backend: b}).Sweep(context.Background()); err == nil {
				t.Fatal("expected spawn failure")
			}
			stopped, released := false, false
			for _, e := range b.events {
				if e == "stop" {
					stopped = true
				}
				if e == "release" {
					released = true
				}
			}
			if known && (!stopped || !released) {
				t.Fatalf("known partial process not cleaned: %v", b.events)
			}
			if !known && released {
				t.Fatal("unknown process released claim")
			}
		})
	}
}
