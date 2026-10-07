package telepathy

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type fakeTools struct {
	result string
	err    error
	name   string
	args   map[string]any
}

func (f *fakeTools) Call(_ context.Context, name string, args map[string]any) (string, error) {
	f.name = name
	f.args = args
	return f.result, f.err
}
func TestClientFramesAndErrors(t *testing.T) {
	f := &fakeTools{}
	c, err := NewClient(f, "opencode-42", time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"message":{"from_session":"opencode-42","to_session":"opencode-9"},"signature":"signed"}`)
	if err = c.Send(context.Background(), "opencode-9", raw); err != nil {
		t.Fatal(err)
	}
	if f.name != "send_message" || f.args["to"] != "opencode-9" || !strings.Contains(f.args["message"].(string), "BEGIN_COORD_ENVELOPE\n") {
		t.Fatal(f.args)
	}
	f.err = errors.New("Rate limit")
	if err = c.Send(context.Background(), "opencode-9", raw); err == nil {
		t.Fatal("delivery error hidden")
	}
}
func TestExtractIgnoresChatAndRejectsOversize(t *testing.T) {
	frames := extract("hello\nBEGIN_COORD_ENVELOPE\n{}\nEND_COORD_ENVELOPE\nchat")
	if len(frames) != 1 || string(frames[0]) != "{}" {
		t.Fatal(frames)
	}
	if len(extract("BEGIN_COORD_ENVELOPE\n"+strings.Repeat("x", 100001)+"\nEND_COORD_ENVELOPE")) != 0 {
		t.Fatal("oversized frame accepted")
	}
}
func TestClientCancellationAndArchiveReplay(t *testing.T) {
	f := &fakeTools{result: "No new messages. Most recent received:\nBEGIN_COORD_ENVELOPE\n{}\nEND_COORD_ENVELOPE"}
	c, err := NewClient(f, "opencode-42", time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	in := c.Incoming(ctx)
	select {
	case raw := <-in:
		if string(raw) != "{}" {
			t.Fatal(string(raw))
		}
	case <-time.After(time.Second):
		t.Fatal("archive frame not replayed")
	}
	cancel()
	select {
	case _, ok := <-in:
		if ok {
			t.Fatal("channel open")
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation hangs")
	}
}
