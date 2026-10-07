//go:build unix

package localchat

import (
	"context"
	"coord/internal/rooms"
	"fmt"
	"time"
)

func (s *Service) waitContext(ctx context.Context, input Request) (rooms.SessionContext, error) {
	return s.waitContextObserved(ctx, input, nil)
}

// waitContextObserved shares the production polling loop and exposes its
// empty-state boundary to deterministic tests. Owner events are polled from
// the room journal every 250ms; no transport listener is involved.
func (s *Service) waitContextObserved(ctx context.Context, input Request, waiting func()) (rooms.SessionContext, error) {
	seconds := input.WaitSeconds
	if seconds == 0 {
		seconds = 30
	}
	if seconds < 1 || seconds > 60 {
		return rooms.SessionContext{}, fmt.Errorf("wait_seconds must be between 1 and 60")
	}
	timer := time.NewTimer(time.Duration(seconds) * time.Second)
	defer timer.Stop()
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		c, err := s.rooms.Context(input.Auth, input.After)
		if err != nil {
			return c, err
		}
		if len(c.Events) > 0 || c.Closed {
			return c, nil
		}
		if waiting != nil {
			waiting()
		}
		select {
		case <-ctx.Done():
			return rooms.SessionContext{}, ctx.Err()
		case <-s.life.Done():
			return rooms.SessionContext{}, s.life.Err()
		case <-timer.C:
			return c, nil
		case <-tick.C:
		}
	}
}
