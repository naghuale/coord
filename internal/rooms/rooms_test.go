//go:build unix

package rooms

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type tool struct {
	bodies []string
	to     []string
	err    error
}

func (t *tool) Call(_ context.Context, _ string, args map[string]any) (string, error) {
	t.to = append(t.to, args["to"].(string))
	t.bodies = append(t.bodies, args["message"].(string))
	return "delivered", t.err
}
func fixture(t *testing.T) (*Manager, *tool) {
	t.Helper()
	dir := t.TempDir()
	tools := &tool{}
	m, err := Open(Config{Path: filepath.Join(dir, "rooms.json"), State: dir, Executable: "/bin/coord", Now: time.Now}, tools)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := m.Close(); err != nil {
			t.Error(err)
		}
	})
	return m, tools
}
func TestRoomsSeparateTasksAndRoles(t *testing.T) {
	m, tools := fixture(t)
	a, err := m.Create("TASK-A", "Implement", "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.Create("TASK-B", "Review", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Invite(a.ID, "opencode:a", "opencode-1", "executor", "agent-a", ""); err != nil {
		t.Fatal(err)
	}
	if err = m.Deliver(context.Background()); err != nil {
		t.Fatal(err)
	}
	token := m.data.Rooms[a.ID].Members["opencode-1"].Invite
	if !strings.Contains(tools.bodies[0], "accept '"+token+"'") {
		t.Fatal("no simple invitation")
	}
	if err = m.Accept(token); err != nil {
		t.Fatal(err)
	}
	member := m.data.Rooms[a.ID].Members["opencode-1"]
	if _, err = m.Say(b.ID, "cross-room", member.Token); err == nil {
		t.Fatal("token crosses rooms")
	}
	event, err := m.SessionSay(SessionAuth{Session: member.SessionID, Generation: member.Generation, Token: member.Token}, "ready-message", "Ready")
	if err != nil {
		t.Fatal(err)
	}
	if event.Role != "executor" || event.Agent != "agent-a" {
		t.Fatalf("%+v", event)
	}
	history, err := m.Events(b.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].Kind != "created" {
		t.Fatal(history)
	}
	if err = m.Invite(a.ID, "codex:b", "codex-2", "auditor", "agent-a", ""); err == nil {
		t.Fatal("executor became independent auditor")
	}
	if err = m.Invite(a.ID, "codex:b", "codex-2", "reviewer", "agent-b", member.Token); err == nil {
		t.Fatal("executor gained role-management power")
	}
}
func TestPeerMessagesFanOutWithoutOwnerRelay(t *testing.T) {
	m, tools := fixture(t)
	room, err := m.Create("task", "Collaborate", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"opencode-1", "opencode-2"} {
		if err = m.Invite(room.ID, p, p, "reviewer", p, ""); err != nil {
			t.Fatal(err)
		}
		if err = m.Accept(m.data.Rooms[room.ID].Members[p].Invite); err != nil {
			t.Fatal(err)
		}
	}
	first := m.data.Rooms[room.ID].Members["opencode-1"]
	event, err := m.SessionSay(SessionAuth{Session: first.SessionID, Generation: first.Generation, Token: first.Token}, "check-message", "Check this")
	if err != nil {
		t.Fatal(err)
	}
	var recipients []string
	for _, entry := range m.data.Outbox {
		if entry.Event == event.ID {
			recipients = append(recipients, entry.Peer)
		}
	}
	if len(recipients) != 1 || recipients[0] != "opencode-2" {
		t.Fatal(recipients)
	}
	tools.err = errors.New("rate limit")
	if err = m.Deliver(context.Background()); err == nil {
		t.Fatal("delivery failure hidden")
	}
	pending := m.List()[0].Pending
	if pending == 0 {
		t.Fatal("failed delivery was lost")
	}
	if err = m.CloseRoom(room.ID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err = m.Say(room.ID, "late", first.Token); err == nil {
		t.Fatal("closed room accepts messages")
	}
}
func TestRoomStateReloadAndOneWriter(t *testing.T) {
	m, tools := fixture(t)
	room, err := m.Create("task", "title", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Open(m.cfg, tools); err == nil {
		t.Fatal("second writer acquired room state")
	}
	if err = m.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(m.cfg, tools)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := reopened.Close(); err != nil {
			t.Error(err)
		}
	}()
	events, err := reopened.Events(room.ID, 0)
	if err != nil || len(events) != 1 {
		t.Fatalf("%v %v", events, err)
	}
	views := reopened.List()
	views[0].Title = "mutated"
	if reopened.List()[0].Title != "title" {
		t.Fatal("public snapshot aliases store")
	}
}
