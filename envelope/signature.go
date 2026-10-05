package envelope

import (
	"crypto/ed25519"
	"encoding/json"
	"fmt"
)

// Signed preserves the exact bytes covered by the signature.
type Signed struct {
	Message   json.RawMessage `json:"message"`
	Signature []byte          `json:"signature"`
}

func Sign(e Envelope, key ed25519.PrivateKey) ([]byte, error) {
	if len(key) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("invalid private key")
	}
	if err := e.Validate(); err != nil {
		return nil, err
	}
	b, err := json.Marshal(e)
	if err != nil {
		return nil, err
	}
	return json.Marshal(Signed{b, ed25519.Sign(key, b)})
}

// Verify pins session identity to a configured public key. It authenticates the
// claimed SHA, but proving the actual filesystem contents requires attestation.
func Verify(raw []byte, keys map[string]ed25519.PublicKey) (Envelope, error) {
	var frame Signed
	if err := json.Unmarshal(raw, &frame); err != nil {
		return Envelope{}, err
	}
	e, err := Decode(frame.Message)
	if err != nil {
		return e, err
	}
	key := keys[e.From]
	if len(key) != ed25519.PublicKeySize || !ed25519.Verify(key, frame.Message, frame.Signature) {
		return e, fmt.Errorf("untrusted signature")
	}
	return e, nil
}
