package architecturecheck

import (
	"io"

	"gopkg.in/yaml.v3"
)

func newYAMLEncoder(w io.Writer) *yaml.Encoder {
	return yaml.NewEncoder(w)
}
