package telepathy

import (
	"context"
	"coord/envelope"
	"coord/transport"
	"crypto/ed25519"
	"errors"
	"strings"
	"testing"
	"time"
)

type staticAgentResolver struct{ key transport.VerificationKey }

func (r staticAgentResolver) ResolveVerificationKey(context.Context, string) (transport.VerificationKey, error) {
	return r.key, nil
}

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

func TestRuntimeAdapterConfiguresCanonicalAgentSessionIngress(t *testing.T) {
	private := ed25519.NewKeyFromSeed([]byte(strings.Repeat("s", ed25519.SeedSize)))
	public := private.Public().(ed25519.PublicKey)
	e := envelope.Envelope{
		SchemaVersion: envelope.SchemaVersionAgentResult, MessageID: "result-1", Timestamp: time.Now().UTC(),
		From: "codex:worker", To: "opencode:coord", Kind: envelope.Result, InReplyTo: "task-1",
		Task: "task_01ARZ3NDEKTSV4RRFFQ69G5FAV", Run: "run-1", Attempt: 1,
		TargetSHA: strings.Repeat("a", 40), Fence: 1,
		AgentResultPayload: &envelope.AgentResultPayload{
			AssignmentID: "assignment-1", Issuer: "claude:coord", CoordinatorGeneration: 1,
			ExpectedRevision: 1, Verdict: "REVIEW_ACCEPTED", Summary: "reviewed",
			Findings: []envelope.AgentFinding{}, InputArtifactRefs: []string{}, OutputArtifactRefs: []string{},
		},
	}
	raw, err := envelope.Sign(e, private)
	if err != nil {
		t.Fatal(err)
	}
	tools := &fakeTools{result: "BEGIN_COORD_ENVELOPE\n" + string(raw) + "\nEND_COORD_ENVELOPE"}
	client, err := NewClient(tools, "opencode:coord", time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	signed := &transport.SignedClient{Client: client}
	runtime := &runtimeAdapter{Adapter: &transport.Adapter{Client: signed, Session: "opencode:coord"}, client: client, signed: signed}
	if err := runtime.ConfigureAgentSessionIngress(staticAgentResolver{key: transport.VerificationKey{PublicKey: public, Generation: 1}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	select {
	case frame := <-runtime.ReceiveAuthenticated(ctx):
		if frame.ReceivedBy() != "opencode:coord" || frame.SenderAddress() != e.From || frame.SessionGeneration() != 1 || frame.Envelope().MessageID != e.MessageID {
			t.Fatalf("authenticated runtime frame=%+v", frame)
		}
	case <-time.After(time.Second):
		t.Fatal("runtime adapter did not expose authenticated Telepathy frame")
	}
}
