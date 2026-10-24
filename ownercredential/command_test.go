package ownercredential

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func signedTestAssertion(t *testing.T, command Command, private ed25519.PrivateKey, clientData string, flags byte, counter uint32) Assertion {
	t.Helper()
	if clientData == "" {
		encoded, err := json.Marshal(map[string]any{"type": "webauthn.get", "challenge": command.WebAuthnChallenge, "origin": "http://localhost:8090"})
		if err != nil {
			t.Fatal(err)
		}
		clientData = string(encoded)
	}
	data := []byte(clientData)
	auth := make([]byte, 37)
	rpHash := sha256.Sum256([]byte("localhost"))
	copy(auth[:32], rpHash[:])
	auth[32] = flags
	binary.BigEndian.PutUint32(auth[33:], counter)
	clientHash := sha256.Sum256(data)
	signed := append(append([]byte(nil), auth...), clientHash[:]...)
	return Assertion{ClientDataJSON: data, AuthenticatorData: auth, Signature: ed25519.Sign(private, signed)}
}

func TestVerifyAssertionRejectsOriginFlagsCounterAndDuplicateClientData(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key := append([]byte(nil), public...)
	credential := Credential{ID: "key", Algorithm: EdDSA, PublicKey: key, Fingerprint: Fingerprint(EdDSA, key), RegisteredAt: time.Now().UTC(), SignCount: 1}
	command, err := NewChallenge(CommandRequest{Kind: CommandCreateTask, MessageID: "m1", PrincipalID: "owner", CredentialID: "key", ExpectedCredentialRevision: 1, Payload: []byte(`{"title":"x"}`)}, time.Now().UTC(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name  string
		data  string
		flags byte
		count uint32
	}{
		{name: "origin", data: `{"type":"webauthn.get","challenge":"` + command.WebAuthnChallenge + `","origin":"https://evil.example"}`, flags: 0x05, count: 2},
		{name: "user verification", flags: 0x01, count: 2},
		{name: "counter", flags: 0x05, count: 1},
		{name: "duplicate client data", data: `{"type":"webauthn.get","type":"webauthn.get","challenge":"` + command.WebAuthnChallenge + `","origin":"http://localhost:8090"}`, flags: 0x05, count: 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertion := signedTestAssertion(t, command, private, tc.data, tc.flags, tc.count)
			if _, err := VerifyAssertion(command, credential, assertion, "localhost", "http://localhost:8090"); err == nil {
				t.Fatal("invalid WebAuthn assertion accepted")
			}
		})
	}
	valid := signedTestAssertion(t, command, private, "", 0x05, 2)
	if count, err := VerifyAssertion(command, credential, valid, "localhost", "http://localhost:8090"); err != nil || count != 2 {
		t.Fatalf("valid assertion count=%d err=%v", count, err)
	}
}

func TestCredentialFromCOSERejectsUnknownPrivateLabelsAndMalformedEncoding(t *testing.T) {
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	base := append([]byte{0xa4, 0x01, 0x01, 0x03, 0x27, 0x20, 0x06, 0x21, 0x58, 0x20}, public...)
	if _, err := CredentialFromCOSE("key", base, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	withPrivateLabel := append([]byte{0xa5, 0x01, 0x01, 0x03, 0x27, 0x20, 0x06, 0x21, 0x58, 0x20}, public...)
	withPrivateLabel = append(withPrivateLabel, 0x23, 0x01) // private d label
	if _, err := CredentialFromCOSE("key", withPrivateLabel, time.Now().UTC()); err == nil || !strings.Contains(err.Error(), "unsupported labels") {
		t.Fatalf("COSE private label error=%v", err)
	}
	if _, err := CredentialFromCOSE("key", append(base, 0x00), time.Now().UTC()); err == nil {
		t.Fatal("COSE trailing data accepted")
	}
}
