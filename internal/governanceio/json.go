package governanceio

import (
	"encoding/json"
	"fmt"
	"io"
)

func DecodeStrict[T any](r io.Reader) (T, error) {
	var out T
	d := json.NewDecoder(r)
	d.DisallowUnknownFields()
	if err := d.Decode(&out); err != nil {
		return out, err
	}
	if d.More() {
		return out, fmt.Errorf("multiple JSON values")
	}
	return out, nil
}
func Encode(w io.Writer, v any) error {
	e := json.NewEncoder(w)
	e.SetIndent("", "  ")
	return e.Encode(v)
}
