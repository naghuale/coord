package supervisor

import "time"

type RealClock struct{}
type realTicker struct{ t *time.Ticker }

func (RealClock) NewTicker(d time.Duration) Ticker { return &realTicker{time.NewTicker(d)} }
func (RealClock) Now() time.Time                   { return time.Now().UTC() }
func (t *realTicker) C() <-chan time.Time          { return t.t.C }
func (t *realTicker) Stop()                        { t.t.Stop() }
