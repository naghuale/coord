package watchdog

import (
	"sync"
	"time"
)

type fakeClock struct {
	mu      sync.Mutex
	current time.Time
	tickers []*fakeTicker
}

func newFakeClock(start time.Time) *fakeClock {
	return &fakeClock{current: start}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.current
}

func (c *fakeClock) NewTicker(interval time.Duration) Ticker {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTicker{ch: make(chan time.Time, 1), interval: interval, start: c.current}
	c.tickers = append(c.tickers, t)
	return t
}

func (c *fakeClock) NewTimer(timeout time.Duration) Timer {
	ch := make(chan time.Time, 1)
	c.mu.Lock()
	deadline := c.current.Add(timeout)
	c.mu.Unlock()
	return &fakeTimer{ch: ch, deadline: deadline}
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.current = c.current.Add(d)
	now := c.current
	tickers := make([]*fakeTicker, len(c.tickers))
	copy(tickers, c.tickers)
	c.mu.Unlock()

	for _, t := range tickers {
		t.maybeFireUntil(now)
	}
}

type fakeTicker struct {
	mu       sync.Mutex
	ch       chan time.Time
	interval time.Duration
	start    time.Time
	stopped  bool
	last     time.Time
}

func (t *fakeTicker) C() <-chan time.Time { return t.ch }

func (t *fakeTicker) Stop() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.stopped = true
}

func (t *fakeTicker) maybeFireUntil(now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.stopped {
		return
	}
	base := t.start
	if !t.last.IsZero() {
		base = t.last
	}
	for base.Add(t.interval).Before(now) || base.Add(t.interval).Equal(now) {
		base = base.Add(t.interval)
		select {
		case t.ch <- base:
		default:
		}
		t.last = base
	}
}

type fakeTimer struct {
	ch       chan time.Time
	deadline time.Time
}

func (t *fakeTimer) C() <-chan time.Time { return t.ch }
func (t *fakeTimer) Stop() bool          { return false }
