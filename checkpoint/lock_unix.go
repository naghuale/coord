//go:build unix

package checkpoint

import "coord/internal/filelock"

// Lock prevents two coordinator processes from owning the same snapshot.
// The kernel releases it when the owning process exits; the lock file stays.
func (f File) Lock() (func() error, error) { return filelock.Lock(f.Path + ".lock") }
