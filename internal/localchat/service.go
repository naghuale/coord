//go:build unix

// Package localchat adapts existing Telepathy sessions to local Coord conversations.
// The trusted boundary is a private Unix socket owned by the same OS user.
package localchat

import (
	"context"
	"coord"
	"coord/checkpoint"
	"coord/envelope"
	"coord/executor"
	"coord/fsm"
	"coord/internal/atomicfile"
	"coord/internal/rooms"
	"coord/retry"
	"coord/store"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// ErrNotConfirmed distinguishes delivered invitations from accepted connections.
var ErrNotConfirmed = errors.New("coord-подключение не подтверждено: агент не принял приглашение")

// ErrNotConnected requires an actual invitation acceptance before chat.
var ErrNotConnected = errors.New("нет подключения; выполните coord connect <агент>")

// ErrInvalidInput marks a malformed request so callers can branch on the class
// of failure instead of retrying the same payload.
var ErrInvalidInput = errors.New("неверный запрос")

// Config controls local state and observable timeouts.
type Config struct {
	State, Executable            string
	Now                          func() time.Time
	ConnectTimeout, ReplyTimeout time.Duration
}

// Tools is the actual Telepathy text-tool boundary.
type Tools interface {
	Call(context.Context, string, map[string]any) (string, error)
}

// Peer retains the exact transport ref to prevent ambiguous name routing.
type Peer struct {
	Address  string `json:"address"`
	Ref      string `json:"ref"`
	Activity string `json:"activity"`
}

// Answer is returned only after durable RESULT_ACK processing.
type Answer struct {
	Run  string `json:"run"`
	Text string `json:"text"`
}

// LastRun distinguishes canonical outcome from local receipt completion.
type LastRun struct {
	Run              string     `json:"run"`
	Status           fsm.Status `json:"status"`
	ReceiptCommitted bool       `json:"receipt_committed"`
}

// Status reports connection and checkpoint state without exposing capability tokens.
type Status struct {
	Connected bool     `json:"connected"`
	Peer      *Peer    `json:"peer,omitempty"`
	Last      *LastRun `json:"last,omitempty"`
	Waiting   bool     `json:"waiting"`
}
type invitation struct {
	ctx       context.Context
	token     string
	peer      Peer
	accepted  chan struct{}
	expires   time.Time
	confirmed bool
}
type pending struct {
	ctx     context.Context
	token   string
	peer    Peer
	run     string
	answer  chan string
	receipt chan envelope.Envelope
	task    envelope.Envelope
	replied bool
}

// Service serializes conversations while permitting connection and reply callbacks.
type Service struct {
	owner       string
	rooms       *rooms.Manager
	roomDone    chan struct{}
	confirmed   bool
	cfg         Config
	tools       Tools
	mu          sync.Mutex
	journalMu   sync.Mutex
	selected    *Peer
	invite      *invitation
	active      *pending
	store       *store.Store
	coordinator *coord.Coordinator
	serial      chan struct{}
	life        context.Context
	cancel      context.CancelFunc
	jobs        sync.WaitGroup
	closed      bool
}

var listing = regexp.MustCompile(`(?m)^- (\S+) \[([^\]]+)\](.*)$`)

// New opens the exclusive canonical store; it never starts an AI process.
func New(cfg Config, tools Tools) (*Service, error) {
	if tools == nil || cfg.Now == nil || !filepath.IsAbs(cfg.State) || !filepath.IsAbs(cfg.Executable) {
		return nil, fmt.Errorf("absolute state/executable, tools and clock required")
	}
	if cfg.ConnectTimeout == 0 {
		cfg.ConnectTimeout = 2 * time.Minute
	}
	if cfg.ReplyTimeout == 0 {
		cfg.ReplyTimeout = 5 * time.Minute
	}
	if cfg.ConnectTimeout <= 0 || cfg.ReplyTimeout <= 0 {
		return nil, fmt.Errorf("positive timeouts required")
	}
	if err := os.MkdirAll(cfg.State, 0700); err != nil {
		return nil, err
	}
	info, err := os.Stat(cfg.State)
	if err != nil {
		return nil, err
	}
	if info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("state directory must be private (0700): %s", cfg.State)
	}
	canonical, err := store.New(checkpoint.File{Path: filepath.Join(cfg.State, "runs.json")})
	if err != nil {
		return nil, err
	}
	owner, err := loadOwner(cfg.State)
	if err != nil {
		return nil, errors.Join(err, canonical.Close())
	}
	life, cancel := context.WithCancel(context.Background())
	s := &Service{owner: owner, cfg: cfg, tools: tools, store: canonical, serial: make(chan struct{}, 1), life: life, cancel: cancel}
	s.serial <- struct{}{}
	options := coord.DefaultConfig()
	options.Now = cfg.Now
	s.coordinator, err = coord.New(hostTransport{s}, canonical, options)
	if err != nil {
		cancel()
		return nil, errors.Join(err, canonical.Close())
	}
	// A persisted selection is only routing memory. Every explicit connect still
	// performs a fresh challenge; no execution is resumed automatically.
	raw, err := os.ReadFile(filepath.Join(cfg.State, "connection.json"))
	if err == nil {
		var p Peer
		if err = json.Unmarshal(raw, &p); err == nil && p.Ref != "" {
			s.selected = &p
		} else {
			cancel()
			return nil, errors.Join(fmt.Errorf("invalid connection state"), canonical.Close())
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		cancel()
		return nil, errors.Join(err, canonical.Close())
	}
	s.rooms, err = rooms.Open(rooms.Config{Path: filepath.Join(cfg.State, "rooms.json"), State: cfg.State, Executable: cfg.Executable, Now: cfg.Now}, tools)
	if err != nil {
		cancel()
		return nil, errors.Join(err, canonical.Close())
	}
	s.roomDone = make(chan struct{})
	go func() { defer close(s.roomDone); s.rooms.Run(s.life) }()
	return s, nil
}

// Close cancels active requests before releasing canonical ownership.
func (s *Service) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.cancel()
	s.mu.Unlock()
	s.jobs.Wait()
	<-s.roomDone
	return errors.Join(s.rooms.Close(), s.store.Close())
}
func (s *Service) begin(ctx context.Context, d time.Duration) (context.Context, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, nil, fmt.Errorf("local Coord is closed")
	}
	s.jobs.Add(1)
	child, cancel := context.WithTimeout(ctx, d)
	stop := context.AfterFunc(s.life, cancel)
	return child, func() { stop(); cancel(); s.jobs.Done() }, nil
}

