//go:build unix

package localchat

import (
	"bytes"
	"coord/internal/rooms"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func malformedHTTPFixture(t *testing.T) (*Service, rooms.Room, rooms.Member) {
	t.Helper()
	s, _ := fixture(t)
	r, err := s.rooms.Create("invalid-input-task", "test room", "")
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.rooms.Assign(r.ID, "agent", "executor", "test instructions", "")
	if err != nil {
		t.Fatal(err)
	}
	// Keep the durable rooms snapshot stable between bytewise comparisons.
	s.cancel()
	<-s.roomDone
	return s, r, m
}

func roomsSnapshotBytes(t *testing.T, s *Service) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(s.cfg.State, "rooms.json"))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestMalformedPOSTBodiesReturn400InvalidInputWithoutChangingRoomsState(t *testing.T) {
	s, _, _ := malformedHTTPFixture(t)
	oversized := `{"instructions":"` + strings.Repeat("x", 2<<20) + `"}`
	bodies := []struct {
		name string
		body string
	}{
		{name: "malformed", body: `{`},
		{name: "unknown field", body: `{"unknown_field":true}`},
		{name: "trailing JSON", body: `{} {}`},
		{name: "oversized", body: oversized},
	}
	postEndpoints := []string{
		"/owner-start", "/session-block", "/session-unblock", "/session-register",
		"/session-registration", "/session-regulation", "/session-ready", "/session-invite",
		"/session-assign", "/session-resume", "/session-wait", "/session-context",
		"/session-say", "/session-ack", "/session-checkpoint", "/history-export",
		"/invite", "/say", "/events", "/room-close", "/connect", "/accept",
		"/reply", "/chat", "/stop",
	}

	for _, endpoint := range postEndpoints {
		if got := expectedMethod(strings.TrimPrefix(endpoint, "/")); got != http.MethodPost {
			t.Fatalf("test endpoint %s uses %s, want POST", endpoint, got)
		}
		for _, body := range bodies {
			t.Run(strings.TrimPrefix(endpoint, "/")+"/"+body.name, func(t *testing.T) {
				before := roomsSnapshotBytes(t, s)
				request := httptest.NewRequest(http.MethodPost, endpoint, strings.NewReader(body.body))
				request.Header.Set("X-Coord-Owner", s.owner)
				response := httptest.NewRecorder()
				s.Handler(nil).ServeHTTP(response, request)
				var result Response
				if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
					t.Fatalf("decode response: %v; body=%q", err, response.Body.String())
				}
				if response.Code != http.StatusBadRequest || result.Code != "invalid_input" || result.Error == "" {
					t.Fatalf("status=%d code=%q error=%q, want 400 invalid_input with error", response.Code, result.Code, result.Error)
				}
				after := roomsSnapshotBytes(t, s)
				if !bytes.Equal(before, after) {
					t.Fatal("malformed POST changed the durable rooms snapshot")
				}
			})
		}
	}
}

func TestSessionBlockInvalidFieldsReturn400WithoutChangingRoomsState(t *testing.T) {
	s, _, member := malformedHTTPFixture(t)
	cases := []struct {
		name   string
		reason string
		source string
	}{
		{name: "unknown reason", reason: "not_a_blocker", source: "owner"},
		{name: "empty source", reason: rooms.BlockedProviderQuota, source: ""},
		{name: "source over 256 bytes", reason: rooms.BlockedProviderQuota, source: strings.Repeat("x", 257)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := roomsSnapshotBytes(t, s)
			body, err := json.Marshal(Request{Value: member.SessionID, Reason: tc.reason, Source: tc.source})
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodPost, "/session-block", bytes.NewReader(body))
			request.Header.Set("X-Coord-Owner", s.owner)
			response := httptest.NewRecorder()
			s.Handler(nil).ServeHTTP(response, request)
			var result Response
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatalf("decode response: %v; body=%q", err, response.Body.String())
			}
			if response.Code != http.StatusBadRequest || result.Code != "invalid_input" || result.Error == "" {
				t.Fatalf("status=%d code=%q error=%q, want 400 invalid_input with error", response.Code, result.Code, result.Error)
			}
			after := roomsSnapshotBytes(t, s)
			if !bytes.Equal(before, after) {
				t.Fatal("invalid block request changed the durable rooms snapshot")
			}
		})
	}
}

func TestValidSessionBlockChangesRoomsSnapshot(t *testing.T) {
	s, _, member := malformedHTTPFixture(t)
	before := roomsSnapshotBytes(t, s)
	body, err := json.Marshal(Request{Value: member.SessionID, Reason: rooms.BlockedProviderQuota, Source: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/session-block", bytes.NewReader(body))
	request.Header.Set("X-Coord-Owner", s.owner)
	response := httptest.NewRecorder()
	s.Handler(nil).ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("valid block status=%d, want 200: %s", response.Code, response.Body)
	}
	after := roomsSnapshotBytes(t, s)
	if bytes.Equal(before, after) {
		t.Fatal("valid block request did not change the durable rooms snapshot")
	}
}

func TestWatchInvalidCursorReturnsBeforeOpeningStreamWithoutStateChanges(t *testing.T) {
	s, r, _ := malformedHTTPFixture(t)
	before := roomsSnapshotBytes(t, s)
	request := httptest.NewRequest(http.MethodGet, "/watch?room="+r.ID+"&after=not-a-number", nil)
	request.Header.Set("X-Coord-Owner", s.owner)
	response := httptest.NewRecorder()
	s.Handler(nil).ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400: %s", response.Code, response.Body)
	}
	if response.Flushed || response.Header().Get("Content-Type") == "application/x-ndjson" {
		t.Fatal("invalid cursor opened or flushed the event stream")
	}
	if got := response.Body.String(); got != "invalid cursor\n" {
		t.Fatalf("response body=%q, want invalid cursor rejection", got)
	}
	after := roomsSnapshotBytes(t, s)
	if !bytes.Equal(before, after) {
		t.Fatal("invalid cursor changed the durable rooms snapshot")
	}
}
