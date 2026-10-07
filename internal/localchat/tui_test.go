//go:build unix

package localchat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"coord/internal/rooms"
)

func tuiCall(t *testing.T, s *Service, path, method string, input Request, owner bool) (*httptest.ResponseRecorder, Response) {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	if owner {
		req.Header.Set("X-Coord-Owner", s.owner)
	}
	rec := httptest.NewRecorder()
	s.Handler(nil).ServeHTTP(rec, req)
	var out Response
	if rec.Body.Len() > 0 {
		if err = json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode %s: %v body=%s", path, err, rec.Body)
		}
	}
	return rec, out
}

// Карточка состояния обязана держать поля РАЗДЕЛЬНО: attach ≠ reachability ≠
// readiness ≠ provider; без внешнего сигнала провайдер — unknown (§1.1 ALIGNED_UX).
func TestAvailabilitySeparatesAttachmentFromReadinessAndProvider(t *testing.T) {
	s, _ := fixture(t)
	r, err := s.rooms.Create("task", "title", "")
	if err != nil {
		t.Fatal(err)
	}
	member, err := s.rooms.Assign(r.ID, "auditor", "reviewer", "check", "")
	if err != nil {
		t.Fatal(err)
	}
	rec, resp := tuiCall(t, s, "/availability", http.MethodGet, Request{}, true)
	if rec.Code != http.StatusOK || len(resp.Availability) != 1 {
		t.Fatalf("availability: code=%d body=%s", rec.Code, rec.Body)
	}
	a := resp.Availability[0]
	if a.SessionID != member.SessionID || a.Attachment != "detached" || a.Readiness != "not_ready" {
		t.Fatalf("fields must stay separate and honest: %+v", a)
	}
	if a.Provider != "unknown" {
		t.Fatalf("provider without external signal must be unknown, got %q", a.Provider)
	}
	if !strings.Contains(a.Expectation, "reconnect") {
		t.Fatalf("expectation should name what we wait for: %q", a.Expectation)
	}
	// Блокер квоты меняет provider на blocked и expectation — но не attach/readiness.
	if err = s.rooms.SetBlocker(member.SessionID, rooms.BlockedProviderQuota, "owner", time.Time{}, ""); err != nil {
		t.Fatal(err)
	}
	_, resp = tuiCall(t, s, "/availability", http.MethodGet, Request{}, true)
	a = resp.Availability[0]
	if a.Provider != "blocked" || a.Blocker == nil || a.Blocker.Reason != rooms.BlockedProviderQuota {
		t.Fatalf("blocker must surface: %+v", a)
	}
	if a.Attachment != "detached" || a.Readiness != "not_ready" {
		t.Fatalf("blocker must not rewrite other fields: %+v", a)
	}
	if !strings.Contains(a.Expectation, "заблокировано") {
		t.Fatalf("expectation must name the blocker: %q", a.Expectation)
	}
	// Назначения остановлены, checkpoint не тронут (§1.3).
	if _, err = s.rooms.AssignRegistered(r.ID, member.SessionID, "executor", "again", ""); err == nil {
		t.Fatal("assignment onto blocked session must be refused")
	}
	if !strings.Contains(err.Error(), "заблокирована") {
		t.Fatalf("error must explain blocker + next action: %v", err)
	}
	// Снятие — только явным действием.
	if err = s.rooms.ClearBlocker(member.SessionID, ""); err != nil {
		t.Fatal(err)
	}
	_, resp = tuiCall(t, s, "/availability", http.MethodGet, Request{}, true)
	if resp.Availability[0].Provider != "unknown" {
		t.Fatalf("after explicit unblock provider returns to unknown: %+v", resp.Availability[0])
	}
}

// /session-block и /session-unblock — owner-эндпоинты с машинно-читаемым протоколом.
func TestSessionBlockHTTPEndpoints(t *testing.T) {
	s, _ := fixture(t)
	r, err := s.rooms.Create("task", "title", "")
	if err != nil {
		t.Fatal(err)
	}
	member, err := s.rooms.Assign(r.ID, "longcat", "coordinator", "coordinate", "")
	if err != nil {
		t.Fatal(err)
	}
	retry := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	rec, _ := tuiCall(t, s, "/session-block", http.MethodPost, Request{Value: member.SessionID, Reason: rooms.BlockedProviderQuota, Source: "owner", RetryAt: retry.Format(time.RFC3339)}, false)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("anonymous block %d", rec.Code)
	}
	// Неизвестная причина, пустой источник и retry_at в прошлом — плохой
	// payload, а не конфликт состояния: 400 + машиночитаемый код.
	for _, tc := range []struct {
		name  string
		input Request
		want  string
	}{
		{"unknown reason", Request{Value: member.SessionID, Reason: "made_up_reason", Source: "owner"}, "неизвестная причина"},
		{"empty source", Request{Value: member.SessionID, Reason: rooms.BlockedProviderQuota}, "источник блокера"},
		{"past retry_at", Request{Value: member.SessionID, Reason: rooms.BlockedProviderQuota, Source: "owner", RetryAt: time.Now().Add(-time.Hour).Format(time.RFC3339)}, "будущем"},
	} {
		rec, resp := tuiCall(t, s, "/session-block", http.MethodPost, tc.input, true)
		if rec.Code != http.StatusBadRequest || resp.Code != "invalid_input" {
			t.Fatalf("%s must be 400 invalid_input: %d %s", tc.name, rec.Code, rec.Body)
		}
		if !strings.Contains(strings.ToLower(resp.Error), strings.ToLower(tc.want)) {
			t.Fatalf("%s error must name the rule: %q", tc.name, resp.Error)
		}
	}
	rec, _ = tuiCall(t, s, "/session-block", http.MethodPost, Request{Value: member.SessionID, Reason: rooms.BlockedProviderQuota, Source: "integration", RetryAt: "not-a-time"}, true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad retry_at %d %s", rec.Code, rec.Body)
	}
	rec, _ = tuiCall(t, s, "/session-block", http.MethodPost, Request{Value: member.SessionID, Reason: rooms.BlockedProviderQuota, Source: "integration", RetryAt: retry.Format(time.RFC3339)}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("owner block %d %s", rec.Code, rec.Body)
	}
	_, resp := tuiCall(t, s, "/availability", http.MethodGet, Request{}, true)
	if len(resp.Availability) == 0 || resp.Availability[0].Blocker == nil || !resp.Availability[0].Blocker.RetryAt.Equal(retry) {
		t.Fatalf("retry_at must round-trip: %+v", resp.Availability)
	}
	rec, _ = tuiCall(t, s, "/session-unblock", http.MethodPost, Request{Value: member.SessionID}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("unblock %d %s", rec.Code, rec.Body)
	}
	rec, _ = tuiCall(t, s, "/session-unblock", http.MethodPost, Request{Value: member.SessionID}, true)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "нет блокера") {
		t.Fatalf("double unblock must say so: %d %s", rec.Code, rec.Body)
	}
}

