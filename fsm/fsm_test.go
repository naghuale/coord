package fsm

import (
	"coord/envelope"
	"testing"
	"time"
)

func TestHeartbeatTransitions(t *testing.T) {
	for _, from := range []Status{Pending, Accepted, Started, Blocked, ResultReady, Done, Failed} {
		for _, to := range []Status{Started, Blocked, ResultReady} {
			t.Run(string(from)+"/"+string(to), func(t *testing.T) {
				r := Run{Status: from}
				e := envelope.Envelope{Kind: envelope.Heartbeat, HeartbeatPayload: &envelope.HeartbeatPayload{State: string(to)}}
				err := r.Apply(e, time.Now())
				allowed := from == Accepted || from == Started || from == Blocked || (from == ResultReady && to == ResultReady)
				if (err == nil) != allowed {
					t.Fatalf("allowed=%v err=%v", allowed, err)
				}
				if !allowed && r.Status != from {
					t.Fatal("invalid transition mutated state")
				}
			})
		}
	}
}
