// Package atomicfile commits snapshots before callers publish their state.
package atomicfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Write commits private data by fsync, rename and directory fsync.
// Cleanup and close failures are returned so callers can refuse further writes
// when the durable outcome is uncertain.
func Write(path string, data []byte) (err error) {
	dir := filepath.Dir(path)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create snapshot directory: %w", err)
	}
	file, err := os.CreateTemp(dir, ".coord-*")
	if err != nil {
		return fmt.Errorf("create temporary snapshot: %w", err)
	}
	name := file.Name()
	defer func() {
		if removeErr := os.Remove(name); removeErr != nil && !os.IsNotExist(removeErr) {
			err = errors.Join(err, fmt.Errorf("remove temporary snapshot: %w", removeErr))
		}
	}()
	closed := false
	defer func() {
		if !closed {
			if closeErr := file.Close(); closeErr != nil {
				err = errors.Join(err, fmt.Errorf("close temporary snapshot: %w", closeErr))
			}
		}
	}()
	if _, err = file.Write(data); err != nil {
		return fmt.Errorf("write snapshot: %w", err)
	}
	if err = file.Sync(); err != nil {
		return fmt.Errorf("sync snapshot: %w", err)
	}
	closed = true
	if err = file.Close(); err != nil {
		return fmt.Errorf("close snapshot: %w", err)
	}
	if err = os.Rename(name, path); err != nil {
		return fmt.Errorf("rename snapshot: %w", err)
	}
	directory, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open snapshot directory: %w", err)
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil {
		err = fmt.Errorf("sync snapshot directory: %w", syncErr)
	}
	if closeErr != nil {
		err = errors.Join(err, fmt.Errorf("close snapshot directory: %w", closeErr))
	}
	return err
}