// Кадр human TUI: секции чата и состояний, раздельные поля, честный unknown.
func TestRenderTUIFrameSections(t *testing.T) {
	m := &tuiModel{observedAt: time.Now()}
	m.peers = []Peer{{Address: "codex.auditor", Ref: "r1", Activity: "idle"}}
	m.rooms = []tuiRoomBrief{{id: "room_1", title: "Аудит", closed: false}}
	m.joined = "room_1"
	m.avail = []rooms.Availability{{SessionID: "sess_1", AgentID: "nemotron", Generation: 2, State: "active",
		Attachment: "attached", Reachability: "unknown", Readiness: "ready", Provider: "unknown",
		Processing: "processed 4 of 4", Expectation: "ожиданий нет"}}
	m.history = []string{"[say] owner/owner: привет"}
	m.notice = "подсказка: /models /connect /sessions /rooms /join"
	frame := renderTUI(m)
	for _, want := range []string{"human TUI", "Чат · room_1", "Состояния сессий", "attach=attached reach=unknown ready=ready provider=unknown", "ждём:", "/models", "/connect", "/sessions", "> "} {
		if !strings.Contains(frame, want) {
			t.Fatalf("frame missing %q:\n%s", want, frame)
		}
	}
	if strings.Contains(frame, "provider=available") {
		t.Fatal("never claim availability without an external signal")
	}
}

// slash-команды идут в те же эндпоинты, что и CLI; /block без owner отклоняется.
func TestTUICommandsUseExistingEndpoints(t *testing.T) {
	s, _ := fixture(t)
	r, err := s.rooms.Create("task", "title", "")
	if err != nil {
		t.Fatal(err)
	}
	member, err := s.rooms.Assign(r.ID, "agent", "executor", "x", "")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler(nil))
	defer srv.Close()
	client := &Client{http: srv.Client(), owner: s.owner, base: srv.URL}
	ctx := context.Background()
	m := &tuiModel{}
	if handled, err2 := m.handleCommand(client, ctx, "/join "+r.ID); !handled || err2 != nil {
		t.Fatalf("join: handled=%v err=%v", handled, err2)
	}
	if m.joined != r.ID {
		t.Fatalf("joined=%s", m.joined)
	}
	if handled, err2 := m.handleCommand(client, ctx, "привет из TUI"); handled || err2 != nil {
		t.Fatalf("plain line must fall through to chat send: handled=%v err=%v", handled, err2)
	}
	if err2 := client.sayInRoom(ctx, r.ID, "привет из TUI"); err2 != nil {
		t.Fatalf("sayInRoom: %v", err2)
	}
	_, evResp := tuiCall(t, s, "/events", http.MethodPost, Request{Room: r.ID}, true)
	found := false
	for _, e := range evResp.Events {
		// The journal records owner text under kind "message"; "say" names the command.
		if e.Kind == "message" && strings.Contains(e.Text, "привет из TUI") {
			found = true
		}
	}
	if !found {
		t.Fatalf("owner chat line must land in room journal: %+v", evResp.Events)
	}
	// /block без owner-capability — отказ ещё до запроса.
	m2 := &tuiModel{}
	if _, err2 := m2.handleCommand(client, ctx, "/block "+member.SessionID+" "+rooms.BlockedProviderQuota); err2 == nil || !strings.Contains(err2.Error(), "owner-полномочия") {
		t.Fatalf("block without owner cap must fail early: %v", err2)
	}
	// с owner-capability — проходит через тот же POST /session-block.
	m3 := &tuiModel{ownerCapable: true}
	if _, err2 := m3.handleCommand(client, ctx, "/block "+member.SessionID+" "+rooms.BlockedProviderQuota); err2 != nil {
		t.Fatalf("owner block via TUI: %v", err2)
	}
	_, av := tuiCall(t, s, "/availability", http.MethodGet, Request{}, true)
	if len(av.Availability) == 0 || av.Availability[0].Provider != "blocked" {
		t.Fatalf("TUI block must persist: %+v", av.Availability)
	}
	if _, err2 := m3.handleCommand(client, ctx, "/unblock "+member.SessionID); err2 != nil {
		t.Fatalf("owner unblock via TUI: %v", err2)
	}
	// неизвестная команда не роняет UI и подсказывает /help
	if handled, err2 := m3.handleCommand(client, ctx, "/nope"); !handled || err2 != nil {
		t.Fatalf("unknown command must be handled politely: %v %v", handled, err2)
	}
	if !strings.Contains(strings.Join(m3.history, "\n"), "/help") {
		t.Fatal("unknown command should point to /help")
	}
}

