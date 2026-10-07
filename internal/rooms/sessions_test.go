//go:build unix

package rooms

import (
	"context"
	"coord/internal/atomicfile"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func sessionFixture(t *testing.T, m *Manager, roomID, agent, role string) Member {
	t.Helper()
	s, err := m.Assign(roomID, agent, role, "Only work on this room", "")
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func attachFixture(t *testing.T, m *Manager, s Member, connection string) Attachment {
	t.Helper()
	a, err := m.Resume(ResumeRequest{Session: s.SessionID, Connection: connection, Expected: s.Generation, Operation: "attach-" + connection})
	if err != nil {
		t.Fatal(err)
	}
	return a
}
func TestSessionRecoveryFencesAndScopesContext(t *testing.T) {
	m, tools := fixture(t)
	a, _ := m.Create("task-a", "first", "")
	b, _ := m.Create("task-b", "second", "")
	s := sessionFixture(t, m, a.ID, "agent-stable", "executor")
	other := sessionFixture(t, m, b.ID, "agent-stable", "auditor")
	first := attachFixture(t, m, s, "terminal-a")
	cp := Checkpoint{Progress: "editing parser", AcknowledgedSeq: uint64(len(first.Context.Events)), PendingActions: []Action{{ID: "change-1", State: "executing"}}, ArtifactRefs: []string{"src/parser.go"}}
	if err := m.SaveCheckpoint(first.Auth, "cp-1", cp); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SessionSay(first.Auth, "message-1", "first result"); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(m.cfg, tools)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	second, err := reopened.Resume(ResumeRequest{Session: s.SessionID, Connection: "terminal-new", Expected: 1, Operation: "resume-2", Token: first.Auth.Token})
	if err != nil {
		t.Fatal(err)
	}
	if second.Auth.Generation != 2 || second.Context.Member.AgentID != "agent-stable" || second.Context.Member.Role != "executor" || second.Context.Member.Checkpoint.Progress != "editing parser" {
		t.Fatalf("%+v", second)
	}
	if second.Context.Member.Checkpoint.PendingActions[0].State != "unknown" {
		t.Fatal("unsafe action replay")
	}
	if _, err = reopened.SessionSay(first.Auth, "late", "stale"); !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("old connection accepted: %v", err)
	}
	if _, err = reopened.Context(SessionAuth{Session: other.SessionID, Generation: second.Auth.Generation, Token: second.Auth.Token}, 0); err == nil {
		t.Fatal("cross-session credential accepted")
	}
	for _, event := range second.Context.Events {
		if event.Room != a.ID {
			t.Fatal("context leaked")
		}
	}
	if _, err = reopened.Resume(ResumeRequest{Session: other.SessionID, Connection: "terminal-a", Expected: 0, Operation: "wrong-context"}); err == nil {
		t.Fatal("reused model context accepted")
	}
}

func TestPastRetryAtIsInvalidInputEvenForUnknownSession(t *testing.T) {
	m, _ := fixture(t)
	past := time.Now().Add(-time.Minute)
	err := m.SetBlocker("missing-session", BlockedProviderQuota, "owner", past, "")
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("past retry_at must be rejected before session lookup: %v", err)
	}
}

