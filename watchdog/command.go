package watchdog

import (
	"context"
	"coord/fsm"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

type Control interface {
	Candidates(context.Context) ([]fsm.Run, error)
	Claim(context.Context, string, uint64) (bool, error)
	Release(context.Context, string, uint64) error
	Transfer(context.Context, string, string, uint64) error
	Fail(context.Context, string, uint64, string) error
}

// CommandBackend uses trusted supervisor executables, without shell interpolation.
// FenceCommand must stop the old process and verify its writer lock was released.
// SpawnCommand prints one session address and starts an executor awaiting TASK.
type CommandBackend struct {
	Control
	FenceCommand, SpawnCommand, StopCommand []string
}

func runCommand(ctx context.Context, args []string, extra ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if len(args) == 0 {
		return "", fmt.Errorf("supervisor command required")
	}
	cmd := exec.CommandContext(ctx, args[0], append(append([]string{}, args[1:]...), extra...)...)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("supervisor command failed: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}
func (b CommandBackend) FenceOld(ctx context.Context, r fsm.Run) error {
	_, err := runCommand(ctx, b.FenceCommand, r.OwnerSession, r.RunID, fmt.Sprint(r.Fence))
	return err
}
func (b CommandBackend) VerifyWorktree(ctx context.Context, r fsm.Run) error {
	sha, err := runCommand(ctx, []string{"git", "-C", r.Worktree, "rev-parse", "HEAD"})
	if err != nil {
		return err
	}
	if sha != r.TargetSHA {
		return fmt.Errorf("worktree HEAD mismatch")
	}
	_, err = runCommand(ctx, []string{"git", "-C", r.Worktree, "diff", "--check"})
	return err
}
func (b CommandBackend) Spawn(ctx context.Context, r fsm.Run) (string, error) {
	s, err := runCommand(ctx, b.SpawnCommand, r.RunID, fmt.Sprint(r.Attempt), r.TargetSHA, r.Worktree, fmt.Sprint(r.Fence+1))
	if err == nil && (s == "" || strings.ContainsAny(s, "\n\r \t") || s == r.OwnerSession) {
		return "", fmt.Errorf("invalid spawned session address")
	}
	return s, err
}
func (b CommandBackend) Stop(ctx context.Context, s string) error {
	_, err := runCommand(ctx, b.StopCommand, s)
	return err
}