// Peers lists actual sessions; plain transport reachability is not Coord acceptance.
func (s *Service) Peers(ctx context.Context) ([]Peer, error) {
	text, err := s.tools.Call(ctx, "list_peers", map[string]any{})
	if err != nil {
		return nil, err
	}
	var peers []Peer
	for _, m := range listing.FindAllStringSubmatch(text, -1) {
		peers = append(peers, Peer{m[1], m[2], strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(m[3]), "·"))})
	}
	return peers, nil
}
func token() (string, error) {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}
func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
func (s *Service) command(action, value string) string {
	return quote(s.cfg.Executable) + " --state " + quote(s.cfg.State) + " " + action + " " + quote(value)
}

// Connect sends a one-time capability invitation to an existing peer and waits
// for its accept callback. No message body or ordinary PONG can satisfy it.
func (s *Service) Connect(ctx context.Context, address string) (Peer, error) {
	ctx, done, err := s.begin(ctx, s.cfg.ConnectTimeout)
	if err != nil {
		return Peer{}, err
	}
	defer done()
	peers, err := s.Peers(ctx)
	if err != nil {
		return Peer{}, err
	}
	var matches []Peer
	for _, p := range peers {
		if p.Ref == address || p.Address == address {
			matches = append(matches, p)
		}
	}
	if len(matches) != 1 {
		return Peer{}, fmt.Errorf("агент не найден или имя неоднозначно; выполните coord peers и используйте ref")
	}
	nonce, err := token()
	if err != nil {
		return Peer{}, err
	}
	invite := &invitation{ctx: ctx, token: nonce, peer: matches[0], accepted: make(chan struct{}), expires: s.cfg.Now().Add(s.cfg.ConnectTimeout)}
	s.mu.Lock()
	if s.invite != nil || s.active != nil {
		s.mu.Unlock()
		return Peer{}, fmt.Errorf("уже идёт подключение или разговор")
	}
	s.invite = invite
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if s.invite == invite {
			s.invite = nil
		}
		s.mu.Unlock()
	}()
	text := "[Coord: приглашение к беседе]\nПользователь хочет общаться с твоей существующей сессией через локальный Coord. Новую модель запускать и файлы проекта менять не нужно.\nЧтобы подтвердить подключение, выполни одну команду:\n" + s.command("accept", nonce) + "\nПосле подтверждения Coord будет присылать вопросы и команду ответа. Если команда недоступна, сообщи об этом отправителю. Обычный текстовый PONG не подтверждает подключение."
	if _, err = s.tools.Call(ctx, "send_message", map[string]any{"to": invite.peer.Ref, "message": text}); err != nil {
		return Peer{}, fmt.Errorf("приглашение не доставлено: %w", err)
	}
	select {
	case <-invite.accepted:
		return invite.peer, nil
	case <-ctx.Done():
		return Peer{}, errors.Join(ErrNotConfirmed, ctx.Err())
	}
}

