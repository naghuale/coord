package checkpoint

import (
	"bytes"
	"coord/internal/atomicfile"
	"coord/store"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestExternalActionMigrationPublicationRecovery(t *testing.T) {
	// Distinct durable failure classes: no publication before backup+manifest;
	// uncertain rename and post-publication receipt loss must reconcile on reopen.
	for _, point := range []string{"backup", "manifest", "snapshot-before", "snapshot-after", "receipt"} {
		t.Run(point, func(t *testing.T) {
			file := migrationTestFile(t)
			source := store.EmptyCanonicalState()
			source.SchemaVersion = 2
			raw, err := json.Marshal(source)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file.Path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			release, err := file.Lock()
			if err != nil {
				t.Fatal(err)
			}
			injected := errors.New("injected publication failure")
			err = file.prepareExternalActions(func(phase, path string, data []byte) error {
				if phase == point || (phase == "snapshot" && point == "snapshot-before") {
					return injected
				}
				if phase == "snapshot" {
					backup, err := readPrivate(file.Path + ".schema2-to3/backup.json")
					if err != nil || !bytes.Equal(backup, raw) {
						t.Fatal("effect before verified backup")
					}
					if _, err := readPrivate(file.Path + ".schema2-to3/manifest.json"); err != nil {
						t.Fatal("effect before durable manifest")
					}
				}
				if err := atomicfile.Write(path, data); err != nil {
					return err
				}
				if phase == "snapshot" && point == "snapshot-after" {
					return injected
				}
				return nil
			})
			if closeErr := release(); closeErr != nil {
				t.Fatal(closeErr)
			}
			if !errors.Is(err, injected) {
				t.Fatalf("uncertain migration incorrectly succeeded: %v", err)
			}
			current, err := os.ReadFile(file.Path)
			if err != nil {
				t.Fatal(err)
			}
			if point == "backup" || point == "manifest" || point == "snapshot-before" {
				if !bytes.Equal(raw, current) {
					t.Fatal("snapshot changed before publication")
				}
			}
			s, err := store.NewCanonical(file)
			if err != nil {
				t.Fatal(err)
			}
			state, err := s.CanonicalSnapshot()
			if err != nil {
				t.Fatal(err)
			}
			if state.SchemaVersion != 3 || len(state.SchemaMigrations) != 1 {
				t.Fatal("restart did not converge")
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = store.NewCanonical(file)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestExternalActionMigrationRejectsForeignArtifacts(t *testing.T) {
	file := migrationTestFile(t)
	source := store.EmptyCanonicalState()
	source.SchemaVersion = 2
	raw, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file.Path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	dir := file.Path + ".schema2-to3"
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "foreign"), []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if s, err := store.NewCanonical(file); err == nil {
		_ = s.Close()
		t.Fatal("foreign migration directory accepted")
	}
	after, err := os.ReadFile(file.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, after) {
		t.Fatal("foreign collision overwrote source")
	}
}

// t.TempDir may retain the macOS /var alias; production migration requires a
// canonical path. Resolve the existing directory before constructing the file.
func migrationTestFile(t *testing.T) CanonicalFile {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return CanonicalFile{Path: filepath.Join(dir, "canonical.json")}
}
