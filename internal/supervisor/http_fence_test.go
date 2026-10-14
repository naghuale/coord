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

// TestHTTPAdapterSupervisorFence covers generation mismatch over the real HTTP
// registry adapter. Revoked-status parity is exercised separately below.
func newHTTPFenceRegistry(t *testing.T) *agentsession.OpenAPIClient {
	return newHTTPRegistry(t, func(uint64) heartbeatWireResponse {
		return heartbeatWireResponse{generation: 2, leaseDigest: "sha256:lease", status: "ADMITTED"}
	}, map[string]agentsession.Status{"ADMITTED": agentsession.StatusAdmitted})
}

func newHTTPRevocationRegistry(t *testing.T) *agentsession.OpenAPIClient {
	return newHTTPRegistry(t, func(sequence uint64) heartbeatWireResponse {
		status := "active"
		if sequence >= 2 {
			status = "revoked"
		}
		return heartbeatWireResponse{generation: 1, leaseDigest: "sha256:lease", status: status}
	}, map[string]agentsession.Status{
		"active":  agentsession.StatusAdmitted,
		"revoked": agentsession.StatusFenced,
	})
}

type heartbeatWireResponse struct {
	generation  uint64
	leaseDigest string
	status      string
}

func newHTTPRegistry(t *testing.T, response func(uint64) heartbeatWireResponse, statusMap map[string]agentsession.Status) *agentsession.OpenAPIClient {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && contains(r.URL.Path, ":heartbeat"):
			var request struct {
				HeartbeatSequence uint64 `json:"heartbeatSequence"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				http.Error(w, "invalid heartbeat", http.StatusBadRequest)
				return
			}
			wire := response(request.HeartbeatSequence)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"sessionId":         "s1",
				"leaseTokenDigest":  wire.leaseDigest,
				"registryRevision":  "r1",
				"generation":        wire.generation,
				"heartbeatSequence": request.HeartbeatSequence,
				"status":            wire.status,
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
			StatusMap:       statusMap,
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

func TestHTTPAdapterSupervisorFenceRevoked(t *testing.T) {
	registry := newHTTPRevocationRegistry(t)
	assertHTTPRegistryFence(t, registry, []time.Time{time.Now(), time.Now()}, FenceRegistryStatus)
}

func TestHTTPAdapterSupervisorFenceLeaseDigest(t *testing.T) {
	registry := newHTTPRegistry(t, func(uint64) heartbeatWireResponse {
		return heartbeatWireResponse{generation: 1, leaseDigest: "sha256:other", status: "active"}
	}, map[string]agentsession.Status{"active": agentsession.StatusAdmitted})
	assertHTTPRegistryFence(t, registry, []time.Time{time.Now()}, FenceLease)
}

func assertHTTPRegistryFence(t *testing.T, registry agentsession.Registry, ticks []time.Time, wantReason FenceReason) {
	t.Helper()
	p := &fakeProcess{}
	tick := &fakeTicker{ch: make(chan time.Time, len(ticks))}
	for _, at := range ticks {
		tick.ch <- at
	}
	loop := Loop{
		Registry: registry,
		Process:  p,
		Clock:    &fakeClock{ticker: tick},
		Policy: Policy{
			HeartbeatInterval:      time.Second,
			HeartbeatTimeout:       time.Second,
			MaxConsecutiveFailures: 1,
			GracefulStopTimeout:    time.Second,
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	receipt, err := loop.Run(ctx, RunRequest{
		Session: agentsession.Session{
			SessionID: "s1", Generation: 1, LeaseTokenDigest: "sha256:lease", Status: agentsession.StatusAdmitted,
		},
		ProjectID: "coord", AssignmentID: "a1", LeaseTokenDigest: "sha256:lease", Command: []string{"agent"},
	})
	if err == nil || !receipt.Fenced || receipt.FenceReason != wantReason {
		t.Fatalf("expected fenced receipt reason %s and an error, got receipt=%+v err=%v", wantReason, receipt, err)
	}
	if !p.terminated {
		t.Fatal("registry fence did not terminate the process")
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
