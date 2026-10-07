package envelope

import (
	"crypto/ed25519"
	"encoding/json"
	"testing"
	"time"
)

func FuzzDecode(f *testing.F) {
	e := Envelope{SchemaVersion: 1, MessageID: NewID(), Timestamp: time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC), From: "sender", To: "receiver", Kind: Ping, PingPayload: &PingPayload{Nonce: "nonce"}}
	raw, _ := json.Marshal(e)
	f.Add(raw)
	f.Add([]byte("null"))
	f.Add([]byte("{}"))
	f.Add([]byte("{broken"))
	f.Fuzz(func(t *testing.T, raw []byte) {
		e, err := Decode(raw)
		if err == nil {
			if err = e.Validate(); err != nil {
				t.Fatal(err)
			}
		}
	})
}
func FuzzVerify(f *testing.F) {
	private := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	keys := map[string]ed25519.PublicKey{"sender": private.Public().(ed25519.PublicKey)}
	e := Envelope{SchemaVersion: 1, MessageID: NewID(), Timestamp: time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC), From: "sender", To: "receiver", Kind: Ping, PingPayload: &PingPayload{Nonce: "nonce"}}
	raw, _ := Sign(e, private)
	f.Add(raw)
	f.Add([]byte("{}"))
	f.Add([]byte("null"))
	f.Fuzz(func(t *testing.T, raw []byte) {
		e, err := Verify(raw, keys)
		if err == nil {
			if err = e.Validate(); err != nil {
				t.Fatal(err)
			}
		}
	})
}
func TestSignedFrameRejectsUnknownFieldsAndTrailingJSON(t *testing.T) {
	private := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	keys := map[string]ed25519.PublicKey{"sender": private.Public().(ed25519.PublicKey)}
	e := Envelope{SchemaVersion: 1, MessageID: NewID(), Timestamp: time.Now().UTC(), From: "sender", To: "receiver", Kind: Ping, PingPayload: &PingPayload{Nonce: "nonce"}}
	raw, err := Sign(e, private)
	if err != nil {
		t.Fatal(err)
	}
	extra := append(append([]byte(nil), raw[:len(raw)-1]...), []byte(`,"unexpected":true}`)...)
	for _, bad := range [][]byte{extra, append(append([]byte(nil), raw...), []byte("{}")...)} {
		if _, err = Verify(bad, keys); err == nil {
			t.Fatal("ambiguous signed frame accepted")
		}
	}
}
