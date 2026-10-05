// Package checkpoint provides atomic durable snapshots. One coordinator owns a file.
package checkpoint

import (
	"coord/fsm"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type Snapshot struct {
	Version int                `json:"version"`
	Runs    map[string]fsm.Run `json:"runs"`
}
type File struct{ Path string }

func (f File) Load() (map[string]fsm.Run, error) {
	b, err := os.ReadFile(f.Path)
	if os.IsNotExist(err) {
		return map[string]fsm.Run{}, nil
	}
	if err != nil {
		return nil, err
	}
	var s Snapshot
	if err = json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	if s.Version != 1 || s.Runs == nil {
		return nil, fmt.Errorf("unsupported snapshot")
	}
	for id, r := range s.Runs {
		if id != r.RunID || r.Attempt == 0 || r.Fence == 0 || r.OwnerSession == "" {
			return nil, fmt.Errorf("invalid snapshot run %s", id)
		}
	}
	return s.Runs, nil
}
func (f File) Save(runs map[string]fsm.Run) error {
	b, err := json.Marshal(Snapshot{1, runs})
	if err != nil {
		return err
	}
	dir := filepath.Dir(f.Path)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".coord-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err = tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Rename(name, f.Path); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
