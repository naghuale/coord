//go:build unix

package localchat

import (
	"bytes"
	"context"
	"coord/internal/rooms"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAPIAgentCannotReadOwnerJournal(t *testing.T) {
	s, _ := fixture(t)
	r, err := s.rooms.Create("private-task", "title", "")
	if err != nil {
		t.Fatal(err)
	}
	own, err := s.rooms.Assign(r.ID, "agent", "executor", "private instructions", "")
	if err != nil {
		t.Fatal(err)
	}
	attached, err := s.rooms.Resume(rooms.ResumeRequest{Session: own.SessionID, Connection: "context", Operation: "attach"})
	if err != nil {
		t.Fatal(err)
	}
	call := func(path, method string, input Request, owner bool) *httptest.ResponseRecorder {
		raw, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(method, path, bytes.NewReader(raw))
		if owner {
			req.Header.Set("X-Coord-Owner", s.owner)
		}
		response := httptest.NewRecorder()
		s.Handler(nil).ServeHTTP(response, req)
		return response
	}
	if got := call("/rooms", http.MethodGet, Request{}, false); got.Code != http.StatusForbidden {
		t.Fatalf("anonymous rooms %d", got.Code)
	}
	if got := call("/events", http.MethodPost, Request{Room: r.ID}, false); got.Code != http.StatusForbidden {
		t.Fatalf("anonymous journal %d", got.Code)
	}
	if got := call("/watch?room="+r.ID, http.MethodGet, Request{}, false); got.Code != http.StatusForbidden {
		t.Fatalf("anonymous watch %d", got.Code)
	}
	if got := call("/session-context", http.MethodPost, Request{Auth: attached.Auth}, false); got.Code != http.StatusOK {
		t.Fatalf("scoped context %d %s", got.Code, got.Body)
	}
	if got := call("/rooms", http.MethodGet, Request{}, true); got.Code != http.StatusOK {
		t.Fatal(got.Code)
	}
	if got := call("/session-resume", http.MethodPost, Request{Resume: rooms.ResumeRequest{Session: own.SessionID, Connection: "imposter", Expected: 1, Operation: "unauthorized"}}, false); got.Code != http.StatusForbidden {
		t.Fatalf("anonymous owner resume %d", got.Code)
	}
}

func TestSessionWaitWakesOnOwnerEventWithoutTransport(t *testing.T) {
	s, _ := fixture(t)
	r, err := s.rooms.Create("wait-task", "title", "")
	if err != nil {
		t.Fatal(err)
	}
	member, err := s.rooms.Assign(r.ID, "agent", "executor", "Wait for owner", "")
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.rooms.Resume(rooms.ResumeRequest{Session: member.SessionID, Connection: "wait-context", Operation: "attach"})
	if err != nil {
		t.Fatal(err)
	}
	events, err := s.rooms.Events(r.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	after := events[len(events)-1].Seq
	done := make(chan rooms.SessionContext, 1)
	fail := make(chan error, 1)
	go func() {
		c, e := s.waitContext(context.Background(), Request{Auth: a.Auth, After: after, WaitSeconds: 1})
		if e != nil {
			fail <- e
			return
		}
		done <- c
	}()
	owner, err := s.rooms.Say(r.ID, "Owner task", "")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-fail:
		t.Fatal(e)
	case c := <-done:
		if len(c.Events) != 1 || c.Events[0].ID != owner.ID {
			t.Fatal("owner task not delivered")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("wait did not wake")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = s.waitContext(canceled, Request{Auth: a.Auth, After: owner.Seq, WaitSeconds: 1}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err = s.waitContext(context.Background(), Request{Auth: a.Auth, WaitSeconds: 61}); err == nil {
		t.Fatal("unbounded wait accepted")
	}
}
