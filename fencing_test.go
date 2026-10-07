package coord

import (
	"context"
	"coord/envelope"
	"coord/fsm"
	"coord/store"
	"errors"
	"math"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestFenceValidationPrecedesOwnerAndDeduplication(t *testing.T) {
	for _, tc := range []struct {
		name   string
		fence  uint64
		owner  string
		metric string
		stale  bool
	}{
		{"old generation", 1, "old", "stale_fence_rejections_total", true},
		{"old generation claimed by current owner", 1, "exec", "stale_fence_rejections_total", true},
		{"future generation from current owner", 3, "exec", "invalid_fence_rejections_total", false},
		{"current generation from another owner", 2, "other", "split_brain_events_total", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t)
			f.assign(t)
			r, _ := f.s.Get("run")
			r.Fence = 2
			if err := f.c.put(r); err != nil {
				t.Fatal(err)
			}
			e := f.ack()
			e.Fence = tc.fence
			e.From = tc.owner
			// A reused message_id cannot bypass the fence check.
			r.Seen[e.MessageID] = true
			if err := f.c.put(r); err != nil {
				t.Fatal(err)
			}
			before, _ := f.s.Get("run")
			err := f.c.Handle(context.Background(), e)
			if !errors.Is(err, ErrStale) {
				t.Fatalf("expected stale identity: %v", err)
			}
			if errors.Is(err, ErrStaleFence) != tc.stale {
				t.Fatalf("wrong stale fence classification: %v", err)
			}
			after, _ := f.s.Get("run")
			if !reflect.DeepEqual(before, after) {
				t.Fatal("rejected message changed canonical state")
			}
			abort := f.tp.sent[len(f.tp.sent)-1]
			if abort.Kind != envelope.Abort || abort.To != tc.owner || abort.Fence != 2 || abort.InReplyTo != e.MessageID {
				t.Fatalf("invalid ABORT: %+v", abort)
			}
			w := httptest.NewRecorder()
			f.c.Metrics.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
			if !strings.Contains(w.Body.String(), "coord_"+tc.metric+" 1\n") {
				t.Fatalf("missing rejection metric: %s", w.Body.String())
			}
		})
	}
}

func TestRecoveryRejectsFenceOverflow(t *testing.T) {
	f := setup(t)
	f.assign(t)
	r, _ := f.s.Get("run")
	r.Fence = math.MaxUint64
	r.Status = fsm.Stalled
	r.RecoveryNeeded = true
	r.RecoveryClaim = "supervisor"
	if err := f.c.put(r); err != nil {
		t.Fatal(err)
	}
	before, _ := f.s.Get("run")
	sent := len(f.tp.sent)
	err := f.c.RecoverClaimed(context.Background(), "run", "replacement", math.MaxUint64, "supervisor")
	if !errors.Is(err, ErrFenceExhausted) {
		t.Fatalf("expected fence exhaustion: %v", err)
	}
	after, _ := f.s.Get("run")
	if !reflect.DeepEqual(before, after) || len(f.tp.sent) != sent {
		t.Fatal("overflow published ownership or sent TASK")
	}
}

type failingRecoveryPersistence struct {
	runs map[string]fsm.Run
	fail bool
}

func (p *failingRecoveryPersistence) Load() (map[string]fsm.Run, error) { return p.runs, nil }
func (p *failingRecoveryPersistence) Save(map[string]fsm.Run) error {
	if p.fail {
		return errors.New("disk full")
	}
	return nil
}
func TestFailedRecoveryCommitRetainsOwnerAndFence(t *testing.T) {
	f := setup(t)
	f.assign(t)
	r, _ := f.s.Get("run")
	r.Status = fsm.Stalled
	r.RecoveryNeeded = true
	r.RecoveryClaim = "supervisor"
	p := &failingRecoveryPersistence{runs: map[string]fsm.Run{"run": r}, fail: true}
	s, err := store.New(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("close test store: %v", err)
		}
	})
	f.c.store = s
	sent := len(f.tp.sent)
	err = f.c.RecoverClaimed(context.Background(), "run", "replacement", 1, "supervisor")
	if !errors.Is(err, store.ErrPersistence) {
		t.Fatalf("expected persistence error: %v", err)
	}
	after, _ := s.Get("run")
	if after.OwnerSession != "exec" || after.Fence != 1 || after.RecoveryClaim != "supervisor" || len(f.tp.sent) != sent {
		t.Fatal("failed commit transferred ownership")
	}
}
