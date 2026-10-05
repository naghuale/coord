package retry

import (
	"fmt"
	"math"
	"math/rand"
	"time"
)

type Policy struct {
	BaseDelay, MaxDelay time.Duration
	Multiplier          float64
	MaxRetries          int
	Jitter              bool
}

func Default() Policy { return Policy{5 * time.Second, 60 * time.Second, 2, 5, true} }
func (p Policy) Validate() error {
	if p.BaseDelay <= 0 || p.MaxDelay < p.BaseDelay || p.Multiplier < 1 || math.IsNaN(p.Multiplier) || math.IsInf(p.Multiplier, 0) || p.MaxRetries < 0 {
		return fmt.Errorf("invalid retry policy")
	}
	return nil
}
func (p Policy) DelayFor(n int) time.Duration {
	if n < 0 {
		n = 0
	}
	d := math.Min(float64(p.MaxDelay), float64(p.BaseDelay)*math.Pow(p.Multiplier, float64(n)))
	if p.Jitter {
		d *= 0.5 + rand.Float64()
	}
	return time.Duration(math.Min(d, float64(p.MaxDelay)))
}
