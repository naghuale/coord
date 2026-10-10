//go:build linux && cgo && seccomp

package sandbox

import (
	"fmt"
	seccomp "github.com/seccomp/libseccomp-golang"
)

type LibseccompLoader struct{}

func (LibseccompLoader) Load(p SeccompProfile) error {
	f, e := seccomp.NewFilter(seccomp.ActErrno.SetReturnCode(int16(p.DefaultErrno)))
	if e != nil {
		return e
	}
	for _, n := range p.Allow {
		s, e := seccomp.GetSyscallFromName(n)
		if e != nil {
			return fmt.Errorf("%s: %w", n, e)
		}
		if e = f.AddRule(s, seccomp.ActAllow); e != nil {
			return e
		}
	}
	return f.Load()
}