// Accept validates a one-use invitation delivered to the selected Telepathy peer.
func (s *Service) Accept(nonce string) error {
	if strings.HasPrefix(nonce, "room.") {
		return s.rooms.Accept(nonce)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.invite
	if s.closed || p == nil || p.ctx.Err() != nil || p.token != nonce || p.confirmed || !s.cfg.Now().Before(p.expires) {
		return fmt.Errorf("приглашение неизвестно, истекло или уже принято")
	}
	raw, err := json.Marshal(p.peer)
	if err != nil {
		return err
	}
	if err = atomicfile.Write(filepath.Join(s.cfg.State, "connection.json"), raw); err != nil {
		return err
	}
	peer := p.peer
	s.selected = &peer
	s.confirmed = true
	p.confirmed = true
	close(p.accepted)
	return nil
}

// Reply delivers an agent answer to exactly one live request, without interpreting
// it as a shell command or accepting an unrelated text message.
func (s *Service) Reply(nonce, text string) error {
	if strings.TrimSpace(text) == "" || len(text) > 32<<10 {
		return fmt.Errorf("ответ должен содержать от 1 до 32768 байт")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.active
	if s.closed || p == nil || p.ctx.Err() != nil || p.token != nonce || p.replied {
		return fmt.Errorf("вопрос неизвестен, завершён или ответ уже принят")
	}
	// Keep the answer durable even if the waiting client disappears immediately.
	raw, err := json.Marshal(Answer{p.run, text})
	if err != nil {
		return err
	}
	if err = atomicfile.Write(filepath.Join(s.cfg.State, "answers", p.run+".json"), raw); err != nil {
		return err
	}
	p.replied = true
	p.answer <- text
	return nil
}
func (s *Service) worktree(ctx context.Context) (string, string, error) {
	dir := filepath.Join(s.cfg.State, "conversation")
	if _, err := os.Stat(filepath.Join(dir, ".git")); errors.Is(err, os.ErrNotExist) {
		if err = os.MkdirAll(dir, 0700); err != nil {
			return "", "", err
		}
		args := [][]string{{"init", "--quiet", dir}, {"-C", dir, "-c", "user.name=Coord", "-c", "user.email=coord@localhost.invalid", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "--quiet", "-m", "local conversation"}}
		for _, a := range args {
			if out, e := exec.CommandContext(ctx, "git", a...).CombinedOutput(); e != nil {
				return "", "", fmt.Errorf("conversation repository: %s %w", out, e)
			}
		}
	} else if err != nil {
		return "", "", err
	}
	raw, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "HEAD").Output()
	return dir, strings.TrimSpace(string(raw)), err
}

// Chat runs one actual coordinator/executor lifecycle around the existing agent's
// response. The local adapter waits BLOCKED; it does not claim the model is working.
func (s *Service) Chat(ctx context.Context, text string) (answer Answer, err error) {
	if strings.TrimSpace(text) == "" || len(text) > 16<<10 {
		return answer, fmt.Errorf("сообщение должно содержать от 1 до 16384 байт")
	}
	ctx, done, err := s.begin(ctx, s.cfg.ReplyTimeout)
	if err != nil {
		return answer, err
	}
	defer done()
	select {
	case <-ctx.Done():
		return answer, ctx.Err()
	case <-s.serial:
	}
	defer func() { s.serial <- struct{}{} }()
	s.mu.Lock()
	if s.selected == nil || !s.confirmed {
		s.mu.Unlock()
		return answer, ErrNotConnected
	}
	peer := *s.selected
	if s.invite != nil {
		s.mu.Unlock()
		return answer, fmt.Errorf("подключение ещё не завершено")
	}
	s.mu.Unlock()
	peers, err := s.Peers(ctx)
	if err != nil {
		return answer, err
	}
	found := false
	for _, p := range peers {
		if p.Ref == peer.Ref {
			found = true
		}
	}
	if !found {
		return answer, fmt.Errorf("подключённый агент больше не доступен; выполните coord connect")
	}
	worktree, sha, err := s.worktree(ctx)
	if err != nil {
		return answer, err
	}
	release, err := (checkpoint.File{Path: filepath.Join(worktree, ".coord-writer")}).Lock()
	if err != nil {
		return answer, err
	}
	defer func() { err = errors.Join(err, release()) }()
	nonce, err := token()
	if err != nil {
		return answer, err
	}
	id := envelope.NewID()
	p := &pending{ctx: ctx, token: nonce, peer: peer, run: id, answer: make(chan string, 1), receipt: make(chan envelope.Envelope, 4)}
	s.mu.Lock()
	s.active = p
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if s.active == p {
			s.active = nil
		}
		s.mu.Unlock()
	}()
	// Allow bounded cancellation cleanup to commit FAILURE before protocol deadline.
	deadline := s.cfg.Now().Add(s.cfg.ReplyTimeout + 30*time.Second)
	r := fsm.Run{Task: "conversation", RunID: id, Attempt: 1, Fence: 1, TargetSHA: sha, OwnerSession: peer.Ref, Worktree: worktree, PayloadRef: "conversation:" + id, Deadline: deadline}
	cp := checkpoint.File{Path: filepath.Join(s.cfg.State, "executors", id+".json")}
	x := &executor.Executor{Transport: peerTransport{s, peer.Ref}, Checkpoint: cp, Policy: retry.Policy{BaseDelay: time.Second, MaxDelay: time.Second, Multiplier: 1}, UpdateInterval: 2 * time.Minute, SendTimeout: 10 * time.Second, Now: s.cfg.Now}
	if err = s.coordinator.Assign(ctx, r); err != nil {
		return answer, err
	}
	if _, err = x.Accept(ctx, p.task); err != nil {
		return answer, err
	}
	if err = x.Heartbeat(ctx, p.task, "BLOCKED", 0, "AWAITING_AGENT_REPLY"); err != nil {
		return answer, err
	}
	finish := func(text string, cause error) (Answer, error) {
		cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer stop()
		outcome := "SUCCESS"
		if cause != nil {
			outcome = "FAILURE"
		}
		if e := x.Heartbeat(cleanup, p.task, "RESULT_READY", 100, ""); e != nil {
			return Answer{}, errors.Join(cause, e)
		}
		if e := x.Result(cleanup, p.task, envelope.ResultPayload{Outcome: outcome, Summary: text}); e != nil {
			return Answer{}, errors.Join(cause, e)
		}
		select {
		case receipt := <-p.receipt:
			if e := x.Receipt(cleanup, receipt); e != nil {
				return Answer{}, errors.Join(cause, e)
			}
		case <-cleanup.Done():
			return Answer{}, errors.Join(cause, fmt.Errorf("RESULT_ACK не подтверждён: %w", cleanup.Err()))
		}
		return Answer{id, text}, cause
	}
	body := "[Coord: вопрос]\n" + text + "\n\nОтветь от своей текущей сессии. Не запускай другого агента и не меняй файлы проекта. Передай ответ одной командой:\n" + s.command("reply", nonce) + " 'текст ответа'\nДля многострочного текста эта же команда поддерживает --stdin. Coord сам сохранит результат и обработает RESULT_ACK."
	if _, err = s.tools.Call(ctx, "send_message", map[string]any{"to": peer.Ref, "message": body}); err != nil {
		return finish("Вопрос не доставлен агенту", err)
	}
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case reply := <-p.answer:
			return finish(reply, nil)
		case <-ctx.Done():
			return finish("Ожидание ответа прервано", ctx.Err())
		case <-ticker.C:
			if err = x.Heartbeat(ctx, p.task, "BLOCKED", 0, "AWAITING_AGENT_REPLY"); err != nil {
				return answer, err
			}
		}
	}
}

