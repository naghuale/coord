package retry

import (
	"testing"
	"time"
)

func TestBoundedBackoff(t *testing.T) {
	p := Policy{BaseDelay: 5 * time.Second, MaxDelay: 60 * time.Second, Multiplier: 2, MaxRetries: 5}
	for n, want := range []time.Duration{5, 10, 20, 40, 60, 60} {
		if got := p.DelayFor(n); got != want*time.Second {
			t.Fatalf("delay %d: %v", n, got)
		}
	}
	p.Jitter = true
	for n := 0; n < 100; n++ {
		if d := p.DelayFor(n); d <= 0 || d > p.MaxDelay {
			t.Fatal("jitter exceeds cap")
		}
	}
}
