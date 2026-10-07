//go:build !linux && !darwin && !dragonfly && !freebsd && !netbsd && !openbsd && !illumos

package filelock

// Lock refuses platforms where this implementation cannot guarantee ownership.
func Lock(string) (func() error, error) { return nil, ErrUnavailable }
