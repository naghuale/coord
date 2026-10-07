// Package checkpoint provides atomic durable snapshots. One coordinator owns a file.
package checkpoint

import (
	"coord/fsm"
	"coord/internal/atomicfile"
	"encoding/json"
	"fmt"
	"os"
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
	if err = validateRuns(s.Runs); err != nil {
		return nil, err
	}
	return s.Runs, nil
}
func validateRuns(runs map[string]fsm.Run) error {
	if runs == nil {
		return fmt.Errorf("snapshot runs required")
	}
	for id, r := range runs {
		if id != r.RunID || r.Attempt == 0 || r.Fence == 0 || r.OwnerSession == "" {
			return fmt.Errorf("invalid snapshot run %s", id)
		}
	}
	return nil
}
func (f File) Save(runs map[string]fsm.Run) error {
	if err := validateRuns(runs); err != nil {
		return err
	}
	b, err := json.Marshal(Snapshot{1, runs})
	if err != nil {
		return err
	}
	return atomicfile.Write(f.Path, b)
}
