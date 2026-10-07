//go:build unix

package rooms

import (
	"coord/envelope"
	"coord/roles"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

// ErrStaleGeneration rejects a disconnected writer even if it retries an old operation.
var ErrStaleGeneration = errors.New("stale session generation")

// ErrOperationConflict prevents reusing an operation ID for different input.
var ErrOperationConflict = errors.New("operation ID reused with different input")

// Action records unfinished external work; unknown actions require reconciliation.
type Action struct {
	ID          string `json:"id"`
	State       string `json:"state"`
	Description string `json:"description,omitempty"`
}

// Checkpoint is application progress, not a model's private reasoning.
type Checkpoint struct {
	Progress        string   `json:"progress"`
	PendingActions  []Action `json:"pending_actions"`
	ArtifactRefs    []string `json:"artifact_refs"`
	AcknowledgedSeq uint64   `json:"acknowledged_seq"`
}

// Assignment preserves participant replacement for audit independence.
type Assignment struct {
	AgentID    string `json:"agent_id"`
	Generation uint64 `json:"generation"`
}

// SessionAuth binds every agent request to one logical session and generation.
type SessionAuth struct {
	Session    string `json:"session"`
	Generation uint64 `json:"generation"`
	Token      string `json:"token"`
}

// SessionContext contains exactly one room's authorized state.
type SessionContext struct {
	Member Member  `json:"member"`
	Room   string  `json:"room"`
	Task   string  `json:"task"`
	Title  string  `json:"title"`
	Closed bool    `json:"closed"`
	Events []Event `json:"events"`
}

// ResumeRequest explicitly identifies the replacement connection.
// Empty Token is the trusted local owner path, not anonymous network access.
type ResumeRequest struct {
	Session    string `json:"session"`
	Connection string `json:"connection"`
	Expected   uint64 `json:"expected"`
	Operation  string `json:"operation"`
	Token      string `json:"token,omitempty"`
	Ref        string `json:"ref,omitempty"`
	Address    string `json:"address,omitempty"`
	AgentID    string `json:"agent_id,omitempty"`
	Actor      string `json:"actor,omitempty"`
}

// Attachment contains a capability and must never be written to Git history.
type Attachment struct {
	Auth    SessionAuth    `json:"auth"`
	Context SessionContext `json:"context"`
}
type operation struct {
	Hash       string
	Event      *Event
	Attachment *Attachment
	Ticket     *RegistrationTicket
	Invitation *Invitation
	Start      *StartResult
}

func fingerprint(input any) string {
	raw, _ := json.Marshal(input)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func validOperation(id string) error {
	if strings.TrimSpace(id) == "" || len(id) > 256 {
		return fmt.Errorf("operation ID required (1..256 bytes)")
	}
	return nil
}
func lookup(d *snapshot, id string) (string, string, member, bool) {
	for roomID, r := range d.Rooms {
		for key, p := range r.Members {
			if p.SessionID == id {
				return roomID, key, p, true
			}
		}
	}
	return "", "", member{}, false
}
func authorized(d *snapshot, auth SessionAuth) (string, string, member, error) {
	roomID, key, p, ok := lookup(d, auth.Session)
	if !ok {
		return "", "", p, fmt.Errorf("session not found")
	}
	if p.Generation != auth.Generation {
		return "", "", p, ErrStaleGeneration
	}
	if !p.Active || p.Token == "" || p.Token != auth.Token {
		return "", "", p, fmt.Errorf("invalid session capability")
	}
	return roomID, key, p, nil
}
func reserve(d *snapshot, binding, session string) error {
	if binding == "" {
		return nil
	}
	if old := d.Bindings[binding]; old != "" && old != session {
		return fmt.Errorf("connection already belongs to another logical session; use a separate context")
	}
	d.Bindings[binding] = session
	return nil
}

// blockedSession refuses a role assignment while an external blocker stands.
// Availability is never inferred by Coord, so the refusal must name the reason,
// its source and the single action that lifts it. An empty id means the caller
// creates a fresh session and no recorded blocker can apply to it.
func blockedSession(d *snapshot, id string) error {
	if id == "" {
		return nil
	}
	_, _, p, ok := lookup(d, id)
	if !ok || p.Blocker == nil {
		return nil
	}
	retry := "не задан"
	if !p.Blocker.RetryAt.IsZero() {
		retry = p.Blocker.RetryAt.Format(time.RFC3339)
	}
	return fmt.Errorf("сессия %s заблокирована: %s (source %s, observed_at %s, retry_at %s); новые назначения остановлены, checkpoint сохранён. Следующее действие: coord session unblock %s после подтверждения доступности", id, p.Blocker.Reason, p.Blocker.Source, p.Blocker.ObservedAt.Format(time.RFC3339), retry, id)
}

// Assign creates a role session independently of any transport or TUI.
func (m *Manager) Assign(id, agentID, role, instructions, as string) (Member, error) {
	return m.assign(id, agentID, role, instructions, as, "")
}
func (m *Manager) assign(id, agentID, role, instructions, as, registrationID string) (Member, error) {
	role = normalizeRole(role)
	if (agentID == "" && registrationID == "") || len(agentID) > 256 || len(instructions) > 16384 || !roles.Role(strings.ToUpper(role)).Valid() {
		return Member{}, fmt.Errorf("stable agent ID, valid role and bounded instructions required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var result Member
	err := m.update(func(d *snapshot) error {
		// The blocker gate runs before every other check, so a blocked session is
		// refused on any assignment path and the refusal explains the next action.
		if err := blockedSession(d, registrationID); err != nil {
			return err
		}
		var waiting registration
		if registrationID != "" {
			var exists bool
			waiting, exists = d.Registrations[registrationID]
			if !exists {
				return fmt.Errorf("registration not found")
			}
			if waiting.State != "WAITING_ASSIGNMENT" {
				existingRoom, exists := d.Rooms[id]
				if !exists {
					return fmt.Errorf("room not found")
				}
				if _, _, err := roomAdmin(d, existingRoom, as); err != nil {
					return err
				}
				if waiting.Room == id && waiting.Role == role {
					_, _, p, ok := lookup(d, registrationID)
					if ok {
						if p.Instructions != instructions {
							return ErrOperationConflict
						}
						result = p.Member
						return nil
					}
				}
				return fmt.Errorf("registration already assigned")
			}
			agentID = waiting.AgentID
		}
		r, ok := d.Rooms[id]
		if !ok || r.Closed {
			return fmt.Errorf("room not found or closed")
		}
		actor, actorRole, err := roomAdmin(d, r, as)
		if err != nil {
			return err
		}
		if err := participantConflict(r, "", role, agentID); err != nil {
			return err
		}
		if len(r.Members) >= 16 {
			return fmt.Errorf("room member limit reached")
		}
		result = Member{SessionID: envelope.NewID(), AgentID: agentID, Role: role, Regulation: "coord-v1", Instructions: instructions, Checkpoint: Checkpoint{PendingActions: []Action{}, ArtifactRefs: []string{}}, Assignments: []Assignment{{AgentID: agentID}}}
		entry := member{Member: result}
		if registrationID != "" {
			result.SessionID = registrationID
			result.ConnectionID = waiting.Connection
			entry.Member = result
			entry.Token = waiting.Token
			waiting.State = "ASSIGNED"
			waiting.Room = id
			waiting.Role = role
			d.Registrations[registrationID] = waiting
		}
		r.Members[result.SessionID] = entry
		appendEvent(d, &r, m.cfg.Now(), "assigned", actor, actorRole, "Assigned "+agentID+" as "+role+"; session "+result.SessionID, false)
		d.Rooms[id] = r
		return nil
	})
	return cloneMember(result), err
}
func cloneMember(p Member) Member {
	raw, _ := json.Marshal(p)
	var result Member
	_ = json.Unmarshal(raw, &result)
	return result
}
func contextFor(d *snapshot, roomID string, p member, after uint64) SessionContext {
	r := d.Rooms[roomID]
	p.IndependentAudit = independentAudit(d, roomID, p.Member)
	p.State = "ASSIGNED"
	if p.Active {
		p.State = "ATTACHED"
	}
	if r.Closed {
		p.State = "CLOSED"
	}
	c := SessionContext{Member: cloneMember(p.Member), Room: roomID, Task: r.Task, Title: r.Title, Closed: r.Closed, Events: []Event{}}
	for _, e := range r.Events {
		if e.Seq > after {
			c.Events = append(c.Events, e)
		}
	}
	return c
}

// Availability separates transport attachment from provider reachability.
// Coord never claims a model is available without an external signal;
// ATTACHED alone means capability issued, not "the agent works".
type Availability struct {
	SessionID    string    `json:"session_id"`
	RoomID       string    `json:"room_id"`
	AgentID      string    `json:"agent_id"`
	Role         string    `json:"role"`
	Generation   uint64    `json:"generation"`
	State        string    `json:"state"`        // active|stopped|waiting_assignment|stale_generation|closed
	Attachment   string    `json:"attachment"`   // attached|detached — только факт capability
	Reachability string    `json:"reachability"` // ok|failed|unknown — факты доставки notification
	Readiness    string    `json:"readiness"`    // ready|not_ready|re_regulation_required
	Processing   string    `json:"processing"`   // processed N of M
	LastActivity time.Time `json:"last_activity,omitempty"`
	Provider     string    `json:"provider_availability"` // available|blocked|unknown
	Blocker      *Blocker  `json:"blocker,omitempty"`
	Expectation  string    `json:"expectation"` // кого/чего ждём
	ObservedAt   time.Time `json:"observed_at"`
}

// latestCredentialGeneration reports the newest generation ever recorded for a
// stable agent identity across rooms. Profiles compare against it to surface a
// stale-generation state explicitly; it is display metadata only and grants no
// access — every operation still requires the exact credential file.
func (d *snapshot) latestCredentialGeneration(agentID string) uint64 {
	var latest uint64
	for _, r := range d.Rooms {
		for _, p := range r.Members {
			if p.AgentID == agentID && p.Generation > latest {
				latest = p.Generation
			}
		}
	}
	return latest
}

// Availability returns honest per-session status cards with separated fields
// (ALIGNED_UX.md §1.1). Fields are never derived from one another.
func (m *Manager) Availability() []Availability {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := []Availability{}
	now := m.cfg.Now()
	for _, r := range m.data.Rooms {
		lastSeq := uint64(len(r.Events))
		for _, p := range r.Members {
			reachability := "unknown"
			inviteQueued := false
			for _, d := range m.data.Outbox {
				if d.Room != r.ID || d.Peer != p.SessionID {
					continue
				}
				if d.Kind == "invite" && !d.Delivered {
					inviteQueued = true
				}
				if d.LastError != "" {
					reachability = "failed"
				} else if d.Delivered && reachability != "failed" {
					reachability = "ok"
				}
			}
			// Generation facts, never inferred from attachment: an older generation
			// of the same agent identity is reported as its own state.
			stale := m.data.latestCredentialGeneration(p.AgentID) > p.Generation
			state := "stopped"
			switch {
			case r.Closed:
				state = "closed"
			case stale:
				state = "stale_generation"
			case p.Active:
				state = "active"
			}
			readiness := "not_ready"
			if p.Ready {
				if p.Regulation != "coord-v1" {
					readiness = "re_regulation_required"
				} else {
					readiness = "ready"
				}
			}
			provider := "unknown"
			if p.Blocker != nil {
				provider = "blocked"
			}
			expectation := ""
			switch {
			case !p.Active && inviteQueued:
				expectation = "ждём accept приглашения агентом " + p.AgentID
			case !p.Active:
				expectation = "ждём reconnect: coord session resume " + p.SessionID
			case readiness != "ready":
				expectation = "ждём ready --regulation coord-v1 от " + p.AgentID
			case p.Checkpoint.AcknowledgedSeq < lastSeq:
				expectation = fmt.Sprintf("ждём обработку событий %d..%d от %s", p.Checkpoint.AcknowledgedSeq+1, lastSeq, p.AgentID)
			default:
				expectation = "ожиданий нет"
			}
			if p.Blocker != nil {
				expectation = "заблокировано: " + p.Blocker.Reason
				if !p.Blocker.RetryAt.IsZero() {
					expectation += " (retry_at " + p.Blocker.RetryAt.Format(time.RFC3339) + ")"
				}
			}
			attachment := "detached"
			if p.Active {
				attachment = "attached"
			}
			result = append(result, Availability{
				SessionID:    p.SessionID,
				RoomID:       r.ID,
				AgentID:      p.AgentID,
				Role:         p.Role,
				Generation:   p.Generation,
				State:        state,
				Attachment:   attachment,
				Reachability: reachability,
				Readiness:    readiness,
				Processing:   fmt.Sprintf("processed %d of %d", p.Checkpoint.AcknowledgedSeq, lastSeq),
				LastActivity: p.ConnectedAt,
				Provider:     provider,
				Blocker:      p.Blocker,
				Expectation:  expectation,
				ObservedAt:   now,
			})
		}
	}
	for _, reg := range m.data.Registrations {
		if reg.State != "WAITING_ASSIGNMENT" {
			continue
		}
		result = append(result, Availability{
			SessionID:    reg.SessionID,
			AgentID:      reg.AgentID,
			State:        "waiting_assignment",
			Attachment:   "detached",
			Reachability: "unknown",
			Readiness:    "not_ready",
			Processing:   "processed 0 of 0",
			Provider:     "unknown",
			Expectation:  "ждёт назначения роли: coord session assign <room> --registration " + reg.SessionID,
			ObservedAt:   now,
		})
	}
	return result
}

// Resume atomically rotates the capability, fences previous requests and returns replay.
func (m *Manager) Resume(req ResumeRequest) (Attachment, error) {
	if err := validOperation(req.Operation); err != nil {
		return Attachment{}, err
	}
	if req.Connection == "" || len(req.Connection) > 256 || len(req.Ref) > 256 || len(req.Address) > 512 || len(req.AgentID) > 256 {
		return Attachment{}, fmt.Errorf("bounded connection identity required")
	}
	token, err := secret()
	if err != nil {
		return Attachment{}, err
	}
	m.dispatch.Lock()
	defer m.dispatch.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	var result Attachment
	err = m.update(func(d *snapshot) error {
		roomID, key, p, ok := lookup(d, req.Session)
		if !ok {
			return fmt.Errorf("session not found")
		}
		r := d.Rooms[roomID]
		if r.Closed {
			return fmt.Errorf("room closed")
		}
		opKey := req.Session + "/resume/" + req.Operation
		hash := fingerprint(req)
		if previous, ok := d.Operations[opKey]; ok {
			if previous.Hash != hash {
				return ErrOperationConflict
			}
			if previous.Attachment == nil || previous.Attachment.Auth.Generation != p.Generation {
				return ErrStaleGeneration
			}
			result = *previous.Attachment
			return nil
		}
		if p.Generation != req.Expected {
			return fmt.Errorf("%w: profile gen%d, current gen%d — следующее действие: coord session resume %s --generation %d или profile refresh", ErrStaleGeneration, req.Expected, p.Generation, p.SessionID, p.Generation)
		}
		if req.Token != "" {
			if (p.Generation > 0 && !p.Active) || p.Token != req.Token {
				return fmt.Errorf("invalid resume capability")
			}
			if req.AgentID != "" && req.AgentID != p.AgentID {
				return fmt.Errorf("participant replacement requires owner or coordinator")
			}
		} else {
			if _, _, err := roomAdmin(d, r, req.Actor); err != nil {
				return err
			}
		}
		if p.Generation == math.MaxUint64 {
			return fmt.Errorf("generation exhausted")
		}
		if err := reserve(d, "context:"+req.Connection, p.SessionID); err != nil {
			return err
		}
		if req.Ref != "" {
			if err := reserve(d, "peer:"+req.Ref, p.SessionID); err != nil {
				return err
			}
		}
		p.Generation++
		p.Ready = false
		p.Readiness = Readiness{}
		p.ConnectionID = req.Connection
		p.Active = true
		p.Token = token
		p.Invite = ""
		p.Ref = req.Ref
		p.Address = req.Address
		p.ConnectedAt = m.cfg.Now()
		if req.AgentID != "" && req.AgentID != p.AgentID {
			// Retain every executor identity rather than erasing independence evidence.
			if p.Role == "auditor" {
				for _, other := range r.Members {
					if other.Role == "executor" {
						for _, a := range other.Assignments {
							if a.AgentID == req.AgentID {
								return fmt.Errorf("replacement auditor previously executed this room")
							}
						}
					}
				}
			}
			if err := participantConflict(r, p.SessionID, p.Role, req.AgentID); err != nil {
				return err
			}
			p.AgentID = req.AgentID
			p.Assignments = append(p.Assignments, Assignment{AgentID: p.AgentID, Generation: p.Generation})
		}
		for i := range p.Checkpoint.PendingActions {
			if p.Checkpoint.PendingActions[i].State == "executing" {
				p.Checkpoint.PendingActions[i].State = "unknown"
			}
		}
		r.Members[key] = p
		for i := range d.Outbox {
			if d.Outbox[i].Room == roomID && d.Outbox[i].Peer == key {
				d.Outbox[i].Delivered = true
			}
		}
		event := appendEvent(d, &r, m.cfg.Now(), "resumed", p.AgentID, p.Role, fmt.Sprintf("Session %s connected; generation %d", p.SessionID, p.Generation), true)
		event.Session = p.SessionID
		event.Generation = p.Generation
		r.Events[len(r.Events)-1] = event
		d.Rooms[roomID] = r
		result = Attachment{Auth: SessionAuth{Session: p.SessionID, Generation: p.Generation, Token: p.Token}, Context: contextFor(d, roomID, p, p.Checkpoint.AcknowledgedSeq)}
		d.Operations[opKey] = operation{Hash: hash, Attachment: &result}
		return nil
	})
	return result, err
}

// Context returns one session's room. Reading never acknowledges processing.
func (m *Manager) Context(auth SessionAuth, after uint64) (SessionContext, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	roomID, _, p, err := authorized(&m.data, auth)
	if err != nil {
		return SessionContext{}, err
	}
	if after > uint64(len(m.data.Rooms[roomID].Events)) {
		return SessionContext{}, fmt.Errorf("cursor beyond journal")
	}
	return contextFor(&m.data, roomID, p, after), nil
}
func (m *Manager) sessionWrite(auth SessionAuth, operationID, kind string, input any, fn func(*snapshot, string, string, member) (*Event, error)) (*Event, error) {
	if err := validOperation(operationID); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var result *Event
	err := m.update(func(d *snapshot) error {
		roomID, key, p, err := authorized(d, auth)
		if err != nil {
			return err
		}
		if d.Rooms[roomID].Closed {
			return fmt.Errorf("room closed")
		}
		opKey := auth.Session + "/" + operationID
		hash := fingerprint(struct {
			Kind  string
			Input any
		}{kind, input})
		if previous, ok := d.Operations[opKey]; ok {
			if previous.Hash != hash {
				return ErrOperationConflict
			}
			result = previous.Event
			return nil
		}
		result, err = fn(d, roomID, key, p)
		if err != nil {
			return err
		}
		d.Operations[opKey] = operation{Hash: hash, Event: result}
		return nil
	})
	return result, err
}

// SessionSay deduplicates an agent's durable message before publishing its outbox.
func (m *Manager) SessionSay(auth SessionAuth, operationID, text string) (Event, error) {
	if strings.TrimSpace(text) == "" || len(text) > 8192 {
		return Event{}, fmt.Errorf("message must contain 1..8192 bytes")
	}
	event, err := m.sessionWrite(auth, operationID, "say", text, func(d *snapshot, roomID, key string, p member) (*Event, error) {
		r := d.Rooms[roomID]
		if err := capacity(r); err != nil {
			return nil, err
		}
		e := appendEvent(d, &r, m.cfg.Now(), "message", p.AgentID, p.Role, text, false)
		e.Session = p.SessionID
		e.Generation = p.Generation
		r.Events[len(r.Events)-1] = e
		d.Rooms[roomID] = r
		return &e, nil
	})
	if err != nil {
		return Event{}, err
	}
	return *event, nil
}
func validateCheckpoint(cp Checkpoint, old uint64, last uint64) error {
	if cp.AcknowledgedSeq < old || cp.AcknowledgedSeq > last {
		return fmt.Errorf("checkpoint cursor must be monotonic and within journal")
	}
	if len(cp.Progress) > 16384 || len(cp.PendingActions) > 256 || len(cp.ArtifactRefs) > 256 {
		return fmt.Errorf("checkpoint exceeds limits")
	}
	seen := map[string]bool{}
	for _, a := range cp.PendingActions {
		if a.ID == "" || len(a.ID) > 256 || len(a.Description) > 4096 || seen[a.ID] {
			return fmt.Errorf("invalid or repeated action ID")
		}
		seen[a.ID] = true
		switch a.State {
		case "planned", "executing", "unknown", "done":
		default:
			return fmt.Errorf("invalid action state")
		}
	}
	for _, ref := range cp.ArtifactRefs {
		if ref == "" || len(ref) > 4096 {
			return fmt.Errorf("invalid artifact reference")
		}
	}
	return nil
}

// SaveCheckpoint persists progress and unresolved actions without executing them.
func (m *Manager) SaveCheckpoint(auth SessionAuth, operationID string, cp Checkpoint) error {
	_, err := m.sessionWrite(auth, operationID, "checkpoint", cp, func(d *snapshot, roomID, key string, p member) (*Event, error) {
		r := d.Rooms[roomID]
		if err := validateCheckpoint(cp, p.Checkpoint.AcknowledgedSeq, uint64(len(r.Events))); err != nil {
			return nil, err
		}
		p.Checkpoint = cp
		r.Members[key] = p
		e := appendEvent(d, &r, m.cfg.Now(), "checkpoint", p.AgentID, p.Role, "Checkpoint saved for "+p.SessionID, false)
		e.Session = p.SessionID
		e.Generation = p.Generation
		r.Events[len(r.Events)-1] = e
		d.Rooms[roomID] = r
		return &e, nil
	})
	return err
}

// Ack advances processing position explicitly; transport delivery cannot call it.
func (m *Manager) Ack(auth SessionAuth, operationID string, seq uint64) error {
	_, err := m.sessionWrite(auth, operationID, "ack", seq, func(d *snapshot, roomID, key string, p member) (*Event, error) {
		r := d.Rooms[roomID]
		if seq < p.Checkpoint.AcknowledgedSeq || seq > uint64(len(r.Events)) {
			return nil, fmt.Errorf("acknowledgement cursor must be monotonic and within journal")
		}
		p.Checkpoint.AcknowledgedSeq = seq
		r.Members[key] = p
		d.Rooms[roomID] = r
		return nil, nil
	})
	return err
}

// Sessions returns logical participants, including disconnected sessions.
func (m *Manager) Sessions() []Member {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := []Member{}
	for _, r := range m.data.Registrations {
		if r.State == "WAITING_ASSIGNMENT" {
			result = append(result, Member{SessionID: r.SessionID, AgentID: r.AgentID, State: r.State})
		}
	}
	for _, r := range m.data.Rooms {
		for _, p := range r.Members {
			v := cloneMember(p.Member)
			v.RoomID = r.ID
			v.Task = r.Task
			v.Active = v.Active && !r.Closed
			v.State = "ASSIGNED"
			if v.Active {
				v.State = "ATTACHED"
			}
			if r.Closed {
				v.State = "CLOSED"
			}
			v.IndependentAudit = independentAudit(&m.data, r.ID, v)
			result = append(result, v)
		}
	}
	return result
}

func normalizeRole(role string) string {
	role = strings.ToLower(role)
	if role == "orchestrator" {
		return "coordinator"
	}
	return role
}

func participantConflict(r room, sessionID, role, agentID string) error {
	for _, p := range r.Members {
		if p.SessionID == sessionID {
			continue
		}
		if p.AgentID == agentID {
			return fmt.Errorf("agent already has a role in this room; use a separate session and room")
		}
		if (role == "auditor" && p.Role == "executor") || (role == "executor" && p.Role == "auditor") {
			for _, a := range p.Assignments {
				if a.AgentID == agentID {
					return fmt.Errorf("auditor independence conflicts with participant history")
				}
			}
		}
	}
	return nil
}
func independentAudit(d *snapshot, roomID string, p Member) bool {
	if p.Role != "auditor" {
		return false
	}
	for _, r := range d.Rooms {
		if r.Task != d.Rooms[roomID].Task {
			continue
		}
		for _, other := range r.Members {
			if other.Role != "executor" {
				continue
			}
			for _, executed := range other.Assignments {
				for _, audited := range p.Assignments {
					if executed.AgentID == audited.AgentID {
						return false
					}
				}
			}
		}
	}
	return true
}
