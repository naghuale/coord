package telepathy

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
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
	os.Exit(0)
}
func TestRPCChecksVersionAndToolFailure(t *testing.T) {
	for _, bad := range []bool{false, true} {
		env := append(os.Environ(), "COORD_MCP_HELPER=1")
		if bad {
			env = append(env, "COORD_MCP_BAD_VERSION=1")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		p, err := Start(ctx, []string{os.Args[0], "-test.run=^TestMCPHelper$"}, t.TempDir(), env)
		if bad {
			cancel()
			if err == nil {
				_ = p.Close()
				t.Fatal("version mismatch accepted")
			}
			continue
		}
		if err != nil {
			cancel()
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
		cancel()
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
