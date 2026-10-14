package agentsession

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type fakeHTTP struct {
	attempts int
	keys     []string
}

func (f *fakeHTTP) Do(req *http.Request) (*http.Response, error) {
	f.attempts++
	f.keys = append(f.keys, req.Header.Get("Idempotency-Key"))
	if f.attempts == 1 {
		return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader("retry")), Header: http.Header{}}, nil
	}
	body := `{"sessionId":"s1","leaseTokenDigest":"sha256:x","registryRevision":"r1","generation":1,"heartbeatSequence":1,"status":"active"}`
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
}

type noSleep struct{}

func (noSleep) Sleep(context.Context, time.Duration) error { return nil }

func TestHeartbeatRetryKeepsIdempotencyKey(t *testing.T) {
	h := &fakeHTTP{}
	c := OpenAPIClient{Config: OpenAPIConfig{BaseURL: "https://registry.invalid", Heartbeat: Operation{Method: "POST", Path: "/v1/agent-sessions/{sessionId}:heartbeat"}, StatusMap: map[string]Status{"active": StatusAdmitted}}, HTTP: h, Credential: StaticCredential("token"), Retry: RetryPolicy{MaxAttempts: 2, InitialDelay: time.Millisecond, Multiplier: 2}, Sleep: noSleep{}}
	out, err := c.Heartbeat(context.Background(), HeartbeatRequest{ProjectID: "coord", AssignmentID: "a1", SessionID: "s1", Generation: 1, Sequence: 1, LeaseTokenDigest: "sha256:x"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != StatusAdmitted {
		t.Fatalf("status=%s", out.Status)
	}
	if len(h.keys) != 2 || h.keys[0] == "" || h.keys[0] != h.keys[1] {
		t.Fatalf("keys=%v", h.keys)
	}
}

func TestHeartbeatMapsTerminalRegistryStatuses(t *testing.T) {
	for _, tc := range []struct {
		wireStatus string
		want       Status
	}{
		{wireStatus: "revoked", want: StatusFenced},
		{wireStatus: "expired", want: StatusExpired},
		{wireStatus: "rejected", want: StatusRejected},
	} {
		t.Run(tc.wireStatus, func(t *testing.T) {
			body := `{"sessionId":"s1","generation":1,"leaseTokenDigest":"sha256:x","registryRevision":"r1","heartbeatSequence":1,"status":"` + tc.wireStatus + `"}`
			c := OpenAPIClient{
				Config: OpenAPIConfig{
					BaseURL:   "https://registry.invalid",
					Heartbeat: Operation{Method: "POST", Path: "/v1/sessions/{sessionId}:heartbeat"},
					StatusMap: map[string]Status{
						"revoked":  StatusFenced,
						"expired":  StatusExpired,
						"rejected": StatusRejected,
					},
				},
				HTTP: admitResponseHTTP(body), Credential: StaticCredential("token"),
				Retry: RetryPolicy{MaxAttempts: 1}, Sleep: noSleep{},
			}
			got, err := c.Heartbeat(context.Background(), HeartbeatRequest{
				ProjectID: "coord", AssignmentID: "a1", SessionID: "s1", Generation: 1,
				Sequence: 1, LeaseTokenDigest: "sha256:x",
			})
			if err != nil {
				t.Fatalf("Heartbeat: %v", err)
			}
			if got.Status != tc.want {
				t.Fatalf("Heartbeat status = %q, want mapped status %q", got.Status, tc.want)
			}
		})
	}
}
