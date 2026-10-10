//go:build !linux

package sandbox

import "errors"

type Bubblewrap struct{ Path string }

func (Bubblewrap) Validate(Profile) error { return errors.New("unsupported") }
func (Bubblewrap) Command(Profile, Launch) ([]string, []string, error) {
	return nil, nil, errors.New("unsupported")
}
