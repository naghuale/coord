//go:build unix

// Package rooms persists task-scoped collaboration between existing AI sessions.
// Owners observe the same event stream that agents receive; no model is spawned.
package rooms

import (
	"bytes"
	"context"
	"coord/checkpoint"
	"coord/envelope"
	"coord/internal/atomicfile"
	"coord/roles"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// Tools delivers notifications through the installed Telepathy tool API.
type Tools interface {
	Call(context.Context, string, map[string]any) (string, error)
}

// Config binds state and callback commands to this private local installation.
type Config struct {
	Path, State, Executable string
	Now                     func() time.Time
	Persist                 func(string, []byte) error
}

// Event is an immutable room record, visible to owner and all participants.
type Event struct {
	ID         string    `json:"id"`
	Seq        uint64    `json:"seq"`
	Room       string    `json:"room"`
	At         time.Time `json:"at"`
	Kind       string    `json:"kind"`
	Agent      string    `json:"agent"`
	Role       string    `json:"role"`
	Text       string    `json:"text"`
	Session    string    `json:"session,omitempty"`
	Generation uint64    `json:"generation,omitempty"`
}

// Member exposes membership without invitation or callback secrets.
type Member struct {
	State            string       `json:"state"`
	Regulation       string       `json:"regulation"`
	Ready            bool         `json:"ready"`
	Readiness        Readiness    `json:"readiness"`
	SessionID        string       `json:"session_id"`
	RoomID           string       `json:"room_id,omitempty"`
	Task             string       `json:"task,omitempty"`
	Generation       uint64       `json:"generation"`
	ConnectionID     string       `json:"connection_id,omitempty"`
	Instructions     string       `json:"instructions"`
	Checkpoint       Checkpoint   `json:"checkpoint"`
	Assignments      []Assignment `json:"assignments"`
	ConnectedAt      time.Time    `json:"connected_at"`
	IndependentAudit bool         `json:"independent_audit"`
	AgentID          string       `json:"agent_id"`
	Address          string       `json:"address"`
	Ref              string       `json:"ref"`
	Role             string       `json:"role"`
	Active           bool         `json:"active"`
	Blocker          *Blocker     `json:"blocker,omitempty"`
}

// Room is a read-only public snapshot.
type Room struct {
	ID             string    `json:"id"`
	Task           string    `json:"task"`
	Title          string    `json:"title"`
	Closed         bool      `json:"closed"`
	Members        []Member  `json:"members"`
	LastActivity   time.Time `json:"last_activity"`
	Pending        int       `json:"pending"`
	DeliveryErrors int       `json:"delivery_errors"`
}
type member struct {
	Member
	Token   string
	Invite  string
	Expires time.Time
}
type room struct {
	ID, Task, Title   string
	ControllerSession string
	Closed            bool
	Members           map[string]member
	Events            []Event
}
type delivery struct {
	ID, Room, Peer, Event, Kind string
	Due                         time.Time
	Delivered                   bool
	Tries                       int
	LastError                   string
}
type snapshot struct {
	Version       int
	Rooms         map[string]room
	Outbox        []delivery
	Revision      uint64
	Bindings      map[string]string
	Operations    map[string]operation
	Registrations map[string]registration
	History       historyState
}

// Manager owns one room snapshot. Publication follows durable persistence.
type Manager struct {
	mu        sync.Mutex
	dispatch  sync.Mutex
	historyMu sync.Mutex
	cfg       Config
	tools     Tools
	data      snapshot
	release   func() error
	closed    bool
	failure   error
}

// Open acquires exclusive ownership and validates persisted room state.
func Open(cfg Config, tools Tools) (*Manager, error) {
	if cfg.Path == "" || cfg.State == "" || cfg.Executable == "" || cfg.Now == nil || tools == nil {
		return nil, fmt.Errorf("room storage, executable, tools and clock required")
	}
	release, err := (checkpoint.File{Path: cfg.Path}).Lock()
	if err != nil {
		return nil, err
	}
	m := &Manager{cfg: cfg, tools: tools, release: release, data: snapshot{Version: 2, Rooms: map[string]room{}, Bindings: map[string]string{}, Operations: map[string]operation{}, Registrations: map[string]registration{}}}
	raw, err := os.ReadFile(cfg.Path)
	if err == nil {
		if err = json.Unmarshal(raw, &m.data); err == nil {
			if m.data.Version != 2 || m.data.Rooms == nil || m.data.Bindings == nil || m.data.Operations == nil || m.data.Registrations == nil {
				err = fmt.Errorf("invalid room snapshot")
			}
			for id, r := range m.data.Rooms {
				if id != r.ID || r.Task == "" || r.Members == nil {
					err = fmt.Errorf("invalid room identity")
					break
				}
				for i, e := range r.Events {
					if e.Room != id || e.Seq != uint64(i+1) || e.ID == "" {
						err = fmt.Errorf("invalid room event sequence")
						break
					}
				}
				for _, p := range r.Members {
					if p.SessionID == "" || p.AgentID == "" || (p.Active && p.Token == "") || !roles.Role(strings.ToUpper(p.Role)).Valid() {
						err = fmt.Errorf("invalid room membership")
						break
					}
				}
			}
		}
	} else if errors.Is(err, os.ErrNotExist) {
		err = nil
	}
	if err == nil {
		err = validateSnapshot(m.data)
	}
	if err != nil {
		return nil, errors.Join(err, release())
	}
	return m, nil
}

// Close releases the snapshot lock after dispatch stops.
func (m *Manager) Close() error {
	m.dispatch.Lock()
	defer m.dispatch.Unlock()
	m.historyMu.Lock()
	defer m.historyMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	m.closed = true
	return m.release()
}
func (m *Manager) update(fn func(*snapshot) error) error { return m.updateWithRevision(fn, true) }
func (m *Manager) updateWithRevision(fn func(*snapshot) error, advance bool) error {
	if m.closed {
		return fmt.Errorf("room manager closed")
	}
	if m.failure != nil {
		return m.failure
	}
	raw, err := json.Marshal(m.data)
	if err != nil {
		return err
	}
	var next snapshot
	if err = json.Unmarshal(raw, &next); err != nil {
		return err
	}
	if err = fn(&next); err != nil {
		return err
	}
	comparison, e := json.Marshal(next)
	if e != nil {
		return e
	}
	if bytes.Equal(comparison, raw) {
		return nil
	}
	if next.Revision == ^uint64(0) {
		return fmt.Errorf("revision exhausted")
	}
	if advance {
		next.Revision++
	}
	raw, err = json.Marshal(next)
	if err != nil {
		return err
	}
	persist := m.cfg.Persist
	if persist == nil {
		persist = atomicfile.Write
	}
	if err = persist(m.cfg.Path, raw); err != nil {
		m.failure = fmt.Errorf("room persistence failed; reopen required: %w", err)
		return m.failure
	}
	var published snapshot
	if err = json.Unmarshal(raw, &published); err != nil {
		return err
	}
	m.data = published
	return nil
}
func secret() (string, error) {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}
func roleActor(r room, as string, admin bool) (string, string, error) {
	if as == "" {
		return "owner", "owner", nil
	}
	for _, p := range r.Members {
		if p.Token == as && p.Active {
			if admin && p.Role != "coordinator" {
				return "", "", fmt.Errorf("эта операция доступна owner или coordinator")
			}
			return p.AgentID, p.Role, nil
		}
	}
	return "", "", fmt.Errorf("участник не подключён к этой комнате")
}
func appendEvent(d *snapshot, r *room, now time.Time, kind, agent, role, text string, notifySelf bool) Event {
	e := Event{ID: envelope.NewID(), Seq: uint64(len(r.Events) + 1), Room: r.ID, At: now, Kind: kind, Agent: agent, Role: role, Text: text}
	r.Events = append(r.Events, e)
	for key, p := range r.Members {
		if p.Active && p.Ref != "" && (notifySelf || p.AgentID != agent) {
			if (kind == "ready" || kind == "checkpoint") && p.Role != "coordinator" {
				continue
			}
			coalesced := false
			for i := range d.Outbox {
				item := &d.Outbox[i]
				if item.Room == r.ID && item.Peer == key && item.Kind == "event" && !item.Delivered {
					item.Event = e.ID
					coalesced = true
					break
				}
			}
			if coalesced {
				continue
			}
			d.Outbox = append(d.Outbox, delivery{ID: envelope.NewID(), Room: r.ID, Peer: key, Event: e.ID, Kind: "event", Due: now})
		}
	}
	return e
}
func capacity(r room) error {
	if len(r.Events) >= 100000 {
		return fmt.Errorf("комната достигла лимита 100000 событий; создайте следующую комнату")
	}
	return nil
}

// Create starts a task room. Agent-created rooms require a confirmed coordinator.
func (m *Manager) Create(task, title, as string) (Room, error) {
	if strings.TrimSpace(task) == "" || len(task) > 256 || len(title) > 512 {
		return Room{}, fmt.Errorf("укажите задачу (до 256 байт) и название до 512 байт")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var created room
	err := m.update(func(d *snapshot) error {
		if len(d.Rooms) >= 64 {
			return fmt.Errorf("достигнут лимит 64 комнат")
		}
		actor, role := "owner", "owner"
		controller := ""
		if as != "" {
			found := false
			for _, existing := range d.Rooms {
				for _, p := range existing.Members {
					if p.Active && p.Token == as && p.Role == "coordinator" {
						actor, role = p.AgentID, p.Role
						controller = p.SessionID
						found = true
					}
				}
			}
			if !found {
				return fmt.Errorf("создавать комнаты может owner или подтверждённый coordinator")
			}
		}
		created = room{ID: envelope.NewID(), Task: task, Title: title, ControllerSession: controller, Members: map[string]member{}}
		appendEvent(d, &created, m.cfg.Now(), "created", actor, role, "Задача: "+task+". "+title, false)
		d.Rooms[created.ID] = created
		return nil
	})
	return view(created, m.data.Outbox), err
}

// Invite persists a role-bound invitation before attempting transport delivery.
func (m *Manager) Invite(id, address, ref, role, agentID, as string) error {
	role = normalizeRole(role)
	if !roles.Role(strings.ToUpper(role)).Valid() || ref == "" {
		return fmt.Errorf("роль: coordinator, executor, reviewer или auditor")
	}
	if agentID == "" || len(agentID) > 256 {
		return fmt.Errorf("укажите стабильный --agent-id (до 256 байт)")
	}
	invite, err := secret()
	if err != nil {
		return err
	}
	callback, err := secret()
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.update(func(d *snapshot) error {
		r, ok := d.Rooms[id]
		if !ok || r.Closed {
			return fmt.Errorf("комната не найдена или закрыта")
		}
		actor, actorRole, err := roomAdmin(d, r, as)
		if err != nil {
			return err
		}
		if err = capacity(r); err != nil {
			return err
		}
		if len(r.Members) >= 16 {
			return fmt.Errorf("в комнате не более 16 агентов")
		}
		if _, exists := r.Members[ref]; exists {
			return fmt.Errorf("сессия уже приглашена в эту комнату")
		}
		if err := participantConflict(r, "", role, agentID); err != nil {
			return err
		}
		sessionID := envelope.NewID()
		if err := reserve(d, "peer:"+ref, sessionID); err != nil {
			return err
		}
		if err := reserve(d, "context:telepathy:"+ref, sessionID); err != nil {
			return err
		}
		r.Members[ref] = member{Member: Member{SessionID: sessionID, AgentID: agentID, Address: address, Ref: ref, Role: role, Regulation: "coord-v1", ConnectionID: "telepathy:" + ref, Assignments: []Assignment{{AgentID: agentID}}}, Token: callback, Invite: "room." + invite, Expires: m.cfg.Now().Add(5 * time.Minute)}
		appendEvent(d, &r, m.cfg.Now(), "invited", actor, actorRole, fmt.Sprintf("Приглашён %s как %s", address, role), false)
		d.Outbox = append(d.Outbox, delivery{ID: envelope.NewID(), Room: id, Peer: ref, Kind: "invite", Due: m.cfg.Now()})
		d.Rooms[id] = r
		return nil
	})
}

// Accept activates only the invitation's task, role and exact peer.
func (m *Manager) Accept(nonce string) error { _, err := m.AcceptAttachment(nonce); return err }

// AcceptAttachment activates an invitation and returns only its scoped capability.
func (m *Manager) AcceptAttachment(nonce string) (Attachment, error) {
	m.dispatch.Lock()
	defer m.dispatch.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	var result Attachment
	err := m.update(func(d *snapshot) error {
		if previous, ok := d.Operations["accept/"+nonce]; ok {
			if previous.Attachment == nil {
				return fmt.Errorf("invalid accept receipt")
			}
			_, _, current, exists := lookup(d, previous.Attachment.Auth.Session)
			if !exists || current.Generation != previous.Attachment.Auth.Generation {
				return ErrStaleGeneration
			}
			result = *previous.Attachment
			return nil
		}
		for id, r := range d.Rooms {
			for ref, p := range r.Members {
				if p.Invite == nonce && p.Invite != "" && !p.Active {
					if r.Closed || !m.cfg.Now().Before(p.Expires) {
						return fmt.Errorf("приглашение истекло или комната закрыта")
					}
					if err := capacity(r); err != nil {
						return err
					}
					p.Active = true
					if p.Generation == ^uint64(0) {
						return fmt.Errorf("generation exhausted")
					}
					p.Generation++
					p.ConnectedAt = m.cfg.Now()
					for i := range d.Outbox {
						if d.Outbox[i].Room == id && d.Outbox[i].Peer == ref && d.Outbox[i].Kind == "invite" {
							d.Outbox[i].Delivered = true
						}
					}
					p.Invite = ""
					r.Members[ref] = p
					appendEvent(d, &r, m.cfg.Now(), "joined", p.AgentID, p.Role, p.Address+" подключился", true)
					d.Rooms[id] = r
					result = Attachment{Auth: SessionAuth{Session: p.SessionID, Generation: p.Generation, Token: p.Token}, Context: contextFor(d, id, p, 0)}
					d.Operations["accept/"+nonce] = operation{Attachment: &result}
					return nil
				}
			}
		}
		return fmt.Errorf("приглашение неизвестно или уже принято")
	})
	return result, err
}

// Say atomically appends an owner/agent message and its fan-out outbox.
func (m *Manager) Say(id, text, as string) (Event, error) {
	if as != "" {
		return Event{}, fmt.Errorf("агент использует session say с credential-file и operation ID")
	}
	if strings.TrimSpace(text) == "" || len(text) > 8192 {
		return Event{}, fmt.Errorf("реплика должна содержать от 1 до 8192 байт")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var event Event
	err := m.update(func(d *snapshot) error {
		r, ok := d.Rooms[id]
		if !ok || r.Closed {
			return fmt.Errorf("комната не найдена или закрыта")
		}
		actor, role, err := roleActor(r, as, false)
		if err != nil {
			return err
		}
		if err = capacity(r); err != nil {
			return err
		}
		event = appendEvent(d, &r, m.cfg.Now(), "message", actor, role, text, false)
		d.Rooms[id] = r
		return nil
	})
	return event, err
}

// CloseRoom ends collaboration without claiming task review or merge success.
func (m *Manager) CloseRoom(id, as string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.update(func(d *snapshot) error {
		r, ok := d.Rooms[id]
		if !ok {
			return fmt.Errorf("комната не найдена")
		}
		if r.Closed {
			return nil
		}
		actor, role, err := roomAdmin(d, r, as)
		if err != nil {
			return err
		}
		if err = capacity(r); err != nil {
			return err
		}
		appendEvent(d, &r, m.cfg.Now(), "closed", actor, role, "Комната закрыта", false)
		r.Closed = true
		d.Rooms[id] = r
		return nil
	})
}
func view(r room, outbox []delivery) Room {
	v := Room{ID: r.ID, Task: r.Task, Title: r.Title, Closed: r.Closed, Members: []Member{}}
	for _, p := range r.Members {
		v.Members = append(v.Members, cloneMember(p.Member))
	}
	if len(r.Events) > 0 {
		v.LastActivity = r.Events[len(r.Events)-1].At
	}
	for _, d := range outbox {
		if d.Room == r.ID && !d.Delivered {
			v.Pending++
			if d.LastError != "" {
				v.DeliveryErrors++
			}
		}
	}
	return v
}

// List exposes no capability tokens.
func (m *Manager) List() []Room {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := []Room{}
	for _, r := range m.data.Rooms {
		result = append(result, view(r, m.data.Outbox))
	}
	return result
}

// Events returns the immutable stream after a sequence cursor.
func (m *Manager) Events(id string, after uint64) ([]Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.data.Rooms[id]
	if !ok {
		return nil, fmt.Errorf("комната не найдена")
	}
	result := []Event{}
	for _, e := range r.Events {
		if e.Seq > after {
			result = append(result, e)
		}
	}
	return result, nil
}
func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
func (m *Manager) command(action, value string) string {
	return quote(m.cfg.Executable) + " --state " + quote(m.cfg.State) + " " + action + " " + quote(value)
}
func (m *Manager) body(r room, p member, d delivery) string {
	if d.Kind == "invite" {
		return fmt.Sprintf("[Coord: role session invitation]\nSession: %s\nRole: %s\nConfirm once:\n%s\nThen use the credential-file returned by accept to read session context and confirm readiness. Use your existing separate context; do not spawn another model. No plain PONG.", p.SessionID, p.Role, m.command("accept", p.Invite))
	}
	credential := fmt.Sprintf("%s/clients/%s-%d.json", m.cfg.State, p.SessionID, p.Generation)
	return fmt.Sprintf("[Coord: new room event]\nSession: %s\nRoom: %s\nGeneration: %d\nEvent: %s\nRead only this session:\n%s --credential-file %s\nReply via session say with the same credential-file and a stable --operation ID. Read the common bootstrap with coord prompt. Transport delivery does not acknowledge processing.", p.SessionID, r.ID, p.Generation, d.Event, m.command("session context", p.SessionID), quote(credential))
}

// Deliver attempts one due notification. Failure preserves the outbox with a
// backoff exceeding Telepathy's two-minute duplicate suppression.
func (m *Manager) Deliver(ctx context.Context) error {
	m.dispatch.Lock()
	defer m.dispatch.Unlock()
	m.mu.Lock()
	if m.closed || m.failure != nil {
		m.mu.Unlock()
		return fmt.Errorf("room delivery stopped")
	}
	sweepErr := m.updateWithRevision(func(d *snapshot) error {
		for i := range d.Outbox {
			item := &d.Outbox[i]
			r := d.Rooms[item.Room]
			p := r.Members[item.Peer]
			if item.Kind == "invite" && (p.Active || r.Closed || !m.cfg.Now().Before(p.Expires)) {
				item.Delivered = true
			}
		}
		return nil
	}, false)
	if sweepErr != nil {
		m.mu.Unlock()
		return sweepErr
	}
	var chosen delivery
	var body string
	var recipient string
	for _, d := range m.data.Outbox {
		if d.Delivered || d.Due.After(m.cfg.Now()) {
			continue
		}
		r := m.data.Rooms[d.Room]
		p := r.Members[d.Peer]
		if p.Ref == "" {
			continue
		}
		if d.Kind == "invite" && (p.Active || r.Closed || !m.cfg.Now().Before(p.Expires)) {
			continue
		}
		chosen = d
		recipient = p.Ref
		body = m.body(r, p, d)
		break
	}
	m.mu.Unlock()
	if chosen.ID == "" {
		return nil
	}
	call, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, sendErr := m.tools.Call(call, "send_message", map[string]any{"to": recipient, "message": body})
	m.mu.Lock()
	defer m.mu.Unlock()
	persistErr := m.updateWithRevision(func(d *snapshot) error {
		for i := range d.Outbox {
			item := &d.Outbox[i]
			if item.ID == chosen.ID {
				item.Tries++
				if sendErr == nil {
					item.Delivered = item.Event == chosen.Event
					if !item.Delivered {
						item.Due = m.cfg.Now()
					}
					item.LastError = ""
				} else {
					item.LastError = sendErr.Error()
					item.Due = m.cfg.Now().Add(130 * time.Second)
				}
				break
			}
		}
		return nil
	}, false)
	return errors.Join(sendErr, persistErr)
}

// Run dispatches durable room notifications until the local service stops.
func (m *Manager) Run(ctx context.Context) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			_ = m.Deliver(ctx)
			_ = m.Export(ctx, m.HistoryStatus().IncludeContent)
		}
	}
}
