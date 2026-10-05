package envelope

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
	"time"
)

func TestSignatureRejectsTamperAndUnknownSession(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	e := Envelope{SchemaVersion: 1, MessageID: NewID(), Timestamp: time.Now().UTC(), From: "a", To: "b", Kind: Ping, PingPayload: &PingPayload{Nonce: "nonce"}}
	raw, err := Sign(e, priv)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Verify(raw, map[string]ed25519.PublicKey{"a": pub}); err != nil {
		t.Fatal(err)
	}
	bad := []byte(strings.Replace(string(raw), "nonce", "other", 1))
	if _, err = Verify(bad, map[string]ed25519.PublicKey{"a": pub}); err == nil {
		t.Fatal("tamper accepted")
	}
	if _, err = Verify(raw, map[string]ed25519.PublicKey{}); err == nil {
		t.Fatal("unknown session accepted")
	}
}
