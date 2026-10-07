//go:build unix

package main

import (
	"bufio"
	"context"
	"coord/internal/localchat"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const testOwnerKey = "01234567890123456789012345678901"

type sessionCLIRequest struct {
	Path, Method, Owner string
	Body                localchat.Request
}

func newSessionCLIHarness(t *testing.T, withOwner bool) (*localchat.Client, string, <-chan sessionCLIRequest) {
	t.Helper()
	state, err := os.MkdirTemp("/tmp", "coord-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(state) })
	if withOwner {
		if err := os.WriteFile(filepath.Join(state, "owner.key"), []byte(testOwnerKey), 0600); err != nil {
			t.Fatal(err)
		}
	}
	listener, err := net.Listen("unix", localchat.SocketPath(state))
	if err != nil {
		t.Fatal(err)
	}
	requests := make(chan sessionCLIRequest, 32)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body localchat.Request
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		requests <- sessionCLIRequest{Path: r.URL.Path, Method: r.Method, Owner: r.Header.Get("X-Coord-Owner"), Body: body}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{}"))
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	client := localchat.NewClient(state)
	t.Cleanup(func() { client.Close() })
	return client, state, requests
}

func TestSessionBlockAndUnblockUseOwnerCapability(t *testing.T) {
	client, state, requests := newSessionCLIHarness(t, true)
	for _, tc := range []struct {
		args       []string
		want       sessionCLIRequest
		wantOutput string
	}{
		{
			args:       []string{"block", "session-1", "--reason", "blocked_provider_quota", "--retry-at", "2026-10-08T10:00:00Z"},
			want:       sessionCLIRequest{Path: "/session-block", Method: http.MethodPost, Owner: testOwnerKey, Body: localchat.Request{Value: "session-1", Reason: "blocked_provider_quota", Source: "owner", RetryAt: "2026-10-08T10:00:00Z"}},
			wantOutput: "session=session-1 blocker=blocked_provider_quota source=owner retry_at=2026-10-08T10:00:00Z\n",
		},
		{
			args:       []string{"unblock", "session-1"},
			want:       sessionCLIRequest{Path: "/session-unblock", Method: http.MethodPost, Owner: testOwnerKey, Body: localchat.Request{Value: "session-1"}},
			wantOutput: "session=session-1 blocker=cleared\n",
		},
		{
			args:       []string{"block", "session-2", "--reason", "blocked_provider_quota"},
			want:       sessionCLIRequest{Path: "/session-block", Method: http.MethodPost, Owner: testOwnerKey, Body: localchat.Request{Value: "session-2", Reason: "blocked_provider_quota", Source: "owner"}},
			wantOutput: "session=session-2 blocker=blocked_provider_quota source=owner retry_at=-\n",
		},
	} {
		var out, diagnostic strings.Builder
		if err := runSessionCLI(context.Background(), client, "session", tc.args, state, strings.NewReader(""), &out, &diagnostic); err != nil {
			t.Fatalf("run %v: %v", tc.args, err)
		}
		got := <-requests
		if got.Path != tc.want.Path || got.Method != tc.want.Method || got.Owner != tc.want.Owner || !reflect.DeepEqual(got.Body, tc.want.Body) {
			t.Fatalf("request for %v = %+v, want %+v", tc.args, got, tc.want)
		}
		if out.String() != tc.wantOutput {
			t.Fatalf("output for %v = %q, want %q", tc.args, out.String(), tc.wantOutput)
		}
	}
}

func TestSessionBlockRejectsInvalidArgsBeforeRequest(t *testing.T) {
	// No owner.key is installed: validation must fail before owner state is read.
	client, state, requests := newSessionCLIHarness(t, false)
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{name: "block without reason", args: []string{"block", "session-1"}, want: "block requires --reason REASON [--retry-at RFC3339]"},
		{name: "block extra positional argument", args: []string{"block", "session-1", "--reason", "blocked_provider_quota", "extra"}, want: "block requires --reason REASON [--retry-at RFC3339]"},
		{name: "block rejects as", args: []string{"block", "session-1", "--reason", "blocked_provider_quota", "--as", ""}, want: "block uses owner capability; --as is not accepted"},
		{name: "unblock rejects as", args: []string{"unblock", "session-1", "--as", ""}, want: "unblock uses owner capability; --as is not accepted"},
		{name: "unblock rejects reason", args: []string{"unblock", "session-1", "--reason", "blocked_provider_quota"}, want: "unblock takes only a session ID; inspect the checkpoint before clearing the blocker"},
		{name: "unblock rejects retry-at", args: []string{"unblock", "session-1", "--retry-at", "2026-10-08T10:00:00Z"}, want: "unblock takes only a session ID; inspect the checkpoint before clearing the blocker"},
		{name: "unblock rejects unrelated flag", args: []string{"unblock", "session-1", "--agent-id", "agent-1"}, want: "unblock takes only a session ID; inspect the checkpoint before clearing the blocker"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := runSessionCLI(context.Background(), client, "session", tc.args, state, strings.NewReader(""), &strings.Builder{}, &strings.Builder{})
			if err == nil || err.Error() != tc.want {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
			if got := len(requests); got != 0 {
				t.Fatalf("invalid arguments sent %d HTTP requests", got)
			}
		})
	}
}

func TestSessionUnblockReferencesRemainExecutable(t *testing.T) {
	client, state, requests := newSessionCLIHarness(t, true)
	paths := []string{"../../internal/rooms/sessions.go", "../../internal/rooms/blockers.go"}
	for _, path := range paths {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		args, err := unblockCommandFromText(string(body))
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		assertSessionCLICommand(t, client, state, requests, args, "/session-unblock")
	}

	docPath := os.Getenv("CREWFLOW_SESSION_CLI_DOC")
	if docPath == "" {
		docPath = "../../docs/SESSION_CLI.md"
	}
	doc, err := os.Open(docPath)
	if err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(doc)
	var commands [][]string
	var current string
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if current == "" && strings.HasPrefix(line, "bin/coord --state state/team session ") {
			current = strings.TrimSuffix(line, "\\")
		} else if current != "" {
			current += " " + strings.TrimSuffix(line, "\\")
		}
		if current != "" && !strings.HasSuffix(strings.TrimSpace(scanner.Text()), "\\") {
			fields := strings.Fields(current)
			for i := range fields {
				if fields[i] == "SESSION_ID" {
					fields[i] = "session-1"
				}
			}
			if len(fields) < 5 || fields[3] != "session" {
				t.Fatalf("cannot parse documented command %q", current)
			}
			if fields[4] == "block" || fields[4] == "unblock" {
				commands = append(commands, fields[4:])
			}
			current = ""
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if err := doc.Close(); err != nil {
		t.Fatal(err)
	}
	commandCounts := map[string]int{}
	for _, args := range commands {
		commandCounts[args[0]]++
		wantPath := "/session-" + args[0]
		assertSessionCLICommand(t, client, state, requests, args, wantPath)
	}
	for _, action := range []string{"block", "unblock"} {
		if commandCounts[action] == 0 {
			t.Errorf("SESSION_CLI.md has no executable session %s example", action)
		}
	}
}

func unblockCommandFromText(text string) ([]string, error) {
	const marker = "coord session unblock "
	index := strings.Index(text, marker)
	if index < 0 {
		return nil, fmt.Errorf("no coord session unblock command")
	}
	fields := strings.Fields(text[index:])
	if len(fields) < 4 || fields[0] != "coord" || fields[1] != "session" || fields[2] != "unblock" {
		return nil, fmt.Errorf("malformed command reference %q", text[index:])
	}
	if fields[3] != "<session>" && fields[3] != "%s" {
		return nil, fmt.Errorf("unsupported session placeholder %q", fields[3])
	}
	return []string{fields[2], "session-1"}, nil
}

func assertSessionCLICommand(t *testing.T, client *localchat.Client, state string, requests <-chan sessionCLIRequest, args []string, wantPath string) {
	t.Helper()
	if err := runSessionCLI(context.Background(), client, "session", args, state, strings.NewReader(""), &strings.Builder{}, &strings.Builder{}); err != nil {
		t.Fatalf("documented command %v: %v", args, err)
	}
	got := <-requests
	if got.Path != wantPath {
		t.Fatalf("documented command %v called %s, want %s", args, got.Path, wantPath)
	}
}
