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

// Агент с действующим Auth не является owner: он читает только свой scope и
// не должен видеть журнал комнаты, список сессий, availability или подписку.
// Мутация: ownerRequired возвращает false для одного из этих путей, тест падает.
func TestAgentAuthCannotReadOwnerEndpoints(t *testing.T) {
	s, _ := fixture(t)
	r, err := s.rooms.Create("private-task", "title", "")
	if err != nil {
		t.Fatal(err)
	}
	own, err := s.rooms.Assign(r.ID, "agent", "executor", "private instructions", "")
	if err != nil {
		t.Fatal(err)
	}
	attached, err := s.rooms.Resume(rooms.ResumeRequest{Session: own.SessionID, Connection: "agent-auth", Operation: "attach"})
	if err != nil {
		t.Fatal(err)
	}
	call := func(path, method string, input Request, ctx ...context.Context) *httptest.ResponseRecorder {
		raw, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		var req *http.Request
		if len(ctx) > 0 {
			req = httptest.NewRequest(method, path, bytes.NewReader(raw)).WithContext(ctx[0])
		} else {
			req = httptest.NewRequest(method, path, bytes.NewReader(raw))
		}
		response := httptest.NewRecorder()
		s.Handler(nil).ServeHTTP(response, req)
		return response
	}
	// До: Auth действителен — агент читает свой собственный scope.
	if got := call("/session-context", http.MethodPost, Request{Auth: attached.Auth}); got.Code != http.StatusOK {
		t.Fatalf("валидный Auth не дал свой scope: %d %s", got.Code, got.Body)
	}
	for _, tc := range []struct {
		path   string
		method string
		input  Request
	}{
		{"/rooms", http.MethodGet, Request{}},
		{"/sessions", http.MethodGet, Request{}},
		{"/availability", http.MethodGet, Request{}},
		{"/events", http.MethodPost, Request{Room: r.ID, Auth: attached.Auth}},
		{"/session-block", http.MethodPost, Request{Value: own.SessionID, Reason: rooms.BlockedProviderQuota, Source: "agent", Auth: attached.Auth}},
	} {
		if got := call(tc.path, tc.method, tc.input); got.Code != http.StatusForbidden {
			t.Fatalf("агент с валидным Auth получил %d на %s, ожидался 403: %s", got.Code, tc.path, got.Body)
		}
	}
	// Блокер не мог быть установлен запрещённым вызовом.
	for _, availability := range s.rooms.Availability() {
		if availability.SessionID == own.SessionID && availability.Blocker != nil {
			t.Fatalf("агент установил блокер через owner-эндпоинт: %+v", availability)
		}
	}
	// /watch — долгоживущий поток, поэтому проверяется отказ за ограниченное
	// время, а не «ответ когда-нибудь»: без owner-проверки handler зависнет
	// на открытом потоке и тест обязан упасть быстро, а не висеть до таймаута.
	streamCtx, cancelStream := context.WithCancel(context.Background())
	streamDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		streamDone <- call("/watch?room="+r.ID, http.MethodGet, Request{}, streamCtx)
	}()
	select {
	case got := <-streamDone:
		if got.Code != http.StatusForbidden {
			t.Fatalf("агент с валидным Auth получил %d на /watch, ожидался 403", got.Code)
		}
	case <-time.After(2 * time.Second):
		cancelStream()
		select {
		case <-streamDone:
		case <-time.After(time.Second):
			t.Error("watch handler did not stop after request cancellation")
		}
		t.Fatal("watch без owner-проверки не отказал и открыл поток агенту")
	}
	cancelStream()
}

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
	waiting := make(chan struct{}, 1)
	go func() {
		c, e := s.waitContextObserved(context.Background(), Request{Auth: a.Auth, After: after, WaitSeconds: 1}, func() {
			select {
			case waiting <- struct{}{}:
			default:
			}
		})
		if e != nil {
			fail <- e
			return
		}
		done <- c
	}()
	// The signal follows an empty Context read and precedes the select. This
	// loop polls the journal every 250ms; the test checks the polling wakeup
	// deadline after Say, not a push notification or transport listener.
	select {
	case <-waiting:
	case <-time.After(time.Second):
		t.Fatal("wait did not enter its empty-state polling loop")
	}
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
