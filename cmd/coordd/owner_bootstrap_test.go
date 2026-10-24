package main

import (
	"bytes"
	"coord/checkpoint"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOwnerBootstrapRequiresConfirmationAndStoresOnlyPublicCredentials(t *testing.T) {
	stateDir := t.TempDir()
	credentialPath := filepath.Join(t.TempDir(), "public-credentials.json")
	credentials := make([]bootstrapCredentialInput, 0, 2)
	for _, id := range []string{"key-primary", "key-backup"} {
		public, _, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		cose := append([]byte{0xa4, 0x01, 0x01, 0x03, 0x27, 0x20, 0x06, 0x21, 0x58, 0x20}, public...)
		credentials = append(credentials, bootstrapCredentialInput{ID: id, COSEPublicKey: cose})
	}
	encoded, err := json.Marshal(credentials)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(credentialPath, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	var out, diagnostic bytes.Buffer
	args := []string{"bootstrap", "--state-dir", stateDir, "--principal", "owner-1", "--credentials", credentialPath}
	if err := runOwnerCommand(args, strings.NewReader("BOOTSTRAP owner-1\n"), &out, &diagnostic); err != nil {
		t.Fatal(err)
	}
	state, err := (checkpoint.CanonicalFile{Path: filepath.Join(stateDir, "canonical.json")}).Load()
	if err != nil {
		t.Fatal(err)
	}
	aggregate := state.OwnerCredentials["owner-1"]
	if state.SchemaVersion != 4 || len(aggregate.Credentials) != 2 || len(aggregate.Receipts) != 1 || !strings.Contains(out.String(), "sha256:") {
		t.Fatalf("bootstrap result state=%+v output=%s", state, out.String())
	}
	if err := runOwnerCommand(args, strings.NewReader("BOOTSTRAP owner-1\n"), &out, &diagnostic); err == nil {
		t.Fatal("second bootstrap reopened initialized state")
	}
}

func TestOwnerBootstrapRejectsMissingHumanConfirmationWithoutCreatingState(t *testing.T) {
	credentialPath := filepath.Join(t.TempDir(), "credentials.json")
	if err := os.WriteFile(credentialPath, []byte(`[]`), 0600); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(t.TempDir(), "state")
	var out, diagnostic bytes.Buffer
	err := runOwnerCommand([]string{"bootstrap", "--state-dir", stateDir, "--principal", "owner-1", "--credentials", credentialPath}, strings.NewReader("no\n"), &out, &diagnostic)
	if err == nil {
		t.Fatal("bootstrap accepted absent key confirmations")
	}
	if _, err := os.Stat(filepath.Join(stateDir, "canonical.json")); !os.IsNotExist(err) {
		t.Fatalf("unconfirmed bootstrap created canonical state: %v", err)
	}
}
