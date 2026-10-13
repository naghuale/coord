package agentsession

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// fakeAdmitHTTP returns a mock ADMITTED response on the first call.
type fakeAdmitHTTP struct{}

func (fakeAdmitHTTP) Do(_ *http.Request) (*http.Response, error) {
	body := `{"sessionId":"s1","leaseTokenDigest":"sha256:x","registryRevision":"r1","generation":1,"status":"ADMITTED"}`
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
}

// TestRegistryClientMockAdmission verifies parity with Python
// test_registry_client_mock: an Admit request to a mock registry returns
// status ADMITTED. This covers the session_registry_client.py admit path in Go.
func TestRegistryClientMockAdmission(t *testing.T) {
	leaseToken := "secret"
	digest := "sha256:" + func() string {
		h := sha256.Sum256([]byte(leaseToken))
		return hex.EncodeToString(h[:])
	}()

	c := OpenAPIClient{
		Config: OpenAPIConfig{
			BaseURL: "https://registry.invalid",
			Admit:   Operation{Method: "POST", Path: "/v1/agent-sessions:admit"},
			StatusMap: map[string]Status{
				"ADMITTED": StatusAdmitted,
			},
			Timeout: time.Second,
		},
		HTTP:       fakeAdmitHTTP{},
		Credential: StaticCredential("token"),
		Retry:      RetryPolicy{MaxAttempts: 1},
		Sleep:      noSleep{},
	}

	out, err := c.Admit(context.Background(), AdmitRequest{
		ProjectID:        "coord",
		AssignmentID:     "a1",
		LeaseTokenDigest: digest,
	})
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	if out.Status != StatusAdmitted {
		t.Errorf("expected status ADMITTED, got %s", out.Status)
	}
	if out.SessionID != "s1" {
		t.Errorf("expected sessionId=s1, got %q", out.SessionID)
	}
}
