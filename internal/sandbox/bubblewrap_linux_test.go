//go:build linux

package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// No Bubblewrap invocation: an existing executable makes backend discovery
// succeed so missing bindings cannot be masked by BUBBLEWRAP_UNAVAILABLE.
func TestSandboxFailsClosedWithoutWorkspaceRoots(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	valid := Launch{ProjectRoot: root, RepositoryRoot: filepath.Join(root, "repo"), AssignedWorktree: filepath.Join(root, "worktree"), ScratchRoot: root, Executable: executable}
	backend := Bubblewrap{Path: executable}
	if _, _, err := backend.Command(Profile{}, valid); err != nil {
		t.Fatalf("valid bindings rejected: %v", err)
	}
	for _, field := range []string{"ProjectRoot", "RepositoryRoot", "AssignedWorktree"} {
		for _, value := range []string{"", "relative"} {
			t.Run(field+"/"+value, func(t *testing.T) {
				launch := valid
				switch field {
				case "ProjectRoot":
					launch.ProjectRoot = value
				case "RepositoryRoot":
					launch.RepositoryRoot = value
				case "AssignedWorktree":
					launch.AssignedWorktree = value
				}
				args, env, err := backend.Command(Profile{}, launch)
				if err == nil || !strings.Contains(err.Error(), field) || args != nil || env != nil {
					t.Fatalf("missing/relative %s: args=%v env=%v err=%v", field, args, env, err)
				}
			})
		}
	}
}
