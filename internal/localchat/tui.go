//go:build unix

package localchat

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"coord/internal/rooms"
)

// ТИХИЙ HUMAN TUI: чат-область + slash-команды, без внешних зависимостей.
// Кадр собирается из данных, которые уже есть в daemon (peers/rooms/availability/
// events) — статусы выводятся раздельно и никогда не выводятся друг из друга
// (ALIGNED_UX.md §1): ATTACHED ≠ готовность, пустой ответ провайдера = unknown.
// Команды соответствуют согласованному контракту (/models /connect /sessions
// /rooms /join /status /block /unblock /help); текстовые реплики пишутся в
// комнату как owner через существующий POST /say. Никакого нового протокола
// здесь нет — только отображение и вызовы уже согласованных эндпоинтов.
//
// Ограничение кадра: события доставляются потоком /watch, который отдаёт
// хвост журнала после курсора; курсор переносится в переподключение, поэтому
// обрыв потока не возвращает уже показанные строки. Историческая загрузка
// до момента входа в комнату остаётся отдельной задачей (UX_TASK.md срез C).

const tuiHelpText = `Команды human TUI:
  /help                 эта справка
  /models               доступные агенты (обновляет кэш peers)
  /connect <агент>      пригласить агента и дождаться подтверждения
  /sessions             карточки состояний (подключение/готовность/ожидание/блокер)
  /rooms                список проектов-комнат
  /join <room|N>        переключиться в комнату; реплики пишутся туда
  /leave                выйти из комнаты (реплики игнорируются с подсказкой)
  /status               подключение последнего агента и последний результат
  /block <session> <reason> [retry_at RFC3339]  пометить блокер (только owner)
  /unblock <session>    снять блокер (только owner; сначала проверьте checkpoint)
  Ctrl+C или /quit      выход`

type tuiRoomBrief struct {
	id     string
	task   string
	title  string
	closed bool
}

type tuiModel struct {
	mu           sync.Mutex
	peers        []Peer
	peersAt      time.Time
	peersErr     string
	rooms        []tuiRoomBrief
	avail        []rooms.Availability
	joined       string // id комнаты, в которую пишем реплики
	history      []string
	lastStatus   string
	notice       string
	observedAt   time.Time
	ownerCapable bool
	// stopWatch отменяет текущую подписку /watch. Смена комнаты обязана рвать
	// старый запрос: иначе Watch(A) остаётся открытым и события B не приходят.
	stopWatch func()
}

func (m *tuiModel) joinedRoom() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.joined
}

// roomCursors хранит последний показанный Seq по комнатам. Курсор двигает
// только цикл отрисовки — после того как событие действительно попало в
// историю. Продвижение на этапе доставки потеряло бы события: событие,
// поставленное в очередь, но отброшенное фильтром комнаты, уже не переотдастся.
type roomCursors struct {
	mu  sync.Mutex
	seq map[string]uint64
}

func newRoomCursors() *roomCursors { return &roomCursors{seq: map[string]uint64{}} }

func (c *roomCursors) get(room string) uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.seq[room]
}

// ack продвигает курсор только вперёд: события могут приходить не по порядку.
func (c *roomCursors) ack(room string, seq uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if seq > c.seq[room] {
		c.seq[room] = seq
	}
}

// beginWatch атомарно читает выбранную комнату и регистрирует отмену её
// подписки. Чтение и регистрация под одним mu: иначе смена комнаты между ними
// потерялась бы и Watch запустился бы уже не для той комнаты.
func (m *tuiModel) beginWatch(cancel func()) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopWatch = cancel
	return m.joined
}

func (m *tuiModel) endWatch() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopWatch = nil
}

// setJoined переключает комнату и рвёт подписку прежней. Запись и вызов отмены
// идут под mu, поэтому watcher не может успеть начать Watch для старой комнаты.
func (m *tuiModel) setJoined(room string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.joined = room
	if m.stopWatch != nil {
		m.stopWatch()
	}
}

func (m *tuiModel) note(line string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.history = appendBounded(m.history, line, 200)
}

func (m *tuiModel) setNotice(text string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.notice = text
}

func (m *tuiModel) setStatus(text string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastStatus = text
}

