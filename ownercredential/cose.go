package ownercredential

import (
	"errors"
	"fmt"
	"time"
)

// CredentialFromCOSE accepts only the two WebAuthn public-key forms backed by
// Go's standard library: ES256/P-256 and EdDSA/Ed25519.
func CredentialFromCOSE(id string, encoded []byte, registeredAt time.Time) (Credential, error) {
	fields, err := parseCOSEMap(encoded)
	if err != nil {
		return Credential{}, err
	}
	kty, err := coseInt(fields, 1)
	if err != nil {
		return Credential{}, err
	}
	alg, err := coseInt(fields, 3)
	if err != nil {
		return Credential{}, err
	}
	curve, err := coseInt(fields, -1)
	if err != nil {
		return Credential{}, err
	}
	if kty == 2 && alg == -7 && curve == 1 {
		if len(fields) != 5 {
			return Credential{}, errors.New("COSE ES256 key contains unsupported labels")
		}
		for label := range fields {
			if label != 1 && label != 3 && label != -1 && label != -2 && label != -3 {
				return Credential{}, errors.New("COSE ES256 key contains unsupported labels")
			}
		}
	} else if kty == 1 && alg == -8 && curve == 6 {
		if len(fields) != 4 {
			return Credential{}, errors.New("COSE EdDSA key contains unsupported labels")
		}
		for label := range fields {
			if label != 1 && label != 3 && label != -1 && label != -2 {
				return Credential{}, errors.New("COSE EdDSA key contains unsupported labels")
			}
		}
	}
	x, err := coseBytes(fields, -2)
	if err != nil {
		return Credential{}, err
	}
	var algorithm Algorithm
	var publicKey []byte
	switch {
	case kty == 2 && alg == -7 && curve == 1:
		y, err := coseBytes(fields, -3)
		if err != nil || len(x) != 32 || len(y) != 32 {
			return Credential{}, errors.New("invalid COSE ES256 coordinates")
		}
		publicKey = append(append([]byte(nil), x...), y...)
		algorithm = ES256
	case kty == 1 && alg == -8 && curve == 6:
		if len(x) != 32 {
			return Credential{}, errors.New("invalid COSE Ed25519 public key")
		}
		publicKey = append([]byte(nil), x...)
		algorithm = EdDSA
	default:
		return Credential{}, fmt.Errorf("unsupported COSE key type/algorithm/curve %d/%d/%d", kty, alg, curve)
	}
	credential := Credential{ID: id, Algorithm: algorithm, PublicKey: publicKey, Fingerprint: Fingerprint(algorithm, publicKey), RegisteredAt: registeredAt}
	return credential, credential.Validate()
}

func parseCOSEMap(data []byte) (map[int64]any, error) {
	if len(data) == 0 || len(data) > 4096 {
		return nil, errors.New("invalid COSE key size")
	}
	c := cborCursor{data: data}
	major, length, err := c.readHead()
	if err != nil || major != 5 || length < 4 || length > 16 {
		return nil, errors.New("COSE key must be a bounded CBOR map")
	}
	fields := make(map[int64]any, length)
	for i := uint64(0); i < length; i++ {
		keyMajor, key, err := c.readInt()
		if err != nil || (keyMajor != 0 && keyMajor != 1) {
			return nil, errors.New("COSE labels must be integer keys")
		}
		if _, exists := fields[key]; exists {
			return nil, errors.New("duplicate COSE key label")
		}
		value, err := c.readValue()
		if err != nil {
			return nil, err
		}
		fields[key] = value
	}
	if c.pos != len(data) {
		return nil, errors.New("trailing CBOR data after COSE key")
	}
	return fields, nil
}

type cborCursor struct {
	data []byte
	pos  int
}

func (c *cborCursor) readHead() (byte, uint64, error) {
	if c.pos >= len(c.data) {
		return 0, 0, errors.New("truncated CBOR")
	}
	b := c.data[c.pos]
	c.pos++
	major, additional := b>>5, b&31
	switch {
	case additional < 24:
		return major, uint64(additional), nil
	case additional == 24:
		if c.pos+1 > len(c.data) {
			return 0, 0, errors.New("truncated CBOR uint8")
		}
		v := uint64(c.data[c.pos])
		c.pos++
		if v < 24 {
			return 0, 0, errors.New("non-canonical CBOR integer")
		}
		return major, v, nil
	case additional == 25:
		if c.pos+2 > len(c.data) {
			return 0, 0, errors.New("truncated CBOR uint16")
		}
		v := uint64(c.data[c.pos])<<8 | uint64(c.data[c.pos+1])
		c.pos += 2
		if v <= 0xff {
			return 0, 0, errors.New("non-canonical CBOR integer")
		}
		return major, v, nil
	case additional == 26:
		if c.pos+4 > len(c.data) {
			return 0, 0, errors.New("truncated CBOR uint32")
		}
		v := uint64(c.data[c.pos])<<24 | uint64(c.data[c.pos+1])<<16 | uint64(c.data[c.pos+2])<<8 | uint64(c.data[c.pos+3])
		c.pos += 4
		if v <= 0xffff {
			return 0, 0, errors.New("non-canonical CBOR integer")
		}
		return major, v, nil
	default:
		return 0, 0, errors.New("indefinite or oversized CBOR value is forbidden")
	}
}

func (c *cborCursor) readInt() (byte, int64, error) {
	major, n, err := c.readHead()
	if err != nil {
		return 0, 0, err
	}
	if major == 0 {
		return major, int64(n), nil
	}
	if major == 1 {
		return major, -1 - int64(n), nil
	}
	return major, 0, errors.New("COSE label is not an integer")
}

func (c *cborCursor) readValue() (any, error) {
	major, n, err := c.readHead()
	if err != nil {
		return nil, err
	}
	switch major {
	case 0:
		return int64(n), nil
	case 1:
		return -1 - int64(n), nil
	case 2:
		if n > uint64(len(c.data)-c.pos) {
			return nil, errors.New("truncated COSE byte string")
		}
		value := append([]byte(nil), c.data[c.pos:c.pos+int(n)]...)
		c.pos += int(n)
		return value, nil
	default:
		return nil, errors.New("COSE values must be bounded integers or byte strings")
	}
}

func coseInt(fields map[int64]any, key int64) (int64, error) {
	value, ok := fields[key]
	if !ok {
		return 0, fmt.Errorf("missing COSE integer label %d", key)
	}
	n, ok := value.(int64)
	if !ok {
		return 0, fmt.Errorf("COSE label %d is not an integer", key)
	}
	return n, nil
}

func coseBytes(fields map[int64]any, key int64) ([]byte, error) {
	value, ok := fields[key]
	if !ok {
		return nil, fmt.Errorf("missing COSE byte-string label %d", key)
	}
	data, ok := value.([]byte)
	if !ok {
		return nil, fmt.Errorf("COSE label %d is not a byte string", key)
	}
	return data, nil
}
