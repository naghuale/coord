//go:build unix

package main

import (
	"context"
	"coord"
	"coord/checkpoint"
	"coord/fsm"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
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
	case "slow":
		time.Sleep(2 * time.Second)
		fmt.Print("slow done")
		os.Exit(0)
	case "hang":
		time.Sleep(time.Hour)
		os.Exit(0)
	case "tree":
		marker := os.Args[index+2]
		child := exec.Command(os.Args[0], "-test.run=^TestHelperAgent$", "--", "coord-helper", "writer", marker)
		if err := child.Start(); err != nil {
			os.Exit(4)
		}
		// Wait for the first write so the deadline always meets a live writer.
		for {
			if _, err := os.Stat(marker); err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
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
	for _, args := range [][]string{{"-c", "commit.gpgsign=false", "init"}, {"-c", "commit.gpgsign=false", "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-m", "base"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git: %v: %s", err, out)
		}
	}
	return options{Worktree: dir, StateDir: t.TempDir(), Prompt: "input", Timeout: 3 * time.Second, Heartbeat: time.Second, Command: []string{os.Args[0], "-test.run=^TestHelperAgent$", "--", "coord-helper", mode}}
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
func TestLocalAgentPeriodicHeartbeatKeepsLongCommandLeased(t *testing.T) {
	// The command outlives several leases (3 x Heartbeat). A missed lease is
	// only visible as a coordinator stall, so the test reads its metrics.
	var observed *coord.Coordinator
	cfg := testOptions(t, "slow")
	cfg.Heartbeat = 500 * time.Millisecond
	cfg.Timeout = 10 * time.Second
	r, err := runWithObserver(context.Background(), cfg, func(c *coord.Coordinator) { observed = c })
	if err != nil || r.Status != fsm.Done || r.Result == nil || r.Result.Summary != "slow done" {
		t.Fatalf("long command: %+v %v", r, err)
	}
	if observed == nil {
		t.Fatal("coordinator was not observed")
	}
	metrics := httptest.NewRecorder()
	observed.Metrics.ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(metrics.Body.String(), "\ncoord_stall_total 0\n") {
		t.Fatalf("lease expired while the command ran:\n%s", metrics.Body.String())
	}
	// STARTED and RESULT_READY are sent outside the loop; a 2s command with a
	// 500ms heartbeat must add at least two periodic heartbeats.
	var received int
	for _, line := range strings.Split(metrics.Body.String(), "\n") {
		if value, ok := strings.CutPrefix(line, "coord_heartbeat_received_total "); ok {
			received, _ = strconv.Atoi(value)
		}
	}
	if received < 4 {
		t.Fatalf("no periodic heartbeats while the command ran: received %d", received)
	}
}
func TestLocalAgentDeadlineStopsDescendantWriter(t *testing.T) {
	cfg := testOptions(t, "tree")
	// Two race-instrumented process starts must fit before the deadline.
	cfg.Timeout = 3 * time.Second
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
