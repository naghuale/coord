package baselinestore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"coord/internal/baseline"
)

var ErrNotFound = errors.New("baseline not found")

// Store persists baseline runs and candidate records on the filesystem.
// Runs are append-only; candidates are atomically replaced by identity.
type Store struct {
	Root string
}

func identityKey(id baseline.Identity) string {
	raw := id.AdapterID + "_" + id.SuiteDigest
	var b strings.Builder
	for _, c := range raw {
		if isASCIIAlphaNumeric(c) {
			b.WriteRune(c)
		} else {
			b.WriteRune('_')
		}
	}
	return b.String()
}

func isASCIIAlphaNumeric(c rune) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

type runRecord struct {
	Run      baseline.Run `json:"run"`
	StoredAt string       `json:"storedAt"`
}

// StoreRun appends a run record. Reusing a digest is rejected to preserve
// append-only semantics.
func (s *Store) StoreRun(ctx context.Context, run baseline.Run) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.Root == "" {
		return errors.New("baseline store root must not be empty")
	}
	if run.Digest == "" {
		return fmt.Errorf("run Digest must not be empty")
	}
	dir := filepath.Join(s.Root, "runs", identityKey(run.Identity))
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	path := filepath.Join(dir, sanitizeFilename(run.Digest)+".json")
	record := runRecord{Run: run, StoredAt: time.Now().UTC().Format(time.RFC3339)}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("create run record: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return err
	}
	return syncDirectory(dir)
}

// ListRuns returns all stored runs for an identity, ordered by filename.
func (s *Store) ListRuns(ctx context.Context, id baseline.Identity) ([]baseline.Run, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dir := filepath.Join(s.Root, "runs", identityKey(id))
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var runs []baseline.Run
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		var record runRecord
		if err := json.Unmarshal(data, &record); err != nil {
			return nil, fmt.Errorf("parse %s: %w", entry.Name(), err)
		}
		runs = append(runs, record.Run)
	}
	return runs, nil
}

// StoreCandidate atomically writes the candidate for its identity.
func (s *Store) StoreCandidate(ctx context.Context, candidate baseline.Candidate) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.Root == "" {
		return errors.New("baseline store root must not be empty")
	}
	dir := filepath.Join(s.Root, "candidates")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	path := filepath.Join(dir, identityKey(candidate.Identity)+".json")
	data, err := json.MarshalIndent(candidate, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".candidate-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	return syncDirectory(dir)
}

// LoadCandidate reads the candidate stored for an identity.
func (s *Store) LoadCandidate(ctx context.Context, id baseline.Identity) (baseline.Candidate, error) {
	if err := ctx.Err(); err != nil {
		return baseline.Candidate{}, err
	}
	path := filepath.Join(s.Root, "candidates", identityKey(id)+".json")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return baseline.Candidate{}, ErrNotFound
	}
	if err != nil {
		return baseline.Candidate{}, err
	}
	var candidate baseline.Candidate
	if err := json.Unmarshal(data, &candidate); err != nil {
		return baseline.Candidate{}, err
	}
	return candidate, nil
}

func sanitizeFilename(value string) string {
	var b strings.Builder
	for _, c := range value {
		if isASCIIAlphaNumeric(c) || c == '-' {
			b.WriteRune(c)
		} else {
			b.WriteRune('_')
		}
	}
	return b.String()
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return dir.Sync()
}