// pumpInput forwards scanned lines until the input ends or ctx is cancelled.
// Cancellation is required on every send: RunHumanTUI can return on /quit while
// the reader still holds lines, and an unconditional send would strand the
// goroutine on an unbuffered channel with nobody left to receive.
func pumpInput(ctx context.Context, scanner *bufio.Scanner, lineCh chan<- string, errCh chan<- error) {
	for scanner.Scan() {
		select {
		case lineCh <- scanner.Text():
		case <-ctx.Done():
			return
		}
	}
	if err := scanner.Err(); err != nil {
		select {
		case errCh <- err:
		case <-ctx.Done():
		}
	}
}

func (m *tuiModel) snapshotState() (joined, notice, status string, lines []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.joined, m.notice, m.lastStatus, m.history
}

// deliverEvent hands one event to the redraw loop without ever dropping it and
// without outliving its room. The send blocks while the queue is full, so the
// pause happens on the producer side; roomCtx is the deadline that matters,
// because /join and /leave cancel the room while the whole stream keeps running.
// A select that listened only to the stream context would resume the send as
// soon as a slot appeared and deliver an event of a room the owner has left.
func deliverEvent(roomCtx, streamCtx context.Context, events chan<- rooms.Event, e rooms.Event) error {
	select {
	case events <- e:
		return nil
	case <-roomCtx.Done():
		return roomCtx.Err()
	case <-streamCtx.Done():
		return streamCtx.Err()
	}
}

// frameQueue is how many room events may wait for the redraw loop. Delivery is
// blocking, so a full queue pauses the stream instead of dropping events; the
// value is a var only so a test can force the backpressure path deterministically.
var frameQueue = 64

// defaultObserveInterval is how often the frame re-reads observed state. The
// card must show an external blocker change, so observation cannot wait for a
// command; a test drives the same loop with a shorter interval.
const defaultObserveInterval = time.Second

// RunHumanTUI serves the line-based owner UI over the private Unix socket.
// It never opens a network listener and requires no third-party libraries.
func (c *Client) RunHumanTUI(ctx context.Context, in io.Reader, out io.Writer) error {
	return c.runHumanTUI(ctx, in, out, defaultObserveInterval)
}