func TestSessionOperationsAreIdempotentAndAckExplicit(t *testing.T) {
	m, _ := fixture(t)
	r, _ := m.Create("task", "title", "")
	s := sessionFixture(t, m, r.ID, "agent", "executor")
	request := ResumeRequest{Session: s.SessionID, Connection: "new-context", Operation: "resume-1"}
	a, err := m.Resume(request)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := m.Resume(request)
	if err != nil || retry.Auth != a.Auth {
		t.Fatalf("resume retry: %v %+v", err, retry)
	}
	e, err := m.SessionSay(a.Auth, "op-1", "hello")
	if err != nil {
		t.Fatal(err)
	}
	again, err := m.SessionSay(a.Auth, "op-1", "hello")
	if err != nil || again.ID != e.ID {
		t.Fatalf("duplicate %v", err)
	}
	if _, err = m.SessionSay(a.Auth, "op-1", "different"); !errors.Is(err, ErrOperationConflict) {
		t.Fatal(err)
	}
	ctx, err := m.Context(a.Auth, 0)
	if err != nil {
		t.Fatal(err)
	}
	if ctx.Member.Checkpoint.AcknowledgedSeq != 0 {
		t.Fatal("read implied acknowledgement")
	}
	if err = m.Ack(a.Auth, "ack-1", e.Seq); err != nil {
		t.Fatal(err)
	}
	if err = m.Ack(a.Auth, "ack-bad", e.Seq+100); err == nil {
		t.Fatal("future cursor")
	}
	if err = m.Ack(a.Auth, "ack-old", 0); err == nil {
		t.Fatal("cursor regressed")
	}
}
func TestHistoryProjectionAndCommitRecovery(t *testing.T) {
	m, _ := fixture(t)
	r, _ := m.Create("task", "title", "")
	s := sessionFixture(t, m, r.ID, "agent", "executor")
	a := attachFixture(t, m, s, "context")
	if _, err := m.SessionSay(a.Auth, "op", "private text"); err != nil {
		t.Fatal(err)
	}
	if err := m.Export(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	status := m.HistoryStatus()
	if status.Backlog != 0 || status.Commit == "" {
		t.Fatalf("%+v", status)
	}
	raw, err := os.ReadFile(filepath.Join(m.cfg.State, "history", "projection.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), a.Auth.Token) || strings.Contains(string(raw), "private text") {
		t.Fatal("private data exported")
	}
	var projection any
	if err = json.Unmarshal(raw, &projection); err != nil {
		t.Fatal(err)
	}
	if err = m.Export(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(filepath.Join(m.cfg.State, "history", "projection.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "private text") || strings.Contains(string(raw), a.Auth.Token) {
		t.Fatal("content policy or credential stripping failed")
	}
	commit := m.HistoryStatus().Commit
	if err = m.Export(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if m.HistoryStatus().Commit != commit {
		t.Fatal("unchanged export duplicated commit")
	}
}

func TestUnassignedRegistrationHasNoRoomContext(t *testing.T) {
	m, _ := fixture(t)
	r, _ := m.Create("classified-task", "title", "")
	ticket, err := m.Register("agent", "fresh-context", "register-1")
	if err != nil {
		t.Fatal(err)
	}
	if ticket.Registration.State != "WAITING_ASSIGNMENT" {
		t.Fatal(ticket)
	}
	if _, err = m.Context(ticket.Auth, 0); err == nil {
		t.Fatal("unassigned context leaked")
	}
	retry, err := m.Register("agent", "fresh-context", "register-1")
	if err != nil || retry.Auth != ticket.Auth {
		t.Fatal("registration not idempotent", err)
	}
	assigned, err := m.AssignRegistered(r.ID, ticket.Auth.Session, "executor", "Stay within task", "")
	if err != nil {
		t.Fatal(err)
	}
	if assigned.SessionID != ticket.Auth.Session {
		t.Fatal("assignment replaced session identity")
	}
	status, err := m.RegistrationStatus(ticket.Auth)
	if err != nil || status.State != "ASSIGNED" {
		t.Fatal(status, err)
	}
	a, err := m.Resume(ResumeRequest{Session: assigned.SessionID, Connection: "fresh-context", Operation: "attach", Token: ticket.Auth.Token})
	if err != nil {
		t.Fatal(err)
	}
	if a.Context.Room != r.ID || a.Context.Member.Role != "executor" {
		t.Fatal(a)
	}
}
func TestGitCommitBeforeCursorCrashDoesNotDuplicate(t *testing.T) {
	m, tools := fixture(t)
	_, err := m.Create("task", "title", "")
	if err != nil {
		t.Fatal(err)
	}
	original := m.cfg
	writes := 0
	m.cfg.Persist = func(path string, raw []byte) error {
		writes++
		if writes == 2 {
			return errors.New("crash after git commit")
		}
		return atomicfile.Write(path, raw)
	}
	if err = m.Export(context.Background(), false); err == nil {
		t.Fatal("injected crash ignored")
	}
	if err = m.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(original, tools)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	path := filepath.Join(original.State, "history")
	first, err := gitCommand(context.Background(), path, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if reopened.HistoryStatus().Pending == "" {
		t.Fatal("export intent lost")
	}
	if err = reopened.Export(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if reopened.HistoryStatus().Commit != first || reopened.HistoryStatus().Backlog != 0 {
		t.Fatal("commit replay duplicated or cursor not recovered")
	}
}
func TestPersistenceFailureDoesNotPublishSessionMessage(t *testing.T) {
	m, _ := fixture(t)
	r, _ := m.Create("task", "title", "")
	s := sessionFixture(t, m, r.ID, "agent", "executor")
	a := attachFixture(t, m, s, "context")
	before, _ := m.Events(r.ID, 0)
	m.cfg.Persist = func(string, []byte) error { return errors.New("disk full") }
	if _, err := m.SessionSay(a.Auth, "fail", "must not publish"); err == nil {
		t.Fatal("failed write accepted")
	}
	after, _ := m.Events(r.ID, 0)
	if len(after) != len(before) {
		t.Fatal("failed event published")
	}
}

func TestReadinessAndInviteRetry(t *testing.T) {
	m, _ := fixture(t)
	r, _ := m.Create("task", "title", "")
	s := sessionFixture(t, m, r.ID, "agent", "executor")
	inv, err := m.InviteSession(s.SessionID, "", "", "invite-1", "")
	if err != nil {
		t.Fatal(err)
	}
	again, err := m.InviteSession(s.SessionID, "", "", "invite-1", "")
	if err != nil || again.Command != inv.Command {
		t.Fatal("invite retry", err)
	}
	_, _, p, _ := lookup(&m.data, s.SessionID)
	a, err := m.AcceptAttachment(p.Invite)
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := m.AcceptAttachment(p.Invite)
	if err != nil || repeated.Auth != a.Auth {
		t.Fatal("lost accept response cannot recover", err)
	}
	if err = m.Ready(a.Auth, "wrong", Readiness{Regulation: "wrong"}); err == nil {
		t.Fatal("wrong regulation accepted")
	}
	if err = m.Ready(a.Auth, "ready", Readiness{Regulation: "coord-v1", Capabilities: []string{"cli"}}); err != nil {
		t.Fatal(err)
	}
	c, err := m.Context(a.Auth, 0)
	if err != nil || !c.Member.Ready {
		t.Fatal("readiness lost", err)
	}
}
func TestAuditorAcrossRoomsShowsSharedIdentity(t *testing.T) {
	m, _ := fixture(t)
	a, _ := m.Create("same-task", "execute", "")
	b, _ := m.Create("same-task", "audit", "")
	_ = sessionFixture(t, m, a.ID, "same-agent", "executor")
	auditor := sessionFixture(t, m, b.ID, "same-agent", "auditor")
	for _, s := range m.Sessions() {
		if s.SessionID == auditor.SessionID && s.IndependentAudit {
			t.Fatal("shared identity called independent")
		}
	}
}

func TestOwnerInvitesCoordinatorThenCoordinatorAssigns(t *testing.T) {
	m, _ := fixture(t)
	started, err := m.Start("project", "title", "root-coordinator", "codex:root", "codex-1", "Coordinate only.", "start-1")
	if err != nil {
		t.Fatal(err)
	}
	repeat, err := m.Start("project", "title", "root-coordinator", "codex:root", "codex-1", "Coordinate only.", "start-1")
	if err != nil || repeat != started {
		t.Fatal("owner start not idempotent", err)
	}
	if len(m.List()) != 1 {
		t.Fatal("start duplicated room")
	}
	_, _, p, _ := lookup(&m.data, started.SessionID)
	a, err := m.AcceptAttachment(p.Invite)
	if err != nil {
		t.Fatal(err)
	}
	if a.Context.Member.Role != "coordinator" {
		t.Fatal("owner did not assign coordinator")
	}
	worker, err := m.Assign(started.RoomID, "worker", "executor", "Fixture only.", a.Auth.Token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.InviteSession(worker.SessionID, "worker", "opencode-2", "invite-worker", a.Auth.Token); err != nil {
		t.Fatal(err)
	}
	child, err := m.Create("child-task", "child", a.Auth.Token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.Assign(child.ID, "another-worker", "executor", "Child only.", a.Auth.Token); err != nil {
		t.Fatal("coordinator cannot manage its own child task", err)
	}
}

func TestCoalescedNotificationsKeepLatestEvent(t *testing.T) {
	m, _ := fixture(t)
	r, _ := m.Create("task", "title", "")
	if err := m.Invite(r.ID, "peer", "opencode-1", "reviewer", "agent", ""); err != nil {
		t.Fatal(err)
	}
	p := m.data.Rooms[r.ID].Members["opencode-1"]
	if err := m.Accept(p.Invite); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 30; i++ {
		if _, err := m.Say(r.ID, fmt.Sprintf("message %d", i), ""); err != nil {
			t.Fatal(err)
		}
	}
	count := 0
	for _, item := range m.data.Outbox {
		if !item.Delivered && item.Kind == "event" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("want one catch-up notification, got %d", count)
	}
	if err := m.Deliver(context.Background()); err != nil {
		t.Fatal(err)
	}
	p = m.data.Rooms[r.ID].Members["opencode-1"]
	if p.Checkpoint.AcknowledgedSeq != 0 {
		t.Fatal("delivery advanced cursor")
	}
}

// Блокер останавливает новые назначения роли на любом пути assign() и не
// трогает checkpoint/журнал; снимается только явным действием (§1.3).
func TestBlockerStopsEveryAssignmentPathAndKeepsCheckpoint(t *testing.T) {
	m, _ := fixture(t)
	room, err := m.Create("task", "title", "")
	if err != nil {
		t.Fatal(err)
	}
	ticket, err := m.Register("first-agent", "context-first", "register-1")
	if err != nil {
		t.Fatal(err)
	}
	if err != nil {
		t.Fatal(err)
	}
	assigned, err := m.AssignRegistered(room.ID, ticket.Registration.SessionID, "executor", "Only work on this room", "")
	if err != nil {
		t.Fatal(err)
	}
	attachment := attachFixture(t, m, assigned, "context-first")
	if err = m.SaveCheckpoint(attachment.Auth, "cp-1", Checkpoint{
		Progress:        "half done",
		PendingActions:  []Action{{ID: "a1", State: "executing"}},
		ArtifactRefs:    []string{},
		AcknowledgedSeq: 1,
	}); err != nil {
		t.Fatal(err)
	}
	retry := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	if err = m.SetBlocker(assigned.SessionID, BlockedProviderQuota, "owner", retry, ""); err != nil {
		t.Fatal(err)
	}

	// Повторное назначение той же сессии отказывает и объясняет следующее действие.
	_, err = m.AssignRegistered(room.ID, assigned.SessionID, "executor", "Only work on this room", "")
	if err == nil || !strings.Contains(err.Error(), "заблокирована") || !strings.Contains(err.Error(), "unblock") {
		t.Fatalf("blocked session must refuse assignment: %v", err)
	}
	if !strings.Contains(err.Error(), retry.Format(time.RFC3339)) {
		t.Fatalf("refusal must name retry_at: %v", err)
	}

	// Checkpoint и журнал переживают блокировку без изменений.
	context, err := m.Context(attachment.Auth, 0)
	if err != nil {
		t.Fatal(err)
	}
	if context.Member.Checkpoint.Progress != "half done" || context.Member.Checkpoint.PendingActions[0].State != "executing" {
		t.Fatalf("blocker must not touch checkpoint: %+v", context.Member.Checkpoint)
	}
	if context.Member.Blocker == nil || context.Member.Blocker.Reason != BlockedProviderQuota {
		t.Fatalf("blocker must be visible to the session: %+v", context.Member.Blocker)
	}

	// Карточка показывает blocked, но attachment/readiness не переписываются.
	var card Availability
	for _, a := range m.Availability() {
		if a.SessionID == assigned.SessionID {
			card = a
		}
	}
	if card.Provider != "blocked" || card.Attachment != "attached" {
		t.Fatalf("card must keep fields separate: %+v", card)
	}
	if !strings.Contains(card.Expectation, BlockedProviderQuota) {
		t.Fatalf("expectation must name the blocker: %q", card.Expectation)
	}

	// Снятие — только явным действием, после назначения снова разрешено.
	if err = m.ClearBlocker(assigned.SessionID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err = m.AssignRegistered(room.ID, assigned.SessionID, "executor", "Only work on this room", ""); err != nil {
		t.Fatalf("assignment after explicit unblock: %v", err)
	}
}

// Сессия с меньшим поколением того же agent_id видна отдельно, а её
// connection/capability остаётся рабочим — stale это факт, не запрет.
func TestAvailabilitySeparatesStaleGeneration(t *testing.T) {
	m, _ := fixture(t)
	first, _ := m.Create("task-1", "one", "")
	second, _ := m.Create("task-2", "two", "")
	older := sessionFixture(t, m, first.ID, "stable-agent", "executor")
	oldAttach := attachFixture(t, m, older, "context-old")
	newer := sessionFixture(t, m, second.ID, "stable-agent", "reviewer")
	attached := attachFixture(t, m, newer, "context-new")
	// Второе переподключение того же агента поднимает только его поколение.
	rotated, err := m.Resume(ResumeRequest{Session: newer.SessionID, Connection: "context-newer", Expected: attached.Auth.Generation, Operation: "rotate-newer"})
	if err != nil {
		t.Fatal(err)
	}
	if rotated.Auth.Generation <= attached.Auth.Generation {
		t.Fatal("fixture must raise the newest generation")
	}
	attached = rotated
	states := map[string]string{}
	for _, a := range m.Availability() {
		states[a.SessionID] = a.State
	}
	if states[older.SessionID] != "stale_generation" {
		t.Fatalf("older generation must be visible: %q", states[older.SessionID])
	}
	if states[newer.SessionID] != "active" {
		t.Fatalf("newest generation stays active: %q", states[newer.SessionID])
	}
	// Отметка stale — только отображение: capability самой сессии не отзывается.
	if _, err = m.Context(oldAttach.Auth, 0); err != nil {
		t.Fatalf("stale reporting must not revoke that session credential: %v", err)
	}
	if _, err = m.Context(attached.Auth, 0); err != nil {
		t.Fatalf("stale reporting must not revoke the newest credential: %v", err)
	}
}
