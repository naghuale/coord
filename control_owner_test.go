package coord

import (
	"bytes"
	"coord/checkpoint"
	"coord/envelope"
	"coord/ownercredential"
	"coord/store"
	"coord/task"
	"coord/taskid"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestControlOwnerCommandEndpointPerformsSignedCanonicalTaskCreate(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key := append([]byte(nil), public...)
	credential := ownercredential.Credential{ID: "owner-key", Algorithm: ownercredential.EdDSA, PublicKey: key, Fingerprint: ownercredential.Fingerprint(ownercredential.EdDSA, key)}
	canonical, err := store.NewCanonical(checkpoint.CanonicalFile{Path: filepath.Join(t.TempDir(), "canonical.json")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := canonical.Close(); err != nil {
			t.Errorf("close canonical store: %v", err)
		}
	})
	if _, err := canonical.BootstrapOwnerCredentials("owner-1", "bootstrap-1", []ownercredential.Credential{credential, testBackupCredential(t)}); err != nil {
		t.Fatal(err)
	}
	if err := canonical.ConfigureOwnerWebAuthnPolicy("localhost", "http://localhost:8090"); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	c, err := New(&fakeTransport{in: make(chan envelope.Envelope, 1)}, canonical, cfg)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(ownercredential.CreateTaskPayload{Title: "from daemon", StagePlan: task.StagePlan{Stages: []task.Stage{task.Execution}, PolicyVersion: task.StagePlanPolicyVersion}})
	commandRequest := ownercredential.CommandRequest{Kind: ownercredential.CommandCreateTask, MessageID: "task-create-message", PrincipalID: "owner-1", CredentialID: credential.ID, ExpectedCredentialRevision: 1, Payload: payload}
	handler := c.ControlHandler("control-secret")
	challengeBody, _ := json.Marshal(commandRequest)
	challengeHTTP := httptest.NewRequest(http.MethodPost, "/owner/challenge", bytes.NewReader(challengeBody))
	challengeHTTP.Header.Set("Authorization", "Bearer control-secret")
	challengeResponse := httptest.NewRecorder()
	handler.ServeHTTP(challengeResponse, challengeHTTP)
	if challengeResponse.Code != http.StatusOK {
		t.Fatalf("challenge status=%d body=%s", challengeResponse.Code, challengeResponse.Body)
	}
	var command ownercredential.Command
	if err := json.Unmarshal(challengeResponse.Body.Bytes(), &command); err != nil {
		t.Fatal(err)
	}
	clientData, _ := json.Marshal(map[string]any{"type": "webauthn.get", "challenge": command.WebAuthnChallenge, "origin": "http://localhost:8090"})
	authData := make([]byte, 37)
	rpHash := sha256.Sum256([]byte("localhost"))
	copy(authData[:32], rpHash[:])
	authData[32] = 0x05
	binary.BigEndian.PutUint32(authData[33:], 1)
	clientHash := sha256.Sum256(clientData)
	signed := append(append([]byte(nil), authData...), clientHash[:]...)
	assertion := ownercredential.Assertion{ClientDataJSON: clientData, AuthenticatorData: authData, Signature: ed25519.Sign(private, signed)}
	commandBody, _ := json.Marshal(ownerCommandRequest{ChallengeID: command.ChallengeID, Command: command, Assertion: assertion})
	commandHTTP := httptest.NewRequest(http.MethodPost, "/owner/command", bytes.NewReader(commandBody))
	commandHTTP.Header.Set("Authorization", "Bearer control-secret")
	commandResponse := httptest.NewRecorder()
	handler.ServeHTTP(commandResponse, commandHTTP)
	if commandResponse.Code != http.StatusOK {
		t.Fatalf("command status=%d body=%s", commandResponse.Code, commandResponse.Body)
	}
	var result struct {
		Receipt ownercredential.Receipt `json:"receipt"`
		Replay  bool                    `json:"replayed"`
	}
	if err := json.Unmarshal(commandResponse.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Replay || result.Receipt.TaskID == "" {
		t.Fatalf("bad command result: %+v", result)
	}
	state, err := canonical.CanonicalSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if got := state.Tasks[taskIDFromOwnerReceipt(result.Receipt)]; got.OwnerID != "owner-1" || got.Title != "from daemon" {
		t.Fatalf("canonical Task not created from signed principal: %+v", got)
	}
	unauthorized := httptest.NewRequest(http.MethodPost, "/owner/challenge", bytes.NewReader(challengeBody))
	denied := httptest.NewRecorder()
	handler.ServeHTTP(denied, unauthorized)
	if denied.Code != http.StatusUnauthorized {
		t.Fatalf("missing channel admission code=%d", denied.Code)
	}
}

func testBackupCredential(t *testing.T) ownercredential.Credential {
	t.Helper()
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key := append([]byte(nil), public...)
	return ownercredential.Credential{ID: "owner-backup", Algorithm: ownercredential.EdDSA, PublicKey: key, Fingerprint: ownercredential.Fingerprint(ownercredential.EdDSA, key)}
}

func taskIDFromOwnerReceipt(receipt ownercredential.Receipt) taskid.ID {
	return taskid.ID(receipt.TaskID)
}
