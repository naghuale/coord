package governanceio

import (
	"io"

	"gopkg.in/yaml.v3"
)

// DecodeStrictYAML decodes r into T rejecting unknown fields.
func DecodeStrictYAML[T any](r io.Reader) (T, error) {
	var out T
	d := yaml.NewDecoder(r)
	d.KnownFields(true)
	if err := d.Decode(&out); err != nil {
		return out, err
	}
	return out, nil
}
