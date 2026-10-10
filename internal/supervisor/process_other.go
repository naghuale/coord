//go:build !unix

package supervisor

import (
	"context"
	"errors"
	"time"
)

type UnixProcess struct{}

func (*UnixProcess) Start(context.Context, []string, []string, string) error {
	return errors.New("unsupported")
}
func (*UnixProcess) Done() <-chan error { c := make(chan error); close(c); return c }
func (*UnixProcess) TerminateGroup(context.Context, time.Duration) error {
	return errors.New("unsupported")
}
func (*UnixProcess) ExitCode() int { return -1 }
