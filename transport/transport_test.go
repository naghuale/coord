package transport

import (
	"context"
	"coord/envelope"
	"crypto/ed25519"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"
)

type bytesClient struct {
	incoming chan []byte
	sent     []byte
}

func (c *bytesClient) Send(_ context.Context, _ string, b []byte) error {
	c.sent = append([]byte(nil), b...)
	return nil
}
func (c *bytesClient) Incoming(context.Context) <-chan []byte { return c.incoming }
func ping(from, to string) envelope.Envelope {
	return envelope.Envelope{SchemaVersion: 1, MessageID: envelope.NewID(), Timestamp: time.Now().UTC(), From: from, To: to, Kind: envelope.Ping, PingPayload: &envelope.PingPayload{Nonce: "nonce"}}
}
func TestAdapterFiltersMalformedAndWrongRecipient(t *testing.T) {
	c := &bytesClient{incoming: make(chan []byte, 3)}
	var malformed atomic.Int32
	a := &Adapter{Client: c, Session: "receiver", Malformed: func() { malformed.Add(1) }}
	wrong, _ := json.Marshal(ping("sender", "other"))
	valid, _ := json.Marshal(ping("sender", "receiver"))
	c.incoming <- []byte("{broken")
	c.incoming <- wrong
	c.incoming <- valid
	close(c.incoming)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	count := 0
	for e := range a.Receive(ctx) {
		count++
		if e.To != "receiver" {
			t.Error("wrong recipient delivered")
		}
	}
	if count != 1 || malformed.Load() != 2 {
		t.Fatalf("count=%d rejected=%d", count, malformed.Load())
	}
}
func TestAdapterCancellationAndNilClient(t *testing.T) {
	a := &Adapter{Session: "sender"}
	if err := a.Send(context.Background(), "receiver", ping("sender", "receiver")); err == nil {
		t.Fatal("missing client accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	select {
	case _, ok := <-a.Receive(ctx):
		if ok {
			t.Fatal("unconfigured receiver produced a message")
		}
	case <-time.After(time.Second):
		t.Fatal("receiver did not close")
	}
	c := &bytesClient{incoming: make(chan []byte)}
	a = &Adapter{Client: c, Session: "sender"}
	ctx, cancel = context.WithCancel(context.Background())
	in := a.Receive(ctx)
	cancel()
	select {
	case _, ok := <-in:
		if ok {
			t.Fatal("message after cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("receiver leaked")
	}
}
func TestSignedClientRoundTripAndInvalidFrames(t *testing.T) {
	private := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	public := private.Public().(ed25519.PublicKey)
	c := &bytesClient{incoming: make(chan []byte, 2)}
	var rejected atomic.Int32
	s := &SignedClient{Client: c, PrivateKey: private, Peers: map[string]ed25519.PublicKey{"sender": public}, Rejected: func() { rejected.Add(1) }}
	raw, _ := json.Marshal(ping("sender", "receiver"))
	if err := s.Send(context.Background(), "receiver", raw); err != nil {
		t.Fatal(err)
	}
	if _, err := envelope.Verify(c.sent, s.Peers); err != nil {
		t.Fatal(err)
	}
	c.incoming <- []byte("{}")
	c.incoming <- c.sent
	close(c.incoming)
	count := 0
	for valid := range s.Incoming(context.Background()) {
		count++
		if _, err := envelope.Decode(valid); err != nil {
			t.Fatal(err)
		}
	}
	if count != 1 || rejected.Load() != 1 {
		t.Fatalf("count=%d rejected=%d", count, rejected.Load())
	}
}
func TestSignedClientRejectsMismatchedRouteAndMissingClient(t *testing.T) {
	private := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	c := &bytesClient{}
	raw, _ := json.Marshal(ping("sender", "receiver"))
	s := &SignedClient{Client: c, PrivateKey: private}
	if err := s.Send(context.Background(), "other", raw); err == nil {
		t.Fatal("mismatched route accepted")
	}
	s = &SignedClient{PrivateKey: private}
	if err := s.Send(context.Background(), "receiver", raw); err == nil {
		t.Fatal("missing client accepted")
	}
	select {
	case _, ok := <-s.Incoming(context.Background()):
		if ok {
			t.Fatal("message without client")
		}
	case <-time.After(time.Second):
		t.Fatal("unconfigured client leaked")
	}
}
