package checkpoint

import "coord/internal/filelock"

// Lock prevents two coordinator processes from owning the same snapshot.
// filelock provides the platform-specific implementation and fails closed
// where process ownership cannot be guaranteed.
func (f File) Lock() (func() error, error) { return filelock.Lock(f.Path + ".lock") }
