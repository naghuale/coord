//go:build unix

package supervisor

import (
	"context"
	"errors"
	"os/exec"
	"syscall"
	"time"
)

type UnixProcess struct {
	cmd      *exec.Cmd
	done     chan error
	exitCode int
}

func (p *UnixProcess) Start(ctx context.Context, command, env []string, dir string) error {
	if len(command) == 0 {
		return errors.New("command empty")
	}
	p.cmd = exec.CommandContext(ctx, command[0], command[1:]...)
	p.cmd.Dir = dir
	p.cmd.Env = env
	p.cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if e := p.cmd.Start(); e != nil {
		return e
	}
	p.done = make(chan error, 1)
	go func() {
		e := p.cmd.Wait()
		if p.cmd.ProcessState != nil {
			p.exitCode = p.cmd.ProcessState.ExitCode()
		}
		p.done <- e
		close(p.done)
	}()
	return nil
}
func (p *UnixProcess) Done() <-chan error { return p.done }
func (p *UnixProcess) ExitCode() int      { return p.exitCode }
func (p *UnixProcess) TerminateGroup(ctx context.Context, grace time.Duration) error {
	if p.cmd == nil {
		return nil
	}
	pg, e := syscall.Getpgid(p.cmd.Process.Pid)
	if e != nil {
		return e
	}
	_ = syscall.Kill(-pg, syscall.SIGTERM)
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-p.done:
		return nil
	case <-ctx.Done():
		_ = syscall.Kill(-pg, syscall.SIGKILL)
		return ctx.Err()
	case <-timer.C:
		return syscall.Kill(-pg, syscall.SIGKILL)
	}
}
