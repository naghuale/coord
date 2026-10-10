//go:build !linux || !cgo || !seccomp

package sandbox

import "errors"

type LibseccompLoader struct{}

func (LibseccompLoader) Load(SeccompProfile) error { return errors.New("seccomp unavailable") }
