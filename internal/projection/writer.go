// Package projection publishes a rebuildable, revision-consistent operations
// bundle. It owns no canonical workflow state.
package projection

import (
	"coord/internal/atomicfile"
	"coord/internal/externalaction"
	"coord/store"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
)

type Manifest struct {
	Revision  uint64            `json:"sourceRevision"`
	Generator string            `json:"generatorVersion"`
	Files     map[string]string `json:"files"`
}
type Writer struct {
	Root string
	mu   sync.Mutex
}

func (w *Writer) Publish(s store.CanonicalState) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !filepath.IsAbs(w.Root) {
		return fmt.Errorf("projection root must be absolute")
	}
	// Refuse symlinks at every existing component before creating/writing files.
	path := w.Root
	for {
		info, err := os.Lstat(path)
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("projection symlink path rejected")
		}
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		parent := filepath.Dir(path)
		if parent == path {
			break
		}
		path = parent
	}
	if err := os.MkdirAll(w.Root, 0700); err != nil {
		return err
	}
	currentPath := filepath.Join(w.Root, "current.json")
	var current Manifest
	if b, err := os.ReadFile(currentPath); err == nil {
		if err = json.Unmarshal(b, &current); err != nil {
			return fmt.Errorf("invalid projection pointer: %w", err)
		}
		if current.Revision > s.Revision {
			return nil
		}
		if current.Revision == s.Revision && w.validBundle(current) {
			return nil
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	dir := filepath.Join(w.Root, "revisions", strconv.FormatUint(s.Revision, 10))
	for _, p := range []string{filepath.Join(w.Root, "revisions"), dir} {
		if info, err := os.Lstat(p); err == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("projection bundle symlink rejected")
		}
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	m := Manifest{s.Revision, "coord-orchestration-v1", map[string]string{}}
	// JSON is valid YAML; all three documents bind to exactly the same snapshot.
	data := map[string]any{
		"CURRENT.yaml": struct {
			SourceRevision uint64 `json:"sourceRevision"`
			Tasks          any    `json:"tasks"`
			Sessions       any    `json:"sessions"`
		}{s.Revision, s.Tasks, s.AgentSessions},
		"OPEN-WORK.yaml": struct {
			SourceRevision uint64 `json:"sourceRevision"`
			Actions        any    `json:"actions"`
		}{s.Revision, s.Orchestration.Actions},
		"LEDGER.yaml": struct {
			SourceRevision uint64 `json:"sourceRevision"`
			Events         any    `json:"events"`
		}{s.Revision, s.Orchestration.Events},
	}
	for name, value := range data {
		b, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			return err
		}
		m.Files[name] = externalaction.Digest(json.RawMessage(b))
		if err = atomicfile.Write(filepath.Join(dir, name), b); err != nil {
			return err
		}
	}
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if err = atomicfile.Write(filepath.Join(dir, "manifest.json"), b); err != nil {
		return err
	}
	// current.json is the sole publication point. No acknowledgement is written
	// to canonical state, avoiding a projection->revision->projection feedback loop.
	return atomicfile.Write(currentPath, b)
}
func (w *Writer) validBundle(m Manifest) bool {
	if m.Generator != "coord-orchestration-v1" || len(m.Files) != 3 {
		return false
	}
	dir := filepath.Join(w.Root, "revisions", strconv.FormatUint(m.Revision, 10))
	for _, name := range []string{"CURRENT.yaml", "OPEN-WORK.yaml", "LEDGER.yaml"} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || externalaction.Digest(json.RawMessage(b)) != m.Files[name] {
			return false
		}
		var binding struct {
			SourceRevision uint64 `json:"sourceRevision"`
		}
		if json.Unmarshal(b, &binding) != nil || binding.SourceRevision != m.Revision {
			return false
		}
	}
	return true
}