// Метод команды определён один раз для handler и client: рассинхрон списков
// раньше давал 405 на /availability и полностью ломал TUI.
func TestCommandMethodsAgreeBetweenClientAndHandler(t *testing.T) {
	s, _ := fixture(t)
	srv := httptest.NewServer(s.Handler(nil))
	defer srv.Close()
	client := &Client{http: srv.Client(), owner: s.owner, base: srv.URL}
	ctx := context.Background()
	if _, err := client.Call(ctx, "availability", Request{}); err != nil {
		t.Fatalf("availability must be a read-only command: %v", err)
	}
	for _, command := range []string{"health", "peers", "status", "rooms", "sessions", "history-status"} {
		rec, _ := tuiCall(t, s, "/"+command, http.MethodGet, Request{}, true)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /%s = %d, want 200", command, rec.Code)
		}
		if expectedMethod(command) != http.MethodGet {
			t.Fatalf("/%s must stay read-only", command)
		}
	}
	if expectedMethod("say") != http.MethodPost {
		t.Fatal("mutating commands must stay POST")
	}
}

// Плохой вход отличается от конфликта состояния: 400 + машиночитаемый код,
// чтобы клиент не повторял неизменяемый payload.
func TestSessionBlockSeparatesInvalidInputFromConflict(t *testing.T) {
	s, _ := fixture(t)
	r, err := s.rooms.Create("task", "title", "")
	if err != nil {
		t.Fatal(err)
	}
	member, err := s.rooms.Assign(r.ID, "agent", "executor", "work", "")
	if err != nil {
		t.Fatal(err)
	}
	eventsBefore, err := s.rooms.Events(r.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	rec, resp := tuiCall(t, s, "/session-block", http.MethodPost, Request{
		Value: member.SessionID, Reason: rooms.BlockedProviderQuota, Source: "integration", RetryAt: "not-a-time",
	}, true)
	if rec.Code != http.StatusBadRequest || resp.Code != "invalid_input" {
		t.Fatalf("malformed retry_at must be 400 invalid_input: %d %s", rec.Code, rec.Body)
	}
	if !strings.Contains(resp.Error, "RFC3339") {
		t.Fatalf("error must name the expected format: %q", resp.Error)
	}
	eventsAfter, err := s.rooms.Events(r.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(eventsAfter) != len(eventsBefore) {
		t.Fatalf("invalid block request appended an event: before=%d after=%d", len(eventsBefore), len(eventsAfter))
	}
	for _, availability := range s.rooms.Availability() {
		if availability.SessionID == member.SessionID && availability.Blocker != nil {
			t.Fatalf("invalid block request mutated session state: %+v", availability)
		}
	}
	// Настоящий конфликт состояния остаётся 409 без машиночитаемого invalid_input.
	rec, resp = tuiCall(t, s, "/session-block", http.MethodPost, Request{
		Value: member.SessionID, Reason: rooms.BlockedProviderQuota, Source: "integration",
	}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("first block %d %s", rec.Code, rec.Body)
	}
	rec, resp = tuiCall(t, s, "/session-block", http.MethodPost, Request{
		Value: "unknown-session", Reason: rooms.BlockedProviderQuota, Source: "integration",
	}, true)
	if rec.Code != http.StatusConflict || resp.Code == "invalid_input" {
		t.Fatalf("missing session is a state conflict: %d %s", rec.Code, rec.Body)
	}
	rec, _ = tuiCall(t, s, "/session-block", http.MethodPost, Request{
		Value: member.SessionID, Reason: rooms.BlockedProviderQuota, Source: "integration",
	}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid block %d %s", rec.Code, rec.Body)
	}
	// Повтор того же блокера идемпотентен: он не меняет время наблюдения
	// и не создаёт дубликат события.
	eventsBefore, err = s.rooms.Events(r.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	var blockerBefore rooms.Blocker
	for _, availability := range s.rooms.Availability() {
		if availability.SessionID == member.SessionID && availability.Blocker != nil {
			blockerBefore = *availability.Blocker
		}
	}
	if blockerBefore.Reason == "" {
		t.Fatal("first block did not produce an availability blocker")
	}
	rec, resp = tuiCall(t, s, "/session-block", http.MethodPost, Request{
		Value: member.SessionID, Reason: rooms.BlockedProviderQuota, Source: "integration",
	}, true)
	if rec.Code != http.StatusOK || resp.Code == "invalid_input" {
		t.Fatalf("repeated identical block stays accepted: %d %s", rec.Code, rec.Body)
	}
	eventsAfter, err = s.rooms.Events(r.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(eventsAfter) != len(eventsBefore) {
		t.Fatalf("repeated identical block appended a duplicate event: before=%d after=%d", len(eventsBefore), len(eventsAfter))
	}
	for _, availability := range s.rooms.Availability() {
		if availability.SessionID == member.SessionID {
			if availability.Blocker == nil || *availability.Blocker != blockerBefore {
				t.Fatalf("repeated identical block changed blocker observation: before=%+v after=%+v", blockerBefore, availability.Blocker)
			}
		}
	}
}

// /join и /leave переключают комнату, которую параллельно опрашивает watcher:
// под -race это ловится, если хоть одна запись идёт без mu.
func TestJoinLeaveAreRaceFreeWithWatcher(t *testing.T) {
	s, _ := fixture(t)
	r, err := s.rooms.Create("task", "title", "")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler(nil))
	defer srv.Close()
	client := &Client{http: srv.Client(), owner: s.owner, base: srv.URL}
	ctx := context.Background()
	m := &tuiModel{ownerCapable: true}
	// Тот же interleaving, что в RunHumanTUI: цикл команд пишет m.joined,
	// watcher-горутина читает его. Проверка под -race ловит рассинхрон.
	var wg sync.WaitGroup
	start := make(chan struct{})
	commandsDone := make(chan struct{})
	commandErr := make(chan error, 1)
	var commandCount int
	wg.Add(2)
	go func() {
		defer wg.Done()
		defer close(commandsDone)
		<-start
		for i := 0; i < 200; i++ {
			if _, err := m.handleCommand(client, ctx, "/join "+r.ID); err != nil {
				commandErr <- err
				return
			}
			commandCount++
			runtime.Gosched()
			if _, err := m.handleCommand(client, ctx, "/leave"); err != nil {
				commandErr <- err
				return
			}
			commandCount++
			runtime.Gosched()
		}
	}()
	go func() {
		defer wg.Done()
		<-start
		for {
			select {
			case <-commandsDone:
				return
			default:
				_ = m.joinedRoom()
				runtime.Gosched()
			}
		}
	}()
	close(start)
	wg.Wait()
	select {
	case err := <-commandErr:
		t.Fatalf("join/leave command failed: %v", err)
	default:
	}
	if commandCount != 400 {
		t.Fatalf("expected all 400 join/leave commands to run, got %d", commandCount)
	}
	if commandCount == 0 {
		t.Fatal("no successful join/leave command was observed")
	}
	if m.joinedRoom() != "" {
		t.Fatalf("последняя команда должна оставить комнату: %q", m.joinedRoom())
	}
}

// Карточка наблюдаема во времени: внешний supervisor ставит blocker между
// тиками, и кадр обязан показать изменение без ввода команды.
func TestAvailabilityRefreshesExternalBlockerWithoutCommand(t *testing.T) {
	s, _ := fixture(t)
	r, err := s.rooms.Create("task", "title", "")
	if err != nil {
		t.Fatal(err)
	}
	member, err := s.rooms.Assign(r.ID, "watched", "executor", "work", "")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler(nil))
	defer srv.Close()
	client := &Client{http: srv.Client(), owner: s.owner, base: srv.URL}

	// Настоящий цикл TUI: ввод не содержит ни одной команды, поэтому кадр может
	// измениться только через тикер наблюдения.
	reader, writer := io.Pipe()
	frames := &lockedBuffer{}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- client.runHumanTUI(ctx, reader, frames, 20*time.Millisecond) }()
	if _, err := io.WriteString(writer, "/join "+r.ID+"\n"); err != nil {
		t.Fatal(err)
	}
	waitFrame(t, frames, "provider=unknown")

	// Внешний supervisor ставит блокер между тиками — команды owner не было.
	if err = s.rooms.SetBlocker(member.SessionID, rooms.BlockedProviderQuota, "supervisor", time.Time{}, ""); err != nil {
		t.Fatal(err)
	}
	waitFrame(t, frames, "provider=blocked")

	if _, err := io.WriteString(writer, "/quit\n"); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	_ = writer.Close()
}

// frameSource — накопитель вывода кадра: тесты ждут в нём строку.
type frameSource interface {
	String() string
}

func waitFrame(t *testing.T, frames frameSource, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(frames.String(), want) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("кадр так и не показал %q:\n%s", want, frames.String())
}

// lockedBuffer собирает кадры: буфер вывода пишет цикл TUI.
type lockedBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// Reset очищает накопленные кадры перед новым наблюдением.
func (b *lockedBuffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf.Reset()
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// Backpressure не теряет события: очередь кадра меньше пачки событий, но каждое
// событие должно быть показано, и ни одно не показано дважды. Проверяет и то,
// что курсор двигается только после успешной поставки в очередь: иначе обрыв
// потока навсегда пропустил бы запись, которую кадр не показал.
func TestEventsSurviveBackpressureWithoutDuplicates(t *testing.T) {
	s, _ := fixture(t)
	r, err := s.rooms.Create("task", "title", "")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler(nil))
	defer srv.Close()
	client := &Client{http: srv.Client(), owner: s.owner, base: srv.URL}

	// Нумерация с ведущими нулями: «#007» не является подстрокой «#70».
	const total = 200 // очередь кадра — 64
	marker := func(i int) string { return fmt.Sprintf("#%03d", i) }
	for i := 0; i < total; i++ {
		if _, err := s.rooms.Say(r.ID, "реплика "+marker(i), ""); err != nil {
			t.Fatal(err)
		}
	}
	reader, writer := io.Pipe()
	frames := &lockedBuffer{}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- client.runHumanTUI(ctx, reader, frames, 50*time.Millisecond) }()
	if _, err := io.WriteString(writer, "/join "+r.ID+"\n"); err != nil {
		t.Fatal(err)
	}
	// Последняя реплика придёт только после всех предыдущих: с потерями или
	// без порядка ожидание не наступило бы.
	waitFrame(t, frames, "реплика "+marker(total-1))
	if _, err := io.WriteString(writer, "/quit\n"); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	_ = writer.Close()

	out := frames.String()
	seen := map[string]int{}
	for i := 0; i < total; i++ {
		seen[marker(i)] = strings.Count(out, "реплика "+marker(i))
	}
	for i := 0; i < total; i++ {
		if seen[marker(i)] == 0 {
			t.Fatalf("реплика %s потеряна при backpressure", marker(i))
		}
	}
	// Внутри одного кадра строка не может повторяться: повтор означал бы, что
	// событие показано дважды (например, после обрыва потока).
	for _, frame := range strings.Split(out, "┌─ Coord") {
		counts := map[string]int{}
		for _, line := range strings.Split(frame, "\n") {
			for i := 0; i < total; i++ {
				if strings.Contains(line, "реплика "+marker(i)) {
					counts[marker(i)]++
				}
			}
		}
		for id, n := range counts {
			if n > 1 {
				t.Fatalf("в одном кадре %s встречается %d раз", id, n)
			}
		}
	}
}

