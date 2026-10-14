//go:build linux

package sandbox

import (
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
)

type Bubblewrap struct{ Path string }

func (b Bubblewrap) Validate(p Profile) error {
	x := b.Path
	if x == "" {
		x = "bwrap"
	}
	_, e := exec.LookPath(x)
	if e != nil {
		return errors.New("bubblewrap unavailable")
	}
	return nil
}
func (b Bubblewrap) Command(p Profile, l Launch) ([]string, []string, error) {
	// Typed roots replace the Python harness's required COORD_* variables.
	// Reject missing bindings even when a Bubblewrap executable is available.
	for _, root := range []struct{ name, path string }{
		{"ProjectRoot", l.ProjectRoot},
		{"RepositoryRoot", l.RepositoryRoot},
		{"AssignedWorktree", l.AssignedWorktree},
	} {
		if !filepath.IsAbs(root.path) {
			return nil, nil, fmt.Errorf("%s must be an absolute nonempty path", root.name)
		}
	}
	if e := b.Validate(p); e != nil {
		return nil, nil, e
	}
	if !filepath.IsAbs(l.Executable) {
		return nil, nil, errors.New("absolute executable required")
	}
	x := b.Path
	if x == "" {
		x = "bwrap"
	}
	a := []string{x, "--die-with-parent", "--new-session", "--unshare-user", "--unshare-pid", "--unshare-ipc", "--unshare-uts", "--unshare-cgroup", "--proc", "/proc", "--dev", "/dev", "--tmpfs", "/tmp", "--ro-bind", l.RepositoryRoot, l.RepositoryRoot, "--bind", l.AssignedWorktree, l.AssignedWorktree, "--bind", l.ScratchRoot, l.ScratchRoot, "--chdir", l.ProjectRoot, "--clearenv"}
	if p.NetworkDeny {
		a = append(a, "--unshare-net")
	}
	a = append(a, "--", l.Executable)
	a = append(a, l.Args...)
	return a, l.Environment, nil
}