// Status reads persisted evidence; after a daemon restart unfinished runs remain
// visible and are never silently re-executed.
func (s *Service) Status() Status {
	s.mu.Lock()
	status := Status{Waiting: s.active != nil, Connected: s.confirmed}
	if s.selected != nil {
		p := *s.selected
		status.Peer = &p
	}
	s.mu.Unlock()
	var latest fsm.Run
	for _, r := range s.store.All() {
		if latest.RunID == "" || r.LastUpdate.After(latest.LastUpdate) {
			latest = r
		}
	}
	if latest.RunID != "" {
		last := &LastRun{Run: latest.RunID, Status: latest.Status}
		cp := checkpoint.File{Path: filepath.Join(s.cfg.State, "executors", latest.RunID+".json")}
		if runs, err := cp.Load(); err == nil {
			local, ok := runs[latest.RunID]
			last.ReceiptCommitted = ok && local.Terminal() && len(local.Outbox) == 0 && local.Status == latest.Status
		}
		status.Last = last
	}
	return status
}
func (s *Service) event(e envelope.Envelope) error {
	s.journalMu.Lock()
	defer s.journalMu.Unlock()
	raw, err := json.Marshal(struct {
		Kind     envelope.Kind     `json:"kind"`
		Envelope envelope.Envelope `json:"envelope"`
	}{e.Kind, e})
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(s.cfg.State, "events.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(append(raw, '\n'))
	return errors.Join(writeErr, f.Sync(), f.Close())
}

type hostTransport struct{ s *Service }

func (hostTransport) WhoAmI() string                                   { return "coord-local" }
func (hostTransport) Receive(context.Context) <-chan envelope.Envelope { return nil }
func (t hostTransport) Send(_ context.Context, to string, e envelope.Envelope) error {
	if err := t.s.event(e); err != nil {
		return err
	}
	t.s.mu.Lock()
	defer t.s.mu.Unlock()
	p := t.s.active
	if p == nil || p.run != e.Run || p.peer.Ref != to {
		return fmt.Errorf("no active local adapter for message")
	}
	switch e.Kind {
	case envelope.Task:
		p.task = e
	case envelope.ResultAck:
		select {
		case p.receipt <- e:
		default:
			return fmt.Errorf("receipt queue full")
		}
	default:
		return fmt.Errorf("unsupported local adapter message %s", e.Kind)
	}
	return nil
}

type peerTransport struct {
	s    *Service
	peer string
}

func (t peerTransport) WhoAmI() string                                 { return t.peer }
func (peerTransport) Receive(context.Context) <-chan envelope.Envelope { return nil }
func (t peerTransport) Send(ctx context.Context, to string, e envelope.Envelope) error {
	if e.From != t.peer || to != "coord-local" {
		return fmt.Errorf("local adapter identity mismatch")
	}
	if err := t.s.event(e); err != nil {
		return err
	}
	return t.s.coordinator.Handle(ctx, e)
}
