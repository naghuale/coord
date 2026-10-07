//go:build unix

package rooms

import (
	"coord/envelope"
	"fmt"
	"strings"
	"time"
)

// StartResult identifies the owner-created coordinator session without exposing its invitation.
type StartResult struct {
	RoomID    string `json:"room_id"`
	SessionID string `json:"session_id"`
}

// Start atomically records the owner's initial coordinator appointment and invitation.
func (m *Manager) Start(task, title, agent, address, ref, instructions, operationID string) (StartResult, error) {
	if err := validOperation(operationID); err != nil {
		return StartResult{}, err
	}
	if strings.TrimSpace(task) == "" || len(task) > 256 || len(title) > 512 || agent == "" || len(agent) > 256 || ref == "" || len(instructions) > 16384 {
		return StartResult{}, fmt.Errorf("bounded project, coordinator identity and peer required")
	}
	nonce, err := secret()
	if err != nil {
		return StartResult{}, err
	}
	token, err := secret()
	if err != nil {
		return StartResult{}, err
	}
	m.dispatch.Lock()
	defer m.dispatch.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	var result StartResult
	err = m.update(func(d *snapshot) error {
		key := "owner-start/" + operationID
		hash := fingerprint([]string{task, title, agent, address, ref, instructions})
		if old, ok := d.Operations[key]; ok {
			if old.Hash != hash {
				return ErrOperationConflict
			}
			if old.Start == nil {
				return fmt.Errorf("invalid start receipt")
			}
			result = *old.Start
			return nil
		}
		if len(d.Rooms) >= 64 {
			return fmt.Errorf("room limit reached")
		}
		result = StartResult{RoomID: envelope.NewID(), SessionID: envelope.NewID()}
		if err := reserve(d, "peer:"+ref, result.SessionID); err != nil {
			return err
		}
		if err := reserve(d, "context:telepathy:"+ref, result.SessionID); err != nil {
			return err
		}
		r := room{ID: result.RoomID, Task: task, Title: title, Members: map[string]member{}}
		p := member{Member: Member{SessionID: result.SessionID, AgentID: agent, Ref: ref, Address: address, Role: "coordinator", Regulation: "coord-v1", Instructions: instructions, ConnectionID: "telepathy:" + ref, Assignments: []Assignment{{AgentID: agent}}}, Token: token, Invite: "room." + nonce, Expires: m.cfg.Now().Add(10 * time.Minute)}
		r.Members[result.SessionID] = p
		appendEvent(d, &r, m.cfg.Now(), "created", "owner", "owner", "Project: "+task, false)
		appendEvent(d, &r, m.cfg.Now(), "assigned", "owner", "owner", "Owner appointed "+agent+" as coordinator; session "+p.SessionID, false)
		appendEvent(d, &r, m.cfg.Now(), "invited", "owner", "owner", "Coordinator invitation queued", false)
		d.Outbox = append(d.Outbox, delivery{ID: envelope.NewID(), Room: r.ID, Peer: p.SessionID, Kind: "invite", Due: m.cfg.Now()})
		d.Rooms[r.ID] = r
		d.Operations[key] = operation{Hash: hash, Start: &result}
		return nil
	})
	return result, err
}
func roomAdmin(d *snapshot, r room, as string) (string, string, error) {
	agent, role, err := roleActor(r, as, true)
	if err == nil {
		return agent, role, nil
	}
	if r.ControllerSession != "" {
		_, _, p, ok := lookup(d, r.ControllerSession)
		if ok && p.Active && p.Role == "coordinator" && p.Token == as {
			return p.AgentID, p.Role, nil
		}
	}
	return "", "", err
}
