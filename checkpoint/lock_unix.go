//go:build unix

package checkpoint

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// Lock prevents two coordinator processes from owning the same snapshot.
// The kernel releases it when the owning process exits; the lock file stays.
func (f File) Lock() (func() error, error) {
	if err := os.MkdirAll(filepath.Dir(f.Path), 0700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(f.Path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		return nil, fmt.Errorf("snapshot already owned: %w", err)
	}
	return func() error { return file.Close() }, nil
}
