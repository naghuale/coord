package transport

import (
	"bytes"
	"context"
	"coord/envelope"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

type testKeyResolver struct {
	key VerificationKey
	err error
}

type recordingKeyResolver struct {
	key       VerificationKey
	err       error
	addresses []string
}

func (r *recordingKeyResolver) ResolveVerificationKey(_ context.Context, address string) (VerificationKey, error) {
	r.addresses = append(r.addresses, address)
	return r.key, r.err
}

func (r testKeyResolver) ResolveVerificationKey(context.Context, string) (VerificationKey, error) {
	return r.key, r.err
}

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

func TestSignedClientIncomingAuthenticatedCarriesResolvedSessionEvidence(t *testing.T) {
	private := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	public := private.Public().(ed25519.PublicKey)
	c := &bytesClient{incoming: make(chan []byte, 1)}
	e := ping("sender", "receiver")
	raw, err := envelope.Sign(e, private)
	if err != nil {
		t.Fatal(err)
	}
	c.incoming <- raw
	close(c.incoming)
	s := &SignedClient{Client: c, Resolver: testKeyResolver{key: VerificationKey{PublicKey: public, Generation: 7}}}
	frames := s.IncomingAuthenticated(context.Background())
	var got AuthenticatedFrame
	for frame := range frames {
		got = frame
	}
	if got.Envelope().MessageID != e.MessageID || got.SenderAddress() != "sender" || got.SessionGeneration() != 7 || string(got.VerifiedPublicKey()) != string(public) {
		t.Fatalf("authenticated frame did not preserve resolver evidence: %+v", got)
	}
	if got.ReceivedBy() != "" {
		t.Fatalf("transport client unexpectedly assigned local recipient: %q", got.ReceivedBy())
	}
	gotEnvelope := got.Envelope()
	gotEnvelope.PingPayload.Nonce = "tampered"
	if got.Envelope().PingPayload.Nonce != "nonce" {
		t.Fatal("Envelope exposed mutable authenticated frame data")
	}
	key := got.VerifiedPublicKey()
	key[0] ^= 0xff
	if string(got.VerifiedPublicKey()) != string(public) {
		t.Fatal("VerifiedPublicKey exposed mutable internal key bytes")
	}
	rawCopy := got.RawSignedFrame()
	rawCopy[0] ^= 0xff
	if string(got.RawSignedFrame()) != string(raw) {
		t.Fatal("RawSignedFrame exposed mutable internal bytes")
	}
}

func TestSignedClientAuthenticatedFrameCopiesReceivedSignedBytes(t *testing.T) {
	private := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	public := private.Public().(ed25519.PublicKey)
	c := &bytesClient{incoming: make(chan []byte, 1)}
	raw, err := envelope.Sign(ping("sender", "receiver"), private)
	if err != nil {
		t.Fatal(err)
	}
	want := append([]byte(nil), raw...)
	c.incoming <- raw
	close(c.incoming)
	s := &SignedClient{Client: c, Resolver: testKeyResolver{key: VerificationKey{PublicKey: public, Generation: 1}}}
	var got AuthenticatedFrame
	for frame := range s.IncomingAuthenticated(context.Background()) {
		got = frame
	}
	for i := range raw {
		raw[i] ^= 0xff
	}
	if actual := got.RawSignedFrame(); !bytes.Equal(actual, want) {
		t.Fatal("AuthenticatedFrame retained aliased bytes from the transport client")
	}
}

func TestAgentCommandLegacyPeersDoNotAuthorizeAgentCommand(t *testing.T) {
	private := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	public := private.Public().(ed25519.PublicKey)
	c := &bytesClient{incoming: make(chan []byte, 1)}
	raw, err := envelope.Sign(ping("sender", "receiver"), private)
	if err != nil {
		t.Fatal(err)
	}
	c.incoming <- raw
	close(c.incoming)
	var rejected atomic.Int32
	s := &SignedClient{Client: c, Peers: map[string]ed25519.PublicKey{"sender": public}, Rejected: func() { rejected.Add(1) }}
	for range s.IncomingAuthenticated(context.Background()) {
		t.Fatal("legacy Peers config authorized an authenticated AgentSession frame")
	}
	if rejected.Load() != 1 {
		t.Fatalf("rejected callback count=%d, want 1", rejected.Load())
	}
}

func TestAgentCommandIgnoresActorIDFromBody(t *testing.T) {
	private := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	public := private.Public().(ed25519.PublicKey)
	message, err := json.Marshal(map[string]any{
		"schema_version": 1, "message_id": "message-1", "timestamp": time.Unix(10, 0).UTC(),
		"from_session": "sender", "to_session": "receiver", "type": "PING",
		"ping_payload": map[string]string{"nonce": "nonce"}, "actor_id": "forged-agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(envelope.Signed{Message: message, Signature: ed25519.Sign(private, message)})
	if err != nil {
		t.Fatal(err)
	}
	c := &bytesClient{incoming: make(chan []byte, 1)}
	c.incoming <- raw
	close(c.incoming)
	var rejected atomic.Int32
	s := &SignedClient{Client: c, Resolver: testKeyResolver{key: VerificationKey{PublicKey: public, Generation: 1}}, Rejected: func() { rejected.Add(1) }}
	for range s.IncomingAuthenticated(context.Background()) {
		t.Fatal("body-supplied actor_id was accepted")
	}
	if rejected.Load() != 1 {
		t.Fatalf("rejected=%d, want 1", rejected.Load())
	}
}

func TestAgentCommandRejectsUntrustedSignature(t *testing.T) {
	private := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	public := private.Public().(ed25519.PublicKey)
	c := &bytesClient{incoming: make(chan []byte, 1)}
	raw, _ := envelope.Sign(ping("sender", "receiver"), private)
	var signed envelope.Signed
	if err := json.Unmarshal(raw, &signed); err != nil {
		t.Fatal(err)
	}
	signed.Signature[0] ^= 1
	bad, _ := json.Marshal(signed)
	c.incoming <- bad
	close(c.incoming)
	var rejected atomic.Int32
	s := &SignedClient{Client: c, Resolver: testKeyResolver{key: VerificationKey{PublicKey: public, Generation: 1}}, Rejected: func() { rejected.Add(1) }}
	for range s.IncomingAuthenticated(context.Background()) {
		t.Fatal("invalid signature was accepted")
	}
	if rejected.Load() != 1 {
		t.Fatalf("rejected=%d, want 1", rejected.Load())
	}
}

func TestSignedClientIncomingAuthenticatedRejectsResolverFailureAndInvalidSignature(t *testing.T) {
	private := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	public := private.Public().(ed25519.PublicKey)
	t.Run("resolver error", func(t *testing.T) {
		c := &bytesClient{incoming: make(chan []byte, 1)}
		raw, _ := envelope.Sign(ping("sender", "receiver"), private)
		c.incoming <- raw
		close(c.incoming)
		var rejected atomic.Int32
		s := &SignedClient{Client: c, Resolver: testKeyResolver{err: errors.New("revoked")}, Rejected: func() { rejected.Add(1) }}
		for range s.IncomingAuthenticated(context.Background()) {
			t.Fatal("frame with resolver error was delivered")
		}
		if rejected.Load() != 1 {
			t.Fatalf("rejected=%d, want 1", rejected.Load())
		}
	})
	t.Run("bad signature", func(t *testing.T) {
		c := &bytesClient{incoming: make(chan []byte, 1)}
		raw, _ := envelope.Sign(ping("sender", "receiver"), private)
		var signed envelope.Signed
		if err := json.Unmarshal(raw, &signed); err != nil {
			t.Fatal(err)
		}
		signed.Signature[0] ^= 1
		bad, _ := json.Marshal(signed)
		c.incoming <- bad
		close(c.incoming)
		var rejected atomic.Int32
		s := &SignedClient{Client: c, Resolver: testKeyResolver{key: VerificationKey{PublicKey: public, Generation: 1}}, Rejected: func() { rejected.Add(1) }}
		for range s.IncomingAuthenticated(context.Background()) {
			t.Fatal("invalid signature was delivered")
		}
		if rejected.Load() != 1 {
			t.Fatalf("rejected=%d, want 1", rejected.Load())
		}
	})
}

func TestSignedClientResolverErrorDoesNotFallbackToPeers(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  VerificationKey
		err  error
	}{
		{name: "missing registration", err: errors.New("unknown AgentSession")},
		{name: "revoked registration", err: errors.New("AgentSession revoked")},
		{name: "different registered key", key: VerificationKey{PublicKey: ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x44}, ed25519.SeedSize)).Public().(ed25519.PublicKey), Generation: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			private := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x33}, ed25519.SeedSize))
			public := private.Public().(ed25519.PublicKey)
			c := &bytesClient{incoming: make(chan []byte, 1)}
			raw, err := envelope.Sign(ping("sender", "receiver"), private)
			if err != nil {
				t.Fatal(err)
			}
			c.incoming <- raw
			close(c.incoming)
			resolver := &recordingKeyResolver{key: tc.key, err: tc.err}
			var rejected atomic.Int32
			s := &SignedClient{
				Client: c, Peers: map[string]ed25519.PublicKey{"sender": public}, Resolver: resolver,
				Rejected: func() { rejected.Add(1) },
			}
			var boundaryCalls atomic.Int32
			a := &AuthenticatedAdapter{Client: s, Session: "receiver"}
			for range a.ReceiveAuthenticated(context.Background()) {
				boundaryCalls.Add(1)
			}
			if len(resolver.addresses) != 1 || resolver.addresses[0] != "sender" {
				t.Fatalf("resolver addresses=%v, want [sender]", resolver.addresses)
			}
			if boundaryCalls.Load() != 0 || rejected.Load() != 1 {
				t.Fatalf("legacy Peers fallback delivered frame: boundaryCalls=%d rejected=%d", boundaryCalls.Load(), rejected.Load())
			}
		})
	}
}