// Выход по /quit освобождает горутину чтения ввода: за reader остаются строки
// после /quit, и TUI всё равно завершается без зависшей горутины.
func TestQuitWithTrailingInputLeavesNoGoroutine(t *testing.T) {
	s, _ := fixture(t)
	srv := httptest.NewServer(s.Handler(nil))
	defer srv.Close()
	client := &Client{http: srv.Client(), owner: s.owner, base: srv.URL}
	lines := strings.Repeat("/help\n", 50) + "/quit\n" + strings.Repeat("/help\n", 50) + "/quit\n"
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- client.runHumanTUI(ctx, strings.NewReader(lines), io.Discard, 50*time.Millisecond) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("TUI не завершился: горутина чтения ввода осталась заблокированной")
	}
	// Читатель успевает выйти; ждём settle и сравниваем число горутин.
	// Считаем именно горутину чтения ввода: сравнение общего числа горутин
	// ненадёжно, потому что одновременно завершаются watcher и keep-alive
	// соединения. Утечка — это читатель, оставшийся на отправке в lineCh.
	for i := 0; i < 60; i++ {
		if countInputReaders() == 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("горитина чтения ввода не завершилась (%d):\n%s", countInputReaders(), goroutineDump())
}

// countInputReaders считает живые горутины pumpInput.
func countInputReaders() int {
	found := 0
	for _, stack := range allStacks() {
		if strings.Contains(stack, "localchat.pumpInput") {
			found++
		}
	}
	return found
}

