package ownercredential

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func testCredential(t *testing.T, id string) Credential {
	t.Helper()
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	publicKey = append([]byte(nil), publicKey...)
	return Credential{ID: id, Algorithm: EdDSA, PublicKey: publicKey, Fingerprint: Fingerprint(EdDSA, publicKey)}
}

func TestBootstrapRequiresTwoDistinctPublicCredentialsAndStoresNoSecrets(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	first, second := testCredential(t, "credential-primary"), testCredential(t, "credential-backup")
	if _, err := Bootstrap("owner-1", "bootstrap-1", []Credential{first}, now); err == nil {
		t.Fatal("bootstrap accepted only one credential")
	}
	state, err := Bootstrap("owner-1", "bootstrap-1", []Credential{first, second}, now)
	if err != nil {
		t.Fatal(err)
	}
	if state.CredentialRevision != 1 || state.Sequence != 1 || len(state.Credentials) != 2 || len(state.Events) != 1 || state.Events[0].Kind != Bootstrapped {
		t.Fatalf("unexpected bootstrap aggregate: %+v", state)
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(encoded)), "private_key") || strings.Contains(strings.ToLower(string(encoded)), "secret") {
		t.Fatalf("private material appears in canonical aggregate: %s", encoded)
	}
	if _, err := Bootstrap("owner-1", "bootstrap-2", []Credential{first, {ID: "same-key", Algorithm: first.Algorithm, PublicKey: first.PublicKey, Fingerprint: first.Fingerprint}}, now); err == nil {
		t.Fatal("bootstrap accepted duplicate public key under another ID")
	}
}

func TestCredentialPublicKeyValidationRejectsMalformedES256(t *testing.T) {
	credential := Credential{ID: "bad-p256", Algorithm: ES256, PublicKey: make([]byte, 64), RegisteredAt: time.Now().UTC()}
	credential.Fingerprint = Fingerprint(ES256, credential.PublicKey)
	if err := credential.Validate(); err == nil {
		t.Fatal("accepted a non-curve ES256 key")
	}
}
