//go:build unix

package rooms

import (
	"coord/envelope"
	"fmt"
	"strings"
)

// Registration is a role-free request approved by an administrator.
type Registration struct {
	SessionID string `json:"session_id"`
	AgentID   string `json:"agent_id"`
	State     string `json:"state"`
	Room      string `json:"room,omitempty"`
	Role      string `json:"role,omitempty"`
}
type registration struct {
	Registration
	Connection string
	Token      string
}

// RegistrationTicket permits querying only this request and attaching after assignment.
type RegistrationTicket struct {
	Auth         SessionAuth  `json:"auth"`
	Registration Registration `json:"registration"`
}

// Register reserves a distinct context but reveals no task or room information.
func (m *Manager) Register(agent, connection, operationID string) (RegistrationTicket, error) {
	if err := validOperation(operationID); err != nil {
		return RegistrationTicket{}, err
	}
	if strings.TrimSpace(agent) == "" || len(agent) > 256 || connection == "" || len(connection) > 256 {
		return RegistrationTicket{}, fmt.Errorf("stable agent and separate connection required")
	}
	token, err := secret()
	if err != nil {
		return RegistrationTicket{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var result RegistrationTicket
	err = m.update(func(d *snapshot) error {
		key := "register/" + operationID
		hash := fingerprint([]string{agent, connection})
		if old, ok := d.Operations[key]; ok {
			if old.Hash != hash {
				return ErrOperationConflict
			}
			if old.Ticket == nil {
				return fmt.Errorf("invalid registration operation")
			}
			result = *old.Ticket
			return nil
		}
		id := envelope.NewID()
		if err := reserve(d, "context:"+connection, id); err != nil {
			return err
		}
		r := registration{Registration: Registration{SessionID: id, AgentID: agent, State: "WAITING_ASSIGNMENT"}, Connection: connection, Token: token}
		d.Registrations[id] = r
		result = RegistrationTicket{Auth: SessionAuth{Session: id, Token: token}, Registration: r.Registration}
		d.Operations[key] = operation{Hash: hash, Ticket: &result}
		return nil
	})
	return result, err
}

// RegistrationStatus discloses only the requesting agent's assignment.
func (m *Manager) RegistrationStatus(auth SessionAuth) (Registration, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.data.Registrations[auth.Session]
	if !ok || r.Token != auth.Token || auth.Generation != 0 {
		return Registration{}, fmt.Errorf("invalid registration ticket")
	}
	return r.Registration, nil
}

// AssignRegistered approves a waiting agent without changing its logical session ID.
func (m *Manager) AssignRegistered(roomID, id, role, instructions, as string) (Member, error) {
	return m.assign(roomID, "", role, instructions, as, id)
}
