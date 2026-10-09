package store

import (
	"context"
	"coord/envelope"
	"coord/fsm"
	"coord/transport"
	"crypto/ed25519"
	"errors"
	"testing"
	"time"
)

type agentCommandByteClient struct{ incoming chan []byte }

func (c *agentCommandByteClient) Send(context.Context, string, []byte) error { return nil }
func (c *agentCommandByteClient) Incoming(context.Context) <-chan []byte     { return c.incoming }

type agentCommandKeyResolver struct{ key transport.VerificationKey }

func (r agentCommandKeyResolver) ResolveVerificationKey(context.Context, string) (transport.VerificationKey, error) {
	return r.key, nil
}

func checkValidSignedDeliveryIsNotAcceptance(t *testing.T) {
	t.Helper()
	state := taskStateWithRun()
	run := state.Runs["run-1"]
	seed := make([]byte, ed25519.SeedSize)
	seed[0] = 0x5a
	private := ed25519.NewKeyFromSeed(seed)
	public := private.Public().(ed25519.PublicKey)
	session := state.AgentSessions[run.OwnerSession]
	session.PublicKey = public
	state.AgentSessions[run.OwnerSession] = session
	for i := range state.AgentSessionEvents {
		if state.AgentSessionEvents[i].Address == run.OwnerSession {
			state.AgentSessionEvents[i].PublicKey = public
		}
	}
	frame := validAgentCommandFrame(state, "signed-delivery")
	frame.envelope.Run = "unowned-run-id"
	raw, err := envelope.Sign(frame.envelope, private)
	if err != nil {
		t.Fatal(err)
	}
	client := &agentCommandByteClient{incoming: make(chan []byte, 1)}
	client.incoming <- raw
	close(client.incoming)
	signed := &transport.SignedClient{
		Client:   client,
		Resolver: agentCommandKeyResolver{key: transport.VerificationKey{PublicKey: public, Generation: session.Generation}},
	}
	adapter := &transport.AuthenticatedAdapter{Client: signed, Session: "coordd"}
	var received transport.AuthenticatedFrame
	count := 0
	for received = range adapter.ReceiveAuthenticated(context.Background()) {
		count++
	}
	if count != 1 || received.ReceivedBy() != "coordd" {
		t.Fatalf("valid signed frame delivery count=%d recipient=%q", count, received.ReceivedBy())
	}
	p := &canonicalMemory{state: state}
	s := newCanonicalWithTestClock(t, p, &canonicalTestClock{now: time.Unix(5000, 0).UTC()})
	_, replayed, err := s.ApplyAgentCommand(received, run.TaskID, 2, func(*CanonicalState, fsm.Run) (InboxReceipt, error) {
		return InboxReceipt{}, errors.New("handler must not run")
	})
	if !errors.Is(err, ErrAgentCommandNotCurrentWriter) || replayed || p.saveCalls != 0 || len(p.state.Inbox) != 0 {
		t.Fatalf("valid transport signature was treated as acceptance: replay=%v error=%v saves=%d", replayed, err, p.saveCalls)
	}
}

func TestAgentCommandValidSignatureWithoutRunOwnershipIsRefused(t *testing.T) {
	checkValidSignedDeliveryIsNotAcceptance(t)
}

func TestAgentCommandTransportDeliveryIsNotAcceptance(t *testing.T) {
	checkValidSignedDeliveryIsNotAcceptance(t)
}
