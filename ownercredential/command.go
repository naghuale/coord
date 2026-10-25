package ownercredential

import (
	"bytes"
	"coord/task"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

const CommandDomain = "coord.owner-command.v1"
const ChallengeLifetime = 2 * time.Minute

const (
	CommandRegisterCredential = "owner.credential.register"
	CommandRevokeCredential   = "owner.credential.revoke"
	CommandCreateTask         = "task.create"
	CommandDelegationGrant    = "coordinator.delegation.grant"
	CommandDelegationRevoke   = "coordinator.delegation.revoke"
	CommandLossConfirm        = "coordinator.loss.confirm"
	CommandVisibilityConfirm  = "coordinator.visibility.confirm"
)

type RegisterCredentialPayload struct {
	CredentialID  string `json:"credential_id"`
	COSEPublicKey []byte `json:"cose_public_key"`
}

type RevokeCredentialPayload struct {
	CredentialID string `json:"credential_id"`
}

// CreateTaskPayload contains only Task parameters; OwnerID and TaskID are
// bound by the authenticated principal and generated inside the daemon commit.
type CreateTaskPayload struct {
	Title       string         `json:"title"`
	Description string         `json:"description,omitempty"`
	StagePlan   task.StagePlan `json:"stage_plan"`
}

// DelegationPrincipalPayload is the exact triple used by #93b OWNER commands.
type DelegationPrincipalPayload struct {
	StableAgentID string `json:"stableAgentId"`
	SessionID     string `json:"sessionId"`
	Generation    uint64 `json:"generation"`
}

// DelegationGrantPayload is the OWNER-signed grant body (ADR §3).
type DelegationGrantPayload struct {
	DelegationID         string                       `json:"delegationId"`
	Scope                DelegationScopePayload       `json:"scope"`
	PrimaryCoordinator   DelegationPrincipalPayload   `json:"primaryCoordinator"`
	Validity             DelegationValidityPayload    `json:"validity"`
	Successors           []DelegationPrincipalPayload `json:"successors"`
	PrimaryRestart       *PrimaryRestartPayload       `json:"primaryRestart,omitempty"`
	Evidence             DelegationEvidencePayload    `json:"evidence"`
	CutoverAssignmentIDs []string                     `json:"cutoverAssignmentIds,omitempty"`
}

type DelegationScopePayload struct {
	Repository       string   `json:"repository"`
	Project          string   `json:"project"`
	AllowedWorkItems []string `json:"allowedWorkItems"`
	AllowedActions   []string `json:"allowedActions"`
}

type DelegationValidityPayload struct {
	NotBefore time.Time `json:"notBefore"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type PrimaryRestartPayload struct {
	StableAgentID string `json:"stableAgentId"`
}

type DelegationEvidencePayload struct {
	OwnerDecisionRef   string `json:"ownerDecisionRef"`
	VisibilityEvidence string `json:"visibilityEvidence"`
}

// DelegationRevokePayload revokes one grant (OWNER only).
type DelegationRevokePayload struct {
	DelegationID string `json:"delegationId"`
	Reason       string `json:"reason"`
}

// LossConfirmPayload is OWNER path (b) loss recording (ADR §9).
type LossConfirmPayload struct {
	DelegationID string                     `json:"delegationId"`
	Coordinator  DelegationPrincipalPayload `json:"coordinator"`
	// AssignmentIDs is the exact sorted set of open assignments the OWNER is
	// authorizing this loss.confirm to affect. It is part of the signed payload.
	AssignmentIDs []string  `json:"assignmentIds"`
	Reason        string    `json:"reason"`
	ObservedAt    time.Time `json:"observedAt"`
	// Optional command claim. Compared to the named grant. Never copied onto
	// an assignment. Empty means "no claim".
	GrantID         string `json:"grantId,omitempty"`
	GrantGeneration uint64 `json:"grantGeneration,omitempty"`
	Repository      string `json:"repository,omitempty"`
	Project         string `json:"project,omitempty"`
}

// VisibilityConfirmPayload is the transitional OWNER visibility carrier.
// authorityCreating=false: proves OWNER saw the triple at issuedAt only.
type VisibilityConfirmPayload struct {
	DelegationID  string                      `json:"delegationId"`
	Successor     *DelegationPrincipalPayload `json:"successor,omitempty"`
	StableAgentID string                      `json:"stableAgentId,omitempty"` // primaryRestart
	IssuedAt      time.Time                   `json:"issuedAt"`
	ExpiresAt     time.Time                   `json:"expiresAt"`
	SingleUse     bool                        `json:"singleUse"`
}

// CommandRequest is the immutable command input used to mint one daemon
// challenge. Payload holds the exact, duplicate-key-free JSON bytes.
type CommandRequest struct {
	Kind                       string `json:"kind"`
	MessageID                  string `json:"message_id"`
	PrincipalID                string `json:"principal_id"`
	CredentialID               string `json:"credential_id"`
	ExpectedCredentialRevision uint64 `json:"expected_credential_revision"`
	Payload                    []byte `json:"payload"`
}

// Command is the complete signed statement. Its digest is domain-separated,
// includes exact payload bytes and is used as the WebAuthn challenge.
type Command struct {
	Domain                     string    `json:"domain"`
	ChallengeID                string    `json:"challenge_id"`
	Kind                       string    `json:"kind"`
	MessageID                  string    `json:"message_id"`
	PrincipalID                string    `json:"principal_id"`
	CredentialID               string    `json:"credential_id"`
	ExpectedCredentialRevision uint64    `json:"expected_credential_revision"`
	Nonce                      string    `json:"nonce"`
	ExpiresAt                  time.Time `json:"expires_at"`
	Payload                    []byte    `json:"payload"`
	Digest                     string    `json:"digest"`
	WebAuthnChallenge          string    `json:"webauthn_challenge"`
}

type commandDigestInput struct {
	Domain                     string    `json:"domain"`
	ChallengeID                string    `json:"challenge_id"`
	Kind                       string    `json:"kind"`
	MessageID                  string    `json:"message_id"`
	PrincipalID                string    `json:"principal_id"`
	CredentialID               string    `json:"credential_id"`
	ExpectedCredentialRevision uint64    `json:"expected_credential_revision"`
	Nonce                      string    `json:"nonce"`
	ExpiresAt                  time.Time `json:"expires_at"`
	Payload                    []byte    `json:"payload"`
}

func (c Command) DigestBytes() ([]byte, error) {
	if c.Domain != CommandDomain || c.ChallengeID == "" || c.Kind == "" || c.MessageID == "" || c.PrincipalID == "" || c.CredentialID == "" || c.ExpectedCredentialRevision == 0 || c.Nonce == "" || c.ExpiresAt.IsZero() || len(c.Payload) == 0 {
		return nil, errors.New("incomplete Owner command")
	}
	if err := ValidatePayload(c.Payload); err != nil {
		return nil, err
	}
	input := commandDigestInput{c.Domain, c.ChallengeID, c.Kind, c.MessageID, c.PrincipalID, c.CredentialID, c.ExpectedCredentialRevision, c.Nonce, c.ExpiresAt.UTC(), c.Payload}
	encoded, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	return append([]byte(CommandDomain+"\x00"), encoded...), nil
}

func (c Command) Validate() error {
	data, err := c.DigestBytes()
	if err != nil {
		return err
	}
	digest := sha256.Sum256(data)
	if c.Digest != hex.EncodeToString(digest[:]) || c.WebAuthnChallenge != base64.RawURLEncoding.EncodeToString(digest[:]) {
		return errors.New("owner command digest or WebAuthn challenge mismatch")
	}
	return nil
}

func (c Command) Same(other Command) bool {
	a, errA := c.DigestBytes()
	b, errB := other.DigestBytes()
	return errA == nil && errB == nil && bytes.Equal(a, b) && c.Digest == other.Digest && c.WebAuthnChallenge == other.WebAuthnChallenge
}

func NewChallenge(request CommandRequest, now time.Time, ttl time.Duration) (Command, error) {
	if !SupportedCommandKind(request.Kind) {
		return Command{}, fmt.Errorf("unsupported Owner command kind %q", request.Kind)
	}
	if strings.TrimSpace(request.MessageID) != request.MessageID || request.MessageID == "" || strings.TrimSpace(request.PrincipalID) != request.PrincipalID || request.PrincipalID == "" || strings.TrimSpace(request.CredentialID) != request.CredentialID || request.CredentialID == "" || request.ExpectedCredentialRevision == 0 || now.IsZero() || ttl <= 0 || ttl > ChallengeLifetime {
		return Command{}, errors.New("invalid Owner challenge request")
	}
	if err := ValidatePayload(request.Payload); err != nil {
		return Command{}, err
	}
	var random [48]byte
	if _, err := rand.Read(random[:]); err != nil {
		return Command{}, fmt.Errorf("generate Owner challenge: %w", err)
	}
	c := Command{
		Domain: CommandDomain, ChallengeID: base64.RawURLEncoding.EncodeToString(random[:16]),
		Kind: request.Kind, MessageID: request.MessageID, PrincipalID: request.PrincipalID,
		CredentialID: request.CredentialID, ExpectedCredentialRevision: request.ExpectedCredentialRevision,
		Nonce: base64.RawURLEncoding.EncodeToString(random[16:]), ExpiresAt: now.UTC().Add(ttl), Payload: append([]byte(nil), request.Payload...),
	}
	data, err := c.DigestBytes()
	if err != nil {
		return Command{}, err
	}
	digest := sha256.Sum256(data)
	c.Digest = hex.EncodeToString(digest[:])
	c.WebAuthnChallenge = base64.RawURLEncoding.EncodeToString(digest[:])
	return c, c.Validate()
}

type Assertion struct {
	ClientDataJSON    []byte `json:"client_data_json"`
	AuthenticatorData []byte `json:"authenticator_data"`
	Signature         []byte `json:"signature"`
}

type clientData struct {
	Type        string `json:"type"`
	Challenge   string `json:"challenge"`
	Origin      string `json:"origin"`
	CrossOrigin bool   `json:"crossOrigin,omitempty"`
	TopOrigin   string `json:"topOrigin,omitempty"`
}

// VerifyAssertion checks a WebAuthn assertion against the immutable credential
// and daemon-issued challenge. It intentionally accepts only the minimum
// assertion grammar needed by this localhost command boundary.
func VerifyAssertion(command Command, credential Credential, assertion Assertion, expectedRPID, expectedOrigin string) (uint32, error) {
	if err := command.Validate(); err != nil {
		return 0, err
	}
	if err := credential.Validate(); err != nil {
		return 0, err
	}
	if expectedRPID == "" || expectedOrigin == "" || len(assertion.ClientDataJSON) == 0 || len(assertion.ClientDataJSON) > 4096 || len(assertion.AuthenticatorData) < 37 || len(assertion.AuthenticatorData) > 1024 || len(assertion.Signature) == 0 || len(assertion.Signature) > 256 {
		return 0, errors.New("invalid WebAuthn assertion size or policy")
	}
	if err := validateJSONNoDuplicateKeys(assertion.ClientDataJSON); err != nil {
		return 0, fmt.Errorf("invalid clientDataJSON: %w", err)
	}
	var data clientData
	decoder := json.NewDecoder(bytes.NewReader(assertion.ClientDataJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&data); err != nil {
		return 0, fmt.Errorf("invalid clientDataJSON: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return 0, errors.New("trailing clientDataJSON")
	}
	if data.Type != "webauthn.get" || data.Challenge != command.WebAuthnChallenge || data.Origin != expectedOrigin || data.CrossOrigin || data.TopOrigin != "" {
		return 0, errors.New("WebAuthn clientData does not match challenge, type, or origin")
	}
	authData := assertion.AuthenticatorData
	rpHash := sha256.Sum256([]byte(expectedRPID))
	if !bytes.Equal(authData[:32], rpHash[:]) {
		return 0, errors.New("WebAuthn RP ID hash mismatch")
	}
	flags := authData[32]
	const up, uv, be, bs, at, ed = byte(0x01), byte(0x04), byte(0x08), byte(0x10), byte(0x40), byte(0x80)
	if flags&(up|uv) != up|uv || flags&(0x02|0x20|at|ed) != 0 || flags&bs != 0 && flags&be == 0 {
		return 0, errors.New("WebAuthn authenticator flags require UP+UV and forbid unsupported data")
	}
	count := uint32(authData[33])<<24 | uint32(authData[34])<<16 | uint32(authData[35])<<8 | uint32(authData[36])
	clientHash := sha256.Sum256(assertion.ClientDataJSON)
	signed := make([]byte, 0, len(authData)+len(clientHash))
	signed = append(signed, authData...)
	signed = append(signed, clientHash[:]...)
	valid := false
	switch credential.Algorithm {
	case ES256:
		if x, y, ok := p256Coordinates(credential.PublicKey); ok {
			valid = ecdsa.VerifyASN1(&ecdsa.PublicKey{Curve: elliptic.P256(), X: x, Y: y}, signedHash(signed), assertion.Signature)
		}
	case EdDSA:
		valid = ed25519.Verify(ed25519.PublicKey(credential.PublicKey), signed, assertion.Signature)
	}
	if !valid {
		return 0, errors.New("invalid WebAuthn assertion signature")
	}
	if credential.SignCount != 0 && (count == 0 || count <= credential.SignCount) {
		return 0, errors.New("WebAuthn signature counter did not increase")
	}
	return count, nil
}

func signedHash(data []byte) []byte {
	h := sha256.Sum256(data)
	return h[:]
}

// SupportedCommandKind reports whether kind is a known OwnerCredential command.
func SupportedCommandKind(kind string) bool {
	switch kind {
	case CommandRegisterCredential, CommandRevokeCredential, CommandCreateTask,
		CommandDelegationGrant, CommandDelegationRevoke, CommandLossConfirm, CommandVisibilityConfirm:
		return true
	default:
		return false
	}
}

// IsDelegationCommand reports #93b OWNER command kinds gated by the disable const.
func IsDelegationCommand(kind string) bool {
	switch kind {
	case CommandDelegationGrant, CommandDelegationRevoke, CommandLossConfirm, CommandVisibilityConfirm:
		return true
	default:
		return false
	}
}

func ValidatePayload(payload []byte) error {
	if len(payload) == 0 || len(payload) > 64*1024 || !json.Valid(payload) {
		return errors.New("owner command payload must be bounded valid JSON")
	}
	return validateJSONNoDuplicateKeys(payload)
}

func validateJSONNoDuplicateKeys(data []byte) error {
	d := json.NewDecoder(bytes.NewReader(data))
	var walk func() error
	walk = func() error {
		token, err := d.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				keyToken, err := d.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok || seen[key] {
					return errors.New("duplicate or invalid JSON object key")
				}
				seen[key] = true
				if err := walk(); err != nil {
					return err
				}
			}
		case '[':
			for d.More() {
				if err := walk(); err != nil {
					return err
				}
			}
		default:
			return errors.New("invalid JSON delimiter")
		}
		_, err = d.Token()
		return err
	}
	if err := walk(); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("trailing JSON value")
	}
	return nil
}