func (c *Client) runHumanTUI(ctx context.Context, in io.Reader, out io.Writer, observe time.Duration) error {
	m := &tuiModel{ownerCapable: c.owner != ""}
	w := newBufioWriter(out)
	defer w.Flush()
	if err := m.refresh(c, ctx, true); err != nil {
		return err
	}
	if _, err := fmt.Fprint(w, renderTUI(m)+"\n"); err != nil {
		return err
	}
	w.Flush()
	events := make(chan rooms.Event, frameQueue)
	streamCtx, cancelStream := context.WithCancel(ctx)
	defer cancelStream()
	// Курсор общий для watcher-горутины и цикла отрисовки: отправка читает
	// его при (пере)подписке, отрисовка подтверждает показанное.
	cursors := newRoomCursors()
	// Watch держится на одной комнате до обрыва, поэтому переподключение
	// происходит сразу, а не по таймеру и не дублирует активный поток.
	// Курсор хранится по комнатам: переподключение продолжает с последнего
	// показанного Seq, иначе весь журнал вернулся бы в историю повторно.
	go func() {
		for {
			if m.joinedRoom() == "" {
				select {
				case <-streamCtx.Done():
					return
				case <-time.After(200 * time.Millisecond):
				}
				continue
			}
			// Подписка живёт на одну комнату и имеет собственный контекст:
			// /join другой комнаты или /leave отменяют ровно этот запрос.
			roomCtx, cancelRoom := context.WithCancel(streamCtx)
			room := m.beginWatch(cancelRoom)
			if room == "" {
				m.endWatch()
				cancelRoom()
				continue
			}
			err := c.Watch(roomCtx, room, cursors.get(room), func(e rooms.Event) error {
				// Курсор здесь не двигается — его подтверждает цикл отрисовки,
				// когда событие реально показано.
				return deliverEvent(roomCtx, streamCtx, events, e)
			})
			if streamCtx.Err() != nil {
				cancelRoom()
				m.endWatch()
				return
			}
			if roomCtx.Err() != nil {
				// Подписку отменили намеренно: смена комнаты или /leave.
				// Это не обрыв, о котором нужно сообщать owner.
				cancelRoom()
				m.endWatch()
				continue
			}
			if err != nil {
				pushNotice(events, "поток событий прерван; /join "+room+" переподключит")
			}
			cancelRoom()
			m.endWatch()
			select {
			case <-streamCtx.Done():
				return
			case <-time.After(200 * time.Millisecond):
			}
		}
	}()
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 4096), 16<<10)
	lineCh := make(chan string)
	errCh := make(chan error, 1)
	// Ввод читается отдельной горутиной, поэтому выход по /quit или отмене
	// контекста должен освобождать её: иначе она навсегда осталась бы на
	// отправке уже прочитанной строки в небуферизованный канал.
	inputCtx, cancelInput := context.WithCancel(ctx)
	defer cancelInput()
	go func() {
		defer close(lineCh)
		pumpInput(inputCtx, scanner, lineCh, errCh)
	}()
	redraw := true
	ticker := time.NewTicker(observe)
	defer ticker.Stop()
	for {
		if redraw {
			if _, err := fmt.Fprint(w, renderTUI(m)+"\n"); err != nil {
				return err
			}
			w.Flush()
			redraw = false
		}
		select {
		case ev := <-events:
			// Событие другой комнаты не показывается и курсор не двигает:
			// её хвост будет перечитан при возврате. Уже показанное событие
			// (Seq не выше подтверждённого) тоже не дублируется — иначе возврат
			// в комнату показал бы одни и те же строки дважды.
			if ev.Kind == "system" {
				m.note(formatEvent(ev))
			} else if m.joinedRoom() == ev.Room {
				if ev.Seq > cursors.get(ev.Room) {
					m.note(formatEvent(ev))
					cursors.ack(ev.Room, ev.Seq)
				}
			}
			redraw = true
		case <-ticker.C:
			// Карточки наблюдаемы во времени: внешний supervisor может поставить
			// или снять blocker, и кадр обязан это показать без ввода команды.
			if err := m.refresh(c, ctx, false); err != nil && !Offline(err) {
				m.note("! наблюдение недоступно: " + err.Error())
			}
			redraw = true
		case err := <-errCh:
			return err
		case line, ok := <-lineCh:
			if !ok {
				return nil
			}
			line = strings.TrimSpace(line)
			if line == "/quit" || line == "/exit" {
				return nil
			}
			handled, err := m.handleCommand(c, ctx, line)
			if err != nil {
				m.note("! " + err.Error())
			}
			if handled {
				redraw = true
				continue
			}
			if line != "" {
				room, _, _, _ := m.snapshotState()
				if room == "" {
					m.setNotice("нет активной комнаты: сначала /join <room>")
				} else if err := c.sayInRoom(ctx, room, line); err != nil {
					m.setNotice("реплика не принята: " + err.Error())
				} else {
					m.setNotice("")
				}
				redraw = true
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (m *tuiModel) handleCommand(c *Client, ctx context.Context, line string) (bool, error) {
	m.setNotice("")
	if !strings.HasPrefix(line, "/") {
		return false, nil
	}
	fields := strings.Fields(line)
	name := strings.ToLower(fields[0])
	args := fields[1:]
	if (name == "/block" || name == "/unblock") && !m.ownerCapable {
		return true, fmt.Errorf("нужны owner-полномочия (запустите TUI из CLI владельца состояния)")
	}
	switch name {
	case "/help":
		m.note(tuiHelpText)
	case "/models":
		return true, m.refresh(c, ctx, true)
	case "/connect":
		if len(args) != 1 {
			return true, fmt.Errorf("использование: /connect <агент> (см. /models)")
		}
		res, err := c.Call(ctx, "connect", Request{Value: args[0]})
		if err != nil {
			return true, err
		}
		if res.Peer != nil {
			m.setStatus(fmt.Sprintf("подключён %s [%s]", res.Peer.Address, res.Peer.Ref))
		}
		return true, m.refreshPeers(c, ctx)
	case "/sessions":
		return true, m.refresh(c, ctx, false)
	case "/rooms":
		return true, m.refreshRooms(c, ctx)
	case "/join":
		if len(args) != 1 {
			return true, fmt.Errorf("использование: /join <room-id или номер из /rooms>")
		}
		id := m.resolveRoom(args[0])
		if id == "" {
			// один автоматический refresh: комнаты могли появиться после последнего /rooms
			if err := m.refreshRooms(c, ctx); err != nil {
				return true, err
			}
			id = m.resolveRoom(args[0])
		}
		if id == "" {
			return true, fmt.Errorf("комната %q не найдена; выполните /rooms", args[0])
		}
		m.setJoined(id)
		m.note("— вы в комнате " + id + "; печатайте реплики, они уходят owner-актором —")
		return true, nil
	case "/leave":
		m.setJoined("")
		return true, nil
	case "/status":
		res, err := c.Call(ctx, "status", Request{})
		if err != nil {
			if Offline(err) {
				m.setStatus("daemon остановлен")
				return true, nil
			}
			return true, err
		}
		m.setStatus(formatStatus(res.Status))
		return true, nil
	case "/block":
		if len(args) < 2 {
			return true, fmt.Errorf("использование: /block <session> <reason> [retry_at RFC3339]")
		}
		req := Request{Value: args[0], Reason: args[1], Source: "owner"}
		if len(args) > 2 {
			req.RetryAt = args[2]
		}
		_, err := c.Call(ctx, "session-block", req)
		if err != nil {
			return true, err
		}
		m.setNotice("блокер записан; назначения остановлены, checkpoint не тронут")
		return true, m.refresh(c, ctx, false)
	case "/unblock":
		if len(args) != 1 {
			return true, fmt.Errorf("использование: /unblock <session> (сначала проверьте checkpoint)")
		}
		if _, err := c.Call(ctx, "session-unblock", Request{Value: args[0]}); err != nil {
			return true, err
		}
		m.setNotice("блокер снят явным действием owner")
		return true, m.refresh(c, ctx, false)
	default:
		m.note("! неизвестная команда " + name + "; /help")
	}
	return true, nil
}

func (m *tuiModel) resolveRoom(arg string) string {
	if n, err := strconv.Atoi(arg); err == nil && n >= 1 && n <= len(m.rooms) {
		return m.rooms[n-1].id
	}
	for _, r := range m.rooms {
		if r.id == arg {
			return r.id
		}
	}
	return ""
}

func (m *tuiModel) refreshPeers(c *Client, ctx context.Context) error {
	res, err := c.Call(ctx, "peers", Request{})
	m.peersAt = time.Now()
	if err != nil {
		if Offline(err) {
			m.peersErr = "daemon недоступен"
		} else {
			m.peersErr = err.Error()
		}
		return nil
	}
	m.peersErr = ""
	m.peers = res.Peers
	sort.Slice(m.peers, func(i, j int) bool { return m.peers[i].Address < m.peers[j].Address })
	return nil
}

func (m *tuiModel) refreshRooms(c *Client, ctx context.Context) error {
	res, err := c.Call(ctx, "rooms", Request{})
	if err != nil {
		return err
	}
	m.rooms = m.rooms[:0]
	for _, r := range res.Rooms {
		m.rooms = append(m.rooms, tuiRoomBrief{id: r.ID, task: r.Task, title: r.Title, closed: r.Closed})
	}
	sort.Slice(m.rooms, func(i, j int) bool { return m.rooms[i].id < m.rooms[j].id })
	return nil
}

func (m *tuiModel) refresh(c *Client, ctx context.Context, withPeers bool) error {
	if withPeers {
		if err := m.refreshPeers(c, ctx); err != nil {
			return err
		}
	}
	if err := m.refreshRooms(c, ctx); err != nil {
		return err
	}
	res, err := c.Call(ctx, "availability", Request{})
	if err != nil {
		return err
	}
	m.avail = res.Availability
	sort.Slice(m.avail, func(i, j int) bool { return m.avail[i].SessionID < m.avail[j].SessionID })
	m.observedAt = time.Now()
	return nil
}

func (c *Client) sayInRoom(ctx context.Context, room, text string) error {
	_, err := c.Call(ctx, "say", Request{Room: room, Text: text})
	return err
}

func pushNotice(ch chan rooms.Event, text string) {
	select {
	case ch <- rooms.Event{Kind: "system", Text: text}:
	default:
	}
}

func formatEvent(e rooms.Event) string {
	at := ""
	if !e.At.IsZero() {
		at = e.At.Local().Format("15:04:05") + " "
	}
	who := e.Agent
	if e.Role != "" {
		who += "/" + e.Role
	}
	if who == "" {
		who = e.Kind
	}
	return at + "[" + e.Kind + "] " + who + ": " + e.Text
}

func formatStatus(st *Status) string {
	if st == nil {
		return "нет состояния"
	}
	out := "подключение: " + map[bool]string{true: "активно", false: "не активно"}[st.Connected]
	if st.Peer != nil {
		out += ", агент " + st.Peer.Address
	}
	if st.Last != nil {
		out += ", последняя реплика " + string(st.Last.Status)
		if st.Last.ReceiptCommitted {
			out += " (receipt подтверждён)"
		} else {
			out += " (receipt НЕ подтверждён)"
		}
	}
	if st.Waiting {
		out += ", ожидание ответа"
	}
	return out
}

// renderTUI собирает один кадр: шапка подключения, проекты, чат-область,
// карточки сессий с РАЗДЕЛЬНЫМИ полями и строка состояния.
func renderTUI(m *tuiModel) string {
	var b strings.Builder
	b.WriteString("┌─ Coord · human TUI ────────────────────────────────────────────────┐\n")
	peersAge := "никогда"
	if !m.peersAt.IsZero() {
		peersAge = m.peersAt.Local().Format("15:04:05")
	}
	fmt.Fprintf(&b, "│ агенты: %d (наблюдено %s)%s\n", len(m.peers), peersAge, suffixIf(m.peersErr != "", " ⚠ "+m.peersErr))
	if len(m.peers) > 0 {
		for _, p := range m.peers {
			fmt.Fprintf(&b, "│   • %s [%s] — %s\n", p.Address, p.Ref, clip(p.Activity, 40))
		}
	} else if m.peersErr == "" {
		b.WriteString("│   (нет доступных агентов)\n")
	}
	b.WriteString("├─ Проекты (комнаты) ────────────────────────────────────────────────┤\n")
	if len(m.rooms) == 0 {
		b.WriteString("│   (пусто; coord start <задача> --coordinator <агент>)\n")
	}
	for i, r := range m.rooms {
		marker := "→"
		if r.id != m.joined {
			marker = " "
		}
		state := "открыта"
		if r.closed {
			state = "закрыта"
		}
		fmt.Fprintf(&b, "│ %s %d) %s — %s (%s)\n", marker, i+1, r.id, clip(r.title, 48), state)
	}
	b.WriteString("├─ Чат" + suffixIf(m.joined != "", " · "+m.joined) + " ─────────────────────────────────────────────────┤\n")
	lines := m.history
	if len(lines) > 12 {
		lines = lines[len(lines)-12:]
	}
	if len(lines) == 0 {
		b.WriteString("│   (пусто; /join <room>, затем печатайте текст)\n")
	}
	for _, l := range lines {
		b.WriteString("│ " + l + "\n")
	}
	b.WriteString("├─ Состояния сессий (наблюдено " + m.observedAt.Local().Format("15:04:05") + ") ──────────────────┤\n")
	if len(m.avail) == 0 {
		b.WriteString("│   (сессий нет)\n")
	}
	for _, a := range m.avail {
		fmt.Fprintf(&b, "│ %s · %s · gen%d · %s\n", a.SessionID, a.AgentID, a.Generation, a.State)
		fmt.Fprintf(&b, "│   attach=%s reach=%s ready=%s provider=%s | %s\n", a.Attachment, a.Reachability, a.Readiness, a.Provider, a.Processing)
		exp := a.Expectation
		if exp == "" {
			exp = "—"
		}
		b.WriteString("│   ждём: " + clip(exp, 78) + "\n")
	}
	status := firstNonEmpty(m.notice, m.lastStatus, "/help · Ctrl+C — выход")
	b.WriteString("└─ " + clip(status, 70) + "\n")
	b.WriteString("> ")
	return b.String()
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func suffixIf(cond bool, s string) string {
	if cond {
		return s
	}
	return ""
}

func appendBounded(lines []string, line string, max int) []string {
	lines = append(lines, strings.Split(line, "\n")...)
	if len(lines) > max {
		lines = lines[len(lines)-max:]
	}
	return lines
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

type bufferedWriter struct {
	w *bufio.Writer
}

func newBufioWriter(w io.Writer) *bufferedWriter {
	return &bufferedWriter{w: bufio.NewWriter(w)}
}

func (b *bufferedWriter) Write(p []byte) (int, error) { return b.w.Write(p) }
func (b *bufferedWriter) Flush()                      { _ = b.w.Flush() }
