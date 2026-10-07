//go:build unix

package localchat

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type sent struct{ to, text string }
type fakeTools struct{ messages chan sent }

func (f *fakeTools) Call(ctx context.Context, name string, args map[string]any) (string, error) {
	if name == "list_peers" {
		return "This session is opencode:coord [opencode-10].\nReachable sessions (1):\n- opencode:other [opencode-20] · idle\n", nil
	}
	if name != "send_message" {
		return "", errors.New("unexpected tool")
	}
	select {
	case f.messages <- sent{args["to"].(string), args["message"].(string)}:
		return "Message delivered.", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}
func fixture(t *testing.T) (*Service, *fakeTools) {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(dir, "conversation")
	if err = os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "--quiet", repo}, {"-C", repo, "-c", "user.name=CoordTest", "-c", "user.email=test@coord.invalid", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "--quiet", "-m", "fixture"}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("%s %v", out, err)
		}
	}
	tools := &fakeTools{messages: make(chan sent, 8)}
	s, err := New(Config{State: dir, Executable: "/usr/local/bin/coord", Now: time.Now}, tools)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	return s, tools
}
func ticket(t *testing.T, text, command string) string {
	t.Helper()
	needle := command + " '"
	_, tail, ok := strings.Cut(text, needle)
	if !ok {
		t.Fatalf("missing command: %s", text)
	}
	value, _, ok := strings.Cut(tail, "'")
	if !ok {
		t.Fatal("ticket malformed")
	}
	return value
}
func paired(t *testing.T, s *Service, tools *fakeTools) {
	t.Helper()
	done := make(chan error, 1)
	go func() { _, err := s.Connect(context.Background(), "opencode:other"); done <- err }()
	message := <-tools.messages
	if message.to != "opencode-20" {
		t.Fatal("ambiguous address used")
	}
	token := ticket(t, message.text, "accept")
	if err := s.Accept("ordinary PONG"); err == nil {
		t.Fatal("chat text accepted as protocol pairing")
	}
	if err := s.Accept(token); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := s.Accept(token); err == nil {
		t.Fatal("pair token reusable")
	}
}
func TestConnectionNeedsCorrelatedAcceptance(t *testing.T) {
	s, tools := fixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := s.Connect(ctx, "opencode:other"); !errors.Is(err, ErrNotConfirmed) {
		t.Fatalf("delivery mistaken for acceptance: %v", err)
	}
	<-tools.messages
	if _, err := s.Chat(context.Background(), "hello"); !errors.Is(err, ErrNotConnected) {
		t.Fatal(err)
	}
	paired(t, s, tools)
}
func TestChatUsesRealCoordinatorAndReceipt(t *testing.T) {
	s, tools := fixture(t)
	paired(t, s, tools)
	done := make(chan struct {
		r   Answer
		err error
	}, 1)
	go func() {
		r, err := s.Chat(context.Background(), "Which failure matters?")
		done <- struct {
			r   Answer
			err error
		}{r, err}
	}()
	message := <-tools.messages
	token := ticket(t, message.text, "reply")
	if err := s.Reply("wrong", "forged"); err == nil {
		t.Fatal("forged reply accepted")
	}
	if err := s.Reply(token, "lost RESULT_ACK"); err != nil {
		t.Fatal(err)
	}
	result := <-done
	if result.err != nil || result.r.Text != "lost RESULT_ACK" {
		t.Fatalf("%+v", result)
	}
	status := s.Status()
	if status.Last == nil || status.Last.Status != "DONE" || !status.Last.ReceiptCommitted {
		t.Fatalf("%+v", status)
	}
	b, err := os.ReadFile(filepath.Join(s.cfg.State, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var e struct {
			Kind string `json:"kind"`
		}
		if err = json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatal(err)
		}
		kinds = append(kinds, e.Kind)
	}
	if strings.Join(kinds, ",") != "TASK,ACK,HEARTBEAT,HEARTBEAT,RESULT,RESULT_ACK" {
		t.Fatal(kinds)
	}
	if err := s.Reply(token, "repeat"); err == nil {
		t.Fatal("duplicate executes again")
	}
}
func TestUnknownPeerAndExpiredConnectionNeverConnect(t *testing.T) {
	s, tools := fixture(t)
	if _, err := s.Connect(context.Background(), "missing"); err == nil {
		t.Fatal("unknown peer accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := s.Connect(ctx, "opencode:other"); done <- err }()
	message := <-tools.messages
	token := ticket(t, message.text, "accept")
	cancel()
	<-done
	if err := s.Accept(token); err == nil {
		t.Fatal("cancelled pairing accepted")
	}
}
func TestCancelWaitingAnswerPersistsFailure(t *testing.T) {
	s, tools := fixture(t)
	paired(t, s, tools)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := s.Chat(ctx, "hello"); done <- err }()
	<-tools.messages
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	status := s.Status()
	if status.Last == nil || status.Last.Status != "FAILED" || !status.Last.ReceiptCommitted {
		t.Fatalf("%+v", status)
	}
}
