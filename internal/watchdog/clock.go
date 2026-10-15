package watchdog

import "time"

type Ticker interface {
	C() <-chan time.Time
	Stop()
}

type Timer interface {
	C() <-chan time.Time
	Stop() bool
}

type Clock interface {
	Now() time.Time
	NewTicker(interval time.Duration) Ticker
	NewTimer(timeout time.Duration) Timer
}

// RealClock uses the system clock.
type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now() }

func (RealClock) NewTicker(interval time.Duration) Ticker {
	return &realTicker{t: time.NewTicker(interval)}
}

func (RealClock) NewTimer(timeout time.Duration) Timer {
	return &realTimer{t: time.NewTimer(timeout)}
}

type realTicker struct{ t *time.Ticker }

func (r *realTicker) C() <-chan time.Time { return r.t.C }
func (r *realTicker) Stop()               { r.t.Stop() }

type realTimer struct{ t *time.Timer }

func (r *realTimer) C() <-chan time.Time { return r.t.C }
func (r *realTimer) Stop() bool          { return r.t.Stop() }
