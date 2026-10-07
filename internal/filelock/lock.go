// Package filelock holds exclusive process ownership of a snapshot.
package filelock

import "errors"

// ErrLocked reports that another process owns the same snapshot.
var ErrLocked = errors.New("snapshot already owned")

// ErrUnavailable refuses an unsupported platform rather than silently dropping ownership.
var ErrUnavailable = errors.New("exclusive snapshot locking requires Unix")