func goroutineDump() string {
	buf := make([]byte, 1<<16)
	n := runtime.Stack(buf, true)
	return string(buf[:n])
}

func allStacks() []string {
	dump := goroutineDump()
	parts := strings.Split(dump, "\n\n")
	stacks := make([]string, 0, len(parts))
	for _, p := range parts {
		if strings.Contains(p, "goroutine ") {
			stacks = append(stacks, p)
		}
	}
	return stacks
}

// Смена комнаты обязана рвать подписку /watch: /join B не может оставить
// Watch(A) открытым, иначе события B не приходят никогда, а /leave не
// прекращает активный запрос вообще.
func TestRoomSwitchCancelsWatchSubscription(t *testing.T) {
	s, _ := fixture(t)
	first, err := s.rooms.Create("room-a", "first", "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.rooms.Create("room-b", "second", "")
	if err != nil {
		t.Fatal(err)
	}
	watcher := &watchCounter{rooms: map[string]int{}, handler: s.Handler(nil)}
	srv := httptest.NewServer(watcher)
	defer srv.Close()
	client := &Client{http: srv.Client(), owner: s.owner, base: srv.URL}

	reader, writer := io.Pipe()
	frames := &lockedBuffer{}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- client.runHumanTUI(ctx, reader, frames, 50*time.Millisecond) }()

	if _, err := io.WriteString(writer, "/join "+first.ID+"\n"); err != nil {
		t.Fatal(err)
	}
	// Событие первой комнаты доходит до кадра.
	if _, err := s.rooms.Say(first.ID, "событие A-1", ""); err != nil {
		t.Fatal(err)
	}
	waitFrame(t, frames, "событие A-1")
	waitRoomSubscriptions(t, watcher, first.ID, 1)
	if n := watcher.active(); n != 1 {
		t.Fatalf("ожидалась одна активная подписка, получено %d", n)
	}

	// Переключение на вторую комнату: события B приходят, новые A — нет.
	if _, err := io.WriteString(writer, "/join "+second.ID+"\n"); err != nil {
		t.Fatal(err)
	}
	waitRoomSubscriptions(t, watcher, second.ID, 1)
	if watcher.activeRoom(first.ID) != 0 {
		t.Fatalf("после переключения осталась подписка прежней комнаты: %d", watcher.activeRoom(first.ID))
	}
	if _, err := s.rooms.Say(second.ID, "событие B-1", ""); err != nil {
		t.Fatal(err)
	}
	waitFrame(t, frames, "событие B-1")
	waitNoSubscriptionsExcept(t, watcher, 1)
	if n := watcher.active(); n != 1 {
		t.Fatalf("после /join второй комнаты ожидалась одна активная подписка, получено %d", n)
	}
	if n := watcher.activeRoom(second.ID); n != 1 {
		t.Fatalf("после события B-1 активных подписок комнаты B = %d, want 1", n)
	}
	if _, err := s.rooms.Say(first.ID, "событие A-2", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.rooms.Say(second.ID, "событие B-2", ""); err != nil {
		t.Fatal(err)
	}
	// B-2 приходит после A-2 по времени, поэтому его присутствие доказывает,
	// что очередь пережила попытку доставки события покинутой комнаты.
	waitFrame(t, frames, "событие B-2")
	time.Sleep(300 * time.Millisecond)
	if strings.Contains(frames.String(), "событие A-2") {
		t.Fatal("после /join другой комнаты события покинутой комнаты не должны показываться")
	}

	// /leave прекращает активный запрос.
	if _, err := io.WriteString(writer, "/leave\n"); err != nil {
		t.Fatal(err)
	}
	waitNoSubscriptions(t, watcher)
	if _, err := io.WriteString(writer, "/quit\n"); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	_ = writer.Close()
}

// watchCounter считает активные /watch запросы, чтобы доказать, что смена
// комнаты рвёт подписку, а не просто переключает фильтр показа.
type watchCounter struct {
	mu      sync.Mutex
	live    int
	rooms   map[string]int
	handler http.Handler
}

func (c *watchCounter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/watch" {
		room := r.URL.Query().Get("room")
		c.mu.Lock()
		c.live++
		c.rooms[room]++
		c.mu.Unlock()
		defer func() {
			c.mu.Lock()
			c.live--
			c.rooms[room]--
			c.mu.Unlock()
		}()
	}
	c.handler.ServeHTTP(w, r)
}

