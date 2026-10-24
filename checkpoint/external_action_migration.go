package checkpoint

import (
	"bytes"
	"coord/internal/atomicfile"
	"coord/store"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"time"
)

var ErrUnsafeDowngrade = errors.New("unsafe canonical schema downgrade")

// CheckSchemaTarget is also used by Save: callers cannot persist an older
// canonical schema over a newer OwnerCredential-aware snapshot.
func CheckSchemaTarget(current, target uint32) error {
	if current >= 3 && target < current {
		return ErrUnsafeDowngrade
	}
	if target != 3 && target != 4 && target != 5 {
		return fmt.Errorf("unsupported target schema")
	}
	return nil
}

type actionMigrationManifest struct {
	MigrationID    string    `json:"migration_id"`
	SourcePath     string    `json:"source_path"`
	BackupPath     string    `json:"backup_path"`
	BackupSize     int       `json:"backup_size"`
	TargetSize     int       `json:"target_size"`
	CreatedAt      time.Time `json:"created_at"`
	BackupSynced   bool      `json:"backup_synced"`
	BackupVerified bool      `json:"backup_verified"`
	Version        uint32    `json:"version"`
	SourceDigest   string    `json:"source_digest"`
	TargetDigest   string    `json:"target_digest"`
	From           uint32    `json:"from"`
	To             uint32    `json:"to"`
}
type migrationWriter func(phase, path string, data []byte) error

func bytesDigest(data []byte) string { h := sha256.Sum256(data); return hex.EncodeToString(h[:]) }
func strictState(data []byte) (store.CanonicalState, error) {
	var s store.CanonicalState
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&s); err != nil {
		return s, err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return s, fmt.Errorf("trailing snapshot")
	}
	return s, nil
}
func readPrivate(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return nil, fmt.Errorf("unsafe migration artifact")
	}
	return os.ReadFile(path)
}
func syncMigration(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	if err = errors.Join(f.Sync(), f.Close()); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	return errors.Join(d.Sync(), d.Close())
}

// PrepareCanonicalOwned runs only under NewCanonical's persistence lock, which
// stays held through Load, validation and service ownership. Load itself is
// read-only, including when called by a projection CLI.
func (f CanonicalFile) PrepareCanonicalOwned() error {
	return f.prepareExternalActions(func(_ string, path string, data []byte) error { return atomicfile.Write(path, data) })
}

