//go:build unix

package supervisor

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

// RunWithSignals runs the supervisor loop and cancels it when the process
// receives SIGTERM or SIGINT.
func (s *Loop) RunWithSignals(ctx context.Context, req RunRequest) (Receipt, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(ch)

	go func() {
		select {
		case <-ch:
			cancel()
		case <-ctx.Done():
		}
	}()

	return s.Run(ctx, req)
}
