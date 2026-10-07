package coord

import (
	"bytes"
	"coord/fsm"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestControlMalformedBodiesReturn400WithoutChangingSnapshot(t *testing.T) {
	oversized, err := json.Marshal(controlRequest{
		Run: "run", Fence: 1, Claim: "claim", Reason: strings.Repeat("x", 9000),
	})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		body string
	}{
		{name: "malformed", body: `{"`},
		{name: "unknown field", body: `{"run":"run","fence":1,"claim":"claim","extra":true}`},
		{name: "truncated", body: `{"run":"run","fence":1,"claim":"claim"`},
		{name: "oversized", body: string(oversized)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t)
			f.assign(t)
			run, ok := f.s.Get("run")
			if !ok {
				t.Fatal("assigned run missing")
			}
			run.Status = fsm.Stalled
			run.RecoveryNeeded = true
			if err := f.c.put(run); err != nil {
				t.Fatal(err)
			}

			before, err := os.ReadFile(f.path)
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodPost, "/claim", strings.NewReader(tc.body))
			request.Header.Set("Authorization", "Bearer token")
			response := httptest.NewRecorder()
			f.c.ControlHandler("token").ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status=%d, want 400: %s", response.Code, response.Body)
			}
			after, err := os.ReadFile(f.path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("malformed control request changed the durable snapshot")
			}
			run, _ = f.s.Get("run")
			if run.RecoveryClaim != "" {
				t.Fatalf("malformed request changed recovery claim: %q", run.RecoveryClaim)
			}
		})
	}
}

func TestControlValidClaimChangesSnapshot(t *testing.T) {
	f := setup(t)
	f.assign(t)
	run, ok := f.s.Get("run")
	if !ok {
		t.Fatal("assigned run missing")
	}
	run.Status = fsm.Stalled
	run.RecoveryNeeded = true
	if err := f.c.put(run); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(f.path)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/claim", strings.NewReader(`{"run":"run","fence":1,"claim":"claim"}`))
	request.Header.Set("Authorization", "Bearer token")
	response := httptest.NewRecorder()
	f.c.ControlHandler("token").ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("valid claim status=%d, want 204: %s", response.Code, response.Body)
	}
	after, err := os.ReadFile(f.path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(before, after) {
		t.Fatal("valid claim did not change the durable snapshot")
	}
	run, _ = f.s.Get("run")
	if run.RecoveryClaim != "claim" {
		t.Fatalf("recovery claim=%q, want claim", run.RecoveryClaim)
	}
}
