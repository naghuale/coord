//go:build unix

package rooms

import (
	"fmt"
	"strings"
)

func validateSnapshot(d snapshot) error {
	if d.Version != 2 || d.Rooms == nil || d.Bindings == nil || d.Operations == nil || d.Registrations == nil {
		return fmt.Errorf("unsupported room snapshot version %d; legacy state is preserved, use a new --state or explicit offline migration", d.Version)
	}
	if d.History.Cursor > d.Revision {
		return fmt.Errorf("history cursor exceeds revision")
	}
	if p := d.History.Pending; p != nil {
		if p.ID == "" || p.Revision > d.Revision || len(p.Projection) == 0 {
			return fmt.Errorf("invalid pending Git projection")
		}
	}
	seen := map[string]bool{}
	for roomID, r := range d.Rooms {
		for _, p := range r.Members {
			if seen[p.SessionID] || p.SessionID == "" || p.Regulation == "" || (p.Active && p.Generation == 0) {
				return fmt.Errorf("invalid logical session")
			}
			seen[p.SessionID] = true
			if err := validateCheckpoint(p.Checkpoint, 0, uint64(len(r.Events))); err != nil {
				return fmt.Errorf("invalid saved checkpoint: %w", err)
			}
			if p.ConnectionID != "" {
				binding := d.Bindings["context:"+p.ConnectionID]
				if binding != "" && binding != p.SessionID {
					return fmt.Errorf("context binding conflicts with session")
				}
			}
			if p.Ref != "" && d.Bindings["peer:"+p.Ref] != p.SessionID {
				return fmt.Errorf("invalid saved peer binding")
			}
			for _, a := range p.Assignments {
				if a.AgentID == "" || a.Generation > p.Generation {
					return fmt.Errorf("invalid assignment history")
				}
			}
		}
		for _, e := range r.Events {
			if e.Room != roomID {
				return fmt.Errorf("event belongs to wrong room")
			}
		}
	}
	for id, r := range d.Registrations {
		if id != r.SessionID || r.AgentID == "" || r.Token == "" || r.Connection == "" {
			return fmt.Errorf("invalid registration")
		}
		if r.State != "WAITING_ASSIGNMENT" && r.State != "ASSIGNED" {
			return fmt.Errorf("invalid registration state")
		}
		if r.State == "ASSIGNED" && !seen[id] {
			return fmt.Errorf("assigned registration missing session")
		}
		seen[id] = true
	}
	for _, id := range d.Bindings {
		if !seen[id] {
			return fmt.Errorf("binding references missing session")
		}
	}
	for _, item := range d.Outbox {
		r, ok := d.Rooms[item.Room]
		if !ok {
			return fmt.Errorf("outbox room missing")
		}
		if _, ok := r.Members[item.Peer]; !ok {
			return fmt.Errorf("outbox participant missing")
		}
		if item.Kind != "invite" && item.Kind != "event" {
			return fmt.Errorf("invalid outbox kind")
		}
	}
	return nil
}

// SetRegulation versions task instructions without silently changing the session's role.
// The agent must explicitly accept this version again; this does not stop OS processes.
func (m *Manager) SetRegulation(auth SessionAuth, operationID, target, version, instructions string) error {
	if strings.TrimSpace(version) == "" || len(version) > 256 || len(instructions) > 16384 {
		return fmt.Errorf("bounded regulation version and instructions required")
	}
	_, err := m.sessionWrite(auth, operationID, "regulation", []string{target, version, instructions}, func(d *snapshot, roomID, key string, actor member) (*Event, error) {
		if actor.Role != "coordinator" {
			return nil, fmt.Errorf("only coordinator can update another session regulation")
		}
		targetRoom, targetKey, p, ok := lookup(d, target)
		if !ok || targetRoom != roomID {
			return nil, fmt.Errorf("target outside coordinator room")
		}
		r := d.Rooms[roomID]
		if p.Regulation == version && p.Instructions != instructions {
			return nil, ErrOperationConflict
		}
		p.Regulation = version
		p.Instructions = instructions
		p.Ready = false
		p.Readiness = Readiness{}
		r.Members[targetKey] = p
		e := appendEvent(d, &r, m.cfg.Now(), "regulation", actor.AgentID, actor.Role, "Regulation "+version+" assigned to session "+target, false)
		e.Session = actor.SessionID
		e.Generation = actor.Generation
		r.Events[len(r.Events)-1] = e
		d.Rooms[roomID] = r
		return &e, nil
	})
	return err
}
