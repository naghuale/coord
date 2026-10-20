package telepathy

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMCPHelper(t *testing.T) {
	if os.Getenv("COORD_MCP_HELPER") != "1" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var request struct {
			ID     uint64 `json:"id"`
			Method string `json:"method"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			os.Exit(3)
		}
		if request.ID == 0 {
			continue
		}
		var result any
		if request.Method == "initialize" {
			version := "0.8.0"
			if os.Getenv("COORD_MCP_BAD_VERSION") == "1" {
				version = "9"
			}
			if os.Getenv("COORD_MCP_VERSION_090") == "1" {
				version = "0.9.0"
			}
			result = map[string]any{"protocolVersion": "2024-11-05", "serverInfo": map[string]any{"name": "telepathy", "version": version}}
		} else {
			if os.Getenv("COORD_MCP_HANG") == "1" {
				time.Sleep(time.Minute)
			}
			result = map[string]any{"isError": true, "content": []any{map[string]any{"type": "text", "text": "Rate limit"}}}
		}
		if err := json.NewEncoder(os.Stdout).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result}); err != nil {
			os.Exit(4)
		}
	}
	// EOF on stdin is the documented shutdown of an MCP stdio server: the peer
	// closed the session, so exiting cleanly is correct behaviour. COORD_MCP_EXIT
	// makes the helper write a marker so a test can wait for the real exit
	// instead of sleeping.
	if path := os.Getenv("COORD_MCP_EXIT_MARKER"); path != "" {
		_ = os.WriteFile(path, []byte("exited"), 0o600)
	}
	os.Exit(0)
}
func TestRPCChecksVersionAndToolFailure(t *testing.T) {
	for _, tc := range []struct {
		name    string
		envVars []string
		wantErr bool
	}{
		{name: "v0.8.0 accepted", envVars: nil, wantErr: false},
		{name: "v0.9.0 accepted", envVars: []string{"COORD_MCP_VERSION_090=1"}, wantErr: false},
		{name: "bad version rejected", envVars: []string{"COORD_MCP_BAD_VERSION=1"}, wantErr: true},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			env := append(os.Environ(), "COORD_MCP_HELPER=1")
			env = append(env, tc.envVars...)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			p, err := Start(ctx, []string{os.Args[0], "-test.run=^TestMCPHelper$"}, t.TempDir(), env)
			if tc.wantErr {
				if err == nil {
					_ = p.Close()
					t.Fatal("version mismatch accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			_, err = p.Call(ctx, "send_message", map[string]any{})
			if err == nil || !strings.Contains(err.Error(), "Rate limit") {
				t.Fatalf("isError hidden: %v", err)
			}
			if err = p.Close(); err != nil {
				t.Fatal(err)
			}
			if err = p.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestRPCCancellationReapsProcess(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p, err := Start(ctx, []string{os.Args[0], "-test.run=^TestMCPHelper$"}, t.TempDir(), append(os.Environ(), "COORD_MCP_HELPER=1", "COORD_MCP_HANG=1"))
	if err != nil {
		t.Fatal(err)
	}
	call, stop := context.WithTimeout(ctx, 30*time.Millisecond)
	defer stop()
	if _, err = p.Call(call, "read_messages", map[string]any{}); err == nil {
		t.Fatal("cancelled call succeeded")
	}
	if err = p.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestCloseAfterCleanExitIsNotAnError reproduces the race deterministically: the
// server exits with code 0 on EOF stdin, so cmd.Wait reports our own cancellation
// instead of the exit status. The test waits for the exit marker written by the
// helper and then for the process to be reaped, so no sleep is used as proof.
func TestCloseAfterCleanExitIsNotAnError(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "exited")
	env := append(os.Environ(), "COORD_MCP_HELPER=1", "COORD_MCP_EXIT_MARKER="+marker)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	p, err := Start(ctx, []string{os.Args[0], "-test.run=^TestMCPHelper$"}, t.TempDir(), env)
	if err != nil {
		t.Fatal(err)
	}
	// Закрываем stdin: helper видит EOF и выходит сам.
	if err = p.input.Close(); err != nil {
		t.Fatal(err)
	}
	// Факт завершения: helper записал маркер непосредственно перед выходом.
	// Close сам отменяет life и дожидается Wait, поэтому отдельного ожидания
	// p.done здесь нет: канал закрывается только после отмены, а не раньше.
	waitForFile(t, marker)
	if err = p.Close(); err != nil {
		t.Fatalf("штатное закрытие вернуло ошибку: %v", err)
	}
	if err = p.Close(); err != nil {
		t.Fatalf("повторное закрытие вернуло ошибку: %v", err)
	}
}

// waitForFile blocks until the helper has written its exit marker. Polling is
// used only to wait for a fact, never to prove the absence of an event.
func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("helper не записал маркер выхода %s", path)
}
