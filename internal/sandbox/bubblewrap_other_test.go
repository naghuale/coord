//go:build !linux

package sandbox

import (
	"os"
	"testing"
)

func TestNonLinuxSandboxFailsClosed(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	backend := Bubblewrap{Path: executable}
	if err := backend.Validate(Profile{}); err == nil {
		t.Fatal("unsupported backend admitted a launch")
	}
	if args, env, err := backend.Command(Profile{}, Launch{}); err == nil || args != nil || env != nil {
		t.Fatalf("unsupported backend returned command: args=%v env=%v err=%v", args, env, err)
	}
}