func (c *watchCounter) activeRoom(room string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.rooms[room]
}

func (c *watchCounter) active() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.live
}

func waitNoSubscriptions(t *testing.T, watcher *watchCounter) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if watcher.active() == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("после /leave активных подписок осталось %d", watcher.active())
}

func waitNoSubscriptionsExcept(t *testing.T, watcher *watchCounter, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got := watcher.active(); got == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("active /watch subscriptions = %d, want %d", watcher.active(), want)
}

func waitRoomSubscriptions(t *testing.T, watcher *watchCounter, room string, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got := watcher.activeRoom(room); got == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("active /watch subscriptions for %s = %d, want %d", room, watcher.activeRoom(room), want)
}

// Смена комнаты при backlog: подтверждённый cursor не должен терять события
// покинутой комнаты и не должен показывать их повторно. Очередь кадра уменьшена
// до 1, поэтому backpressure почти наверняка возникает, но сам факт блокировки
// producer-а здесь НЕ проверяется — он доказан отдельно в
// TestDeliverEventWakesOnRoomCancelWhileQueueIsFull, где очередь занята и
// никто её не читает. Этот тест отвечает за replay и отсутствие дублей.
func TestRoomSwitchReplaysBacklogWithoutDuplicates(t *testing.T) {
	s, _ := fixture(t)
	first, err := s.rooms.Create("room-a", "first", "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.rooms.Create("room-b", "second", "")
	if err != nil {
		t.Fatal(err)
	}
	// Очередь кадра уменьшена до 1: 300 событий при ёмкости 1 делают
	// backpressure почти наверняка, но без утверждения о нём — доказательство
	// блокировки лежит в отдельном unit test.
	previous := frameQueue
	frameQueue = 1
	t.Cleanup(func() { frameQueue = previous })
	const total = 300
	for i := 0; i < total; i++ {
		if _, err := s.rooms.Say(first.ID, "A-"+fmt.Sprintf("%03d", i), ""); err != nil {
			t.Fatal(err)
		}
	}
	srv := httptest.NewServer(s.Handler(nil))
	defer srv.Close()
	client := &Client{http: srv.Client(), owner: s.owner, base: srv.URL}

	reader, writer := io.Pipe()
	frames := &lockedBuffer{}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- client.runHumanTUI(ctx, reader, frames, 20*time.Millisecond) }()

	if _, err := io.WriteString(writer, "/join "+first.ID+"\n"); err != nil {
		t.Fatal(err)
	}
	// Даём циклу показать часть backlog до переключения комнаты: именно эти
	// события станут подтверждённым cursor, и проверка ниже отделит их от
	// неподтверждённого хвоста, который обязан вернуться после возврата.
	time.Sleep(1000 * time.Millisecond)
	pending := countUniqueA(frames.String())
	if pending == 0 {
		t.Fatal("до переключения не показано ни одного события A: сценарий не воспроизводится")
	}
	t.Logf("до переключения показано %d из %d событий A", pending, total)
	// Накопленные кадры до переключения нужны для проверки покрытия backlog:
	// окно чата прокручивается, поэтому ранние события видны только в них.
	preSwitch := frames.String()

	if _, err := io.WriteString(writer, "/join "+second.ID+"\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.rooms.Say(second.ID, "B-событие", ""); err != nil {
		t.Fatal(err)
	}
	waitFrame(t, frames, "B-событие")
	// Пока выбрана B, новые события A не дописываются в историю и не двигают
	// её курсор: хвост будет перечитан при возврате.
	if _, err := s.rooms.Say(first.ID, "A-позднее", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.rooms.Say(second.ID, "B-позднее", ""); err != nil {
		t.Fatal(err)
	}
	waitFrame(t, frames, "B-позднее")
	if strings.Contains(frames.String(), "A-позднее") {
		t.Fatal("в кадре комнаты B не должно быть новых событий комнаты A")
	}

	// Возврат в A обязан перечитать хвост заново.
	frames.Reset()
	if _, err := io.WriteString(writer, "/join "+first.ID+"\n"); err != nil {
		t.Fatal(err)
	}
	waitFrame(t, frames, fmt.Sprintf("A-%03d", total-1))
	// Событие, созданное пока выбрана B, обязано появиться после возврата.
	waitFrame(t, frames, "A-позднее")
	if _, err := io.WriteString(writer, "/quit\n"); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	_ = writer.Close()

	// Хвост backlog показан непрерывно и строго по порядку: пропуск означал бы
	// продвинутый курсор при не показанном событии, повтор — двойную доставку.
	// Проверяется последний кадр: накопленные кадры содержат одну и ту же
	// строку многократно, что не является повторной доставкой события.
	last := frames.String()
	if at := strings.LastIndex(last, "┌─ Coord"); at >= 0 {
		last = last[at:]
	}
	var numbered []string
	for _, line := range chatLines(last) {
		at := strings.LastIndex(line, "A-")
		// Нумерованные события — ровно три цифры после дефиса; «A-позднее» нет.
		if at < 0 || len(line)-at != 5 {
			continue
		}
		numbered = append(numbered, line[at:])
	}
	seen := 0
	for j := len(numbered) - 1; j >= 0; j-- {
		if numbered[j] != "A-"+fmt.Sprintf("%03d", total-1-seen) {
			break
		}
		seen++
	}
	// Окно чата показывает последние строки, поэтому проверяется непрерывность
	// видимого хвоста, а не весь журнал.
	if seen < 5 {
		sample := numbered
		if len(sample) > 5 {
			sample = sample[len(sample)-5:]
		}
		t.Fatalf("после возврата в комнату хвост backlog показан только %d событий подряд; послед=%v (показано %d из %d)", seen, sample, pending, total)
	}
	// Ни одно событие не показано дважды в последнем кадре. Вхождения считаются
	// по строкам чата, а не через strings.Contains: Contains для одного кадра
	// даёт 0 или 1, поэтому старая проверка не могла упасть никогда.
	// Мутация: курсор не подтверждается после показа → строка дублируется.
	perFrame := map[string]int{}
	for _, line := range numbered {
		perFrame[line]++
	}
	for _, line := range numbered {
		if perFrame[line] > 1 {
			t.Fatalf("%s показан %d раз в последнем кадре: %v", line, perFrame[line], numbered)
		}
	}
	// Событие, созданное во время просмотра другой комнаты, идёт после хвоста
	// backlog, а не вместо него. Мутация: хвост replay не доходит до конца или
	// позднее событие вставляется в середину.
	lastChat := chatLines(last)
	late := -1
	for j, line := range lastChat {
		if strings.Contains(line, "A-позднее") {
			late = j
			break
		}
	}
	if late < 0 {
		t.Fatalf("A-позднее отсутствует в последнем кадре: %v", lastChat)
	}
	if late < len(lastChat)-1 {
		t.Fatalf("A-позднее не последняя строка чата: %v", lastChat)
	}
	if lastNumbered := len(lastChat) - 1 - late; lastNumbered != 0 {
		t.Fatalf("после A-позднее ожидался конец чата, строк после него=%d: %v", lastNumbered, lastChat)
	}
	// A-позднее показан позже последнего события backlog, то есть хвост replay
	// не потерян и не подменён. Список чата общий для комнат, поэтому между
	// хвостом и A-позднее допустимы события покинутой комнаты.
	newest := -1
	for j := range lastChat {
		at := strings.LastIndex(lastChat[j], "A-")
		if at >= 0 && len(lastChat[j])-at == 5 {
			newest = j
		}
	}
	if newest >= late {
		t.Fatalf("A-позднее (позиция %d) должен идти после последнего события backlog (позиция %d): %v", late, newest, lastChat)
	}
	// Каждое пронумерованное событие backlog показано хотя бы раз за сессию:
	// окно чата ограничено, поэтому в последнем кадре видно только хвост.
	delivered := map[string]bool{}
	for _, out := range []string{preSwitch, frames.String()} {
		for _, line := range chatLines(out) {
			at := strings.LastIndex(line, "A-")
			if at < 0 || len(line)-at != 5 {
				continue
			}
			delivered[line[at:]] = true
		}
	}
	missing := 0
	for i := 0; i < total; i++ {
		if !delivered["A-"+fmt.Sprintf("%03d", i)] {
			missing++
		}
	}
	if missing > 0 {
		t.Fatalf("%d из %d событий backlog не показаны ни разу", missing, total)
	}
}

// A room cursor advances only after display. Returning to a room must replay
// events created while away without replaying events already shown.
func TestShownEventsAreNotRedisplayedAfterRoomSwitch(t *testing.T) {
	s, _ := fixture(t)
	first, err := s.rooms.Create("room-a", "first", "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.rooms.Create("room-b", "second", "")
	if err != nil {
		t.Fatal(err)
	}
	previous := frameQueue
	frameQueue = 1
	t.Cleanup(func() { frameQueue = previous })
	const shown = 4
	for i := 0; i < shown; i++ {
		if _, err := s.rooms.Say(first.ID, fmt.Sprintf("A-%03d", i), ""); err != nil {
			t.Fatal(err)
		}
	}
	srv := httptest.NewServer(s.Handler(nil))
	defer srv.Close()
	client := &Client{http: srv.Client(), owner: s.owner, base: srv.URL}
	reader, writer := io.Pipe()
	frames := &lockedBuffer{}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- client.runHumanTUI(ctx, reader, frames, 20*time.Millisecond) }()
	quit := func() {
		t.Helper()
		_, _ = io.WriteString(writer, "/quit\n")
		if err := <-done; err != nil {
			t.Errorf("TUI shutdown: %v", err)
		}
		_ = writer.Close()
	}
	if _, err := io.WriteString(writer, "/join "+first.ID+"\n"); err != nil {
		t.Fatal(err)
	}
	waitFrame(t, frames, "A-003") // ordered stream: A-000..A-003 are now displayed
	if _, err := io.WriteString(writer, "/join "+second.ID+"\n"); err != nil {
		quit()
		t.Fatal(err)
	}
	if _, err := s.rooms.Say(second.ID, "B-between", ""); err != nil {
		quit()
		t.Fatal(err)
	}
	waitFrame(t, frames, "B-between")
	if _, err := s.rooms.Say(first.ID, "A-late", ""); err != nil {
		quit()
		t.Fatal(err)
	}
	frames.Reset()
	if _, err := io.WriteString(writer, "/join "+first.ID+"\n"); err != nil {
		quit()
		t.Fatal(err)
	}
	waitFrame(t, frames, "A-late")
	quit()

	last := frames.String()
	if at := strings.LastIndex(last, "┌─ Coord"); at >= 0 {
		last = last[at:]
	}
	counts := map[string]int{}
	for _, line := range chatLines(last) {
		at := strings.LastIndex(line, "A-")
		if at >= 0 {
			counts[line[at:]]++
		}
	}
	for i := 0; i < shown; i++ {
		id := fmt.Sprintf("A-%03d", i)
		if counts[id] != 1 {
			t.Fatalf("already shown event %s appeared %d times after returning: %v", id, counts[id], chatLines(last))
		}
	}
	if counts["A-late"] != 1 {
		t.Fatalf("event created while away should appear once: %v", chatLines(last))
	}
}

// countUniqueA считает уникальные пронумерованные A-события в выводе.
func countUniqueA(out string) int {
	uniq := map[string]bool{}
	for _, line := range chatLines(out) {
		at := strings.LastIndex(line, "A-")
		if at >= 0 && len(line)-at == 5 {
			uniq[line[at:]] = true
		}
	}
	return len(uniq)
}

// chatLines достаёт строки секции «Чат» из накопленных кадров.
func chatLines(out string) []string {
	var chat []string
	inChat := false
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "├─ Чат") {
			inChat = true
			continue
		}
		if inChat && strings.Contains(line, "├─ Состояния сессий") {
			inChat = false
		}
		if inChat && strings.HasPrefix(line, "│") {
			text := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "│"))
			if text != "" && !strings.Contains(text, "(пусто") && !strings.HasPrefix(text, "— вы в комнате") {
				chat = append(chat, text)
			}
		}
	}
	return chat
}