func (f CanonicalFile) prepareExternalActions(write migrationWriter) error {
	current, err := os.ReadFile(f.Path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	s, err := strictState(current)
	if err != nil {
		return err
	}
	if s.SchemaVersion != 2 && s.SchemaVersion != 3 {
		return nil
	} // existing legacy validation remains in Load
	if s.SchemaVersion == 2 {
		if _, err := readPrivate(f.Path); err != nil {
			return err
		}
	}
	dir := f.Path + ".schema2-to3"
	info, dirErr := os.Lstat(dir)
	if s.SchemaVersion == 3 && os.IsNotExist(dirErr) {
		return store.ValidateCanonicalState(s)
	}
	if dirErr != nil && !os.IsNotExist(dirErr) {
		return dirErr
	}
	if os.IsNotExist(dirErr) {
		if err := store.ValidateSchema2(s); err != nil {
			return err
		}
		if err := os.Mkdir(dir, 0700); err != nil {
			return err
		}
		info, err = os.Lstat(dir)
		if err != nil {
			return err
		}
	}
	if !info.IsDir() || info.Mode().Perm() != 0700 {
		return fmt.Errorf("unsafe migration directory")
	}
	if err := syncMigration(dir); err != nil {
		return err
	}
	resolved, err := filepath.EvalSymlinks(f.Path)
	if err != nil {
		return err
	}
	if !filepath.IsAbs(f.Path) || filepath.Clean(f.Path) != resolved {
		return fmt.Errorf("canonical migration path required")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Name() != "backup.json" && e.Name() != "manifest.json" && e.Name() != "receipt.json" {
			return fmt.Errorf("foreign migration artifact")
		}
		if _, err := readPrivate(filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	backupPath, manifestPath, receiptPath := filepath.Join(dir, "backup.json"), filepath.Join(dir, "manifest.json"), filepath.Join(dir, "receipt.json")
	backup, backupErr := readPrivate(backupPath)
	manifestData, manifestErr := readPrivate(manifestPath)
	if backupErr != nil && !os.IsNotExist(backupErr) {
		return backupErr
	}
	if manifestErr != nil && !os.IsNotExist(manifestErr) {
		return manifestErr
	}
	if os.IsNotExist(backupErr) {
		if s.SchemaVersion != 2 || !os.IsNotExist(manifestErr) {
			return fmt.Errorf("backup provenance missing")
		}
		if _, err := os.Lstat(receiptPath); !os.IsNotExist(err) {
			return fmt.Errorf("receipt without backup")
		}
		if err := store.ValidateSchema2(s); err != nil {
			return err
		}
		if err := write("backup", backupPath, current); err != nil {
			return err
		}
		backup, err = readPrivate(backupPath)
		if err != nil {
			return err
		}
		if !bytes.Equal(backup, current) {
			return fmt.Errorf("backup verification failed")
		}
	}
	old, err := strictState(backup)
	if err != nil {
		return err
	}
	sourceDigest := bytesDigest(backup)
	next, err := store.UpgradeExternalActions(old, sourceDigest)
	if err != nil {
		return err
	}
	target, err := json.Marshal(next)
	if err != nil {
		return err
	}
	created := time.Now().UTC()
	if manifestErr == nil {
		var prior actionMigrationManifest
		if err := json.Unmarshal(manifestData, &prior); err != nil {
			return err
		}
		if prior.CreatedAt.IsZero() {
			return fmt.Errorf("migration creation time missing")
		}
		created = prior.CreatedAt
	}
	expected := actionMigrationManifest{MigrationID: "external-action-schema-2-to-3", SourcePath: resolved, BackupPath: backupPath, BackupSize: len(backup), TargetSize: len(target), CreatedAt: created, BackupSynced: true, BackupVerified: true, Version: 1, SourceDigest: sourceDigest, TargetDigest: bytesDigest(target), From: 2, To: 3}
	if err := syncMigration(backupPath); err != nil {
		return err
	}
	if os.IsNotExist(manifestErr) {
		if !bytes.Equal(current, backup) {
			return fmt.Errorf("source changed before manifest")
		}
		if _, err := os.Lstat(receiptPath); !os.IsNotExist(err) {
			return fmt.Errorf("receipt before manifest")
		}
		data, err := json.Marshal(expected)
		if err != nil {
			return err
		}
		if err := write("manifest", manifestPath, data); err != nil {
			return err
		}
		manifestData, err = readPrivate(manifestPath)
		if err != nil {
			return err
		}
	}
	var manifest actionMigrationManifest
	d := json.NewDecoder(bytes.NewReader(manifestData))
	d.DisallowUnknownFields()
	if err := d.Decode(&manifest); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("trailing migration manifest")
	}
	if manifest != expected {
		return fmt.Errorf("migration manifest mismatch")
	}
	if err := syncMigration(manifestPath); err != nil {
		return err
	}
	if receipt, err := readPrivate(receiptPath); err == nil {
		if string(receipt) != expected.TargetDigest {
			return fmt.Errorf("migration receipt mismatch")
		}
		if !reflect.DeepEqual(s.SchemaMigrations, next.SchemaMigrations) {
			return fmt.Errorf("migration event provenance mismatch")
		}
		// A completed migration may be followed by ordinary canonical commits.
		return store.ValidateCanonicalState(s)
	} else if !os.IsNotExist(err) {
		return err
	}
	latest, err := os.ReadFile(f.Path)
	if err != nil {
		return err
	}
	switch bytesDigest(latest) {
	case expected.SourceDigest:
		if err := write("snapshot", f.Path, target); err != nil {
			return fmt.Errorf("migration publication uncertain: %w", err)
		}
	case expected.TargetDigest:
	default:
		return fmt.Errorf("migration source/target conflict")
	}
	installed, err := os.ReadFile(f.Path)
	if err != nil {
		return err
	}
	if bytesDigest(installed) != expected.TargetDigest {
		return fmt.Errorf("partial migration publication")
	}
	published, err := strictState(installed)
	if err != nil {
		return err
	}
	if err := store.ValidateCanonicalState(published); err != nil {
		return err
	}
	if err := syncMigration(f.Path); err != nil {
		return err
	}
	return write("receipt", receiptPath, []byte(expected.TargetDigest))
}
