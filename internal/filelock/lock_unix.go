//go:build unix

package filelock

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// Lock holds a kernel flock until release or process exit. The lock file remains.
func Lock(path string) (func() error, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("create lock directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open snapshot lock: %w", err)
	}
	if err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("%w: %w", ErrLocked, err)
	}
	return file.Close, nil
}
