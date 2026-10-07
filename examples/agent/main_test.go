//go:build unix

package main

import (
	"context"
	"coord/checkpoint"
	"coord/fsm"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestHelperAgent(t *testing.T) {
	index := -1
	for i, arg := range os.Args {
		if arg == "coord-helper" {
			index = i
			break
		}
	}
	if index < 0 {
		return
	}
	mode := os.Args[index+1]
	switch mode {
	case "success":
		b, _ := io.ReadAll(os.Stdin)
		fmt.Printf("done: %s", b)
		os.Exit(0)
	case "failure":
		fmt.Fprintln(os.Stderr, "test failure")
		os.Exit(3)
	case "hang":
		time.Sleep(time.Hour)
		os.Exit(0)
	case "tree":
		marker := os.Args[index+2]
		child := exec.Command(os.Args[0], "-test.run=^TestHelperAgent$", "--", "coord-helper", "writer", marker)
		if err := child.Start(); err != nil {
			os.Exit(4)
		}
		fmt.Println("started")
		time.Sleep(time.Hour)
		os.Exit(0)
	case "writer":
		marker := os.Args[index+2]
		for i := 0; ; i++ {
			if err := os.WriteFile(marker, []byte(strconv.Itoa(i)), 0600); err != nil {
				os.Exit(5)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	os.Exit(6)
}
func testOptions(t *testing.T, mode string) options {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{{"init"}, {"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-m", "base"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git: %v: %s", err, out)
		}
	}
	return options{Worktree: dir, StateDir: t.TempDir(), Prompt: "input", Timeout: 3 * time.Second, Heartbeat: 20 * time.Millisecond, Command: []string{os.Args[0], "-test.run=^TestHelperAgent$", "--", "coord-helper", mode}}
}
func TestLocalAgentSuccessAndFailure(t *testing.T) {
	for _, mode := range []string{"success", "failure"} {
		t.Run(mode, func(t *testing.T) {
			cfg := testOptions(t, mode)
			r, err := run(context.Background(), cfg)
			if mode == "success" {
				if err != nil || r.Status != fsm.Done || r.Result == nil || r.Result.Summary != "done: input" {
					t.Fatalf("success: %+v %v", r, err)
				}
			} else {
				if err == nil || r.Status != fsm.Failed || r.Result == nil || r.Result.Outcome != "FAILURE" {
					t.Fatalf("failure: %+v %v", r, err)
				}
			}
			cp := checkpoint.File{Path: filepath.Join(cfg.StateDir, "executor.json")}
			local, loadErr := cp.Load()
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			if local[r.RunID].Status != r.Status || len(local[r.RunID].Outbox) != 0 {
				t.Fatalf("coordinator receipt not committed: canonical=%+v executor=%+v", r, local[r.RunID])
			}
		})
	}
}
func TestLocalAgentDeadlineStopsDescendantWriter(t *testing.T) {
	cfg := testOptions(t, "tree")
	cfg.Timeout = 500 * time.Millisecond
	marker := filepath.Join(cfg.Worktree, "ticks")
	cfg.Command = append(cfg.Command, marker)
	r, err := run(context.Background(), cfg)
	if err == nil || r.Status != fsm.Failed {
		t.Fatalf("timeout: %+v %v", r, err)
	}
	before, readErr := os.ReadFile(marker)
	if readErr != nil {
		t.Fatal(readErr)
	}
	time.Sleep(50 * time.Millisecond)
	after, readErr := os.ReadFile(marker)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(before) != string(after) {
		t.Fatal("descendant writer survived cancellation")
	}
	release, lockErr := (checkpoint.File{Path: filepath.Join(cfg.Worktree, ".coord-writer")}).Lock()
	if lockErr != nil {
		t.Fatal("writer lock not released")
	}
	if err = release(); err != nil {
		t.Fatal(err)
	}
}
func TestLocalAgentRefusesConcurrentWorktreeOwner(t *testing.T) {
	cfg := testOptions(t, "success")
	release, err := (checkpoint.File{Path: filepath.Join(cfg.Worktree, ".coord-writer")}).Lock()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := release(); err != nil {
			t.Errorf("release test lock: %v", err)
		}
	})
	if _, err = run(context.Background(), cfg); err == nil {
		t.Fatal("second writer admitted")
	}
}
