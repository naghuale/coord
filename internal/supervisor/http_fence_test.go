package supervisor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"coord/internal/agentsession"
)

// TestHTTPAdapterSupervisorFence verifies parity with Python
// test_http_adapter_and_supervisor_fence: the supervisor loop connects to a
// real HTTP registry (httptest.Server), the registry reports a generation
// mismatch, and the receipt shows fenced=true.
//
// This covers the full HTTP path from the OpenAPIClient through the Loop, using
// a local test server instead of the Python session_supervisor.py + mock server.
func newHTTPFenceRegistry(t *testing.T) *agentsession.OpenAPIClient {
	t.Helper()
	// Mock HTTP registry: returns a heartbeat response with generation 2 while
	// the local session has generation 1 — triggers FenceGeneration.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && contains(r.URL.Path, ":heartbeat"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"sessionId":         "s1",
				"leaseTokenDigest":  "sha256:lease",
				"registryRevision":  "r1",
				"generation":        2, // mismatch: local is 1
				"heartbeatSequence": 1,
				"status":            "ADMITTED",
			})
		case r.Method == "POST" && contains(r.URL.Path, ":fence"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		case r.Method == "POST" && contains(r.URL.Path, ":complete"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	registry := &agentsession.OpenAPIClient{
		Config: agentsession.OpenAPIConfig{
			BaseURL:         srv.URL,
			Heartbeat:       agentsession.Operation{Method: "POST", Path: "/v1/sessions/{sessionId}:heartbeat"},
			Fence:           agentsession.Operation{Method: "POST", Path: "/v1/sessions/{sessionId}:fence"},
			Complete:        agentsession.Operation{Method: "POST", Path: "/v1/sessions/{sessionId}:complete"},
			StatusMap:       map[string]agentsession.Status{"ADMITTED": agentsession.StatusAdmitted},
			IdempotencyHead: "Idempotency-Key",
			Timeout:         2 * time.Second,
		},
		HTTP:       srv.Client(),
		Credential: agentsession.StaticCredential("test-token"),
		Retry:      agentsession.RetryPolicy{MaxAttempts: 1},
	}

	return registry
}

func TestHTTPAdapterSupervisorFence(t *testing.T) {
	registry := newHTTPFenceRegistry(t)
	p := &fakeProcess{}
	tick := &fakeTicker{ch: make(chan time.Time, 1)}
	loop := Loop{
		Registry: registry,
		Process:  p,
		Clock:    &fakeClock{ticker: tick},
		Policy: Policy{
			HeartbeatInterval:      time.Second,
			HeartbeatTimeout:       2 * time.Second,
			MaxConsecutiveFailures: 2,
			GracefulStopTimeout:    time.Second,
		},
	}

	result := make(chan Receipt, 1)
	go func() {
		receipt, _ := loop.Run(context.Background(), RunRequest{
			Session: agentsession.Session{
				SessionID:        "s1",
				Generation:       1, // local generation 1; registry returns 2
				LeaseTokenDigest: "sha256:lease",
				Status:           agentsession.StatusAdmitted,
			},
			ProjectID:        "coord",
			AssignmentID:     "a1",
			LeaseTokenDigest: "sha256:lease",
			Command:          []string{"agent"},
		})
		result <- receipt
	}()

	// Trigger first heartbeat.
	tick.ch <- time.Now()

	select {
	case receipt := <-result:
		if !receipt.Fenced {
			t.Errorf("expected receipt.Fenced=true, got %+v", receipt)
		}
		if receipt.FenceReason != FenceGeneration {
			t.Errorf("expected FenceReason=%s, got %s", FenceGeneration, receipt.FenceReason)
		}
		if !p.terminated {
			t.Error("generation mismatch did not terminate the process")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for supervisor fence receipt")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		findSubstr(s, substr) >= 0)
}

func findSubstr(s, sub string) int {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