func TestSignedClientRejectsZeroResolvedGeneration(t *testing.T) {
	private := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x35}, ed25519.SeedSize))
	public := private.Public().(ed25519.PublicKey)
	c := &bytesClient{incoming: make(chan []byte, 1)}
	raw, err := envelope.Sign(ping("sender", "receiver"), private)
	if err != nil {
		t.Fatal(err)
	}
	c.incoming <- raw
	close(c.incoming)
	resolver := &recordingKeyResolver{key: VerificationKey{PublicKey: public}}
	var rejected atomic.Int32
	s := &SignedClient{
		Client: c, Resolver: resolver,
		Rejected: func() { rejected.Add(1) },
	}
	var boundaryCalls atomic.Int32
	a := &AuthenticatedAdapter{Client: s, Session: "receiver"}
	for range a.ReceiveAuthenticated(context.Background()) {
		boundaryCalls.Add(1)
	}
	if len(resolver.addresses) != 1 || boundaryCalls.Load() != 0 || rejected.Load() != 1 {
		t.Fatalf("generation-zero delivery resolver=%v boundaryCalls=%d rejected=%d", resolver.addresses, boundaryCalls.Load(), rejected.Load())
	}
}

func TestAuthenticatedAdapterRejectsWrongRecipient(t *testing.T) {
	private := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	public := private.Public().(ed25519.PublicKey)
	c := &bytesClient{incoming: make(chan []byte, 1)}
	raw, _ := envelope.Sign(ping("sender", "someone-else"), private)
	c.incoming <- raw
	close(c.incoming)
	var rejected atomic.Int32
	signed := &SignedClient{Client: c, Resolver: testKeyResolver{key: VerificationKey{PublicKey: public, Generation: 1}}}
	a := &AuthenticatedAdapter{Client: signed, Session: "receiver", Rejected: func() { rejected.Add(1) }}
	for range a.ReceiveAuthenticated(context.Background()) {
		t.Fatal("frame for another recipient was delivered")
	}
	if rejected.Load() != 1 {
		t.Fatalf("rejected=%d, want 1", rejected.Load())
	}
}
