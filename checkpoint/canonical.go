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
	if err := decoder.Decode(new(any)); err != io.EOF {
		if err == nil {
			return store.CanonicalState{}, fmt.Errorf("multiple JSON values in canonical snapshot")
		}
		return store.CanonicalState{}, fmt.Errorf("trailing canonical snapshot data: %w", err)
	}
	if err := store.ValidateCanonicalState(state); err != nil {
		return store.CanonicalState{}, err
	}
	return state, nil
}

func (f CanonicalFile) Save(state store.CanonicalState) error {
	if err := store.ValidateCanonicalState(state); err != nil {
		return err
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return atomicfile.Write(f.Path, data)
}
