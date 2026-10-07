//go:build unix

package rooms

import (
	"coord/envelope"
	"fmt"
	"time"
)

// Invitation is a one-use connection instruction; it must not enter public Git history.
type Invitation struct {
	Session string    `json:"session"`
	Command string    `json:"command"`
	Expires time.Time `json:"expires"`
}

// InviteSession grants one preassigned role session without creating another identity.
func (m *Manager) InviteSession(id, address, ref, operationID, as string) (Invitation, error) {
	if err := validOperation(operationID); err != nil {
		return Invitation{}, err
	}
	nonce, err := secret()
	if err != nil {
		return Invitation{}, err
	}
	token, err := secret()
	if err != nil {
		return Invitation{}, err
	}
	m.dispatch.Lock()
	defer m.dispatch.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	var result Invitation
	err = m.update(func(d *snapshot) error {
		roomID, key, p, ok := lookup(d, id)
		if !ok {
			return fmt.Errorf("session not found")
		}
		r := d.Rooms[roomID]
		if r.Closed {
			return fmt.Errorf("room closed")
		}
		actor, role, err := roomAdmin(d, r, as)
		if err != nil {
			return err
		}
		opKey := id + "/invite/" + operationID
		hash := fingerprint([]string{address, ref})
		if previous, ok := d.Operations[opKey]; ok {
			if previous.Hash != hash {
				return ErrOperationConflict
			}
			if previous.Invitation == nil {
				return fmt.Errorf("invalid invite operation")
			}
			result = *previous.Invitation
			return nil
		}
		if p.Active {
			return fmt.Errorf("session already connected; resume with generation CAS")
		}
		if ref != "" {
			if err := reserve(d, "peer:"+ref, id); err != nil {
				return err
			}
			if err := reserve(d, "context:telepathy:"+ref, id); err != nil {
				return err
			}
		}
		p.Invite = "room." + nonce
		p.Token = token
		p.Expires = m.cfg.Now().Add(10 * time.Minute)
		p.Ref = ref
		p.Address = address
		p.ConnectionID = "telepathy:" + ref
		if ref == "" {
			p.ConnectionID = "invite:" + id
		}
		r.Members[key] = p
		appendEvent(d, &r, m.cfg.Now(), "invited", actor, role, "Invitation for session "+id, false)
		if ref != "" {
			d.Outbox = append(d.Outbox, delivery{ID: envelope.NewID(), Room: roomID, Peer: key, Kind: "invite", Due: m.cfg.Now()})
		}
		d.Rooms[roomID] = r
		result = Invitation{Session: id, Command: m.command("accept", p.Invite), Expires: p.Expires}
		d.Operations[opKey] = operation{Hash: hash, Invitation: &result}
		return nil
	})
	return result, err
}

// Readiness records the agent's explicit acceptance of its role and regulations.
type Readiness struct {
	Regulation   string   `json:"regulation"`
	Capabilities []string `json:"capabilities"`
	Blockers     []string `json:"blockers"`
}

// Ready confirms the current assignment after context restoration.
func (m *Manager) Ready(auth SessionAuth, operationID string, ready Readiness) error {
	if len(ready.Capabilities) > 64 || len(ready.Blockers) > 64 {
		return fmt.Errorf("readiness exceeds limits")
	}
	for _, s := range append(append([]string{}, ready.Capabilities...), ready.Blockers...) {
		if len(s) > 1024 {
			return fmt.Errorf("readiness item exceeds limits")
		}
	}
	_, err := m.sessionWrite(auth, operationID, "ready", ready, func(d *snapshot, roomID, key string, p member) (*Event, error) {
		if ready.Regulation != p.Regulation {
			return nil, fmt.Errorf("regulation version mismatch")
		}
		r := d.Rooms[roomID]
		p.Readiness = ready
		p.Ready = true
		r.Members[key] = p
		e := appendEvent(d, &r, m.cfg.Now(), "ready", p.AgentID, p.Role, "Role and regulation accepted by session "+p.SessionID, false)
		e.Session = p.SessionID
		e.Generation = p.Generation
		r.Events[len(r.Events)-1] = e
		d.Rooms[roomID] = r
		return &e, nil
	})
	return err
}
