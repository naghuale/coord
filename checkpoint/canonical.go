package checkpoint

import (
	"bytes"
	"coord/internal/atomicfile"
	"coord/internal/filelock"
	"coord/store"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// CanonicalFile stores the complete versioned Coord state in one atomic JSON
// document. Legacy Run snapshots are decoded only by File and are never
// written through this persistence.
type CanonicalFile struct {
	Path string
}

func (f CanonicalFile) Lock() (func() error, error) {
	return filelock.Lock(f.Path + ".lock")
}

func (f CanonicalFile) Load() (store.CanonicalState, error) {
	data, err := os.ReadFile(f.Path)
	if os.IsNotExist(err) {
		return store.EmptyCanonicalState(), nil
	}
	if err != nil {
		return store.CanonicalState{}, err
	}
	return DecodeCanonicalSnapshot(data)
}

// DecodeCanonicalSnapshot validates one atomic snapshot without acquiring
// ownership, migration, creating an empty namespace, or writing any files.
func DecodeCanonicalSnapshot(data []byte) (store.CanonicalState, error) {
	if err := rejectDuplicateCanonicalFields(data); err != nil {
		return store.CanonicalState{}, err
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return store.CanonicalState{}, fmt.Errorf("decode canonical snapshot envelope: %w", err)
	}
	if _, legacy := fields["version"]; legacy {
		if _, canonical := fields["schema_version"]; !canonical {
			return store.CanonicalState{}, fmt.Errorf("legacy Run snapshot is not a canonical state document")
		}
	}
	if _, ok := fields["schema_version"]; !ok {
		return store.CanonicalState{}, fmt.Errorf("canonical schema version is missing")
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var state store.CanonicalState
	if err := decoder.Decode(&state); err != nil {
		return store.CanonicalState{}, fmt.Errorf("decode canonical snapshot: %w", err)
	}
	state = store.NormalizeCanonicalState(state)
	if err := decoder.Decode(new(any)); err != io.EOF {
		if err == nil {
			return store.CanonicalState{}, fmt.Errorf("multiple JSON values in canonical snapshot")
		}
		return store.CanonicalState{}, fmt.Errorf("trailing canonical snapshot data: %w", err)
	}
	if state.SchemaVersion == 2 {
		if err := store.ValidateSchema2(state); err != nil {
			return store.CanonicalState{}, err
		}
	} else if err := store.ValidateCanonicalState(state); err != nil {
		return store.CanonicalState{}, err
	}
	return state, nil
}

// A duplicate field must not hide ledger authority (e.g. a second null ledger)
// or replace an identity/state while the decoder silently keeps the last value.
func rejectDuplicateCanonicalFields(data []byte) error {
	d := json.NewDecoder(bytes.NewReader(data))
	var value func() error
	value = func() error {
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
				key, err := d.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok {
					return fmt.Errorf("invalid canonical JSON key")
				}
				if seen[name] {
					return fmt.Errorf("duplicate canonical JSON field %q", name)
				}
				seen[name] = true
				if err := value(); err != nil {
					return err
				}
			}
		case '[':
			for d.More() {
				if err := value(); err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("invalid canonical JSON delimiter")
		}
		_, err = d.Token()
		return err
	}
	if err := value(); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return fmt.Errorf("trailing canonical JSON")
	}
	return nil
}

func (f CanonicalFile) Save(state store.CanonicalState) error {
	if data, err := os.ReadFile(f.Path); err == nil {
		var previous struct {
			Version uint32 `json:"schema_version"`
		}
		if err := json.Unmarshal(data, &previous); err != nil {
			return err
		}
		if err := CheckSchemaTarget(previous.Version, state.SchemaVersion); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := store.ValidateCanonicalState(state); err != nil {
		return err
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return atomicfile.Write(f.Path, data)
}
