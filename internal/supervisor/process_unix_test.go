//go:build unix

package supervisor

import (
	"context"
	"testing"
	"time"
)

func TestUnixProcessStartAndWait(t *testing.T) {
	process := &UnixProcess{}
	if err := process.Start(context.Background(), []string{"/bin/echo", "hello"}, nil, t.TempDir()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	select {
	case waitErr, ok := <-process.Done():
		if !ok {
			t.Fatal("Done channel closed without a process result")
		}
		if waitErr != nil {
			t.Fatalf("process wait: %v", waitErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for process")
	}
	if got := process.ExitCode(); got != 0 {
		t.Fatalf("ExitCode() = %d, want 0", got)
	}
}

func TestUnixProcessTerminateGroup(t *testing.T) {
	process := &UnixProcess{}
	if err := process.Start(context.Background(), []string{"/bin/sleep", "60"}, nil, t.TempDir()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := process.TerminateGroup(ctx, 100*time.Millisecond); err != nil {
		t.Fatalf("TerminateGroup: %v", err)
	}
	select {
	case <-process.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("process not terminated within 2 seconds")
	}
	if got := process.ExitCode(); got == 0 {
		t.Fatal("terminated process has exit code 0")
	}
}