// deliverEvent при заполненной очереди обязан проснуться на отмене КОМНАТИ.
// Это ровно тот инвариант, который сценарий end-to-end не может надёжно
// проверить: очередь кадра полна, но слот освобождается раньше, чем успевает
// сработать /join. Здесь очередь полна и НЕ освобождается — единственный выход
// это отмена комнаты.
func TestDeliverEventWakesOnRoomCancelWhileQueueIsFull(t *testing.T) {
	events := make(chan rooms.Event, 1)
	events <- rooms.Event{ID: "busy", Room: "room-a", Seq: 1}

	streamCtx, cancelStream := context.WithCancel(context.Background())
	defer cancelStream()
	roomCtx, cancelRoom := context.WithCancel(streamCtx)

	blocked := make(chan error, 1)
	go func() {
		blocked <- deliverEvent(roomCtx, streamCtx, events, rooms.Event{ID: "next", Room: "room-a", Seq: 2})
	}()

	// Событие не может уйти: очередь занята и никто её не читает.
	select {
	case err := <-blocked:
		t.Fatalf("поставка не должна завершиться при полной очереди: %v", err)
	case <-time.After(200 * time.Millisecond):
	}

	// Смена комнаты освобождает поставку.
	cancelRoom()
	select {
	case err := <-blocked:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ожидалась отмена комнаты, получено %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("отмена комнаты не разблокировала поставку в заполненную очередь")
	}
	if len(events) != 1 {
		t.Fatalf("в очередь не должно попасть событие покинутой комнаты: %d", len(events))
	}

	// Второй случай: комната жива, отменяется весь поток. Поставка обязана
	// завершиться отменой streamCtx, не дожидаясь свободного слота.
	full := make(chan rooms.Event, 1)
	full <- rooms.Event{ID: "busy", Room: "room-a", Seq: 1}
	liveRoomCtx, liveCancelRoom := context.WithCancel(context.Background())
	defer liveCancelRoom()
	loseStreamCtx, loseStream := context.WithCancel(context.Background())
	loseStreamErr := make(chan error, 1)
	go func() {
		loseStreamErr <- deliverEvent(liveRoomCtx, loseStreamCtx, full, rooms.Event{ID: "last", Room: "room-a", Seq: 2})
	}()
	select {
	case err := <-loseStreamErr:
		t.Fatalf("поставка не должна завершиться при полной очереди: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	loseStream()
	select {
	case err := <-loseStreamErr:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ожидалась отмена потока, получено %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("отмена потока не разблокировала поставку в заполненную очередь")
	}
	if len(full) != 1 {
		t.Fatalf("событие не должно попасть в очередь после отмены потока: %d", len(full))
	}
}
