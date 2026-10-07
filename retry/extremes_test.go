package retry

import (
	"math"
	"testing"
	"time"
)

func TestDelayNeverOverflowsAtDurationLimit(t *testing.T) {
	p := Policy{BaseDelay: time.Duration(math.MaxInt64), MaxDelay: time.Duration(math.MaxInt64), Multiplier: 2, MaxRetries: 1}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, n := range []int{0, 1, 10000} {
		if d := p.DelayFor(n); d <= 0 || d > p.MaxDelay {
			t.Fatalf("overflow: %v", d)
		}
	}
}
func TestNanosecondJitterNeverReturnsZero(t *testing.T) {
	p := Policy{BaseDelay: time.Nanosecond, MaxDelay: time.Second, Multiplier: 2, MaxRetries: 1, Jitter: true}
	for i := 0; i < 1000; i++ {
		if d := p.DelayFor(0); d <= 0 {
			t.Fatalf("nonpositive jitter: %v", d)
		}
	}
}
